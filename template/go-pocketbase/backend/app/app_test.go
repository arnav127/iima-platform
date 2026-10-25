package app

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"

	iimakit "github.com/arnav127/iima-platform/kit"

	_ "example.com/myapp/backend/migrations"
)

func newApp(t *testing.T) core.App {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.ResetBootstrapState() })
	return app
}

func TestUserFor(t *testing.T) {
	app := newApp(t)
	u, err := UserFor(app, &iimakit.Claims{Email: "P25Someone@iima.ac.in", Name: "Some One"})
	if err != nil || u.Email() != "p25someone@iima.ac.in" || u.GetString("name") != "Some One" {
		t.Fatalf("first visit: %v %v", u, err)
	}
	again, err := UserFor(app, &iimakit.Claims{Email: "p25someone@iima.ac.in", Name: "Someone Else"})
	if err != nil || again.Id != u.Id || again.GetString("name") != "Someone Else" {
		t.Fatalf("second visit: %v %v", again, err)
	}
}
