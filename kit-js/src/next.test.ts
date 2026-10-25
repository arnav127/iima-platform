import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer, type Server } from 'node:http';
import { SignJWT, exportJWK, generateKeyPair, type CryptoKey } from 'jose';
import { GATEWAY_CSP, TokenError, contentSecurityPolicy, devClaims, getUser, hasRole, newNonce, sameOrigin, verifier } from './next.ts';

let server: Server;
let jwksUrl = '';
let key: CryptoKey;
let other: CryptoKey;
let fetches = 0;

before(async () => {
  ({ privateKey: key } = await generateKeyPair('EdDSA', { crv: 'Ed25519' }).then(async (k) => {
    const jwk = { ...(await exportJWK(k.publicKey)), kid: 'k1', alg: 'EdDSA', use: 'sig' };
    server = createServer((_, res) => {
      fetches++;
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ keys: [jwk] }));
    });
    return k;
  }));
  ({ privateKey: other } = await generateKeyPair('EdDSA', { crv: 'Ed25519' }));
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', r));
  const addr = server.address() as { port: number };
  jwksUrl = `http://127.0.0.1:${addr.port}/jwks.json`;
});
after(() => server.close());

const now = () => Math.floor(Date.now() / 1000);
function claims(over: Record<string, unknown> = {}) {
  return { v: 1, iss: 'students.iima.ac.in', aud: 'betamarkets', sub: 'u_1', sid: 's_1', iat: now(), exp: now() + 60,
    email: 'p25me@iima.ac.in', provider: 'google', member: true, roles: ['admin'], ...over };
}
function sign(payload: Record<string, unknown>, opts: { kid?: string | null; k?: CryptoKey } = {}) {
  const header: Record<string, string> = { alg: 'EdDSA' };
  if (opts.kid !== null) header.kid = opts.kid ?? 'k1';
  return new SignJWT(payload).setProtectedHeader(header as never).sign(opts.k ?? key);
}

test('accepts a good token', async () => {
  const c = await verifier('betamarkets', { jwksUrl }).verify(await sign(claims()));
  assert.equal(c.email, 'p25me@iima.ac.in');
  assert.ok(hasRole(c, 'admin'));
  assert.ok(!hasRole(c, 'writer'));
});

test('rejects bad tokens', async () => {
  const v = verifier('betamarkets', { jwksUrl });
  const cases: Record<string, Promise<string> | string> = {
    'wrong aud': sign(claims({ aud: 'portfolioleague' })),
    'aud array': sign(claims({ aud: ['betamarkets', 'x'] })),
    'expired': sign(claims({ exp: now() - 120, iat: now() - 180 })),
    'from the future': sign(claims({ iat: now() + 600, exp: now() + 900 })),
    'wrong issuer': sign(claims({ iss: 'evil.example' })),
    'wrong version': sign(claims({ v: 2 })),
    'no email': sign(claims({ email: '' })),
    'no kid': sign(claims(), { kid: null }),
    'unknown kid': sign(claims(), { kid: 'k9' }),
    'other key': sign(claims(), { k: other }),
    'none alg': 'eyJhbGciOiJub25lIiwia2lkIjoiazEifQ.' + Buffer.from(JSON.stringify(claims())).toString('base64url') + '.',
    'garbage': 'not.a.token',
  };
  for (const [name, tok] of Object.entries(cases)) {
    await assert.rejects(v.verify(await tok), TokenError, name);
  }
});

test('HS256 signed with the public key is refused', async () => {
  const pub = (await (await fetch(jwksUrl)).json()).keys[0].x as string;
  const tok = await new SignJWT(claims()).setProtectedHeader({ alg: 'HS256', kid: 'k1' }).sign(Buffer.from(pub));
  await assert.rejects(verifier('betamarkets', { jwksUrl }).verify(tok), TokenError);
});

test('unknown kids do not refetch on every request', async () => {
  const v = verifier('betamarkets', { jwksUrl });
  await v.verify(await sign(claims()));
  const before = fetches;
  for (let i = 0; i < 5; i++) await assert.rejects(v.verify(await sign(claims(), { kid: 'k' + i + 'x' })));
  assert.ok(fetches - before <= 1, `fetched ${fetches - before} times`);
});

test('getUser: signed out, good, rejected', async () => {
  delete process.env.IIMA_DEV_USER;
  const h = (t?: string) => new Headers(t ? { 'x-user-token': t } : {});
  assert.equal(await getUser('betamarkets', h(), { jwksUrl }), null);
  assert.equal((await getUser('betamarkets', h(await sign(claims())), { jwksUrl }))?.sub, 'u_1');
  assert.equal(await getUser('betamarkets', h(await sign(claims({ aud: 'x' }))), { jwksUrl }), null);
});

test('dev user only without a token and off the cluster', async () => {
  process.env.IIMA_DEV_USER = ' Me@IIMA.ac.in ';
  process.env.IIMA_DEV_ROLES = 'admin, writer';
  try {
    assert.deepEqual(devClaims('x')?.roles, ['admin', 'writer']);
    assert.equal(devClaims('x')?.email, 'me@iima.ac.in');
    // A rejected token is never replaced by the dev user.
    const bad = new Headers({ 'x-user-token': await sign(claims({ aud: 'other' })) });
    assert.equal(await getUser('betamarkets', bad, { jwksUrl }), null);
    process.env.NOMAD_ALLOC_ID = 'a1';
    assert.equal(devClaims('x'), null);
  } finally {
    delete process.env.IIMA_DEV_USER;
    delete process.env.IIMA_DEV_ROLES;
    delete process.env.NOMAD_ALLOC_ID;
  }
});

test('sameOrigin', () => {
  const h = (o: Record<string, string>) => new Headers(o);
  assert.ok(sameOrigin(h({ host: 'students.iima.ac.in', origin: 'https://students.iima.ac.in' })));
  assert.ok(!sameOrigin(h({ host: 'students.iima.ac.in', origin: 'https://evil.example' })));
  assert.ok(sameOrigin(h({ 'sec-fetch-site': 'same-origin' })));
  assert.ok(!sameOrigin(h({ 'sec-fetch-site': 'cross-site' })));
  assert.ok(sameOrigin(h({})));
});

test('the CSP matches the gateway and adds the nonce', async () => {
  const { readFile } = await import('node:fs/promises');
  const yml = await readFile(new URL('../../deploy/traefik/dynamic/platform.yml', import.meta.url), 'utf8');
  const m = yml.match(/contentSecurityPolicy: "([^"]+)"/);
  assert.equal(m?.[1], GATEWAY_CSP);
  const csp = contentSecurityPolicy('abc');
  assert.match(csp, /script-src 'self' 'nonce-abc' 'strict-dynamic';/);
  assert.doesNotMatch(csp, /unsafe-eval/);
  assert.match(contentSecurityPolicy('abc', { dev: true }), /'unsafe-eval'/);
  assert.notEqual(newNonce(), newNonce());
});
