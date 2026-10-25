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

The app's env (e.g. `ADMIN_EMAILS`) comes from your shell; its data lives in `.dev/data/<id>`.
In Git Bash, prefix the build with `MSYS_NO_PATHCONV=1` so `/auxesiscapital/` is not turned into a
Windows path.

## Layout

| Folder | What |
| --- | --- |
| `core/` | Sign-in, sessions, `/auth/verify`, JWKS, `/auth/me`, home page. Go, SQLite. |
| `kit/` | `iimakit`: verifies `X-User-Token` in Go apps (`Middleware`, `User`, `RequireUser`, `RequireRole`), plus `Addr`, `Serve`, `SPA`, `Health`, dev user. |
| `kit/pb/` | `iimakit/pb`: puts a Go + PocketBase app behind the platform (`pb.New`). Its own Go module. |
| `kit-js/` | `@iima/kit` for frontends: `api`, `me`, `login`, `logout`, router, stores. (`/next` in phase 6.) |
| `apps/` | One `app.toml` per app: the registry. |
| `deploy/traefik/` | Gateway config and the `platformstrip` plugin. |
| `deploy/nomad/` | Job specs (phase 4). |
| `dev/` | The dev stack command, the stub app and its manifests. |
| `docs/` | Maintainer handbook. |

## Checks

Each Go module is tested on its own, as CI does:

```
cd core && go vet ./... && go test ./...
```

Same for `kit`, `kit/pb`, `dev` and `deploy/traefik/plugins/platformstrip`. For the JS kit:
`cd kit-js && npm test`.
