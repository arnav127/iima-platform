# Roadmap

Each phase ends with something working end to end. Tick items as they land. `ARCHITECTURE.md` explains the
why. Run everything alongside today's Apache setup until an app is cut over; nothing live changes until then.

## Phase 0: repo and local dev loop
- [x] Monorepo layout: `core/`, `kit/` (Go module, with `kit/pb`), `kit-js/`, `apps/`, `deploy/traefik/`, `deploy/nomad/`, `docs/`
- [x] Local stack that runs on Windows: Traefik (file provider, no Nomad) + `core` + one app, on `http://localhost:8080/<app>`
- [x] One command to start it: `go run ./dev/cmd/devstack`
- [x] `apps/` registry with `app.toml` for auxesiscapital, beta-app, garba2026, betamarkets, portfolioleague
- [x] CI: `go test ./...`, `go vet`, `govulncheck` for `core` and `kit` (written; first run once the repo is on GitHub)

## Phase 1: core auth
- [x] Google OIDC login, `/auth/login?rd=`, `/auth/callback`, `/auth/logout`; `rd` limited to same-origin paths
- [x] `hd=iima.ac.in` hint plus server-side `hd` claim check; per-app `login = "iima" | "external"`
- [x] Server-side sessions (SQLite to start), HttpOnly Secure SameSite=Lax cookie
- [x] App lookup from `X-Forwarded-Uri` against the registry
- [x] `/auth/verify?mode=required|optional` minting Ed25519 JWTs (claims `v: 1`, per-app `aud`, short expiry, `kid`)
- [x] `/auth/.well-known/jwks.json`, `/auth/me`
- [x] "Needs your @iima.ac.in account" page with Switch account
- [x] Dev mode with a fake user (dev sign-in form in core, localhost only)
- [x] Traefik middlewares: `auth-required`, `auth-optional`, `auth-none`, header stripping, reserved paths, `/_/` block
- [x] Session cookie stripping before apps (written: `deploy/traefik/plugins/platformstrip`)
- [x] Tests: token audience, expiry, spoofed headers, wrong-domain user, open redirect
- [ ] Sign in once with a real Google account (needs an OAuth client in `.env`)

## Phase 2: kits and the first app
- [x] `iimakit`: Verify, Middleware, User, RequireUser, RequireRole, Addr, health, logging, SPA handler, dev mode, `Prefix`/`SameOrigin`
- [x] `iimakit/pb`: `pb.New`, SSO middleware calling `Access`, OAuth off, roles mirror, SPA serving, env config, `/_/` and superuser API tunnel-only, backups, `OnFirstSeen`
- [x] `@iima/kit`: generic parts of `lib.ts`, `login()`, `logout()`, `me()`
- [x] Gateway strips the app path for non-Next apps (`strip_path` in `app.toml`)
- [x] Port **Auxesis** on a branch (`platform-migration` in `../auxesiscapital`): drop its OAuth, `applyEnvConfig` provider setup and `/auth/callback`; `SignIn` becomes `Access`; sign-in visits from `OnFirstSeen`; frontend drops `pb.authStore`
- [x] Auxesis works end to end on the local stack: `go run ./dev/cmd/devstack -app auxesiscapital=../auxesiscapital`
- [ ] Publish the kits as versions (tag `kit/v0.1.0`, `kit/pb/v0.1.0`) so apps drop their `replace` and `file:` paths

## Phase 3: roles and attributes
- [x] Attributes from email patterns (Garba's cohort logic), with overrides; `year` derived from batch
- [x] Super admins (CLI bootstrap: `core superadmin add`), app admins, per-app roles declared in `app.toml`
- [x] `/admin` UI and `PUT /auth/api/apps/{app}/roles/{email}`; audit log
- [x] Role cache (~30 s); roles and attributes in the token and in `/auth/me?app=`
- [x] Auxesis drops `ADMIN_EMAILS` (on its `platform-migration` branch)
- [ ] Before Auxesis cuts over: grant today's `ADMIN_EMAILS` the `admin` role on the platform

## Phase 4: the cluster
- [ ] Nomad: 3 servers (also clients) + 1 client, ACLs, `exec` driver, Node and Python runtimes on every client
- [ ] Traefik on the entry node with the Nomad provider, HTTP/2 and HTTP/3, TLS
- [ ] Host volumes for app data; scheduled Nomad snapshots; off-box backups
- [ ] Nomad jobs for `core` and Auxesis; firewall so app ports are cluster-only
- [ ] Cut over `/auxesiscapital` from Apache to Traefik; keep a rollback path

## Phase 5: the other Go + PocketBase apps
- [ ] Port **beta-app** (roles member, writer, mentor, admin become platform roles)
- [ ] Port **Garba** (external login with any Google account; Microsoft login added only if exchange students need it; cohorts move to attributes; volunteers become roles; guest limits stay in the app)
- [ ] Extract the app template from Auxesis

## Phase 6: Next.js and Python apps
- [ ] `@iima/kit/next` (`getUser()` with `jose`); Python helper for FastAPI
- [ ] Decide the standalone PocketBase approach (see ARCHITECTURE.md)
- [ ] Port **BetaMarkets**; close the public `/betamarkets/pb` route
- [ ] Port **Portfolio League** (confirm Supabase is gone first)
- [ ] Next.js app template

## Phase 7: shared services
- [ ] Postgres (shared, for core and Inngest); move core to Postgres with 2 instances
- [ ] Self-hosted Inngest (internal only, namespaced events, dashboard for super admins)
- [ ] Market data service (union of symbols, NSE calendar, corporate actions, `market/close.ready`)
- [ ] Move Portfolio League's polling and close pipeline onto it (idempotent steps, concurrency key on date)
- [ ] `iimakit.Call` over Nomad discovery, with service tokens

## Phase 8: deploy portal
- [ ] GitHub App; reusable GitHub Actions build workflow publishing release artifacts
- [ ] Portal in `core`: connect repo, config form, Deploy, Rollback, Logs; Nomad job generation; Nomad Variables for secrets
- [ ] First deploy needs super-admin approval
- [ ] `iima` CLI

## Phase 9: operations
- [ ] Prometheus + Grafana, Loki, Uptime Kuma (alerts to owners, status page), GlitchTip, Umami
- [ ] Monthly restore drill; load test before events
- [ ] App lifecycle: owners, review dates, archive mode
- [ ] Maintainer handbook: add an app, rotate keys, restore a backup, hand over super admin

## Later, when two or more apps ask for it
Notifications (email, web push, Telegram), short links (`/go/...`), imgproxy, S3 storage (Garage or MinIO),
a multi-tenant events app generalised from Garba, Consul or a service mesh, HTTP/2 between services.
