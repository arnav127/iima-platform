package iimakit

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type ctxKey struct{}

// Middleware verifies X-User-Token on every request and makes the user available through User(r).
// A missing or invalid token means "signed out"; the request still goes through, so handlers
// decide with RequireUser and RequireRole. This matches the gateway's recommended auth-optional mode.
func Middleware(appID string) func(http.Handler) http.Handler {
	return VerifierMiddleware(NewVerifier(appID))
}

// VerifierMiddleware is Middleware with a Verifier you configured yourself.
func VerifierMiddleware(v *Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get(TokenHeader)
			if raw == "" {
				next.ServeHTTP(w, r)
				return
			}
			c, err := v.Verify(r.Context(), raw)
			if err != nil {
				// Expected now and then (a token minted just before a key rotation); a steady
				// stream means something is misconfigured or someone is probing.
				slog.Warn("identity token rejected", "app", v.AppID, "err", err)
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
		})
	}
}

// User returns the signed-in user, or nil when signed out.
func User(r *http.Request) *Claims {
	c, _ := r.Context().Value(ctxKey{}).(*Claims)
	return c
}

// RequireUser lets only signed-in users through. Browsers are sent to the platform login page and
// come back to the same URL; API calls get 401.
func RequireUser(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if User(r) == nil {
			if wantsHTML(r) {
				http.Redirect(w, r, LoginURL(r.URL.RequestURI()), http.StatusFound)
				return
			}
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// RequireRole lets through only users with role in this app. Signed-out users are handled as in RequireUser.
func RequireRole(role string, h http.Handler) http.Handler {
	return RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !User(r).HasRole(role) {
			writeError(w, http.StatusForbidden, "missing role "+role)
			return
		}
		h.ServeHTTP(w, r)
	}))
}

// LoginURL is the platform login link that returns to rd (a same-origin path) afterwards.
func LoginURL(rd string) string {
	return "/auth/login?rd=" + url.QueryEscape(rd)
}

// LogoutURL is the platform logout page. Logging out needs a POST, so link here rather than
// calling it from code.
const LogoutURL = "/auth/logout"

// Addr is the address the app should listen on: Nomad's NOMAD_IP_http and NOMAD_PORT_http when
// running on the cluster, else ADDR, else 127.0.0.1:PORT, else 127.0.0.1:8090. Apps never bind
// 0.0.0.0, so they are reachable only through the gateway.
func Addr() string {
	if ip, port := os.Getenv("NOMAD_IP_http"), os.Getenv("NOMAD_PORT_http"); ip != "" && port != "" {
		return net.JoinHostPort(ip, port)
	}
	if a := os.Getenv("ADDR"); a != "" {
		return a
	}
	return net.JoinHostPort("127.0.0.1", envOr("PORT", "8090"))
}

func wantsHTML(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
