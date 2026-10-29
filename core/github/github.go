// Package github is the deploy portal's client for the platform's GitHub App: it lists the repos an
// installation can see, reads a repo's app.toml, lists releases and downloads their artifacts, and
// checks webhook signatures. Plain net/http against GitHub's REST API; no SDK.
//
// The App authenticates as itself with a short JWT signed by its private key, and exchanges that for
// an installation token (one hour, only the repos the owner chose). Tokens never leave core.
package github

import (
	"context"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// ErrNotFound is GitHub's 404: no such file, release or repo, or the App cannot see it.
var ErrNotFound = errors.New("github: not found")

type App struct {
	ID            int64
	Slug          string // the App's URL name, for the install link
	Key           *rsa.PrivateKey
	WebhookSecret string
	API           string // default https://api.github.com
	Web           string // default https://github.com
	HTTP          *http.Client

	mu     sync.Mutex
	tokens map[int64]cachedToken
}

type cachedToken struct {
	token string
	exp   time.Time
}

// ParseKey reads the App's private key as GitHub hands it out (PEM, PKCS#1 or PKCS#8).
func ParseKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("github: private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github: private key is not RSA")
	}
	return rk, nil
}

// InstallURL is where an owner installs the App on their repos. GitHub sends them back to the
// App's setup URL with installation_id and this state.
func (a *App) InstallURL(state string) string {
	return a.web() + "/apps/" + url.PathEscape(a.Slug) + "/installations/new?state=" + url.QueryEscape(state)
}

// VerifyWebhook checks GitHub's X-Hub-Signature-256 header against the body.
func (a *App) VerifyWebhook(body []byte, signature string) bool {
	if a.WebhookSecret == "" || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, []byte(a.WebhookSecret))
	m.Write(body)
	return hmac.Equal(got, m.Sum(nil))
}

// RepoInstallation returns the id of the App's installation that covers repo, or ErrNotFound when
// the App is not installed there. Asked as the App itself, so a person cannot point the portal at an
// installation id of their choosing.
func (a *App) RepoInstallation(ctx context.Context, repo string) (int64, error) {
	jwt, err := a.appJWT()
	if err != nil {
		return 0, err
	}
	resp, err := a.send(ctx, http.MethodGet, a.api()+"/repos/"+repoPath(repo)+"/installation", "Bearer "+jwt, "application/vnd.github+json")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var res struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || res.ID == 0 {
		return 0, fmt.Errorf("github: installation for %s: %v", repo, err)
	}
	return res.ID, nil
}

type Repo struct {
	FullName      string `json:"full_name"` // owner/name
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

// Repos lists the repos an installation can see.
func (a *App) Repos(ctx context.Context, installation int64) ([]Repo, error) {
	var all []Repo
	for page := 1; page <= 10; page++ {
		var res struct {
			Total int    `json:"total_count"`
			Repos []Repo `json:"repositories"`
		}
		if err := a.getJSON(ctx, installation, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), &res); err != nil {
			return nil, err
		}
		all = append(all, res.Repos...)
		if len(all) >= res.Total || len(res.Repos) == 0 {
			break
		}
	}
	return all, nil
}

// File returns a file from the repo at ref (a branch, tag or commit), up to 1 MB.
func (a *App) File(ctx context.Context, installation int64, repo, path, ref string) ([]byte, error) {
	u := "/repos/" + repoPath(repo) + "/contents/" + strings.TrimLeft(path, "/") + "?ref=" + url.QueryEscape(ref)
	resp, err := a.do(ctx, installation, u, "application/vnd.github.raw+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

type Release struct {
	ID         int64     `json:"id"`
	Tag        string    `json:"tag_name"`
	Target     string    `json:"target_commitish"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	Assets     []Asset   `json:"assets"`
}

type Asset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // "sha256:<hex>", when GitHub has computed it
}

// Asset returns the release's asset with this name.
func (r *Release) Asset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// Releases returns the repo's newest releases (drafts excluded).
func (a *App) Releases(ctx context.Context, installation int64, repo string) ([]Release, error) {
	var rs []Release
	if err := a.getJSON(ctx, installation, "/repos/"+repoPath(repo)+"/releases?per_page=20", &rs); err != nil {
		return nil, err
	}
	out := rs[:0]
	for _, r := range rs {
		if !r.Draft {
			out = append(out, r)
		}
	}
	return out, nil
}

// Download writes an asset to w, refusing more than max bytes. GitHub redirects to its storage;
// Go's client drops the Authorization header when the redirect leaves api.github.com.
func (a *App) Download(ctx context.Context, installation int64, repo string, asset int64, w io.Writer, max int64) (int64, error) {
	resp, err := a.do(ctx, installation, fmt.Sprintf("/repos/%s/releases/assets/%d", repoPath(repo), asset), "application/octet-stream")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	n, err := io.Copy(w, io.LimitReader(resp.Body, max+1))
	if err == nil && n > max {
		err = fmt.Errorf("github: asset is larger than %d bytes", max)
	}
	return n, err
}

// ---- plumbing ----

func (a *App) getJSON(ctx context.Context, installation int64, path string, out any) error {
	resp, err := a.do(ctx, installation, path, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// do sends an authenticated GET as the installation. A non-2xx answer is an error (ErrNotFound for 404).
func (a *App) do(ctx context.Context, installation int64, path, accept string) (*http.Response, error) {
	tok, err := a.installationToken(ctx, installation)
	if err != nil {
		return nil, err
	}
	return a.send(ctx, http.MethodGet, a.api()+path, "token "+tok, accept)
}

func (a *App) send(ctx context.Context, method, u, auth, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("github: %s %s: %s: %s", method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

// installationToken returns a cached token, or a fresh one when the cached one has under five
// minutes left.
func (a *App) installationToken(ctx context.Context, installation int64) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.tokens[installation]; ok && time.Until(t.exp) > 5*time.Minute {
		return t.token, nil
	}
	jwt, err := a.appJWT()
	if err != nil {
		return "", err
	}
	resp, err := a.send(ctx, http.MethodPost, a.api()+"/app/installations/"+strconv.FormatInt(installation, 10)+"/access_tokens", "Bearer "+jwt, "application/vnd.github+json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var res struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || res.Token == "" {
		return "", fmt.Errorf("github: installation token: %v", err)
	}
	if a.tokens == nil {
		a.tokens = map[int64]cachedToken{}
	}
	a.tokens[installation] = cachedToken{res.Token, res.ExpiresAt}
	return res.Token, nil
}

// appJWT is the App's own credential: RS256, issued a minute in the past for clock drift, valid for
// nine minutes (GitHub allows ten).
func (a *App) appJWT() (string, error) {
	if a.Key == nil || a.ID == 0 {
		return "", errors.New("github: App ID and private key are not configured")
	}
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: a.Key}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", err
	}
	now := time.Now()
	payload, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(a.ID, 10)})
	jws, err := sig.Sign(payload)
	if err != nil {
		return "", err
	}
	return jws.CompactSerialize()
}

func (a *App) api() string {
	if a.API != "" {
		return strings.TrimRight(a.API, "/")
	}
	return "https://api.github.com"
}

func (a *App) web() string {
	if a.Web != "" {
		return strings.TrimRight(a.Web, "/")
	}
	return "https://github.com"
}

func (a *App) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// repoPath escapes owner/name for a URL path, keeping the slash.
func repoPath(repo string) string {
	owner, name, _ := strings.Cut(repo, "/")
	return url.PathEscape(owner) + "/" + url.PathEscape(name)
}
