// Package portal is the deploy portal's logic: apps requested from a GitHub repo, approved by a
// super admin, built by GitHub Actions and deployed to Nomad. The server package does the pages and
// decides who may call what; this package does the work and keeps core's registry in step.
//
// The flow for one app:
//
//	Request (owner)  -> pending, reads app.toml from the repo
//	Approve (super)  -> live in the registry; the newest build deploys
//	Sync (every few minutes, or on GitHub's webhook) -> new builds are fetched, checked, stored,
//	                    and the newest deploys by itself
package portal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arnav127/iima-platform/core/github"
	"github.com/arnav127/iima-platform/core/jobgen"
	"github.com/arnav127/iima-platform/core/nomad"
	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/store"
)

// UserError is a problem the person can fix; its text is shown to them as is.
type UserError string

func (e UserError) Error() string { return string(e) }

type Config struct {
	Store   *store.Store
	GitHub  *github.App   // nil: nobody can connect repos (the files in apps/ still work)
	Nomad   *nomad.Client // nil: approvals work but nothing deploys
	Cluster jobgen.Cluster
	// ArtifactDir holds downloaded builds; ArtifactURL is where Nomad clients fetch them from core,
	// e.g. http://10.0.0.11:24555/platform/artifacts.
	ArtifactDir string
	ArtifactURL string
	FileApps    []*registry.App // apps from apps/*.toml, set up by hand
	Keep        int             // builds kept on disk per app (default 5)
	MaxArtifact int64           // bytes (default 512 MB)
	Log         *slog.Logger
}

type Portal struct {
	cfg  Config
	reg  atomic.Pointer[registry.Registry]
	mu   sync.Mutex // one sync or deploy at a time
	kick chan struct{}
}

func New(ctx context.Context, cfg Config) (*Portal, error) {
	if cfg.Keep == 0 {
		cfg.Keep = 5
	}
	if cfg.MaxArtifact == 0 {
		cfg.MaxArtifact = 512 << 20
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.ArtifactDir != "" {
		if err := os.MkdirAll(cfg.ArtifactDir, 0o700); err != nil {
			return nil, err
		}
	}
	p := &Portal{cfg: cfg, kick: make(chan struct{}, 1)}
	if err := p.rebuild(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// Registry is every live app: the files in apps/ plus approved portal apps.
func (p *Portal) Registry() *registry.Registry { return p.reg.Load() }

// Connectable reports whether people can connect repos (a GitHub App is configured).
func (p *Portal) Connectable() bool { return p.cfg.GitHub != nil }

// InstallURL is the GitHub page where an owner installs the platform's App on a repo.
func (p *Portal) InstallURL() string {
	if p.cfg.GitHub == nil {
		return ""
	}
	return p.cfg.GitHub.InstallURL("portal")
}

func (p *Portal) rebuild(ctx context.Context) error {
	apps := slices.Clone(p.cfg.FileApps)
	all, err := p.cfg.Store.PortalApps(ctx)
	if err != nil {
		return err
	}
	for _, pa := range all {
		if pa.Status != store.AppApproved {
			continue
		}
		a, err := decode(pa.Approved)
		if err != nil {
			p.cfg.Log.Error("portal: bad stored settings", "app", pa.ID, "err", err)
			continue
		}
		a.File = "portal"
		apps = append(apps, a)
	}
	reg, err := registry.New(apps...)
	if err != nil {
		return err
	}
	p.reg.Store(reg)
	return nil
}

// ---- requesting and approving ----

var repoRe = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// NormRepo accepts "owner/name" or a GitHub URL.
func NormRepo(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	if !repoRe.MatchString(s) {
		return "", UserError("Give the repository as owner/name, for example iima-clubs/quiz.")
	}
	return s, nil
}

// Request records a new app from repo's app.toml. The person must be one of its owners there,
// which shows they can write to the repo. It stays offline until a super admin approves it.
func (p *Portal) Request(ctx context.Context, actor, repoIn string) (*registry.App, error) {
	gh := p.cfg.GitHub
	if gh == nil {
		return nil, UserError("Connecting repos is not set up on this platform yet.")
	}
	repo, err := NormRepo(repoIn)
	if err != nil {
		return nil, err
	}
	inst, err := gh.RepoInstallation(ctx, repo)
	if errors.Is(err, github.ErrNotFound) {
		return nil, UserError("The platform's GitHub App is not installed on " + repo + ". Install it on that repo first.")
	} else if err != nil {
		return nil, err
	}
	branch := "main"
	if repos, err := gh.Repos(ctx, inst); err == nil {
		for _, r := range repos {
			if strings.EqualFold(r.FullName, repo) && r.DefaultBranch != "" {
				repo, branch = r.FullName, r.DefaultBranch
			}
		}
	}
	b, err := gh.File(ctx, inst, repo, "app.toml", branch)
	if errors.Is(err, github.ErrNotFound) {
		return nil, UserError(repo + " has no app.toml on " + branch + ". Copy the one from the app template and fill it in.")
	} else if err != nil {
		return nil, err
	}
	a, err := registry.Parse(b)
	if err != nil {
		return nil, UserError("app.toml in " + repo + ": " + err.Error())
	}
	me := store.NormEmail(actor)
	if !slices.ContainsFunc(a.Owners, func(o string) bool { return store.NormEmail(o) == me }) {
		return nil, UserError("Add " + me + " to owners in " + repo + "'s app.toml, so the platform knows you may deploy it.")
	}
	defaults(a)
	if err := p.checkFree(ctx, a, ""); err != nil {
		return nil, err
	}
	err = p.cfg.Store.CreatePortalApp(ctx, store.PortalApp{ID: a.ID, Pending: encode(a), Repo: repo, InstallationID: inst, Branch: branch}, me)
	if errors.Is(err, store.ErrExists) {
		return nil, UserError("An app with id " + a.ID + " or from " + repo + " already exists.")
	}
	if err != nil {
		return nil, err
	}
	// Fetch its builds now, so the approver sees there is something to deploy.
	go p.Kick()
	return a, nil
}

// Approve makes an app's pending settings live and deploys its newest build.
func (p *Portal) Approve(ctx context.Context, actor, id string) error {
	pa, err := p.cfg.Store.PortalApp(ctx, id)
	if err != nil {
		return err
	}
	if pa.Pending == "" {
		return UserError("Nothing is waiting for approval.")
	}
	a, err := decode(pa.Pending)
	if err != nil {
		return err
	}
	if err := p.checkFree(ctx, a, id); err != nil {
		return err
	}
	if err := p.cfg.Store.ApprovePortalApp(ctx, actor, id); err != nil {
		return err
	}
	if err := p.rebuild(ctx); err != nil {
		return err
	}
	if pa.Status == store.AppApproved {
		// A routing change on a live app: redeploy so the gateway's routes follow.
		return p.Redeploy(ctx, actor, id)
	}
	go p.Kick()
	return nil
}

// Reject drops the pending settings (and archives an app that was never approved).
func (p *Portal) Reject(ctx context.Context, actor, id string) error {
	return p.cfg.Store.RejectPending(ctx, actor, id)
}

// checkFree makes sure a's id and path are not used by any other app, live or pending.
func (p *Portal) checkFree(ctx context.Context, a *registry.App, self string) error {
	for _, f := range p.cfg.FileApps {
		if f.ID == a.ID || f.Path == a.Path {
			return UserError(fmt.Sprintf("The id %s or path %s is already used by %s.", a.ID, a.Path, f.Name))
		}
	}
	all, err := p.cfg.Store.PortalApps(ctx)
	if err != nil {
		return err
	}
	for _, pa := range all {
		if pa.ID == self || pa.Status == store.AppArchived {
			continue
		}
		for _, js := range []string{pa.Approved, pa.Pending} {
			if o, err := decode(js); err == nil && (o.ID == a.ID || o.Path == a.Path) {
				return UserError(fmt.Sprintf("The id %s or path %s is already used by %s.", a.ID, a.Path, o.Name))
			}
		}
	}
	return nil
}

// ---- settings ----

// Settings returns the live settings (or the requested ones before approval) and pending ones.
func (p *Portal) Settings(pa *store.PortalApp) (live, pending *registry.App) {
	live, _ = decode(pa.Approved)
	pending, _ = decode(pa.Pending)
	return live, pending
}

// UpdateSettings saves next. Changes to how the gateway routes and signs people in (path, login,
// auth, public paths, shape, volume) wait for a super admin; the rest applies now.
func (p *Portal) UpdateSettings(ctx context.Context, actor, id string, next *registry.App, super bool) (waiting bool, err error) {
	pa, err := p.cfg.Store.PortalApp(ctx, id)
	if err != nil {
		return false, err
	}
	next.ID = id // the id is the job name and token audience: fixed for good
	if next.Volume == "" {
		// The form has no volume field: keep the one the app has (renaming it would lose its data).
		if cur, pend := p.Settings(pa); cur != nil {
			next.Volume = cur.Volume
		} else if pend != nil {
			next.Volume = pend.Volume
		}
	}
	defaults(next)
	if err := next.Normalize(); err != nil {
		return false, UserError(err.Error())
	}
	if err := p.checkFree(ctx, next, id); err != nil {
		return false, err
	}
	live, pending := p.Settings(pa)
	approved, pend := pa.Approved, ""

	switch {
	case live == nil: // not approved yet: the request itself changes
		pend = encode(next)
	case super || sameRouting(live, next):
		approved = encode(next)
		if pending != nil && !sameRouting(next, pending) {
			merged := *pending
			copyNonRouting(&merged, next)
			pend = encode(&merged)
		}
	default:
		merged := *live
		copyNonRouting(&merged, next)
		approved, pend = encode(&merged), encode(next)
	}
	if err := p.cfg.Store.SetPortalSettings(ctx, actor, id, approved, pend, diff(live, next)); err != nil {
		return false, err
	}
	if err := p.rebuild(ctx); err != nil {
		return false, err
	}
	if live != nil && approved != pa.Approved {
		if now, _ := decode(approved); now != nil && (now.Memory != live.Memory || !sameRouting(now, live)) {
			if err := p.Redeploy(ctx, actor, id); err != nil {
				return false, err
			}
		}
	}
	return pend != "" && live != nil, nil
}

func sameRouting(a, b *registry.App) bool {
	return a.Path == b.Path && a.Login == b.Login && a.Auth == b.Auth && a.Shape == b.Shape &&
		a.Volume == b.Volume && a.PocketBase == b.PocketBase && a.Strips() == b.Strips() && slices.Equal(a.Public, b.Public)
}

func copyNonRouting(dst, src *registry.App) {
	dst.Name, dst.Roles, dst.ManagerRole, dst.Memory, dst.Realtime, dst.Review = src.Name, src.Roles, src.ManagerRole, src.Memory, src.Realtime, src.Review
}

func defaults(a *registry.App) {
	a.Owners = nil // owners live in the portal's table, editable there
	if (a.Shape == "go-pocketbase" || a.PocketBase) && a.Volume == "" {
		a.Volume = a.ID + "-data"
	}
}

func diff(old, next *registry.App) map[string]any {
	if old == nil {
		return map[string]any{"path": next.Path}
	}
	d := map[string]any{}
	if old.Path != next.Path {
		d["path"] = next.Path
	}
	if old.Login != next.Login {
		d["login"] = next.Login
	}
	if old.Auth != next.Auth {
		d["auth"] = next.Auth
	}
	if !slices.Equal(old.Roles, next.Roles) {
		d["roles"] = next.Roles
	}
	if !slices.Equal(old.Public, next.Public) {
		d["public"] = next.Public
	}
	if old.Memory != next.Memory {
		d["memory"] = next.Memory
	}
	return d
}

func encode(a *registry.App) string {
	b, _ := json.Marshal(a)
	return string(b)
}

func decode(s string) (*registry.App, error) {
	if s == "" {
		return nil, errors.New("portal: no settings")
	}
	var a registry.App
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return nil, err
	}
	return &a, a.Normalize()
}

// ---- env vars ----

var envKeyRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

// Set by the platform in every job; an app cannot override them.
var reservedEnv = []string{"IIMA_JWKS_URL", "IIMA_ISSUER", "APP_URL", "PORT", "ADDR", "HOME", "PATH"}

// SetEnv saves a plain value in core (it goes into the job), or a secret straight into the app's
// Nomad Variable (core keeps only its name). A secret change restarts the app by itself (the job's
// template watches the variable); a plain change redeploys it.
func (p *Portal) SetEnv(ctx context.Context, actor, id, key, value string, secret bool) error {
	key = strings.TrimSpace(key)
	if !envKeyRe.MatchString(key) {
		return UserError("Names are capital letters, digits and _ (like DATABASE_URL).")
	}
	if slices.Contains(reservedEnv, key) || strings.HasPrefix(key, "NOMAD_") || strings.HasPrefix(key, "IIMA_DEV") {
		return UserError(key + " is set by the platform.")
	}
	if len(value) > 32<<10 {
		return UserError("Values are limited to 32 KB.")
	}
	was, err := p.envVar(ctx, id, key)
	if err != nil {
		return err
	}
	if secret || (was != nil && was.Secret) {
		if p.cfg.Nomad == nil {
			return UserError("Secrets need Nomad, which is not configured.")
		}
		var v *string
		if secret {
			v = &value
		}
		if err := p.cfg.Nomad.SetSecret(ctx, nomad.VariablePath(id), key, v); err != nil {
			return err
		}
	}
	if err := p.cfg.Store.SetEnv(ctx, actor, id, store.EnvVar{Key: key, Value: value, Secret: secret}); err != nil {
		return err
	}
	if !secret {
		return p.Redeploy(ctx, actor, id)
	}
	return nil
}

func (p *Portal) DeleteEnv(ctx context.Context, actor, id, key string) error {
	was, err := p.envVar(ctx, id, key)
	if err != nil || was == nil {
		return err
	}
	if was.Secret && p.cfg.Nomad != nil {
		if err := p.cfg.Nomad.SetSecret(ctx, nomad.VariablePath(id), key, nil); err != nil {
			return err
		}
	}
	if err := p.cfg.Store.DeleteEnv(ctx, actor, id, key); err != nil {
		return err
	}
	if !was.Secret {
		return p.Redeploy(ctx, actor, id)
	}
	return nil
}

func (p *Portal) envVar(ctx context.Context, id, key string) (*store.EnvVar, error) {
	all, err := p.cfg.Store.Env(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Key == key {
			return &all[i], nil
		}
	}
	return nil, nil
}

// ---- deploys ----

// Deploy runs the release tag of an approved app on Nomad.
func (p *Portal) Deploy(ctx context.Context, actor, id, tag string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deployLocked(ctx, actor, id, tag)
}

// Redeploy runs the app's current release again (after a settings or env change). An app that has
// never been deployed is left alone.
func (p *Portal) Redeploy(ctx context.Context, actor, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, err := p.cfg.Store.Deploys(ctx, id, 20)
	if err != nil {
		return err
	}
	for _, x := range d {
		if x.Status == "submitted" {
			return p.deployLocked(ctx, actor, id, x.Tag)
		}
	}
	return nil
}

func (p *Portal) deployLocked(ctx context.Context, actor, id, tag string) error {
	if p.cfg.Nomad == nil {
		return UserError("Deploying needs Nomad, which is not configured.")
	}
	pa, err := p.cfg.Store.PortalApp(ctx, id)
	if err != nil {
		return err
	}
	app, _ := p.Settings(pa)
	if pa.Status != store.AppApproved || app == nil {
		return UserError("A super admin has to approve this app before it can deploy.")
	}
	rel, err := p.release(ctx, id, tag)
	if err != nil {
		return err
	}
	if app.Owners, err = p.cfg.Store.Owners(ctx, id); err != nil {
		return err
	}
	env := map[string]string{}
	vars, err := p.cfg.Store.Env(ctx, id)
	if err != nil {
		return err
	}
	for _, v := range vars {
		if !v.Secret {
			env[v.Key] = v.Value
		}
	}
	fail := func(err error) error {
		_ = p.cfg.Store.AddDeploy(ctx, store.Deploy{AppID: id, Tag: tag, Actor: actor, Status: "failed", Message: err.Error()})
		return err
	}
	if app.Volume != "" && p.cfg.Cluster.Driver == "exec" {
		if err := p.cfg.Nomad.EnsureHostVolume(ctx, app.Volume); err != nil {
			return fail(err)
		}
	}
	job, err := jobgen.Build(p.cfg.Cluster, app, jobgen.Release{
		Version: tag, ArtifactURL: strings.TrimRight(p.cfg.ArtifactURL, "/") + "/" + rel.SHA256 + ".tar.gz", SHA256: rel.SHA256, Env: env,
	})
	if err != nil {
		return fail(err)
	}
	if _, err := p.cfg.Nomad.SubmitJob(ctx, job); err != nil {
		return fail(err)
	}
	p.cfg.Log.Info("portal: deployed", "app", id, "release", tag, "by", actor)
	return p.cfg.Store.AddDeploy(ctx, store.Deploy{AppID: id, Tag: tag, Actor: actor, Status: "submitted"})
}

func (p *Portal) release(ctx context.Context, id, tag string) (*store.Release, error) {
	rs, err := p.cfg.Store.Releases(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		if rs[i].Tag == tag {
			if rs[i].File == "" {
				return nil, UserError("That build is no longer kept; push again to rebuild it.")
			}
			return &rs[i], nil
		}
	}
	return nil, UserError("No build " + tag + ".")
}

// Status asks Nomad how the app is doing (nil when it has never run or Nomad is off).
func (p *Portal) Status(ctx context.Context, id string) *nomad.Status {
	if p.cfg.Nomad == nil {
		return nil
	}
	s, err := p.cfg.Nomad.Status(ctx, id)
	if err != nil {
		return nil
	}
	return s
}

// Logs returns the end of the app's output.
func (p *Portal) Logs(ctx context.Context, id string) (string, string, error) {
	if p.cfg.Nomad == nil {
		return "", "", UserError("Nomad is not configured.")
	}
	return p.cfg.Nomad.Logs(ctx, id, 32<<10)
}

// ArtifactFile is the stored build with this SHA-256, for Nomad to download.
func (p *Portal) ArtifactFile(ctx context.Context, sha string) (string, error) {
	return p.cfg.Store.ReleaseFile(ctx, sha)
}

// ---- syncing builds ----

// Kick asks the background loop to sync now (after a webhook or a request).
func (p *Portal) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Run syncs every interval, and whenever kicked, until ctx ends.
func (p *Portal) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		p.Sync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.kick:
		}
	}
}

// Sync fetches new builds for every app and deploys the newest one of each approved app with
// auto-deploy on, unless that build was already tried.
func (p *Portal) Sync(ctx context.Context) {
	if p.cfg.GitHub == nil {
		return
	}
	all, err := p.cfg.Store.PortalApps(ctx)
	if err != nil {
		p.cfg.Log.Error("portal: sync", "err", err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pa := range all {
		if pa.Status == store.AppArchived {
			continue
		}
		newest, err := p.fetch(ctx, pa)
		if err != nil {
			p.cfg.Log.Warn("portal: fetching builds", "app", pa.ID, "err", err)
			continue
		}
		if newest == "" || pa.Status != store.AppApproved || !pa.AutoDeploy {
			continue
		}
		last, err := p.cfg.Store.Deploys(ctx, pa.ID, 1)
		if err != nil || (len(last) > 0 && last[0].Tag == newest) {
			continue
		}
		if err := p.deployLocked(ctx, "auto", pa.ID, newest); err != nil {
			p.cfg.Log.Warn("portal: auto deploy", "app", pa.ID, "release", newest, "err", err)
		}
	}
}

// fetch downloads builds the portal has not seen and returns the newest build's tag.
func (p *Portal) fetch(ctx context.Context, pa *store.PortalApp) (string, error) {
	rs, err := p.cfg.GitHub.Releases(ctx, pa.InstallationID, pa.Repo)
	if err != nil {
		return "", err
	}
	known, err := p.cfg.Store.Releases(ctx, pa.ID)
	if err != nil {
		return "", err
	}
	have := map[string]bool{}
	for _, r := range known {
		have[r.Tag] = true
	}
	newest := ""
	tried := 0
	for _, r := range rs { // newest first
		if !strings.HasPrefix(r.Tag, "build-") || r.Asset("app.tar.gz") == nil {
			continue
		}
		if newest == "" {
			newest = r.Tag
		}
		if have[r.Tag] {
			continue
		}
		if tried++; tried > 3 { // an old backlog is not worth downloading
			break
		}
		if err := p.download(ctx, pa, r); err != nil {
			if r.Tag == newest {
				return "", err
			}
			p.cfg.Log.Warn("portal: build", "app", pa.ID, "release", r.Tag, "err", err)
		}
	}
	p.prune(ctx, pa.ID)
	return newest, nil
}

// download fetches a build's app.tar.gz, checks its SHA-256 against what the release says, and
// stores it as <sha256>.tar.gz.
func (p *Portal) download(ctx context.Context, pa *store.PortalApp, r github.Release) error {
	asset := r.Asset("app.tar.gz")
	want := strings.TrimPrefix(asset.Digest, "sha256:")
	if s := r.Asset("app.tar.gz.sha256"); s != nil {
		var b strings.Builder
		if _, err := p.cfg.GitHub.Download(ctx, pa.InstallationID, pa.Repo, s.ID, &b, 1024); err != nil {
			return err
		}
		fromFile, _, _ := strings.Cut(strings.TrimSpace(b.String()), " ")
		if want != "" && fromFile != want {
			return fmt.Errorf("release %s: checksum file and GitHub disagree", r.Tag)
		}
		want = fromFile
	}
	if len(want) != 64 {
		return fmt.Errorf("release %s: no SHA-256 for app.tar.gz", r.Tag)
	}

	tmp, err := os.CreateTemp(p.cfg.ArtifactDir, "download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := p.cfg.GitHub.Download(ctx, pa.InstallationID, pa.Repo, asset.ID, io.MultiWriter(tmp, h), p.cfg.MaxArtifact)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("release %s: app.tar.gz has SHA-256 %s, release says %s", r.Tag, got, want)
	}
	file := filepath.Join(p.cfg.ArtifactDir, got+".tar.gz")
	if err := os.Rename(tmp.Name(), file); err != nil {
		return err
	}
	commit := r.Target
	if len(commit) != 40 {
		commit = strings.TrimPrefix(r.Tag, "build-")
	}
	return p.cfg.Store.AddRelease(ctx, store.Release{AppID: pa.ID, Tag: r.Tag, Commit: commit, SHA256: got, Size: n, File: file, CreatedAt: time.Now()})
}

// prune deletes all but the newest Keep builds of an app, never the one running.
func (p *Portal) prune(ctx context.Context, id string) {
	rs, err := p.cfg.Store.Releases(ctx, id)
	if err != nil {
		return
	}
	running := ""
	if d, err := p.cfg.Store.Deploys(ctx, id, 20); err == nil {
		for _, x := range d {
			if x.Status == "submitted" {
				running = x.Tag
				break
			}
		}
	}
	kept := 0
	for _, r := range rs {
		if r.File == "" {
			continue
		}
		if kept < p.cfg.Keep || r.Tag == running {
			kept++
			continue
		}
		if err := os.Remove(r.File); err == nil || errors.Is(err, os.ErrNotExist) {
			_ = p.cfg.Store.ForgetReleaseFile(ctx, id, r.Tag)
		}
	}
}

// Webhook checks a GitHub webhook's signature and, if it is genuine, looks for new builds now.
func (p *Portal) Webhook(body []byte, signature string) bool {
	if p.cfg.GitHub == nil || !p.cfg.GitHub.VerifyWebhook(body, signature) {
		return false
	}
	p.Kick()
	return true
}
