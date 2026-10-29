package gateway

import (
	"slices"
	"testing"

	"github.com/arnav127/iima-platform/core/registry"
)

func route(t *testing.T, src string) *registry.App {
	t.Helper()
	a, err := registry.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRoutes(t *testing.T) {
	legacy := route(t, `id = "legacy"
name = "Legacy"
path = "/legacy"
shape = "external"
upstream = "http://10.0.0.5:3010/app"
auth = "none"
public = ["/health"]
`)
	live := route(t, `id = "live"
name = "Live"
path = "/live"
shape = "external"
upstream = "https://10.0.0.6:8443"
auth = "required"
realtime = true
insecure_tls = true
preserve_host = false
`)
	home := route(t, `id = "home"
name = "Home"
path = "/"
shape = "external"
upstream = "http://10.0.0.9:8000"
`)
	app := route(t, `id = "go"
name = "Go"
path = "/go"
shape = "go"
`)
	h := Routes([]*registry.App{legacy, live, home, app}).HTTP

	if _, ok := h.Routers["ext-go"]; ok || len(h.Services) != 3 {
		t.Fatal("platform apps are routed by Nomad, not here")
	}

	r := h.Routers["ext-legacy"]
	want := []string{"strip-identity@file", "strip-session@file", "security-headers-next@file", "compress@file", "ext-legacy-strip", "ext-legacy-prefix", "ext-legacy-reverse"}
	if !slices.Equal(r.Middlewares, want) || r.Priority != registry.PriorityApp || r.Rule != "Path(`/legacy`) || PathPrefix(`/legacy/`)" {
		t.Fatalf("legacy router: %+v", r)
	}
	if h.Middlewares["ext-legacy-prefix"]["addPrefix"].(map[string]any)["prefix"] != "/app" {
		t.Fatal("the upstream's path goes back in front")
	}
	rev := h.Middlewares["ext-legacy-reverse"]["plugin"].(map[string]any)["proxyreverse"].(map[string]any)
	if rev["upstream"] != "http://10.0.0.5:3010/app" || rev["publicPath"] != "/legacy" {
		t.Fatalf("reverse: %v", rev)
	}
	if s := h.Services["ext-legacy"].LoadBalancer; s.Servers[0].URL != "http://10.0.0.5:3010" || !s.PassHostHeader {
		t.Fatalf("legacy service: %+v", s)
	}
	if p := h.Routers["ext-legacy-public-0"]; p.Priority != registry.PriorityAppPublic || slices.Contains(p.Middlewares, "verify-optional@file") {
		t.Fatalf("public path: %+v", p)
	}

	// Sign-in required, realtime (no compression), TLS not checked, upstream Host.
	lr := h.Routers["ext-live"]
	if !slices.Contains(lr.Middlewares, "verify-required@file") || slices.Contains(lr.Middlewares, "compress@file") {
		t.Fatalf("live router: %v", lr.Middlewares)
	}
	if lr.Middlewares[1] != "verify-required@file" || lr.Middlewares[2] != "strip-session@file" {
		t.Fatal("forward auth must run before the session cookie is removed and before the path changes")
	}
	if s := h.Services["ext-live"].LoadBalancer; s.ServersTransport != "ext-insecure" || s.PassHostHeader || !h.ServersTransports["ext-insecure"].InsecureSkipVerify {
		t.Fatalf("live service: %+v", s)
	}
	if _, ok := h.Middlewares["ext-live-prefix"]; ok {
		t.Fatal("an upstream at its root needs no prefix")
	}

	// The home route takes everything else, with no strip.
	hr := h.Routers["ext-home"]
	if hr.Rule != "PathPrefix(`/`)" || hr.Priority != registry.PriorityRoot || slices.Contains(hr.Middlewares, "ext-home-strip") {
		t.Fatalf("home router: %+v", hr)
	}
}

func TestRouteValidation(t *testing.T) {
	for name, src := range map[string]string{
		"no upstream":         "id = \"x1\"\nname = \"X\"\npath = \"/x\"\nshape = \"external\"\n",
		"bad scheme":          "id = \"x1\"\nname = \"X\"\npath = \"/x\"\nshape = \"external\"\nupstream = \"ftp://h\"\n",
		"credentials":         "id = \"x1\"\nname = \"X\"\npath = \"/x\"\nshape = \"external\"\nupstream = \"http://u:p@h\"\n",
		"upstream on an app":  "id = \"x1\"\nname = \"X\"\npath = \"/x\"\nshape = \"go\"\nupstream = \"http://h\"\n",
		"auth none on an app": "id = \"x1\"\nname = \"X\"\npath = \"/x\"\nshape = \"go\"\nauth = \"none\"\n",
		"reserved path":       "id = \"x1\"\nname = \"X\"\npath = \"/admin\"\nshape = \"external\"\nupstream = \"http://h\"\n",
	} {
		if _, err := registry.Parse([]byte(src)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
