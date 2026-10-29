// Command core runs the platform's identity service. Configuration comes from the environment;
// see the README and the env() calls below.
//
//	core                                      serve
//	core superadmin add|remove EMAIL          bootstrap or change super admins
//	core superadmin list
//	core roles set APP EMAIL [ROLE...]        no roles removes them all
//	core roles list APP
//
// The admin commands use the same CORE_DATA and REGISTRY as the server, and are logged in the
// audit log as "cli".
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/arnav127/iima-platform/core/portal"
	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/server"
	"github.com/arnav127/iima-platform/core/store"
	"github.com/arnav127/iima-platform/core/token"
)

func main() {
	if len(os.Args) > 1 {
		if err := command(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "core:", err)
			os.Exit(1)
		}
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("core stopped", "err", err)
		os.Exit(1)
	}
}

const usage = `usage:
  core                                 serve
  core superadmin add|remove EMAIL
  core superadmin list
  core roles set APP EMAIL [ROLE...]
  core roles list APP`

// command runs one admin command against the database, without starting the server.
func command(args []string) error {
	ctx := context.Background()
	if err := os.MkdirAll(env("CORE_DATA", "data"), 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(env("CORE_DATA", "data"), "core.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	switch {
	case len(args) == 2 && args[0] == "superadmin" && args[1] == "list":
		admins, err := st.SuperAdmins(ctx)
		if err != nil {
			return err
		}
		for _, a := range admins {
			fmt.Println(a.Email)
		}
		return nil
	case len(args) == 3 && args[0] == "superadmin" && (args[1] == "add" || args[1] == "remove"):
		email := store.NormEmail(args[2])
		if !strings.Contains(email, "@") {
			return fmt.Errorf("not an email: %s", args[2])
		}
		if args[1] == "add" {
			err = st.AddSuperAdmin(ctx, "cli", email)
		} else {
			err = st.RemoveSuperAdmin(ctx, "cli", email)
		}
		if err == nil {
			fmt.Printf("%s: %s %s\n", args[1], email, "done")
		}
		return err
	case len(args) >= 3 && args[0] == "roles":
		files, err := registry.LoadDirs(strings.Split(env("REGISTRY", "apps"), ",")...)
		if err != nil {
			return err
		}
		// Approved portal apps too (no GitHub or Nomad needed to read them).
		p, err := portal.New(ctx, portal.Config{Store: st, FileApps: files.Apps()})
		if err != nil {
			return err
		}
		reg := p.Registry()
		app := reg.ByID(args[2])
		if app == nil {
			return fmt.Errorf("no app %q in the registry", args[2])
		}
		switch {
		case args[1] == "list" && len(args) == 3:
			grants, err := st.Grants(ctx, app.ID)
			if err != nil {
				return err
			}
			for _, g := range grants {
				fmt.Printf("%s\t%s\n", g.Email, strings.Join(g.Roles, ","))
			}
			return nil
		case args[1] == "set" && len(args) >= 4:
			email := store.NormEmail(args[3])
			for _, r := range args[4:] {
				if !slices.Contains(app.Roles, r) {
					return fmt.Errorf("role %q is not declared in %s's app.toml (declared: %s)", r, app.ID, strings.Join(app.Roles, ", "))
				}
			}
			if err := st.SetRoles(ctx, "cli", app.ID, email, args[4:]); err != nil {
				return err
			}
			fmt.Printf("%s in %s: %s\n", email, app.ID, strings.Join(args[4:], ","))
			return nil
		}
	}
	return errors.New(usage)
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

	// Under Nomad: the allocated address (joined properly, as it may be IPv6). Otherwise CORE_ADDR.
	addr := env("CORE_ADDR", "127.0.0.1:4180")
	if ip, port := os.Getenv("NOMAD_IP_http"), os.Getenv("NOMAD_PORT_http"); ip != "" && port != "" && os.Getenv("CORE_ADDR") == "" {
		addr = net.JoinHostPort(ip, port)
	}

	p, err := newPortal(ctx, log, st, reg, dataDir, addr)
	if err != nil {
		return err
	}
	every, err := time.ParseDuration(env("PORTAL_SYNC", "2m"))
	if err != nil {
		return fmt.Errorf("PORTAL_SYNC: %w", err)
	}
	go p.Run(ctx, every)

	srv := server.New(server.Config{
		PublicURL: publicURL, MemberDomain: env("MEMBER_DOMAIN", "iima.ac.in"), SessionTTL: sessionTTL,
		DevLogin: devLogin, Registry: reg, Portal: p, Store: st, Signer: signer, Provider: provider, Log: log,
	})
	go srv.Cleanup(ctx, 10*time.Minute)
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	log.Info("core listening", "addr", addr, "public_url", publicURL.String(), "apps", len(p.Registry().Apps()), "google", provider != nil, "dev_login", devLogin)
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
