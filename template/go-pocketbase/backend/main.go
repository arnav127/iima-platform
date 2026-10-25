// myapp: a PocketBase app on the students.iima.ac.in platform. Start from here for a new app.
//
//	go run . serve                     # dev on its own, http://127.0.0.1:8090 (dashboard at /_/)
//	IIMA_DEV_USER=you@iima.ac.in go run . serve   # ...signed in as you, no platform needed
//
// On the platform the gateway signs people in and passes a verified X-User-Token; iimakit/pb turns
// it into e.Auth, keeps the users.roles field in step with the platform's roles, serves the
// frontend from pb_public, and keeps the dashboard off the internet.
package main

import (
	"log"

	"github.com/pocketbase/pocketbase/core"

	iimakit "github.com/arnav127/iima-platform/kit"
	"github.com/arnav127/iima-platform/kit/pb"

	"example.com/myapp/backend/app"
	_ "example.com/myapp/backend/migrations"
)

func main() {
	papp := pb.New(pb.Config{
		AppID:   "myapp", // the id in apps/myapp.toml
		AppName: "My App",
		Access: func(e *core.RequestEvent, c *iimakit.Claims) (*core.Record, error) {
			return app.UserFor(e.App, c)
		},
	})
	app.Register(papp)

	if err := papp.Start(); err != nil {
		log.Fatal(err)
	}
}
