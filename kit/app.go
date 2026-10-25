package iimakit

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"
)

// Prefix is the app's public path ("/demo") when the gateway strips it before forwarding, read
// from X-Forwarded-Prefix. Empty when the app sees the full path. Only a single clean segment is
// accepted, so a stray header cannot turn links into another site.
func Prefix(r *http.Request) string {
	p := r.Header.Get("X-Forwarded-Prefix")
	if len(p) < 2 || p[0] != '/' || strings.ContainsAny(p[1:], "/\\?#") || path.Clean(p) != p {
		return ""
	}
	return p
}

// OriginalURI is the path and query the browser asked for, including the app's prefix.
func OriginalURI(r *http.Request) string {
	return Prefix(r) + r.URL.RequestURI()
}

// SameOrigin reports whether a request came from a page on this site. Use it before acting on
// state-changing requests (POST, PUT, PATCH, DELETE): with the session in a cookie, a page on
// another site could otherwise make the browser send one. Requests without Origin and
// Sec-Fetch-Site come from non-browser clients, which have no platform cookie to abuse.
func SameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

// Unsafe reports whether the method can change state.
func Unsafe(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// DevClaims returns a fake signed-in user from IIMA_DEV_USER (an email) and IIMA_DEV_ROLES
// (comma-separated), so an app can be worked on without the platform running. Returns nil when
// IIMA_DEV_USER is unset, and always on the cluster (NOMAD_ALLOC_ID set), so it cannot be left on
// by accident in production.
func DevClaims(appID string) *Claims {
	email := strings.ToLower(strings.TrimSpace(os.Getenv("IIMA_DEV_USER")))
	if email == "" || os.Getenv("NOMAD_ALLOC_ID") != "" {
		return nil
	}
	name, _, _ := strings.Cut(email, "@")
	var roles []string
	for _, r := range strings.Split(os.Getenv("IIMA_DEV_ROLES"), ",") {
		if r = strings.TrimSpace(r); r != "" {
			roles = append(roles, r)
		}
	}
	now := time.Now()
	return &Claims{
		V: ClaimsVersion, Issuer: "dev", Audience: appID, Subject: "dev_" + email, Session: "dev",
		IssuedAt: now.Unix(), Expiry: now.Add(time.Hour).Unix(),
		Email: email, Name: name, Provider: "dev", Member: strings.HasSuffix(email, "@iima.ac.in"), Roles: roles,
	}
}

// Health answers the platform's health checks.
func Health() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
}

// SetupLogging switches the default logger to JSON lines on stderr, which the cluster ships to Loki.
// Set LOG_LEVEL=debug for more detail.
func SetupLogging() *slog.Logger {
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(os.Getenv("LOG_LEVEL")))
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	return log
}

// SPA serves a built single-page app from fsys, falling back to index.html for app routes.
// Hashed files under assets/ are cached for a year; HTML and sw.js are revalidated every time.
// Compression is left to the gateway.
func SPA(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if st, err := fs.Stat(fsys, p); err != nil || st.IsDir() {
			if path.Ext(p) != "" {
				http.NotFound(w, r) // a missing file, not an app route
				return
			}
			p = "index.html"
		}
		setCacheHeaders(w, p)
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + p
		if p == "index.html" {
			// FileServer and ServeFileFS redirect .../index.html to .../; serve the bytes instead.
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r2, p, time.Time{}, bytes.NewReader(b))
			return
		}
		files.ServeHTTP(w, r2)
	})
}

// CacheControl returns the Cache-Control value SPA uses for a file path (relative, no leading /).
func CacheControl(p string) string {
	switch {
	case strings.HasPrefix(p, "assets/"):
		return "public, max-age=31536000, immutable"
	case strings.HasSuffix(p, ".html"), path.Base(p) == "sw.js", path.Ext(p) == "":
		return "no-cache"
	}
	return "public, max-age=3600"
}

func setCacheHeaders(w http.ResponseWriter, p string) {
	w.Header().Set("Cache-Control", CacheControl(p))
}

// Serve runs h on Addr() until SIGINT or SIGTERM, then gives requests in flight 10 seconds to finish.
func Serve(h http.Handler) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: Addr(), Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("listening", "addr", srv.Addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
