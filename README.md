# iima-platform

The shared platform for student and club apps at `students.iima.ac.in`: one gateway (Traefik), one
login (`core`), and kits so apps get auth for free. Read [ARCHITECTURE.md](ARCHITECTURE.md) for the
decisions and [ROADMAP.md](ROADMAP.md) for progress.

## Run it on your laptop

Needs Go 1.27 or newer. From the repo root:

```
go run ./dev/cmd/devstack
```

The first run downloads Traefik (pinned, checksum verified) into `.dev/`. Then open
http://localhost:8080/:

| URL | What it shows |
| --- | --- |
| `/demo/` | IIMA-only app, auth optional. Signed out until you sign in with an @iima.ac.in address. |
| `/demo-external/` | Same app, open to any Google account. |
| `/demo-private/` | IIMA-only, auth required at the gateway: signed-out visitors go straight to sign-in. |

Sign-in uses a dev form that accepts any email: `someone@iima.ac.in` acts as an IIMA member,
`someone@gmail.com` as an outsider. Each demo page lists what reached the app, so you can check
that spoofed `X-User-*` headers and the session cookie never get through.

To test real Google sign-in, copy `.env.example` to `.env`, fill in an OAuth client, and run
`go run ./dev/cmd/devstack -google`.

To run a real Go + PocketBase app from its repo next to this one, build its frontend for its path
and add `-app`:

```
cd ../auxesiscapital && VITE_BASE=/auxesiscapital/ npm run build && cd ../iima-platform
go run ./dev/cmd/devstack -app auxesiscapital=../auxesiscapital
```

The app's env (e.g. `PRICE_HISTORY=off`) comes from your shell; its data lives in `.dev/data/<id>`.
In Git Bash, prefix the build with `MSYS_NO_PATHCONV=1` so `/auxesiscapital/` is not turned into a
Windows path.

Add `-nomad` to run the cluster's layout instead: a local Nomad agent (downloaded and pinned like
Traefik), core and every app as Nomad jobs generated from their manifests, and Traefik reading the
routes from Nomad. The Nomad UI is at http://127.0.0.1:4646/ui. See [docs/cluster.md](docs/cluster.md)
for the real servers.

Super admins: `cd core && CORE_DATA=../.dev/core-data go run ./cmd/core superadmin add you@iima.ac.in`,
then open http://localhost:8080/admin.

A Next.js app runs the same way (`npm run build` in its repo first); an app not in `apps/` is read
from its own `app.toml`. `-nomad -portal` runs the deploy portal against a fake GitHub with both
templates as demo repos: connect one, approve it, and it deploys. See [docs/portal.md](docs/portal.md).

## Host an app

Owners connect a GitHub repo at `/admin/apps/new`; every push to `main` builds and deploys once a
super admin has approved the app. Start from `template/go-pocketbase`, `template/next` or `template/python`. Guide:
[docs/portal.md](docs/portal.md).

## Layout

| Folder | What |
| --- | --- |
| `core/` | Sign-in, sessions, `/auth/verify`, JWKS, `/auth/me`, home page. Go, SQLite. |
| `kit/` | `iimakit`: verifies `X-User-Token` in Go apps (`Middleware`, `User`, `RequireUser`, `RequireRole`), plus `Addr`, `Serve`, `SPA`, `Health`, dev user. |
| `kit/pb/` | `iimakit/pb`: puts a Go + PocketBase app behind the platform (`pb.New`). Its own Go module. |
| `kit-js/` | `@iima/kit` for frontends: `api`, `me`, `login`, `logout`, router, stores. `@iima/kit/next`: `getUser()` for Next.js servers. |
| `pocketbase/` | The platform's build of stock PocketBase (pinned version) for apps that run it next to a Next.js or Python server. |
| `template/` | `go-pocketbase`, `next` and `python`: the starting points for a new app, built and tested in CI. |
| `kit-py/` | `iimakit` for Python (FastAPI): `Verifier(app).user(request)`, `require_user`. |
| `apps/` | One `app.toml` per app: the registry. |
| `deploy/traefik/` | Gateway config and the `platformstrip` plugin. |
| `deploy/nomad/` | Core job, cluster settings, agent config template, ACL policies, volume example. |
| `deploy/servers/` | `setup.sh` for each campus server, systemd units, firewall, snapshot timer. |
| `dev/` | The dev stack command, the stub app and its manifests. |
| `template/go-pocketbase/` | Starting point for a new Go + PocketBase app (copy the folder). |
| `docs/` | Maintainer handbook. |

## Checks

Each Go module is tested on its own, as CI does:

```
cd core && go vet ./... && go test ./...
```

Same for `kit`, `kit/pb`, `dev` and `deploy/traefik/plugins/platformstrip`. For the JS kit:
`cd kit-js && npm ci && npm test`. For the Python kit: `cd kit-py && pip install . && python -m unittest discover -s tests`.
