# iima-platform

The shared platform for student and club apps at `students.iima.ac.in`: a Traefik gateway, Nomad to run
apps, a `core` service for one login and central roles, and kits so apps get auth, health, routing and
backups for free.

Read **`ARCHITECTURE.md`** before changing anything. It holds the decisions and the reasons, including what
was deliberately ruled out. Work goes phase by phase in **`ROADMAP.md`**. Tick items off as they land.

## Where things are

This folder sits in `C:\sandbox` next to the apps it serves:
- `..\auxesiscapital`: Go + PocketBase v0.40 + Preact/Vite. The reference app and the first to migrate.
- `..\beta-app`: same stack as Auxesis.
- `..\iima-garba`: same stack; needs external login (Gmail guests, Microsoft for exchange students).
- `..\predictionmarkets` (BetaMarkets): Next.js 16 + a standalone PocketBase binary.
- `..\papertrading` (Portfolio League): Next.js + FastAPI (Python) + a standalone PocketBase + Inngest (Python).

## Rules

- **Don't touch the app repos until their phase**, and then work on a branch in that repo (`platform-migration`), never on main. Each app must keep working on today's Apache + PM2 setup until it is cut over.
- **Don't change the architecture silently.** If something in `ARCHITECTURE.md` turns out wrong, say so, propose the change, and update the doc once agreed.
- **Keep it simple enough to hand over.** Maintainers change every year. Prefer the standard library and boring tools; no new infrastructure unless the doc calls for it.
- **The dev machine is Windows.** Scripts and dev tooling must run on Windows (Go or Node scripts, not bash-only). Servers are Linux.
- **Never commit secrets.** `.env` files stay untracked; provide `.env.example`.
- **Security-critical code** (`/auth/verify`, token minting and checking, header stripping) gets tests for the failure cases: wrong `aud`, expired token, spoofed headers, wrong-domain user, open redirects.
- Go: `go test ./...` and `go vet ./...` pass before calling something done. Match the style of the existing apps (small packages, plain `net/http`, comments that explain why).
- PocketBase is pre-1.0. Pin `v0.40.x` to match the apps.
- Docs and READMEs: short, plain language. No em dashes or en dashes.

## Layout to create (phase 0)

```
iima-platform/
  core/          Go: auth, sessions, verify, JWKS, roles, registry, admin UI (later: deploy portal)
  kit/           Go module: iimakit (no PocketBase dependency)
    pb/          iimakit/pb (wraps PocketBase)
  kit-js/        @iima/kit (and @iima/kit/next)
  apps/          app.toml per app (the registry)
  deploy/
    traefik/     static + dynamic config, middlewares
    nomad/       job specs and templates
  docs/          handbook
```

Module path assumption: `github.com/arnav127/iima-platform/...`. Change it if the repo lives elsewhere.

## Starting point

Begin with **phase 0, then phase 1**. The first milestone is: on a Windows laptop, one command starts
Traefik, `core` and a stub app; visiting `http://localhost:8080/demo` in optional mode shows "signed out";
signing in with an @iima.ac.in Google account (or dev mode) shows the user from a verified `X-User-Token`;
a Gmail account is refused for an `iima` app and allowed for an `external` one.
