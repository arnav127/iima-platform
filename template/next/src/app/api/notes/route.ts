import { NextRequest, NextResponse } from "next/server";
import { userPb } from "@/lib/server/pb";
import { requireUser } from "@/lib/server/session";

// An example of the pattern: the person comes from the platform token, and PocketBase is used as
// them (impersonation), so the notes collection's API rules decide what they can see and change.

export async function GET(req: NextRequest) {
  const s = await requireUser(req);
  if (s instanceof NextResponse) return s;
  const pb = await userPb(s.userId);
  const notes = await pb.collection("notes").getFullList({ sort: "-created" });
  return NextResponse.json({ notes });
}

export async function POST(req: NextRequest) {
  const s = await requireUser(req);
  if (s instanceof NextResponse) return s;
  const { text } = (await req.json().catch(() => ({}))) as { text?: unknown };
  if (typeof text !== "string" || !text.trim() || text.length > 2000) {
    return NextResponse.json({ message: "Write something (up to 2000 characters)." }, { status: 400 });
  }
  const pb = await userPb(s.userId);
  const note = await pb.collection("notes").create({ text: text.trim(), owner: s.userId });
  return NextResponse.json({ note });
}
