// Package githubtest is a fake of the bits of GitHub's API the deploy portal uses, for tests and for
// running the portal on a laptop without a real GitHub App. It checks the App's JWT and scopes each
// installation token to that installation's repos, like GitHub does.
package githubtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/arnav127/iima-platform/core/github"
)

const AppID = 4242

type Repo struct {
	Installation int64
	Branch       string
	Files        map[string][]byte // path -> content, on Branch
	Releases     []*Release        // newest first
}

type Release struct {
	ID     int64
	Tag    string
	Commit string
	Assets map[string][]byte
	// Digests are what GitHub computed at upload; Tamper changes an asset but not its digest.
	Digests map[string]string
}

type Server struct {
	*httptest.Server
	Key *rsa.PrivateKey

	mu    sync.Mutex
	repos map[string]*Repo
	next  int64
}

// New starts a fake GitHub. Close it when done.
func New() *Server {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	s := &Server{Key: key, repos: map[string]*Repo{}, next: 100}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// App returns a client for this fake.
func (s *Server) App(webhookSecret string) *github.App {
	return &github.App{ID: AppID, Slug: "iima-platform-test", Key: s.Key, WebhookSecret: webhookSecret, API: s.URL, Web: s.URL}
}

// AddRepo installs the App on a repo with these files on its default branch.
func (s *Server) AddRepo(name string, installation int64, files map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos[name] = &Repo{Installation: installation, Branch: "main", Files: files}
}

// AddRelease publishes a release with one app.tar.gz asset (plus its .sha256, as the platform's
// build workflow does) and returns the artifact's SHA-256.
func (s *Server) AddRelease(repo, tag, commit string, artifact []byte) string {
	sum := sha256.Sum256(artifact)
	hexSum := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	r := &Release{ID: s.next, Tag: tag, Commit: commit, Assets: map[string][]byte{
		"app.tar.gz":        artifact,
		"app.tar.gz.sha256": []byte(hexSum + "  app.tar.gz\n"),
	}}
	r.Digests = map[string]string{}
	for name, b := range r.Assets {
		d := sha256.Sum256(b)
		r.Digests[name] = "sha256:" + hex.EncodeToString(d[:])
	}
	s.repos[repo].Releases = append([]*Release{r}, s.repos[repo].Releases...)
	return hexSum
}

// Tamper swaps a release's artifact without updating its checksum file or digest, as an attacker
// who could write to the storage but not the release would.
func (s *Server) Tamper(repo, tag string, artifact []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.repos[repo].Releases {
		if r.Tag == tag {
			r.Assets["app.tar.gz"] = artifact
		}
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	auth := r.Header.Get("Authorization")

	// The App itself (JWT): installation tokens and "which installation covers this repo".
	if strings.HasPrefix(auth, "Bearer ") {
		if !s.validJWT(strings.TrimPrefix(auth, "Bearer ")) {
			http.Error(w, `{"message":"bad credentials"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && len(p) == 4 && p[0] == "app" && p[1] == "installations" && p[3] == "access_tokens":
			id, _ := strconv.ParseInt(p[2], 10, 64)
			if !s.installed(id) {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, map[string]any{"token": "ghs_" + p[2], "expires_at": time.Now().Add(time.Hour)})
		case r.Method == http.MethodGet && len(p) == 4 && p[0] == "repos" && p[3] == "installation":
			repo := s.repos[p[1]+"/"+p[2]]
			if repo == nil {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, map[string]any{"id": repo.Installation})
		default:
			http.NotFound(w, r)
		}
		return
	}

	// An installation token sees only its own repos.
	inst, err := strconv.ParseInt(strings.TrimPrefix(auth, "token ghs_"), 10, 64)
	if err != nil || !strings.HasPrefix(auth, "token ghs_") {
		http.Error(w, `{"message":"requires authentication"}`, http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/installation/repositories" {
		var list []map[string]any
		for name, repo := range s.repos {
			if repo.Installation == inst {
				list = append(list, map[string]any{"full_name": name, "default_branch": repo.Branch, "private": true})
			}
		}
		writeJSON(w, map[string]any{"total_count": len(list), "repositories": list})
		return
	}
	if len(p) < 4 || p[0] != "repos" {
		http.NotFound(w, r)
		return
	}
	repo := s.repos[p[1]+"/"+p[2]]
	if repo == nil || repo.Installation != inst {
		http.NotFound(w, r)
		return
	}
	switch {
	case p[3] == "contents" && len(p) >= 5:
		f, ok := repo.Files[strings.Join(p[4:], "/")]
		if !ok || r.URL.Query().Get("ref") != repo.Branch {
			http.NotFound(w, r)
			return
		}
		w.Write(f)
	case p[3] == "releases" && len(p) == 4:
		var out []map[string]any
		for _, rel := range repo.Releases {
			var assets []map[string]any
			for name, b := range rel.Assets {
				assets = append(assets, map[string]any{"id": rel.ID*100 + int64(len(name)), "name": name, "size": len(b), "digest": rel.Digests[name]})
			}
			out = append(out, map[string]any{"id": rel.ID, "tag_name": rel.Tag, "target_commitish": rel.Commit, "published_at": time.Now(), "assets": assets})
		}
		writeJSON(w, out)
	case p[3] == "releases" && len(p) == 6 && p[4] == "assets":
		id, _ := strconv.ParseInt(p[5], 10, 64)
		for _, rel := range repo.Releases {
			for name, b := range rel.Assets {
				if rel.ID*100+int64(len(name)) == id {
					w.Header().Set("Content-Type", "application/octet-stream")
					w.Write(b)
					return
				}
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) installed(id int64) bool {
	for _, r := range s.repos {
		if r.Installation == id {
			return true
		}
	}
	return false
}

func (s *Server) validJWT(raw string) bool {
	jws, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return false
	}
	payload, err := jws.Verify(&s.Key.PublicKey)
	if err != nil {
		return false
	}
	var c struct {
		Iss string
		Exp int64
	}
	return json.Unmarshal(payload, &c) == nil && c.Iss == fmt.Sprint(AppID) && c.Exp > time.Now().Unix()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
