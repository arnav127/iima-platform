package server

import (
	"html/template"
	"net/http"
	"strings"

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
	Portal       bool // the deploy portal is on: offer "Build an app"
}

// Who is the signed-in person's email for the top bar ("" when signed out).
func (d pageData) Who() string {
	if d.Session == nil {
		return ""
	}
	return d.Session.User.Email
}

// WaitingCount is shown to super admins in the top bar; plain pages have no actor.
func (d pageData) WaitingCount() int { return 0 }

// Funcs shared by every page template.
var pageFuncs = template.FuncMap{
	// href links to an app: "/quiz/", or "/" for the home app (never "//", which is another host).
	"href": func(path string) string { return strings.TrimSuffix(path, "/") + "/" },
	// initial is the letter on an app's tile.
	"initial": func(name string) string {
		for _, r := range strings.ToUpper(name) {
			return string(r)
		}
		return "?"
	},
}

// Plain server-rendered pages: no JS, so the gateway's default CSP (no inline scripts) applies.
// One stylesheet for every page, the platform's look: a top bar, then either a narrow card (sign-in
// and messages) or a wide page of sections (directory, dashboard, admin).
var pages = template.Must(template.New("").Funcs(pageFuncs).Parse(`
{{define "top"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · IIMA Students</title>
<style>
:root{color-scheme:light dark;
--fg:#1b1b1f;--muted:#5f6068;--faint:#8b8c94;--bg:#f6f5f3;--card:#fff;--line:#e4e2de;--soft:#efedea;
--accent:#8a1538;--accent-fg:#fff;--accent-soft:#f6e6eb;--ok:#1d7a46;--ok-soft:#e3f3ea;--warn:#9a5b00;--warn-soft:#fbefd9;--bad:#b42318;--bad-soft:#fde8e6;
--radius:14px;--shadow:0 1px 2px rgba(0,0,0,.04),0 4px 16px rgba(0,0,0,.04)}
@media (prefers-color-scheme:dark){:root{
--fg:#f1f0ee;--muted:#a9a9b0;--faint:#7c7c84;--bg:#121214;--card:#1b1b1e;--line:#2d2d32;--soft:#232327;
--accent:#e0577f;--accent-fg:#1b0b10;--accent-soft:#3a1a25;--ok:#5cc58a;--ok-soft:#163323;--warn:#f0b35a;--warn-soft:#3a2a12;--bad:#f2867b;--bad-soft:#3d1a17;
--shadow:none}}
*{box-sizing:border-box}
body{margin:0;font:15px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:var(--fg);background:var(--bg)}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
code{font:13px/1.4 ui-monospace,SFMono-Regular,Consolas,monospace;background:var(--soft);padding:.1rem .35rem;border-radius:6px}
pre{font:12.5px/1.5 ui-monospace,SFMono-Regular,Consolas,monospace;background:var(--soft);padding:1rem;border-radius:10px;overflow:auto;max-height:28rem;white-space:pre-wrap}
h1{font-size:1.55rem;line-height:1.25;margin:0 0 .35rem;letter-spacing:-.01em}h2{font-size:1.1rem;margin:0 0 .75rem}h3{font-size:1rem;margin:1.25rem 0 .5rem}
p{margin:.45rem 0}.muted{color:var(--muted)}.small{font-size:.86rem}

/* top bar */
.bar{background:var(--card);border-bottom:1px solid var(--line)}
.bar .in{max-width:72rem;margin:0 auto;padding:.7rem 1.25rem;display:flex;gap:1.25rem;align-items:center;flex-wrap:wrap}
.brand{display:flex;gap:.55rem;align-items:center;font-weight:650;color:var(--fg);letter-spacing:-.01em}.brand:hover{text-decoration:none}
.mark{height:28px;padding:0 .5rem;border-radius:8px;background:var(--accent);color:var(--accent-fg);display:grid;place-items:center;font-size:.72rem;font-weight:750;letter-spacing:.06em}
.bar nav{display:flex;gap:1rem;flex:1;flex-wrap:wrap}.bar nav a{color:var(--muted);font-size:.93rem}.bar nav a:hover{color:var(--fg);text-decoration:none}
.who{font-size:.86rem;color:var(--muted);display:flex;gap:.75rem;align-items:center}

/* layouts */
main{max-width:72rem;margin:0 auto;padding:2rem 1.25rem 4rem}
main.narrow{max-width:27rem;padding-top:3.5rem}
.card{background:var(--card);border:1px solid var(--line);border-radius:var(--radius);padding:1.5rem;box-shadow:var(--shadow)}
.section{background:var(--card);border:1px solid var(--line);border-radius:var(--radius);padding:1.25rem 1.4rem;margin:1rem 0;box-shadow:var(--shadow)}
.section>h2:first-child{margin-top:0}
.head{display:flex;gap:1rem;justify-content:space-between;align-items:flex-start;flex-wrap:wrap;margin-bottom:1rem}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(15.5rem,1fr));gap:.9rem}
.tile{display:flex;gap:.85rem;align-items:flex-start;background:var(--card);border:1px solid var(--line);border-radius:var(--radius);padding:1rem;color:var(--fg);box-shadow:var(--shadow)}
.tile:hover{border-color:var(--accent);text-decoration:none}
.tile .ic{flex:none;width:40px;height:40px;border-radius:10px;background:var(--accent-soft);color:var(--accent);display:grid;place-items:center;font-weight:700}
.tile b{display:block}.tile .muted{font-size:.84rem}
.subnav{display:flex;gap:.35rem;flex-wrap:wrap;margin:0 0 1rem}.subnav a{padding:.35rem .75rem;border-radius:999px;color:var(--muted);font-size:.9rem}
.subnav a:hover{background:var(--soft);text-decoration:none;color:var(--fg)}.subnav a.on{background:var(--accent-soft);color:var(--accent)}
.hero{padding:1.25rem 0 1.5rem}.hero p{max-width:40rem}
.cta{display:flex;gap:1rem;align-items:center;justify-content:space-between;flex-wrap:wrap;background:var(--accent-soft);border-radius:var(--radius);padding:1rem 1.25rem;margin-top:1.75rem}

/* bits */
.pill{display:inline-block;font-size:.75rem;font-weight:600;padding:.12rem .55rem;border-radius:999px;background:var(--soft);color:var(--muted);vertical-align:middle;white-space:nowrap}
.pill.ok{background:var(--ok-soft);color:var(--ok)}.pill.warn{background:var(--warn-soft);color:var(--warn)}.pill.bad{background:var(--bad-soft);color:var(--bad)}.pill.acc{background:var(--accent-soft);color:var(--accent)}
.note{padding:.7rem .9rem;border-radius:10px;background:var(--soft);margin:.75rem 0;font-size:.93rem}
.note.ok{background:var(--ok-soft);color:var(--ok)}.note.err{background:var(--bad-soft);color:var(--bad)}.note.warn{background:var(--warn-soft);color:var(--warn)}
table{border-collapse:collapse;width:100%;font-size:.92rem}th,td{text-align:left;padding:.55rem .5rem;border-bottom:1px solid var(--line);vertical-align:middle}
th{font-weight:600;color:var(--faint);font-size:.78rem;text-transform:uppercase;letter-spacing:.04em}tr:last-child td{border-bottom:0}
.steps{counter-reset:s;list-style:none;padding:0;margin:0}.steps li{counter-increment:s;position:relative;padding:0 0 1.25rem 2.6rem}
.steps li::before{content:counter(s);position:absolute;left:0;top:0;width:1.8rem;height:1.8rem;border-radius:50%;background:var(--accent-soft);color:var(--accent);display:grid;place-items:center;font-weight:700;font-size:.85rem}
.kv{display:grid;grid-template-columns:max-content 1fr;gap:.35rem 1.25rem;font-size:.93rem}.kv dt{color:var(--muted)}.kv dd{margin:0}

/* forms */
form{margin:.75rem 0}label{display:block;margin:.8rem 0 .3rem;font-size:.88rem;font-weight:550}
.hint{font-size:.8rem;color:var(--faint);margin-top:.25rem}
input,select{width:100%;padding:.55rem .7rem;border:1px solid var(--line);border-radius:9px;font:inherit;background:var(--bg);color:var(--fg)}
input:focus,select:focus{outline:2px solid var(--accent-soft);border-color:var(--accent)}
.cols{display:grid;grid-template-columns:repeat(auto-fit,minmax(16rem,1fr));gap:0 1.25rem}
.inline{display:flex;gap:.5rem;align-items:center;flex-wrap:wrap;margin:.5rem 0}.inline input,.inline select{width:auto;min-width:13rem}
.chk{display:inline-flex;gap:.35rem;align-items:center;margin:0 .75rem 0 0;font-size:.9rem;font-weight:400}.chk input{width:auto}
.row{display:flex;gap:.5rem;flex-wrap:wrap;margin-top:1rem;align-items:center}
.btn{display:inline-flex;align-items:center;gap:.35rem;padding:.55rem 1rem;border-radius:9px;border:1px solid var(--accent);background:var(--accent);color:var(--accent-fg);font:inherit;font-weight:550;cursor:pointer;text-decoration:none;white-space:nowrap}
.btn:hover{text-decoration:none;filter:brightness(1.06)}.btn.alt{background:transparent;color:var(--accent)}.btn.ghost{background:transparent;border-color:var(--line);color:var(--fg)}
.btn.sm{padding:.3rem .7rem;font-size:.84rem;border-radius:8px}.btn.danger{background:transparent;border-color:var(--bad);color:var(--bad)}
.dev{border-top:1px dashed var(--line);margin-top:1.25rem;padding-top:.75rem}
</style></head><body>
<header class="bar"><div class="in">
<a class="brand" href="/"><span class="mark">IIMA</span>Students</a>
<nav><a href="/platform/apps">Apps</a>{{if .Who}}<a href="/admin">Dashboard{{with .WaitingCount}} <span class="pill warn">{{.}} waiting</span>{{end}}</a><a href="/admin/apps/new">Build an app</a>{{end}}</nav>
<div class="who">{{if .Who}}<span>{{.Who}}</span><a href="/auth/logout">Sign out</a>{{else}}<a class="btn sm" href="/auth/login?rd=/admin">Sign in</a>{{end}}</div>
</div></header>
<main{{if not .Wide}} class="narrow"{{end}}>{{if not .Wide}}<div class="card">{{end}}{{end}}
{{define "bottom"}}{{if not .Wide}}</div>{{end}}</main></body></html>{{end}}

{{define "login"}}{{template "top" .}}
<h1>Sign in{{if .App}} to {{.App.Name}}{{end}}</h1>
<p class="muted">One sign-in for every student app on this site.</p>
{{if .Google}}<div class="row"><a class="btn" href="/auth/login?rd={{.RD}}&provider=google{{if .Switch}}&switch=1{{end}}">Continue with Google</a></div>{{end}}
<div class="dev">
<p class="muted small">Dev mode: sign in as anyone. An @{{.MemberDomain}} address counts as an IIMA member; anything else is treated like a Gmail account.</p>
<form method="post" action="/auth/login/dev">
<input type="hidden" name="state" value="{{.State}}">
<label for="email">Email</label><input id="email" name="email" type="email" required placeholder="p25someone@{{.MemberDomain}}">
<label for="name">Name (optional)</label><input id="name" name="name">
<div class="row"><button class="btn" type="submit">Sign in (dev)</button></div>
</form></div>
{{template "bottom" .}}{{end}}

{{define "denied"}}{{template "top" .}}
<h1>{{.App.Name}} needs your @{{.MemberDomain}} account</h1>
<p>You are signed in as <strong>{{.Session.User.Email}}</strong>, which this app does not accept.</p>
<div class="row"><a class="btn" href="{{.Retry}}">Switch account</a><a class="btn ghost" href="/platform/apps">All apps</a></div>
{{template "bottom" .}}{{end}}

{{define "logout"}}{{template "top" .}}
<h1>Sign out</h1>
{{if .Session}}<p>Signed in as <strong>{{.Session.User.Email}}</strong>. This signs you out of every app.</p>
<form method="post" action="/auth/logout"><input type="hidden" name="rd" value="{{.RD}}"><button class="btn" type="submit">Sign out</button></form>
{{else}}<p>You are not signed in.</p><div class="row"><a class="btn ghost" href="{{.RD}}">Back</a></div>{{end}}
{{template "bottom" .}}{{end}}

{{define "error"}}{{template "top" .}}
<h1>{{.Title}}</h1><p>{{.Message}}</p>
<div class="row">{{if .Retry}}<a class="btn" href="{{.Retry}}">Try again</a>{{end}}<a class="btn ghost" href="/platform/apps">All apps</a></div>
{{template "bottom" .}}{{end}}

{{define "home"}}{{template "top" .}}
<div class="hero"><h1>Apps for IIMA students</h1>
<p class="muted">Built by students and clubs, all behind one sign-in with your institute account.</p></div>
{{if .Apps}}<div class="grid">{{range .Apps}}<a class="tile" href="{{href .Path}}"><span class="ic">{{initial .Name}}</span><span><b>{{.Name}}</b>
<span class="muted">{{.Path}}</span>
{{if eq .Login "external"}} <span class="pill acc">open to guests</span>{{end}}{{if eq .Auth "required"}} <span class="pill">sign-in needed</span>{{end}}</span></a>{{end}}</div>
{{else}}<p class="muted">No apps yet.</p>{{end}}
{{if .Portal}}<div class="cta"><div><b>Building something for campus?</b><p class="muted small">Host it here: connect a GitHub repo, and every push to main deploys.</p></div>
<a class="btn" href="/admin/apps/new">Build an app</a></div>{{end}}
{{template "bottom" .}}{{end}}

{{define "notfound"}}{{template "top" .}}
<h1>Page not found</h1><p class="muted">There is nothing at this address.</p><div class="row"><a class="btn ghost" href="/platform/apps">All apps</a></div>
{{template "bottom" .}}{{end}}
`))

func (s *Server) render(w http.ResponseWriter, status int, name string, d pageData) {
	d.Portal = s.cfg.Portal != nil && s.cfg.Portal.Connectable()
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
	s.render(w, http.StatusOK, "home", pageData{Title: "Apps", Session: s.session(r), Apps: s.registry().Apps(), Wide: true})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusNotFound, "notfound", pageData{Title: "Not found", Session: s.session(r)})
}
