package server

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/arnav127/iima-platform/core/attrs"
	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/store"
)

// The /admin pages: plain forms, no JavaScript, so they work under the gateway's CSP and need no
// build step. Every change is a POST that redirects back with a message.

type adminData struct {
	Title, Message, Error string
	Wide                  bool
	Actor                 *actor
	Apps                  []*registry.App
	App                   *registry.App
	Grants                []store.Grant
	Admins                []store.Admin
	Audit                 []auditRow
	Person                *person
	Query                 string
	Affiliations          []string
	Portal                bool        // the deploy portal is on
	PortalApps            []portalRow // apps this person owns, or every portal app for super admins
	Stats                 *dashStats
}

// dashStats are the dashboard's numbers.
type dashStats struct {
	Total, Live, Manual, Routes, Waiting, Overdue int
}

type portalRow struct {
	ID, Name, Path, Repo, Status, Review string
	Waiting                              bool // has settings waiting for approval
}

var adminPages = template.Must(template.Must(pages.Clone()).Funcs(template.FuncMap{
	"has": func(list []string, v string) bool { return slices.Contains(list, v) },
	"inc": func(i int) int { return i + 1 },
	// overdue: the app's review date (YYYY-MM-DD) has passed, so its owners should confirm it is
	// still needed (or archive it).
	"overdue": func(review string) bool {
		d, err := time.Parse("2006-01-02", review)
		return err == nil && time.Now().After(d.AddDate(0, 0, 1))
	},
}).Parse(`
{{define "adminnav"}}{{if .Message}}<div class="note ok">{{.Message}}</div>{{end}}{{if .Error}}<div class="note err">{{.Error}}</div>{{end}}{{end}}

{{define "statuspill"}}{{if eq . "approved"}}<span class="pill ok">live</span>{{else if eq . "pending"}}<span class="pill warn">waiting for approval</span>{{else}}<span class="pill bad">turned down</span>{{end}}{{end}}

{{define "admin"}}{{template "top" .}}
<div class="head"><div><h1>Dashboard</h1><p class="muted">{{if .Actor.Super}}You are a super admin: you approve apps, set up routes and appoint app admins.{{else}}Your apps and the roles you manage.{{end}}</p></div>
{{if .Portal}}<a class="btn" href="/admin/apps/new">Connect an app</a>{{end}}</div>
{{template "adminnav" .}}
{{with .Stats}}<div class="cards">
<div class="kpi"><b>{{.Total}}</b><span>Apps on the site</span></div>
<div class="kpi"><b>{{.Live}}</b><span>{{if $.Actor.Super}}Deployed from GitHub{{else}}Your live apps{{end}}</span></div>
<div class="kpi"><b>{{.Waiting}}</b><span>Waiting for approval</span></div>
<div class="kpi"><b>{{.Routes}}</b><span>External routes</span></div>
{{if .Overdue}}<div class="kpi"><b>{{.Overdue}}</b><span>Past their review date</span></div>{{end}}
</div>
{{if .Total}}<div class="section"><h2>Apps by how they run</h2>
<div class="stats"><div class="stat y"><b>{{.Live}}</b><span>Deployed from GitHub</span></div><div class="stat g2"><b>{{.Manual}}</b><span>Set up by hand</span></div><div class="stat g"><b>{{.Routes}}</b><span>External routes</span></div><div class="stat d"><b>{{.Waiting}}</b><span>Waiting</span></div></div>
<div class="bars">{{if .Live}}<i class="y" style="flex:{{.Live}}"></i>{{end}}{{if .Manual}}<i class="g2" style="flex:{{.Manual}}"></i>{{end}}{{if .Routes}}<i class="g" style="flex:{{.Routes}}"></i>{{end}}{{if .Waiting}}<i class="d" style="flex:{{.Waiting}}"></i>{{end}}</div></div>{{end}}{{end}}
{{if .Portal}}
{{if .Actor.Super}}{{$w := false}}{{range .PortalApps}}{{if .Waiting}}{{$w = true}}{{end}}{{end}}
{{if $w}}<div class="section"><h2>Waiting for your approval</h2><table><tr><th>App</th><th>Path</th><th>Repo</th><th></th></tr>
{{range .PortalApps}}{{if .Waiting}}<tr><td><b>{{.Name}}</b></td><td><code>{{.Path}}</code></td><td class="muted">{{.Repo}}</td><td style="text-align:right"><a class="btn sm yellow" href="/admin/apps/{{.ID}}/deploy">Review</a></td></tr>{{end}}{{end}}
</table></div>{{end}}{{end}}
<div class="section"><h2>{{if .Actor.Super}}Deployed apps{{else}}Your apps{{end}}</h2>
{{if .PortalApps}}<table><tr><th></th><th>App</th><th>Path</th><th>Repo</th><th>Status</th></tr>
{{range $i, $a := .PortalApps}}<tr><td class="muted small">{{inc $i}}</td><td><a href="/admin/apps/{{.ID}}/deploy"><b>{{.Name}}</b></a></td><td><code>{{.Path}}</code></td><td class="muted">{{.Repo}}</td><td>{{template "statuspill" .Status}}{{if and .Waiting (eq .Status "approved")}} <span class="pill warn">change waiting</span>{{end}}{{if overdue .Review}} <span class="pill bad">review due</span>{{end}}</td></tr>{{end}}
</table>{{else}}<p class="muted">Nothing here yet. <a href="/admin/apps/new">Connect a GitHub repo</a> to host your app on this site.</p>{{end}}</div>
{{end}}
<div class="section"><h2>Roles you manage</h2>
{{if .Apps}}<table><tr><th>App</th><th>Path</th><th>Who may sign in</th><th>Roles</th></tr>
{{range .Apps}}<tr><td><a href="/admin/apps/{{.ID}}"><b>{{.Name}}</b></a>{{if eq .Shape "external"}} <span class="pill acc">route</span>{{end}}{{if overdue .Review}} <span class="pill bad">review due</span>{{end}}</td><td><code>{{.Path}}</code></td><td>{{if eq .Login "external"}}any Google account{{else}}@iima.ac.in{{end}}</td><td>{{range $i, $r := .Roles}}{{if $i}}, {{end}}{{$r}}{{else}}<span class="muted">none</span>{{end}}</td></tr>{{end}}
</table>{{else}}<p class="muted">You don't manage roles in any app.</p>{{end}}</div>
{{template "bottom" .}}{{end}}

{{define "adminapp"}}{{template "top" .}}
{{$app := .App}}
<div class="head"><div><h1>{{.App.Name}}: roles</h1><p class="muted"><code>{{.App.Path}}</code> · {{if eq .App.Login "external"}}any Google account{{else}}@iima.ac.in accounts{{end}}</p></div>
<a class="btn ghost" href="/admin">Dashboard</a></div>
{{if .Message}}<div class="note ok">{{.Message}}</div>{{end}}{{if .Error}}<div class="note err">{{.Error}}</div>{{end}}
<div class="section"><h2>Roles</h2>
{{if not .App.Roles}}<p class="muted">This app declares no roles.</p>{{else}}
<table><tr><th>Person</th><th>Roles</th></tr>
{{range .Grants}}<tr><td>{{.Email}}</td><td>
<form class="inline" method="post" action="/admin/apps/{{$app.ID}}/roles"><input type="hidden" name="email" value="{{.Email}}">
{{$g := .}}{{range $app.Roles}}<label class="chk"><input type="checkbox" name="role" value="{{.}}"{{if has $g.Roles .}} checked{{end}}>{{.}}</label>{{end}}
<button class="btn sm ghost" type="submit">Save</button></form></td></tr>{{else}}<tr><td colspan="2" class="muted">Nobody has a role yet.</td></tr>{{end}}
</table>
<form class="inline" method="post" action="/admin/apps/{{$app.ID}}/roles" style="margin-top:1rem">
<input type="email" name="email" required placeholder="someone@iima.ac.in">
{{range $app.Roles}}<label class="chk"><input type="checkbox" name="role" value="{{.}}">{{.}}</label>{{end}}
<button class="btn sm" type="submit">Grant</button></form>
<p class="hint">Untick every role and save to remove someone. Changes reach the app on its next request.</p>{{end}}</div>
<div class="section"><h2>App admins</h2>
<p class="muted small">App admins manage this app's roles, here or from the app's own screens.</p>
<table>{{range .Admins}}<tr><td>{{.Email}}</td><td style="text-align:right">{{if $.Actor.Super}}<form class="inline" method="post" action="/admin/apps/{{$app.ID}}/admins"><input type="hidden" name="email" value="{{.Email}}"><input type="hidden" name="action" value="remove"><button class="btn sm danger" type="submit">Remove</button></form>{{end}}</td></tr>{{else}}<tr><td class="muted">None</td><td></td></tr>{{end}}</table>
{{if .Actor.Super}}<form class="inline" method="post" action="/admin/apps/{{$app.ID}}/admins" style="margin-top:.75rem"><input type="email" name="email" required placeholder="owner@iima.ac.in"><input type="hidden" name="action" value="add"><button class="btn sm" type="submit">Add app admin</button></form>{{end}}</div>
<div class="section"><h2>Recent changes</h2>{{template "audittable" .Audit}}</div>
{{template "bottom" .}}{{end}}

{{define "audittable"}}{{if .}}<table><tr><th>When (IST)</th><th>Who</th><th>What</th><th>App</th><th>Whom</th><th>Detail</th></tr>
{{range .}}<tr><td class="small">{{.At}}</td><td class="small">{{.Actor}}</td><td><span class="pill">{{.Action}}</span></td><td>{{.App}}</td><td class="small">{{.Target}}</td><td><code>{{.Detail}}</code></td></tr>{{end}}</table>{{else}}<p class="muted">Nothing yet.</p>{{end}}{{end}}

{{define "adminaudit"}}{{template "top" .}}<div class="head"><h1>Audit log</h1></div>{{template "adminnav" .}}<div class="section">{{template "audittable" .Audit}}</div>{{template "bottom" .}}{{end}}

{{define "adminpeople"}}{{template "top" .}}
<div class="head"><div><h1>People</h1><p class="muted">Look someone up: what their address says about them, their roles, and corrections.</p></div></div>
{{template "adminnav" .}}
<div class="section"><form class="inline" method="get" action="/admin/people"><input type="email" name="email" required placeholder="p25someone@iima.ac.in" value="{{.Query}}"><button class="btn sm" type="submit">Look up</button></form></div>
{{with .Person}}
<div class="section"><h2>{{.Email}}</h2>
<dl class="kv"><dt>Affiliation</dt><dd>{{.Attributes.Affiliation}}</dd><dt>Programme</dt><dd>{{.Attributes.Programme}}</dd><dt>Batch</dt><dd>{{if .Attributes.Batch}}{{.Attributes.Batch}}{{end}}</dd><dt>Year</dt><dd>{{if .Attributes.Year}}{{.Attributes.Year}}{{end}}</dd></dl>
<p class="hint">Worked out from the address{{if .Override}}, with the override below{{end}}. Year follows the academic year (from June).</p>
<h3>Roles</h3>{{if .Roles}}<table>{{range $app, $r := .Roles}}<tr><td><a href="/admin/apps/{{$app}}">{{$app}}</a></td><td>{{range $i, $x := $r}}{{if $i}}, {{end}}{{$x}}{{end}}</td></tr>{{end}}</table>{{else}}<p class="muted">None.</p>{{end}}</div>
<div class="section"><h2>Override</h2>
<form method="post" action="/admin/people"><input type="hidden" name="email" value="{{.Email}}"><div class="cols">
<div><label>Affiliation</label><select name="affiliation"><option value="">(derive)</option>{{$cur := ""}}{{with .Override}}{{$cur = .Affiliation}}{{end}}{{range $.Affiliations}}<option{{if eq . $cur}} selected{{end}}>{{.}}</option>{{end}}</select></div>
<div><label>Programme</label><input name="programme" placeholder="(derive)" value="{{with .Override}}{{.Programme}}{{end}}"></div>
<div><label>Batch (year of entry)</label><input name="batch" type="number" placeholder="(derive)" value="{{with .Override}}{{if .Batch}}{{.Batch}}{{end}}{{end}}"></div>
<div><label>Note</label><input name="note" placeholder="why, e.g. exchange student 2026" value="{{with .Override}}{{.Note}}{{end}}"></div></div>
<div class="row"><button class="btn" type="submit">Save override</button></div>
<p class="hint">Leave all three empty and save to go back to what the address says.</p></form></div>
{{end}}
{{template "bottom" .}}{{end}}

{{define "adminsupers"}}{{template "top" .}}
<div class="head"><div><h1>Super admins</h1><p class="muted">They approve apps, appoint app admins and fix attributes. Keep two or three, so nobody gets locked out.</p></div></div>
{{template "adminnav" .}}
<div class="section"><table>{{range .Admins}}<tr><td>{{.Email}}</td><td class="muted small">added by {{.AddedBy}}</td><td style="text-align:right"><form class="inline" method="post" action="/admin/super-admins"><input type="hidden" name="email" value="{{.Email}}"><input type="hidden" name="action" value="remove"><button class="btn sm danger" type="submit">Remove</button></form></td></tr>{{end}}</table>
<form class="inline" method="post" action="/admin/super-admins" style="margin-top:1rem"><input type="email" name="email" required placeholder="someone@iima.ac.in"><input type="hidden" name="action" value="add"><button class="btn sm" type="submit">Add super admin</button></form></div>
{{template "bottom" .}}{{end}}
`))

// WaitingCount is how many apps wait for this super admin's approval.
func (d adminData) WaitingCount() int {
	if d.Actor == nil {
		return 0
	}
	return d.Actor.Waiting
}

// Who is the signed-in person's email for the top bar.
func (d adminData) Who() string {
	if d.Actor == nil {
		return ""
	}
	return d.Actor.Email
}

func (s *Server) renderAdmin(w http.ResponseWriter, status int, name string, d adminData) {
	d.Wide = true
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := adminPages.ExecuteTemplate(w, name, d); err != nil {
		s.cfg.Log.Error("render", "page", name, "err", err)
	}
}

// pageActor is apiActor for pages: sends signed-out visitors to sign in, refuses non-admins and
// cross-origin POSTs.
func (s *Server) pageActor(w http.ResponseWriter, r *http.Request) *actor {
	sess := s.session(r)
	if sess == nil {
		http.Redirect(w, r, loginURL(r.URL.RequestURI()), http.StatusFound)
		return nil
	}
	if r.Method == http.MethodPost && !s.sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return nil
	}
	a, err := s.actorFor(r.Context(), sess)
	if err != nil {
		s.serverError(w, "admin actor", err)
		return nil
	}
	if !a.isAdmin() {
		s.render(w, http.StatusForbidden, "error", pageData{Title: "Admins only", Message: "You are not an admin of any app. Ask a super admin if you should be."})
		return nil
	}
	return a
}

// done redirects back to the page with a message, or shows a user error there.
func (s *Server) done(w http.ResponseWriter, r *http.Request, back string, err error, ok string) {
	var ue userError
	switch {
	case err == nil:
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(ok), http.StatusSeeOther)
	case errors.As(err, &ue):
		http.Redirect(w, r, back+"?err="+url.QueryEscape(ue.Error()), http.StatusSeeOther)
	case errors.Is(err, errForbidden):
		s.render(w, http.StatusForbidden, "error", pageData{Title: "Not allowed", Message: "Only super admins can do that."})
	default:
		s.serverError(w, "admin change", err)
	}
}

func flash(r *http.Request) (msg, errMsg string) {
	return r.URL.Query().Get("msg"), r.URL.Query().Get("err")
}

func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		// With the portal on, any member may come here to connect an app.
		var a *actor
		if s.cfg.Portal != nil {
			a = s.memberActor(w, r)
		} else {
			a = s.pageActor(w, r)
		}
		if a == nil {
			return
		}
		var apps []*registry.App
		for _, app := range s.registry().Apps() {
			if a.canManage(app.ID) {
				apps = append(apps, app)
			}
		}
		d := adminData{Title: "Dashboard", Actor: a, Apps: apps, Portal: s.cfg.Portal != nil}
		d.Message, d.Error = flash(r)
		if d.Portal {
			rows, err := s.portalRows(r, a)
			if err != nil {
				s.serverError(w, "portal apps", err)
				return
			}
			d.PortalApps = rows
		}
		st := &dashStats{}
		for _, app := range s.registry().Apps() {
			if d, err := time.Parse("2006-01-02", app.Review); err == nil && time.Now().After(d.AddDate(0, 0, 1)) && (a.Super || a.canManage(app.ID)) {
				st.Overdue++
			}
			switch {
			case app.Shape == registry.ShapeExternal:
				st.Routes++
			case app.File == "portal":
				st.Live++
			default:
				st.Manual++
			}
		}
		st.Total = st.Live + st.Manual + st.Routes
		for _, row := range d.PortalApps {
			if row.Waiting {
				st.Waiting++
			}
		}
		if !a.Super { // owners: their own apps
			st.Live = 0
			for _, row := range d.PortalApps {
				if row.Status == "approved" {
					st.Live++
				}
			}
		}
		d.Stats = st
		s.renderAdmin(w, http.StatusOK, "admin", d)
	})

	appPage := func(w http.ResponseWriter, r *http.Request) (*actor, *registry.App) {
		a := s.pageActor(w, r)
		if a == nil {
			return nil, nil
		}
		app := s.registry().ByID(r.PathValue("app"))
		if app == nil || !a.canManage(app.ID) {
			s.handleNotFound(w, r)
			return nil, nil
		}
		return a, app
	}

	mux.HandleFunc("GET /admin/apps/{app}", func(w http.ResponseWriter, r *http.Request) {
		a, app := appPage(w, r)
		if app == nil {
			return
		}
		ctx := r.Context()
		grants, err := s.cfg.Store.Grants(ctx, app.ID)
		if err != nil {
			s.serverError(w, "grants", err)
			return
		}
		admins, err := s.cfg.Store.AppAdmins(ctx, app.ID)
		if err != nil {
			s.serverError(w, "app admins", err)
			return
		}
		entries, err := s.cfg.Store.Audit(ctx, app.ID, 50)
		if err != nil {
			s.serverError(w, "audit", err)
			return
		}
		msg, e := flash(r)
		s.renderAdmin(w, http.StatusOK, "adminapp", adminData{Title: app.Name, Actor: a, App: app, Grants: grants, Admins: admins, Audit: auditRows(entries), Message: msg, Error: e})
	})
	mux.HandleFunc("POST /admin/apps/{app}/roles", func(w http.ResponseWriter, r *http.Request) {
		a, app := appPage(w, r)
		if app == nil {
			return
		}
		_ = r.ParseForm()
		email := r.PostForm.Get("email")
		err := s.setRoles(r.Context(), a, app, email, r.PostForm["role"])
		s.done(w, r, "/admin/apps/"+app.ID, err, "Saved roles for "+store.NormEmail(email)+".")
	})
	mux.HandleFunc("POST /admin/apps/{app}/admins", func(w http.ResponseWriter, r *http.Request) {
		a, app := appPage(w, r)
		if app == nil {
			return
		}
		email, add := r.PostFormValue("email"), r.PostFormValue("action") == "add"
		err := s.setAppAdmin(r.Context(), a, app, email, add)
		s.done(w, r, "/admin/apps/"+app.ID, err, "Updated app admins.")
	})

	mux.HandleFunc("GET /admin/people", func(w http.ResponseWriter, r *http.Request) {
		a := s.pageActor(w, r)
		if a == nil {
			return
		}
		if !a.Super {
			s.done(w, r, "", errForbidden, "")
			return
		}
		d := adminData{Title: "People", Actor: a, Affiliations: attrs.Affiliations}
		d.Message, d.Error = flash(r)
		if q := r.URL.Query().Get("email"); q != "" {
			d.Query = q
			p, err := s.lookupPerson(r.Context(), q)
			var ue userError
			if errors.As(err, &ue) {
				d.Error = ue.Error()
			} else if err != nil {
				s.serverError(w, "lookup", err)
				return
			}
			d.Person = p
		}
		s.renderAdmin(w, http.StatusOK, "adminpeople", d)
	})
	mux.HandleFunc("POST /admin/people", func(w http.ResponseWriter, r *http.Request) {
		a := s.pageActor(w, r)
		if a == nil {
			return
		}
		email := r.PostFormValue("email")
		o := store.Override{Email: email, Affiliation: r.PostFormValue("affiliation"), Programme: r.PostFormValue("programme"), Note: r.PostFormValue("note")}
		var err error
		if b := r.PostFormValue("batch"); b != "" {
			if o.Batch, err = strconv.Atoi(b); err != nil {
				err = userError("batch must be a year like 2025")
			}
		}
		if err == nil {
			err = s.setOverride(r.Context(), a, o)
		}
		s.done(w, r, "/admin/people", err, "Saved.")
	})

	mux.HandleFunc("GET /admin/super-admins", func(w http.ResponseWriter, r *http.Request) {
		a := s.pageActor(w, r)
		if a == nil {
			return
		}
		if !a.Super {
			s.done(w, r, "", errForbidden, "")
			return
		}
		admins, err := s.cfg.Store.SuperAdmins(r.Context())
		if err != nil {
			s.serverError(w, "super admins", err)
			return
		}
		msg, e := flash(r)
		s.renderAdmin(w, http.StatusOK, "adminsupers", adminData{Title: "Super admins", Actor: a, Admins: admins, Message: msg, Error: e})
	})
	mux.HandleFunc("POST /admin/super-admins", func(w http.ResponseWriter, r *http.Request) {
		a := s.pageActor(w, r)
		if a == nil {
			return
		}
		err := s.setSuperAdmin(r.Context(), a, r.PostFormValue("email"), r.PostFormValue("action") == "add")
		s.done(w, r, "/admin/super-admins", err, "Updated super admins.")
	})

	mux.HandleFunc("GET /admin/audit", func(w http.ResponseWriter, r *http.Request) {
		a := s.pageActor(w, r)
		if a == nil {
			return
		}
		if !a.Super {
			s.done(w, r, "", errForbidden, "")
			return
		}
		entries, err := s.cfg.Store.Audit(r.Context(), "", 200)
		if err != nil {
			s.serverError(w, "audit", err)
			return
		}
		s.renderAdmin(w, http.StatusOK, "adminaudit", adminData{Title: "Audit log", Actor: a, Audit: auditRows(entries)})
	})
}

// Shell puts admin pages in the sidebar layout.
func (d adminData) Shell() bool { return true }

// IsSuper shows the super admins' links in the sidebar.
func (d adminData) IsSuper() bool { return d.Actor != nil && d.Actor.Super }
