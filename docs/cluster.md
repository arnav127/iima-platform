# Running the cluster

How the campus servers are set up, how an app gets deployed, and how Auxesis moves from Apache to
the platform. Everything here is run by a super admin until the deploy portal (phase 8).

## The layout

| Machine | Role | Runs |
| --- | --- | --- |
| node 1 | Nomad server + client, **entry** | Traefik on 80/443 (systemd), apps |
| node 2, 3 | Nomad server + client | apps |
| node 4 | Nomad client | apps |

- Traefik runs under systemd on the entry node, not under Nomad, so the front door does not depend
  on the scheduler. It reads app routes from Nomad's service catalogue and core's routes from its
  own files (`/etc/traefik/dynamic/platform.yml`), so no job can claim `/auth` or `/admin`.
- Core runs as a Nomad job like any app. Traefik reaches it on its `internal` entry point
  (127.0.0.1:8081), which the core job's tags route on to wherever Nomad placed core.
- Apps listen on Nomad-assigned ports on each node's private address. The firewall lets only the
  other nodes reach them.

## 1. Set up each machine

From a checkout of this repo on the machine, as root:

```
NODE_NAME=iima-node-1 NODE_IP=10.0.0.11 NET_IFACE=eth0 \
SERVER_IPS=10.0.0.11,10.0.0.12,10.0.0.13 CLUSTER_CIDR=10.0.0.0/24 \
ROLE=server ENTRY=true ./deploy/servers/setup.sh
```

Nodes 2 and 3: `ROLE=server ENTRY=false`. Node 4: `ROLE=client`. The script installs pinned Nomad
(and Traefik on the entry node), writes the configs, and writes the firewall rules to
`/etc/nftables.d/iima.nft` without loading them. Read them, then run again with `FIREWALL=apply`.

## 2. Bootstrap Nomad's ACLs (once)

On any server, after all three are up:

```
nomad acl bootstrap                       # prints the management token: keep it in the password manager
export NOMAD_TOKEN=<management token>
nomad acl policy apply -description "deploy apps" deploy deploy/nomad/acl/deploy.policy.hcl
nomad acl policy apply -description "traefik reads services" traefik deploy/nomad/acl/traefik.policy.hcl
nomad acl token create -name traefik -policy traefik     # secret goes in /etc/traefik/nomad.token on the entry node
nomad acl token create -name snapshot -type management   # secret goes in /etc/nomad.d/snapshot.token on each server
```

Run `setup.sh` again on the entry node so Traefik picks up its token, and add the TLS certificate
(`deploy/traefik/dynamic/tls.prod.yml.example`).

## 3. Deploy core

```
nomad volume create core-data.hcl         # from deploy/nomad/volume.hcl.example, name core-data
nomad var put nomad/jobs/core GOOGLE_CLIENT_ID=... GOOGLE_CLIENT_SECRET=...
nomad job run -var version=v0.3.0 -var artifact=<core tar.gz URL> -var sha256=<hash> deploy/nomad/core.nomad.hcl
nomad alloc exec -task core <alloc id> local/core superadmin add you@iima.ac.in
```

The Google OAuth client needs `https://students.iima.ac.in/auth/callback` as a redirect URI.

## 4. Deploy an app

```
nomad volume create auxesiscapital-data.hcl          # PocketBase apps: once, before the first deploy
nomad var put nomad/jobs/auxesiscapital EXCEL_URL=... # secrets, if any
cd core && go run ./cmd/jobgen -app auxesiscapital -version v1.2.0 \
    -artifact <release tar.gz URL> -sha256 <hash> > auxesiscapital.json
nomad job run -json auxesiscapital.json
```

The release tar.gz holds `./app` (the Linux binary) and `./pb_public` (the frontend built with
`VITE_BASE=/auxesiscapital/`). Nomad checks the SHA-256 before running it.

**Roll back:** `nomad job history auxesiscapital`, then `nomad job revert auxesiscapital <version>`.
A deploy that fails its health check reverts by itself.

## 5. Cut Auxesis over from Apache

Before: Auxesis's `platform-migration` branch merged and released; core deployed; the fund's
admins given the `admin` role in `/admin/apps/auxesiscapital` (they replace `ADMIN_EMAILS`).

1. **Copy the data.** Stop Auxesis under PM2 (`pm2 stop auxesiscapital`), copy its `data/` folder
   into the `auxesiscapital-data` volume's directory on its node, and deploy the job.
2. **Check it on the platform** through the entry node before switching DNS or Apache:
   `curl --resolve students.iima.ac.in:443:<entry IP> https://students.iima.ac.in/auxesiscapital/api/health`,
   then sign in as an investor and as an admin.
3. **Switch traffic.** Point `/auxesiscapital` (or the whole host, once every app has moved) at the
   entry node instead of the PM2 process: in Apache, `ProxyPass /auxesiscapital/ https://<entry node>/auxesiscapital/`,
   or move DNS when the whole site moves.
4. **Watch** the Traefik access log and `nomad alloc logs -job auxesiscapital` for a day.

**Rollback path:** Apache's old `ProxyPass` to PM2 stays in the file, commented. To go back: stop the
Nomad job, copy the data folder back if anything was written since, `pm2 start auxesiscapital`, and
restore the old Apache lines. Keep the PM2 setup until the next term.

## Moving Beta and Garba

The same steps as Auxesis, plus their roles. Before switching, on the server where the app runs
today, print the platform commands for its current writers, mentors, volunteers and admins, and run
them where core runs:

```
./beta-server platform-roles --dir <beta data dir>     # core roles set beta <email> <role>
./garba platform-roles --dir <garba data dir>          # core roles set garba2026 <email> <role>
```

Then appoint the apps' app admins (or rely on `manager_role = "admin"`: their admins can already
manage roles from the apps' own People screens). Garba: check that exchange colleges can sign in
with Google before the event; Microsoft login is not on the platform yet.

## Backups on the cluster

- Nomad state: `nomad-snapshot.timer` saves `/srv/iima/backups/nomad-*.snap` daily on each server.
- App data: PocketBase backups to S3-compatible storage (`BACKUP_CRON`, `BACKUP_S3_*` in the app's
  Nomad Variable).
- Core's database lives in the `core-data` volume; copy it off the node with the other backups.
- Off-box storage is still an open question (see ARCHITECTURE.md).

## Try it on a laptop

`go run ./dev/cmd/devstack -nomad -app auxesiscapital=../auxesiscapital` runs the same layout locally:
a Nomad dev agent, core and the apps as Nomad jobs (raw_exec instead of exec), and Traefik with
the Nomad provider and the internal entry point.
