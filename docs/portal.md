# Hosting an app: the deploy portal

Any student or club with an @iima.ac.in account can host an app at `students.iima.ac.in/<path>`.
Sign-in, roles, backups and deploys come with it.

## For app owners

1. **Start from a template** in this repo: `template/go-pocketbase` (Go + PocketBase + Preact),
   `template/next` (Next.js) or `template/python` (FastAPI). Next.js and Python apps can have
   PocketBase beside them (`pocketbase = true`). Copy one into your own GitHub repo.
2. **Fill in `app.toml`**: an `id`, a `path` like `/quiz`, and your @iima.ac.in email in `owners`
   (that is how the platform knows you may deploy the repo). Keep `.github/workflows/deploy.yml`:
   on every push to `main` it builds your app and publishes a release `build-<commit>`.
3. **Install the platform's GitHub App** on that repo only (the link is on `/admin/apps/new`). It can
   read the repo's files and releases, nothing else.
4. **Request the app** at `/admin/apps/new` with the repo's name (`your-org/your-app`). A super admin
   reviews it; once approved, the newest build goes live, and every later push deploys by itself.

On the app's page (`/admin/apps/<id>/deploy`):

| | |
| --- | --- |
| Builds | Every release the platform fetched. Deploy any of them; deploying an older one is a rollback. A build that fails its health check rolls back by itself. |
| Settings | Name, roles, memory and review date apply at once. The path, who may sign in, signed-out visitors, public paths and shape wait for a super admin. |
| Environment | Plain values go into the app (saving one redeploys). Secrets go straight to Nomad and are never shown again (saving one restarts the app). |
| Owners | Owners deploy, change settings, and manage roles. Add next year's team before you leave. |
| Logs | The end of the running copy's output. |

Apps with `pocketbase = true` need two secrets, `PB_ADMIN_EMAIL` and `PB_ADMIN_PASSWORD`:
PocketBase creates that superuser on start and the app uses it.

`NEXT_PUBLIC_*` values are built into the page, so set them as GitHub repository variables for
the build, not here.

## For super admins

- **Once: create the GitHub App** at `/admin/github`. Pick a name (and ideally an organization the
  institute or a club controls, so it outlives one maintainer) and confirm on GitHub. Core keeps the
  App's key in its database. To use one made by hand instead, set `GITHUB_APP_ID`, `GITHUB_APP_SLUG`,
  `GITHUB_APP_PRIVATE_KEY` and `GITHUB_WEBHOOK_SECRET` in core's Nomad Variable.
- **Approve requests** from the dashboard. Look at the repo first: it will run on campus servers. The
  review shows the path, who may sign in and the roles.
- **The home app.** One app may take the path `/`. It gets every request no other app or the platform
  claims (the mess menu at `/mess`, events at `/events`...), so approve it only for the app meant to
  be the site's front page. The platform's own app directory stays at `/platform/apps`.

## Trying it on a laptop

```
go run ./dev/cmd/devstack -nomad -portal
```

This starts a fake GitHub with three demo repos (the templates, already built). Sign in as
`you@iima.ac.in`, connect `iima-clubs/myapp`, `iima-clubs/mynextapp` or `iima-clubs/mypyapp`, then sign in as
`admin@iima.ac.in` (switch account) and approve. For the Next.js one, set the two `PB_ADMIN_*` secrets
first.
