// Command core runs the platform's identity service. Configuration comes from the environment;
// see the README and the env() calls below.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/server"
	"github.com/arnav127/iima-platform/core/store"
	"github.com/arnav127/iima-platform/core/token"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("core stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	publicURL, err := url.Parse(env("PUBLIC_URL", "http://localhost:8080"))
	if err != nil || publicURL.Host == "" {
		return fmt.Errorf("PUBLIC_URL must be a URL like https://students.iima.ac.in")
	}
	tokenTTL, err := time.ParseDuration(env("TOKEN_TTL", "5m"))
	if err != nil {
		return fmt.Errorf("TOKEN_TTL: %w", err)
	}
	sessionTTL, err := time.ParseDuration(env("SESSION_TTL", "720h"))
	if err != nil {
		return fmt.Errorf("SESSION_TTL: %w", err)
	}

	devLogin := os.Getenv("CORE_DEV_LOGIN") == "1"
	if devLogin && publicURL.Hostname() != "localhost" && publicURL.Hostname() != "127.0.0.1" {
		return errors.New("CORE_DEV_LOGIN lets anyone sign in as anyone; it only runs with a localhost PUBLIC_URL")
	}

	reg, err := registry.LoadDirs(strings.Split(env("REGISTRY", "apps"), ",")...)
	if err != nil {
		return err
	}

	dataDir := env("CORE_DATA", "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "core.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	signer, err := token.Load(ctx, st, env("TOKEN_ISSUER", "students.iima.ac.in"), tokenTTL)
	if err != nil {
		return err
	}

	var provider server.Provider
	if id, secret := os.Getenv("GOOGLE_CLIENT_ID"), os.Getenv("GOOGLE_CLIENT_SECRET"); id != "" && secret != "" {
		g, err := server.NewGoogle(ctx, id, secret, publicURL.JoinPath("/auth/callback").String())
		if err != nil {
			return err
		}
		provider = g
	} else if !devLogin {
		log.Warn("GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET not set and dev login is off: nobody can sign in")
	}
	if devLogin {
		log.Warn("DEV LOGIN IS ON: anyone can sign in as any email. Never use this outside your laptop.")
	}

	srv := server.New(server.Config{
		PublicURL: publicURL, MemberDomain: env("MEMBER_DOMAIN", "iima.ac.in"), SessionTTL: sessionTTL,
		DevLogin: devLogin, Registry: reg, Store: st, Signer: signer, Provider: provider, Log: log,
	})
	go srv.Cleanup(ctx, 10*time.Minute)

	addr := env("CORE_ADDR", "127.0.0.1:4180")
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	log.Info("core listening", "addr", addr, "public_url", publicURL.String(), "apps", len(reg.Apps()), "google", provider != nil, "dev_login", devLogin)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
