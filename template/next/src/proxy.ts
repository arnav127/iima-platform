import { NextRequest, NextResponse } from "next/server";
import { contentSecurityPolicy, newNonce } from "@iima/kit/next";

// The platform gateway leaves Content-Security-Policy to Next.js apps, because Next puts inline
// scripts in every page. Each page request gets the gateway's policy plus a fresh nonce; Next reads
// the request header and puts the nonce on its scripts (pages render per request for that).
export function proxy(request: NextRequest) {
  const csp = contentSecurityPolicy(newNonce(), { dev: process.env.NODE_ENV === "development" });
  const headers = new Headers(request.headers);
  headers.set("Content-Security-Policy", csp);
  const response = NextResponse.next({ request: { headers } });
  response.headers.set("Content-Security-Policy", csp);
  return response;
}

// Next reads this at build time, so it must be written out literally.
export const config = {
  matcher: [
    // The app's root: with a basePath the pattern below needs something after it.
    {
      source: "/",
      missing: [
        { type: "header", key: "next-router-prefetch" },
        { type: "header", key: "purpose", value: "prefetch" },
      ],
    },
    // Pages only: API routes return JSON, static files have no inline scripts.
    {
      source: "/((?!api|_next/static|_next/image|favicon.ico).*)",
      missing: [
        { type: "header", key: "next-router-prefetch" },
        { type: "header", key: "purpose", value: "prefetch" },
      ],
    },
  ],
};
