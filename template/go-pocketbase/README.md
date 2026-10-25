# App template: Go + PocketBase + Preact

The starting point for a new app on students.iima.ac.in, lifted from Auxesis. Sign-in, roles,
attributes, serving the frontend, backups and the dashboard lock are all done by the platform kits,
so a new app starts with its own features.

## Start a new app

1. Copy this folder to a new repo next to `iima-platform`.
2. Pick an id (lower case, e.g. `quizclub`) and replace `myapp` everywhere: `app.toml`,
   `backend/main.go`, `backend/app/app.go` (the `/api/myapp` routes), `web/src`, and the module path
   in `backend/go.mod`.
3. Point the kits at the platform: in `backend/go.mod` the `replace` lines and in `package.json` the
   `@iima/kit` path assume this repo sits next to `iima-platform` (fix `../../` to `../`).
4. Add `app.toml` to `iima-platform/apps/` with a pull request.

## Work on it

```
npm install
cd backend && go test ./...
```

On the platform's local stack (from `iima-platform`):

```
cd ../myapp && VITE_BASE=/myapp/ npm run build && cd ../iima-platform
go run ./dev/cmd/devstack -app myapp=../myapp
```

Without the platform: `IIMA_DEV_USER=you@iima.ac.in IIMA_DEV_ROLES=admin npm run backend`, and
`npm run dev` in another terminal (Vite on :5173 proxies `/api`).

## What lives where

| | |
| --- | --- |
| `backend/main.go` | `pb.New`: the platform hooks. `Access` decides who gets an account here. |
| `backend/app/` | Your routes (`/api/myapp/...`) and rules. `pb.Claims(e)`, `pb.HasRole(e, "admin")`. |
| `backend/migrations/` | Your schema. `users.roles` mirrors the platform's roles for API rules. |
| `web/src/` | The frontend. `api()`, `login()`, `logout()`, `link()` come from `@iima/kit`. |

Keep inline scripts out of `index.html`: the gateway's CSP only runs scripts from files.
