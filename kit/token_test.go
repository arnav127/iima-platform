package iimakit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

var testNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

type testKey struct {
	kid  string
	priv ed25519.PrivateKey
}

func newTestKey(t *testing.T, kid string) testKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{kid, priv}
}

// jwksServer serves the given keys (changeable through the returned pointer) and counts fetches.
func jwksServer(t *testing.T, keys *[]testKey, fetches *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		var set jose.JSONWebKeySet
		for _, k := range *keys {
			set.Keys = append(set.Keys, jose.JSONWebKey{Key: k.priv.Public(), KeyID: k.kid, Algorithm: "EdDSA", Use: "sig"})
		}
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sign(t *testing.T, k testKey, payload any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: jose.JSONWebKey{Key: k.priv, KeyID: k.kid}}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(payload)
	jws, err := signer.Sign(b)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func goodClaims() map[string]any {
	return map[string]any{
		"v": 1, "iss": DefaultIssuer, "aud": "demo", "sub": "u_1", "sid": "s_1",
		"iat": testNow.Unix(), "exp": testNow.Add(5 * time.Minute).Unix(),
		"email": "p25someone@iima.ac.in", "provider": "google", "member": true, "roles": []string{"admin"},
	}
}

func setup(t *testing.T) (*Verifier, testKey, *[]testKey, *atomic.Int32) {
	t.Helper()
	k := newTestKey(t, "k1")
	keys := &[]testKey{k}
	fetches := &atomic.Int32{}
	srv := jwksServer(t, keys, fetches)
	v := &Verifier{AppID: "demo", Issuer: DefaultIssuer, JWKSURL: srv.URL, Leeway: 30 * time.Second, now: func() time.Time { return testNow }}
	return v, k, keys, fetches
}

func TestVerifyAcceptsGoodToken(t *testing.T) {
	v, k, _, _ := setup(t)
	c, err := v.Verify(context.Background(), sign(t, k, goodClaims()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "p25someone@iima.ac.in" || !c.Member || !c.HasRole("admin") || c.HasRole("writer") {
		t.Fatalf("unexpected claims %+v", c)
	}
}

func TestVerifyRejects(t *testing.T) {
	v, k, _, _ := setup(t)
	other := newTestKey(t, "k1") // same kid, different key: a forgery attempt

	tests := []struct {
		name   string
		mutate func(map[string]any)
		key    testKey
		want   error
	}{
		{"wrong audience", func(c map[string]any) { c["aud"] = "auxesiscapital" }, k, ErrWrongAudience},
		{"audience as array", func(c map[string]any) { c["aud"] = []string{"demo", "other"} }, k, ErrMalformed},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "evil.example" }, k, ErrWrongIssuer},
		{"expired", func(c map[string]any) { c["exp"] = testNow.Add(-31 * time.Second).Unix() }, k, ErrExpired},
		{"no expiry", func(c map[string]any) { delete(c, "exp") }, k, ErrExpired},
		{"issued in the future", func(c map[string]any) { c["iat"] = testNow.Add(time.Hour).Unix() }, k, ErrNotYetValid},
		{"unknown version", func(c map[string]any) { c["v"] = 2 }, k, ErrVersion},
		{"no version", func(c map[string]any) { delete(c, "v") }, k, ErrVersion},
		{"no subject", func(c map[string]any) { delete(c, "sub") }, k, ErrMalformed},
		{"signed by another key", func(c map[string]any) {}, other, ErrBadSignature},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := goodClaims()
			tt.mutate(c)
			_, err := v.Verify(context.Background(), sign(t, tt.key, c))
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestVerifyAllowsSmallClockSkew(t *testing.T) {
	v, k, _, _ := setup(t)
	c := goodClaims()
	c["exp"] = testNow.Add(-10 * time.Second).Unix()
	if _, err := v.Verify(context.Background(), sign(t, k, c)); err != nil {
		t.Fatalf("expired 10s ago should pass with 30s leeway: %v", err)
	}
}

func TestVerifyRejectsOtherAlgorithms(t *testing.T) {
	v, k, _, _ := setup(t)
	b64 := base64.RawURLEncoding.EncodeToString
	payload, _ := json.Marshal(goodClaims())

	none := b64([]byte(`{"alg":"none","kid":"k1"}`)) + "." + b64(payload) + "."
	if _, err := v.Verify(context.Background(), none); !errors.Is(err, ErrMalformed) {
		t.Fatalf("alg none: got %v", err)
	}

	// HS256 keyed with the public key bytes: the classic confusion attack.
	hs, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(k.priv.Public().(ed25519.PublicKey))}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	jws, _ := hs.Sign(payload)
	tok, _ := jws.CompactSerialize()
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrMalformed) {
		t.Fatalf("HS256: got %v", err)
	}

	if _, err := v.Verify(context.Background(), "not-a-token"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("garbage: got %v", err)
	}

	// Payload swapped after signing.
	good := sign(t, k, goodClaims())
	parts := strings.Split(good, ".")
	c := goodClaims()
	c["email"] = "someone.else@iima.ac.in"
	forged, _ := json.Marshal(c)
	if _, err := v.Verify(context.Background(), parts[0]+"."+b64(forged)+"."+parts[2]); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered payload: got %v", err)
	}
}

func TestVerifyPicksUpRotatedKey(t *testing.T) {
	v, k1, keys, fetches := setup(t)
	if _, err := v.Verify(context.Background(), sign(t, k1, goodClaims())); err != nil {
		t.Fatal(err)
	}

	k2 := newTestKey(t, "k2")
	*keys = []testKey{k1, k2}
	tok := sign(t, k2, goodClaims())

	// Within minRefetch of the last fetch the unknown kid is refused without calling core again.
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("got %v, want ErrUnknownKey", err)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetched %d times, want 1", n)
	}

	later := testNow.Add(minRefetch)
	v.now = func() time.Time { return later }
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("after refetch: %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("fetched %d times, want 2", n)
	}
}

func TestMiddleware(t *testing.T) {
	v, k, _, _ := setup(t)
	var got *Claims
	h := VerifierMiddleware(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = User(r) }))

	for _, tc := range []struct {
		name, header string
		signedIn     bool
	}{
		{"no header", "", false},
		{"spoofed header", `{"email":"admin@iima.ac.in"}`, false},
		{"token for another app", func() string { c := goodClaims(); c["aud"] = "other"; return sign(t, k, c) }(), false},
		{"valid token", sign(t, k, goodClaims()), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got = nil
			r := httptest.NewRequest("GET", "/demo/", nil)
			if tc.header != "" {
				r.Header.Set(TokenHeader, tc.header)
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			if (got != nil) != tc.signedIn {
				t.Fatalf("signed in = %v, want %v", got != nil, tc.signedIn)
			}
		})
	}
}

func TestRequireUserAndRole(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	withUser := func(r *http.Request, c *Claims) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), ctxKey{}, c))
	}

	// Browser, signed out: redirect to login with a return path.
	r := httptest.NewRequest("GET", "/demo/admin?x=1", nil)
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	RequireUser(ok).ServeHTTP(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/auth/login?rd=%2Fdemo%2Fadmin%3Fx%3D1" {
		t.Fatalf("browser: %d %q", w.Code, w.Header().Get("Location"))
	}

	// API, signed out: 401.
	w = httptest.NewRecorder()
	RequireUser(ok).ServeHTTP(w, httptest.NewRequest("POST", "/demo/api/x", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("api: %d", w.Code)
	}

	// Signed in without the role: 403. With it: through.
	w = httptest.NewRecorder()
	RequireRole("admin", ok).ServeHTTP(w, withUser(httptest.NewRequest("GET", "/demo/admin", nil), &Claims{Email: "a@iima.ac.in"}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("no role: %d", w.Code)
	}
	w = httptest.NewRecorder()
	RequireRole("admin", ok).ServeHTTP(w, withUser(httptest.NewRequest("GET", "/demo/admin", nil), &Claims{Email: "a@iima.ac.in", Roles: []string{"admin"}}))
	if w.Code != http.StatusTeapot {
		t.Fatalf("with role: %d", w.Code)
	}
}
