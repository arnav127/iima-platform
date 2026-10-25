// Command devstack runs the platform on a laptop: Traefik on http://localhost:8080, core, and one
// stub app per manifest in dev/apps. Works on Windows, macOS and Linux.
//
//	go run ./dev/cmd/devstack            (from the repo root)
//	go run ./dev/cmd/devstack -google    (real Google sign-in only; needs GOOGLE_* in .env)
//	go run ./dev/cmd/devstack -app auxesiscapital=../auxesiscapital
//
// -app runs a real Go + PocketBase app from its repo next to this one (any registry id, repeatable):
// it builds <repo>/backend, serves <repo>/backend/pb_public (build the frontend first with the
// app's base path), and keeps its data in .dev/data/<id>. Its env comes from your shell.
//
// Everything it downloads, builds or stores goes in .dev/ (ignored by git). Ctrl+C stops it all.
package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/arnav127/iima-platform/core/registry"
)

// Traefik is pinned. To upgrade, change the version and copy the new hashes from the release's
// traefik_<version>_checksums.txt.
const traefikVersion = "v3.7.14"

var traefikSHA256 = map[string]string{
	"windows_amd64": "47b5d11c5d8b2d798357edf9d73e33a40bb045b4c7371da5e322c5083501c5c4",
	"linux_amd64":   "d08641f5de2dd3788097bddab1ce4b29c10a0b788566970db707fa5c94c71f15",
	"darwin_arm64":  "ff42488703fe5b7d627e36e581ab7bb0eb6685262fce9c838d964175cbf0d1fc",
	"darwin_amd64":  "54693a6e9b99fdbbe790f346911495aef95cb40efd24562d4b9d092d62a5d82f",
}

const (
	pluginModule = "github.com/arnav127/iima-platform/deploy/traefik/plugins/platformstrip"
)

func main() {
	googleOnly := flag.Bool("google", false, "turn off dev login; sign in with Google only (needs GOOGLE_CLIENT_ID/SECRET)")
	realApps := map[string]string{}
	flag.Func("app", "run a real app: id=path/to/its/repo (repeatable)", func(v string) error {
		id, dir, ok := strings.Cut(v, "=")
		if !ok || id == "" || dir == "" {
			return errors.New("use -app id=path/to/repo")
		}
		realApps[id] = dir
		return nil
	})
	nomadMode := flag.Bool("nomad", false, "run core and apps as Nomad jobs, with Traefik reading routes from Nomad (the cluster's layout)")
	flag.Parse()
	if err := run(*googleOnly, *nomadMode, realApps); err != nil {
		fmt.Fprintln(os.Stderr, "devstack:", err)
		os.Exit(1)
	}
}

// realApp is an app from the registry whose code lives in a sibling repo.
type realApp struct {
	app  *registry.App
	repo string
	bin  string
}

func run(googleOnly, nomadMode bool, realApps map[string]string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	dev := filepath.Join(root, ".dev")
	bin := filepath.Join(dev, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	dotenv, err := readDotenv(filepath.Join(root, ".env"))
	if err != nil {
		return err
	}

	reg, err := registry.LoadDirs(filepath.Join(root, "apps"), filepath.Join(root, "dev", "apps"))
	if err != nil {
		return err
	}
	devApps, err := registry.LoadDirs(filepath.Join(root, "dev", "apps"))
	if err != nil {
		return err
	}
	// Real apps come from the registry (apps/); their code from the sibling repo.
	var reals []realApp
	for id, dir := range realApps {
		a := reg.ByID(id)
		if a == nil {
			return fmt.Errorf("-app %s: not in the registry", id)
		}
		if a.Shape != "go-pocketbase" && a.Shape != "next" {
			return fmt.Errorf("-app %s: devstack runs go-pocketbase and next apps (this one is %s)", id, a.Shape)
		}
		if a.Shape == "next" && nomadMode {
			return fmt.Errorf("-app %s: next apps run in file mode only for now (drop -nomad)", id)
		}
		repo, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		reals = append(reals, realApp{a, repo, filepath.Join(bin, exe(id))})
	}

	traefik, err := ensureTraefik(bin)
	if err != nil {
		return err
	}
	step("building core and the demo app")
	coreBin, demoBin := filepath.Join(bin, exe("core")), filepath.Join(bin, exe("demo"))
	if err := goBuild(filepath.Join(root, "core"), coreBin, "./cmd/core"); err != nil {
		return err
	}
	if err := goBuild(filepath.Join(root, "dev"), demoBin, "./cmd/demo"); err != nil {
		return err
	}
	routed := devApps.Apps()
	pbBin := filepath.Join(bin, exe("pocketbase"))
	builtPB := false
	for _, r := range reals {
		routed = append(routed, r.app)
		if r.app.Shape == "next" {
			// The app builds itself (npm run build); devstack only needs the platform's PocketBase.
			if !builtPB {
				step("building the platform's PocketBase")
				if err := goBuild(filepath.Join(root, "pocketbase"), pbBin, "."); err != nil {
					return err
				}
				builtPB = true
			}
			continue
		}
		step("building " + r.app.ID + " from " + r.repo)
		if err := goBuild(filepath.Join(r.repo, "backend"), r.bin, "."); err != nil {
			return err
		}
	}

	tdir := filepath.Join(dev, "traefik")
	if nomadMode {
		return runNomad(nomadSetup{
			root: root, dev: dev, bin: bin, tdir: tdir, dotenv: dotenv, googleOnly: googleOnly,
			traefik: traefik, coreBin: coreBin, demoBin: demoBin, devApps: devApps.Apps(), reals: reals,
		})
	}
	ports, err := prepareTraefik(root, tdir, "traefik.local.yml", routed)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var g group

	corePort, err := freePort()
	if err != nil {
		return err
	}
	coreAddr := fmt.Sprintf("127.0.0.1:%d", corePort)

	coreEnv := map[string]string{
		"CORE_ADDR":  coreAddr,
		"PUBLIC_URL": "http://localhost:8080",
		"CORE_DATA":  filepath.Join(dev, "core-data"),
		"REGISTRY":   filepath.Join(root, "apps") + "," + filepath.Join(root, "dev", "apps"),
	}
	if !googleOnly {
		coreEnv["CORE_DEV_LOGIN"] = "1"
	}
	g.start(ctx, "core", coreBin, nil, root, merge(dotenv, coreEnv))
	if err := waitHTTP(ctx, "http://"+coreAddr+"/health"); err != nil {
		g.stopAll()
		return fmt.Errorf("core did not start: %w", err)
	}
	for _, a := range devApps.Apps() {
		g.start(ctx, a.ID, demoBin, nil, root, map[string]string{
			"APP_ID": a.ID, "PORT": fmt.Sprint(ports[a.ID]),
			// Straight to core, so the apps do not depend on Traefik being up first.
			"IIMA_JWKS_URL": "http://" + coreAddr + "/auth/.well-known/jwks.json",
		})
	}
	for _, r := range reals {
		data := filepath.Join(dev, "data", r.app.ID)
		if r.app.Shape == "next" {
			if err := startNext(ctx, &g, r, pbBin, data, ports[r.app.ID], coreAddr); err != nil {
				g.stopAll()
				return err
			}
			continue
		}
		args := []string{"serve", "--dir", data, "--publicDir", filepath.Join(r.repo, "backend", "pb_public")}
		g.start(ctx, r.app.ID, r.bin, args, filepath.Join(r.repo, "backend"), map[string]string{
			"PORT":          fmt.Sprint(ports[r.app.ID]),
			"IIMA_JWKS_URL": "http://" + coreAddr + "/auth/.well-known/jwks.json",
			"APP_URL":       "http://localhost:8080" + r.app.Path,
		})
	}
	g.start(ctx, "traefik", traefik, []string{"--configFile=traefik.yml"}, tdir, map[string]string{"IIMA_CORE_URL": "http://" + coreAddr})

	if err := waitHTTP(ctx, "http://localhost:8080/"); err == nil {
		fmt.Println()
		step("ready: http://localhost:8080/")
		for _, a := range routed {
			fmt.Printf("    http://localhost:8080%s/   (%s, login=%s, auth=%s)\n", a.Path, a.Name, a.Login, a.Auth)
		}
		if googleOnly {
			fmt.Println("    Sign-in: Google only.")
		} else {
			fmt.Println("    Sign-in: dev form (any email). Use -google to test real Google sign-in.")
		}
		fmt.Println("    Ctrl+C to stop.")
	}

	<-g.firstExit(ctx)
	g.stopAll()
	return nil
}

func step(msg string) { fmt.Println("==> " + msg) }

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// repoRoot walks up from the working directory to the folder with go.work.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run this from inside the iima-platform repo")
		}
		dir = parent
	}
}

// readDotenv reads KEY=VALUE lines. Missing file is fine.
func readDotenv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out, sc.Err()
}

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func goBuild(moduleDir, out, pkg string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = moduleDir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// ensureTraefik downloads the pinned Traefik release into bin once, checking its SHA-256.
func ensureTraefik(bin string) (string, error) {
	platform := runtime.GOOS + "_" + runtime.GOARCH
	want, ok := traefikSHA256[platform]
	if !ok {
		return "", fmt.Errorf("no pinned Traefik build for %s; add it to traefikSHA256", platform)
	}
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	url := fmt.Sprintf("https://github.com/traefik/traefik/releases/download/%s/traefik_%s_%s%s", traefikVersion, traefikVersion, platform, ext)
	return fetchPinned(bin, "traefik", traefikVersion, url, want)
}

// fetchPinned downloads a release archive (zip or tar.gz) once, checks its SHA-256 and extracts
// the binary called name into bin.
func fetchPinned(bin, name, version, url, want string) (string, error) {
	path := filepath.Join(bin, exe(name))
	marker := path + ".version"
	if v, err := os.ReadFile(marker); err == nil && string(v) == version {
		return path, nil
	}
	ext := ".tar.gz"
	if strings.HasSuffix(url, ".zip") {
		ext = ".zip"
	}
	step("downloading " + name + " " + version)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	archive, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return "", fmt.Errorf("%s download checksum mismatch: got %s, want %s", name, got, want)
	}

	var binary []byte
	if ext == ".zip" {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return "", err
		}
		for _, f := range zr.File {
			if f.Name == exe(name) || f.Name == name {
				rc, err := f.Open()
				if err != nil {
					return "", err
				}
				binary, err = io.ReadAll(rc)
				rc.Close()
				if err != nil {
					return "", err
				}
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return "", err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
			if h.Name == name {
				if binary, err = io.ReadAll(tr); err != nil {
					return "", err
				}
			}
		}
	}
	if binary == nil {
		return "", fmt.Errorf("%s binary not found in the release archive", name)
	}
	if err := os.WriteFile(path, binary, 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(marker, []byte(version), 0o644)
}

// prepareTraefik lays out tdir: the static config (from deploy/traefik/<static>), platform
// middlewares, generated app routes (none when apps is empty, e.g. in Nomad mode, where routes come
// from job tags) and the local plugin source. Returns the port given to each app.
func prepareTraefik(root, tdir, static string, apps []*registry.App) (map[string]int, error) {
	dyn := filepath.Join(tdir, "dynamic")
	plugin := filepath.Join(tdir, "plugins-local", "src", filepath.FromSlash(pluginModule))
	for _, d := range []string{dyn, filepath.Join(tdir, "plugins-local")} {
		if err := os.RemoveAll(d); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dyn, 0o755); err != nil {
		return nil, err
	}
	if err := copyFile(filepath.Join(root, "deploy", "traefik", static), filepath.Join(tdir, "traefik.yml")); err != nil {
		return nil, err
	}
	if err := copyFile(filepath.Join(root, "deploy", "traefik", "dynamic", "platform.yml"), filepath.Join(dyn, "platform.yml")); err != nil {
		return nil, err
	}
	if err := os.CopyFS(plugin, os.DirFS(filepath.Join(root, "deploy", "traefik", "plugins", "platformstrip"))); err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, nil
	}

	ports := map[string]int{}
	for _, a := range apps {
		p, err := freePort()
		if err != nil {
			return nil, err
		}
		ports[a.ID] = p
	}
	var b strings.Builder
	b.WriteString("# Generated by devstack from dev/apps. On the cluster these routes come from Nomad.\nhttp:\n  routers:\n")
	for _, a := range apps {
		// The strip runs last, after forward auth has seen the full path.
		strip := ""
		if a.Strips() {
			strip = ", app-" + a.ID + "-strip"
		}
		fmt.Fprintf(&b, "    app-%s:\n      rule: \"%s\"\n      priority: 2000\n      service: app-%s\n      middlewares: [%s%s]\n",
			a.ID, prefixRule(a.Path), a.ID, a.Chain(a.Auth), strip)
		for j, p := range a.Public {
			fmt.Fprintf(&b, "    app-%s-public-%d:\n      rule: \"%s\"\n      priority: 3000\n      service: app-%s\n      middlewares: [%s%s]\n",
				a.ID, j, prefixRule(a.Path+p), a.ID, a.Chain("none"), strip)
		}
	}
	b.WriteString("  middlewares:\n")
	for _, a := range apps {
		if a.Strips() {
			fmt.Fprintf(&b, "    app-%s-strip:\n      stripPrefix:\n        prefixes: [\"%s\"]\n", a.ID, a.Path)
		}
	}
	b.WriteString("  services:\n")
	for _, a := range apps {
		fmt.Fprintf(&b, "    app-%s:\n      loadBalancer:\n        servers:\n          - url: \"http://127.0.0.1:%d\"\n", a.ID, ports[a.ID])
	}
	return ports, os.WriteFile(filepath.Join(dyn, "apps.yml"), []byte(b.String()), 0o644)
}

// freePort asks the OS for an unused port. Fixed ports clash on Windows, which reserves ranges
// for Hyper-V and WSL that change between reboots.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// prefixRule matches p and everything under it, but not p followed by other characters.
func prefixRule(p string) string {
	return fmt.Sprintf("Path(`%s`) || PathPrefix(`%s/`)", p, p)
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func waitHTTP(ctx context.Context, url string) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s not reachable after 20s", url)
}

// group runs child processes with prefixed output and stops them together.
type group struct {
	mu    sync.Mutex
	procs []*exec.Cmd
	exits chan string
}

func (g *group) start(ctx context.Context, name, bin string, args []string, dir string, env map[string]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.exits == nil {
		g.exits = make(chan string, 16)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out := &prefixWriter{prefix: fmt.Sprintf("%-14s| ", name)}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start %s: %v\n", name, err)
		g.exits <- name
		return
	}
	g.procs = append(g.procs, cmd)
	go func() {
		err := cmd.Wait()
		if ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "%s exited: %v\n", name, err)
		}
		g.exits <- name
	}()
}

func (g *group) firstExit(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-g.exits:
		}
		close(done)
	}()
	return done
}

func (g *group) stopAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.procs {
		if p.ProcessState == nil && p.Process != nil {
			_ = p.Process.Kill()
		}
	}
	step("stopped")
}

// prefixWriter prefixes each line of a child's output with its name.
type prefixWriter struct {
	mu     sync.Mutex
	prefix string
	buf    []byte
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		os.Stdout.Write(append([]byte(w.prefix), w.buf[:i+1]...))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}
