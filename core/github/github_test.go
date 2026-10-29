package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// fake answers like GitHub: the token endpoint needs a valid App JWT, everything else the token.
func fake(t *testing.T, key **rsa.PrivateKey, tokenCalls *int) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/7/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		*tokenCalls++
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		jws, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
		if err != nil {
			http.Error(w, "bad jwt", 401)
			return
		}
		payload, err := jws.Verify(&(*key).PublicKey)
		if err != nil {
			http.Error(w, "bad signature", 401)
			return
		}
		var c struct{ Iss string }
		_ = json.Unmarshal(payload, &c)
		if c.Iss != "42" {
			http.Error(w, "wrong app", 401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "ghs_x", "expires_at": time.Now().Add(time.Hour)})
	})
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "token ghs_x" {
				http.Error(w, "no", 401)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /installation/repositories", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			json.NewEncoder(w).Encode(map[string]any{"total_count": 2, "repositories": []map[string]any{{"full_name": "club/quiz", "default_branch": "main"}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"total_count": 2, "repositories": []map[string]any{{"full_name": "club/other"}}})
	}))
	mux.HandleFunc("GET /repos/club/quiz/contents/app.toml", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "main" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`id = "quiz"`))
	}))
	mux.HandleFunc("GET /repos/club/quiz/releases", auth(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "tag_name": "draft", "draft": true},
			{"id": 2, "tag_name": "build-abc1234", "target_commitish": "abc1234def", "assets": []map[string]any{{"id": 9, "name": "app.tar.gz", "size": 5}}},
		})
	}))
	mux.HandleFunc("GET /repos/club/quiz/releases/assets/9", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "json", 415)
			return
		}
		w.Write([]byte("hello"))
	}))
	return mux
}

func TestClient(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(fake(t, &key, &calls))
	defer srv.Close()
	a := &App{ID: 42, Slug: "iima-platform", Key: key, API: srv.URL}
	ctx := context.Background()

	repos, err := a.Repos(ctx, 7)
	if err != nil || len(repos) != 2 || repos[0].FullName != "club/quiz" {
		t.Fatalf("repos: %v %v", repos, err)
	}
	b, err := a.File(ctx, 7, "club/quiz", "app.toml", "main")
	if err != nil || string(b) != `id = "quiz"` {
		t.Fatalf("file: %q %v", b, err)
	}
	if _, err := a.File(ctx, 7, "club/quiz", "app.toml", "dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	rs, err := a.Releases(ctx, 7, "club/quiz")
	if err != nil || len(rs) != 1 || rs[0].Tag != "build-abc1234" || rs[0].Asset("app.tar.gz") == nil {
		t.Fatalf("releases: %+v %v", rs, err)
	}
	var buf bytes.Buffer
	if n, err := a.Download(ctx, 7, "club/quiz", 9, &buf, 100); err != nil || n != 5 || buf.String() != "hello" {
		t.Fatalf("download: %d %v %q", n, err, buf.String())
	}
	if _, err := a.Download(ctx, 7, "club/quiz", 9, &bytes.Buffer{}, 3); err == nil {
		t.Fatal("download over the limit should fail")
	}
	if calls != 1 {
		t.Fatalf("installation token fetched %d times, want 1 (cached)", calls)
	}

	// Another installation is refused by the fake: the token is per installation.
	if _, err := a.Repos(ctx, 8); err == nil {
		t.Fatal("unknown installation should fail")
	}
	// A key GitHub does not know is refused.
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	b2 := &App{ID: 42, Key: other, API: srv.URL}
	if _, err := b2.Repos(ctx, 7); err == nil {
		t.Fatal("wrong key should fail")
	}
}

func TestVerifyWebhook(t *testing.T) {
	a := &App{WebhookSecret: "s3cret"}
	body := []byte(`{"action":"published"}`)
	m := hmac.New(sha256.New, []byte("s3cret"))
	m.Write(body)
	good := "sha256=" + hex.EncodeToString(m.Sum(nil))
	if !a.VerifyWebhook(body, good) {
		t.Fatal("good signature refused")
	}
	for name, sig := range map[string]string{"empty": "", "sha1": "sha1=abc", "wrong": "sha256=" + strings.Repeat("0", 64), "not hex": "sha256=zz"} {
		if a.VerifyWebhook(body, sig) {
			t.Fatalf("%s signature accepted", name)
		}
	}
	if a.VerifyWebhook(append(body, ' '), good) {
		t.Fatal("changed body accepted")
	}
	if (&App{}).VerifyWebhook(body, good) {
		t.Fatal("no secret configured must refuse everything")
	}
}

func TestInstallURL(t *testing.T) {
	a := &App{Slug: "iima-platform"}
	if got := a.InstallURL("a b"); got != "https://github.com/apps/iima-platform/installations/new?state=a+b" {
		t.Fatal(got)
	}
}
