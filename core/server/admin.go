package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arnav127/iima-platform/core/attrs"
	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/store"
)

// Three tiers: super admins (create apps, appoint app admins, fix attributes), app admins (manage
// roles in their own apps), and everyone else. The same rules back the JSON API under /auth/api
// (for apps' own admin screens) and the /admin pages.

type actor struct {
	Email  string
	Member bool // signed in with an @iima.ac.in account: may connect a repo
	Super  bool
	Apps   map[string]bool // apps this person is an app admin of
	Owns   map[string]bool // portal apps this person owns (owners manage the app's roles too)
}

func (a *actor) canManage(app string) bool { return a.Super || a.Apps[app] }
func (a *actor) isAdmin() bool             { return a.Super || len(a.Apps) > 0 }
func (a *actor) canDeploy(app string) bool { return a.Super || a.Owns[app] }

func (s *Server) actorFor(ctx context.Context, sess *store.Session) (*actor, error) {
	a := &actor{Email: sess.User.Email, Member: sess.User.Member, Apps: map[string]bool{}, Owns: map[string]bool{}}
	var err error
	if a.Super, err = s.cfg.Store.IsSuperAdmin(ctx, a.Email); err != nil {
		return nil, err
	}
	owned, err := s.cfg.Store.OwnedApps(ctx, a.Email)
	if err != nil {
		return nil, err
	}
	for _, id := range owned {
		a.Owns[id], a.Apps[id] = true, true
	}
	ids, err := s.cfg.Store.AdminOf(ctx, a.Email)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		a.Apps[id] = true
	}
	// Holders of an app's manager_role manage it too.
	for _, app := range s.registry().Apps() {
		if app.ManagerRole == "" || a.Apps[app.ID] {
			continue
		}
		roles, err := s.roles(ctx, app.ID, a.Email)
		if err != nil {
			return nil, err
		}
		if slices.Contains(roles, app.ManagerRole) {
			a.Apps[app.ID] = true
		}
	}
	return a, nil
}

// userError is a problem with the request, shown to the admin as is.
type userError string

func (e userError) Error() string { return string(e) }

func validEmail(email string) (string, error) {
	email = store.NormEmail(email)
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email || len(email) > 254 {
		return "", userError("not a valid email address: " + email)
	}
	return email, nil
}

// ---- the operations, shared by the API and the pages ----

func (s *Server) setRoles(ctx context.Context, a *actor, app *registry.App, email string, roles []string) error {
	if !a.canManage(app.ID) {
		return errForbidden
	}
	email, err := validEmail(email)
	if err != nil {
		return err
	}
	for _, r := range roles {
		if !slices.Contains(app.Roles, r) {
			return userError("role " + strconv.Quote(r) + " is not declared in " + app.ID + "'s app.toml")
		}
	}
	if err := s.cfg.Store.SetRoles(ctx, a.Email, app.ID, email, roles); err != nil {
		return err
	}
	s.ids.forget(email)
	return nil
}

func (s *Server) setAppAdmin(ctx context.Context, a *actor, app *registry.App, email string, add bool) error {
	if !a.Super {
		return errForbidden
	}
	email, err := validEmail(email)
	if err != nil {
		return err
	}
	if add {
		return s.cfg.Store.AddAppAdmin(ctx, a.Email, app.ID, email)
	}
	return s.cfg.Store.RemoveAppAdmin(ctx, a.Email, app.ID, email)
}

func (s *Server) setSuperAdmin(ctx context.Context, a *actor, email string, add bool) error {
	if !a.Super {
		return errForbidden
	}
	email, err := validEmail(email)
	if err != nil {
		return err
	}
	if add {
		return s.cfg.Store.AddSuperAdmin(ctx, a.Email, email)
	}
	err = s.cfg.Store.RemoveSuperAdmin(ctx, a.Email, email)
	if errors.Is(err, store.ErrLastSuperAdmin) {
		return userError("that is the last super admin; add another one first")
	}
	return err
}

func (s *Server) setOverride(ctx context.Context, a *actor, o store.Override) error {
	if !a.Super {
		return errForbidden
	}
	email, err := validEmail(o.Email)
	if err != nil {
		return err
	}
	o.Email = email
	o.Programme = strings.TrimSpace(o.Programme)
	o.Note = strings.TrimSpace(o.Note)
	if o.Affiliation != "" && !slices.Contains(attrs.Affiliations, o.Affiliation) {
		return userError("affiliation must be one of " + strings.Join(attrs.Affiliations, ", "))
	}
	if o.Batch != 0 && (o.Batch < 1961 || o.Batch > time.Now().Year()+1) {
		return userError("batch must be a year like 2025")
	}
	if err := s.cfg.Store.SetOverride(ctx, a.Email, o); err != nil {
		return err
	}
	s.ids.forget(email)
	return nil
}

// person is what the platform says about one email: derived attributes, the override, roles.
type person struct {
	Email      string              `json:"email"`
	Attributes attrs.Attributes    `json:"attributes"`
	Override   *store.Override     `json:"override,omitempty"`
	Roles      map[string][]string `json:"roles"` // app id -> roles
}

func (s *Server) lookupPerson(ctx context.Context, email string) (*person, error) {
	email, err := validEmail(email)
	if err != nil {
		return nil, err
	}
	o, err := s.cfg.Store.AttributeOverride(ctx, email)
	if err != nil {
		return nil, err
	}
	// Membership is only known once someone signs in with Google; for a lookup, an address on the
	// member domain stands in for it.
	member := strings.HasSuffix(email, "@"+strings.ToLower(s.cfg.MemberDomain))
	p := &person{Email: email, Attributes: attrs.Derive(email, member, o, time.Now()), Override: o, Roles: map[string][]string{}}
	for _, app := range s.registry().Apps() {
		r, err := s.cfg.Store.Roles(ctx, app.ID, email)
		if err != nil {
			return nil, err
		}
		if len(r) > 0 {
			p.Roles[app.ID] = r
		}
	}
	return p, nil
}

var errForbidden = errors.New("forbidden")

// ---- JSON API ----

// apiActor authenticates an API call: a signed-in admin, and for changes a same-origin request.
// Changes are PUT and DELETE, which a page on another site cannot send without a CORS preflight
// that core never answers; the Origin check covers older browsers.
func (s *Server) apiActor(w http.ResponseWriter, r *http.Request) *actor {
	sess := s.session(r)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in required"})
		return nil
	}
	if r.Method != http.MethodGet {
		if !s.sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request refused"})
			return nil
		}
	}
	a, err := s.actorFor(r.Context(), sess)
	if err != nil {
		s.apiError(w, err)
		return nil
	}
	if !a.isAdmin() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admins only"})
		return nil
	}
	return a
}

func (s *Server) apiError(w http.ResponseWriter, err error) {
	var ue userError
	switch {
	case errors.As(err, &ue):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": ue.Error()})
	case errors.Is(err, errForbidden):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not allowed for this app"})
	default:
		s.cfg.Log.Error("admin api", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "something went wrong"})
	}
}

// apiApp finds {app} and checks the actor may manage it.
func (s *Server) apiApp(w http.ResponseWriter, r *http.Request, a *actor) *registry.App {
	app := s.registry().ByID(r.PathValue("app"))
	if app == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such app"})
		return nil
	}
	if !a.canManage(app.ID) {
		s.apiError(w, errForbidden)
		return nil
	}
	return app
}

func (s *Server) apiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/api/apps", func(w http.ResponseWriter, r *http.Request) {
		a := s.apiActor(w, r)
		if a == nil {
			return
		}
		type appView struct {
			ID    string   `json:"id"`
			Name  string   `json:"name"`
			Roles []string `json:"roles"`
		}
		out := []appView{}
		for _, app := range s.registry().Apps() {
			if a.canManage(app.ID) {
				out = append(out, appView{app.ID, app.Name, append([]string{}, app.Roles...)})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"super": a.Super, "apps": out})
	})

	mux.HandleFunc("GET /auth/api/apps/{app}/roles", func(w http.ResponseWriter, r *http.Request) {
		a := s.apiActor(w, r)
		if a == nil {
			return
		}
		app := s.apiApp(w, r, a)
		if app == nil {
			return
		}
		g, err := s.cfg.Store.Grants(r.Context(), app.ID)
		if err != nil {
			s.apiError(w, err)
			return
		}
		if g == nil {
			g = []store.Grant{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"roles": app.Roles, "grants": g})
	})

	setRoles := func(w http.ResponseWriter, r *http.Request, roles []string) {
		a := s.apiActor(w, r)
		if a == nil {
			return
		}
		app := s.apiApp(w, r, a)
		if app == nil {
			return
		}
		if err := s.setRoles(r.Context(), a, app, r.PathValue("email"), roles); err != nil {
			s.apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, store.Grant{Email: store.NormEmail(r.PathValue("email")), Roles: append([]string{}, roles...)})
	}
	mux.HandleFunc("PUT /auth/api/apps/{app}/roles/{email}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Roles []string `json:"roles"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"roles": [...]}`})
			return
		}
		setRoles(w, r, body.Roles)
	})
	mux.HandleFunc("DELETE /auth/api/apps/{app}/roles/{email}", func(w http.ResponseWriter, r *http.Request) {
		setRoles(w, r, nil)
	})

	mux.HandleFunc("GET /auth/api/apps/{app}/admins", func(w http.ResponseWriter, r *http.Request) {
		a := s.apiActor(w, r)
		if a == nil {
			return
		}
		app := s.apiApp(w, r, a)
		if app == nil {
			return
		}
		admins, err := s.cfg.Store.AppAdmins(r.Context(), app.ID)
		if err != nil {
			s.apiError(w, err)
			return
		}
		emails := []string{}
		for _, ad := range admins {
			emails = append(emails, ad.Email)
		}
		writeJSON(w, http.StatusOK, map[string]any{"admins": emails})
	})
	appAdmin := func(add bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			a := s.apiActor(w, r)
			if a == nil {
				return
			}
			app := s.apiApp(w, r, a)
			if app == nil {
				return
			}
			if err := s.setAppAdmin(r.Context(), a, app, r.PathValue("email"), add); err != nil {
				s.apiError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}
	mux.HandleFunc("PUT /auth/api/apps/{app}/admins/{email}", appAdmin(true))
	mux.HandleFunc("DELETE /auth/api/apps/{app}/admins/{email}", appAdmin(false))

	mux.HandleFunc("GET /auth/api/people/{email}", func(w http.ResponseWriter, r *http.Request) {
		a := s.apiActor(w, r)
		if a == nil {
			return
		}
		if !a.Super {
			s.apiError(w, errForbidden)
			return
		}
		p, err := s.lookupPerson(r.Context(), r.PathValue("email"))
		if err != nil {
			s.apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})
	mux.HandleFunc("PUT /auth/api/people/{email}", func(w http.ResponseWriter, r *http.Request) {
		a := s.apiActor(w, r)
		if a == nil {
			return
		}
		var o store.Override
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&o); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be an override object"})
			return
		}
		o.Email = r.PathValue("email")
		if err := s.setOverride(r.Context(), a, o); err != nil {
			s.apiError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /auth/api/audit", func(w http.ResponseWriter, r *http.Request) {
		a := s.apiActor(w, r)
		if a == nil {
			return
		}
		app := r.URL.Query().Get("app")
		if !a.Super && (app == "" || !a.canManage(app)) {
			s.apiError(w, errForbidden)
			return
		}
		entries, err := s.cfg.Store.Audit(r.Context(), app, 200)
		if err != nil {
			s.apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": auditRows(entries)})
	})
}

type auditRow struct {
	At     string `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	App    string `json:"app,omitempty"`
	Target string `json:"target,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func auditRows(entries []store.AuditEntry) []auditRow {
	out := []auditRow{}
	for _, e := range entries {
		out = append(out, auditRow{e.At.In(ist).Format("2006-01-02 15:04"), e.Actor, e.Action, e.AppID, e.Target, e.Detail})
	}
	return out
}

var ist = time.FixedZone("IST", 5*3600+1800)
