// Central enforces both idle and absolute session limits; browser warnings use its reported deadlines.

import type { Whoami } from "./api";

/** Browser-clock milliseconds; null means the server did not report a deadline. */
export type Deadlines = { idle: number | null; absolute: number | null };

export type Ending = { at: number; kind: "idle" | "absolute" };

export const WARN_BEFORE_MS = 2 * 60 * 1000;

const DEFAULT_DESTINATION = "/overview";

/** Anchor server deadlines to response arrival so browser clock drift does not shift warnings. */
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

/** Ignore deadline differences within request latency. */
export function sameDeadlines(a: Deadlines, b: Deadlines, toleranceMs = 5000): boolean {
  const eq = (x: number | null, y: number | null) => (x === null || y === null ? x === y : Math.abs(x - y) <= toleranceMs);
  return eq(a.idle, b.idle) && eq(a.absolute, b.absolute);
}

/** On a tie, absolute expiry wins because activity cannot extend it. */
export function nextEnding(d: Deadlines): Ending | null {
  if (d.idle === null && d.absolute === null) return null;
  if (d.absolute !== null && (d.idle === null || d.absolute <= d.idle)) return { at: d.absolute, kind: "absolute" };
  return { at: d.idle as number, kind: "idle" };
}

/** Reject external redirects: browsers can reinterpret backslashes and control characters as a leading // host. */
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

export function destinationAfterSignIn(raw: string | null | undefined): string {
  return safeNext(raw) ?? DEFAULT_DESTINATION;
}

export function expiredSignInUrl(path: string | null | undefined): string {
  const q = new URLSearchParams();
  const next = safeNext(path);
  if (next) q.set("next", next);
  q.set("reason", "expired");
  return `/login?${q.toString()}`;
}
