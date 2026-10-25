package pb

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	iimakit "github.com/arnav127/iima-platform/kit"
)

// tokens mints platform tokens and serves the matching JWKS, standing in for core.
type tokens struct {
	priv ed25519.PrivateKey
	srv  *httptest.Server
}

func newTokens(t testing.TB) *tokens {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	tk := &tokens{priv: priv}
	tk.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: priv.Public(), KeyID: "k1", Algorithm: "EdDSA", Use: "sig"}}})
	}))
	t.Cleanup(tk.srv.Close)
	return tk
}

func (tk *tokens) mint(t testing.TB, aud, email, sid string, roles ...string) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: jose.JSONWebKey{Key: tk.priv, KeyID: "k1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	b, _ := json.Marshal(iimakit.Claims{V: 1, Issuer: iimakit.DefaultIssuer, Audience: aud, Subject: "u_" + email, Session: sid,
		IssuedAt: now.Unix(), Expiry: now.Add(time.Minute).Unix(), Email: email, Provider: "google", Member: true, Roles: roles})
	jws, err := signer.Sign(b)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := jws.CompactSerialize()
	return s
}

func (tk *tokens) verifier() *iimakit.Verifier {
	v := iimakit.NewVerifier("demo")
	v.JWKSURL = tk.srv.URL
	return v
}

// The PocketBase test data has users test@example.com, test2@example.com and test3@example.com.
func accessByEmail(e *core.RequestEvent, c *iimakit.Claims) (*core.Record, error) {
	rec, err := e.App.FindAuthRecordByEmail("users", c.Email)
	if err != nil {
		return nil, nil // not on this app's list
	}
	return rec, nil
}

func whoami(e *core.RequestEvent) error {
	out := map[string]any{"auth": "", "claims": ""}
	if e.Auth != nil {
		out["auth"] = e.Auth.Email()
		out["roles"] = e.Auth.GetStringSlice("roles")
	}
	if c := Claims(e); c != nil {
		out["claims"] = c.Email
	}
	return e.JSON(200, out)
}

func scenario(t *testing.T, tk *tokens, cfg Config, s tests.ApiScenario) {
	t.Helper()
	cfg.AppID, cfg.Verifier, cfg.NoEnvConfig = "demo", tk.verifier(), true
	if cfg.Access == nil {
		cfg.Access = accessByEmail
	}
	s.TestAppFactory = func(t testing.TB) *tests.TestApp {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		if err := Register(app, cfg); err != nil {
			t.Fatal(err)
		}
		return app
	}
	prev := s.BeforeTestFunc
	s.BeforeTestFunc = func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		e.Router.GET("/whoami", whoami)
		e.Router.POST("/whoami", whoami)
		if prev != nil {
			prev(t, app, e)
		}
	}
	s.Test(t)
}

func TestSSO(t *testing.T) {
	tk := newTokens(t)
	for _, s := range []tests.ApiScenario{
		{Name: "no token", Method: "GET", URL: "/whoami",
			ExpectedStatus: 200, ExpectedContent: []string{`"auth":""`, `"claims":""`}},
		{Name: "valid token for a listed user", Method: "GET", URL: "/whoami",
			Headers:        map[string]string{iimakit.TokenHeader: tk.mint(t, "demo", "test@example.com", "s1")},
			ExpectedStatus: 200, ExpectedContent: []string{`"auth":"test@example.com"`}},
		{Name: "signed in but not on the app's list", Method: "GET", URL: "/whoami",
			Headers:        map[string]string{iimakit.TokenHeader: tk.mint(t, "demo", "stranger@iima.ac.in", "s2")},
			ExpectedStatus: 200, ExpectedContent: []string{`"auth":""`, `"claims":"stranger@iima.ac.in"`}},
		{Name: "token for another app", Method: "GET", URL: "/whoami",
			Headers:        map[string]string{iimakit.TokenHeader: tk.mint(t, "other", "test@example.com", "s3")},
			ExpectedStatus: 200, ExpectedContent: []string{`"auth":""`, `"claims":""`}},
		{Name: "forged token", Method: "GET", URL: "/whoami",
			Headers:        map[string]string{iimakit.TokenHeader: "forged"},
			ExpectedStatus: 200, ExpectedContent: []string{`"auth":""`, `"claims":""`}},
		{Name: "cross-origin POST runs signed out", Method: "POST", URL: "/whoami",
			Headers:        map[string]string{iimakit.TokenHeader: tk.mint(t, "demo", "test@example.com", "s4"), "Origin": "https://evil.example"},
			ExpectedStatus: 200, ExpectedContent: []string{`"auth":""`}},
		{Name: "same-origin POST is signed in", Method: "POST", URL: "http://example.com/whoami",
			Headers:        map[string]string{iimakit.TokenHeader: tk.mint(t, "demo", "test@example.com", "s5"), "Origin": "http://example.com"},
			ExpectedStatus: 200, ExpectedContent: []string{`"auth":"test@example.com"`}},
		{Name: "PocketBase's own password login is off", Method: "POST", URL: "/api/collections/users/auth-with-password",
			Body:           strings.NewReader(`{"identity":"test@example.com","password":"1234567890"}`),
			ExpectedStatus: 403, ExpectedContent: []string{`"data":{}`}},
		{Name: "dashboard hidden from non-localhost", Method: "GET", URL: "/_/",
			ExpectedStatus: 404, ExpectedContent: []string{`"data":{}`}},
		{Name: "superuser API hidden from non-localhost", Method: "POST", URL: "/api/collections/_superusers/auth-with-password",
			Body:           strings.NewReader(`{"identity":"test@example.com","password":"1234567890"}`),
			ExpectedStatus: 404, ExpectedContent: []string{`"data":{}`}},
	} {
		scenario(t, tk, Config{}, s)
	}
}

func TestDevUserNeverOverridesARejectedToken(t *testing.T) {
	t.Setenv("IIMA_DEV_USER", "test@example.com")
	tk := newTokens(t)
	scenario(t, tk, Config{}, tests.ApiScenario{Name: "dev user, no token", Method: "GET", URL: "/whoami",
		ExpectedStatus: 200, ExpectedContent: []string{`"auth":"test@example.com"`}})
	scenario(t, tk, Config{}, tests.ApiScenario{Name: "dev user, forged token", Method: "GET", URL: "/whoami",
		Headers:        map[string]string{iimakit.TokenHeader: "forged"},
		ExpectedStatus: 200, ExpectedContent: []string{`"auth":""`}})
}

func TestRolesMirrorAndFirstSeen(t *testing.T) {
	tk := newTokens(t)
	var firstSeen atomic.Int32
	cfg := Config{OnFirstSeen: func(e *core.RequestEvent, u *core.Record, c *iimakit.Claims) { firstSeen.Add(1) }}

	scenario(t, tk, cfg, tests.ApiScenario{
		Name: "roles copied and sign-in seen once", Method: "GET", URL: "/whoami",
		Headers: map[string]string{iimakit.TokenHeader: tk.mint(t, "demo", "test@example.com", "s1", "volunteer", "admin")},
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			users, _ := app.FindCollectionByNameOrId("users")
			users.Fields.Add(&core.JSONField{Name: "roles"})
			if err := app.Save(users); err != nil {
				t.Fatal(err)
			}
		},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			u, _ := app.FindAuthRecordByEmail("users", "test@example.com")
			if got := strings.Join(u.GetStringSlice("roles"), ","); got != "admin,volunteer" {
				t.Fatalf("roles = %q", got)
			}
			if n := firstSeen.Load(); n != 1 {
				t.Fatalf("first seen %d times", n)
			}
			users, _ := app.FindCollectionByNameOrId("users")
			if users.PasswordAuth.Enabled || users.OAuth2.Enabled {
				t.Fatal("auth methods still on")
			}
		},
		ExpectedStatus: 200, ExpectedContent: []string{`"roles":["admin","volunteer"]`},
	})
}

func TestSeenSessions(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	s := &seenSessions{}
	if err := s.init(app); err != nil {
		t.Fatal(err)
	}
	if !s.first(app, "s1") || s.first(app, "s1") || s.first(app, "") {
		t.Fatal("s1 should be first exactly once")
	}
	// A restart forgets memory but not the table.
	if (&seenSessions{}).first(app, "s1") {
		t.Fatal("s1 counted again after restart")
	}
}

func TestFromTunnel(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:5000": true, "[::1]:5000": true, "10.0.0.5:5000": false, "garbage": false} {
		r := httptest.NewRequest("GET", "/_/", nil)
		r.RemoteAddr = addr
		if got := fromTunnel(r); got != want {
			t.Errorf("%s: got %v", addr, got)
		}
		// Through a gateway on the same machine: loopback peer, but forwarded.
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		if fromTunnel(r) {
			t.Errorf("%s with X-Forwarded-For counted as a tunnel", addr)
		}
	}
}
