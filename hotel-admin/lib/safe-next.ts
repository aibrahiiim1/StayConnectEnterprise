// Where the login page may send an operator after sign-in: only a path on THIS console (single leading "/", not "//" or
// "/\",
// no scheme, control character or backslash); anything else goes to the dashboard. Blocks open redirects via
// /login?next=.

export const DEFAULT_AFTER_LOGIN = "/dashboard";

export function safeNextPath(raw: string | null | undefined): string {
  if (!raw) return DEFAULT_AFTER_LOGIN;
  const next = raw.trim();
  if (!next.startsWith("/") || next.startsWith("//") || next.startsWith("/\\")) return DEFAULT_AFTER_LOGIN;
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f\\]/.test(next)) return DEFAULT_AFTER_LOGIN;
  // Resolve it the way the browser will, and insist it stays on the same origin.
  try {
    const base = "https://console.invalid";
    const u = new URL(next, base);
    if (u.origin !== base) return DEFAULT_AFTER_LOGIN;
    if (u.pathname === "/login") return DEFAULT_AFTER_LOGIN;
    return u.pathname + u.search + u.hash;
  } catch {
    return DEFAULT_AFTER_LOGIN;
  }
}

/**
 * Where the console sends an operator whose session ended: the sign-in page with `reason=expired` and a return path
 * (re-checked by safeNextPath).
 */
export function loginAfterExpiry(path: string): string {
  return `/login?next=${encodeURIComponent(safeNextPath(path))}&reason=expired`;
}
