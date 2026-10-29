// Command pocketbase is the platform's build of stock PocketBase, for apps that run it as a separate
// process next to a Next.js or Python server (BetaMarkets, Portfolio League). Building it here pins
// the version in go.mod, the same one iimakit/pb uses, instead of each app downloading its own.
//
// It is PocketBase's examples/base with the parts that do not fit the platform taken out:
// no self-update (versions change through this repo), no static file serving (PocketBase has no
// public route; the app server is the only client), and automigrate and hook watching off by
// default (on the cluster nothing should write migration files or restart on its own).
//
// One addition: with PB_ADMIN_EMAIL and PB_ADMIN_PASSWORD set (the app's secrets on the platform),
// it makes sure that superuser exists on every start, so a new app's data needs no manual setup and
// the app server and PocketBase always agree on the credentials.
//
//	pocketbase serve --http 127.0.0.1:8091 --dir data --migrationsDir pb_migrations --hooksDir pb_hooks
//	pocketbase superuser upsert you@iima.ac.in <password> --dir data
package main

import (
	"log"
	"os"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/jsvm"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
)

func main() {
	app := pocketbase.New()

	var hooksDir, migrationsDir string
	var hooksWatch, automigrate bool
	flags := app.RootCmd.PersistentFlags()
	flags.StringVar(&hooksDir, "hooksDir", "", "the directory with the JS app hooks")
	flags.StringVar(&migrationsDir, "migrationsDir", "", "the directory with the JS migrations")
	flags.BoolVar(&hooksWatch, "hooksWatch", false, "restart on pb_hooks changes (local development only)")
	flags.BoolVar(&automigrate, "automigrate", false, "write a migration file for every schema change made in the dashboard (local development only)")
	_ = app.RootCmd.ParseFlags(os.Args[1:])

	jsvm.MustRegister(app, jsvm.Config{
		MigrationsDir: migrationsDir,
		HooksDir:      hooksDir,
		HooksWatch:    hooksWatch,
	})
	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		TemplateLang: migratecmd.TemplateLangJS,
		Automigrate:  automigrate,
		Dir:          migrationsDir,
	})

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		email, password := os.Getenv("PB_ADMIN_EMAIL"), os.Getenv("PB_ADMIN_PASSWORD")
		if email != "" && password != "" {
			if err := ensureSuperuser(e.App, email, password); err != nil {
				return err
			}
		}
		return e.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

// ensureSuperuser creates the superuser, or resets its password when it no longer matches. A
// matching password is left alone: setting it again would sign out the app server's session.
func ensureSuperuser(app core.App, email, password string) error {
	col, err := app.FindCachedCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		return err
	}
	rec, err := app.FindAuthRecordByEmail(col, email)
	if err != nil {
		rec = core.NewRecord(col)
		rec.SetEmail(email)
	} else if rec.ValidatePassword(password) {
		return nil
	}
	rec.SetPassword(password)
	return app.Save(rec)
}
