package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arnav127/iima-platform/core/github/githubtest"
)

// The deploy portal on a laptop (-nomad -portal): a fake GitHub that has the platform's App
// installed on two repos, whose first releases are the two app templates built for this machine
// and packed the way the build workflow packs them. Core gets the App's credentials and Nomad, so
// the whole flow runs locally: connect a repo at /admin/apps/new as you@iima.ac.in, approve it as
// admin@iima.ac.in, and it deploys.
const (
	demoRepo     = "iima-clubs/myapp"     // template/go-pocketbase, at /myapp
	demoNextRepo = "iima-clubs/mynextapp" // template/next, at /mynextapp
	demoOwner    = "you@iima.ac.in"
	demoSuper    = "admin@iima.ac.in"
)

func portalDemo(s nomadSetup, cluster string) (env map[string]string, stop func(), err error) {
	gh := githubtest.New()
	fail := func(err error) (map[string]string, func(), error) {
		gh.Close()
		return nil, nil, err
	}
	for repo, build := range map[string]func(nomadSetup) ([]byte, string, error){demoRepo: buildGoTemplate, demoNextRepo: buildNextTemplate} {
		step("building " + repo + "'s first release")
		artifact, dir, err := build(s)
		if err != nil {
			return fail(err)
		}
		toml, err := os.ReadFile(filepath.Join(dir, "app.toml"))
		if err != nil {
			return fail(err)
		}
		inst := int64(1)
		if repo == demoNextRepo {
			inst = 2
		}
		gh.AddRepo(repo, inst, map[string][]byte{"app.toml": toml})
		gh.AddRelease(repo, "build-0000001", strings.Repeat("0", 39)+"1", artifact)
	}

	keyFile := filepath.Join(s.dev, "github-app.pem")
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(gh.Key)})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return fail(err)
	}
	clusterFile := filepath.Join(s.dev, "cluster.toml")
	if err := os.WriteFile(clusterFile, []byte(cluster), 0o600); err != nil {
		return fail(err)
	}

	// Super admin for approving, before core starts.
	cmd := exec.Command(s.coreBin, "superadmin", "add", demoSuper)
	cmd.Env = append(os.Environ(), "CORE_DATA="+filepath.Join(s.dev, "core-data"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fail(fmt.Errorf("superadmin add: %v: %s", err, out))
	}

	return map[string]string{
		"GITHUB_APP_ID":               fmt.Sprint(githubtest.AppID),
		"GITHUB_APP_SLUG":             "iima-platform-dev",
		"GITHUB_APP_PRIVATE_KEY_FILE": keyFile,
		"GITHUB_API_URL":              gh.URL,
		"GITHUB_WEB_URL":              gh.URL,
		"NOMAD_ADDR":                  nomadAPI,
		"CLUSTER_CONFIG":              clusterFile,
		"PORTAL_SYNC":                 "15s",
	}, gh.Close, nil
}

// buildGoTemplate builds template/go-pocketbase: ./app (./app.exe on Windows) and ./pb_public.
func buildGoTemplate(s nomadSetup) ([]byte, string, error) {
	tmpl := filepath.Join(s.root, "template", "go-pocketbase")
	if err := runIn(tmpl, []string{"VITE_BASE=/myapp/"}, "npm", "run", "build"); err != nil {
		return nil, "", fmt.Errorf("template frontend (npm install in %s first?): %w", tmpl, err)
	}
	bin := filepath.Join(s.bin, exe("myapp"))
	if err := goBuild(filepath.Join(tmpl, "backend"), bin, "."); err != nil {
		return nil, "", err
	}
	b, err := pack([]entry{{exe("app"), bin}, {"pb_public", filepath.Join(tmpl, "backend", "pb_public")}})
	return b, tmpl, err
}

// buildNextTemplate builds template/next like the workflow's Next.js steps: Next's standalone
// server under ./app (with an entry file when Next nests it), its static files, and the platform's
// PocketBase with the app's migrations.
func buildNextTemplate(s nomadSetup) ([]byte, string, error) {
	tmpl := filepath.Join(s.root, "template", "next")
	if err := runIn(tmpl, []string{"NEXT_TELEMETRY_DISABLED=1"}, "npm", "run", "build"); err != nil {
		return nil, "", fmt.Errorf("Next template (npm install in %s first?): %w", tmpl, err)
	}
	standalone := filepath.Join(tmpl, ".next", "standalone")
	sub := ""
	err := filepath.WalkDir(standalone, func(p string, d fs.DirEntry, err error) error {
		if err != nil || sub != "" {
			return err
		}
		if d.IsDir() && d.Name() == "node_modules" {
			return filepath.SkipDir
		}
		if d.Name() == "server.js" {
			rel, _ := filepath.Rel(standalone, filepath.Dir(p))
			sub = filepath.ToSlash(rel)
		}
		return nil
	})
	if err != nil || sub == "" {
		return nil, "", fmt.Errorf("no server.js in %s (output: 'standalone' in next.config?)", standalone)
	}
	pbBin := filepath.Join(s.bin, exe("pocketbase"))
	if err := goBuildAlone(filepath.Join(s.root, "pocketbase"), pbBin, "."); err != nil {
		return nil, "", err
	}
	entries := []entry{
		{"app", standalone},
		{"app/" + sub + "/.next/static", filepath.Join(tmpl, ".next", "static")},
		{"app/" + sub + "/public", filepath.Join(tmpl, "public")},
		{exe("pocketbase"), pbBin},
		{"pb_migrations", filepath.Join(tmpl, "pb_migrations")},
	}
	if sub != "." {
		entries = append(entries, entry{"app/server.js", "require('./" + sub + "/server.js')\n"})
	}
	b, err := pack(entries)
	return b, tmpl, err
}

// entry is one thing in the artifact: a file or directory from disk, or (when src does not exist
// as a path) the literal content of a small file.
type entry struct{ dest, src string }

func pack(entries []entry) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, b []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(b))}); err != nil {
			return err
		}
		_, err := tw.Write(b)
		return err
	}
	for _, e := range entries {
		st, err := os.Stat(e.src)
		if err != nil {
			if strings.Contains(e.src, "\n") { // literal content
				if err := add(e.dest, 0o644, []byte(e.src)); err != nil {
					return nil, err
				}
				continue
			}
			if os.IsNotExist(err) {
				continue // an optional folder, like public/
			}
			return nil, err
		}
		if !st.IsDir() {
			b, err := os.ReadFile(e.src)
			if err != nil {
				return nil, err
			}
			if err := add(e.dest, 0o755, b); err != nil {
				return nil, err
			}
			continue
		}
		err = filepath.WalkDir(e.src, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() {
				return err
			}
			rel, _ := filepath.Rel(e.src, p)
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return add(e.dest+"/"+filepath.ToSlash(rel), 0o644, b)
		})
		if err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func runIn(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v\n%s", err, out)
	}
	return nil
}
