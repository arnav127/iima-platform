// Package platformstrip is a Traefik middleware plugin that removes request headers and cookies
// before a request reaches an app. The gateway uses it twice:
//
//   - before forward auth, to drop any X-User-* and Authorization headers a browser sent, so only
//     the gateway ever sets identity;
//   - after forward auth, to drop the platform session cookie, so apps never see it.
//
// Traefik's built-in headers middleware cannot remove headers by prefix or a single cookie, hence
// this plugin. It runs inside Traefik's Yaegi interpreter, so it uses only the standard library.
package platformstrip

import (
	"context"
	"net/http"
	"strings"
)

// Config is the plugin's configuration in Traefik's dynamic config.
type Config struct {
	Headers        []string `json:"headers,omitempty"`        // exact names, e.g. Authorization
	HeaderPrefixes []string `json:"headerPrefixes,omitempty"` // e.g. X-User-
	Cookies        []string `json:"cookies,omitempty"`        // cookie names, e.g. iima_session
}

// CreateConfig is called by Traefik.
func CreateConfig() *Config { return &Config{} }

type stripper struct {
	next     http.Handler
	headers  []string
	prefixes []string
	cookies  map[string]bool
}

// New is called by Traefik for every middleware instance.
func New(ctx context.Context, next http.Handler, cfg *Config, name string) (http.Handler, error) {
	s := &stripper{next: next, cookies: map[string]bool{}}
	for _, h := range cfg.Headers {
		s.headers = append(s.headers, http.CanonicalHeaderKey(h))
	}
	for _, p := range cfg.HeaderPrefixes {
		s.prefixes = append(s.prefixes, strings.ToLower(p))
	}
	for _, c := range cfg.Cookies {
		s.cookies[c] = true
	}
	return s, nil
}

func (s *stripper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, h := range s.headers {
		r.Header.Del(h)
	}
	if len(s.prefixes) > 0 {
		for name := range r.Header {
			lower := strings.ToLower(name)
			for _, p := range s.prefixes {
				if strings.HasPrefix(lower, p) {
					delete(r.Header, name)
					break
				}
			}
		}
	}
	if len(s.cookies) > 0 {
		s.stripCookies(r)
	}
	s.next.ServeHTTP(w, r)
}

// stripCookies rewrites the Cookie header(s) without the configured cookies, keeping every other
// cookie byte for byte (re-encoding through http.Cookie could change values).
func (s *stripper) stripCookies(r *http.Request) {
	lines := r.Header.Values("Cookie")
	if len(lines) == 0 {
		return
	}
	var kept []string
	for _, line := range lines {
		for _, part := range strings.Split(line, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, _, _ := strings.Cut(part, "=")
			if !s.cookies[strings.TrimSpace(name)] {
				kept = append(kept, part)
			}
		}
	}
	r.Header.Del("Cookie")
	if len(kept) > 0 {
		r.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}
