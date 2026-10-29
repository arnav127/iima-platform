// Package proxyreverse is a Traefik middleware plugin that does what Apache's ProxyPassReverse and
// ProxyPassReverseCookiePath do for an external route: when the upstream answers with a redirect,
// a Content-Location or a cookie that points at itself, the browser gets the public path instead.
//
// With an external route /legacy -> http://10.0.0.5:3010/app:
//
//	Location: http://10.0.0.5:3010/app/login        becomes  /legacy/login
//	Location: https://students.iima.ac.in/app/x     becomes  https://students.iima.ac.in/legacy/x
//	Location: /app/x                                becomes  /legacy/x
//	Set-Cookie: s=1; Path=/app                      becomes  s=1; Path=/legacy
//
// Anything else (another host, a path outside the upstream's) passes untouched. Streaming responses
// keep working: the wrapper passes Flush (server-sent events) and Hijack (websockets) through.
// It runs inside Traefik's Yaegi interpreter, so it uses only the standard library.
package proxyreverse

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Config is the plugin's configuration in Traefik's dynamic config (written by core's gateway
// package for each external route).
type Config struct {
	Upstream     string `json:"upstream,omitempty"`     // http://10.0.0.5:3010/app: server and path
	UpstreamPath string `json:"upstreamPath,omitempty"` // "/app", or "" when the upstream serves at its root
	PublicPath   string `json:"publicPath,omitempty"`   // "/legacy", or "" for the home route at /
}

// CreateConfig is called by Traefik.
func CreateConfig() *Config { return &Config{} }

type rewriter struct {
	next         http.Handler
	upstreamHost string // host:port of the upstream
	upPath       string
	public       string
}

// New is called by Traefik for every middleware instance.
func New(ctx context.Context, next http.Handler, cfg *Config, name string) (http.Handler, error) {
	u, err := url.Parse(cfg.Upstream)
	if err != nil || u.Host == "" {
		return nil, errors.New("proxyreverse: upstream must be a URL like http://10.0.0.5:3010/app")
	}
	return &rewriter{
		next: next, upstreamHost: strings.ToLower(u.Host),
		upPath: strings.TrimRight(cfg.UpstreamPath, "/"), public: strings.TrimRight(cfg.PublicPath, "/"),
	}, nil
}

func (rw *rewriter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Streams have nothing to rewrite, and the proxy needs the original writer for them: a wrapper
	// made in Traefik's interpreter hides Hijack (websockets) and Flush (server-sent events, which
	// would then arrive all at once at the end). Browsers' EventSource always sends this Accept.
	if r.Header.Get("Upgrade") != "" || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		rw.next.ServeHTTP(w, r)
		return
	}
	rw.next.ServeHTTP(&response{ResponseWriter: w, rw: rw, host: strings.ToLower(r.Host)}, r)
}

// mapPath turns a path under the upstream's into the same path under the public one. ok is false
// for paths outside the upstream's.
func (rw *rewriter) mapPath(p string) (string, bool) {
	if p == "" {
		p = "/"
	}
	if rw.upPath == "" {
		return rw.public + p, true
	}
	if p == rw.upPath {
		return rw.public + "/", true
	}
	if strings.HasPrefix(p, rw.upPath+"/") {
		return rw.public + p[len(rw.upPath):], true
	}
	return "", false
}

// location rewrites a Location or Content-Location value, the way ProxyPassReverse does.
func (rw *rewriter) location(v, host string) string {
	u, err := url.Parse(v)
	if err != nil {
		return v
	}
	switch {
	case u.Host != "" && strings.EqualFold(u.Host, rw.upstreamHost):
		// Pointing at the upstream itself: back onto the site, as a path on the same origin.
		p, ok := rw.mapPath(u.Path)
		if !ok {
			return v
		}
		return withQuery(p, u)
	case u.Host != "" && strings.EqualFold(u.Host, host):
		// The site's own host (the upstream saw it thanks to PreserveHost), but the upstream's path.
		if rw.upPath == rw.public {
			return v
		}
		p, ok := rw.mapPath(u.Path)
		if !ok {
			return v
		}
		u.Path, u.RawPath = p, ""
		return u.String()
	case u.Host == "" && u.Scheme == "" && strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//"):
		// A path on the same origin: rewrite only when it is under the upstream's own path and that
		// differs from the public one (Apache leaves other relative paths alone too).
		if rw.upPath == "" || rw.upPath == rw.public {
			return v
		}
		p, ok := rw.mapPath(u.Path)
		if !ok {
			return v
		}
		return withQuery(p, u)
	}
	return v
}

func withQuery(p string, u *url.URL) string {
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		p += "#" + u.EscapedFragment()
	}
	return p
}

// cookie rewrites a Set-Cookie's Path attribute (ProxyPassReverseCookiePath).
func (rw *rewriter) cookie(v string) string {
	if rw.upPath == rw.public {
		return v
	}
	parts := strings.Split(v, ";")
	for i, part := range parts {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(k, "path") {
			continue
		}
		if p, ok := rw.mapPath(val); ok {
			if p != "/" {
				p = strings.TrimRight(p, "/")
			}
			parts[i] = " Path=" + p
		}
	}
	return strings.Join(parts, ";")
}

// response rewrites headers just before they are sent, and otherwise stays out of the way.
type response struct {
	http.ResponseWriter
	rw    *rewriter
	host  string
	wrote bool
}

func (r *response) fix() {
	if r.wrote {
		return
	}
	r.wrote = true
	h := r.ResponseWriter.Header()
	for _, k := range []string{"Location", "Content-Location"} {
		if v := h.Get(k); v != "" {
			h.Set(k, r.rw.location(v, r.host))
		}
	}
	if cs := h.Values("Set-Cookie"); len(cs) > 0 {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = r.rw.cookie(c)
		}
		h["Set-Cookie"] = out
	}
}

func (r *response) WriteHeader(code int) {
	r.fix()
	r.ResponseWriter.WriteHeader(code)
}

func (r *response) Write(b []byte) (int, error) {
	r.fix()
	return r.ResponseWriter.Write(b)
}

// Flush keeps server-sent events and other streams flowing.
func (r *response) Flush() {
	r.fix()
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack hands websocket upgrades the raw connection.
func (r *response) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	r.fix()
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("proxyreverse: the connection cannot be hijacked")
	}
	return h.Hijack()
}

// Unwrap lets http.ResponseController reach the real writer.
func (r *response) Unwrap() http.ResponseWriter { return r.ResponseWriter }
