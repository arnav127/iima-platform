// Command demo is the stub app for the local stack. It shows who the gateway says is signed in,
// and what identity-related headers actually reached the app (there should be none but the token).
//
// Configuration: APP_ID and APP_PATH (e.g. demo and /demo), PORT, IIMA_JWKS_URL.
package main

import (
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	iimakit "github.com/arnav127/iima-platform/kit"
)

func main() {
	appID := os.Getenv("APP_ID")
	base := os.Getenv("APP_PATH")
	if appID == "" || !strings.HasPrefix(base, "/") {
		slog.Error("set APP_ID and APP_PATH, e.g. APP_ID=demo APP_PATH=/demo")
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+base+"/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("GET "+base, http.RedirectHandler(base+"/", http.StatusFound))
	mux.HandleFunc("GET "+base+"/{$}", func(w http.ResponseWriter, r *http.Request) { page(w, r, appID, base) })
	mux.Handle("GET "+base+"/private", iimakit.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("Only signed-in users see this. Hello, " + iimakit.User(r).Email + "\n"))
	})))
	mux.Handle("GET "+base+"/admin", iimakit.RequireRole("admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("admin area\n"))
	})))

	addr := iimakit.Addr()
	slog.Info("demo listening", "app", appID, "path", base, "addr", addr)
	srv := &http.Server{Addr: addr, Handler: iimakit.Middleware(appID)(mux), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("demo stopped", "err", err)
		os.Exit(1)
	}
}

var tmpl = template.Must(template.New("").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.AppID}}</title>
<style>
:root{color-scheme:light dark}body{font:16px/1.5 system-ui,sans-serif;max-width:42rem;margin:2rem auto;padding:0 1rem}
pre{background:rgba(127,127,127,.12);padding:1rem;border-radius:8px;overflow:auto;font-size:.85rem}
.status{font-size:1.4rem;font-weight:600}td{padding:.15rem .75rem .15rem 0;vertical-align:top}code{font-size:.9em}
</style></head><body>
<p><a href="/">Home</a> · app <code>{{.AppID}}</code></p>
{{if .User}}
<p class="status">Signed in as {{.User.Email}}</p>
<p>{{if .User.Member}}IIMA member{{else}}Not an IIMA member{{end}} · provider {{.User.Provider}} · <a href="{{.Logout}}">Sign out</a></p>
<p>Verified claims from <code>X-User-Token</code>:</p>
<pre>{{.ClaimsJSON}}</pre>
{{else}}
<p class="status">Signed out</p>
<p><a href="{{.Login}}">Sign in</a></p>
{{end}}
<p><a href="{{.Base}}/private">{{.Base}}/private</a> (needs sign-in) · <a href="{{.Base}}/admin">{{.Base}}/admin</a> (needs role admin)</p>
<h3>What reached the app</h3>
<table>
<tr><td>X-User-Token</td><td>{{if .HasToken}}present{{else}}absent{{end}}</td></tr>
<tr><td>Other X-User-* headers</td><td>{{if .OtherIdentity}}<strong>{{.OtherIdentity}}</strong> (should never happen){{else}}none{{end}}</td></tr>
<tr><td>Authorization</td><td>{{if .HasAuthz}}<strong>present</strong> (should never happen){{else}}absent{{end}}</td></tr>
<tr><td>Cookies</td><td>{{if .Cookies}}{{.Cookies}}{{else}}none{{end}}{{if .SawSession}} <strong>(platform session cookie leaked)</strong>{{end}}</td></tr>
</table>
</body></html>`))

func page(w http.ResponseWriter, r *http.Request, appID, base string) {
	u := iimakit.User(r)
	d := map[string]any{
		"AppID": appID, "Base": base, "User": u,
		"Login": iimakit.LoginURL(base + "/"), "Logout": iimakit.LogoutURL + "?rd=" + base + "/",
		"HasToken": r.Header.Get(iimakit.TokenHeader) != "",
		"HasAuthz": r.Header.Get("Authorization") != "",
	}
	if u != nil {
		b, _ := json.MarshalIndent(u, "", "  ")
		d["ClaimsJSON"] = string(b)
	}
	var other []string
	for name := range r.Header {
		// Underscore and dot variants count too: some backends read X_User_Token as X-User-Token.
		norm := strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(name))
		if strings.HasPrefix(norm, "x-user-") && name != iimakit.TokenHeader {
			other = append(other, name)
		}
	}
	sort.Strings(other)
	d["OtherIdentity"] = strings.Join(other, ", ")
	var names []string
	for _, c := range r.Cookies() {
		names = append(names, c.Name)
		if c.Name == "iima_session" {
			d["SawSession"] = true
		}
	}
	d["Cookies"] = strings.Join(names, ", ")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := tmpl.Execute(w, d); err != nil {
		slog.Error("render", "err", err)
	}
}
