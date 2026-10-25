import { useEffect, useState } from 'preact/hooks';
import * as kit from '@iima/kit';

// The platform kit knows the app's path and talks to the platform; this file adds what is
// specific to this app.
kit.setBase(import.meta.env.BASE_URL);
kit.startRouter();

export { api, ApiError, link, login, logout, navigate } from '@iima/kit';

/** Re-renders when a kit store changes. */
export function useStore<T>(s: kit.Store<T>): T {
  const [, force] = useState(0);
  const seen = s.get();
  useEffect(() => {
    const off = s.subscribe(() => force((n) => n + 1));
    if (s.get() !== seen) force((n) => n + 1); // changed between render and subscribing
    return off;
  }, [s]);
  return s.get();
}

export const routeStore = kit.routeStore;

export interface Me {
  signedIn: boolean;
  email?: string;
  name?: string;
  programme?: string;
  year?: number;
  roles?: string[];
}
