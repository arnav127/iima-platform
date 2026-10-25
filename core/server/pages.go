package server

import (
	"html/template"
	"net/http"

	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/store"
)

type pageData struct {
	Title        string
	Message      string
	App          *registry.App
	Apps         []*registry.App
	Session      *store.Session
	State        string
	RD           string
	Retry        string
	Switch       bool
	Google       bool
	MemberDomain string
	Wide         bool
}

// Plain server-rendered pages: no JS, so the gateway's default CSP (no inline scripts) applies.
var pages = template.Must(template.New("").Parse(`
{{define "top"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · IIMA students</title>
<style>
:root{color-scheme:light dark;--fg:#1d1d1f;--muted:#6e6e73;--bg:#fafafa;--card:#fff;--line:#e3e3e6;--accent:#8a1538}
@media (prefers-color-scheme:dark){:root{--fg:#f2f2f2;--muted:#a1a1a6;--bg:#141416;--card:#1e1e21;--line:#333338;--accent:#e0577f}}
body{margin:0;font:16px/1.5 system-ui,sans-serif;color:var(--fg);background:var(--bg)}
main{max-width:30rem;margin:3rem auto;padding:0 1rem}main.wide{max-width:64rem}
table{border-collapse:collapse;width:100%;font-size:.92rem}th,td{text-align:left;padding:.45rem .5rem;border-bottom:1px solid var(--line);vertical-align:top}th{font-weight:600;color:var(--muted)}
.inline{display:inline-flex;gap:.5rem;align-items:center;flex-wrap:wrap;margin:0}.inline input[type=email],.inline input[type=text],.inline input[type=number],.inline select{width:auto;min-width:14rem}
.chk{display:inline-flex;gap:.25rem;align-items:center;margin:0 .5rem 0 0;font-size:.9rem}.chk input{width:auto}.btn.sm{padding:.3rem .7rem;font-size:.85rem}
.note{padding:.6rem .8rem;border-radius:8px;background:var(--bg);border:1px solid var(--line);margin:.75rem 0}.err{border-color:var(--accent)}nav.admin{display:flex;gap:1rem;flex-wrap:wrap;margin-bottom:1rem;font-size:.95rem}code{font-size:.85em}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:1.5rem}
h1{font-size:1.4rem;margin:0 0 .75rem}p{margin:.5rem 0}.muted{color:var(--muted);font-size:.9rem}
a{color:var(--accent)}ul{padding-left:1.2rem}
.btn{display:inline-block;padding:.55rem 1rem;border-radius:8px;border:1px solid var(--accent);background:var(--accent);color:#fff;text-decoration:none;font:inherit;cursor:pointer}
.btn.alt{background:transparent;color:var(--accent)}
form{margin:1rem 0}label{display:block;margin:.5rem 0 .2rem;font-size:.9rem}
input{width:100%;box-sizing:border-box;padding:.5rem;border:1px solid var(--line);border-radius:8px;font:inherit;background:var(--bg);color:var(--fg)}
.row{display:flex;gap:.5rem;flex-wrap:wrap;margin-top:1rem}.dev{border-top:1px dashed var(--line);margin-top:1.25rem;padding-top:.5rem}
</style></head><body><main{{if .Wide}} class="wide"{{end}}><div class="card">{{end}}
{{define "bottom"}}</div></main></body></html>{{end}}

{{define "login"}}{{template "top" .}}
<h1>Sign in{{if .App}} to {{.App.Name}}{{end}}</h1>
{{if .Google}}<div class="row"><a class="btn" href="/auth/login?rd={{.RD}}&provider=google{{if .Switch}}&switch=1{{end}}">Continue with Google</a></div>{{end}}
<div class="dev">
<p class="muted">Dev mode: sign in as anyone. An @{{.MemberDomain}} address counts as an IIMA member; anything else is treated like a Gmail account.</p>
<form method="post" action="/auth/login/dev">
<input type="hidden" name="state" value="{{.State}}">
<label for="email">Email</label><input id="email" name="email" type="email" required placeholder="p25someone@{{.MemberDomain}}">
<label for="name">Name (optional)</label><input id="name" name="name">
<div class="row"><button class="btn" type="submit">Sign in (dev)</button></div>
</form></div>
{{template "bottom"}}{{end}}

{{define "denied"}}{{template "top" .}}
<h1>{{.App.Name}} needs your @{{.MemberDomain}} account</h1>
<p>You are signed in as <strong>{{.Session.User.Email}}</strong>, which this app does not accept.</p>
<div class="row"><a class="btn" href="{{.Retry}}">Switch account</a><a class="btn alt" href="/">Home</a></div>
{{template "bottom"}}{{end}}

{{define "logout"}}{{template "top" .}}
<h1>Sign out</h1>
{{if .Session}}<p>Signed in as <strong>{{.Session.User.Email}}</strong>. This signs you out of every app.</p>
<form method="post" action="/auth/logout"><input type="hidden" name="rd" value="{{.RD}}"><button class="btn" type="submit">Sign out</button></form>
{{else}}<p>You are not signed in.</p><div class="row"><a class="btn alt" href="{{.RD}}">Back</a></div>{{end}}
{{template "bottom"}}{{end}}

{{define "error"}}{{template "top" .}}
<h1>{{.Title}}</h1><p>{{.Message}}</p>
<div class="row">{{if .Retry}}<a class="btn" href="{{.Retry}}">Try again</a>{{end}}<a class="btn alt" href="/">Home</a></div>
{{template "bottom"}}{{end}}

{{define "home"}}{{template "top" .}}
<h1>IIMA student apps</h1>
{{if .Session}}<p class="muted">Signed in as {{.Session.User.Email}} · <a href="/auth/logout">Sign out</a></p>
{{else}}<p class="muted"><a href="/auth/login">Sign in</a></p>{{end}}
<ul>{{range .Apps}}<li><a href="{{.Path}}/">{{.Name}}</a>{{if eq .Login "external"}} <span class="muted">(open to guests)</span>{{end}}</li>{{end}}</ul>
{{template "bottom"}}{{end}}

{{define "notfound"}}{{template "top" .}}
<h1>Page not found</h1><p>There is nothing at this address.</p><div class="row"><a class="btn alt" href="/">Home</a></div>
{{template "bottom"}}{{end}}
`))

func (s *Server) render(w http.ResponseWriter, status int, name string, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, name, d); err != nil {
		s.cfg.Log.Error("render", "page", name, "err", err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, what string, err error) {
	s.cfg.Log.Error(what, "err", err)
	s.render(w, http.StatusInternalServerError, "error", pageData{Title: "Something went wrong", Message: "Please try again in a moment."})
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "home", pageData{Title: "Home", Session: s.session(r), Apps: s.cfg.Registry.Apps()})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusNotFound, "notfound", pageData{Title: "Not found"})
}
