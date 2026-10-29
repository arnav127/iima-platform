# Maintainer handbook

For whoever runs students.iima.ac.in this year. The decisions and their reasons are in
[ARCHITECTURE.md](../ARCHITECTURE.md); the servers in [cluster.md](cluster.md); hosting an app in
[portal.md](portal.md); moving an old one over in [porting.md](porting.md).

## The pieces, in one breath

Traefik on the entry node takes every request. It asks **core** who the person is
(`/auth/verify`), strips anything a browser could fake, and sends the request to the app, which
Nomad runs somewhere on the cluster. Core keeps sessions, roles, the apps' settings, the external
routes and the GitHub App's key in one SQLite file on the `core-data` volume. Apps keep their own
data on their own volumes.

## Every term

- **Review dates.** The dashboard tags apps past their review date. Ask their owners: still needed? If
  not, archive the app on its page (its data volume stays until you delete it by hand).
- **Owners.** Graduating owners add next year's team on the app's page before they leave.
- **Super admins.** Keep two or three, so nobody gets locked out (`/admin/super-admins`).
- **Restore drill.** Restore one random app's backup to a scratch volume and open it (below).
- **Updates.** Bump pinned versions (Traefik, Nomad, PocketBase, Go, Node) in their own commits; CI
  runs `govulncheck` on every push.

## Handing over super admin

1. Add the new super admins (`/admin/super-admins`, or `core superadmin add` inside core's allocation).
2. Hand over the GitHub organization that owns the GitHub App, and the Google OAuth client in the
   Google Cloud console.
3. Hand over the Nomad management token (password manager) and SSH access to the nodes.
4. Rotate what the old team held (below), then remove them as super admins.

## Rotating secrets

| Secret | How |
| --- | --- |
| Core's token signing key | Inside core's allocation: `local/core keys rotate`, then `nomad alloc restart <alloc>`. Core signs with the new key and still publishes the old one; apps fetch the new key on first sight. After 5 minutes (the token lifetime), `local/core keys prune`. |
| Google OAuth secret | New secret in the Google Cloud console, `nomad var put -force nomad/jobs/core ...` with every key again; core restarts by itself. |
| GitHub App key | GitHub App settings, Private keys, generate one; set `GITHUB_APP_PRIVATE_KEY` in core's variable (or update `github_app_key` in core's settings table); restart core; delete the old key on GitHub. |
| Nomad tokens | `nomad acl token create` a new one (deploy policy for core, traefik policy for Traefik), put it in place, `nomad acl token delete` the old one. |
| An app's secrets | Its page, Environment: save a new value; the app restarts by itself. For PocketBase beside a Next.js or Python app, a new `PB_ADMIN_PASSWORD` resets the superuser's password on restart. |

## Restoring a backup

- **An app's PocketBase.** With S3 backups on (`BACKUP_CRON`, `BACKUP_S3_*` in its secrets), restore
  from its dashboard over an SSH tunnel (Backups, restore). From files instead: stop the app on its
  page, put the backup's contents into its volume folder (the app page's Data section shows the node
  and folder), `chown -R nobody:nogroup`, deploy the running build again.
- **Core.** Stop core (`nomad job stop core`), replace `core.db` in the `core-data` volume's folder
  with the copy, `nomad job run` core again. Sessions in the copy are the ones people get back.
- **Nomad's own state.** Daily snapshots are in `/srv/iima/backups/` on each server:
  `nomad operator snapshot restore <file>` with a management token.

## When something is wrong

| Symptom | Look at |
| --- | --- |
| A page answers 404 | `/admin/routes` and the dashboard: is the path someone's? Traefik's log for the router it picked. |
| Everyone is signed out or sees "sign-in unavailable" | Core: `nomad job status core`, `nomad alloc logs -job core`. |
| One app is down | Its page: Running and Logs; Deploys for the last build. Deploy the previous build to roll back. |
| A new build does not deploy | Its page: is auto-deploy on, is a deploy held for data, did the build reach Builds? The release on GitHub needs `app.tar.gz` and `app.tar.gz.sha256`. |
| Repos cannot be connected | `/admin/github`: is the App connected? Owners must install it on the repo and list their email in `app.toml`. |
| An external route misbehaves | Its upstream answers directly? Redirects to the wrong place: the upstream's path should match the public one, or rely on the rewrite. Streams arrive late: tick realtime. |

## Running it locally

`go run ./dev/cmd/devstack` (file mode), `-nomad` (the cluster's layout), `-nomad -portal` (the deploy
portal against a fake GitHub with demo repos). The README has the details.
