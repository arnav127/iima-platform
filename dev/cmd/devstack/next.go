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
// superuser). The app must be built first (npm run build in its repo).
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

	pbPort, err := freePort()
	if err != nil {
		return err
	}
	pbAddr := fmt.Sprintf("127.0.0.1:%d", pbPort)
	// A fresh superuser password every run: it only ever lives in the two processes' environments.
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	email, password := "devstack@localhost.test", hex.EncodeToString(b)

	args := []string{"--dir", data}
	for flag, dir := range map[string]string{"--migrationsDir": "pb_migrations", "--hooksDir": "pb_hooks"} {
		if st, err := os.Stat(filepath.Join(r.repo, dir)); err == nil && st.IsDir() {
			args = append(args, flag, filepath.Join(r.repo, dir))
		}
	}
	// Migrations run on serve too, but upsert needs the schema in place first.
	for _, pre := range [][]string{{"migrate", "up"}, {"superuser", "upsert", email, password}} {
		cmd := exec.CommandContext(ctx, pbBin, append(pre, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s pocketbase %s: %v\n%s", r.app.ID, pre[0], err, out)
		}
	}
	g.start(ctx, r.app.ID+"-pb", pbBin, append([]string{"serve", "--http", pbAddr}, args...), r.repo, nil)
	if err := waitHTTP(ctx, "http://"+pbAddr+"/api/health"); err != nil {
		return errors.Join(fmt.Errorf("%s: PocketBase did not start", r.app.ID), err)
	}

	g.start(ctx, r.app.ID, "node", []string{nextBin, "start", "-p", fmt.Sprint(port), "-H", "127.0.0.1"}, web, merge(env, map[string]string{
		"IIMA_JWKS_URL":     "http://" + coreAddr + "/auth/.well-known/jwks.json",
		"POCKETBASE_URL":    "http://" + pbAddr,
		"PB_ADMIN_EMAIL":    email,
		"PB_ADMIN_PASSWORD": password,
		"APP_URL":           "http://localhost:8080" + r.app.Path,
	}))
	return nil
}
