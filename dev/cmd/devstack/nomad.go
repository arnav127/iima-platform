package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/arnav127/iima-platform/core/jobgen"
	"github.com/arnav127/iima-platform/core/registry"
)

// Nomad is pinned like Traefik. To upgrade, copy the hashes from nomad_<version>_SHA256SUMS.
const nomadVersion = "2.0.7"

var nomadSHA256 = map[string]string{
	"windows_amd64": "15bd49bd834ace9ce27bfad8e69461978f3f5b483a2e92ae71a8c310d3503e68",
	"linux_amd64":   "4c9b8a0850d6fd9caadbbab09b3e6fdf8b77aa777729543c70c61b85acca68c1",
	"darwin_arm64":  "4ada34db43f8b75c80c4e09ce50f4753e661bcb4a3745f415dd4aa7d01483e58",
}

const nomadAPI = "http://127.0.0.1:4646"

type nomadSetup struct {
	root, dev, bin, tdir string
	dotenv               map[string]string
	googleOnly           bool
	traefik              string
	coreBin, demoBin     string
	devApps              []*registry.App
	reals                []realApp
}

// runNomad runs the cluster's layout on one machine: a Nomad dev agent, core and every app as
// Nomad jobs (raw_exec, since exec is Linux only), Traefik reading app routes from Nomad and
// reaching core through its internal entry point. App jobs come from jobgen, as on the cluster.
func runNomad(s nomadSetup) error {
	platform := runtime.GOOS + "_" + runtime.GOARCH
	want, ok := nomadSHA256[platform]
	if !ok {
		return fmt.Errorf("no pinned Nomad build for %s; add it to nomadSHA256", platform)
	}
	nomad, err := fetchPinned(s.bin, "nomad", nomadVersion,
		fmt.Sprintf("https://releases.hashicorp.com/nomad/%s/nomad_%s_%s.zip", nomadVersion, nomadVersion, platform), want)
	if err != nil {
		return err
	}
	if _, err := prepareTraefik(s.root, s.tdir, "traefik.local-nomad.yml", nil); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var g group

	dataDir := filepath.Join(s.dev, "nomad")
	if err := os.RemoveAll(dataDir); err != nil { // a dev agent starts empty every time
		return err
	}
	g.start(ctx, "nomad", nomad, []string{"agent", "-dev", "-bind=127.0.0.1", "-log-level=WARN", "-data-dir=" + dataDir}, s.root, nil)
	if err := waitOK(ctx, nomadAPI+"/v1/agent/health", 60*time.Second); err != nil {
		g.stopAll()
		return fmt.Errorf("nomad did not start: %w", err)
	}
	g.start(ctx, "traefik", s.traefik, []string{"--configFile=traefik.yml"}, s.tdir, map[string]string{"IIMA_CORE_URL": "http://127.0.0.1:8081"})

	var jobs []*jobgen.Job
	jobs = append(jobs, coreJob(s))
	cluster := jobgen.Cluster{
		Datacenter: "dc1", Driver: "raw_exec", PublicURL: "http://localhost:8080",
		JWKSURL: "http://127.0.0.1:8081/auth/.well-known/jwks.json", LocalDataRoot: filepath.Join(s.dev, "data"),
	}
	for _, a := range s.devApps {
		j, err := jobgen.Build(cluster, a, jobgen.Release{Version: "dev", Command: s.demoBin, Env: map[string]string{"APP_ID": a.ID}})
		if err != nil {
			g.stopAll()
			return err
		}
		jobs = append(jobs, j)
	}
	for _, r := range s.reals {
		j, err := jobgen.Build(cluster, r.app, jobgen.Release{Version: "dev", Command: r.bin, PublicDir: filepath.Join(r.repo, "backend", "pb_public")})
		if err != nil {
			g.stopAll()
			return err
		}
		jobs = append(jobs, j)
	}

	stopJobs := func() {
		for _, j := range jobs {
			req, _ := http.NewRequest(http.MethodDelete, nomadAPI+"/v1/job/"+j.ID+"?purge=true", nil)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		}
		time.Sleep(3 * time.Second) // let Nomad stop the tasks before the agent goes
		g.stopAll()
	}

	for _, j := range jobs {
		step("submitting job " + j.ID)
		if err := submit(j); err != nil {
			stopJobs()
			return err
		}
	}

	// Ready when Traefik reaches core through Nomad, and each app through its job's tags.
	if err := waitOK(ctx, "http://localhost:8080/", 90*time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "core not reachable through Traefik yet:", err)
	}
	routed := append([]*registry.App{}, s.devApps...)
	for _, r := range s.reals {
		routed = append(routed, r.app)
	}
	for _, a := range routed {
		health := a.Path + "/health"
		if a.Shape == "go-pocketbase" {
			health = a.Path + "/api/health"
		}
		if err := waitOK(ctx, "http://localhost:8080"+health, 90*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "%s not reachable through Traefik yet: %v\n", a.ID, err)
		}
	}
	fmt.Println()
	step("ready (Nomad mode): http://localhost:8080/")
	for _, a := range routed {
		fmt.Printf("    http://localhost:8080%s/   (%s)\n", a.Path, a.Name)
	}
	fmt.Println("    Nomad UI: http://127.0.0.1:4646/ui   Logs: .dev\\bin\\nomad alloc logs -job <id>")
	fmt.Println("    Ctrl+C to stop.")

	<-g.firstExit(ctx)
	stopJobs()
	return nil
}

// coreJob is the local twin of deploy/nomad/core.nomad.hcl: the binary in place, raw_exec,
// data in .dev/core-data, dev sign-in on.
func coreJob(s nomadSetup) *jobgen.Job {
	env := map[string]string{
		"PUBLIC_URL": "http://localhost:8080",
		"CORE_DATA":  filepath.Join(s.dev, "core-data"),
		"REGISTRY":   filepath.Join(s.root, "apps") + "," + filepath.Join(s.root, "dev", "apps"),
	}
	if !s.googleOnly {
		env["CORE_DEV_LOGIN"] = "1"
	}
	for k, v := range s.dotenv {
		env[k] = v
	}
	return &jobgen.Job{
		ID: "core", Name: "core", Type: "service", Datacenters: []string{"dc1"},
		TaskGroups: []*jobgen.TaskGroup{{
			Name: "core", Count: 1,
			Networks: []*jobgen.Network{{DynamicPorts: []jobgen.Port{{Label: "http"}}}},
			Services: []*jobgen.Service{{
				Name: "core", Provider: "nomad", PortLabel: "http",
				Tags: []string{
					"traefik.enable=true",
					"traefik.http.routers.core-internal.entrypoints=internal",
					"traefik.http.routers.core-internal.rule=PathPrefix(`/`)",
					"traefik.http.routers.core-internal.service=core",
				},
				Checks: []*jobgen.Check{{Name: "health", Type: "http", Path: "/health", Interval: int64(10 * time.Second), Timeout: int64(3 * time.Second)}},
			}},
			Tasks: []*jobgen.Task{{Name: "core", Driver: "raw_exec", Config: map[string]any{"command": s.coreBin}, Env: env}},
		}},
	}
}

func submit(j *jobgen.Job) error {
	body, err := json.Marshal(map[string]any{"Job": j})
	if err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodPut, nomadAPI+"/v1/jobs", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var b bytes.Buffer
		b.ReadFrom(resp.Body)
		return fmt.Errorf("nomad refused job %s: %s %s", j.ID, resp.Status, strings.TrimSpace(b.String()))
	}
	return nil
}

// waitOK waits for url to answer 200.
func waitOK(ctx context.Context, url string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	last := "no answer"
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = resp.Status
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s: %s after %s", url, last, limit)
}
