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
	"runtime"
	"strings"

	"github.com/arnav127/iima-platform/core/github/githubtest"
)

// The deploy portal on a laptop (-nomad -portal): a fake GitHub that has the platform's App
// installed on one repo, iima-clubs/myapp, whose release is the app template built for this machine.
// Core gets the App's credentials and Nomad, so the whole flow runs locally: connect the repo at
// /admin/apps/new as you@iima.ac.in, approve it as admin@iima.ac.in, and it deploys.
const (
	demoRepo  = "iima-clubs/myapp"
	demoOwner = "you@iima.ac.in"
	demoSuper = "admin@iima.ac.in"
)

func portalDemo(s nomadSetup, cluster string) (env map[string]string, stop func(), err error) {
	tmpl := filepath.Join(s.root, "template", "go-pocketbase")
	step("building the app template as the demo repo's first release")
	artifact, err := buildTemplate(s, tmpl)
	if err != nil {
		return nil, nil, err
	}
	toml, err := os.ReadFile(filepath.Join(tmpl, "app.toml"))
	if err != nil {
		return nil, nil, err
	}

	gh := githubtest.New()
	gh.AddRepo(demoRepo, 1, map[string][]byte{"app.toml": toml})
	gh.AddRelease(demoRepo, "build-0000001", strings.Repeat("0", 39)+"1", artifact)

	keyFile := filepath.Join(s.dev, "github-app.pem")
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(gh.Key)})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		gh.Close()
		return nil, nil, err
	}
	clusterFile := filepath.Join(s.dev, "cluster.toml")
	if err := os.WriteFile(clusterFile, []byte(cluster), 0o600); err != nil {
		gh.Close()
		return nil, nil, err
	}

	// Super admin for approving, before core starts.
	cmd := exec.Command(s.coreBin, "superadmin", "add", demoSuper)
	cmd.Env = append(os.Environ(), "CORE_DATA="+filepath.Join(s.dev, "core-data"))
	if out, err := cmd.CombinedOutput(); err != nil {
		gh.Close()
		return nil, nil, fmt.Errorf("superadmin add: %v: %s", err, out)
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

// buildTemplate builds the template's frontend and backend for this machine and packs them the way
// the platform's build workflow does: ./app (./app.exe on Windows) and ./pb_public.
func buildTemplate(s nomadSetup, tmpl string) ([]byte, error) {
	npm := exec.Command("npm", "run", "build")
	npm.Dir = tmpl
	npm.Env = append(os.Environ(), "VITE_BASE=/myapp/")
	if out, err := npm.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("template frontend (npm install in %s first?): %v\n%s", tmpl, err, out)
	}
	bin := filepath.Join(s.bin, exe("myapp"))
	if err := goBuild(filepath.Join(tmpl, "backend"), bin, "."); err != nil {
		return nil, err
	}

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
	b, err := os.ReadFile(bin)
	if err != nil {
		return nil, err
	}
	name := "app"
	if runtime.GOOS == "windows" {
		name = "app.exe"
	}
	if err := add(name, 0o755, b); err != nil {
		return nil, err
	}
	public := filepath.Join(tmpl, "backend", "pb_public")
	err = filepath.WalkDir(public, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(public, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return add("pb_public/"+filepath.ToSlash(rel), 0o644, data)
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
