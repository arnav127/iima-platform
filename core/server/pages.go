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

// Shell is false for plain pages (sign-in, directory): they get the slim top bar, not the admin
// sidebar.
func (d pageData) Shell() bool   { return false }
func (d pageData) IsSuper() bool { return false }

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
{{define "icon"}}<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">{{if eq . "dashboard"}}<rect x="3" y="3" width="7" height="9"/><rect x="14" y="3" width="7" height="5"/><rect x="14" y="12" width="7" height="9"/><rect x="3" y="16" width="7" height="5"/>{{else if eq . "plus"}}<path d="M12 5v14M5 12h14"/>{{else if eq . "routes"}}<path d="M4 7h11l3 3-3 3H4z"/><path d="M20 17H9l-3-3"/>{{else if eq . "people"}}<circle cx="9" cy="8" r="4"/><path d="M2 21c0-4 3-6 7-6s7 2 7 6"/><path d="M17 11a3 3 0 1 0 0-6M22 21c0-3-2-5-4-5.5"/>{{else if eq . "shield"}}<path d="M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z"/>{{else if eq . "log"}}<path d="M6 3h9l4 4v14H6z"/><path d="M9 12h7M9 16h7M9 8h3"/>{{else if eq . "github"}}<path d="M9 19c-4 1.5-4-2-6-2.5M15 21v-3.5a3 3 0 0 0-1-2.5c3 0 6-1.5 6-6.5a5 5 0 0 0-1.5-3.5 4.5 4.5 0 0 0-.1-3.5s-1.2-.4-3.9 1.4a13 13 0 0 0-7 0C4.8 1.6 3.6 2 3.6 2a4.5 4.5 0 0 0-.1 3.5A5 5 0 0 0 2 9c0 5 3 6.5 6 6.5a3 3 0 0 0-1 2.5V21"/>{{else if eq . "apps"}}<rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/>{{else if eq . "theme"}}<circle cx="12" cy="12" r="9"/><path d="M12 3a9 9 0 0 0 0 18z" fill="currentColor"/>{{else if eq . "out"}}<path d="M15 4h4v16h-4M10 8l-4 4 4 4M6 12h10"/>{{end}}</svg>{{end}}

{{define "top"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · IIMA Students</title>
<script src="/platform/theme.js"></script>
<style>
:root{color-scheme:light;
--fg:#1d1d1d;--muted:#6b6b68;--faint:#8e8e8a;--bg:#dcdcd9;--side:#e3e3e0;--panel:#efefec;--card:#f8f8f4;
--rule:#1d1d1d;--line:#d5d5d0;--soft:#e7e7e3;--accent:#eadc62;--accent-fg:#1d1d1d;--ink:#1d1d1d;--ink-fg:#fafaf7;
--grey:#b9b9b5;--grey-2:#6f6f6c;--ok:#1d1d1d;--bad:#b42318;--bad-soft:#f6e1de;--radius:4px}
/* dark: the system's choice, unless the theme button picked one (data-theme on <html>) */
@media (prefers-color-scheme:dark){:root:not([data-theme=light]){color-scheme:dark;
--fg:#ececea;--muted:#a3a3a0;--faint:#7d7d7a;--bg:#0f0f0f;--side:#161616;--panel:#1d1d1d;--card:#242424;
--rule:#ececea;--line:#363636;--soft:#2b2b2b;--accent:#e6d457;--accent-fg:#141414;--ink:#ececea;--ink-fg:#141414;
--grey:#5c5c5a;--grey-2:#8f8f8c;--bad:#f2867b;--bad-soft:#3d1a17}}
:root[data-theme=dark]{color-scheme:dark;
--fg:#ececea;--muted:#a3a3a0;--faint:#7d7d7a;--bg:#0f0f0f;--side:#161616;--panel:#1d1d1d;--card:#242424;
--rule:#ececea;--line:#363636;--soft:#2b2b2b;--accent:#e6d457;--accent-fg:#141414;--ink:#ececea;--ink-fg:#141414;
--grey:#5c5c5a;--grey-2:#8f8f8c;--bad:#f2867b;--bad-soft:#3d1a17}
*{box-sizing:border-box}
body{margin:0;font:14px/1.5 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:var(--fg);background:var(--bg)}
a{color:inherit;text-decoration:none}a:hover{text-decoration:underline}
code{font:12.5px/1.4 ui-monospace,SFMono-Regular,Consolas,monospace;background:var(--soft);padding:.1rem .35rem;border-radius:3px}
pre{font:12.5px/1.5 ui-monospace,SFMono-Regular,Consolas,monospace;background:var(--soft);padding:1rem;border-radius:var(--radius);overflow:auto;max-height:28rem;white-space:pre-wrap}
h1{font-size:1.75rem;line-height:1.2;margin:0;letter-spacing:-.015em;font-weight:700}
h2{font-size:1.02rem;font-weight:650;margin:0 0 1rem;padding-bottom:.6rem;border-bottom:2px solid var(--rule)}
h3{font-size:.95rem;margin:1.25rem 0 .5rem}
p{margin:.45rem 0}.muted{color:var(--muted)}.small{font-size:.84rem}

/* public pages: a slim top bar */
.bar{background:var(--side);border-bottom:1px solid var(--line)}
.bar .in{max-width:72rem;margin:0 auto;padding:.65rem 1.25rem;display:flex;gap:1.25rem;align-items:center;flex-wrap:wrap}
.brand{display:flex;gap:.55rem;align-items:center;font-weight:700;text-decoration:none}.brand:hover{text-decoration:none}
.mark{height:28px;padding:0 .5rem;border-radius:3px;background:var(--accent);color:var(--accent-fg);display:grid;place-items:center;font-size:.72rem;font-weight:800;letter-spacing:.06em;border:1.5px solid var(--rule)}
.bar nav{display:flex;gap:1.1rem;flex:1;flex-wrap:wrap}.bar nav a{color:var(--muted);text-decoration:none}.bar nav a:hover{color:var(--fg)}
.who{font-size:.86rem;color:var(--muted);display:flex;gap:.75rem;align-items:center}
main.pub{max-width:72rem;margin:0 auto;padding:2rem 1.25rem 4rem}main.narrow{max-width:27rem;padding-top:3.5rem}

/* admin shell: sidebar and main panel */
.app{display:grid;grid-template-columns:240px 1fr;min-height:100vh}
.side{background:var(--side);padding:1.25rem .9rem;display:flex;flex-direction:column;gap:.4rem;border-right:1px solid var(--line)}
.side .title{font-weight:700;font-size:1.08rem;padding:0 .4rem .5rem}
.seg{display:grid;grid-template-columns:1fr 1fr;border:1.5px solid var(--rule);margin:0 .2rem .8rem;font-size:.85rem}
.seg a{text-align:center;padding:.35rem 0;text-decoration:none}.seg a.on{background:var(--ink);color:var(--ink-fg)}
.side nav a{display:flex;gap:.6rem;align-items:center;padding:.42rem .6rem;border-radius:3px;color:var(--muted);text-decoration:none}
.side nav a:hover{color:var(--fg);background:var(--soft)}.side nav a.on{background:var(--card);color:var(--fg);font-weight:550;box-shadow:0 1px 2px rgba(0,0,0,.05)}
.side nav a .count{margin-left:auto}
.side hr{border:0;border-top:1px solid var(--grey);margin:.6rem .4rem}
.side .me{margin-top:auto;font-size:.8rem;color:var(--muted);padding:0 .6rem;word-break:break-all}
.main{background:var(--panel);padding:1.5rem 2rem 4rem;min-width:0}
.main>.head{border-bottom:2px solid var(--rule);padding-bottom:1rem;margin-bottom:1.5rem}
@media (max-width:860px){.app{grid-template-columns:1fr}.side{border-right:0;border-bottom:1px solid var(--line)}.side nav{display:flex;flex-wrap:wrap;gap:.2rem}.side hr,.side .me{display:none}.main{padding:1.25rem 1rem 3rem}}
/* blocks */
.card{background:var(--card);border:1px solid var(--line);border-radius:var(--radius);padding:1.5rem}
.section{background:var(--card);border-radius:var(--radius);padding:1.2rem 1.4rem 1.3rem;margin:0 0 1rem;box-shadow:0 1px 2px rgba(0,0,0,.04)}
.head{display:flex;gap:1rem;justify-content:space-between;align-items:flex-end;flex-wrap:wrap;margin-bottom:1rem}
.head p{margin:.35rem 0 0}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(15.5rem,1fr));gap:.8rem}
.tile{display:flex;gap:.85rem;align-items:flex-start;background:var(--card);border-radius:var(--radius);padding:1rem;text-decoration:none;box-shadow:0 1px 2px rgba(0,0,0,.04);border-left:3px solid var(--grey)}
.tile:hover{border-left-color:var(--accent);text-decoration:none}
.tile .ic{flex:none;width:38px;height:38px;border:1.5px solid var(--rule);background:var(--accent);color:var(--accent-fg);display:grid;place-items:center;font-weight:800;border-radius:2px}
.tile b{display:block}.tile .muted{font-size:.82rem}
.subnav{display:flex;gap:1.1rem;flex-wrap:wrap;margin:0 0 1.25rem;border-bottom:1px solid var(--line)}
.subnav a{padding:.4rem 0;color:var(--muted);text-decoration:none;border-bottom:2px solid transparent;margin-bottom:-1px}.subnav a:hover{color:var(--fg)}.subnav a.on{color:var(--fg);border-color:var(--rule)}
.hero{padding:1rem 0 1.5rem;border-bottom:2px solid var(--rule);margin-bottom:1.5rem}.hero p{max-width:40rem}
.cta{display:flex;gap:1rem;align-items:center;justify-content:space-between;flex-wrap:wrap;background:var(--card);border-left:3px solid var(--accent);padding:1rem 1.25rem;margin-top:1.75rem}

/* numbers: label under a big value, a coloured marker on the left */
.stats{display:grid;grid-template-columns:repeat(auto-fit,minmax(9rem,1fr));gap:1.25rem;margin-bottom:1rem}
.stat{border-left:3px solid var(--grey);padding:.1rem 0 .1rem .75rem}.stat.y{border-color:var(--accent)}.stat.d{border-color:var(--ink)}.stat.g2{border-color:var(--grey-2)}
.stat b{display:block;font-size:1.35rem;line-height:1.2}.stat span{font-size:.7rem;letter-spacing:.06em;text-transform:uppercase;color:var(--muted)}
.bars{display:flex;height:28px;border-radius:2px;overflow:hidden;background:var(--soft)}
.bars i{display:block}.bars .y{background:var(--accent)}.bars .g{background:var(--grey)}.bars .g2{background:var(--grey-2)}.bars .d{background:var(--ink)}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(13rem,1fr));gap:1rem;margin-bottom:1rem}
.kpi{background:var(--card);border-radius:var(--radius);padding:1rem 1.1rem;box-shadow:0 1px 2px rgba(0,0,0,.04)}
.kpi span{display:block;font-size:.7rem;letter-spacing:.06em;text-transform:uppercase;color:var(--muted);margin-top:.9rem}.kpi b{font-size:1.3rem}

/* tags */
.pill{display:inline-block;font-size:.66rem;font-weight:700;letter-spacing:.05em;text-transform:uppercase;padding:.12rem .4rem;border-radius:2px;color:var(--fg);vertical-align:middle;white-space:nowrap}
.pill.ok{background:var(--accent);color:var(--accent-fg)}.pill.warn{border:1.5px solid var(--rule);padding:.05rem .35rem}.pill.bad{background:var(--ink);color:var(--ink-fg)}.pill.acc{background:var(--soft)}
.note{padding:.65rem .9rem;margin:.75rem 0 1rem;font-size:.92rem;background:var(--card);border-left:3px solid var(--grey)}
.note.ok{border-color:var(--accent)}.note.err{border-color:var(--bad);color:var(--bad)}.note.warn{border-color:var(--ink)}
table{border-collapse:collapse;width:100%;font-size:.9rem}th,td{text-align:left;padding:.65rem .5rem;border-bottom:1px solid var(--line);vertical-align:middle}
th{font-weight:600;color:var(--muted);font-size:.68rem;text-transform:uppercase;letter-spacing:.07em;border-bottom:1.5px solid var(--rule)}tr:last-child td{border-bottom:0}
td b a,td a b{text-decoration:none}
.steps{counter-reset:s;list-style:none;padding:0;margin:0}.steps li{counter-increment:s;position:relative;padding:0 0 1.25rem 2.6rem}
.steps li::before{content:counter(s);position:absolute;left:0;top:0;width:1.8rem;height:1.8rem;border:1.5px solid var(--rule);background:var(--accent);color:var(--accent-fg);display:grid;place-items:center;font-weight:800;font-size:.85rem;border-radius:2px}
.kv{display:grid;grid-template-columns:max-content 1fr;gap:.45rem 1.5rem;font-size:.92rem}.kv dt{color:var(--muted);font-size:.72rem;text-transform:uppercase;letter-spacing:.06em;padding-top:.15rem}.kv dd{margin:0}

/* forms */
form{margin:.75rem 0}label{display:block;margin:.85rem 0 .3rem;font-size:.72rem;font-weight:600;text-transform:uppercase;letter-spacing:.06em;color:var(--muted)}
.hint{font-size:.78rem;color:var(--faint);margin-top:.25rem}
input,select{width:100%;padding:.5rem .65rem;border:1px solid var(--line);border-radius:3px;font:inherit;background:var(--panel);color:var(--fg)}
input:focus,select:focus{outline:2px solid var(--accent);border-color:var(--rule)}
.cols{display:grid;grid-template-columns:repeat(auto-fit,minmax(16rem,1fr));gap:0 1.5rem}
.inline{display:flex;gap:.5rem;align-items:center;flex-wrap:wrap;margin:.5rem 0}.inline input,.inline select{width:auto;min-width:13rem}
.chk{display:flex;gap:.45rem;align-items:center;margin:.35rem 0;font-size:.9rem;font-weight:400;text-transform:none;letter-spacing:0;color:var(--fg)}.chk input{width:auto}
.inline .chk{display:inline-flex;margin:0 .5rem 0 0}
.row{display:flex;gap:.5rem;flex-wrap:wrap;margin-top:1rem;align-items:center}
.btn{display:inline-flex;align-items:center;gap:.35rem;padding:.5rem .95rem;border-radius:2px;border:1.5px solid var(--ink);background:var(--ink);color:var(--ink-fg);font:inherit;font-weight:600;cursor:pointer;text-decoration:none;white-space:nowrap}
.btn:hover{text-decoration:none;opacity:.88}.btn.alt,.btn.ghost{background:transparent;color:var(--fg)}.btn.yellow{background:var(--accent);color:var(--accent-fg)}
.btn.sm{padding:.28rem .65rem;font-size:.82rem}.btn.danger{background:transparent;border-color:var(--bad);color:var(--bad)}
.dev{border-top:1px dashed var(--line);margin-top:1.25rem;padding-top:.75rem}
.theme{display:inline-flex;gap:.45rem;align-items:center;background:none;border:0;padding:0;font:inherit;color:var(--muted);cursor:pointer}.theme:hover{color:var(--fg)}
.side nav .theme{padding:.42rem .6rem;width:100%;text-align:left}
</style></head><body>
{{if .Shell}}<div class="app">
<aside class="side"><a class="title brand" href="/platform/apps"><span class="mark">IIMA</span>Students</a>
<div class="seg"><a class="on" href="/admin">Admin</a><a href="/platform/apps">Site</a></div>
<nav><a href="/admin"{{if eq .Title "Dashboard"}} class="on"{{end}}>{{template "icon" "dashboard"}}Dashboard{{with .WaitingCount}}<span class="count pill warn">{{.}} waiting</span>{{end}}</a>
<a href="/admin/apps/new"{{if eq .Title "Connect an app"}} class="on"{{end}}>{{template "icon" "plus"}}Build an app</a>
{{if .IsSuper}}<a href="/admin/routes"{{if eq .Title "Routes"}} class="on"{{end}}>{{template "icon" "routes"}}External routes</a></nav><hr><nav>
<a href="/admin/people"{{if eq .Title "People"}} class="on"{{end}}>{{template "icon" "people"}}People</a>
<a href="/admin/super-admins"{{if eq .Title "Super admins"}} class="on"{{end}}>{{template "icon" "shield"}}Super admins</a>
<a href="/admin/audit"{{if eq .Title "Audit log"}} class="on"{{end}}>{{template "icon" "log"}}Audit log</a>
<a href="/admin/github"{{if eq .Title "GitHub App"}} class="on"{{end}}>{{template "icon" "github"}}GitHub App</a>{{end}}</nav><hr>
<nav><a href="/platform/apps">{{template "icon" "apps"}}All apps</a><button class="theme" type="button" data-theme-toggle>{{template "icon" "theme"}}Light / dark</button><a href="/auth/logout">{{template "icon" "out"}}Sign out</a></nav>
<div class="me">{{.Who}}</div></aside>
<main class="main">{{else}}
<header class="bar"><div class="in">
<a class="brand" href="/"><span class="mark">IIMA</span>Students</a>
<nav><a href="/platform/apps">Apps</a>{{if .Who}}<a href="/admin">Dashboard{{with .WaitingCount}} <span class="pill warn">{{.}} waiting</span>{{end}}</a><a href="/admin/apps/new">Build an app</a>{{end}}</nav>
<div class="who"><button class="theme" type="button" data-theme-toggle title="Light / dark">{{template "icon" "theme"}}</button>{{if .Who}}<span>{{.Who}}</span><a href="/auth/logout">Sign out</a>{{else}}<a class="btn sm" href="/auth/login?rd=/admin">Sign in</a>{{end}}</div>
</div></header>
<main class="pub{{if not .Wide}} narrow{{end}}">{{if not .Wide}}<div class="card">{{end}}{{end}}{{end}}
{{define "bottom"}}{{if .Shell}}</main></div>{{else}}{{if not .Wide}}</div>{{end}}</main>{{end}}</body></html>{{end}}

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

// themeJS applies the theme chosen with the light / dark button (kept in this browser) before the
// page draws, and switches it. A file, not inline, because the gateway's CSP runs scripts from files
// only.
const themeJS = `(function () {
  var key = "iima-theme", root = document.documentElement;
  try { var saved = localStorage.getItem(key); if (saved) root.dataset.theme = saved; } catch (e) {}
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("[data-theme-toggle]");
    if (!b) return;
    var now = root.dataset.theme || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
    var next = now === "dark" ? "light" : "dark";
    root.dataset.theme = next;
    try { localStorage.setItem(key, next); } catch (e) {}
  });
})();
`

func (s *Server) handleThemeJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(themeJS))
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusNotFound, "notfound", pageData{Title: "Not found", Session: s.session(r)})
}
