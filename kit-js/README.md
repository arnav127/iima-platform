# @iima/kit

Frontend kit for apps on the platform. TypeScript source, no build step: Vite and Next.js compile it
with the app.

```ts
import { setBase, startRouter, api, me, login, logout } from '@iima/kit';

setBase(import.meta.env.BASE_URL); // "/auxesiscapital/"
startRouter();

const data = await api<Portfolio>('/api/aux/portfolio'); // the app's own API, cookie auth
const who = await me('auxesiscapital');                 // platform user from /auth/me
login('/portfolio');                                     // platform sign-in, back to /portfolio
```

Identity is the platform session cookie. The gateway turns it into a verified token for the app's
backend, so the frontend never holds a token.

Until it is published, apps depend on it by path: `"@iima/kit": "file:../iima-platform/kit-js"`.

## Next.js and other Node servers: `@iima/kit/next`

```ts
import { headers } from 'next/headers';
import { getUser, hasRole, sameOrigin } from '@iima/kit/next';

const user = await getUser('betamarkets', await headers()); // server component; null when signed out
const user = await getUser('betamarkets', req.headers);     // route handler
if (!hasRole(user, 'admin')) return new Response('Forbidden', { status: 403 });
if (req.method !== 'GET' && !sameOrigin(req.headers)) return new Response('Bad origin', { status: 403 });
```

It verifies the gateway's `X-User-Token` (Ed25519, with `jose`) against core's keys, with the same
rules as Go's `iimakit`: right app (`aud`), issuer, version, not expired. Take the user from the
token, never from a request body or query. Dev mode: `IIMA_DEV_USER` and `IIMA_DEV_ROLES`.

Tests: `npm test` (Node 22.6+ runs the TypeScript directly).
