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

## Signing keys

Core creates an Ed25519 key in its database on first start and publishes it in the JWKS. Rotation
(add a key, sign with it, keep publishing the old one for a few minutes) gets a command later.
