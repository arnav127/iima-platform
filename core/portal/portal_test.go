package portal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arnav127/iima-platform/core/github/githubtest"
	"github.com/arnav127/iima-platform/core/jobgen"
	"github.com/arnav127/iima-platform/core/nomad"
	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/store"
)

const quizToml = `id = "quiz"
name = "Quiz Club"
path = "/quiz"
owners = ["P25Owner@iima.ac.in"]
shape = "go-pocketbase"
roles = ["admin"]
`

// fakeNomad records submitted jobs and keeps variables in memory.
type fakeNomad struct {
	mu   sync.Mutex
	jobs []jobgen.Job
	vars map[string]map[string]string
}

func (f *fakeNomad) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
		var body struct{ Job jobgen.Job }
		json.NewDecoder(r.Body).Decode(&body)
		f.jobs = append(f.jobs, body.Job)
		w.Write([]byte(`{"EvalID":"e"}`))
	case strings.HasPrefix(r.URL.Path, "/v1/var/"):
		path := strings.TrimPrefix(r.URL.Path, "/v1/var/")
		switch r.Method {
		case http.MethodGet:
			if f.vars[path] == nil {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Items": f.vars[path], "ModifyIndex": 1})
		case http.MethodPut:
			var body struct{ Items map[string]string }
			json.NewDecoder(r.Body).Decode(&body)
			f.vars[path] = body.Items
		case http.MethodDelete:
			delete(f.vars, path)
		}
	case r.URL.Path == "/v1/volumes":
		w.Write([]byte(`[]`))
	default:
		w.Write([]byte(`{}`))
	}
}

func (f *fakeNomad) last() jobgen.Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobs[len(f.jobs)-1]
}

func (f *fakeNomad) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.jobs)
}

type env struct {
	p  *Portal
	gh *githubtest.Server
	nm *fakeNomad
	st *store.Store
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	gh := githubtest.New()
	t.Cleanup(gh.Close)
	nm := &fakeNomad{vars: map[string]map[string]string{}}
	ns := httptest.NewServer(http.HandlerFunc(nm.serve))
	t.Cleanup(ns.Close)

	demo, _ := registry.Parse([]byte(`id = "demo"` + "\n" + `name = "Demo"` + "\n" + `path = "/demo"` + "\n" + `shape = "go"`))
	p, err := New(context.Background(), Config{
		Store: st, GitHub: gh.App("whsec"), Nomad: &nomad.Client{Addr: ns.URL},
		Cluster:     jobgen.Cluster{Datacenter: "dc1", Driver: "exec", PublicURL: "https://students.iima.ac.in"},
		ArtifactDir: filepath.Join(dir, "artifacts"), ArtifactURL: "http://10.0.0.11:4180/platform/artifacts",
		FileApps: []*registry.App{demo},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &env{p, gh, nm, st}
}

func isUserError(err error) bool {
	var ue UserError
	return errors.As(err, &ue)
}

func TestRequestChecks(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	if _, err := e.p.Request(ctx, "p25owner@iima.ac.in", "club/quiz"); !isUserError(err) || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("not installed: %v", err)
	}
	e.gh.AddRepo("club/empty", 1, map[string][]byte{})
	if _, err := e.p.Request(ctx, "p25owner@iima.ac.in", "club/empty"); !isUserError(err) || !strings.Contains(err.Error(), "no app.toml") {
		t.Fatalf("no app.toml: %v", err)
	}
	e.gh.AddRepo("club/quiz", 2, map[string][]byte{"app.toml": []byte(quizToml)})
	if _, err := e.p.Request(ctx, "p25someone@iima.ac.in", "https://github.com/club/quiz.git"); !isUserError(err) || !strings.Contains(err.Error(), "owners") {
		t.Fatalf("not an owner: %v", err)
	}
	e.gh.AddRepo("club/clash", 3, map[string][]byte{"app.toml": []byte(strings.Replace(quizToml, `path = "/quiz"`, `path = "/demo"`, 1))})
	if _, err := e.p.Request(ctx, "p25owner@iima.ac.in", "club/clash"); !isUserError(err) || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("path of a file app: %v", err)
	}
	if _, err := e.p.Request(ctx, "p25owner@iima.ac.in", "club/../evil"); !isUserError(err) {
		t.Fatalf("bad repo name: %v", err)
	}

	a, err := e.p.Request(ctx, "p25owner@iima.ac.in", "club/quiz")
	if err != nil || a.Volume != "quiz-data" {
		t.Fatalf("request: %v %+v", err, a)
	}
	if e.p.Registry().ByID("quiz") != nil {
		t.Fatal("a pending app must not be live")
	}
	if ok, _ := e.st.IsOwner(ctx, "quiz", "p25owner@iima.ac.in"); !ok {
		t.Fatal("requester should own the app")
	}
	// The same repo or id cannot be requested twice.
	if _, err := e.p.Request(ctx, "p25owner@iima.ac.in", "club/quiz"); !isUserError(err) {
		t.Fatalf("second request: %v", err)
	}
}

func TestApproveSyncDeploy(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.gh.AddRepo("club/quiz", 2, map[string][]byte{"app.toml": []byte(quizToml)})
	if _, err := e.p.Request(ctx, "p25owner@iima.ac.in", "club/quiz"); err != nil {
		t.Fatal(err)
	}
	sha := e.gh.AddRelease("club/quiz", "build-aaaaaaa", strings.Repeat("a", 40), []byte("build one"))

	// Before approval: the build is fetched, nothing deploys.
	e.p.Sync(ctx)
	if e.nm.count() != 0 {
		t.Fatal("deployed before approval")
	}
	if f, err := e.p.ArtifactFile(ctx, sha); err != nil {
		t.Fatalf("artifact not stored: %v", err)
	} else if b, _ := os.ReadFile(f); string(b) != "build one" {
		t.Fatalf("artifact content %q", b)
	}

	if err := e.p.Approve(ctx, "super@iima.ac.in", "quiz"); err != nil {
		t.Fatal(err)
	}
	if e.p.Registry().ByID("quiz") == nil {
		t.Fatal("approved app should be live")
	}
	e.p.Sync(ctx)
	if e.nm.count() != 1 {
		t.Fatalf("jobs after approval: %d", e.nm.count())
	}
	j := e.nm.last()
	art := j.TaskGroups[0].Tasks[0].Artifacts[0]
	if art.GetterSource != "http://10.0.0.11:4180/platform/artifacts/"+sha+".tar.gz" || art.GetterOptions["checksum"] != "sha256:"+sha {
		t.Fatalf("artifact: %+v", art)
	}
	if j.Meta["owners"] != "p25owner@iima.ac.in" || j.Meta["version"] != "build-aaaaaaa" {
		t.Fatalf("meta: %v", j.Meta)
	}

	// Nothing new: no second deploy.
	e.p.Sync(ctx)
	if e.nm.count() != 1 {
		t.Fatal("redeployed the same build")
	}
	// A new build deploys by itself.
	e.gh.AddRelease("club/quiz", "build-bbbbbbb", strings.Repeat("b", 40), []byte("build two"))
	e.p.Sync(ctx)
	if e.nm.count() != 2 || e.nm.last().Meta["version"] != "build-bbbbbbb" {
		t.Fatalf("auto deploy of the new build: %d", e.nm.count())
	}
	// Rollback is deploying the older build.
	if err := e.p.Deploy(ctx, "p25owner@iima.ac.in", "quiz", "build-aaaaaaa"); err != nil || e.nm.last().Meta["version"] != "build-aaaaaaa" {
		t.Fatalf("rollback: %v", err)
	}
}

func TestTamperedBuildIsRefused(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.gh.AddRepo("club/quiz", 2, map[string][]byte{"app.toml": []byte(quizToml)})
	e.p.Request(ctx, "p25owner@iima.ac.in", "club/quiz")
	e.p.Approve(ctx, "super@iima.ac.in", "quiz")
	e.gh.AddRelease("club/quiz", "build-ccccccc", "", []byte("real"))
	// Swap the artifact after the checksum was published.
	e.gh.Tamper("club/quiz", "build-ccccccc", []byte("evil"))
	e.p.Sync(ctx)
	if rs, _ := e.st.Releases(ctx, "quiz"); len(rs) != 0 || e.nm.count() != 0 {
		t.Fatalf("tampered build was stored (%d) or deployed (%d)", len(rs), e.nm.count())
	}
}

func TestSettingsApproval(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.gh.AddRepo("club/quiz", 2, map[string][]byte{"app.toml": []byte(quizToml)})
	e.p.Request(ctx, "p25owner@iima.ac.in", "club/quiz")
	e.p.Approve(ctx, "super@iima.ac.in", "quiz")
	pa, _ := e.st.PortalApp(ctx, "quiz")
	live, _ := e.p.Settings(pa)

	// A name or role change applies at once.
	next := *live
	next.Name, next.Roles = "Quiz", []string{"admin", "host"}
	if waiting, err := e.p.UpdateSettings(ctx, "p25owner@iima.ac.in", "quiz", &next, false); err != nil || waiting {
		t.Fatalf("plain change: %v waiting=%v", err, waiting)
	}
	if a := e.p.Registry().ByID("quiz"); a.Name != "Quiz" || len(a.Roles) != 2 {
		t.Fatalf("not applied: %+v", a)
	}
	// Opening the app to outside accounts waits for a super admin; the live app keeps its login.
	next.Login = registry.LoginExternal
	next.Name = "Quiz Club 2"
	if waiting, err := e.p.UpdateSettings(ctx, "p25owner@iima.ac.in", "quiz", &next, false); err != nil || !waiting {
		t.Fatalf("login change: %v waiting=%v", err, waiting)
	}
	if a := e.p.Registry().ByID("quiz"); a.Login != registry.LoginIIMA || a.Name != "Quiz Club 2" {
		t.Fatalf("live app changed early: %+v", a)
	}
	if err := e.p.Approve(ctx, "super@iima.ac.in", "quiz"); err != nil {
		t.Fatal(err)
	}
	if a := e.p.Registry().ByID("quiz"); a.Login != registry.LoginExternal {
		t.Fatalf("approved login not live: %+v", a)
	}
	// The id never changes, and the path cannot take a reserved one.
	bad := *live
	bad.Path = "/admin"
	if _, err := e.p.UpdateSettings(ctx, "p25owner@iima.ac.in", "quiz", &bad, false); !isUserError(err) {
		t.Fatalf("reserved path: %v", err)
	}
}

func TestEnv(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.gh.AddRepo("club/quiz", 2, map[string][]byte{"app.toml": []byte(quizToml)})
	e.p.Request(ctx, "p25owner@iima.ac.in", "club/quiz")
	e.p.Approve(ctx, "super@iima.ac.in", "quiz")
	e.gh.AddRelease("club/quiz", "build-aaaaaaa", "", []byte("one"))
	e.p.Sync(ctx)

	if err := e.p.SetEnv(ctx, "p25owner@iima.ac.in", "quiz", "API_KEY", "s3cret", true); err != nil {
		t.Fatal(err)
	}
	if e.nm.vars["nomad/jobs/quiz"]["API_KEY"] != "s3cret" {
		t.Fatalf("secret not in Nomad: %v", e.nm.vars)
	}
	vars, _ := e.st.Env(ctx, "quiz")
	if len(vars) != 1 || vars[0].Value != "" || !vars[0].Secret {
		t.Fatalf("secret value kept in core: %+v", vars)
	}

	before := e.nm.count()
	if err := e.p.SetEnv(ctx, "p25owner@iima.ac.in", "quiz", "SITE_NAME", "Quiz", false); err != nil {
		t.Fatal(err)
	}
	if e.nm.count() != before+1 || e.nm.last().TaskGroups[0].Tasks[0].Env["SITE_NAME"] != "Quiz" {
		t.Fatal("plain env change should redeploy with the value")
	}
	if _, ok := e.nm.last().TaskGroups[0].Tasks[0].Env["API_KEY"]; ok {
		t.Fatal("secret leaked into the job")
	}

	for _, k := range []string{"IIMA_JWKS_URL", "NOMAD_TOKEN", "lower", "APP_URL"} {
		if err := e.p.SetEnv(ctx, "p25owner@iima.ac.in", "quiz", k, "x", false); !isUserError(err) {
			t.Fatalf("%s accepted: %v", k, err)
		}
	}
	// Turning a secret into a plain value removes it from Nomad.
	if err := e.p.SetEnv(ctx, "p25owner@iima.ac.in", "quiz", "API_KEY", "public", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.nm.vars["nomad/jobs/quiz"]["API_KEY"]; ok {
		t.Fatal("old secret left in Nomad")
	}
}

func TestStopAndArchive(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.gh.AddRepo("club/quiz", 2, map[string][]byte{"app.toml": []byte(quizToml)})
	e.p.Request(ctx, "p25owner@iima.ac.in", "club/quiz")
	e.p.Approve(ctx, "super@iima.ac.in", "quiz")
	e.gh.AddRelease("club/quiz", "build-aaaaaaa", "", []byte("one"))
	e.p.Sync(ctx)
	if err := e.p.Stop(ctx, "p25owner@iima.ac.in", "quiz"); err != nil {
		t.Fatal(err)
	}
	n := e.nm.count()
	// A new build or a settings change must not bring a stopped app back.
	e.gh.AddRelease("club/quiz", "build-bbbbbbb", "", []byte("two"))
	e.p.Sync(ctx)
	if err := e.p.SetEnv(ctx, "p25owner@iima.ac.in", "quiz", "SITE", "x", false); err != nil {
		t.Fatal(err)
	}
	if e.nm.count() != n {
		t.Fatal("a stopped app was deployed again")
	}
	// Deploying by hand brings it back.
	if err := e.p.Deploy(ctx, "p25owner@iima.ac.in", "quiz", "build-bbbbbbb"); err != nil || e.nm.count() != n+1 {
		t.Fatalf("deploy after stop: %v", err)
	}
	if err := e.p.Archive(ctx, "super@iima.ac.in", "quiz"); err != nil {
		t.Fatal(err)
	}
	if e.p.Registry().ByID("quiz") != nil {
		t.Fatal("archived app still routed")
	}
	if err := e.p.Deploy(ctx, "p25owner@iima.ac.in", "quiz", "build-bbbbbbb"); !isUserError(err) {
		t.Fatalf("deploying an archived app: %v", err)
	}
}
