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

Tests: `npm test` (Node 22.6+ runs the TypeScript directly).
