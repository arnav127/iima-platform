package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// startNext runs a Next.js app the way the cluster will: `next start` on its routed port, keeping
// its base path, with its own PocketBase on loopback that only the Next server talks to (as a
// superuser), and its Python backend beside it when app.toml has backend = "...". The app must be
// built first (npm run build in its repo).
func startNext(ctx context.Context, g *group, r realApp, pbBin, data string, port int, coreAddr string) error {
	web := r.repo
	if _, err := os.Stat(filepath.Join(r.repo, "web", "package.json")); err == nil {
		web = filepath.Join(r.repo, "web") // Next app in a subfolder (Portfolio League)
	}
	nextBin := filepath.Join(web, "node_modules", "next", "dist", "bin", "next")
	if _, err := os.Stat(filepath.Join(web, ".next", "BUILD_ID")); err != nil {
		return fmt.Errorf("%s: build it first (npm run build in %s)", r.app.ID, web)
	}
	if _, err := os.Stat(nextBin); err != nil {
		return fmt.Errorf("%s: npm install first in %s", r.app.ID, web)
	}
	env, err := readDotenv(filepath.Join(web, ".env"))
	if err != nil {
		return err
	}
	jwks := "http://" + coreAddr + "/auth/.well-known/jwks.json"
	appEnv := map[string]string{"IIMA_JWKS_URL": jwks, "APP_URL": "http://localhost:8080" + r.app.Path}

	if r.app.PocketBase || r.app.Shape == "next" && dirExists(filepath.Join(r.repo, "pb_migrations")) {
		pbAddr, email, password, err := startPocketBase(ctx, g, r, pbBin, data)
		if err != nil {
			return err
		}
		appEnv["POCKETBASE_URL"], appEnv["PB_ADMIN_EMAIL"], appEnv["PB_ADMIN_PASSWORD"] = "http://"+pbAddr, email, password
	}

	if r.app.Backend != "" {
		be := filepath.Join(r.repo, "backend")
		bePort, err := freePort()
		if err != nil {
			return err
		}
		py := pythonCmd()
		for _, venv := range []string{filepath.Join(be, "venv", "Scripts", "python.exe"), filepath.Join(be, ".venv", "Scripts", "python.exe"), filepath.Join(be, "venv", "bin", "python"), filepath.Join(be, ".venv", "bin", "python")} {
			if _, err := os.Stat(venv); err == nil {
				py = venv
				break
			}
		}
		beEnv, err := readDotenv(filepath.Join(be, ".env"))
		if err != nil {
			return err
		}
		addr := fmt.Sprintf("127.0.0.1:%d", bePort)
		g.start(ctx, r.app.ID+"-backend", py, []string{"-m", "uvicorn", r.app.Backend, "--host", "127.0.0.1", "--port", fmt.Sprint(bePort)}, be, merge(beEnv, appEnv))
		appEnv["BACKEND_URL"] = "http://" + addr
	}

	g.start(ctx, r.app.ID, "node", []string{nextBin, "start", "-p", fmt.Sprint(port), "-H", "127.0.0.1"}, web, merge(env, appEnv))
	return nil
}

// startPocketBase runs the platform's PocketBase on loopback with the app's migrations and hooks (in
// the repo's root or its pocketbase/ folder) and a throwaway superuser.
func startPocketBase(ctx context.Context, g *group, r realApp, pbBin, data string) (addr, email, password string, err error) {
	pbPort, err := freePort()
	if err != nil {
		return "", "", "", err
	}
	addr = fmt.Sprintf("127.0.0.1:%d", pbPort)
	// A fresh superuser password every run: it only ever lives in these processes' environments.
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	email, password = "devstack@localhost.test", hex.EncodeToString(b)

	args := []string{"--dir", data}
	for _, base := range []string{r.repo, filepath.Join(r.repo, "pocketbase")} {
		for flag, dir := range map[string]string{"--migrationsDir": "pb_migrations", "--hooksDir": "pb_hooks"} {
			if dirExists(filepath.Join(base, dir)) {
				args = append(args, flag, filepath.Join(base, dir))
			}
		}
	}
	// Migrations run on serve too, but upsert needs the schema in place first.
	for _, pre := range [][]string{{"migrate", "up"}, {"superuser", "upsert", email, password}} {
		cmd := exec.CommandContext(ctx, pbBin, append(pre, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", "", "", fmt.Errorf("%s pocketbase %s: %v\n%s", r.app.ID, pre[0], err, out)
		}
	}
	g.start(ctx, r.app.ID+"-pb", pbBin, append([]string{"serve", "--http", addr}, args...), r.repo, nil)
	if err := waitHTTP(ctx, "http://"+addr+"/api/health"); err != nil {
		return "", "", "", errors.Join(fmt.Errorf("%s: PocketBase did not start", r.app.ID), err)
	}
	return addr, email, password, nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
