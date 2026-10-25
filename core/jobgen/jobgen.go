// Package jobgen turns an app's manifest and a release into a Nomad job. Routes and middlewares come
// from the reviewed manifest here, never from anything the app's author writes, and only the deploy
// portal and super admins may submit jobs (Nomad ACLs), so an app cannot claim another app's path.
//
// The output is Nomad's JSON job format: `nomad job run -json job.json`, or the jobs API.
package jobgen

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/arnav127/iima-platform/core/registry"
)

// Cluster is what is the same for every app on one cluster (deploy/nomad/cluster.toml).
type Cluster struct {
	Datacenter string `toml:"datacenter"` // "iima"
	// Driver is "exec" on the Linux cluster (isolated), "raw_exec" for a local Nomad on a laptop.
	Driver    string `toml:"driver"`
	PublicURL string `toml:"public_url"` // https://students.iima.ac.in
	// JWKSURL is where apps fetch core's public keys. Default: PublicURL + /auth/.well-known/jwks.json.
	JWKSURL string `toml:"jwks_url"`
	// LocalDataRoot holds app data with raw_exec, which cannot mount volumes: <root>/<app id>.
	LocalDataRoot string `toml:"local_data_root"`
}

// Release is one build of an app: a tar.gz with the binary at ./app and, for PocketBase apps,
// the built frontend at ./pb_public. Built in CI and published as a release (phase 8).
type Release struct {
	Version     string // git tag or commit; shown in Nomad and used for rollback
	ArtifactURL string
	SHA256      string // of the artifact; Nomad refuses a download that does not match
	// Local mode (laptop): run this binary and frontend in place instead of downloading.
	Command   string
	PublicDir string
	// Env is extra non-secret configuration. Secrets go in Nomad Variables at nomad/jobs/<app id>.
	Env map[string]string
}

// Health check paths by shape. They are checked on the app directly, not through the gateway.
var healthPath = map[string]string{
	"go-pocketbase": "/api/health",
	"go":            "/health",
}

const (
	priorityApp    = 2000
	priorityPublic = 3000
)

// Build returns the Nomad job for app.
func Build(c Cluster, app *registry.App, rel Release) (*Job, error) {
	health, ok := healthPath[app.Shape]
	if !ok {
		return nil, fmt.Errorf("jobgen: shape %q is not supported yet (go-pocketbase and go are)", app.Shape)
	}
	if c.Driver != "exec" && c.Driver != "raw_exec" {
		return nil, fmt.Errorf("jobgen: driver must be exec or raw_exec, not %q", c.Driver)
	}
	local := rel.Command != ""
	if !local && (rel.ArtifactURL == "" || len(rel.SHA256) != 64) {
		return nil, errors.New("jobgen: a release needs ArtifactURL and a SHA-256, or a local Command")
	}
	if local && c.Driver != "raw_exec" {
		return nil, errors.New("jobgen: a local Command only works with raw_exec")
	}
	if c.Datacenter == "" {
		c.Datacenter = "dc1"
	}
	publicURL := strings.TrimRight(c.PublicURL, "/")
	if c.JWKSURL == "" {
		c.JWKSURL = publicURL + "/auth/.well-known/jwks.json"
	}

	env := map[string]string{
		"IIMA_JWKS_URL": c.JWKSURL,
		"APP_URL":       publicURL + app.Path,
	}
	maps.Copy(env, rel.Env)

	task := &Task{
		Name: "app", Driver: c.Driver, Env: env,
		Resources:   &Resources{CPU: 200, MemoryMB: orDefault(app.Memory, 256)},
		KillTimeout: dur(10 * time.Second), // let a running sync or request finish
		// Secrets: every key in the app's Nomad Variable becomes an env var. nomadVarExists keeps
		// the task from waiting forever when an app has no secrets.
		Templates: []*Template{{
			EmbeddedTmpl: fmt.Sprintf(`{{ if nomadVarExists "nomad/jobs/%[1]s" }}{{ with nomadVar "nomad/jobs/%[1]s" }}{{ range .Tuples }}{{ .K }}={{ .V | toJSON }}
{{ end }}{{ end }}{{ end }}`, app.ID),
			DestPath: "secrets/app.env", Envvars: true, ChangeMode: "restart",
		}},
	}

	binary, publicDir := "local/app", "local/pb_public"
	if local {
		binary, publicDir = rel.Command, rel.PublicDir
	} else {
		task.Artifacts = []*Artifact{{
			GetterSource: rel.ArtifactURL, RelativeDest: "local/",
			GetterOptions: map[string]string{"checksum": "sha256:" + rel.SHA256},
		}}
	}

	tg := &TaskGroup{
		Name: app.ID, Count: 1,
		Networks: []*Network{{DynamicPorts: []Port{{Label: "http"}}}},
		Services: []*Service{{
			Name: app.ID, Provider: "nomad", PortLabel: "http", Tags: Tags(app),
			Checks: []*Check{{Name: "health", Type: "http", Path: health, Interval: dur(10 * time.Second), Timeout: dur(3 * time.Second)}},
		}},
		// "fail", not "delay": a task that keeps crashing must fail its allocation, so the deployment
		// fails and auto_revert brings back the last good version instead of retrying forever.
		RestartPolicy: &RestartPolicy{Attempts: 2, Interval: dur(5 * time.Minute), Delay: dur(10 * time.Second), Mode: "fail"},
		// Deregister from Traefik a moment before stopping, so no request lands on a dying copy.
		ShutdownDelay: dur(5 * time.Second),
		Tasks:         []*Task{task},
	}

	switch app.Shape {
	case "go-pocketbase":
		dataDir := "/data"
		if app.Volume == "" {
			return nil, fmt.Errorf("jobgen: %s is a PocketBase app and needs volume in app.toml", app.ID)
		}
		if c.Driver == "exec" {
			// A host volume pins the app to the node that holds its data, so a redeploy can never
			// start it on an empty database.
			tg.Volumes = map[string]*Volume{"data": {
				Name: "data", Type: "host", Source: app.Volume,
				AccessMode: "single-node-single-writer", AttachmentMode: "file-system",
			}}
			task.VolumeMounts = []*VolumeMount{{Volume: "data", Destination: "/data"}}
		} else {
			dataDir = strings.TrimRight(c.LocalDataRoot, `/\`) + "/" + app.ID
		}
		task.Config = map[string]any{"command": binary, "args": []string{"serve", "--dir", dataDir, "--publicDir", publicDir}}
	case "go":
		task.Config = map[string]any{"command": binary}
	}

	return &Job{
		ID: app.ID, Name: app.ID, Type: "service", Datacenters: []string{c.Datacenter},
		Meta: map[string]string{"version": rel.Version, "owners": strings.Join(app.Owners, ","), "review": app.Review},
		// One instance with SQLite: stop the old version before starting the new (Nomad's default
		// for a destructive update), and go back to the last good version if the new one is unhealthy.
		// A broken release therefore means a few minutes down (about the progress deadline) before
		// the revert; two copies on one SQLite file would be worse. Measured on a local Nomad.
		Update: &Update{
			MaxParallel: 1, HealthCheck: "checks", MinHealthyTime: dur(10 * time.Second),
			HealthyDeadline: dur(2 * time.Minute), ProgressDeadline: dur(3 * time.Minute), AutoRevert: true,
		},
		TaskGroups: []*TaskGroup{tg},
	}, nil
}

// Tags are the Traefik routes for app, read by Traefik's Nomad provider. They mirror what the dev
// stack writes for the file provider: the app's auth middleware, its public paths on auth-none,
// and the path strip last (after forward auth has seen the full path).
func Tags(app *registry.App) []string {
	id := app.ID
	mw := []string{app.Chain(app.Auth) + "@file"}
	tags := []string{
		"traefik.enable=true",
		fmt.Sprintf("traefik.http.routers.%s.rule=%s", id, prefixRule(app.Path)),
		fmt.Sprintf("traefik.http.routers.%s.priority=%d", id, priorityApp),
		fmt.Sprintf("traefik.http.routers.%s.entrypoints=public", id),
	}
	if app.Strips() {
		tags = append(tags, fmt.Sprintf("traefik.http.middlewares.%s-strip.stripprefix.prefixes=%s", id, app.Path))
		mw = append(mw, id+"-strip")
	}
	tags = append(tags, fmt.Sprintf("traefik.http.routers.%s.middlewares=%s", id, strings.Join(mw, ",")))
	for i, p := range app.Public {
		r := fmt.Sprintf("%s-public-%d", id, i)
		pmw := []string{app.Chain("none") + "@file"}
		if app.Strips() {
			pmw = append(pmw, id+"-strip")
		}
		tags = append(tags,
			fmt.Sprintf("traefik.http.routers.%s.rule=%s", r, prefixRule(app.Path+p)),
			fmt.Sprintf("traefik.http.routers.%s.priority=%d", r, priorityPublic),
			fmt.Sprintf("traefik.http.routers.%s.entrypoints=public", r),
			fmt.Sprintf("traefik.http.routers.%s.middlewares=%s", r, strings.Join(pmw, ",")),
			fmt.Sprintf("traefik.http.routers.%s.service=%s", r, id),
		)
	}
	tags = append(tags, fmt.Sprintf("traefik.http.routers.%s.service=%s", id, id))
	slices.Sort(tags)
	return tags
}

func prefixRule(p string) string { return fmt.Sprintf("Path(`%s`) || PathPrefix(`%s/`)", p, p) }

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

func dur(d time.Duration) int64 { return int64(d) }
