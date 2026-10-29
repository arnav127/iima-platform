package jobgen

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/arnav127/iima-platform/core/registry"
)

func app(t *testing.T, src string) *registry.App {
	t.Helper()
	a, err := registry.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var aux = `id = "auxesiscapital"
name = "Auxesis Capital"
path = "/auxesiscapital"
owners = ["a@iima.ac.in"]
shape = "go-pocketbase"
public = ["/api/health"]
volume = "auxesiscapital-data"
memory = 256`

var cluster = Cluster{Datacenter: "iima", Driver: "exec", PublicURL: "https://students.iima.ac.in/"}

var release = Release{Version: "v1.2.0", ArtifactURL: "https://github.com/x/y/releases/download/v1.2.0/app.tar.gz", SHA256: strings.Repeat("a", 64)}

func TestTags(t *testing.T) {
	got := Tags(app(t, aux))
	want := []string{
		"traefik.enable=true",
		"traefik.http.middlewares.auxesiscapital-strip.stripprefix.prefixes=/auxesiscapital",
		"traefik.http.routers.auxesiscapital-public-0.entrypoints=public",
		"traefik.http.routers.auxesiscapital-public-0.middlewares=auth-none@file,auxesiscapital-strip",
		"traefik.http.routers.auxesiscapital-public-0.priority=3000",
		"traefik.http.routers.auxesiscapital-public-0.rule=Path(`/auxesiscapital/api/health`) || PathPrefix(`/auxesiscapital/api/health/`)",
		"traefik.http.routers.auxesiscapital-public-0.service=auxesiscapital",
		"traefik.http.routers.auxesiscapital.entrypoints=public",
		"traefik.http.routers.auxesiscapital.middlewares=auth-optional@file,auxesiscapital-strip",
		"traefik.http.routers.auxesiscapital.priority=2000",
		"traefik.http.routers.auxesiscapital.rule=Path(`/auxesiscapital`) || PathPrefix(`/auxesiscapital/`)",
		"traefik.http.routers.auxesiscapital.service=auxesiscapital",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tags:\n%s", strings.Join(got, "\n"))
	}

	// Next.js keeps its path and gets the -next chain (it sets its own CSP); auth-required apps get
	// the required middleware.
	next := Tags(app(t, "id=\"web\"\nname=\"W\"\npath=\"/web\"\nshape=\"next\"\nauth=\"required\""))
	if slices.ContainsFunc(next, func(s string) bool { return strings.Contains(s, "strip") }) ||
		!slices.Contains(next, "traefik.http.routers.web.middlewares=auth-required-next@file") {
		t.Fatalf("next tags: %v", next)
	}
}

func TestJobOnTheCluster(t *testing.T) {
	j, err := Build(cluster, app(t, aux), release)
	if err != nil {
		t.Fatal(err)
	}
	tg := j.TaskGroups[0]
	task := tg.Tasks[0]
	if j.ID != "auxesiscapital" || j.Datacenters[0] != "iima" || !j.Update.AutoRevert || j.Meta["version"] != "v1.2.0" {
		t.Fatalf("job: %+v", j)
	}
	if tg.Volumes["data"].Source != "auxesiscapital-data" || task.VolumeMounts[0].Destination != "/data" {
		t.Fatal("PocketBase data must be on the host volume")
	}
	if task.Artifacts[0].GetterOptions["checksum"] != "sha256:"+release.SHA256 {
		t.Fatal("artifact checksum missing")
	}
	if args := task.Config["args"].([]string); strings.Join(args, " ") != "serve --dir /data --publicDir local/pb_public" {
		t.Fatalf("args: %v", args)
	}
	if task.Env["APP_URL"] != "https://students.iima.ac.in/auxesiscapital" || task.Env["IIMA_JWKS_URL"] != "https://students.iima.ac.in/auth/.well-known/jwks.json" {
		t.Fatalf("env: %v", task.Env)
	}
	if !strings.Contains(task.Templates[0].EmbeddedTmpl, `nomadVarExists "nomad/jobs/auxesiscapital"`) || !task.Templates[0].Envvars {
		t.Fatal("secrets template")
	}
	if tg.Services[0].Checks[0].Path != "/api/health" {
		t.Fatal("health check")
	}
	if _, err := json.Marshal(j); err != nil {
		t.Fatal(err)
	}
}

func TestJobRefusals(t *testing.T) {
	a := app(t, aux)
	if _, err := Build(cluster, a, Release{Version: "x"}); err == nil {
		t.Error("release without artifact accepted")
	}
	if _, err := Build(cluster, a, Release{Version: "x", ArtifactURL: "https://x", SHA256: "short"}); err == nil {
		t.Error("bad checksum accepted")
	}
	if _, err := Build(cluster, a, Release{Command: "C:/app.exe"}); err == nil {
		t.Error("local command accepted on exec")
	}
	noVol := app(t, strings.Replace(aux, `volume = "auxesiscapital-data"`, "", 1))
	if _, err := Build(cluster, noVol, release); err == nil {
		t.Error("PocketBase app without a volume accepted")
	}
	if _, err := Build(cluster, app2(t, "pocketbase"), release); err == nil {
		t.Error("pocketbase shape accepted before it is supported")
	}
}

func TestJobLocal(t *testing.T) {
	c := Cluster{Driver: "raw_exec", PublicURL: "http://localhost:8080", JWKSURL: "http://127.0.0.1:8081/auth/.well-known/jwks.json", LocalDataRoot: `C:\data\`}
	j, err := Build(c, app(t, aux), Release{Version: "dev", Command: `C:\bin\aux.exe`, PublicDir: `C:\aux\pb_public`})
	if err != nil {
		t.Fatal(err)
	}
	task := j.TaskGroups[0].Tasks[0]
	if len(task.Artifacts) != 0 || len(task.VolumeMounts) != 0 || j.TaskGroups[0].Volumes != nil {
		t.Fatal("local job should not download or mount")
	}
	if args := task.Config["args"].([]string); args[2] != "C:\\data/auxesiscapital" || task.Config["command"] != `C:\bin\aux.exe` {
		t.Fatalf("config: %v", task.Config)
	}
}

func TestBinarySuffix(t *testing.T) {
	a := app(t, `id="svc"`+"\n"+`name="S"`+"\n"+`path="/svc"`+"\n"+`shape="go"`)
	rel := Release{Version: "v1", ArtifactURL: "http://core/platform/artifacts/x.tar.gz", SHA256: strings.Repeat("a", 64)}
	for suffix, want := range map[string]string{"": "local/app", ".exe": "local/app.exe"} {
		j, err := Build(Cluster{Datacenter: "dc1", Driver: "raw_exec", BinarySuffix: suffix}, a, rel)
		if err != nil {
			t.Fatal(err)
		}
		if got := j.TaskGroups[0].Tasks[0].Config["command"]; got != want {
			t.Fatalf("suffix %q: command %v, want %s", suffix, got, want)
		}
	}
}

func TestNextWithPocketBase(t *testing.T) {
	a := app(t, `id = "betamarkets"
name = "BetaMarkets"
path = "/betamarkets"
shape = "next"
pocketbase = true
volume = "betamarkets-data"
`)
	rel := Release{Version: "build-1", ArtifactURL: "http://core/platform/artifacts/x.tar.gz", SHA256: strings.Repeat("b", 64), Env: map[string]string{"X": "1"}}
	j, err := Build(Cluster{Datacenter: "dc1", Driver: "exec", PublicURL: "https://students.iima.ac.in"}, a, rel)
	if err != nil {
		t.Fatal(err)
	}
	tg := j.TaskGroups[0]
	if len(tg.Tasks) != 2 || tg.Tasks[0].Name != "pocketbase" || tg.Tasks[1].Name != "app" {
		t.Fatalf("tasks: %+v", tg.Tasks)
	}
	pb, app := tg.Tasks[0], tg.Tasks[1]
	if pb.Lifecycle == nil || pb.Lifecycle.Hook != "prestart" || !pb.Lifecycle.Sidecar {
		t.Fatalf("pocketbase must be a prestart sidecar: %+v", pb.Lifecycle)
	}
	args := pb.Config["args"].([]string)
	if args[2] != "127.0.0.1:${NOMAD_PORT_pb}" || args[4] != "/data" {
		t.Fatalf("pocketbase must listen on loopback with its data on the volume: %v", args)
	}
	if len(pb.VolumeMounts) != 1 || len(app.VolumeMounts) != 0 || tg.Volumes["data"].Source != "betamarkets-data" {
		t.Fatal("only PocketBase mounts the data volume")
	}
	if app.Config["command"] != "node" || app.Env["POCKETBASE_URL"] != "http://127.0.0.1:${NOMAD_PORT_pb}" || app.Env["X"] != "1" {
		t.Fatalf("app task: %v %v", app.Config, app.Env)
	}
	if len(pb.Artifacts) != 1 || len(pb.Templates) != 1 {
		t.Fatal("pocketbase needs the build and the secrets (its superuser)")
	}
	if c := tg.Services[0].Checks[0]; c.Path != "/betamarkets/api/health" {
		t.Fatalf("health check %q: Next keeps its base path", c.Path)
	}
	for _, tag := range tg.Services[0].Tags {
		if strings.Contains(tag, "strip") {
			t.Fatalf("Next apps are not stripped: %s", tag)
		}
	}

	// Without pocketbase, one task; a Go app cannot ask for the sidecar.
	plain := app2(t, "next")
	if j, err := Build(Cluster{Datacenter: "dc1", Driver: "exec"}, plain, rel); err != nil || len(j.TaskGroups[0].Tasks) != 1 {
		t.Fatalf("plain next: %v", err)
	}
	if _, err := registry.Parse([]byte("id = \"g\"\nname = \"G\"\npath = \"/g\"\nshape = \"go\"\npocketbase = true\n")); err == nil {
		t.Fatal("pocketbase = true on a Go app accepted")
	}
}

func app2(t *testing.T, shape string) *registry.App {
	return app(t, "id = \"web\"\nname = \"W\"\npath = \"/web\"\nshape = \""+shape+"\"\n")
}

func TestPython(t *testing.T) {
	a := app(t, "id = \"api\"\nname = \"API\"\npath = \"/api-demo\"\nshape = \"python\"\nentry = \"server:app\"\n")
	rel := Release{Version: "build-1", ArtifactURL: "http://core/platform/artifacts/x.tar.gz", SHA256: strings.Repeat("c", 64)}
	j, err := Build(Cluster{Datacenter: "dc1", Driver: "exec", PublicURL: "https://students.iima.ac.in"}, a, rel)
	if err != nil {
		t.Fatal(err)
	}
	task := j.TaskGroups[0].Tasks[0]
	args := task.Config["args"].([]string)
	if task.Config["command"] != "python3" || args[2] != "server:app" || task.Env["PYTHONPATH"] != "local/app:local/site" {
		t.Fatalf("python task: %v %v", task.Config, task.Env)
	}
	if j.TaskGroups[0].Services[0].Checks[0].Path != "/health" {
		t.Fatal("python apps are stripped: health at /health")
	}
	strip := false
	for _, tag := range j.TaskGroups[0].Services[0].Tags {
		strip = strip || strings.Contains(tag, "stripprefix")
	}
	if !strip {
		t.Fatal("python apps should be stripped")
	}
}

func TestNextWithBackend(t *testing.T) {
	a := app(t, "id = \"portfolioleague\"\nname = \"PL\"\npath = \"/portfolioleague\"\nshape = \"next\"\npocketbase = true\nbackend = \"main:app\"\nvolume = \"portfolioleague-data\"\n")
	rel := Release{Version: "b1", ArtifactURL: "http://core/platform/artifacts/x.tar.gz", SHA256: strings.Repeat("d", 64)}
	j, err := Build(Cluster{Datacenter: "dc1", Driver: "exec", PublicURL: "https://students.iima.ac.in"}, a, rel)
	if err != nil {
		t.Fatal(err)
	}
	tasks := map[string]*Task{}
	for _, task := range j.TaskGroups[0].Tasks {
		tasks[task.Name] = task
	}
	be, web := tasks["backend"], tasks["app"]
	if be == nil || web == nil || tasks["pocketbase"] == nil || len(j.TaskGroups[0].Tasks) != 3 {
		t.Fatalf("tasks: %v", j.TaskGroups[0].Tasks)
	}
	args := be.Config["args"].([]string)
	if args[2] != "main:app" || args[6] != "127.0.0.1" || be.Lifecycle == nil || !be.Lifecycle.Sidecar {
		t.Fatalf("backend: %v %+v", args, be.Lifecycle)
	}
	if web.Env["BACKEND_URL"] != "http://127.0.0.1:${NOMAD_PORT_backend}" || be.Env["POCKETBASE_URL"] == "" || len(be.Templates) != 1 {
		t.Fatalf("wiring: web %v, backend %v", web.Env, be.Env)
	}
	if _, err := registry.Parse([]byte("id = \"g\"\nname = \"G\"\npath = \"/g\"\nshape = \"go\"\nbackend = \"main:app\"\n")); err == nil {
		t.Fatal("backend on a Go app accepted")
	}
}
