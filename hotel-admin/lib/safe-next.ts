// WHERE THE LOGIN PAGE MAY SEND AN OPERATOR AFTER SIGNING IN (audit N-18).
//
// /login?next=... is how the middleware returns an operator to the page they asked for. It used to be passed to
// router.replace unchecked, so /login?next=https://evil.example signed the operator in and then handed them to
// another site that could imitate the console and ask for the password again. Only a path on THIS console is
// accepted: it must start with a single "/", must not start with "//" or "/\" (both are another host to a
// browser), and must not carry a scheme, a control character or a backslash anywhere. Anything else goes to
// the dashboard.

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
