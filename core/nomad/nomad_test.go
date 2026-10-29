package nomad

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/arnav127/iima-platform/core/jobgen"
)

// fakeNomad keeps one variable and records jobs and volumes, checking the token on every call.
type fakeNomad struct {
	mu      sync.Mutex
	items   map[string]string
	index   uint64
	jobs    []string
	volumes []string
}

func (f *fakeNomad) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Job jobgen.Job }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.jobs = append(f.jobs, body.Job.ID)
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"EvalID": "e1"})
	})
	mux.HandleFunc("/v1/var/nomad/jobs/quiz", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if f.index == 0 {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Items": f.items, "ModifyIndex": f.index})
		case http.MethodPut, http.MethodDelete:
			if r.URL.Query().Get("cas") != jsonNum(f.index) {
				http.Error(w, "cas conflict", http.StatusConflict)
				return
			}
			var body struct{ Items map[string]string }
			json.NewDecoder(r.Body).Decode(&body)
			f.items, f.index = body.Items, f.index+1
		}
	})
	mux.HandleFunc("GET /v1/volumes", func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]string
		for _, v := range f.volumes {
			out = append(out, map[string]string{"Name": v})
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("PUT /v1/volume/host/create", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Volume struct{ Name, PluginID string }
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.volumes = append(f.volumes, body.Volume.Name)
		w.Write([]byte("{}"))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Nomad-Token") != "tok" {
			http.Error(w, "Permission denied", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func jsonNum(n uint64) string { b, _ := json.Marshal(n); return string(b) }

func TestClient(t *testing.T) {
	f := &fakeNomad{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := &Client{Addr: srv.URL, Token: "tok"}
	ctx := context.Background()

	if id, err := c.SubmitJob(ctx, &jobgen.Job{ID: "quiz"}); err != nil || id != "e1" || f.jobs[0] != "quiz" {
		t.Fatalf("submit: %q %v %v", id, err, f.jobs)
	}

	a, b := "1", "2"
	path := VariablePath("quiz")
	if err := c.SetSecret(ctx, path, "A", &a); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSecret(ctx, path, "B", &b); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSecret(ctx, path, "A", nil); err != nil {
		t.Fatal(err)
	}
	if items, _, _ := c.Variable(ctx, path); len(items) != 1 || items["B"] != "2" {
		t.Fatalf("variable: %v", items)
	}

	for range 2 {
		if err := c.EnsureHostVolume(ctx, "quiz-data"); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.volumes) != 1 {
		t.Fatalf("volume created %d times", len(f.volumes))
	}

	bad := &Client{Addr: srv.URL, Token: "nope"}
	if _, err := bad.SubmitJob(ctx, &jobgen.Job{ID: "x"}); err == nil {
		t.Fatal("wrong token accepted")
	}
}
