package platformstrip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func run(t *testing.T, cfg *Config, req *http.Request) *http.Request {
	t.Helper()
	var got *http.Request
	h, err := New(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r }), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestStripsIdentityHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/demo/", nil)
	r.Header.Set("X-User-Token", "forged")
	r.Header.Set("x-user-email", "admin@iima.ac.in")
	r.Header["X-USER-ROLES"] = []string{"admin"} // non-canonical key, as a raw client could send
	r.Header.Set("Authorization", "Bearer forged")
	r.Header.Set("X-Request-Id", "keep")

	got := run(t, &Config{Headers: []string{"authorization"}, HeaderPrefixes: []string{"X-User-"}}, r)
	for name := range got.Header {
		if name != "X-Request-Id" {
			t.Errorf("header %q survived", name)
		}
	}
}

func TestStripsOnlyTheSessionCookie(t *testing.T) {
	r := httptest.NewRequest("GET", "/demo/", nil)
	r.Header.Add("Cookie", "theme=dark; iima_session=secret; pb_auth=abc==")
	r.Header.Add("Cookie", "iima_session=again")

	got := run(t, &Config{Cookies: []string{"iima_session"}}, r)
	if c := got.Header.Values("Cookie"); len(c) != 1 || c[0] != "theme=dark; pb_auth=abc==" {
		t.Fatalf("cookies %q", c)
	}

	r = httptest.NewRequest("GET", "/demo/", nil)
	r.Header.Set("Cookie", "iima_session=secret")
	if got := run(t, &Config{Cookies: []string{"iima_session"}}, r); got.Header.Get("Cookie") != "" {
		t.Fatalf("cookie header left: %q", got.Header.Get("Cookie"))
	}
}
