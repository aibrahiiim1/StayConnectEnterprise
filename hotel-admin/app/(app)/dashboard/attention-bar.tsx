"use client";

// Everything that needs a person, in one slim bar. A single line says how many things need attention and names the
// first; opening it lists every item on one line each with the screen that fixes it. Nothing here takes more room than
// a toolbar, so the page's measurements stay the first thing on it.

import Link from "next/link";
import { AlertTriangle, CheckCircle2, ChevronDown, Info, OctagonAlert } from "lucide-react";
import { cn } from "@/lib/utils";
import { Explain } from "@/components/ui/tooltip";

export type AttentionRow = { text: string; detail?: string; href?: string; action?: string; tone: "warn" | "err" };
export type NoteRow = { text: string; href: string };

export function AttentionBar({
  rows, notes, ready,
}: {
  rows: AttentionRow[];
  notes: NoteRow[];
  /** False until the first answers are in, so the bar never claims "all normal" before it knows. */
  ready: boolean;
}) {
  if (rows.length === 0 && notes.length === 0) {
    if (!ready) return null;
    return (
      <div
        className="flex h-10 items-center gap-2 rounded-lg border border-border bg-card px-3.5 text-sm text-muted-foreground shadow-card"
        data-testid="all-normal"
      >
        <CheckCircle2 className="size-4 text-success" aria-hidden />
        All systems normal — nothing needs attention right now.
      </div>
    );
  }
  const err = rows.some((r) => r.tone === "err");
  const Icon = rows.length === 0 ? Info : err ? OctagonAlert : AlertTriangle;
  const tone = rows.length === 0 ? "text-muted-foreground" : err ? "text-destructive" : "text-warning-subtle-foreground";
  const headline = rows.length === 0
    ? `${notes.length} note${notes.length === 1 ? "" : "s"} for information`
    : `${rows.length} need${rows.length === 1 ? "s" : ""} attention`;
  const first = rows[0]?.text ?? notes[0]?.text;
  return (
    <details
      className={cn(
        "group rounded-lg border bg-card shadow-card",
        rows.length === 0 ? "border-border" : err ? "border-destructive/40" : "border-warning/40",
      )}
      open={err}
    >
      <summary className="flex h-10 cursor-pointer list-none items-center gap-2.5 rounded-lg px-3.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <Icon className={cn("size-4 shrink-0", tone)} aria-hidden />
        <span className="font-medium">{headline}</span>
        {rows.length > 0 && notes.length > 0 && (
          <span className="text-muted-foreground">· {notes.length} note{notes.length === 1 ? "" : "s"}</span>
        )}
        <span className="min-w-0 flex-1 truncate text-muted-foreground group-open:invisible">{first}</span>
        <ChevronDown className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" aria-hidden />
      </summary>
      <ul className="divide-y divide-border border-t border-border" data-testid="attention">
        {rows.map((a, i) => (
          <li key={`a${i}`} className="flex min-h-9 items-center gap-2.5 px-3.5 py-1.5 text-sm">
            <span className={cn("size-2 shrink-0 rounded-full", a.tone === "err" ? "bg-destructive" : "bg-warning")} aria-hidden />
            <span className="min-w-0 flex-1 truncate font-medium">{a.text}</span>
            {a.detail && <Explain>{a.detail}</Explain>}
            {a.href && (
              <Link href={a.href} className="shrink-0 font-medium text-primary underline-offset-2 hover:underline">
                {a.action ?? "Open"}
              </Link>
            )}
          </li>
        ))}
        {notes.map((n, i) => (
          <li key={`n${i}`} className="flex min-h-9 items-center gap-2.5 px-3.5 py-1.5 text-sm text-muted-foreground">
            <Info className="size-3.5 shrink-0" aria-hidden />
            <span className="min-w-0 flex-1">{n.text}</span>
            <Link href={n.href} className="shrink-0 font-medium text-primary underline-offset-2 hover:underline">See details</Link>
          </li>
        ))}
      </ul>
    </details>
  );
}
