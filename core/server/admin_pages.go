package server

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"

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
}

var adminPages = template.Must(template.Must(pages.Clone()).Funcs(template.FuncMap{
	"has": func(list []string, v string) bool { return slices.Contains(list, v) },
}).Parse(`
{{define "adminnav"}}<nav class="admin"><a href="/admin">Apps</a>{{if .Actor.Super}}<a href="/admin/people">People</a><a href="/admin/super-admins">Super admins</a><a href="/admin/audit">Audit log</a>{{end}}<span class="muted">{{.Actor.Email}}</span></nav>
{{if .Message}}<div class="note">{{.Message}}</div>{{end}}{{if .Error}}<div class="note err">{{.Error}}</div>{{end}}{{end}}

{{define "admin"}}{{template "top" .}}{{template "adminnav" .}}
<h1>Apps you manage</h1>
{{if .Apps}}<table><tr><th>App</th><th>Path</th><th>Login</th><th>Roles declared</th></tr>
{{range .Apps}}<tr><td><a href="/admin/apps/{{.ID}}">{{.Name}}</a></td><td><code>{{.Path}}</code></td><td>{{.Login}}</td><td>{{range $i, $r := .Roles}}{{if $i}}, {{end}}{{$r}}{{else}}<span class="muted">none</span>{{end}}</td></tr>{{end}}
</table>{{else}}<p>None yet.</p>{{end}}
{{template "bottom"}}{{end}}

{{define "adminapp"}}{{template "top" .}}{{template "adminnav" .}}
{{$app := .App}}
<h1>{{.App.Name}}</h1>
<p class="muted"><code>{{.App.Path}}</code> · login {{.App.Login}} · roles come from its <code>app.toml</code></p>
<h3>Roles</h3>
{{if not .App.Roles}}<p>This app declares no roles.</p>{{else}}
<table><tr><th>Email</th><th>Roles</th></tr>
{{range .Grants}}<tr><td>{{.Email}}</td><td>
<form class="inline" method="post" action="/admin/apps/{{$app.ID}}/roles"><input type="hidden" name="email" value="{{.Email}}">
{{$g := .}}{{range $app.Roles}}<label class="chk"><input type="checkbox" name="role" value="{{.}}"{{if has $g.Roles .}} checked{{end}}>{{.}}</label>{{end}}
<button class="btn sm" type="submit">Save</button></form></td></tr>{{end}}
</table>
<form class="inline" method="post" action="/admin/apps/{{$app.ID}}/roles" style="margin-top:1rem">
<input type="email" name="email" required placeholder="someone@iima.ac.in">
{{range $app.Roles}}<label class="chk"><input type="checkbox" name="role" value="{{.}}">{{.}}</label>{{end}}
<button class="btn sm" type="submit">Grant</button></form>
<p class="muted">Untick every role and save to remove someone. Changes reach the app on its next request.</p>{{end}}
<h3>App admins</h3>
<p class="muted">App admins manage this app's roles, here or from the app's own screens.</p>
<table>{{range .Admins}}<tr><td>{{.Email}}</td><td>{{if $.Actor.Super}}<form class="inline" method="post" action="/admin/apps/{{$app.ID}}/admins"><input type="hidden" name="email" value="{{.Email}}"><input type="hidden" name="action" value="remove"><button class="btn sm alt" type="submit">Remove</button></form>{{end}}</td></tr>{{else}}<tr><td class="muted">None</td><td></td></tr>{{end}}</table>
{{if .Actor.Super}}<form class="inline" method="post" action="/admin/apps/{{$app.ID}}/admins" style="margin-top:.75rem"><input type="email" name="email" required placeholder="owner@iima.ac.in"><input type="hidden" name="action" value="add"><button class="btn sm" type="submit">Add app admin</button></form>{{end}}
<h3>Recent changes</h3>{{template "audittable" .Audit}}
{{template "bottom"}}{{end}}

{{define "audittable"}}{{if .}}<table><tr><th>When (IST)</th><th>Who</th><th>What</th><th>App</th><th>Whom</th><th>Detail</th></tr>
{{range .}}<tr><td>{{.At}}</td><td>{{.Actor}}</td><td>{{.Action}}</td><td>{{.App}}</td><td>{{.Target}}</td><td><code>{{.Detail}}</code></td></tr>{{end}}</table>{{else}}<p class="muted">Nothing yet.</p>{{end}}{{end}}

{{define "adminaudit"}}{{template "top" .}}{{template "adminnav" .}}<h1>Audit log</h1>{{template "audittable" .Audit}}{{template "bottom"}}{{end}}

{{define "adminpeople"}}{{template "top" .}}{{template "adminnav" .}}
<h1>People</h1>
<form class="inline" method="get" action="/admin/people"><input type="email" name="email" required placeholder="p25someone@iima.ac.in" value="{{.Query}}"><button class="btn sm" type="submit">Look up</button></form>
{{with .Person}}
<h3>{{.Email}}</h3>
<table><tr><th>Affiliation</th><th>Programme</th><th>Batch</th><th>Year</th></tr>
<tr><td>{{.Attributes.Affiliation}}</td><td>{{.Attributes.Programme}}</td><td>{{if .Attributes.Batch}}{{.Attributes.Batch}}{{end}}</td><td>{{if .Attributes.Year}}{{.Attributes.Year}}{{end}}</td></tr></table>
<p class="muted">Worked out from the address{{if .Override}}, with the override below{{end}}. Year follows the academic year (from June).</p>
<h3>Roles</h3>{{if .Roles}}<table>{{range $app, $r := .Roles}}<tr><td><a href="/admin/apps/{{$app}}">{{$app}}</a></td><td>{{range $i, $x := $r}}{{if $i}}, {{end}}{{$x}}{{end}}</td></tr>{{end}}</table>{{else}}<p class="muted">None.</p>{{end}}
<h3>Override</h3>
<form method="post" action="/admin/people"><input type="hidden" name="email" value="{{.Email}}">
<label>Affiliation</label><select name="affiliation"><option value="">(derive)</option>{{$cur := ""}}{{with .Override}}{{$cur = .Affiliation}}{{end}}{{range $.Affiliations}}<option{{if eq . $cur}} selected{{end}}>{{.}}</option>{{end}}</select>
<label>Programme</label><input name="programme" placeholder="(derive)" value="{{with .Override}}{{.Programme}}{{end}}">
<label>Batch (year of entry)</label><input name="batch" type="number" placeholder="(derive)" value="{{with .Override}}{{if .Batch}}{{.Batch}}{{end}}{{end}}">
<label>Note</label><input name="note" placeholder="why, e.g. exchange student 2026" value="{{with .Override}}{{.Note}}{{end}}">
<div class="row"><button class="btn" type="submit">Save override</button></div>
<p class="muted">Leave all three empty and save to go back to what the address says.</p></form>
{{end}}
{{template "bottom"}}{{end}}

{{define "adminsupers"}}{{template "top" .}}{{template "adminnav" .}}
<h1>Super admins</h1>
<p class="muted">They create apps, appoint app admins and fix attributes. Keep two or three, so nobody gets locked out.</p>
<table>{{range .Admins}}<tr><td>{{.Email}}</td><td class="muted">added by {{.AddedBy}}</td><td><form class="inline" method="post" action="/admin/super-admins"><input type="hidden" name="email" value="{{.Email}}"><input type="hidden" name="action" value="remove"><button class="btn sm alt" type="submit">Remove</button></form></td></tr>{{end}}</table>
<form class="inline" method="post" action="/admin/super-admins" style="margin-top:1rem"><input type="email" name="email" required placeholder="someone@iima.ac.in"><input type="hidden" name="action" value="add"><button class="btn sm" type="submit">Add super admin</button></form>
{{template "bottom"}}{{end}}
`))

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
		a := s.pageActor(w, r)
		if a == nil {
			return
		}
		var apps []*registry.App
		for _, app := range s.cfg.Registry.Apps() {
			if a.canManage(app.ID) {
				apps = append(apps, app)
			}
		}
		msg, e := flash(r)
		s.renderAdmin(w, http.StatusOK, "admin", adminData{Title: "Admin", Actor: a, Apps: apps, Message: msg, Error: e})
	})

	appPage := func(w http.ResponseWriter, r *http.Request) (*actor, *registry.App) {
		a := s.pageActor(w, r)
		if a == nil {
			return nil, nil
		}
		app := s.cfg.Registry.ByID(r.PathValue("app"))
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
