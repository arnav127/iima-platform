package proxyreverse

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustNew(t *testing.T, cfg *Config, next http.Handler) http.Handler {
	t.Helper()
	h, err := New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestLocationAndCookies(t *testing.T) {
	cfg := &Config{Upstream: "http://10.0.0.5:3010/app", UpstreamPath: "/app", PublicPath: "/legacy"}
	cases := map[string]string{
		"http://10.0.0.5:3010/app/login?x=1":       "/legacy/login?x=1",
		"http://10.0.0.5:3010/app":                 "/legacy/",
		"https://students.iima.ac.in/app/x":        "https://students.iima.ac.in/legacy/x",
		"/app/x#top":                               "/legacy/x#top",
		"/elsewhere":                               "/elsewhere",
		"http://10.0.0.5:3010/other":               "http://10.0.0.5:3010/other",
		"https://accounts.google.com/o/oauth2?a=b": "https://accounts.google.com/o/oauth2?a=b",
		"//evil.example/app/x":                     "//evil.example/app/x",
		"relative/path":                            "relative/path",
	}
	for in, want := range cases {
		h := mustNew(t, cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", in)
			w.Header().Add("Set-Cookie", "s=1; Path=/app; HttpOnly")
			w.Header().Add("Set-Cookie", "t=2; path=/app/sub")
			w.Header().Add("Set-Cookie", "u=3; Path=/other")
			w.WriteHeader(http.StatusFound)
		}))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "https://students.iima.ac.in/legacy/", nil)
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("Location %q -> %q, want %q", in, got, want)
		}
		c := rec.Header().Values("Set-Cookie")
		if c[0] != "s=1; Path=/legacy; HttpOnly" || c[1] != "t=2; Path=/legacy/sub" || c[2] != "u=3; Path=/other" {
			t.Errorf("cookies: %q", c)
		}
	}
}

func TestSamePathAndRoot(t *testing.T) {
	// Same path on both sides (ProxyPass /bm http://h:3010/bm): only the upstream's own host changes.
	same := mustNew(t, &Config{Upstream: "http://127.0.0.1:3010/bm", UpstreamPath: "/bm", PublicPath: "/bm"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", r.URL.Query().Get("to"))
		w.Header().Add("Set-Cookie", "s=1; Path=/bm")
		w.WriteHeader(http.StatusFound)
	}))
	for in, want := range map[string]string{"http://127.0.0.1:3010/bm/a": "/bm/a", "/bm/a": "/bm/a", "https://students.iima.ac.in/bm/a": "https://students.iima.ac.in/bm/a"} {
		rec := httptest.NewRecorder()
		same.ServeHTTP(rec, httptest.NewRequest("GET", "https://students.iima.ac.in/bm/?to="+in, nil))
		if got := rec.Header().Get("Location"); got != want || rec.Header().Get("Set-Cookie") != "s=1; Path=/bm" {
			t.Errorf("same path: %q -> %q (%q)", in, got, rec.Header().Get("Set-Cookie"))
		}
	}
	// The home route at / with an upstream at its root.
	root := mustNew(t, &Config{Upstream: "http://10.0.0.9:8000", PublicPath: ""}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://10.0.0.9:8000/mess?d=1")
		w.WriteHeader(http.StatusFound)
	}))
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest("GET", "https://students.iima.ac.in/", nil))
	if got := rec.Header().Get("Location"); got != "/mess?d=1" {
		t.Errorf("root: %q", got)
	}
}

func TestStreamsPassThrough(t *testing.T) {
	// Server-sent events: each event reaches the client as it is flushed, not at the end.
	h := mustNew(t, &Config{Upstream: "http://up", PublicPath: "/x"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 3; i++ {
			io.WriteString(w, "data: tick\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	start := time.Now()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: tick\n" || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("first event %q after %v (%v): not flushed", line, time.Since(start), err)
	}

	// Websockets: the upgrade gets the raw connection.
	ws := mustNew(t, &Config{Upstream: "http://up", PublicPath: "/x"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\nhello")
		buf.Flush()
	}))
	srv2 := httptest.NewServer(ws)
	defer srv2.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(srv2.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b, _ := io.ReadAll(c)
	if !strings.HasPrefix(string(b), "HTTP/1.1 101") || !strings.HasSuffix(string(b), "hello") {
		t.Fatalf("upgrade: %q", b)
	}
}

func TestBadConfig(t *testing.T) {
	if _, err := New(context.Background(), http.NotFoundHandler(), &Config{}, "x"); err == nil {
		t.Fatal("missing upstream accepted")
	}
}
