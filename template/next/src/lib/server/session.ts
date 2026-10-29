// Who is calling. The gateway signs people in and puts a verified token on every request; this
// checks it and finds (or creates) the person's record in PocketBase. Take the user from here, never
// from an id in the request.
import { randomBytes } from "node:crypto";
import { NextResponse } from "next/server";
import { getUser, hasRole, sameOrigin, type Claims, type HeaderGetter } from "@iima/kit/next";
import { adminPb } from "./pb";

export const APP_ID = "mynextapp"; // the id in app.toml

export interface Session {
  claims: Claims;
  userId: string; // the PocketBase users record
  admin: boolean;
}

/** The signed-in person, or null when signed out. */
export async function getSession(req: { headers: HeaderGetter }): Promise<Session | null> {
  const claims = await getUser(APP_ID, req.headers);
  if (!claims) return null;
  const pb = await adminPb();
  const email = claims.email.toLowerCase();
  const byEmail = pb.filter("email = {:email}", { email });
  let rec;
  try {
    rec = await pb.collection("users").getFirstListItem(byEmail);
  } catch (e) {
    if ((e as { status?: number }).status !== 404) throw e;
    const password = randomBytes(24).toString("base64url"); // never used: nobody signs in to PocketBase
    try {
      rec = await pb.collection("users").create({ email, emailVisibility: true, verified: true, password, passwordConfirm: password, name: claims.name || "" });
    } catch {
      rec = await pb.collection("users").getFirstListItem(byEmail); // created by a request in parallel
    }
  }
  return { claims, userId: rec.id, admin: hasRole(claims, "admin") };
}

function fail(status: number, message: string) {
  return NextResponse.json({ message }, { status });
}

/**
 * For route handlers: the session, or a response to return as is (401 signed out, 403 for a
 * state-changing request from another site; every app shares this origin's cookie).
 */
export async function requireUser(req: { method?: string; headers: HeaderGetter }): Promise<Session | NextResponse> {
  if (req.method && !["GET", "HEAD", "OPTIONS"].includes(req.method) && !sameOrigin(req.headers)) {
    return fail(403, "Cross-site request refused.");
  }
  return (await getSession(req)) ?? fail(401, "Please sign in.");
}
