package registry

import (
	"strings"
	"testing"
)

func mustApp(t *testing.T, src string) *App {
	t.Helper()
	a, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestParseDefaultsAndErrors(t *testing.T) {
	a := mustApp(t, `id = "demo"
name = "Demo"
path = "/demo"
shape = "go"`)
	if a.Login != LoginIIMA || a.Auth != AuthOptional {
		t.Fatalf("defaults: login %q auth %q", a.Login, a.Auth)
	}

	bad := map[string]string{
		"reserved path":  `id="x1" name="X" path="/auth" shape="go"`,
		"nested path":    `id="x1" name="X" path="/a/b" shape="go"`,
		"bad login":      `id="x1" name="X" path="/x" shape="go" login="microsoft"`,
		"bad auth":       `id="x1" name="X" path="/x" shape="go" auth="none"`,
		"typo in field":  `id="x1" name="X" path="/x" shape="go" logn="external"`,
		"bad id":         `id="X" name="X" path="/x" shape="go"`,
		"unclean public": `id="x1" name="X" path="/x" shape="go" public=["/api/../admin"]`,
	}
	for name, src := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(strings.ReplaceAll(src, " ", "\n"))); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestDuplicates(t *testing.T) {
	a := mustApp(t, "id=\"one\"\nname=\"A\"\npath=\"/x\"\nshape=\"go\"")
	b := mustApp(t, "id=\"two\"\nname=\"B\"\npath=\"/x\"\nshape=\"go\"")
	if _, err := New(a, b); err == nil {
		t.Fatal("duplicate path accepted")
	}
}

func TestLookup(t *testing.T) {
	r, err := New(
		mustApp(t, "id=\"demo\"\nname=\"D\"\npath=\"/demo\"\nshape=\"go\""),
		mustApp(t, "id=\"demo-external\"\nname=\"E\"\npath=\"/demo-external\"\nshape=\"go\"\nlogin=\"external\""),
	)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/demo":                    "demo",
		"/demo/":                   "demo",
		"/demo/api/x?y=1":          "demo",
		"/demo-external/x":         "demo-external",
		"/demox":                   "",
		"/":                        "",
		"/auth/login":              "",
		"/demo/../demo-external/x": "demo-external",
		"/demo-external/../demo":   "demo",
		"/%64emo/x":                "demo",
		"//demo":                   "demo", // the gateway cleans // too
		"http://evil/demo":         "",
		"not a uri":                "",
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
}

func TestLoadRepoRegistry(t *testing.T) {
	// The checked-in manifests must always load together.
	if _, err := LoadDirs("../../apps", "../../dev/apps"); err != nil {
		t.Fatal(err)
	}
}
