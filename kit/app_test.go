package iimakit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestPrefix(t *testing.T) {
	cases := map[string]string{
		"/demo":         "/demo",
		"":              "",
		"/":             "",
		"demo":          "",
		"//evil.com":    "",
		"/a/b":          "",
		"/demo?x":       "",
		"/\\evil.com":   "",
		"/demo/../evil": "",
	}
	for h, want := range cases {
		r := httptest.NewRequest("GET", "/x?y=1", nil)
		r.Header.Set("X-Forwarded-Prefix", h)
		if got := Prefix(r); got != want {
			t.Errorf("Prefix(%q) = %q, want %q", h, got, want)
		}
		if want != "" && OriginalURI(r) != want+"/x?y=1" {
			t.Errorf("OriginalURI = %q", OriginalURI(r))
		}
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		origin, site string
		want         bool
	}{
		{"http://localhost:8080", "", true},
		{"https://evil.example", "", false},
		{"null", "", false},
		{"", "same-origin", true},
		{"", "cross-site", false},
		{"", "same-site", false}, // another subdomain is still another app's code
		{"", "", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "http://localhost:8080/x", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.site != "" {
			r.Header.Set("Sec-Fetch-Site", c.site)
		}
		if got := SameOrigin(r); got != c.want {
			t.Errorf("origin %q site %q: got %v", c.origin, c.site, got)
		}
	}
}

func TestDevClaims(t *testing.T) {
	t.Setenv("IIMA_DEV_USER", "P25Me@iima.ac.in")
	t.Setenv("IIMA_DEV_ROLES", "admin, writer")
	c := DevClaims("demo")
	if c == nil || c.Email != "p25me@iima.ac.in" || !c.Member || !c.HasRole("writer") || c.Audience != "demo" {
		t.Fatalf("claims %+v", c)
	}

	// Never on the cluster.
	t.Setenv("NOMAD_ALLOC_ID", "abc")
	if DevClaims("demo") != nil {
		t.Fatal("dev user active on the cluster")
	}

	// A real token always wins over the dev user; a bad one does not fall back to it.
	t.Setenv("NOMAD_ALLOC_ID", "")
	v, _, _, _ := setup(t)
	var got *Claims
	h := VerifierMiddleware(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = User(r) }))
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(TokenHeader, "forged")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if got != nil {
		t.Fatal("forged token fell back to the dev user")
	}
}

func TestSPA(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":       {Data: []byte("<html>app</html>")},
		"assets/app-1a.js": {Data: []byte("js")},
		"favicon.svg":      {Data: []byte("<svg/>")},
	}
	h := SPA(fsys)
	cases := []struct {
		path, cache string
		status      int
		body        string
	}{
		{"/", "no-cache", 200, "<html>app</html>"},
		{"/portfolio/x", "no-cache", 200, "<html>app</html>"},
		{"/index.html", "no-cache", 200, "<html>app</html>"},
		{"/assets/app-1a.js", "public, max-age=31536000, immutable", 200, "js"},
		{"/favicon.svg", "public, max-age=3600", 200, "<svg/>"},
		{"/missing.js", "", 404, ""},
		{"/../../etc/passwd", "no-cache", 200, "<html>app</html>"}, // cleaned to an app route, never a file outside fsys
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", c.path, nil))
		if w.Code != c.status || (c.cache != "" && w.Header().Get("Cache-Control") != c.cache) || (c.body != "" && w.Body.String() != c.body) {
			t.Errorf("%s: %d %q %q", c.path, w.Code, w.Header().Get("Cache-Control"), w.Body.String())
		}
	}
}
