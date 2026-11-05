// Where a certificate stands, from its expiry time.

export const DAY_MS = 86_400_000;
/** Where the certificate stands, from its expiry time: the server's whole-day count truncates toward zero, so 0 is
 * both "expired an hour ago" and "expires tomorrow". That count is used only when no expiry time is known. */
export function expiryState(expiresAt: string | undefined, daysRemaining: number | null, now: number):
  { kind: "expired" | "within_day" | "days"; days: number } | null {
  const at = expiresAt ? Date.parse(expiresAt) : NaN;
  if (!Number.isNaN(at)) {
    const left = at - now;
    if (left < 0) return { kind: "expired", days: Math.floor(-left / DAY_MS) };
    if (left < DAY_MS) return { kind: "within_day", days: 0 };
    return { kind: "days", days: Math.floor(left / DAY_MS) };
  }
  if (daysRemaining === null) return null;
  return daysRemaining < 0 ? { kind: "expired", days: -daysRemaining } : { kind: "days", days: daysRemaining };
}
