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
	// BinarySuffix is ".exe" for a local Nomad on Windows: Nomad looks for the exact file name, and a
	// build made for Windows is app.exe. Empty on the Linux cluster.
	BinarySuffix string `toml:"binary_suffix"`
	// Node is the Node.js binary on every client, for Next.js apps (default "node" on the PATH).
	Node string `toml:"node"`
	// Python is the Python on every client, for Python apps (default "python3"). Builds install
	// dependencies for the same minor version (the build workflow's python-version).
	Python string `toml:"python"`
}

// pathSep separates PYTHONPATH entries: ";" for a local Nomad on Windows.
func (c Cluster) pathSep() string {
	if c.BinarySuffix == ".exe" {
		return ";"
	}
	return ":"
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
	"next":          "/api/health", // under the app's path: Next keeps its basePath
	"python":        "/health",
}

const ()

// Build returns the Nomad job for app.
func Build(c Cluster, app *registry.App, rel Release) (*Job, error) {
	health, ok := healthPath[app.Shape]
	if !ok {
		return nil, fmt.Errorf("jobgen: shape %q is not supported yet (go-pocketbase, go, next and python are)", app.Shape)
	}
	if app.Shape == "next" && !app.IsRoot() {
		health = app.Path + health
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

	binary, publicDir := "local/app"+c.BinarySuffix, "local/pb_public"
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

	// data gives the task (the one running PocketBase) the app's data directory.
	data := func(t *Task) (string, error) {
		if app.Volume == "" {
			return "", fmt.Errorf("jobgen: %s runs PocketBase and needs volume in app.toml", app.ID)
		}
		if c.Driver != "exec" {
			return strings.TrimRight(c.LocalDataRoot, `/\`) + "/" + app.ID, nil
		}
		// A host volume pins the app to the node that holds its data, so a redeploy can never
		// start it on an empty database.
		tg.Volumes = map[string]*Volume{"data": {
			Name: "data", Type: "host", Source: app.Volume,
			AccessMode: "single-node-single-writer", AttachmentMode: "file-system",
		}}
		t.VolumeMounts = []*VolumeMount{{Volume: "data", Destination: "/data"}}
		return "/data", nil
	}

	switch app.Shape {
	case "go-pocketbase":
		dir, err := data(task)
		if err != nil {
			return nil, err
		}
		task.Config = map[string]any{"command": binary, "args": []string{"serve", "--dir", dir, "--publicDir", publicDir}}
	case "go":
		task.Config = map[string]any{"command": binary}
	case "next":
		if local {
			return nil, errors.New("jobgen: Next.js apps run from a release, not a local command")
		}
		// Next's standalone server, with its base path kept (Next apps are not stripped).
		task.Config = map[string]any{"command": orStr(c.Node, "node"), "args": []string{"local/app/server.js"}}
		task.Env["PORT"] = "${NOMAD_PORT_http}"
		task.Env["HOSTNAME"] = "${NOMAD_IP_http}"
		task.Env["NODE_ENV"] = "production"
	case "python":
		if local {
			return nil, errors.New("jobgen: Python apps run from a release, not a local command")
		}
		// uvicorn serving the ASGI app; the code is in local/app and its dependencies, installed by
		// the build, in local/site. The gateway strips the path, as for Go apps.
		task.Config = map[string]any{"command": orStr(c.Python, "python3"), "args": []string{
			"-m", "uvicorn", orStr(app.Entry, "main:app"), "--host", "${NOMAD_IP_http}", "--port", "${NOMAD_PORT_http}",
		}}
		task.Env["PYTHONPATH"] = "local/app" + c.pathSep() + "local/site"
		task.Env["PYTHONUNBUFFERED"] = "1"
	}

	if app.Backend != "" {
		// The app's Python backend beside the Next server, on loopback only: the Next server is
		// its only client. Code in local/backend, dependencies (from the build) in local/site.
		be := &Task{
			Name: "backend", Driver: c.Driver,
			Env: map[string]string{
				"PYTHONPATH": "local/backend" + c.pathSep() + "local/site", "PYTHONUNBUFFERED": "1",
				"IIMA_JWKS_URL": c.JWKSURL, "APP_URL": publicURL + app.Path,
			},
			Lifecycle:   &Lifecycle{Hook: "prestart", Sidecar: true},
			Resources:   &Resources{CPU: 200, MemoryMB: 256},
			KillTimeout: dur(10 * time.Second),
			Artifacts:   task.Artifacts,
			Templates:   task.Templates,
			Config: map[string]any{"command": orStr(c.Python, "python3"), "args": []string{
				"-m", "uvicorn", app.Backend, "--app-dir", "local/backend", "--host", "127.0.0.1", "--port", "${NOMAD_PORT_backend}",
			}},
		}
		maps.Copy(be.Env, rel.Env)
		tg.Networks[0].DynamicPorts = append(tg.Networks[0].DynamicPorts, Port{Label: "backend"})
		task.Env["BACKEND_URL"] = "http://127.0.0.1:${NOMAD_PORT_backend}"
		tg.Tasks = append([]*Task{be}, tg.Tasks...)
	}

	if app.PocketBase {
		// The platform's PocketBase beside the app, on loopback only: the app's server is its only
		// client. It starts first and runs as long as the app (a sidecar).
		pb := &Task{
			Name: "pocketbase", Driver: c.Driver, Env: map[string]string{},
			Lifecycle:   &Lifecycle{Hook: "prestart", Sidecar: true},
			Resources:   &Resources{CPU: 100, MemoryMB: 128},
			KillTimeout: dur(10 * time.Second),
			Artifacts:   task.Artifacts,
			Templates:   task.Templates, // PB_ADMIN_EMAIL and PB_ADMIN_PASSWORD bootstrap its superuser
		}
		dir, err := data(pb)
		if err != nil {
			return nil, err
		}
		pb.Config = map[string]any{"command": "local/pocketbase" + c.BinarySuffix, "args": []string{
			"serve", "--http", "127.0.0.1:${NOMAD_PORT_pb}", "--dir", dir,
			"--migrationsDir", "local/pb_migrations", "--hooksDir", "local/pb_hooks",
		}}
		tg.Networks[0].DynamicPorts = append(tg.Networks[0].DynamicPorts, Port{Label: "pb"})
		task.Env["POCKETBASE_URL"] = "http://127.0.0.1:${NOMAD_PORT_pb}"
		for _, t := range tg.Tasks {
			if t.Name == "backend" {
				t.Env["POCKETBASE_URL"] = task.Env["POCKETBASE_URL"]
			}
		}
		tg.Tasks = append([]*Task{pb}, tg.Tasks...)
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
		fmt.Sprintf("traefik.http.routers.%s.rule=%s", id, app.Rule("")),
		fmt.Sprintf("traefik.http.routers.%s.priority=%d", id, app.Priority(false)),
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
			fmt.Sprintf("traefik.http.routers.%s.rule=%s", r, app.Rule(p)),
			fmt.Sprintf("traefik.http.routers.%s.priority=%d", r, app.Priority(true)),
			fmt.Sprintf("traefik.http.routers.%s.entrypoints=public", r),
			fmt.Sprintf("traefik.http.routers.%s.middlewares=%s", r, strings.Join(pmw, ",")),
			fmt.Sprintf("traefik.http.routers.%s.service=%s", r, id),
		)
	}
	tags = append(tags, fmt.Sprintf("traefik.http.routers.%s.service=%s", id, id))
	slices.Sort(tags)
	return tags
}

func orStr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

func dur(d time.Duration) int64 { return int64(d) }
