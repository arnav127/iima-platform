// Package server is core's HTTP side: sign-in, sessions, /auth/verify for the gateway, JWKS,
// /auth/me, and the platform home and 404 pages.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/arnav127/iima-platform/core/portal"
	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/store"
	"github.com/arnav127/iima-platform/core/token"
)

const (
	sessionCookie = "iima_session"
	loginCookie   = "iima_login"
	loginTTL      = 10 * time.Minute
	cacheTTL      = 30 * time.Second
)

// Config is everything the server needs. cmd/core fills it from the environment.
type Config struct {
	PublicURL    *url.URL // e.g. https://students.iima.ac.in, or http://localhost:8080 locally
	MemberDomain string   // "iima.ac.in"
	SessionTTL   time.Duration
	// DevLogin shows a form that signs in as any email without Google. Local stack only:
	// cmd/core refuses to turn it on unless PublicURL is localhost.
	DevLogin bool

	// Registry is the apps from apps/*.toml. With a Portal, the portal's registry (those plus approved
	// portal apps, kept up to date) is used instead.
	Registry *registry.Registry
	Portal   *portal.Portal // the deploy portal; nil turns it off
	Store    *store.Store
	Signer   *token.Signer
	Provider Provider // Google; nil when not configured
	Log      *slog.Logger
}

type Server struct {
	cfg   Config
	cache sessionCache
	ids   identityCache
}

func New(cfg Config) *Server {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Server{cfg: cfg, cache: sessionCache{m: map[string]cacheEntry{}}, ids: identityCache{m: map[string]identityEntry{}}}
}

// Handler returns core's routes. In front of it, the gateway sends /auth/*, /platform/* and the
// catch-all here, and calls /auth/verify for every app request (that path is not public).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", s.handleLogin)
	mux.HandleFunc("POST /auth/login/dev", s.handleDevLogin)
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("GET /auth/logout", s.handleLogoutPage)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	mux.HandleFunc("GET /auth/denied", s.handleDenied)
	mux.HandleFunc("/auth/verify", s.handleVerify)
	mux.HandleFunc("GET /auth/me", s.handleMe)
	mux.HandleFunc("GET /auth/.well-known/jwks.json", s.handleJWKS)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	s.apiRoutes(mux)
	s.portalRoutes(mux)
	s.githubRoutes(mux)
	s.routesRoutes(mux)
	s.adminRoutes(mux)
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /platform/apps", s.handleHome) // the directory, also when a home app owns /
	mux.HandleFunc("GET /platform/theme.js", s.handleThemeJS)
	mux.HandleFunc("GET /platform/logo.svg", s.handleLogo)
	mux.HandleFunc("/", s.handleNotFound)
	return mux
}

// allowed is the login policy: IIMA-only apps need a verified @iima.ac.in Google account.
// registry returns the live set of apps.
func (s *Server) registry() *registry.Registry {
	if s.cfg.Portal != nil {
		return s.cfg.Portal.Registry()
	}
	return s.cfg.Registry
}

func allowed(u store.User, a *registry.App) bool {
	return a.Login == registry.LoginExternal || u.Member
}

// isMember decides membership at sign-in. For Google, hd is the account's Workspace domain and is
// only present for Workspace accounts; the email suffix alone is not enough.
func (s *Server) isMember(email, hostedDomain string) bool {
	d := s.cfg.MemberDomain
	return strings.EqualFold(hostedDomain, d) && strings.HasSuffix(strings.ToLower(email), "@"+strings.ToLower(d))
}

// --- sessions ---

type cacheEntry struct {
	sess *store.Session
	at   time.Time
}

// sessionCache keeps recently seen sessions for cacheTTL, because /auth/verify runs on every
// request. Only valid sessions are cached, so junk cookies cannot fill it.
type sessionCache struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}

func (c *sessionCache) get(token string) *store.Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[token]
	if !ok || time.Since(e.at) > cacheTTL || time.Now().After(e.sess.ExpiresAt) {
		delete(c.m, token)
		return nil
	}
	return e.sess
}

func (c *sessionCache) put(token string, sess *store.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 50_000 {
		for k, e := range c.m {
			if time.Since(e.at) > cacheTTL {
				delete(c.m, k)
			}
		}
	}
	c.m[token] = cacheEntry{sess, time.Now()}
}

func (c *sessionCache) drop(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, token)
}

// session returns the request's session, or nil when signed out.
func (s *Server) session(r *http.Request) *store.Session {
	ck, err := r.Cookie(sessionCookie)
	if err != nil || ck.Value == "" {
		return nil
	}
	if sess := s.cache.get(ck.Value); sess != nil {
		return sess
	}
	sess, err := s.cfg.Store.Session(r.Context(), ck.Value)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.cfg.Log.Error("session lookup", "err", err)
		}
		return nil
	}
	s.cache.put(ck.Value, sess)
	return sess
}

func (s *Server) secureCookies() bool { return s.cfg.PublicURL.Scheme == "https" }

func (s *Server) setCookie(w http.ResponseWriter, name, value, path string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: int(maxAge.Seconds()),
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: path, MaxAge: -1, HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode})
}

// --- helpers ---

// safeRedirect accepts only same-origin paths ("/x/y?z"), never "//host", "/\host" or absolute
// URLs, so login links cannot be turned into open redirects.
func safeRedirect(rd string) (string, bool) {
	if !strings.HasPrefix(rd, "/") || strings.HasPrefix(rd, "//") || strings.ContainsRune(rd, '\\') || len(rd) > 2048 {
		return "", false
	}
	for _, c := range rd {
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	u, err := url.Parse(rd)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	return rd, true
}

func safeOr(rd, fallback string) string {
	if v, ok := safeRedirect(rd); ok {
		return v
	}
	return fallback
}

func loginURL(rd string) string  { return "/auth/login?rd=" + url.QueryEscape(rd) }
func deniedURL(rd string) string { return "/auth/denied?rd=" + url.QueryEscape(rd) }

// sameOrigin is a CSRF check for POSTs on top of SameSite=Lax. Browsers always send Origin on
// cross-origin POSTs; a missing Origin means a non-browser client, which has no cookie to abuse.
func (s *Server) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	want := s.cfg.PublicURL.Scheme + "://" + s.cfg.PublicURL.Host
	return subtle.ConstantTimeCompare([]byte(o), []byte(want)) == 1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Cleanup deletes expired sessions and logins every interval until ctx is done.
func (s *Server) Cleanup(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.cfg.Store.DeleteExpired(ctx, loginTTL); err != nil {
				s.cfg.Log.Error("cleanup", "err", err)
			}
		}
	}
}
