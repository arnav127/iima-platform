// Package app is myapp's own code: who gets in, and its routes under /api/myapp.
package app

import (
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"

	iimakit "github.com/arnav127/iima-platform/kit"
	"github.com/arnav127/iima-platform/kit/pb"
)

// UserFor finds or creates the users record for a platform user. Everyone the platform lets in
// (see login in app.toml) gets an account here. To keep a list of your own (members, investors,
// guests), check it here and return nil, nil for people who are not on it: they then see the
// app signed out.
func UserFor(app core.App, c *iimakit.Claims) (*core.Record, error) {
	email := strings.ToLower(c.Email)
	u, err := app.FindAuthRecordByEmail("users", email)
	if err == nil {
		if c.Name != "" && u.GetString("name") != c.Name {
			u.Set("name", c.Name)
			return u, app.Save(u)
		}
		return u, nil
	}
	col, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return nil, err
	}
	u = core.NewRecord(col)
	u.SetEmail(email)
	u.SetVerified(true)
	u.SetRandomPassword() // never used: PocketBase's own sign-in is off
	u.Set("name", c.Name)
	return u, app.Save(u)
}

// Register adds myapp's routes.
func Register(app core.App) {
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		g := se.Router.Group("/api/myapp")

		// Anyone: says who is signed in, as the platform and this app see them.
		g.GET("/me", func(e *core.RequestEvent) error {
			out := map[string]any{"signedIn": e.Auth != nil}
			if c := pb.Claims(e); c != nil {
				out["email"], out["name"], out["programme"], out["year"] = c.Email, c.Name, c.Programme, c.Year
				out["roles"] = append([]string{}, c.Roles...)
			}
			return e.JSON(http.StatusOK, out)
		})

		// Admins only: roles come from the platform (declared in app.toml, granted in /admin).
		g.GET("/admin/stats", func(e *core.RequestEvent) error {
			if !pb.HasRole(e, "admin") {
				return e.ForbiddenError("Admins only.", nil)
			}
			n, err := e.App.CountRecords("users")
			if err != nil {
				return err
			}
			return e.JSON(http.StatusOK, map[string]int64{"users": n})
		}).Bind(apis.RequireAuth("users"))

		return se.Next()
	})
}
