# IIMA student apps platform: architecture

How student and club apps at `students.iima.ac.in` share one gateway, one login and one set of
platform services, while each app stays independent. This records what was decided and why, and what was
deliberately left out.

## Goals and constraints

- **One login for every app.** Google with an @iima.ac.in account by default. A few apps also let outsiders in.
- **Subpaths only** (`students.iima.ac.in/<app>`). Subdomains are not available.
- **3 to 4 servers.** Apps today are started by hand (PM2), with a manual Apache block per subpath.
- **New apps keep arriving**, written by students and clubs. The platform should handle 50 to 60 apps.
- **Maintainers change every year.** Every moving part has to be simple enough to hand over.
- App authors should **never SSH into a server** to deploy.

## The shape in one picture

```
browser ──HTTPS (HTTP/2, HTTP/3)──▶ Traefik (gateway)
                                       │ 1. forward auth: "who is this?"
                                       ├──────────────▶ core  /auth/verify  (session cookie → signed JWT)
                                       │ 2. request + X-User-Token, minus cookie and spoofed headers
                                       ▼
                         app (any server, any port, picked by Nomad)
                                       │ verifies JWT locally with the public key
                                       ├── its own PocketBase / data folder
                                       └── iimakit.Call("marketdata", "GET /v1/prices") ─▶ other services

Nomad: runs every app and service, restarts them, holds the service catalogue Traefik reads.
```

Rule of thumb used throughout:
- Same **code** in many apps → a **library** (`iimakit`, `@iima/kit`, the app template).
- Same **data** needed by many apps → a **service** (identity and roles, market data, analytics).
- Everything else stays **inside the app** (a modular monolith).

## Existing apps (in `C:\sandbox`)

| App | Path | Stack today | Login today | Notes |
| --- | --- | --- | --- | --- |
| `auxesiscapital` | `/auxesiscapital` | Go + PocketBase v0.40 as a framework, Preact/Vite SPA, PM2, port 8091 | PocketBase Google OAuth; investor/guest/admin list | Reference app. First to migrate. |
| `beta-app` | check its `deploy/` | Same as Auxesis (Go + PB v0.40, Preact/Vite) | PB Google OAuth, @iima.ac.in only; roles member, writer, mentor, admin (`ADMIN_EMAILS`) | Content from files in `content/`, Yahoo index data cached 1 min |
| `predictionmarkets` (BetaMarkets) | `/betamarkets` | Next.js 16 (`basePath: /betamarkets`, port 3010, 2 PM2 instances) + **standalone PocketBase binary** (port 8091, JS migrations in `pb_migrations`) | Browser talks to PocketBase directly (`NEXT_PUBLIC_POCKETBASE_URL`, exposed at `/betamarkets/pb`) | Web push. PB currently reachable from outside. |
| `papertrading` (Portfolio League) | `/portfolioleague` | Next.js (`web/`, port 3000) + **FastAPI** (`backend/`, Python, port 8000) + **standalone PocketBase** (port 8090) + **Inngest (Python)** | On PocketBase (`pb_client.py`); Supabase is only left in its README and old SQL files | Price polling, order matching at close, dividends, splits, league rotation in `inngest_functions.py` |
| `iima-garba` | `/garba2026` | Go + PB v0.40, Preact/Vite, port 8090 | PB Google + Microsoft OAuth; IIMA members, guests, exchange students | Needs external login. Rotating QR passes. |

Port clashes today (8090 and 8091 are each used twice) disappear once Nomad assigns ports.

So the platform must support **four app shapes**, not one:
1. Go + PocketBase as a framework (Auxesis, beta-app, Garba). The default for new apps.
2. Next.js (BetaMarkets frontend, Portfolio League frontend).
3. Python API (Portfolio League backend).
4. A standalone PocketBase binary with no Go code (BetaMarkets, Portfolio League).

## Gateway: Traefik

- Single front door for `students.iima.ac.in`, reading routes from **Nomad's service catalogue** (Traefik's Nomad provider). No hand-edited proxy config per app.
- Serves **HTTP/2 and HTTP/3 to browsers**. This matters on one origin: HTTP/1.1 allows only 6 connections per origin, and a few tabs of realtime (SSE) apps can use them all up.
- Internal hops stay HTTP/1.1 with keep-alive pools. h2c can be turned on later if metrics ever show a need.
- **Platform middlewares**, defined once in Traefik's file config:
  - `auth-required`, `auth-optional`: forward auth to `core` `/auth/verify?mode=...`, copying `X-User-Token` back onto the request.
  - `auth-none`: no auth call; strips any incoming `X-User-Token`.
  - security headers and a default CSP on every response; compression; per-app rate limits, body size limits and timeouts.
- **Always strip** incoming `X-User-Token`, `X-User-*` and `Authorization` from browsers. Only the gateway sets identity.
- **Strip the platform session cookie** before forwarding to apps. Traefik has no built-in way to drop a single cookie or headers by prefix, so a small local plugin does both: `deploy/traefik/plugins/platformstrip` (standard library only, runs in Traefik's Yaegi interpreter).
- Entry points use `aliasHeadersStrategy: delete`, so `X_User_Token` and similar spellings cannot reach backends that would read them as `X-User-Token`.
- **Reserved paths** no app may claim: `/auth`, `/platform`, `/admin`, `/` (the core app). Routes come from the approved manifest via the deploy portal, never freely from app tags.
- **Block `/<app>/_/`** (the PocketBase dashboard) for every app. Admins use an SSH tunnel.
- Realtime (SSE) routes such as `/<app>/api/realtime` get long timeouts and no buffering, generated automatically for PocketBase apps.

## Orchestration: Nomad from day one

- **3 servers run Nomad in server mode** (a quorum that survives one failure) and also run apps. The 4th is client-only.
- **No Consul at first.** Nomad's built-in service discovery and health checks cover routing and `iimakit.Call`. Add Consul later only for DNS lookups, services outside Nomad, KV and locks, or a service mesh.
- **Nomad ACLs on from the start.** Only the deploy portal and super admins can submit jobs.
- **Drivers:** `exec` for Go binaries. Node and Python apps use `exec`/`raw_exec` with the runtime installed on every client (Node 22, Python with per-app venv built in CI).
- **Dynamic ports.** Apps listen on `NOMAD_IP_http:NOMAD_PORT_http`. The kits read these automatically.
- **PocketBase data lives on a host volume** outside the job's directory, and the job is pinned to that node, so a redeploy can never start an app on an empty database. **Dynamic host volumes** (`nomad volume create`), so a new app needs no change to a node's config.
- Secrets in **Nomad Variables** (encrypted, per job), injected as env. Config files in Git encrypted with **sops**.
- Snapshot Nomad's own state on a schedule (`nomad operator snapshot save`).
- **Jobs are generated from the manifest** (`core/jobgen`), never written by app authors: the Traefik tags (routes, middlewares, path strip) come from the reviewed `app.toml`, which together with Nomad ACLs keeps an app from claiming another's path.
- **Deploys:** one instance per SQLite app, so a deploy stops the old copy before starting the new. A release that fails its health check reverts by itself (`restart` mode `fail`, `auto_revert`), after about the 3-minute progress deadline; that is the cost of a broken release. Stateless apps (Next.js) can use canaries later.
- **Traefik runs under systemd on the entry node,** not as a Nomad job, so the front door does not depend on the scheduler. It reaches core through an `internal` entry point (127.0.0.1:8081) that core's job routes on to wherever Nomad placed it; forward auth uses the same address, so core can move or (phase 7) run twice without touching Traefik's config.

## The core app (`core`)

One Go service that owns everything platform-wide. It runs behind Traefik like any app.

| Path | What |
| --- | --- |
| `/` | Home page and app directory (built from the registry) |
| `/auth/login`, `/auth/callback`, `/auth/logout` | Sign-in with Google (Microsoft later, for external apps) |
| `/auth/verify` | Forward-auth endpoint called by Traefik on every request |
| `/auth/me` | Current user as JSON, for frontends |
| `/auth/.well-known/jwks.json` | Public keys for verifying tokens |
| `/auth/api/...` | Role management API (app admins), user lookup for services |
| `/platform/...` | Shared navbar web component (`<iima-nav>`), CSS, icons |
| `/admin` | Super admin and app admin UI: apps, roles, attributes, audit log |
| catch-all | Platform 404 |

`/auth/verify` is on the path of every authenticated request. Keep it the most boring, best-tested code on
the platform. Phase 1 runs one instance on SQLite. Later it moves to Postgres with two instances on
different servers (Postgres is also needed by self-hosted Inngest).

### Sign-in

- **Google OIDC.** For IIMA-only apps, send `hd=iima.ac.in` (so Google shows only IIMA accounts) and **check the `hd` claim in the ID token server-side**. The URL parameter is only a hint.
- **External login is opt-in per app** (`login = "external"` in its manifest). The login page then also accepts any Google account (no `hd` hint, no `hd` check).
- **Microsoft login is left out for now.** When it is added, follow Garba's tenant rule: `organizations` or one named tenant, never `common` or `consumers`. Until then, exchange students sign in to external apps with a Google account.
- **One session for everyone**, in an HttpOnly, Secure, `SameSite=Lax` cookie on path `/`. Sessions are stored server-side so logout and revocation work instantly. They last 30 days and **slide**: `/auth/me` extends one that is past half its life, so people who open an app now and then (Beta's installed app checks daily) stay signed in.
- **Tokens are only issued to apps whose policy the user fits.** A Gmail user visiting an IIMA-only app looks signed out there and sees "This app needs your @iima.ac.in account" with a **Switch account** button (`prompt=select_account`).
- Login links: `/auth/login?rd=/auxesiscapital/portfolio`. `rd` must be a same-origin path.

### `/auth/verify`

- Traefik calls it with the original request's headers (`X-Forwarded-Uri`, `X-Forwarded-Method`, cookies).
- It works out the app from the path prefix against the registry, then:
  - `mode=required`: 401 if not signed in or not allowed. Traefik sends browsers to the login page; API calls get 401.
  - `mode=optional`: always 200. Adds `X-User-Token` only if there is a valid session the app's policy allows.
- **Recommended default: every app on `auth-optional`**, with checks in the app's handlers (`RequireUser`, `RequireRole`, PocketBase API rules). Use `auth-required` at the gateway only for fully private apps, as a safety net. Use `auth-none` for webhooks and health checks.
- Fast path: sessions and role lookups cached in memory (about 30 seconds), so a role change applies within seconds.

### The identity token (`X-User-Token`)

- **Ed25519-signed JWT** (`EdDSA`), minted per request, short-lived (a few minutes), with `kid` for rotation.
- Apps verify with the **public key** from JWKS. A leaked public key can't forge anything, so there are no per-app secrets to protect.
- Claims, versioned as `v: 1`:

```json
{
  "v": 1, "iss": "students.iima.ac.in", "aud": "auxesiscapital",
  "sub": "u_8f3k...", "sid": "s_19ab...", "iat": 0, "exp": 0,
  "email": "p25arnavd@iima.ac.in", "name": "Arnav Dixit", "picture": "https://lh3.googleusercontent.com/...",
  "provider": "google", "member": true,
  "affiliation": "student", "programme": "PGP", "batch": 2025, "year": 2,
  "roles": ["admin"]
}
```

- `aud` is the app id, so a token minted for one app is rejected by every other app.
- `roles` holds **only this app's roles**.
- `sid` lets apps notice a new sign-in (for example, Auxesis's sign-in log).

### Roles and attributes

Three tiers:
- **Super admins** (2 or 3 people, so nobody gets locked out): create apps, appoint app admins. Bootstrapped by a CLI command.
- **App admins**: manage roles for their own app only.
- **Users.**

What lives where:
- **Common attributes are facts about a person, not grants.** `affiliation` (student, faculty, staff, alum, external), `programme` (PGP, PGPX, PhD, ePGP...), `batch` (a year). `year` (PGP1 or PGP2) is **derived** from batch and the academic year when the token is minted, so nobody updates 400 people every June. Defaults come from email patterns (moving Garba's `p26`/`f26` cohort logic here), with super-admin overrides and a roll-list import later.
- **How the email is read** (`core/attrs`): a roll-number address is up to 10 letters then two digits (`p25aarav`, `phd23neha`, `bpgpbaai26x`). The letters give the programme (`p` PGP and `f` PGP-FABM, two years; `x` PGPX and `afp` AFP, one year; `phd` PhD; others shown in capitals), the digits the batch (year of entry, so `x26` is the 2026-27 batch). Year 1 starts in June. A batch past its programme length becomes `alum`. Any other @iima.ac.in address is `faculty`: the address cannot tell faculty from staff, so overrides do. Non-members are `external`.
- **Per-app roles** are declared by the app in its manifest (`roles = ["admin", "volunteer"]`), stored centrally, and edited in `/admin` or by the app through `PUT /auth/api/apps/{app}/roles/{email}` (so Garba's People tab keeps its UI). Roles, app admins and super admins are keyed by email, so they can be granted before someone's first sign-in.
- **App admin is not an app role.** Being an app admin means "may manage this app's roles". An app's own `admin` role (Auxesis's fund admins) is just one of its declared roles.
- **The API** (same-origin only; every change audited): `GET /auth/api/apps`; `GET /auth/api/apps/{app}/roles`; `PUT|DELETE /auth/api/apps/{app}/roles/{email}` (app admins); `GET /auth/api/apps/{app}/admins`; `PUT|DELETE /auth/api/apps/{app}/admins/{email}`, `GET|PUT /auth/api/people/{email}` (super admins); `GET /auth/api/audit[?app=]`.
- **App data that only looks like a role stays in the app.** Auxesis investors (from its ledger), Garba guest limits and per-person overrides, exchange guests. Test: if granting it needs domain knowledge, it is app data. If it is "let this person do X", it is a role.
- **Every role change goes into an audit log** (who, what, when).
- PocketBase apps **mirror** the token's roles into a `roles` field on their local `users` record (saving only on change), so API rules like `@request.auth.roles ~ "volunteer"` keep working. The mirror is a cache, never edited locally.

## The app manifest (`app.toml`)

One small file per app, at the root of the app's repo. The deploy portal reads it when the repo is
connected and fills its settings form from it; after that the approved settings live in core (see Deploy
portal). Apps set up by hand keep theirs in this repo's `apps/` folder.

```toml
id      = "auxesiscapital"          # also the JWT aud and the Nomad job name
name    = "Auxesis Capital"
path    = "/auxesiscapital"
owners  = ["arnavdixit127@gmail.com"]   # who gets alerts; reviewed every term
review  = "2027-03-31"
shape   = "go-pocketbase"           # go-pocketbase | go | next | python | pocketbase
login   = "iima"                    # iima (default) | external
auth    = "optional"                # optional (default) | required
roles   = ["admin"]
public  = ["/api/aux/public", "/api/health"]   # optional: routes on auth-none
realtime = true                     # long-timeout route for /api/realtime
volume  = "auxesiscapital-data"     # host volume for pb_data
memory  = 256                       # MB
strip_path = true                   # default: true, except shape = "next"
manager_role = "admin"              # optional: holders of this role also manage the app's roles
```

**The home app.** One app may take `path = "/"`: it gets every request that no other app and none
of the platform's paths claim, so the site's front page (mess menu, events...) can be an app like any
other. Its routes sit just above core's catch-all and below every other app, its public paths included,
and its path is never stripped. Core's app directory then lives at `/platform/apps`.

**Path stripping.** The gateway removes the app's path before forwarding (as Apache does today), so
a Go, PocketBase or Python app serves at `/` and learns its public prefix from `X-Forwarded-Prefix`
(`iimakit.Prefix`). Next.js apps keep the full path, because their `basePath` expects it. The strip
runs after forward auth, so core always sees the full path.

**External routes** (shape `external`). Apps not on the platform yet keep running where they are, and
a super admin points their path at a host and port in `/admin/routes`, the way Apache's `ProxyPass`
lines did. So the whole site can move to the gateway before every app is ported.
- `/path/x` goes to `<upstream>/x`, with the upstream's own path kept in front. The public Host is
  passed on by default (`ProxyPreserveHost`). Redirects, `Content-Location` and cookie paths that point
  back at the upstream are rewritten to the public path by a small gateway plugin
  (`deploy/traefik/plugins/proxyreverse`, Apache's `ProxyPassReverse` and `ProxyPassReverseCookiePath`).
- Sign-in at the gateway is `none` (passed through, as Apache did), `optional` or `required`. Either
  way the platform's identity headers and session cookie never reach the upstream.
- Websockets pass through. Server-sent events stream as sent, and a `realtime` route also skips
  compression. The plugin steps aside for upgrades and `Accept: text/event-stream` requests, because a
  wrapper made in Traefik's interpreter would hide `Hijack` and `Flush`.
- Core serves the routes as Traefik configuration at `/platform/routes`, and Traefik polls it every
  5 seconds with its HTTP provider, through the loopback-only internal entry point. The public entry
  point blocks that path.
- When the app moves onto the platform with the same path, approving it replaces the route in one step.

## Kits (shared code, no shared state)

### `iimakit` (Go, no PocketBase dependency)
- `iimakit.NewVerifier(appID).Verify(ctx, token)`: accepts only EdDSA; checks signature (JWKS fetched once, cached, refreshed on unknown `kid`), expiry and `aud`.
- `iimakit.Middleware(appID)`, `iimakit.User(r)`, `RequireUser(h)` (redirects browsers to login, 401 for APIs), `RequireRole(role, h)`.
- `iimakit.Call(ctx, "marketdata", "GET /v1/prices?symbols=TCS,INFY", body, &out)`: service lookup in Nomad (cached), one pooled HTTP client, default timeout, retry on another instance, a service token identifying the caller, JSON decoding. The caller declares only the fields it needs.
- `iimakit.Addr()` from `NOMAD_IP_http`/`NOMAD_PORT_http`, `Health()`, `Serve()` with graceful shutdown, `SetupLogging()` (JSON), `Prefix(r)`/`OriginalURI(r)` for the stripped base path, `SPA(fs)` (`immutable` on hashed assets, `no-cache` on HTML and `sw.js`; compression is left to the gateway).
- `SameOrigin(r)`: a CSRF check for state-changing requests, now that identity rides in a cookie.
- **Dev mode**: `IIMA_DEV_USER=you@iima.ac.in` (and `IIMA_DEV_ROLES`) injects a fake signed-in user when no token is present, so authors can work without the platform. Ignored on the cluster (`NOMAD_ALLOC_ID` set), and never used to replace a rejected token.

### `iimakit/pb` (Go, wraps PocketBase)
Everything Auxesis and Garba each wrote by hand:
- `pb.New(pb.Config{AppID, Access})` returns the real `*pocketbase.PocketBase`, never hidden, so apps keep full access to hooks and routes. Every default can be turned off. `serve` listens on `iimakit.Addr()` unless `--http` is given.
- SSO middleware: verifies the token, calls the app's `Access(e, claims)` (for Auxesis, `SignIn`, which maps an email to admin, investor or guest), sets `e.Auth`. `Access` returning no record means "signed in, but not a user of this app": the request runs signed out and `pb.Claims(e)` still says who it is. Removing someone takes effect on their next request. Cross-origin state-changing requests run signed out.
- Turns off OAuth2, password, OTP and MFA on `users`; superusers still log in, but only through the tunnel.
- Mirrors roles into a `roles` field when the collection has one; `pb.HasRole(e, "admin")`.
- SPA serving from `--publicDir` (default `pb_public` next to the binary) with the cache rules; `APP_URL`, trusted proxy headers and app name from env (today's `applyEnvConfig`).
- `/_/` and the superuser API refused unless the request comes over an SSH tunnel: loopback peer and no `X-Forwarded-For` (a gateway on the same machine also connects from loopback, but always adds that header).
- Built-in backups to S3-compatible storage from env (`BACKUP_CRON`, `BACKUP_S3_*`).
- `OnFirstSeen(e, user, claims)` hook, once per platform session, replacing the OAuth sign-in event Auxesis uses for its visit log. Seen sessions are kept in a small table so restarts do not repeat it.
- A separate Go module (`kit/pb`), so `iimakit` never depends on PocketBase. Pinned per PocketBase minor version (both apps are on v0.40.4), because PocketBase is pre-1.0 and breaks APIs between minors.

### `@iima/kit` (npm)
- Everything generic in today's `web/src/lib.ts`: `BASE`, `asset()`, `api()`, `ApiError`, `createStore`, `storage`, the router (`appPath`, `navigate`), `me()` from `/auth/me`, `login(next)` and `logout()` redirects.
- TypeScript source with no build step and no framework dependency; stores expose `subscribe()`, and each app keeps its own few-line hook (Preact's `use()` in Auxesis). Until it is published, apps depend on it with `"@iima/kit": "file:../iima-platform/kit-js"`.
- `@iima/kit/next`: `getUser()` for server components and route handlers, verifying the token with `jose` (Ed25519 works in Next middleware).

### Python
A small helper (PyJWT with the JWKS) for FastAPI: `user = iima_user(request)`. Only Portfolio League needs it today.

### App template
In this repo, built and tested by CI so they never rot: `template/go-pocketbase` (lifted from Auxesis: Vite + Preact frontend, `iimakit/pb` backend), `template/next` (Next.js, PocketBase behind the server) and `template/python` (FastAPI with `kit-py`). A new app copies one. Next.js and Python apps get the platform's pinned PocketBase as a sidecar with `pocketbase = true`, and a Next.js app can have a Python (ASGI) backend beside it with `backend = "main:app"`: a loopback sidecar the Next server reaches at `BACKEND_URL`, which checks the forwarded `X-User-Token` itself (Portfolio League's FastAPI).

### Apps' own role screens
Beta and Garba manage roles from their own People screens. `manager_role` in `app.toml` lets the holders of that app role manage the app's roles on the platform (like app admins, for that app only). The screen calls `@iima/kit`'s `setRoles` (the platform API, with the person's own session), then saves the app's copy as before; the app's `role` field is a mirror that `Access` overwrites from the token on every request.

### Frontends under the gateway's CSP
No inline scripts: the default policy runs scripts from files on this site only (Beta's theme snippet moved to `theme.js`). An app that needs more (a font CDN, an analytics host) gets a per-app policy when the first one asks.

Next.js puts inline scripts in every page, so `shape = "next"` apps get the `-next` middleware chains, which
leave `Content-Security-Policy` to the app. The app sets the same policy plus a fresh nonce on each page
request in its `proxy.ts` (`contentSecurityPolicy()` from `@iima/kit/next`, tested against the gateway's), and
its pages render per request. Review checks that a Next app has this proxy.

## Apps on a standalone PocketBase binary

BetaMarkets and Portfolio League run PocketBase as a separate process, with no Go code to put middleware in.
**Decided (phase 6): PocketBase sits behind the app server.**
- PocketBase gets **no public route**. It listens on loopback only, inside the app's job; the browser never
  talks to it, and its dashboard is reached only over an SSH tunnel.
- The app server (Next.js route handlers and server components, or FastAPI) checks the platform token with
  `@iima/kit/next` or `kit-py`, and finds or creates the person's `users` record by email.
- To act **as the user**, the server asks PocketBase for a short-lived token for that record with the
  superuser-only `impersonate` API, so the collections' API rules keep applying. Server code that already
  runs as a superuser (BetaMarkets) takes the user's id from the verified token, never from the request.
- The superuser's email and password come from the app's Nomad Variable; there are no defaults in code.
- Realtime from PocketBase to the browser is gone with the public route: pages poll an app route instead
  (an SSE route in the app server can come later if one needs it).
- Roles come from the token (`admin` is a platform role), mirrored into the record's `role` field like the
  Go apps do.
- Ruled out: a pb_hooks bridge with HS256 tokens from core (a second token type and a shared secret to
  guard), and rewriting both apps in Go (most of their code is Next.js and Python; a rewrite is far bigger
  than the port).

## Data and files

- **Every app keeps its own PocketBase** (database, files, realtime). No shared PocketBase: SQLite has one writer, apps compile PocketBase in, and shared instances would couple deploys, backups and failures. Garba's "20 parallel scans let in exactly once" depends on one local SQLite transaction.
- Apps own their files and the access rules for them. If disk or mobility becomes a problem, add self-hosted S3 (Garage or MinIO) as **infrastructure**: each app gets its own bucket, and PocketBase points at it natively.
- Cross-app data access only through HTTP APIs, never shared tables.
- Next.js and Python apps bring their own storage, so backups for them are set up per app (or on a platform-run Postgres).

## Internal calls

- **Plain HTTP + JSON** through `iimakit.Call`. No gRPC, no JSON-RPC, no shared client packages, no codegen. An internal call costs well under a millisecond on the LAN; the time goes to handler work and SQLite.
- What keeps it safe: **versioned paths** (`/v1/...`, add `/v2` for breaking changes and keep `/v1` alive for a while); **add fields, never rename or remove** within a version; timeouts and error checks on every call (the kit sets defaults).
- Each service documents its endpoints in its README. OpenAPI (Huma) and an API catalogue page can be added later if callers start breaking.
- Service-to-service calls carry a service token so the callee knows who is calling.

## Background jobs: Inngest

- **One self-hosted Inngest server** for every app that needs durable, multi-step workflows with retries. Needs Postgres and Redis.
- Internal only. Apps send events to it at its internal address; Inngest calls each app's `/api/inngest` **directly on the app's internal address**, not through Traefik (there is no user session, so forward auth would block it). Apps verify Inngest's signature on every call.
- **Namespaced events** per app (`portfolioleague/close.done`, `garba/pass.issued`). Check how the Inngest version in use isolates apps and signing keys.
- Dashboard exposed to super admins only.
- Not mandatory. PocketBase cron plus a jobs collection is enough for simple scheduled work, such as Auxesis's tracker sync.

## Market data service

Shared, because Auxesis, beta-app and Portfolio League each fetch prices on their own today.
- Polls prices every few minutes for the **union of symbols** apps register (Portfolio League's ~100 securities, Auxesis's holdings, indices): one fetch per symbol instead of one per app, which matters with Yahoo-style rate limits.
- Owns the NSE holiday calendar, market hours in IST, retries, stale-price detection and **corporate actions** (dividends, splits).
- Internal API (`GET /v1/prices`, `GET /v1/closes?date=`), plus a read-only public route if frontends need live prices.
- Emits `market/close.ready {date}` once official closes are in.

Portfolio League's close pipeline then runs in Inngest on that event: match orders → execute fills → apply corporate actions → recompute portfolios and leaderboard. Every step **idempotent per trading date** (fills keyed by `(order_id, date)`, dividends recorded as applied before cash is credited), with a **concurrency key on the date** so two runs for one day never overlap. It never runs on a holiday or on partial prices.

## Deploy portal (no SSH)

Owners point the platform at a GitHub repo, fill in settings and env vars, and every push to `main`
deploys by itself. The portal lives in `core` at `/admin/apps`.

- **Control plane only.** It never runs or supervises apps, so apps keep running if the portal is down.
- **Apps live in core's database.** The files in `apps/` remain for apps set up by hand; core's registry is
  both together, and ids and paths must be unique across them. (This replaces "the registry is the files in
  `apps/`, reviewed by PR" for portal apps: the super-admin review below is the review.)
- **The GitHub App** is created once by a super admin from `/admin/github` (GitHub's manifest flow:
  read-only contents and metadata, release events). Core keeps its key in its database.
- **Connecting a repo.** Any @iima.ac.in user installs the platform's **GitHub App** on their repo and picks
  it in the portal. The repo must have an `app.toml` at its root whose `owners` include that person, which
  proves they can write to it; the portal fills its form from that file. The person becomes an owner and
  can add co-owners.
- **Approval.** A new app stays offline until a super admin approves it. Later changes to its path, login,
  auth mode, public paths or shape wait for approval too, while the live app keeps its approved settings.
  Everything else (name, roles, memory, env vars, owners) applies at once.
- **Builds run in GitHub Actions**, never on our servers, because building runs arbitrary install scripts.
  The repo calls the platform's reusable workflow (`.github/workflows/build-app.yml`), which on every push to
  `main` publishes a GitHub release `build-<commit>` with `app.tar.gz` and its SHA-256.
- **Auto-deploy.** The portal notices new releases by webhook, or by polling every two minutes when
  GitHub cannot reach us. For an approved app it deploys the newest release on `main`. A release that
  fails its health check rolls back by itself (Nomad `auto_revert`).
- **Artifacts go through core.** Core downloads the release with the App's short-lived token, checks the
  SHA-256, keeps the last few per app on its volume, and Nomad clients fetch them from core over the cluster
  network (`/platform/artifacts/<sha256>.tar.gz`). The gateway blocks that path, and core refuses it when
  a request came through the gateway. So private repos work and no GitHub token is ever handed to Nomad.
- **Deploys** are `jobgen` plus Nomad's jobs API, with a Nomad token under the `deploy` policy. PocketBase
  apps get their data volume created on the first deploy. **Rollback** is deploying an earlier release.
  **Logs** show the app's recent output from Nomad.
- **Env vars:** plain values are stored in core and put in the job; **secrets** go straight into the app's
  Nomad Variable (`nomad/jobs/<id>`) and are never shown again (only their names).
- Every portal action goes into the audit log.
- Later: an `iima` CLI (`iima new`, `iima dev`, `iima deploy`, `iima logs`, `iima backup restore`) over the same API.

## Observability, backups, operations

- **Metrics:** Traefik's Prometheus metrics (per-app rate, latency, errors) in Grafana, with no app changes.
- **Logs:** Nomad task logs and Traefik access logs into Loki.
- **Uptime:** Uptime Kuma pings each health check and alerts the app's **owners**, plus a public status page.
- **Errors:** GlitchTip (Sentry-compatible), hooked into the kits.
- **Page analytics:** self-hosted Umami, **anonymous** (salted daily hashes, as Auxesis already does). App admins see only their own app.
- **Privacy:** emails in logs only where an app needs them, with a retention limit (30 to 90 days). India's DPDP Act applies.
- **Backups:** PocketBase scheduled backups or Litestream to off-box S3 for every app; Nomad snapshots; core's database. **Monthly restore drill** of one random app.
- **Load-test event apps** (Garba-style gate rushes) before the night.
- **App lifecycle:** every app has owners and a review date; an archive mode (read-only or static snapshot); prefer **multi-tenant apps over yearly clones** (one events app rather than `garba2026`, `garba2027`...).
- **Platform contract versioned** (`v` claim, manifest version) with deprecation windows. Renovate keeps kit and PocketBase versions in step across app repos.

## Security summary

- Gateway strips identity headers and the session cookie; only it sets `X-User-Token`.
- App ports accept connections only from the cluster (firewall), and apps bind to the internal interface, never `0.0.0.0` on a public interface.
- Ed25519 tokens with per-app `aud`; no shared signing secret in apps.
- Reserved paths; routes only from approved manifests; Nomad ACLs.
- PocketBase dashboards never public.
- Gateway CSP and security headers by default; CSRF protection required on state-changing endpoints (one origin means app A's JS can POST to app B).
- CI for every app runs `govulncheck` / `npm audit` before deploy; review before first deploy.
- The biggest remaining risk is that every app shares one origin. Keep asking IT for a second hostname for unreviewed apps.

## Decided against (for now)

| Idea | Why not |
| --- | --- |
| gRPC, JSON-RPC between apps | No real speed gain on a LAN; protobuf tooling students will fight; browsers, Inngest and forward auth are HTTP anyway. Connect RPC is the escape hatch if ever needed. |
| Go Micro, go-kit and similar microservice frameworks; Gin, Echo, Fiber | Discovery is Nomad's job and routing and auth are Traefik's; these frameworks bring their own versions of both, plus RPC and codegen. Go 1.22+ `net/http` routing covers what core and the kits need. Fiber does not even use `net/http`. |
| Typed client packages per service, OpenAPI codegen | Two places to update. Dynamic `iimakit.Call` with locally declared structs is enough. |
| Consul | Nomad's discovery covers it. Revisit for DNS, services outside Nomad, KV/locks or a mesh. |
| Vault | Heavy to run (unsealing, HA, policies). Nomad Variables plus sops cover it. |
| Consul Connect / service mesh | Sidecars on every app. Revisit at 30+ apps. |
| Kubernetes, k3s | More than 3 to 4 servers need. |
| One shared PocketBase | Single writer, coupled deploys, all-or-nothing backups, one superuser over everyone's data. |
| A caching server (Redis) | Not needed at campus scale; add only for a measured hotspot. |
| The core app proxying all traffic | Single point of failure and a reimplementation of Traefik. Traefik routes; core only answers "who is this?". |
| Apps calling the auth service per request | Every app would need the session cookie and correct code; one mistake leaves an app open. |
| Writing our own gateway | Traefik already does TLS, HTTP/2, WebSockets, retries, rate limits. |
| Terraform, Packer, Waypoint, Boundary | Fixed campus machines; a setup script or Ansible is enough. Waypoint is discontinued. |

HashiCorp products are under the Business Source License (and HashiCorp is now part of IBM). Internal use
like ours is allowed.

## Open questions

- Which server hosts what today, and where does `chatbot.iima.ac.in` (used by BetaMarkets' PocketBase URL) fit?
- For the cluster: each machine's OS, private IP and interface; which one faces the internet (the entry node); whether IT provides the TLS certificate for students.iima.ac.in or Let's Encrypt can reach it; how `students.iima.ac.in` reaches the entry node today (DNS to Apache, or a campus proxy).
- Off-box backups: where (another campus machine, an S3-compatible bucket, institute storage)?
- Will IT give a second hostname for unreviewed apps?
- Can the institute share a roll list for attributes, or do email patterns have to do?
