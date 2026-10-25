// Package migrations holds myapp's schema, applied by PocketBase on start. Add a new file for each
// change; never edit one that has run on the server.
package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

func init() {
	m.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		// People read only their own record; the app's routes do the rest.
		users.ListRule = types.Pointer("id = @request.auth.id")
		users.ViewRule = types.Pointer("id = @request.auth.id")
		users.CreateRule, users.UpdateRule, users.DeleteRule = nil, nil, nil
		users.AuthAlert.Enabled = false
		// The platform's roles for this app, mirrored by iimakit/pb on every request, so API rules
		// like @request.auth.roles ~ "admin" work. Never edited here.
		users.Fields.Add(&core.JSONField{Name: "roles"})
		return app.Save(users)
	}, nil)
}
