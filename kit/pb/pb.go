// Package pb puts a Go + PocketBase app behind the platform. It does what each app used to write by
// hand: sign-in (replaced by the gateway's X-User-Token), env config, frontend serving, backups,
// and keeping the PocketBase dashboard off the internet.
//
//	app := pb.New(pb.Config{AppID: "auxesiscapital", Access: s.Access})
//	// app is the real *pocketbase.PocketBase: add hooks, routes and commands as usual.
//	app.Start()
//
// Every default can be turned off in Config. This is a separate Go module so that iimakit itself
// never depends on PocketBase. It is pinned to the PocketBase minor version the apps use (v0.40).
package pb

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/osutils"

	iimakit "github.com/arnav127/iima-platform/kit"
)

// AccessFunc maps a signed-in platform user to the app's own auth record (usually in "users"),
// creating or updating it as needed. Return (nil, nil) when the person is not a user of this app:
// the request then continues as signed out, and Claims(e) still says who they are. Return an
// error only for real failures; it fails the request.
//
// It runs on every request with a token, so removing someone from the app's list takes effect on
// their next request. Keep it to a few indexed lookups.
type AccessFunc func(e *core.RequestEvent, c *iimakit.Claims) (*core.Record, error)

// FirstSeenFunc is called once per platform session (sid), on the first request this app sees
// from it: a new sign-in. It replaces PocketBase's OAuth2 sign-in event.
type FirstSeenFunc func(e *core.RequestEvent, user *core.Record, c *iimakit.Claims)

type Config struct {
	AppID   string     // the registry id; also the token's aud. Required.
	AppName string     // shown in the PocketBase dashboard and emails
	Access  AccessFunc // required

	// Collection is the auth collection Access returns records from. Default "users".
	Collection string
	// PublicDir is the built frontend (index.html plus assets/), served at "/" with SPA fallback
	// and the cache rules from iimakit.SPA. New adds a --publicDir flag for it, defaulting to this
	// value, or to pb_public next to the binary (./pb_public under `go run`).
	PublicDir string
	// NoSPA serves no frontend (API-only apps).
	NoSPA       bool
	OnFirstSeen FirstSeenFunc

	// KeepAuthMethods leaves OAuth2, password and OTP sign-in on the collection as configured.
	// By default they are turned off: the platform is the only way in.
	KeepAuthMethods bool
	// RemoteDashboard allows the /_/ dashboard and superuser API from addresses other than
	// localhost. By default admins reach them through an SSH tunnel only.
	RemoteDashboard bool
	// NoEnvConfig skips applying APP_URL, TRUSTED_PROXY_HEADERS and BACKUP_* on start.
	NoEnvConfig bool

	// Verifier overrides how tokens are checked (tests). Default iimakit.NewVerifier(AppID).
	Verifier *iimakit.Verifier
}

const claimsKey = "iimakit.claims"

// New returns a PocketBase app wired to the platform. It also makes `serve` listen on
// iimakit.Addr() (Nomad's port, or PORT) unless --http is given.
func New(cfg Config) *pocketbase.PocketBase {
	app := pocketbase.New()
	if cfg.PublicDir == "" {
		cfg.PublicDir = defaultPublicDir()
	}
	app.RootCmd.PersistentFlags().StringVar(&cfg.PublicDir, "publicDir", cfg.PublicDir, "the directory with the built frontend")
	_ = app.RootCmd.ParseFlags(os.Args[1:])
	if err := register(app, &cfg); err != nil {
		panic(err) // a programming error in Config
	}
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" && !slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--http") }) {
		args = append(slices.Clone(args), "--http="+iimakit.Addr())
	}
	app.RootCmd.SetArgs(args)
	return app
}

// Register adds the platform hooks to an existing app. New calls it; tests call it directly.
func Register(app core.App, cfg Config) error {
	return register(app, &cfg)
}

// defaultPublicDir mirrors what the apps did by hand: pb_public beside the built binary.
func defaultPublicDir() string {
	if osutils.IsProbablyGoRun() {
		return "./pb_public"
	}
	return filepath.Join(filepath.Dir(os.Args[0]), "pb_public")
}

// register keeps a pointer to cfg so flags parsed after New (like --publicDir) are seen at serve time.
func register(app core.App, cfg *Config) error {
	if cfg.AppID == "" || cfg.Access == nil {
		return errors.New("pb: Config needs AppID and Access")
	}
	if cfg.Collection == "" {
		cfg.Collection = "users"
	}
	if cfg.Verifier == nil {
		cfg.Verifier = iimakit.NewVerifier(cfg.AppID)
	}
	seen := &seenSessions{}

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Id: "iimakitServe",
		Func: func(se *core.ServeEvent) error {
			if !cfg.NoEnvConfig {
				if err := applyEnvConfig(se.App, cfg.AppName); err != nil {
					return fmt.Errorf("pb: env config: %w", err)
				}
			}
			if !cfg.KeepAuthMethods {
				if err := disableAuthMethods(se.App, cfg.Collection); err != nil {
					return fmt.Errorf("pb: turn off auth methods: %w", err)
				}
			}
			if cfg.OnFirstSeen != nil {
				if err := seen.init(se.App); err != nil {
					return err
				}
			}
			if !cfg.RemoteDashboard {
				se.Router.Bind(&hook.Handler[*core.RequestEvent]{
					Id:       "iimakitLocalDashboard",
					Priority: apis.DefaultLoadAuthTokenMiddlewarePriority - 1,
					Func:     localDashboardOnly,
				})
			}
			se.Router.Bind(&hook.Handler[*core.RequestEvent]{
				Id:       "iimakitSSO",
				Priority: apis.DefaultLoadAuthTokenMiddlewarePriority + 1, // after PocketBase's own token check
				Func:     ssoMiddleware(*cfg, seen),
			})
			if !cfg.NoSPA && cfg.PublicDir != "" && !se.Router.HasRoute(http.MethodGet, "/{path...}") {
				se.Router.GET("/{path...}", apis.WrapStdHandler(iimakit.SPA(os.DirFS(cfg.PublicDir))))
			}
			return se.Next()
		},
		Priority: 999, // after the app's own OnServe hooks, so app routes win
	})
	return nil
}

// Claims returns the platform user for this request, or nil. It is set even when Access returned
// no record, so the app can say "signed in as X, but not on the list".
func Claims(e *core.RequestEvent) *iimakit.Claims {
	c, _ := e.Get(claimsKey).(*iimakit.Claims)
	return c
}

// HasRole reports whether the platform gives the user this role in this app.
func HasRole(e *core.RequestEvent, role string) bool { return Claims(e).HasRole(role) }

func ssoMiddleware(cfg Config, seen *seenSessions) func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		if e.Auth != nil {
			// Already authenticated by PocketBase itself: a superuser on the dashboard via the tunnel.
			return e.Next()
		}
		var c *iimakit.Claims
		if raw := e.Request.Header.Get(iimakit.TokenHeader); raw != "" {
			var err error
			if c, err = cfg.Verifier.Verify(e.Request.Context(), raw); err != nil {
				e.App.Logger().Warn("identity token rejected", slog.String("err", err.Error()))
				return e.Next()
			}
		} else if c = iimakit.DevClaims(cfg.AppID); c == nil {
			return e.Next()
		}
		// The session rides in a cookie, so a page on another site could make the browser send a
		// POST. Such requests run as signed out.
		if iimakit.Unsafe(e.Request.Method) && !iimakit.SameOrigin(e.Request) {
			e.App.Logger().Warn("cross-origin request treated as signed out", slog.String("path", e.Request.URL.Path))
			return e.Next()
		}
		e.Set(claimsKey, c)

		rec, err := cfg.Access(e, c)
		if err != nil {
			return err
		}
		if rec == nil {
			return e.Next()
		}
		if rec.Collection().Name != cfg.Collection {
			return fmt.Errorf("pb: Access returned a %q record, want %q", rec.Collection().Name, cfg.Collection)
		}
		if err := mirrorRoles(e.App, rec, c.Roles); err != nil {
			return err
		}
		e.Auth = rec
		if cfg.OnFirstSeen != nil && seen.first(e.App, c.Session) {
			cfg.OnFirstSeen(e, rec, c)
		}
		return e.Next()
	}
}

// mirrorRoles copies the token's roles into a "roles" field, if the collection has one, so API
// rules like @request.auth.roles ~ "volunteer" keep working. The field is a cache of the platform's
// roles, never edited in the app. Saves only on change.
func mirrorRoles(app core.App, rec *core.Record, roles []string) error {
	if rec.Collection().Fields.GetByName("roles") == nil {
		return nil
	}
	want := slices.Clone(roles)
	slices.Sort(want)
	have := slices.Clone(rec.GetStringSlice("roles"))
	slices.Sort(have)
	if slices.Equal(want, have) {
		return nil
	}
	if want == nil {
		want = []string{}
	}
	rec.Set("roles", want)
	return app.Save(rec)
}

// localDashboardOnly hides the dashboard and the superuser API from anything but an SSH tunnel.
// The gateway blocks these paths too; this also covers someone reaching the app's port directly.
func localDashboardOnly(e *core.RequestEvent) error {
	p := e.Request.URL.Path
	if (p == "/_" || strings.HasPrefix(p, "/_/") || strings.HasPrefix(p, "/api/collections/_superusers/")) && !fromTunnel(e.Request) {
		return e.NotFoundError("", nil)
	}
	return e.Next()
}

// fromTunnel reports a request made on this machine directly to the app's port. The TCP peer must
// be loopback, and there must be no X-Forwarded-For: when the gateway runs on the same machine it
// also connects from loopback, but it always adds that header (and browsers cannot remove it).
func fromTunnel(r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func disableAuthMethods(app core.App, collection string) error {
	c, err := app.FindCollectionByNameOrId(collection)
	if err != nil {
		return err
	}
	if !c.OAuth2.Enabled && !c.PasswordAuth.Enabled && !c.OTP.Enabled && !c.MFA.Enabled {
		return nil
	}
	c.OAuth2.Enabled, c.PasswordAuth.Enabled, c.OTP.Enabled, c.MFA.Enabled = false, false, false, false
	return app.Save(c)
}

// applyEnvConfig copies deployment settings from the environment into PocketBase on every start,
// so an app is configured by its Nomad job alone. Unset variables leave the dashboard's values.
//
//	APP_URL                public URL, e.g. https://students.iima.ac.in/auxesiscapital
//	TRUSTED_PROXY_HEADERS  default X-Forwarded-For (the gateway appends the client IP on the right)
//	BACKUP_CRON            e.g. "0 3 * * *"; BACKUP_KEEP (default 7)
//	BACKUP_S3_ENDPOINT, BACKUP_S3_BUCKET, BACKUP_S3_REGION, BACKUP_S3_ACCESS_KEY,
//	BACKUP_S3_SECRET, BACKUP_S3_FORCE_PATH_STYLE   off-box backup storage
func applyEnvConfig(app core.App, appName string) error {
	env := os.Getenv
	s := app.Settings()
	before, err := s.MarshalJSON()
	if err != nil {
		return err
	}

	if v := env("APP_URL"); v != "" {
		s.Meta.AppURL = strings.TrimRight(v, "/")
	}
	if appName != "" {
		s.Meta.AppName = appName
	}
	headers := env("TRUSTED_PROXY_HEADERS")
	if headers == "" {
		headers = "X-Forwarded-For"
	}
	s.TrustedProxy.Headers = strings.Split(headers, ",")
	s.TrustedProxy.UseLeftmostIP = false

	if v := env("BACKUP_CRON"); v != "" {
		s.Backups.Cron = v
		s.Backups.CronMaxKeep = 7
		if n, err := strconv.Atoi(env("BACKUP_KEEP")); err == nil && n > 0 {
			s.Backups.CronMaxKeep = n
		}
	}
	if v := env("BACKUP_S3_BUCKET"); v != "" {
		s.Backups.S3 = core.S3Config{
			Enabled: true, Bucket: v, Endpoint: env("BACKUP_S3_ENDPOINT"), Region: env("BACKUP_S3_REGION"),
			AccessKey: env("BACKUP_S3_ACCESS_KEY"), Secret: env("BACKUP_S3_SECRET"),
			ForcePathStyle: env("BACKUP_S3_FORCE_PATH_STYLE") == "true",
		}
	}

	after, err := s.MarshalJSON()
	if err != nil {
		return err
	}
	if string(before) == string(after) {
		return nil
	}
	return app.Save(s)
}

// seenSessions remembers which platform sessions this app has seen, in a small table so restarts
// do not count everyone as a new sign-in. Rows older than 90 days are pruned on start.
type seenSessions struct {
	mem sync.Map // sid -> struct{}
}

const seenTable = "iimakit_seen_sessions"

func (s *seenSessions) init(app core.App) error {
	if _, err := app.NonconcurrentDB().NewQuery("CREATE TABLE IF NOT EXISTS " + seenTable + " (sid TEXT PRIMARY KEY, seen INTEGER NOT NULL)").Execute(); err != nil {
		return fmt.Errorf("pb: create %s: %w", seenTable, err)
	}
	cutoff := time.Now().Add(-90 * 24 * time.Hour).Unix()
	_, err := app.NonconcurrentDB().NewQuery("DELETE FROM " + seenTable + " WHERE seen < {:cutoff}").Bind(dbx.Params{"cutoff": cutoff}).Execute()
	return err
}

// first reports whether this is the first time sid is seen. Only one caller ever gets true.
func (s *seenSessions) first(app core.App, sid string) bool {
	if sid == "" {
		return false
	}
	if _, ok := s.mem.Load(sid); ok {
		return false
	}
	res, err := app.NonconcurrentDB().NewQuery("INSERT OR IGNORE INTO " + seenTable + " (sid, seen) VALUES ({:sid}, {:seen})").
		Bind(dbx.Params{"sid": sid, "seen": time.Now().Unix()}).Execute()
	if err != nil {
		app.Logger().Warn("first-seen check", slog.String("err", err.Error()))
		return false
	}
	s.mem.Store(sid, struct{}{})
	n, _ := res.RowsAffected()
	return n == 1
}
