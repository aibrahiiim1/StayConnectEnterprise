// WHEN THE SIGN-IN ENDS, AND WHERE TO GO AFTERWARDS.
//
// Central ends a console sign-in after a period without activity and, however active the person is, a fixed time
// after it began (control-plane internal/auth/session_policy.go). whoami reports both moments; this module turns
// them into deadlines on this browser's clock, and builds the way back to the page the person was on.

import type { Whoami } from "./api";

/** Milliseconds on this browser's clock (Date.now()). null = not reported. */
export type Deadlines = { idle: number | null; absolute: number | null };

export type Ending = { at: number; kind: "idle" | "absolute" };

/** How long before the end the warning appears. */
export const WARN_BEFORE_MS = 2 * 60 * 1000;

const DEFAULT_DESTINATION = "/overview";

/**
 * The deadlines in whoami, moved onto this browser's clock: each is "so long after the server's now", counted from
 * when the answer arrived, so a browser whose clock is wrong still warns at the right moment.
 */
export function deadlinesFrom(m: Pick<Whoami, "idle_expires_at" | "absolute_expires_at" | "server_time">, receivedAt = Date.now()): Deadlines {
  const server = m.server_time ? Date.parse(m.server_time) : NaN;
  const toLocal = (iso?: string | null): number | null => {
    if (!iso) return null;
    const t = Date.parse(iso);
    if (Number.isNaN(t)) return null;
    return Number.isNaN(server) ? t : receivedAt + (t - server);
  };
  return { idle: toLocal(m.idle_expires_at), absolute: toLocal(m.absolute_expires_at) };
}

/** Equal within `toleranceMs`: two readings of the same deadline differ by the request's latency. */
export function sameDeadlines(a: Deadlines, b: Deadlines, toleranceMs = 5000): boolean {
  const eq = (x: number | null, y: number | null) => (x === null || y === null ? x === y : Math.abs(x - y) <= toleranceMs);
  return eq(a.idle, b.idle) && eq(a.absolute, b.absolute);
}

/** The deadline that comes first. On a tie the absolute one wins: it is the one that cannot be extended. */
export function nextEnding(d: Deadlines): Ending | null {
  if (d.idle === null && d.absolute === null) return null;
  if (d.absolute !== null && (d.idle === null || d.absolute <= d.idle)) return { at: d.absolute, kind: "absolute" };
  return { at: d.idle as number, kind: "idle" };
}

/**
 * The `next` address after sign-in, or null when it is not safe to follow.
 *
 * `next` arrives in the address bar, so anybody can write one. Only a same-origin, relative path is followed:
 * it starts with exactly one "/" and has no backslash (browsers read "/\evil" as "//evil"), no control character
 * (a tab or newline is dropped by the URL parser, so "/\t/evil" becomes "//evil"), and no scheme. The sign-in page
 * itself is refused, so a sign-in never lands back on the sign-in form.
 */
export function safeNext(raw: string | null | undefined): string | null {
  if (typeof raw !== "string" || raw === "") return null;
  if (!raw.startsWith("/") || raw.startsWith("//")) return null;
  if (raw.includes("\\")) return null;
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(raw)) return null;
  let url: URL;
  try {
    url = new URL(raw, "https://central.invalid");
  } catch {
    return null;
  }
  if (url.origin !== "https://central.invalid") return null;
  if (url.pathname === "/login" || url.pathname.startsWith("/login/")) return null;
  return url.pathname + url.search + url.hash;
}

/** Where to go after sign-in: `next` when it is safe, the Overview otherwise. */
export function destinationAfterSignIn(raw: string | null | undefined): string {
  return safeNext(raw) ?? DEFAULT_DESTINATION;
}

/** The sign-in page, saying why the person is there and how to get back to `path`. */
export function expiredSignInUrl(path: string | null | undefined): string {
  const q = new URLSearchParams();
  const next = safeNext(path);
  if (next) q.set("next", next);
  q.set("reason", "expired");
  return `/login?${q.toString()}`;
}
