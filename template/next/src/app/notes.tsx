"use client";

import { useEffect, useState } from "react";

interface Note {
  id: string;
  text: string;
  created: string;
}

// The browser only ever calls this app's own routes, with the platform cookie; never PocketBase.
export function Notes({ base }: { base: string }) {
  const [notes, setNotes] = useState<Note[]>([]);
  const [text, setText] = useState("");
  const [error, setError] = useState("");

  async function load() {
    const res = await fetch(`${base}/api/notes`, { cache: "no-store" });
    if (res.ok) setNotes((await res.json()).notes);
  }

  async function add(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    const res = await fetch(`${base}/api/notes`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ text }),
    });
    if (!res.ok) {
      setError((await res.json().catch(() => ({}))).message || "Could not save.");
      return;
    }
    setText("");
    load();
  }

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <section>
      <h2>Your notes</h2>
      <form onSubmit={add}>
        <input value={text} onChange={(e) => setText(e.target.value)} placeholder="Write a note" maxLength={2000} />
        <button className="btn" type="submit">Add</button>
      </form>
      {error && <p className="error">{error}</p>}
      <ul>
        {notes.map((n) => (
          <li key={n.id}>{n.text}</li>
        ))}
      </ul>
    </section>
  );
}
