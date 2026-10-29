package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/arnav127/iima-platform/core/github/githubtest"
	"github.com/arnav127/iima-platform/core/jobgen"
	"github.com/arnav127/iima-platform/core/nomad"
	"github.com/arnav127/iima-platform/core/portal"
)

const quizToml = `id = "quiz"
name = "Quiz Club"
path = "/quiz"
owners = ["p25owner@iima.ac.in"]
shape = "go"
`

// portalEnv is adminEnv plus the deploy portal, a fake GitHub with club/quiz installed, and a Nomad
// that accepts everything.
func portalEnv(t *testing.T) (e *env, gh *githubtest.Server, p *portal.Portal) {
	e, _, _, _ = adminEnv(t)
	gh = githubtest.New()
	t.Cleanup(gh.Close)
	gh.AddRepo("club/quiz", 7, map[string][]byte{"app.toml": []byte(quizToml)})
	nm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/volumes" {
			w.Write([]byte("[]"))
			return
		}
		w.Write([]byte("{}"))
	}))
	t.Cleanup(nm.Close)
	var err error
	p, err = portal.New(context.Background(), portal.Config{
		Store: e.cfg.Store, GitHub: gh.App("whsec"), Nomad: &nomad.Client{Addr: nm.URL},
		Cluster:     jobgen.Cluster{Datacenter: "dc1", Driver: "exec", PublicURL: e.srv.URL},
		ArtifactDir: filepath.Join(t.TempDir(), "artifacts"), ArtifactURL: e.srv.URL + "/platform/artifacts",
		FileApps: e.cfg.Registry.Apps(),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.cfg.Portal = p
	e.srv.Config.Handler = New(e.cfg).Handler()
	return e, gh, p
}

func TestPortalRequestAndApproval(t *testing.T) {
	e, gh, p := portalEnv(t)
	owner, other, gmail, super := e.browser(), e.browser(), e.browser(), e.browser()
	e.devSignIn(owner, "p25owner@iima.ac.in", "/")
	e.devSignIn(other, "p25other@iima.ac.in", "/")
	e.devSignIn(gmail, "someone@gmail.com", "/")
	e.devSignIn(super, "boss@iima.ac.in", "/")
	form := url.Values{"repo": {"club/quiz"}}

	if r := e.do(gmail, "POST", "/admin/apps/new", form, nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("outside account connecting a repo: %d", r.StatusCode)
	}
	if r := e.do(other, "POST", "/admin/apps/new", form, nil); !strings.Contains(r.Header.Get("Location"), "err=") {
		t.Fatalf("someone not in app.toml owners: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := e.do(owner, "POST", "/admin/apps/new", form, map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site request: %d", r.StatusCode)
	}
	if r := e.do(owner, "POST", "/admin/apps/new", form, nil); r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(r.Header.Get("Location"), "/admin/apps/quiz/deploy") {
		t.Fatalf("request: %d %s", r.StatusCode, r.Header.Get("Location"))
	}

	// Only owners and super admins see the app's page.
	for who, c := range map[string]*http.Client{"owner": owner, "super": super} {
		if r := e.do(c, "GET", "/admin/apps/quiz/deploy", nil, nil); r.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", who, r.StatusCode)
		}
	}
	if r := e.do(other, "GET", "/admin/apps/quiz/deploy", nil, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("another member sees the app: %d", r.StatusCode)
	}

	// A pending app is not routed: the gateway check knows nothing of /quiz yet.
	if e.verify(owner, "optional", "/quiz/", nil).Header.Get("X-User-Token") != "" {
		t.Fatal("pending app got a token")
	}
	// The owner cannot approve their own app; a super admin can.
	if r := e.do(owner, "POST", "/admin/apps/quiz/approve", url.Values{}, nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("owner approving: %d", r.StatusCode)
	}
	if r := e.do(super, "POST", "/admin/apps/quiz/approve", url.Values{}, nil); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("super approving: %d", r.StatusCode)
	}
	if e.verify(owner, "optional", "/quiz/", nil).Header.Get("X-User-Token") == "" {
		t.Fatal("approved app got no token")
	}
	// Owners manage the app's roles page too.
	if r := e.do(owner, "GET", "/admin/apps/quiz", nil, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("owner on the roles page: %d", r.StatusCode)
	}

	// Builds are served to Nomad, never through the gateway.
	sha := gh.AddRelease("club/quiz", "build-abcdef0", strings.Repeat("a", 40), []byte("the build"))
	p.Sync(context.Background())
	path := "/platform/artifacts/" + sha + ".tar.gz"
	resp, err := http.Get(e.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "the build" {
		t.Fatalf("artifact for Nomad: %d %q", resp.StatusCode, body)
	}
	for name, p := range map[string]string{"through the gateway": path, "bad name": "/platform/artifacts/..%2Fcore.db", "unknown": "/platform/artifacts/" + strings.Repeat("0", 64) + ".tar.gz"} {
		hdr := map[string]string{}
		if name == "through the gateway" {
			hdr["X-Forwarded-For"] = "1.2.3.4"
		}
		if r := e.do(other, "GET", p, nil, hdr); r.StatusCode != http.StatusNotFound {
			t.Fatalf("artifact %s: %d", name, r.StatusCode)
		}
	}
}

func TestPortalWebhook(t *testing.T) {
	e, _, _ := portalEnv(t)
	c := e.browser()
	body := `{"action":"published"}`
	post := func(sig string) int {
		req, _ := http.NewRequest("POST", e.srv.URL+"/platform/github/webhook", strings.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", sig)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("sha256=" + strings.Repeat("0", 64)); code != http.StatusUnauthorized {
		t.Fatalf("forged webhook: %d", code)
	}
	m := hmac.New(sha256.New, []byte("whsec"))
	m.Write([]byte(body))
	if code := post("sha256=" + hex.EncodeToString(m.Sum(nil))); code != http.StatusNoContent {
		t.Fatalf("genuine webhook: %d", code)
	}
}

func TestGitHubAppSetup(t *testing.T) {
	e, _, _ := portalEnv(t)
	gh := githubtest.New()
	t.Cleanup(gh.Close)
	st := e.cfg.Store
	p, err := portal.New(context.Background(), portal.Config{Store: st, FileApps: e.cfg.Registry.Apps(), GitHubAPI: gh.URL, GitHubWeb: gh.URL,
		Cluster: jobgen.Cluster{PublicURL: e.srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	e.cfg.Portal = p
	e.srv.Config.Handler = New(e.cfg).Handler()
	super, user := e.browser(), e.browser()
	e.devSignIn(super, "boss@iima.ac.in", "/")
	e.devSignIn(user, "p25aarav@iima.ac.in", "/")

	if r := e.do(user, "GET", "/admin/github", nil, nil); r.StatusCode != http.StatusForbidden && r.StatusCode != http.StatusOK {
		t.Fatalf("non-admin: %d", r.StatusCode)
	} else if r.StatusCode == http.StatusOK {
		t.Fatal("a non-super-admin saw the GitHub setup page")
	}

	// Step one: the page holds the manifest form, posting to GitHub with a state.
	req, _ := http.NewRequest("GET", e.srv.URL+"/admin/github?name=Test+App", nil)
	resp, err := super.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := regexp.MustCompile(`action="` + regexp.QuoteMeta(gh.URL) + `/settings/apps/new\?state=([0-9a-f]+)"`).FindSubmatch(body)
	if m == nil || !strings.Contains(string(body), `name="manifest"`) || !strings.Contains(string(body), "/platform/github/webhook") {
		t.Fatalf("no manifest form:\n%s", body)
	}
	state := string(m[1])

	// A wrong state is refused, and so is anyone but a super admin.
	if r := e.do(super, "GET", "/admin/github/created?code=c1&state=nope", nil, nil); !strings.Contains(r.Header.Get("Location"), "err=") {
		t.Fatalf("wrong state: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := e.do(user, "GET", "/admin/github/created?code=c1&state="+state, nil, nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("non-super completing: %d", r.StatusCode)
	}
	if p.Connectable() {
		t.Fatal("connected too early")
	}
	if r := e.do(super, "GET", "/admin/github/created?code=c1&state="+state, nil, nil); !strings.Contains(r.Header.Get("Location"), "msg=") {
		t.Fatalf("finish: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if !p.Connectable() || p.GitHubSlug() != "iima-platform-test" {
		t.Fatal("App not switched on")
	}
	// The state is single use, and a restart loads the stored App.
	if r := e.do(super, "GET", "/admin/github/created?code=c1&state="+state, nil, nil); !strings.Contains(r.Header.Get("Location"), "err=") {
		t.Fatal("state reused")
	}
	p2, _ := portal.New(context.Background(), portal.Config{Store: st, FileApps: e.cfg.Registry.Apps(), GitHubAPI: gh.URL})
	if err := p2.LoadGitHub(context.Background()); err != nil || !p2.Connectable() {
		t.Fatalf("stored App not loaded: %v", err)
	}
}
