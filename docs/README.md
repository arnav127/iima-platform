# Maintainer handbook

Short how-tos for whoever runs the platform this year. Filled in as each phase lands.

## How a request is authenticated

1. Traefik receives `GET /demo/page` and drops any `X-User-*` and `Authorization` headers.
2. Traefik asks core `GET /auth/verify?mode=optional` with the browser's cookie and `X-Forwarded-Uri: /demo/page`.
3. Core finds the app (`/demo` in `apps/`), the session and the user. If the app's `login` policy allows the user, it mints a JWT for `aud=demo`, valid 5 minutes, and returns it as `X-User-Token`.
4. Traefik copies `X-User-Token` onto the request, removes the `iima_session` cookie, and forwards to the app.
5. The app verifies the token with core's public keys (`/auth/.well-known/jwks.json`) using `iimakit`.

`/auth/verify` itself is never reachable from outside, nor is any `/<app>/_/` (PocketBase dashboards).

## Add an app to the registry

Add `apps/<id>.toml` (copy an existing one) in a pull request. Core refuses to start if a manifest is
invalid, has an unknown field, or reuses an id or path, so a typo cannot quietly change who can sign in.

## Super admins, app admins and roles

The first super admin is added on the server, with core's own binary and the same `CORE_DATA`:

```
core superadmin add someone@iima.ac.in
core superadmin list
```

After that, everything is in `/admin` (signed in as a super admin):

- **Apps:** pick an app, then grant or remove its roles (only roles its `app.toml` declares) and appoint app admins. App admins see only their apps and can only change roles.
- **People:** look up an email to see the attributes the platform derives (affiliation, programme, batch, year) and their roles in every app. Set an override when the address is misleading (staff, exchange students, someone who repeated a year). Clear all three fields to go back to the derived values.
- **Super admins:** keep two or three. The last one cannot be removed.
- **Audit log:** every change, with who made it.

Changes reach the apps on the next request. From a script: `core roles set APP EMAIL [ROLE...]`.

When handing over in June: add next year's super admins first, then remove your own entry.

## Signing keys

Core creates an Ed25519 key in its database on first start and publishes it in the JWKS. Rotation
(add a key, sign with it, keep publishing the old one for a few minutes) gets a command later.
