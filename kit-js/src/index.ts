// @iima/kit: the frontend side of the platform. Talks to the app's own API under its base path and
// to the platform's /auth endpoints. No framework dependency: stores expose subscribe() for hooks.

// ---------- base path ----------

/**
 * The app's public path without a trailing slash, e.g. "/auxesiscapital". Set it once at start-up
 * with setBase(import.meta.env.BASE_URL) (Vite) or your basePath (Next.js).
 */
export let BASE = '';

export function setBase(base: string) {
  BASE = base.replace(/\/+$/, '');
}

/** URL of a file shipped with the app, e.g. asset('logo.png'). */
export const asset = (file: string) => `${BASE}/${file.replace(/^\/+/, '')}`;

// ---------- API ----------

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export interface ApiInit {
  method?: string;
  /** A plain object is sent as JSON; FormData is sent as is (file uploads). */
  body?: unknown;
  signal?: AbortSignal;
}

/**
 * Calls the app's own API: api('/api/aux/me') fetches BASE + '/api/aux/me'. Identity comes from
 * the platform session cookie, which the gateway turns into a verified token, so there is no
 * token to attach. Errors become ApiError with the server's message (PocketBase's `message` or
 * the platform's `error`).
 */
export async function api<T>(path: string, init: ApiInit = {}): Promise<T> {
  const method = init.method ?? (init.body !== undefined ? 'POST' : 'GET');
  const headers: Record<string, string> = { Accept: 'application/json' };
  let body: BodyInit | undefined;
  if (init.body instanceof FormData) {
    body = init.body;
  } else if (init.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(init.body);
  }
  let res: Response;
  try {
    res = await fetch(BASE + path, { method, headers, body, credentials: 'same-origin', signal: init.signal });
  } catch (e) {
    if ((e as Error).name === 'AbortError') throw e;
    throw new ApiError(0, "You're offline. Check your connection and try again.");
  }
  const text = await res.text();
  let data: any = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    /* not JSON */
  }
  if (!res.ok) {
    throw new ApiError(res.status, data?.message || data?.error || res.statusText || 'Something went wrong');
  }
  return data as T;
}

// ---------- platform identity ----------

export interface PlatformUser {
  signedIn: boolean;
  email?: string;
  name?: string;
  picture?: string;
  provider?: string;
  /** Signed in with an @iima.ac.in Google account. */
  member?: boolean;
  /** Whether the app's login policy accepts this user (when asked with an app id). */
  allowed?: boolean;
}

/**
 * Who is signed in to the platform, from /auth/me. Pass the app id to also learn whether the
 * app's policy accepts them. Being signed in does not mean the app has them on its list: ask the
 * app's own API for that.
 */
export async function me(appID?: string): Promise<PlatformUser> {
  const res = await fetch('/auth/me' + (appID ? '?app=' + encodeURIComponent(appID) : ''), { credentials: 'same-origin', headers: { Accept: 'application/json' } });
  if (!res.ok) return { signedIn: false };
  return res.json();
}

/** The platform sign-in link that returns to `next` (an app route like '/portfolio'). */
export const loginURL = (next = '/', switchAccount = false) =>
  '/auth/login?rd=' + encodeURIComponent(BASE + next) + (switchAccount ? '&switch=1' : '');

/** Sends the browser to sign in, then back to `next`. */
export function login(next = '/', switchAccount = false) {
  location.assign(loginURL(next, switchAccount));
}

/** Signs out of the platform (every app) and returns to `next`. Logout is a POST, so this submits a form. */
export function logout(next = '/') {
  const form = document.createElement('form');
  form.method = 'POST';
  form.action = '/auth/logout';
  const rd = document.createElement('input');
  rd.type = 'hidden';
  rd.name = 'rd';
  rd.value = BASE + next;
  form.append(rd);
  document.body.append(form);
  form.submit();
}

// ---------- tiny store ----------

export interface Store<T> {
  get(): T;
  set(next: T): void;
  subscribe(listener: () => void): () => void;
}

export function createStore<T>(initial: T): Store<T> {
  let value = initial;
  const listeners = new Set<() => void>();
  return {
    get: () => value,
    set(next: T) {
      value = next;
      listeners.forEach((l) => l());
    },
    subscribe(l) {
      listeners.add(l);
      return () => {
        listeners.delete(l);
      };
    },
  };
}

/** localStorage that never throws (private mode, full quota). */
export const storage = {
  get<T>(key: string): T | null {
    try {
      const v = localStorage.getItem(key);
      return v ? (JSON.parse(v) as T) : null;
    } catch {
      return null;
    }
  },
  set(key: string, v: unknown) {
    try {
      if (v === null) localStorage.removeItem(key);
      else localStorage.setItem(key, JSON.stringify(v));
    } catch {
      /* private mode */
    }
  },
};

// ---------- router ----------

/** The current app route, without BASE: '/portfolio'. */
export const appPath = () => location.pathname.slice(BASE.length) || '/';

export const routeStore = createStore('/');

let listening = false;
/** Starts tracking back/forward navigation. Call once at start-up, after setBase. */
export function startRouter() {
  routeStore.set(appPath());
  if (listening) return;
  listening = true;
  addEventListener('popstate', () => routeStore.set(appPath()));
}

export function navigate(to: string, replace = false) {
  if (to === appPath() + location.search) return;
  if (replace) history.replaceState(null, '', BASE + to);
  else history.pushState(null, '', BASE + to);
  routeStore.set(appPath());
  window.scrollTo(0, 0);
}

/** Props for an <a> that navigates inside the app (and still opens in a new tab on cmd-click). */
export function link(to: string) {
  return {
    href: BASE + to,
    onClick: (e: MouseEvent) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      navigate(to);
    },
  };
}
