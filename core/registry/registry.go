// Package registry loads the app manifests (apps/*.toml). Core uses it to work out which app a
// request is for and what that app's login policy is; the dev stack uses it to generate routes.
package registry

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// App is one app.toml. See ARCHITECTURE.md for what each field means.
type App struct {
	ID       string   `toml:"id"`
	Name     string   `toml:"name"`
	Path     string   `toml:"path"`
	Owners   []string `toml:"owners"`
	Review   string   `toml:"review"`
	Shape    string   `toml:"shape"`
	Login    string   `toml:"login"`
	Auth     string   `toml:"auth"`
	Roles    []string `toml:"roles"`
	Public   []string `toml:"public"`
	Realtime bool     `toml:"realtime"`
	Volume   string   `toml:"volume"`
	Memory   int      `toml:"memory"`

	File string `toml:"-"` // where it was loaded from, for error messages
}

const (
	LoginIIMA     = "iima"
	LoginExternal = "external"
	AuthOptional  = "optional"
	AuthRequired  = "required"
)

// Reserved first path segments that belong to the platform. "/" itself belongs to core too.
var Reserved = []string{"auth", "platform", "admin"}

var (
	idRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
	pathRe = regexp.MustCompile(`^/[a-z0-9][a-z0-9-]*$`)
	shapes = []string{"go-pocketbase", "go", "next", "python", "pocketbase"}
)

// Registry is the set of known apps, keyed by their path.
type Registry struct {
	byPath map[string]*App // "/demo" -> app
	byID   map[string]*App
}

// LoadDirs reads every *.toml in the given directories. Any invalid or conflicting manifest is an
// error: a typo should stop core from starting rather than silently change an app's login policy.
func LoadDirs(dirs ...string) (*Registry, error) {
	var apps []*App
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.toml"))
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			if _, err := os.Stat(dir); err != nil {
				return nil, fmt.Errorf("registry: %w", err)
			}
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			a, err := Parse(b)
			if err != nil {
				return nil, fmt.Errorf("registry: %s: %w", f, err)
			}
			a.File = f
			apps = append(apps, a)
		}
	}
	return New(apps...)
}

// Parse reads one manifest, fills in defaults and validates it on its own.
func Parse(b []byte) (*App, error) {
	var a App
	md, err := toml.NewDecoder(bytes.NewReader(b)).Decode(&a)
	if err != nil {
		return nil, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("unknown field %q", und[0].String())
	}
	if a.Login == "" {
		a.Login = LoginIIMA
	}
	if a.Auth == "" {
		a.Auth = AuthOptional
	}
	return &a, a.validate()
}

func (a *App) validate() error {
	switch {
	case !idRe.MatchString(a.ID):
		return fmt.Errorf("id %q: use 2 to 40 lowercase letters, digits and dashes", a.ID)
	case !pathRe.MatchString(a.Path):
		return fmt.Errorf("path %q: must be a single segment like /myapp", a.Path)
	case slices.Contains(Reserved, a.Path[1:]):
		return fmt.Errorf("path %q is reserved for the platform", a.Path)
	case a.Name == "":
		return fmt.Errorf("name is required")
	case a.Login != LoginIIMA && a.Login != LoginExternal:
		return fmt.Errorf("login %q: must be %q or %q", a.Login, LoginIIMA, LoginExternal)
	case a.Auth != AuthOptional && a.Auth != AuthRequired:
		return fmt.Errorf("auth %q: must be %q or %q", a.Auth, AuthOptional, AuthRequired)
	case !slices.Contains(shapes, a.Shape):
		return fmt.Errorf("shape %q: must be one of %s", a.Shape, strings.Join(shapes, ", "))
	}
	for _, p := range a.Public {
		if !strings.HasPrefix(p, "/") || path.Clean(p) != p {
			return fmt.Errorf("public %q: must be a clean path starting with /, relative to the app path", p)
		}
	}
	return nil
}

// New builds a registry from already-parsed apps, rejecting duplicate ids and paths.
func New(apps ...*App) (*Registry, error) {
	r := &Registry{byPath: map[string]*App{}, byID: map[string]*App{}}
	for _, a := range apps {
		if err := a.validate(); err != nil {
			return nil, fmt.Errorf("registry: app %q: %w", a.ID, err)
		}
		if o := r.byID[a.ID]; o != nil {
			return nil, fmt.Errorf("registry: id %q used by both %s and %s", a.ID, o.File, a.File)
		}
		if o := r.byPath[a.Path]; o != nil {
			return nil, fmt.Errorf("registry: path %q used by both %s and %s", a.Path, o.File, a.File)
		}
		r.byID[a.ID] = a
		r.byPath[a.Path] = a
	}
	return r, nil
}

// Apps returns every app, sorted by path.
func (r *Registry) Apps() []*App {
	out := make([]*App, 0, len(r.byPath))
	for _, a := range r.byPath {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ByID returns the app with this id, or nil.
func (r *Registry) ByID(id string) *App { return r.byID[id] }

// Lookup returns the app that owns a request URI such as "/demo/api/x?y=1", or nil.
// The path is decoded and cleaned first, so "/demo/../other" belongs to "/other", like the
// gateway sees it. Even if the gateway and core ever disagreed, the token's aud would make the
// app that actually receives the request reject it.
func (r *Registry) Lookup(uri string) *App {
	u, err := url.ParseRequestURI(uri)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return nil
	}
	p := path.Clean("/" + u.Path)
	first, _, _ := strings.Cut(p[1:], "/")
	return r.byPath["/"+first]
}
