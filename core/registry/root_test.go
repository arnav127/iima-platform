package registry

import "testing"

func TestRootApp(t *testing.T) {
	home := mustApp(t, `id = "home"
name = "Home"
path = "/"
shape = "go"
public = ["/api/health"]
`)
	if home.Strips() {
		t.Fatal("the home app must never have its path stripped")
	}
	r, err := New(home, mustApp(t, `id = "demo"
name = "D"
path = "/demo"
shape = "go"
`))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/":              "home",
		"/mess-menu":     "home",
		"/events/12?x=1": "home",
		"/demo/x":        "demo",
		"/demo":          "demo",
		"/demox":         "home",
		"/auth/login":    "",
		"/admin/apps":    "",
		"/platform/404":  "",
	}
	for uri, want := range cases {
		got := ""
		if a := r.Lookup(uri); a != nil {
			got = a.ID
		}
		if got != want {
			t.Errorf("Lookup(%q) = %q, want %q", uri, got, want)
		}
	}

	// The home app's routes rank below every other app's, public paths included.
	demo := r.ByID("demo")
	if home.Rule("") != "PathPrefix(`/`)" || home.Rule("/api/health") != "Path(`/api/health`) || PathPrefix(`/api/health/`)" {
		t.Fatalf("rules: %q %q", home.Rule(""), home.Rule("/api/health"))
	}
	if !(home.Priority(false) < home.Priority(true) && home.Priority(true) < demo.Priority(false)) || home.Priority(false) <= 1 {
		t.Fatalf("priorities: home %d/%d, demo %d", home.Priority(false), home.Priority(true), demo.Priority(false))
	}

	// Only one home app.
	if _, err := New(home, mustApp(t, `id = "home2"
name = "H"
path = "/"
shape = "go"
`)); err == nil {
		t.Fatal("two apps at / accepted")
	}
}

func TestNoRootMeansNoFallback(t *testing.T) {
	r, _ := New(mustApp(t, `id = "demo"
name = "D"
path = "/demo"
shape = "go"
`))
	if r.Lookup("/anything") != nil {
		t.Fatal("unknown path matched without a home app")
	}
}
