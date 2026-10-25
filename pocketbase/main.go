// Command pocketbase is the platform's build of stock PocketBase, for apps that run it as a separate
// process next to a Next.js or Python server (BetaMarkets, Portfolio League). Building it here pins
// the version in go.mod, the same one iimakit/pb uses, instead of each app downloading its own.
//
// It is PocketBase's examples/base with the parts that do not fit the platform taken out:
// no self-update (versions change through this repo), no static file serving (PocketBase has no
// public route; the app server is the only client), and automigrate and hook watching off by
// default (on the cluster nothing should write migration files or restart on its own).
//
//	pocketbase serve --http 127.0.0.1:8091 --dir data --migrationsDir pb_migrations --hooksDir pb_hooks
//	pocketbase superuser upsert you@iima.ac.in <password> --dir data
package main

import (
	"log"
	"os"

	"github.com/pocketbase/pocketbase"
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

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
