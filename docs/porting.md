# Porting an existing app

How to move an app that runs today under PM2 behind Apache onto the platform, keeping its data, with a
way back at every step. Auxesis, Beta, Garba and BetaMarkets have `platform-migration` branches that
already did the code part.

## 0. Before: keep it working as it is

Give the app an **external route** in `/admin/routes` (super admins): its path, the host and port it
runs on today (e.g. `http://10.0.0.5:3010/betamarkets`), sign-in `none`. Once the site's traffic comes
through the gateway, the app keeps working exactly as it did behind Apache, while you port it.

## 1. The code (on a `platform-migration` branch)

- Sign-in comes from the platform: drop the app's own Google login, take the person from the gateway's
  token (`iimakit` in Go, `@iima/kit/next` in Next.js, `kit-py` in Python). Never trust a user id
  from the request.
- Roles the app had (admins, writers...) become platform roles, declared in `app.toml`.
- A standalone PocketBase gets no public route; only the app's server talks to it
  (`pocketbase = true` for Next.js and Python apps).
- Add `app.toml` at the repo root (same `id` as the route, if it had one, so roles carry over) and
  `.github/workflows/deploy.yml` from a template.
- Secrets move out of the code into the app's page in the portal.
- Try it locally: `go run ./dev/cmd/devstack -app <id>=../<repo>`.

## 2. Carry the roles over

Grant today's admins and other roles on the platform before the switch. Beta and Garba have a
`platform-roles` command, BetaMarkets `node scripts/platform-roles.mjs`: run it where the app runs today
and run its output (`core roles set ...`) where core runs.

## 3. Carry the data over (PocketBase apps)

You do not point the platform at the old `pb_data` folder: you copy its contents into the app's data
volume once, before its first deploy on the platform. The old folder stays as it is, as the rollback.

What the platform does with data:
- Each PocketBase app has a **volume**, `<id>-data` unless `app.toml` says otherwise. Inside the app
  it is mounted at `/data` and PocketBase runs with `--dir /data`, so the volume holds exactly what
  `pb_data/` holds: `data.db`, `auxiliary.db`, `storage/`, `backups/`.
- On the node it is a folder under `/opt/nomad/data/host_volumes/<volume id>/`. The volume pins the
  app to that node, so it never starts somewhere with an empty database.

Steps:

1. **Create the volume yourself, on the node you choose**, before connecting the app (the portal
   creates one only when none exists, and then Nomad picks the node):
   ```
   cp deploy/nomad/volume.hcl.example betamarkets-data.hcl   # set name and the node
   nomad volume create betamarkets-data.hcl
   nomad volume status -verbose betamarkets-data             # shows the folder on that node
   ```
2. **Stop the app on PM2** (`pm2 stop betamarkets-pocketbase` and the app), so its SQLite files stop
   changing. The route keeps pointing at it; visitors see it down for the minutes this takes.
3. **Copy the contents in**, as root on the node:
   ```
   rsync -a /home/beta/betamarkets/pb_data/ <host path>/      # or scp from the old server
   chown -R nobody:nogroup <host path>                         # apps run as nobody (exec driver)
   ```
4. **Connect the repo and approve it** (`/admin/apps/new`, same `id` and `volume`). Set its secrets on
   the app's page first (`PB_ADMIN_EMAIL`, `PB_ADMIN_PASSWORD` for a standalone PocketBase). The first
   deploy starts on the copied data; PocketBase runs any pending migrations then (BetaMarkets moves
   from PocketBase 0.39 to 0.40 and locks its collections down), one more reason the old folder stays.

Without copying files: download a backup zip from the old PocketBase dashboard, then restore it in the
new app's dashboard over an SSH tunnel. Fine for small apps; slow for a large `storage/`.

## 4. Switch

Approving the app replaces its external route: the path now goes to the platform's copy. If the app
was still behind Apache instead, change its `ProxyPass` line to the entry node. Then check: sign in as
an ordinary user and as an admin, and watch `nomad alloc logs -job <id>` and the app page's Logs for a
day.

## 5. Way back

- Stop the app on its page (or `nomad job stop <id>`).
- If anything was written on the platform since the switch, copy the volume's folder back over the old
  `pb_data`.
- `pm2 start` the app again and put its route back in `/admin/routes` (or the old `ProxyPass` line).
- Keep the PM2 setup and the old folder until the next term.
