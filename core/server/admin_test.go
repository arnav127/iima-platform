package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	iimakit "github.com/arnav127/iima-platform/kit"
)

// api makes a JSON call as browser c, from the platform's own origin unless hdr says otherwise.
func (e *env) api(c *http.Client, method, path string, body any, hdr map[string]string) (int, map[string]any) {
	e.t.Helper()
	var rb io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rb = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rb)
	req.Header.Set("Origin", e.srv.URL)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) claims(c *http.Client, app string) *iimakit.Claims {
	e.t.Helper()
	tok := e.verify(c, "optional", "/"+app+"/", nil).Header.Get("X-User-Token")
	if tok == "" {
		e.t.Fatal("no token")
	}
	cl, err := e.verifier(app).Verify(context.Background(), tok)
	if err != nil {
		e.t.Fatal(err)
	}
	return cl
}

// adminEnv has a super admin, an app admin of demo, and a plain user, each signed in.
func adminEnv(t *testing.T) (e *env, super, appAdmin, user *http.Client) {
	e = newEnv(t, true)
	ctx := context.Background()
	if err := e.cfg.Store.AddSuperAdmin(ctx, "test", "boss@iima.ac.in"); err != nil {
		t.Fatal(err)
	}
	if err := e.cfg.Store.AddAppAdmin(ctx, "test", "demo", "owner@iima.ac.in"); err != nil {
		t.Fatal(err)
	}
	super, appAdmin, user = e.browser(), e.browser(), e.browser()
	e.devSignIn(super, "boss@iima.ac.in", "/")
	e.devSignIn(appAdmin, "owner@iima.ac.in", "/")
	e.devSignIn(user, "p25aarav@iima.ac.in", "/")
	return
}

func TestTokenCarriesAttributesAndRoles(t *testing.T) {
	e, _, owner, user := adminEnv(t)

	cl := e.claims(user, "demo")
	if cl.Affiliation != "student" || cl.Programme != "PGP" || cl.Batch != 2025 || cl.Year == 0 || len(cl.Roles) != 0 {
		t.Fatalf("before: %+v", cl)
	}

	// The app admin grants a role; the very next token has it, and only for that app.
	if code, body := e.api(owner, "PUT", "/auth/api/apps/demo/roles/P25Aarav@iima.ac.in", map[string]any{"roles": []string{"writer"}}, nil); code != 200 {
		t.Fatalf("grant: %d %v", code, body)
	}
	if cl := e.claims(user, "demo"); strings.Join(cl.Roles, ",") != "writer" {
		t.Fatalf("roles after grant: %v", cl.Roles)
	}
	if cl := e.claims(user, "demo-external"); len(cl.Roles) != 0 {
		t.Fatalf("roles leaked to another app: %v", cl.Roles)
	}
	_, me := e.api(user, "GET", "/auth/me?app=demo", nil, nil)
	if r, _ := me["roles"].([]any); len(r) != 1 || me["programme"] != "PGP" {
		t.Fatalf("/auth/me: %v", me)
	}

	// Removing takes effect at once too.
	if code, _ := e.api(owner, "DELETE", "/auth/api/apps/demo/roles/p25aarav@iima.ac.in", nil, nil); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	if cl := e.claims(user, "demo"); len(cl.Roles) != 0 {
		t.Fatalf("roles after removal: %v", cl.Roles)
	}
}

func TestRoleAPIPermissions(t *testing.T) {
	e, super, owner, user := adminEnv(t)
	grant := map[string]any{"roles": []string{"admin"}}

	cases := []struct {
		name   string
		c      *http.Client
		method string
		path   string
		body   any
		hdr    map[string]string
		want   int
	}{
		{"signed out", e.browser(), "GET", "/auth/api/apps", nil, nil, 401},
		{"plain user", user, "GET", "/auth/api/apps", nil, nil, 403},
		{"plain user cannot grant", user, "PUT", "/auth/api/apps/demo/roles/x@iima.ac.in", grant, nil, 403},
		{"app admin, own app", owner, "PUT", "/auth/api/apps/demo/roles/x@iima.ac.in", grant, nil, 200},
		{"app admin, other app", owner, "PUT", "/auth/api/apps/demo-external/roles/x@iima.ac.in", map[string]any{"roles": []string{}}, nil, 403},
		{"undeclared role", owner, "PUT", "/auth/api/apps/demo/roles/x@iima.ac.in", map[string]any{"roles": []string{"god"}}, nil, 400},
		{"bad email", owner, "PUT", "/auth/api/apps/demo/roles/not-an-email", grant, nil, 400},
		{"unknown app", super, "GET", "/auth/api/apps/nope/roles", nil, nil, 404},
		{"cross-origin", owner, "PUT", "/auth/api/apps/demo/roles/x@iima.ac.in", grant, map[string]string{"Origin": "https://evil.example"}, 403},
		{"app admin cannot appoint app admins", owner, "PUT", "/auth/api/apps/demo/admins/friend@iima.ac.in", nil, nil, 403},
		{"super admin appoints an app admin", super, "PUT", "/auth/api/apps/demo/admins/friend@iima.ac.in", nil, nil, 204},
		{"app admin cannot read people", owner, "GET", "/auth/api/people/p25aarav@iima.ac.in", nil, nil, 403},
		{"app admin cannot set attributes", owner, "PUT", "/auth/api/people/p25aarav@iima.ac.in", map[string]any{"affiliation": "staff"}, nil, 403},
		{"app admin reads own audit", owner, "GET", "/auth/api/audit?app=demo", nil, nil, 200},
		{"app admin cannot read the whole audit", owner, "GET", "/auth/api/audit", nil, nil, 403},
		{"super admin reads the whole audit", super, "GET", "/auth/api/audit", nil, nil, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, body := e.api(c.c, c.method, c.path, c.body, c.hdr); code != c.want {
				t.Fatalf("got %d (%v), want %d", code, body, c.want)
			}
		})
	}
}

func TestOverridesAndAudit(t *testing.T) {
	e, super, _, user := adminEnv(t)

	if code, body := e.api(super, "PUT", "/auth/api/people/p25aarav@iima.ac.in", map[string]any{"affiliation": "staff", "note": "works at the library"}, nil); code != 204 {
		t.Fatalf("override: %d %v", code, body)
	}
	if cl := e.claims(user, "demo"); cl.Affiliation != "staff" || cl.Year != 0 {
		t.Fatalf("after override: %+v", cl)
	}
	if code, _ := e.api(super, "PUT", "/auth/api/people/p25aarav@iima.ac.in", map[string]any{"affiliation": "wizard"}, nil); code != 400 {
		t.Fatalf("bad affiliation accepted: %d", code)
	}
	// Clearing goes back to what the address says.
	e.api(super, "PUT", "/auth/api/people/p25aarav@iima.ac.in", map[string]any{}, nil)
	if cl := e.claims(user, "demo"); cl.Affiliation != "student" {
		t.Fatalf("after clearing: %+v", cl)
	}

	_, body := e.api(super, "GET", "/auth/api/audit", nil, nil)
	entries, _ := body["entries"].([]any)
	var actions []string
	for _, x := range entries {
		actions = append(actions, x.(map[string]any)["action"].(string))
	}
	if got := strings.Join(actions, ","); !strings.Contains(got, "attributes.clear,attributes.set") || !strings.Contains(got, "appadmin.add") || !strings.Contains(got, "superadmin.add") {
		t.Fatalf("audit: %s", got)
	}
}

func TestLastSuperAdminStays(t *testing.T) {
	e, super, _, _ := adminEnv(t)
	resp := e.do(super, "POST", "/admin/super-admins", url.Values{"email": {"boss@iima.ac.in"}, "action": {"remove"}}, nil)
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "err=") {
		t.Fatalf("removing the last super admin: %d %q", resp.StatusCode, loc)
	}
	if ok, _ := e.cfg.Store.IsSuperAdmin(context.Background(), "boss@iima.ac.in"); !ok {
		t.Fatal("last super admin removed")
	}
}

func TestAdminPages(t *testing.T) {
	e, super, owner, user := adminEnv(t)

	get := func(c *http.Client, path string) (int, string) {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, _ := get(e.browser(), "/admin"); code != http.StatusFound {
		t.Fatalf("signed out: %d", code)
	}
	if code, _ := get(user, "/admin"); code != http.StatusForbidden {
		t.Fatalf("plain user: %d", code)
	}
	if code, body := get(owner, "/admin"); code != 200 || !strings.Contains(body, "/admin/apps/demo") || strings.Contains(body, "/admin/apps/demo-external") || strings.Contains(body, "/admin/people") {
		t.Fatalf("app admin index: %d", code)
	}
	for _, p := range []string{"/admin", "/admin/apps/demo", "/admin/apps/demo-external", "/admin/people?email=p25aarav@iima.ac.in", "/admin/super-admins", "/admin/audit"} {
		if code, body := get(super, p); code != 200 || !strings.Contains(body, "boss@iima.ac.in") {
			t.Fatalf("super %s: %d", p, code)
		}
	}
	if code, _ := get(owner, "/admin/apps/demo-external"); code != 404 {
		t.Fatalf("app admin on another app: %d", code)
	}

	// Grant through the form, as a browser would post it.
	resp := e.do(owner, "POST", "/admin/apps/demo/roles", url.Values{"email": {"p25aarav@iima.ac.in"}, "role": {"admin", "writer"}}, map[string]string{"Origin": e.srv.URL})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "msg=") {
		t.Fatalf("form grant: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if cl := e.claims(user, "demo"); strings.Join(cl.Roles, ",") != "admin,writer" {
		t.Fatalf("roles: %v", cl.Roles)
	}
	if code, body := get(owner, "/admin/apps/demo"); code != 200 || !strings.Contains(body, "p25aarav@iima.ac.in") || !strings.Contains(body, "roles.set") {
		t.Fatalf("app page after grant: %d", code)
	}

	// Cross-origin form posts are refused.
	resp = e.do(owner, "POST", "/admin/apps/demo/roles", url.Values{"email": {"p25aarav@iima.ac.in"}}, map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin form: %d", resp.StatusCode)
	}
}
