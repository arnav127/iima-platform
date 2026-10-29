package server

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/arnav127/iima-platform/core/registry"
)

// /admin/routes: super admins point a path at a host and port outside the platform, for apps that
// are not ported yet (what Apache's ProxyPass lines did). Routes are live within seconds; see
// core/gateway for exactly what the gateway does with one.

type routesData struct {
	deployData
	Routes []*registry.App
	Edit   *registry.App // the route in the form ("" ID for a new one)
	IsNew  bool
}

var routesPages = template.Must(template.Must(deployPages.Clone()).Parse(`
{{define "routes"}}{{template "top" .}}
<div class="head"><div><h1>External routes</h1><p class="muted">Send a path to an app still running outside the platform, the way Apache's <code>ProxyPass</code> did. Moving the app onto the platform later replaces its route.</p></div>
<a class="btn" href="/admin/routes?edit=new#form">Add a route</a></div>
{{template "adminnav" .}}
<div class="section"><h2>Routes</h2>
{{if .Routes}}<table><tr><th>Path</th><th>Goes to</th><th>Sign-in</th><th>Options</th><th></th></tr>
{{range .Routes}}<tr><td><b>{{.Name}}</b><br><code>{{.Path}}</code></td><td><code>{{.Upstream}}</code></td>
<td>{{if eq .Auth "none"}}<span class="pill">passed through</span>{{else if eq .Auth "required"}}<span class="pill acc">required</span>{{else}}<span class="pill">optional</span>{{end}}{{if eq .Login "external"}} <span class="pill warn">any Google account</span>{{end}}</td>
<td class="small">{{if .PreservesHost}}keeps Host{{else}}upstream Host{{end}}{{if .Realtime}} · realtime{{end}}{{if .InsecureTLS}} · <span class="pill warn">TLS not checked</span>{{end}}</td>
<td style="text-align:right"><a class="btn sm ghost" href="/admin/routes?edit={{.ID}}#form">Edit</a>
<form class="inline" method="post" action="/admin/routes/delete" style="margin:0"><input type="hidden" name="id" value="{{.ID}}"><button class="btn sm danger" type="submit">Remove</button></form></td></tr>{{end}}</table>
{{else}}<p class="muted">No external routes. Add one for each app still served the old way.</p>{{end}}</div>

{{with .Edit}}<div class="section" id="form"><h2>{{if $.IsNew}}Add a route{{else}}Edit {{.Name}}{{end}}</h2>
<form method="post" action="/admin/routes"><input type="hidden" name="existing" value="{{if not $.IsNew}}{{.ID}}{{end}}"><div class="cols">
<div><label>Id</label><input name="id" required value="{{.ID}}" placeholder="betamarkets"{{if not $.IsNew}} disabled{{end}}><div class="hint">Lower case; also the token audience when sign-in is on. Use the app's future id, so its roles carry over when it moves onto the platform.</div></div>
<div><label>Name</label><input name="name" required value="{{.Name}}" placeholder="BetaMarkets"></div>
<div><label>Path on this site</label><input name="path" required value="{{.Path}}" placeholder="/betamarkets"><div class="hint">One segment, or / for the home app (it then gets every path nothing else claims).</div></div>
<div><label>Goes to</label><input name="upstream" required value="{{.Upstream}}" placeholder="http://10.0.0.5:3010/betamarkets"><div class="hint">/path/x goes to this URL + /x. Keep the upstream's path the same as the public one when the app builds its own links.</div></div>
<div><label>Sign-in at the gateway</label><select name="auth">
<option value="none"{{if eq .Auth "none"}} selected{{end}}>none: pass requests through as Apache did</option>
<option value="optional"{{if eq .Auth "optional"}} selected{{end}}>optional: add X-User-Token when signed in</option>
<option value="required"{{if eq .Auth "required"}} selected{{end}}>required: send signed-out visitors to sign in</option></select></div>
<div><label>Who may sign in</label><select name="login"><option value="iima"{{if eq .Login "iima"}} selected{{end}}>@iima.ac.in accounts</option><option value="external"{{if eq .Login "external"}} selected{{end}}>any Google account</option></select></div>
<div><label>Public paths (no sign-in check)</label><input name="public" value="{{join .Public}}" placeholder="/api/health"></div>
<div><label>Roles</label><input name="roles" value="{{join .Roles}}" placeholder="admin"><div class="hint">Only matters with sign-in on.</div></div>
</div>
<label class="chk" style="margin-top:1rem"><input type="checkbox" name="preserve_host" value="1"{{if .PreservesHost}} checked{{end}}> Pass the site's Host header on (Apache's ProxyPreserveHost)</label>
<label class="chk"><input type="checkbox" name="realtime" value="1"{{if .Realtime}} checked{{end}}> Realtime: server-sent events or long polling (turns compression off, so events arrive as they are sent). Websockets work either way.</label>
<label class="chk"><input type="checkbox" name="insecure_tls" value="1"{{if .InsecureTLS}} checked{{end}}> Don't check the upstream's TLS certificate (a self-signed https upstream)</label>
<div class="row"><button class="btn" type="submit">Save route</button><a class="btn ghost" href="/admin/routes">Cancel</a></div>
<p class="hint">Live within a few seconds. Redirects and cookie paths that point at the upstream are rewritten to this path (Apache's ProxyPassReverse). The platform's session cookie and identity headers never reach the upstream.</p>
</form></div>{{end}}
{{template "bottom" .}}{{end}}
`))

func (s *Server) routesRoutes(mux *http.ServeMux) {
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
	mux.HandleFunc("GET /admin/routes", func(w http.ResponseWriter, r *http.Request) {
		a := superOnly(w, r)
		if a == nil {
			return
		}
		routes, err := p.Routes(r.Context())
		if err != nil {
			s.serverError(w, "routes", err)
			return
		}
		d := routesData{deployData: deployData{Title: "Routes", Actor: a, Wide: true, Portal: true}, Routes: routes}
		d.Message, d.Error = flash(r)
		switch id := r.URL.Query().Get("edit"); id {
		case "":
		case "new":
			d.Edit, d.IsNew = &registry.App{Auth: registry.AuthNone, Login: registry.LoginIIMA}, true
		default:
			for _, rt := range routes {
				if rt.ID == id {
					d.Edit = rt
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := routesPages.ExecuteTemplate(w, "routes", d); err != nil {
			s.cfg.Log.Error("render", "page", "routes", "err", err)
		}
	})
	mux.HandleFunc("POST /admin/routes", func(w http.ResponseWriter, r *http.Request) {
		a := superOnly(w, r)
		if a == nil {
			return
		}
		list := func(name string) []string {
			var out []string
			for _, v := range strings.Split(r.PostFormValue(name), ",") {
				if v = strings.TrimSpace(v); v != "" {
					out = append(out, v)
				}
			}
			return out
		}
		keepHost := r.PostFormValue("preserve_host") == "1"
		rt := &registry.App{
			ID: strings.TrimSpace(r.PostFormValue("id")), Name: strings.TrimSpace(r.PostFormValue("name")),
			Path: strings.TrimSpace(r.PostFormValue("path")), Upstream: strings.TrimSpace(r.PostFormValue("upstream")),
			Auth: r.PostFormValue("auth"), Login: r.PostFormValue("login"),
			Public: list("public"), Roles: list("roles"), PreserveHost: &keepHost,
			Realtime: r.PostFormValue("realtime") == "1", InsecureTLS: r.PostFormValue("insecure_tls") == "1",
		}
		err := p.SaveRoute(r.Context(), a.Email, rt, r.PostFormValue("existing"))
		s.portalDone(w, r, "/admin/routes", err, "Saved "+rt.Path+". Live within a few seconds.")
	})
	mux.HandleFunc("POST /admin/routes/delete", func(w http.ResponseWriter, r *http.Request) {
		a := superOnly(w, r)
		if a == nil {
			return
		}
		err := p.DeleteRoute(r.Context(), a.Email, r.PostFormValue("id"))
		s.portalDone(w, r, "/admin/routes", err, "Removed. The path is free within a few seconds.")
	})
}
