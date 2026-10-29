# App template: Next.js + PocketBase

The second starting point for an app on students.iima.ac.in (the first is `template/go-pocketbase`).
Sign-in, roles and attributes come from the platform; PocketBase runs beside the app with no public
route, and only this app's server talks to it.

## Start a new app

1. Copy this folder to a new repo next to `iima-platform`.
2. Pick an id (lower case, e.g. `quizclub`) and set `id` and `path` in `app.toml` and `APP_ID` in
   `src/lib/server/session.ts`. Put your email in `owners`.
3. Point the kit at the platform: in `package.json`, `@iima/kit` assumes this folder sits inside
   `iima-platform/template`; in your own repo change `file:../../kit-js` to `file:../iima-platform/kit-js`.
4. Push to GitHub and connect the repo at `/admin/apps/new` on the platform. After a super admin
   approves it, every push to `main` builds (`.github/workflows/deploy.yml`) and deploys.
5. Set `PB_ADMIN_EMAIL` and `PB_ADMIN_PASSWORD` as secrets on the app's page: PocketBase creates that
   superuser on start, and the app uses it to reach PocketBase.

## Work on it

```
npm install
npm run build
cd ../iima-platform
go run ./dev/cmd/devstack -app mynextapp=../mynextapp
```

`devstack` runs the platform's PocketBase with your `pb_migrations` and a throwaway superuser, and
`next start`. Your app needs an `app.toml` entry in the platform's registry for that (`apps/`), or
connect it through the portal with `devstack -nomad -portal`.

## The rules this template follows

| | |
| --- | --- |
| `src/lib/server/session.ts` | `getSession` / `requireUser`: the person from the platform token. Never trust an id from the request. |
| `src/lib/server/pb.ts` | `adminPb()` (superuser) and `userPb(id)` (impersonation, so API rules apply). |
| `src/proxy.ts` | The platform's CSP with a per-request nonce; pages render per request. |
| `src/app/api/health` | The platform's health check, at `<path>/api/health`. |
| `pb_migrations/` | Your schema. Keep API rules strict: they are what `userPb` is held to. |
