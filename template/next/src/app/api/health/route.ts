// The platform's health check (Nomad calls <path>/api/health on the app directly).
export const dynamic = "force-dynamic";

export function GET() {
  return Response.json({ ok: true });
}
