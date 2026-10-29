// PocketBase as seen from this server. It has no public route: the browser talks to this app's
// route handlers and server components, and they talk to PocketBase on loopback. On the platform,
// POCKETBASE_URL is set by the job and PB_ADMIN_EMAIL / PB_ADMIN_PASSWORD come from the app's
// secrets (PocketBase creates that superuser itself on start).
import PocketBase from "pocketbase";

const url = process.env.POCKETBASE_URL || "http://127.0.0.1:8090";

let admin: PocketBase | null = null;
let signingIn: Promise<PocketBase> | null = null;

/** A client signed in as the superuser. It bypasses every API rule: decide who may do what first. */
export async function adminPb(): Promise<PocketBase> {
  if (admin?.authStore.isValid) return admin;
  signingIn ??= (async () => {
    const email = process.env.PB_ADMIN_EMAIL;
    const password = process.env.PB_ADMIN_PASSWORD;
    if (!email || !password) throw new Error("PB_ADMIN_EMAIL and PB_ADMIN_PASSWORD must be set");
    const pb = new PocketBase(url);
    pb.autoCancellation(false);
    await pb.collection("_superusers").authWithPassword(email, password);
    admin = pb;
    return pb;
  })().finally(() => {
    signingIn = null;
  });
  return signingIn;
}

/**
 * A client acting as one user, so the collections' API rules apply to everything it does. The
 * token comes from PocketBase's impersonate API and lasts a few minutes: get one per request.
 */
export async function userPb(userId: string): Promise<PocketBase> {
  const su = await adminPb();
  const pb = await su.collection("users").impersonate(userId, 300);
  pb.autoCancellation(false);
  return pb;
}
