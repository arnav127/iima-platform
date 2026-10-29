package server

import (
	"errors"
	"html/template"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/arnav127/iima-platform/core/nomad"
	"github.com/arnav127/iima-platform/core/portal"
	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/store"
)

// The deploy portal's pages: connect a repo, then one page per app for approval, settings, env
// vars, builds and deploys. Owners and super admins only; any @iima.ac.in user may connect a repo.

type deployData struct {
	Title, Message, Error string
	Wide                  bool
	Actor                 *actor
	InstallURL            string
	Connectable           bool
	Repo                  string

	App      *store.PortalApp
	Live     *registry.App
	Pending  *registry.App
	Form     *registry.App
	Owners   []string
	Env      []store.EnvVar
	Releases []store.Release
	Deploys  []store.Deploy
	Running  string
	Status   *nomad.Status
	Stdout   string
	Stderr   string
	Shapes   []string
}

var deployPages = template.Must(template.Must(adminPages.Clone()).Funcs(template.FuncMap{
	"join":  func(l []string) string { return strings.Join(l, ", ") },
	"short": func(s string) string { return s[:min(7, len(s))] },
	"mb":    func(n int64) string { return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB" },
}).Parse(`
{{define "flash"}}{{if .Message}}<div class="note ok">{{.Message}}</div>{{end}}{{if .Error}}<div class="note err">{{.Error}}</div>{{end}}{{end}}

{{define "portalnew"}}{{template "top" .}}
<div class="head"><div><h1>Build an app</h1><p class="muted">Host your app at students.iima.ac.in: one sign-in for everyone, roles, backups and deploys on every push.</p></div>
<a class="btn ghost" href="/admin">Dashboard</a></div>
{{template "flash" .}}
{{if not .Connectable}}<div class="section"><p>Connecting repos is not set up on this platform yet (no GitHub App). Ask a super admin.</p></div>{{else}}
<div class="section"><ol class="steps">
<li><h3 style="margin-top:0">Start from the template</h3>
<p class="muted">Copy <code>template/go-pocketbase</code> from the platform repo into your own GitHub repo. Fill in <code>app.toml</code>: an id, a path like <code>/quiz</code>, and your email in <code>owners</code>. Keep <code>.github/workflows/deploy.yml</code>: it builds a release on every push to <code>main</code>.</p></li>
<li><h3 style="margin-top:0">Install the platform's GitHub App</h3>
<p class="muted">Choose only the repo for this app. The platform reads its <code>app.toml</code> and its releases, nothing else.</p>
<div class="row"><a class="btn ghost" href="{{.InstallURL}}">Install on GitHub</a></div></li>
<li><h3 style="margin-top:0">Request the app</h3>
<form method="post" action="/admin/apps/new" class="inline"><input name="repo" required placeholder="your-org/your-app" value="{{.Repo}}" style="min-width:18rem"><button class="btn" type="submit">Request</button></form>
<p class="hint">A super admin reviews the request. Once approved, the newest build goes live, and every push after that deploys by itself.</p></li>
</ol></div>{{end}}
{{template "bottom" .}}{{end}}

{{define "portalapp"}}{{template "top" .}}
{{$a := .App}}{{$id := .App.ID}}{{$name := ""}}{{$path := ""}}{{with .Live}}{{$name = .Name}}{{$path = .Path}}{{else}}{{with .Pending}}{{$name = .Name}}{{$path = .Path}}{{end}}{{end}}
<div class="head"><div><h1>{{$name}} {{template "statuspill" $a.Status}}</h1>
<p class="muted"><code>{{$path}}</code> · <a href="https://github.com/{{$a.Repo}}">{{$a.Repo}}</a> on {{$a.Branch}}</p></div>
<div class="row" style="margin:0">{{if eq $a.Status "approved"}}<a class="btn" href="{{href $path}}">Open app</a><a class="btn ghost" href="/admin/apps/{{$id}}">Roles</a><a class="btn ghost" href="/admin/apps/{{$id}}/logs">Logs</a>{{end}}</div></div>
<div class="subnav"><a href="#builds">Builds</a><a href="#settings">Settings</a><a href="#env">Environment</a><a href="#owners">Owners</a><a href="#deploys">Deploys</a></div>
{{template "flash" .}}

{{if eq $a.Status "pending"}}<div class="note warn">Waiting for a super admin to approve this app. It is offline until then.</div>{{end}}
{{if eq $a.Status "archived"}}<div class="note err">This request was turned down.</div>{{end}}
{{if and .Pending .Live}}<div class="note warn">A change to how the app is reached waits for a super admin: <code>{{.Pending.Path}}</code> · {{if eq .Pending.Login "external"}}any Google account{{else}}@iima.ac.in{{end}} · signed-out visitors {{.Pending.Auth}}{{with .Pending.Public}} · public {{join .}}{{end}}</div>{{end}}
{{if and .Actor.Super .Pending}}<div class="section"><h2>Review</h2>
{{with .Pending}}<dl class="kv"><dt>Path</dt><dd><code>{{.Path}}</code>{{if eq .Path "/"}} <span class="pill warn">home app: gets every path no other app claims</span>{{end}}</dd><dt>Shape</dt><dd>{{.Shape}}</dd>
<dt>Who may sign in</dt><dd>{{if eq .Login "external"}}<span class="pill warn">any Google account</span>{{else}}@iima.ac.in accounts{{end}}</dd>
<dt>Signed-out visitors</dt><dd>{{.Auth}}</dd><dt>Public paths</dt><dd>{{with .Public}}{{join .}}{{else}}none{{end}}</dd><dt>Roles</dt><dd>{{with .Roles}}{{join .}}{{else}}none{{end}}</dd></dl>{{end}}
<p class="muted small">Requested by {{$a.CreatedBy}} from <a href="https://github.com/{{$a.Repo}}">{{$a.Repo}}</a>. Check the repo before approving.</p>
<div class="row"><form class="inline" method="post" action="/admin/apps/{{$id}}/approve"><button class="btn" type="submit">Approve</button></form>
<form class="inline" method="post" action="/admin/apps/{{$id}}/reject"><button class="btn danger" type="submit">Turn down</button></form></div></div>{{end}}

{{if eq $a.Status "approved"}}<div class="section"><h2>Running</h2>
{{with .Status}}<dl class="kv"><dt>State</dt><dd>{{if eq .Job "running"}}<span class="pill ok">running</span>{{else}}<span class="pill warn">{{.Job}}</span>{{end}}</dd>
<dt>Build</dt><dd><code>{{.Release}}</code></dd>{{with .Deploy}}<dt>Last deployment</dt><dd>{{if eq . "successful"}}<span class="pill ok">{{.}}</span>{{else if eq . "failed"}}<span class="pill bad">{{.}}</span>{{else}}<span class="pill warn">{{.}}</span>{{end}}{{end}}{{with .DeployNote}} <span class="muted small">{{.}}</span>{{end}}</dd></dl>
{{else}}<p class="muted">Not running yet. The newest build deploys shortly after approval.</p>{{end}}
<form class="inline" method="post" action="/admin/apps/{{$id}}/autodeploy"><input type="hidden" name="on" value="{{if $a.AutoDeploy}}0{{else}}1{{end}}">
<span>Auto-deploy from <code>{{$a.Branch}}</code>: <b>{{if $a.AutoDeploy}}on{{else}}off{{end}}</b></span><button class="btn sm ghost" type="submit">Turn {{if $a.AutoDeploy}}off{{else}}on{{end}}</button></form></div>{{end}}

<div class="section" id="builds"><h2>Builds</h2>
{{if .Releases}}<table><tr><th>Build</th><th>Commit</th><th>Fetched (IST)</th><th>Size</th><th></th></tr>
{{range .Releases}}<tr><td><code>{{.Tag}}</code>{{if eq .Tag $.Running}} <span class="pill ok">running</span>{{end}}</td><td><a href="https://github.com/{{$a.Repo}}/commit/{{.Commit}}"><code>{{short .Commit}}</code></a></td><td class="small">{{.CreatedAt.Format "02 Jan 15:04"}}</td><td class="small">{{mb .Size}}</td>
<td style="text-align:right">{{if and (eq $a.Status "approved") .File}}<form class="inline" method="post" action="/admin/apps/{{$id}}/deploy" style="margin:0"><input type="hidden" name="tag" value="{{.Tag}}"><button class="btn sm{{if eq .Tag $.Running}} ghost{{end}}" type="submit">{{if eq .Tag $.Running}}Redeploy{{else}}Deploy{{end}}</button></form>{{else if not .File}}<span class="muted small">not kept</span>{{end}}</td></tr>{{end}}</table>
<p class="hint">Deploying an older build is how you roll back. A build that fails its health check rolls back by itself.</p>
{{else}}<p class="muted">No builds yet. Push to <code>{{$a.Branch}}</code> with the build workflow in place; builds show up here within a few minutes.</p>{{end}}</div>

<div class="section" id="settings"><h2>Settings</h2>
{{with .Form}}<form method="post" action="/admin/apps/{{$id}}/settings"><div class="cols">
<div><label>Name</label><input name="name" required value="{{.Name}}"></div>
<div><label>Path</label><input name="path" required value="{{.Path}}"><div class="hint">Like /quiz. Only one app can be the home app at /.</div></div>
<div><label>Shape</label><select name="shape">{{$sh := .Shape}}{{range $.Shapes}}<option{{if eq . $sh}} selected{{end}}>{{.}}</option>{{end}}</select></div>
<div><label>Who may sign in</label><select name="login"><option value="iima"{{if eq .Login "iima"}} selected{{end}}>@iima.ac.in accounts</option><option value="external"{{if eq .Login "external"}} selected{{end}}>any Google account</option></select></div>
<div><label>Signed-out visitors</label><select name="auth"><option value="optional"{{if eq .Auth "optional"}} selected{{end}}>may see the app (it decides)</option><option value="required"{{if eq .Auth "required"}} selected{{end}}>are sent to sign in</option></select></div>
<div><label>Public paths</label><input name="public" value="{{join .Public}}" placeholder="/api/health"><div class="hint">Comma separated, relative to the app; no sign-in check.</div></div>
<div><label>Roles</label><input name="roles" value="{{join .Roles}}" placeholder="admin, volunteer"><div class="hint">Comma separated. Granted on the Roles page.</div></div>
<div><label>Manager role</label><input name="manager_role" value="{{.ManagerRole}}"><div class="hint">Its holders manage the app's roles too.</div></div>
<div><label>Memory (MB)</label><input name="memory" type="number" min="64" max="4096" value="{{if .Memory}}{{.Memory}}{{else}}256{{end}}"></div>
<div><label>Review date</label><input name="review" type="date" value="{{.Review}}"><div class="hint">When the owners check the app is still needed.</div></div>
</div><label class="chk" style="margin-top:1rem"><input type="checkbox" name="realtime" value="1"{{if .Realtime}} checked{{end}}> Uses realtime (long-lived connections)</label>
<label class="chk"><input type="checkbox" name="pocketbase" value="1"{{if .PocketBase}} checked{{end}}> Next.js or Python: run PocketBase beside the app (data kept on its own volume)</label>
<div class="row"><button class="btn" type="submit">Save settings</button></div>
<p class="hint">Changes to the path, who may sign in, signed-out visitors, public paths or shape wait for a super admin. The rest applies now.</p>
</form>{{end}}</div>

<div class="section" id="env"><h2>Environment variables</h2>
<table>{{range .Env}}<tr><td><code>{{.Key}}</code></td><td>{{if .Secret}}<span class="pill">secret</span> <span class="muted small">set {{.UpdatedAt.Format "02 Jan"}} by {{.UpdatedBy}}</span>{{else}}<code>{{.Value}}</code>{{end}}</td>
<td style="text-align:right"><form class="inline" method="post" action="/admin/apps/{{$id}}/env/delete" style="margin:0"><input type="hidden" name="key" value="{{.Key}}"><button class="btn sm danger" type="submit">Remove</button></form></td></tr>
{{else}}<tr><td class="muted">None yet.</td></tr>{{end}}</table>
<form class="inline" method="post" action="/admin/apps/{{$id}}/env" style="margin-top:.9rem">
<input name="key" required placeholder="NAME" pattern="[A-Z_][A-Z0-9_]*" style="min-width:10rem"><input name="value" placeholder="value" autocomplete="off">
<label class="chk"><input type="checkbox" name="secret" value="1" checked> secret</label><button class="btn sm" type="submit">Save</button></form>
<p class="hint">Secrets go straight to Nomad and are never shown again; saving one restarts the app. Saving a plain value redeploys it.</p></div>

<div class="section" id="owners"><h2>Owners</h2>
<table>{{range .Owners}}<tr><td>{{.}}</td><td style="text-align:right"><form class="inline" method="post" action="/admin/apps/{{$id}}/owners" style="margin:0"><input type="hidden" name="email" value="{{.}}"><input type="hidden" name="action" value="remove"><button class="btn sm danger" type="submit">Remove</button></form></td></tr>{{end}}</table>
<form class="inline" method="post" action="/admin/apps/{{$id}}/owners" style="margin-top:.9rem"><input type="email" name="email" required placeholder="someone@iima.ac.in"><input type="hidden" name="action" value="add"><button class="btn sm" type="submit">Add owner</button></form>
<p class="hint">Owners deploy, change settings and env vars, and manage roles. Alerts go to them. Hand the app over by adding next year's team.</p></div>

<div class="section" id="deploys"><h2>Deploys</h2>
{{if .Deploys}}<table><tr><th>When (IST)</th><th>Build</th><th>By</th><th>Result</th></tr>
{{range .Deploys}}<tr><td class="small">{{.At.Format "02 Jan 15:04"}}</td><td><code>{{.Tag}}</code></td><td class="small">{{if eq .Actor "auto"}}<span class="pill">auto</span>{{else}}{{.Actor}}{{end}}</td><td>{{if eq .Status "submitted"}}<span class="pill ok">sent to Nomad</span>{{else}}<span class="pill bad">{{.Status}}</span>{{end}}{{with .Message}} <span class="muted small">{{.}}</span>{{end}}</td></tr>{{end}}</table>
{{else}}<p class="muted">None yet.</p>{{end}}</div>
{{template "bottom" .}}{{end}}

{{define "portallogs"}}{{template "top" .}}
<div class="head"><div><h1>Logs: {{.App.ID}}</h1><p class="muted">The end of the running copy's output.</p></div><a class="btn ghost" href="/admin/apps/{{.App.ID}}/deploy">Back to the app</a></div>
<div class="section"><h2>stderr</h2><pre>{{.Stderr}}</pre></div>
<div class="section"><h2>stdout</h2><pre>{{.Stdout}}</pre></div>
{{template "bottom" .}}{{end}}
`))

// Who is the signed-in person's email for the top bar.
func (d deployData) Who() string {
	if d.Actor == nil {
		return ""
	}
	return d.Actor.Email
}

func (s *Server) renderDeploy(w http.ResponseWriter, status int, name string, d deployData) {
	d.Wide = true
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := deployPages.ExecuteTemplate(w, name, d); err != nil {
		s.cfg.Log.Error("render", "page", name, "err", err)
	}
}

// memberActor is pageActor for pages any @iima.ac.in member may use (connecting a repo).
func (s *Server) memberActor(w http.ResponseWriter, r *http.Request) *actor {
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
	if !a.Member && !a.isAdmin() {
		s.render(w, http.StatusForbidden, "error", pageData{Title: "Institute accounts only", Message: "Sign in with your @iima.ac.in account to connect an app."})
		return nil
	}
	return a
}

var artifactRe = regexp.MustCompile(`^[0-9a-f]{64}\.tar\.gz$`)

func (s *Server) portalRoutes(mux *http.ServeMux) {
	p := s.cfg.Portal
	if p == nil {
		return
	}

	mux.HandleFunc("GET /admin/apps/new", func(w http.ResponseWriter, r *http.Request) {
		a := s.memberActor(w, r)
		if a == nil {
			return
		}
		msg, e := flash(r)
		s.renderDeploy(w, http.StatusOK, "portalnew", deployData{Title: "Connect an app", Actor: a, Message: msg, Error: e,
			Connectable: p.Connectable(), InstallURL: p.InstallURL(), Repo: r.URL.Query().Get("repo")})
	})
	mux.HandleFunc("POST /admin/apps/new", func(w http.ResponseWriter, r *http.Request) {
		a := s.memberActor(w, r)
		if a == nil {
			return
		}
		app, err := p.Request(r.Context(), a.Email, r.PostFormValue("repo"))
		if err != nil {
			s.portalDone(w, r, "/admin/apps/new", err, "")
			return
		}
		s.portalDone(w, r, "/admin/apps/"+app.ID+"/deploy", nil, "Requested. A super admin has been asked to approve it.")
	})

	// appFor loads a portal app the person may deploy (owners and super admins).
	appFor := func(w http.ResponseWriter, r *http.Request, member bool) (*actor, *store.PortalApp) {
		var a *actor
		if member {
			a = s.memberActor(w, r)
		} else {
			a = s.pageActor(w, r)
		}
		if a == nil {
			return nil, nil
		}
		pa, err := s.cfg.Store.PortalApp(r.Context(), r.PathValue("app"))
		if errors.Is(err, store.ErrNotFound) || (err == nil && !a.canDeploy(pa.ID)) {
			s.handleNotFound(w, r)
			return nil, nil
		}
		if err != nil {
			s.serverError(w, "portal app", err)
			return nil, nil
		}
		return a, pa
	}
	back := func(id string) string { return "/admin/apps/" + id + "/deploy" }

	mux.HandleFunc("GET /admin/apps/{app}/deploy", func(w http.ResponseWriter, r *http.Request) {
		a, pa := appFor(w, r, true)
		if pa == nil {
			return
		}
		ctx := r.Context()
		d := deployData{Title: pa.ID, Actor: a, App: pa, Shapes: []string{"go-pocketbase", "go", "next", "python"}}
		d.Message, d.Error = flash(r)
		d.Live, d.Pending = p.Settings(pa)
		d.Form = d.Live
		if d.Pending != nil {
			d.Form = d.Pending
		}
		var err error
		if d.Owners, err = s.cfg.Store.Owners(ctx, pa.ID); err == nil {
			if d.Env, err = s.cfg.Store.Env(ctx, pa.ID); err == nil {
				if d.Releases, err = s.cfg.Store.Releases(ctx, pa.ID); err == nil {
					d.Deploys, err = s.cfg.Store.Deploys(ctx, pa.ID, 20)
				}
			}
		}
		if err != nil {
			s.serverError(w, "portal page", err)
			return
		}
		for _, x := range d.Deploys {
			if x.Status == "submitted" {
				d.Running = x.Tag
				break
			}
		}
		if pa.Status == store.AppApproved {
			d.Status = p.Status(ctx, pa.ID)
			if d.Status != nil && d.Status.Release != "" {
				d.Running = d.Status.Release
			}
		}
		s.renderDeploy(w, http.StatusOK, "portalapp", d)
	})

	mux.HandleFunc("GET /admin/apps/{app}/logs", func(w http.ResponseWriter, r *http.Request) {
		a, pa := appFor(w, r, false)
		if pa == nil {
			return
		}
		out, errOut, err := p.Logs(r.Context(), pa.ID)
		if err != nil && !errors.Is(err, nomad.ErrNotFound) {
			s.portalDone(w, r, back(pa.ID), err, "")
			return
		}
		s.renderDeploy(w, http.StatusOK, "portallogs", deployData{Title: "Logs", Actor: a, App: pa, Stdout: out, Stderr: errOut})
	})

	super := func(fn func(a *actor, pa *store.PortalApp, r *http.Request) (string, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			a, pa := appFor(w, r, false)
			if pa == nil {
				return
			}
			if !a.Super {
				s.done(w, r, "", errForbidden, "")
				return
			}
			msg, err := fn(a, pa, r)
			s.portalDone(w, r, back(pa.ID), err, msg)
		}
	}
	owner := func(fn func(a *actor, pa *store.PortalApp, r *http.Request) (string, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			a, pa := appFor(w, r, true)
			if pa == nil {
				return
			}
			msg, err := fn(a, pa, r)
			s.portalDone(w, r, back(pa.ID), err, msg)
		}
	}

	mux.HandleFunc("POST /admin/apps/{app}/approve", super(func(a *actor, pa *store.PortalApp, r *http.Request) (string, error) {
		return "Approved. The newest build deploys within a minute.", p.Approve(r.Context(), a.Email, pa.ID)
	}))
	mux.HandleFunc("POST /admin/apps/{app}/reject", super(func(a *actor, pa *store.PortalApp, r *http.Request) (string, error) {
		return "Turned down.", p.Reject(r.Context(), a.Email, pa.ID)
	}))
	mux.HandleFunc("POST /admin/apps/{app}/settings", owner(func(a *actor, pa *store.PortalApp, r *http.Request) (string, error) {
		next, err := settingsFromForm(r)
		if err != nil {
			return "", err
		}
		waiting, err := p.UpdateSettings(r.Context(), a.Email, pa.ID, next, a.Super)
		if waiting {
			return "Saved. The change to how the app is reached waits for a super admin.", err
		}
		return "Saved.", err
	}))
	mux.HandleFunc("POST /admin/apps/{app}/env", owner(func(a *actor, pa *store.PortalApp, r *http.Request) (string, error) {
		key := r.PostFormValue("key")
		return "Saved " + key + ".", p.SetEnv(r.Context(), a.Email, pa.ID, key, r.PostFormValue("value"), r.PostFormValue("secret") == "1")
	}))
	mux.HandleFunc("POST /admin/apps/{app}/env/delete", owner(func(a *actor, pa *store.PortalApp, r *http.Request) (string, error) {
		key := r.PostFormValue("key")
		return "Removed " + key + ".", p.DeleteEnv(r.Context(), a.Email, pa.ID, key)
	}))
	mux.HandleFunc("POST /admin/apps/{app}/deploy", owner(func(a *actor, pa *store.PortalApp, r *http.Request) (string, error) {
		tag := r.PostFormValue("tag")
		return "Deploying " + tag + ". It takes a minute or two; a build that fails its health check rolls back.", p.Deploy(r.Context(), a.Email, pa.ID, tag)
	}))
	mux.HandleFunc("POST /admin/apps/{app}/autodeploy", owner(func(a *actor, pa *store.PortalApp, r *http.Request) (string, error) {
		on := r.PostFormValue("on") == "1"
		if on {
			p.Kick()
		}
		return "Saved.", s.cfg.Store.SetAutoDeploy(r.Context(), a.Email, pa.ID, on)
	}))
	mux.HandleFunc("POST /admin/apps/{app}/owners", owner(func(a *actor, pa *store.PortalApp, r *http.Request) (string, error) {
		email, err := validEmail(r.PostFormValue("email"))
		if err != nil {
			return "", err
		}
		err = s.cfg.Store.SetOwner(r.Context(), a.Email, pa.ID, email, r.PostFormValue("action") == "add")
		if err != nil && strings.Contains(err.Error(), "at least one owner") {
			err = userError("An app keeps at least one owner.")
		}
		return "Updated owners.", err
	}))

	// GitHub tells us about new releases here. Public (GitHub must reach it), so the signature is
	// all that is trusted, and all it does is make the portal look for new builds sooner.
	mux.HandleFunc("POST /platform/github/webhook", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
		if err != nil || !p.Webhook(body, r.Header.Get("X-Hub-Signature-256")) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Nomad clients download builds from here over the cluster network. The gateway blocks this
	// path, and anything that came through a proxy (X-Forwarded-For) is refused too.
	mux.HandleFunc("GET /platform/artifacts/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if r.Header.Get("X-Forwarded-For") != "" || !artifactRe.MatchString(name) {
			s.handleNotFound(w, r)
			return
		}
		path, err := p.ArtifactFile(r.Context(), strings.TrimSuffix(name, ".tar.gz"))
		if err != nil {
			s.handleNotFound(w, r)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			s.handleNotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/gzip")
		if st, err := f.Stat(); err == nil {
			w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		}
		_, _ = io.Copy(w, f)
	})
}

// portalDone is done() that also shows the portal's own user errors on the page.
func (s *Server) portalDone(w http.ResponseWriter, r *http.Request, back string, err error, ok string) {
	var pe portal.UserError
	if errors.As(err, &pe) {
		err = userError(pe.Error())
	}
	s.done(w, r, back, err, ok)
}

func settingsFromForm(r *http.Request) (*registry.App, error) {
	list := func(name string) []string {
		var out []string
		for _, v := range strings.Split(r.PostFormValue(name), ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	a := &registry.App{
		Name: strings.TrimSpace(r.PostFormValue("name")), Path: strings.TrimSpace(r.PostFormValue("path")),
		Shape: r.PostFormValue("shape"), Login: r.PostFormValue("login"), Auth: r.PostFormValue("auth"),
		Public: list("public"), Roles: list("roles"), ManagerRole: strings.TrimSpace(r.PostFormValue("manager_role")),
		Review: r.PostFormValue("review"), Realtime: r.PostFormValue("realtime") == "1",
		PocketBase: r.PostFormValue("pocketbase") == "1",
	}
	if m := r.PostFormValue("memory"); m != "" {
		n, err := strconv.Atoi(m)
		if err != nil || n < 64 || n > 4096 {
			return nil, userError("Memory is in MB, between 64 and 4096.")
		}
		a.Memory = n
	}
	return a, nil
}

// portalRows lists the portal apps a person owns (every one for super admins).
func (s *Server) portalRows(r *http.Request, a *actor) ([]portalRow, error) {
	all, err := s.cfg.Store.PortalApps(r.Context())
	if err != nil {
		return nil, err
	}
	var rows []portalRow
	for _, pa := range all {
		if !a.canDeploy(pa.ID) || (pa.Status == store.AppArchived && !a.Super) {
			continue
		}
		live, pending := s.cfg.Portal.Settings(pa)
		show := live
		if show == nil {
			show = pending
		}
		if show == nil {
			continue
		}
		rows = append(rows, portalRow{ID: pa.ID, Name: show.Name, Path: show.Path, Repo: pa.Repo, Status: pa.Status, Waiting: pending != nil})
	}
	return rows, nil
}
