import { headers } from "next/headers";
import { getSession } from "@/lib/server/session";
import { Notes } from "./notes";

const BASE = process.env.NEXT_PUBLIC_BASE_PATH || "";

// A server component: the platform user comes straight from the request's verified token.
export default async function Home() {
  const session = await getSession({ headers: await headers() });
  return (
    <main>
      <h1>My Next App</h1>
      {session ? (
        <>
          <p className="muted">
            Signed in as <b>{session.claims.email}</b>
            {session.claims.programme ? ` · ${session.claims.programme}` : ""}
            {session.admin ? " · admin" : ""} · <a href="/auth/logout">Sign out</a>
          </p>
          <Notes base={BASE} />
        </>
      ) : (
        <p>
          <a className="btn" href={`/auth/login?rd=${encodeURIComponent(BASE + "/")}`}>Sign in with your IIMA account</a>
        </p>
      )}
    </main>
  );
}
