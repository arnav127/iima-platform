package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/store"
	"github.com/arnav127/iima-platform/core/token"
	iimakit "github.com/arnav127/iima-platform/kit"
)

type fakeGoogle struct {
	lastOpt AuthOptions
	id      Identity
}

func (f *fakeGoogle) AuthCodeURL(ls store.LoginState, opt AuthOptions) string {
	f.lastOpt = opt
	return "https://accounts.example/auth?state=" + url.QueryEscape(ls.State)
}

func (f *fakeGoogle) Exchange(ctx context.Context, code string, ls *store.LoginState) (*Identity, error) {
	if code != "good" {
		return nil, errors.New("bad code")
	}
	id := f.id
	return &id, nil
}

type env struct {
	t      *testing.T
	srv    *httptest.Server
	google *fakeGoogle
	cfg    Config
}

func newEnv(t *testing.T, devLogin bool) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	signer, err := token.Load(context.Background(), st, iimakit.DefaultIssuer, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(
		&registry.App{ID: "demo", Name: "Demo", Path: "/demo", Shape: "go", Login: "iima", Auth: "optional"},
		&registry.App{ID: "demo-external", Name: "Demo ext", Path: "/demo-external", Shape: "go", Login: "external", Auth: "optional"},
	)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, google: &fakeGoogle{}}
	e.srv = httptest.NewUnstartedServer(nil)
	e.srv.Start()
	t.Cleanup(e.srv.Close)
	pub, _ := url.Parse(e.srv.URL)
	e.cfg = Config{PublicURL: pub, MemberDomain: "iima.ac.in", SessionTTL: time.Hour, DevLogin: devLogin,
		Registry: reg, Store: st, Signer: signer, Provider: e.google}
	e.srv.Config.Handler = New(e.cfg).Handler()
	return e
}

// browser is a client with its own cookies that does not follow redirects.
func (e *env) browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (e *env) do(c *http.Client, method, path string, body url.Values, hdr map[string]string) *http.Response {
	e.t.Helper()
	var rb io.Reader
	if body != nil {
		rb = strings.NewReader(body.Encode())
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rb)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func (e *env) cookie(c *http.Client, name string) string {
	u, _ := url.Parse(e.srv.URL + "/auth/")
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

// devSignIn signs a browser in through the dev form and returns where it was sent afterwards.
func (e *env) devSignIn(c *http.Client, email, rd string) string {
	e.t.Helper()
	e.do(c, "GET", "/auth/login?rd="+url.QueryEscape(rd), nil, nil)
	resp := e.do(c, "POST", "/auth/login/dev", url.Values{"state": {e.cookie(c, loginCookie)}, "email": {email}}, nil)
	if resp.StatusCode != http.StatusFound {
		e.t.Fatalf("dev login: status %d", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

func (e *env) verify(c *http.Client, mode, uri string, hdr map[string]string) *http.Response {
	h := map[string]string{"X-Forwarded-Uri": uri, "X-Forwarded-Method": "GET"}
	for k, v := range hdr {
		h[k] = v
	}
	return e.do(c, "GET", "/auth/verify?mode="+mode, nil, h)
}

func (e *env) verifier(appID string) *iimakit.Verifier {
	v := iimakit.NewVerifier(appID)
	v.JWKSURL = e.srv.URL + "/auth/.well-known/jwks.json"
	return v
}

func TestSafeRedirect(t *testing.T) {
	good := []string{"/", "/demo", "/demo/x?y=1#z", "/%2F%2Fevil.com"}
	bad := []string{"", "demo", "//evil.com", "///evil.com", "/\\evil.com", "\\\\evil.com", "https://evil.com",
		"javascript:alert(1)", "/demo\r\nSet-Cookie: x=1", "/\tevil.com", "http:/evil.com"}
	for _, rd := range good {
		if _, ok := safeRedirect(rd); !ok {
			t.Errorf("%q refused", rd)
		}
	}
	for _, rd := range bad {
		if _, ok := safeRedirect(rd); ok {
			t.Errorf("%q accepted", rd)
		}
	}
}

func TestLoginRefusesOpenRedirect(t *testing.T) {
	e := newEnv(t, true)
	for _, rd := range []string{"https://evil.com/", "//evil.com", "/\\evil.com"} {
		if got := e.devSignIn(e.browser(), "p25a@iima.ac.in", rd); got != "/" {
			t.Errorf("rd %q: sent to %q, want /", rd, got)
		}
	}
	// Logout too.
	c := e.browser()
	e.devSignIn(c, "p25a@iima.ac.in", "/")
	resp := e.do(c, "POST", "/auth/logout", url.Values{"rd": {"//evil.com"}}, nil)
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("logout sent to %q", loc)
	}
}

func TestVerifySignedOut(t *testing.T) {
	e := newEnv(t, true)
	c := e.browser()

	resp := e.verify(c, "optional", "/demo/", nil)
	if resp.StatusCode != 200 || resp.Header.Get("X-User-Token") != "" {
		t.Fatalf("optional: %d token=%q", resp.StatusCode, resp.Header.Get("X-User-Token"))
	}

	resp = e.verify(c, "required", "/demo/x?y=1", map[string]string{"Accept": "text/html"})
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != e.srv.URL+"/auth/login?rd=%2Fdemo%2Fx%3Fy%3D1" {
		t.Fatalf("required, browser: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp = e.verify(c, "required", "/demo/api/x", map[string]string{"Accept": "application/json"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("required, api: %d", resp.StatusCode)
	}

	if resp := e.do(c, "GET", "/auth/verify", nil, nil); resp.StatusCode != 500 {
		t.Fatalf("no mode: %d", resp.StatusCode)
	}
}

func TestVerifyIgnoresSpoofedHeaders(t *testing.T) {
	e := newEnv(t, true)
	c := e.browser()
	spoof := map[string]string{"X-User-Token": "forged", "X-User-Email": "admin@iima.ac.in", "Authorization": "Bearer forged"}
	resp := e.verify(c, "optional", "/demo/", spoof)
	if got := resp.Header.Get("X-User-Token"); got != "" {
		t.Fatalf("token returned for a signed-out request: %q", got)
	}
	// A made-up session cookie is just signed out.
	jarURL, _ := url.Parse(e.srv.URL)
	c.Jar.SetCookies(jarURL, []*http.Cookie{{Name: sessionCookie, Value: "made-up", Path: "/"}})
	if resp := e.verify(c, "optional", "/demo/", nil); resp.Header.Get("X-User-Token") != "" {
		t.Fatal("token for a made-up session")
	}
}

func TestMemberGetsTokenForTheirAppOnly(t *testing.T) {
	e := newEnv(t, true)
	c := e.browser()
	if got := e.devSignIn(c, "P25Someone@IIMA.ac.in", "/demo/"); got != "/demo/" {
		t.Fatalf("sent to %q", got)
	}
	resp := e.verify(c, "optional", "/demo/page", nil)
	tok := resp.Header.Get("X-User-Token")
	if tok == "" {
		t.Fatal("no token")
	}
	claims, err := e.verifier("demo").Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Email != "p25someone@iima.ac.in" || !claims.Member || claims.Audience != "demo" || claims.Session == "" {
		t.Fatalf("claims %+v", claims)
	}
	// The same token presented to another app is refused there.
	if _, err := e.verifier("demo-external").Verify(context.Background(), tok); !errors.Is(err, iimakit.ErrWrongAudience) {
		t.Fatalf("other app: %v", err)
	}
}

func TestGmailUserRefusedForIIMAAppAllowedForExternal(t *testing.T) {
	e := newEnv(t, true)
	c := e.browser()
	// Signing in from an IIMA-only app lands on the "needs your IIMA account" page.
	if got := e.devSignIn(c, "someone@gmail.com", "/demo/"); got != "/auth/denied?rd=%2Fdemo%2F" {
		t.Fatalf("sent to %q", got)
	}

	if resp := e.verify(c, "optional", "/demo/", nil); resp.StatusCode != 200 || resp.Header.Get("X-User-Token") != "" {
		t.Fatalf("iima app, optional: %d token=%v", resp.StatusCode, resp.Header.Get("X-User-Token") != "")
	}
	resp := e.verify(c, "required", "/demo/", map[string]string{"Accept": "text/html"})
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), e.srv.URL+"/auth/denied?") {
		t.Fatalf("iima app, required: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := e.verify(c, "required", "/demo/api", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("iima app, required api: %d", resp.StatusCode)
	}
	if resp := e.do(c, "GET", "/auth/denied?rd=%2Fdemo%2F", nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied page: %d", resp.StatusCode)
	}

	tok := e.verify(c, "optional", "/demo-external/", nil).Header.Get("X-User-Token")
	claims, err := e.verifier("demo-external").Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Member || claims.Email != "someone@gmail.com" {
		t.Fatalf("claims %+v", claims)
	}
}

func TestGoogleCallback(t *testing.T) {
	e := newEnv(t, false)

	start := func(c *http.Client, rd string) string {
		resp := e.do(c, "GET", "/auth/login?rd="+url.QueryEscape(rd), nil, nil)
		loc, _ := url.Parse(resp.Header.Get("Location"))
		return loc.Query().Get("state")
	}
	member := func(c *http.Client) bool {
		tok := e.verify(c, "optional", "/demo-external/", nil).Header.Get("X-User-Token")
		claims, err := e.verifier("demo-external").Verify(context.Background(), tok)
		if err != nil {
			t.Fatal(err)
		}
		return claims.Member
	}

	// The hd hint goes only to IIMA-only apps; it is never what makes someone a member.
	c := e.browser()
	start(c, "/demo/")
	if e.google.lastOpt.HostedDomain != "iima.ac.in" {
		t.Fatalf("no hd hint for an iima app")
	}
	start(c, "/demo-external/")
	if e.google.lastOpt.HostedDomain != "" {
		t.Fatalf("hd hint for an external app")
	}
	start(c, "/demo/?x")
	e.do(c, "GET", "/auth/login?rd=%2Fdemo%2F&switch=1", nil, nil)
	if !e.google.lastOpt.SelectAccount {
		t.Fatal("switch=1 did not ask for the account chooser")
	}

	tests := []struct {
		name   string
		id     Identity
		member bool
	}{
		{"workspace account", Identity{Subject: "g1", Email: "p25a@iima.ac.in", EmailVerified: true, HostedDomain: "iima.ac.in"}, true},
		{"iima email without hd claim", Identity{Subject: "g2", Email: "p25b@iima.ac.in", EmailVerified: true}, false},
		{"other workspace", Identity{Subject: "g3", Email: "x@iima.ac.in.evil.com", EmailVerified: true, HostedDomain: "iima.ac.in.evil.com"}, false},
		{"gmail", Identity{Subject: "g4", Email: "x@gmail.com", EmailVerified: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e.google.id = tt.id
			c := e.browser()
			state := start(c, "/demo-external/")
			resp := e.do(c, "GET", "/auth/callback?code=good&state="+url.QueryEscape(state), nil, nil)
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("callback: %d", resp.StatusCode)
			}
			if got := member(c); got != tt.member {
				t.Fatalf("member = %v, want %v", got, tt.member)
			}
		})
	}

	t.Run("unverified email", func(t *testing.T) {
		e.google.id = Identity{Subject: "g5", Email: "p25c@iima.ac.in", HostedDomain: "iima.ac.in"}
		c := e.browser()
		state := start(c, "/demo/")
		if resp := e.do(c, "GET", "/auth/callback?code=good&state="+url.QueryEscape(state), nil, nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})

	t.Run("state from another browser", func(t *testing.T) {
		e.google.id = tests[0].id
		attacker, victim := e.browser(), e.browser()
		state := start(attacker, "/demo/")
		start(victim, "/demo/")
		if resp := e.do(victim, "GET", "/auth/callback?code=good&state="+url.QueryEscape(state), nil, nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})

	t.Run("state used twice", func(t *testing.T) {
		e.google.id = tests[0].id
		c := e.browser()
		state := start(c, "/demo/")
		e.do(c, "GET", "/auth/callback?code=good&state="+url.QueryEscape(state), nil, nil)
		jarURL, _ := url.Parse(e.srv.URL + "/auth/")
		c.Jar.SetCookies(jarURL, []*http.Cookie{{Name: loginCookie, Value: state, Path: "/auth/"}})
		if resp := e.do(c, "GET", "/auth/callback?code=good&state="+url.QueryEscape(state), nil, nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})

	t.Run("dev login is off", func(t *testing.T) {
		if resp := e.do(e.browser(), "POST", "/auth/login/dev", url.Values{"email": {"a@iima.ac.in"}}, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
}

func TestLogout(t *testing.T) {
	e := newEnv(t, true)
	c := e.browser()
	e.devSignIn(c, "p25a@iima.ac.in", "/")

	if resp := e.do(c, "POST", "/auth/logout", nil, map[string]string{"Origin": "https://evil.example"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin logout: %d", resp.StatusCode)
	}
	if e.verify(c, "optional", "/demo/", nil).Header.Get("X-User-Token") == "" {
		t.Fatal("signed out by a cross-origin POST")
	}

	old := e.cookie(c, sessionCookie)
	e.do(c, "POST", "/auth/logout", nil, map[string]string{"Origin": e.srv.URL})
	// Even replaying the old cookie, straight after logout (the cache must not keep it alive).
	jarURL, _ := url.Parse(e.srv.URL)
	c.Jar.SetCookies(jarURL, []*http.Cookie{{Name: sessionCookie, Value: old, Path: "/"}})
	if e.verify(c, "optional", "/demo/", nil).Header.Get("X-User-Token") != "" {
		t.Fatal("token after logout")
	}
}
