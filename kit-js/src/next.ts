// Server side of @iima/kit for Next.js (server components, route handlers, middleware) and any other
// Node server behind the gateway. The gateway puts a short-lived Ed25519 token in X-User-Token on
// every request from a signed-in user the app's policy allows; this checks it against core's public
// keys, the same way iimakit.Verifier does in Go.
//
//   import { headers } from 'next/headers';
//   const user = await getUser('betamarkets', await headers());  // server component
//   const user = await getUser('betamarkets', req.headers);      // route handler
//
// It takes any headers object, so it does not depend on a Next.js version.
import { createRemoteJWKSet, errors, jwtVerify, type JWTHeaderParameters, type JWTPayload } from 'jose';

/** The request header the gateway sets. Browsers can never set it: the gateway strips it first. */
export const TOKEN_HEADER = 'x-user-token';
/** The iss claim core puts in every token. */
export const DEFAULT_ISSUER = 'students.iima.ac.in';
/** The v claim this kit understands. */
export const CLAIMS_VERSION = 1;

/** The identity token's payload (version 1). Mirrors iimakit.Claims. */
export interface Claims {
  v: number;
  iss: string;
  aud: string; // the app id
  sub: string; // stable user id
  sid: string; // changes on every new sign-in
  iat: number;
  exp: number;
  email: string;
  name?: string;
  picture?: string;
  provider: string; // "google", or "dev"
  member: boolean; // signed in with an @iima.ac.in Google account
  affiliation?: string;
  programme?: string;
  batch?: number;
  year?: number;
  roles?: string[]; // only this app's roles
}

export interface HeaderGetter {
  get(name: string): string | null | undefined;
}

export interface VerifierOptions {
  /** Default: IIMA_JWKS_URL, then the local stack's JWKS. */
  jwksUrl?: string;
  /** Default: IIMA_ISSUER, then students.iima.ac.in. */
  issuer?: string;
  /** Allowed clock difference between servers, in seconds. Default 30. */
  leeway?: number;
}

/** Thrown by verify(). Callers should treat any failure as "signed out". */
export class TokenError extends Error {}

export interface Verifier {
  verify(token: string): Promise<Claims>;
}

/** A verifier for one app. Keys are fetched once, cached, and fetched again for an unknown key id. */
export function verifier(appId: string, opts: VerifierOptions = {}): Verifier {
  if (!appId) throw new Error('@iima/kit/next: verifier needs an app id');
  const env: Record<string, string | undefined> = typeof process !== 'undefined' ? process.env : {};
  const issuer = opts.issuer ?? (env.IIMA_ISSUER || DEFAULT_ISSUER);
  const jwksUrl = opts.jwksUrl ?? (env.IIMA_JWKS_URL || 'http://localhost:8080/auth/.well-known/jwks.json');
  // jose caches the set, refetches on an unknown kid at most every 10 s (so forged tokens cannot
  // hammer core), and refreshes keys older than an hour.
  const jwks = createRemoteJWKSet(new URL(jwksUrl), { cooldownDuration: 10_000, cacheMaxAge: 3_600_000, timeoutDuration: 5_000 });
  const keyFor = (header: JWTHeaderParameters, token: Parameters<typeof jwks>[1]) => {
    // Core always names its key. Without this jose would try a lone key in the set.
    if (!header.kid) throw new TokenError('token has no key id');
    return jwks(header, token);
  };

  return {
    async verify(token: string): Promise<Claims> {
      let payload: JWTPayload;
      try {
        // Only EdDSA: rules out "none" and HS256-with-the-public-key tricks.
        ({ payload } = await jwtVerify(token, keyFor, {
          algorithms: ['EdDSA'],
          issuer,
          audience: appId,
          clockTolerance: opts.leeway ?? 30,
          requiredClaims: ['exp', 'iat', 'sub'],
        }));
      } catch (e) {
        if (e instanceof TokenError) throw e;
        if (e instanceof errors.JOSEError) throw new TokenError(e.code);
        throw e; // core unreachable and no cached keys: let the caller see it
      }
      const c = payload as unknown as Claims;
      // Core only ever mints a single string aud; an array that happens to include us is rejected.
      if (typeof payload.aud !== 'string') throw new TokenError('aud must be a single app id');
      if (c.v !== CLAIMS_VERSION) throw new TokenError('unsupported token version');
      if (!c.sub || !c.email) throw new TokenError('token has no subject or email');
      // jose does not check iat; iimakit does.
      if (c.iat > Date.now() / 1000 + (opts.leeway ?? 30)) throw new TokenError('token issued in the future');
      return c;
    },
  };
}

const cache = new Map<string, Verifier>();

/**
 * The signed-in user, or null when signed out or the token does not check out. Rejected tokens are
 * logged, never trusted. With IIMA_DEV_USER set and no token (off the cluster only), returns a fake
 * user so the app can run without the platform.
 */
export async function getUser(appId: string, headers: HeaderGetter, opts?: VerifierOptions): Promise<Claims | null> {
  const token = headers.get(TOKEN_HEADER);
  if (!token) return devClaims(appId);
  let v = cache.get(appId);
  if (!v || opts) {
    v = verifier(appId, opts);
    if (!opts) cache.set(appId, v);
  }
  try {
    return await v.verify(token);
  } catch (e) {
    console.warn(`@iima/kit/next: rejected token: ${(e as Error).message}`);
    return null;
  }
}

/** Whether the user has the given role in this app. */
export function hasRole(user: Claims | null | undefined, role: string): boolean {
  return !!user?.roles?.includes(role);
}

/**
 * A fake user from IIMA_DEV_USER (an email) and IIMA_DEV_ROLES (comma separated). Off when
 * IIMA_DEV_USER is unset, and always on the cluster (NOMAD_ALLOC_ID set).
 */
export function devClaims(appId: string): Claims | null {
  const env: Record<string, string | undefined> = typeof process !== 'undefined' ? process.env : {};
  const email = (env.IIMA_DEV_USER || '').trim().toLowerCase();
  if (!email || env.NOMAD_ALLOC_ID) return null;
  const roles = (env.IIMA_DEV_ROLES || '').split(',').map((r) => r.trim()).filter(Boolean);
  const now = Math.floor(Date.now() / 1000);
  return {
    v: CLAIMS_VERSION, iss: DEFAULT_ISSUER, aud: appId, sub: 'dev_' + email, sid: 'dev',
    iat: now, exp: now + 3600, email, name: email.split('@')[0], provider: 'dev',
    member: email.endsWith('@iima.ac.in'), roles,
  };
}

/**
 * CSRF check for state-changing route handlers: identity rides in a cookie on a shared origin, so
 * another app's page could post here. True when the browser says the request came from this site.
 */
// Same rules as iimakit.SameOrigin. The gateway passes the public Host through.
export function sameOrigin(headers: HeaderGetter): boolean {
  const origin = headers.get('origin');
  if (origin) {
    try {
      return new URL(origin).host === headers.get('host');
    } catch {
      return false;
    }
  }
  const site = headers.get('sec-fetch-site');
  return !site || site === 'same-origin' || site === 'none';
}

/**
 * The gateway's Content-Security-Policy with a per-request nonce for Next.js's inline scripts. The
 * gateway leaves this header to Next.js apps (it cannot know the nonce), so set it in the app's
 * proxy.ts on every page request; Next adds the nonce to its own scripts when the request carries
 * the header. See the README.
 */
export function contentSecurityPolicy(nonce: string, opts: { dev?: boolean } = {}): string {
  const script = `script-src 'self' 'nonce-${nonce}' 'strict-dynamic'` + (opts.dev ? " 'unsafe-eval'" : '');
  return GATEWAY_CSP.replace("script-src 'self'", script);
}

/** A fresh random nonce for contentSecurityPolicy. */
export function newNonce(): string {
  return btoa(crypto.randomUUID());
}

// Kept in step with security-headers in deploy/traefik/dynamic/platform.yml (a test checks it).
export const GATEWAY_CSP =
  "default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'self'; base-uri 'self'; object-src 'none'; form-action 'self'";
