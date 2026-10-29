package server

import (
	"html/template"
	"net/http"
)

// /admin/github: a super admin creates the platform's GitHub App in two clicks (GitHub's manifest
// flow). The gateway gives this one page a policy that lets its form post to github.com.

type githubData struct {
	deployData
	Slug       string
	InstallURL string
	Action     string // where the manifest is posted
	Manifest   string
	Name, Org  string
}

var githubPages = template.Must(template.Must(deployPages.Clone()).Parse(`
{{define "github"}}{{template "top" .}}
<div class="head"><div><h1>GitHub App</h1><p class="muted">The platform reads app repos and their builds through one GitHub App that owners install on their repo.</p></div>
<a class="btn ghost" href="/admin">Dashboard</a></div>
{{template "flash" .}}
{{if .Slug}}<div class="section"><h2>Connected <span class="pill ok">on</span></h2>
<dl class="kv"><dt>App</dt><dd><a href="https://github.com/apps/{{.Slug}}">{{.Slug}}</a></dd><dt>Install link for owners</dt><dd><a href="{{.InstallURL}}">{{.InstallURL}}</a></dd></dl>
<p class="hint">To start over (for example after the key leaked): delete the App on GitHub, clear the github_* settings in core's database, restart core, and create a new one here.</p></div>
{{else if .Action}}<div class="section"><h2>Create it on GitHub</h2>
<p>GitHub will ask you to confirm the App <b>{{.Name}}</b>{{if .Org}} in the <b>{{.Org}}</b> organization{{end}}, then send you back here. Core keeps its key; nobody copies it by hand.</p>
<p class="muted small">It asks for read-only access to contents and metadata of the repos owners pick, and release events. Nothing else.</p>
<form method="post" action="{{.Action}}"><input type="hidden" name="manifest" value="{{.Manifest}}"><div class="row"><button class="btn" type="submit">Continue to GitHub</button><a class="btn ghost" href="/admin/github">Cancel</a></div></form></div>
{{else}}<div class="section"><h2>Set it up</h2>
<p class="muted">No GitHub App yet, so nobody can connect a repo. Create one now; it takes a minute.</p>
<form method="get" action="/admin/github"><div class="cols">
<div><label>App name</label><input name="name" required value="IIMA Students Platform"><div class="hint">Must be unique on GitHub.</div></div>
<div><label>GitHub organization (optional)</label><input name="org" placeholder="leave empty for your own account"><div class="hint">An organization outlives any one maintainer: prefer one the institute or a club controls.</div></div>
</div><div class="row"><button class="btn" type="submit">Next</button></div></form></div>{{end}}
{{template "bottom" .}}{{end}}
`))

func (s *Server) githubRoutes(mux *http.ServeMux) {
	p := s.cfg.Portal
	if p == nil {
		return
	}
	superOnly := func(w http.ResponseWriter, r *http.Request) *actor {
		a := s.pageActor(w, r)
		if a != nil && !a.Super {
			s.done(w, r, "", errForbidden, "")
			return nil
		}
		return a
	}
	mux.HandleFunc("GET /admin/github", func(w http.ResponseWriter, r *http.Request) {
		a := superOnly(w, r)
		if a == nil {
			return
		}
		d := githubData{deployData: deployData{Title: "GitHub App", Actor: a, Wide: true}, Slug: p.GitHubSlug(), InstallURL: p.InstallURL()}
		d.Message, d.Error = flash(r)
		if d.Slug == "" && r.URL.Query().Get("name") != "" {
			d.Name, d.Org = r.URL.Query().Get("name"), r.URL.Query().Get("org")
			action, manifest, err := p.StartManifest(r.Context(), a.Email, d.Name, d.Org)
			if err != nil {
				s.portalDone(w, r, "/admin/github", err, "")
				return
			}
			d.Action, d.Manifest = action, string(manifest)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := githubPages.ExecuteTemplate(w, "github", d); err != nil {
			s.cfg.Log.Error("render", "page", "github", "err", err)
		}
	})
	// GitHub sends the super admin back here with a one-time code.
	mux.HandleFunc("GET /admin/github/created", func(w http.ResponseWriter, r *http.Request) {
		a := superOnly(w, r)
		if a == nil {
			return
		}
		err := p.FinishManifest(r.Context(), a.Email, r.URL.Query().Get("code"), r.URL.Query().Get("state"))
		s.portalDone(w, r, "/admin/github", err, "The GitHub App is ready. Owners can connect repos now.")
	})
}
