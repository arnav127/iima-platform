import { readFileSync, realpathSync } from "node:fs";
import path from "node:path";
import type { NextConfig } from "next";

// The app's path on students.iima.ac.in is Next's basePath. It is read from app.toml, so it lives in
// one place: the portal routes that path here, and Next serves under it.
const toml = readFileSync(path.join(process.cwd(), "app.toml"), "utf8");
const appPath = /^path\s*=\s*"([^"]*)"/m.exec(toml)?.[1] ?? "/";

// @iima/kit is linked from ../iima-platform until it is published, so the build root is the folder
// holding both this app and the kit.
function commonRoot(a: string, b: string): string {
  const pa = a.split(path.sep), pb = b.split(path.sep);
  let i = 0;
  while (i < pa.length && i < pb.length && pa[i].toLowerCase() === pb[i].toLowerCase()) i++;
  return pa.slice(0, i).join(path.sep) || path.sep;
}
const kit = realpathSync(path.join(process.cwd(), "node_modules", "@iima", "kit"));
const root = commonRoot(realpathSync(process.cwd()), kit);

const nextConfig: NextConfig = {
  basePath: appPath === "/" ? "" : appPath,
  output: "standalone", // the platform runs .next/standalone/server.js
  transpilePackages: ["@iima/kit"],
  outputFileTracingRoot: root,
  turbopack: { root },
  env: { NEXT_PUBLIC_BASE_PATH: appPath === "/" ? "" : appPath },
};

export default nextConfig;
