// BYTES, AS A HOTEL READS THEM.
//
// An operator in a dispute is holding a guest's phone showing "you said 5 GB". Reporting 5368709120 to them
// is technically exact and practically useless, and reporting "5.4 GB" when the quota was written as 5 GB
// starts a second argument. So: binary units, because that is what the quota was expressed in, one decimal
// place, and no rounding that could make a figure look like it crossed a threshold it did not.

const UNITS = ["B", "KB", "MB", "GB", "TB"] as const;

export function formatBytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  if (n === 0) return "0 B";
  let v = Math.abs(n);
  let i = 0;
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024;
    i++;
  }
  // Whole numbers below a kilobyte; one decimal above, which is the precision a guest's own device reports.
  const s = i === 0 ? String(Math.round(v)) : v.toFixed(v >= 100 ? 0 : 1);
  return `${n < 0 ? "-" : ""}${s} ${UNITS[i]}`;
}

/** How much of an allowance has been used, or null when there was no allowance to use. */
export function quotaPercent(consumed?: number | null, quota?: number | null): number | null {
  if (!quota || quota <= 0 || consumed === null || consumed === undefined) return null;
  return Math.min(100, Math.round((consumed / quota) * 100));
}

/** WHY ACCESS ENDED, in words rather than an enum.
 *
 *  These are the codes the entitlement and session records carry. An operator reading "TIME" has to guess;
 *  reading "the time allowance ran out" they can answer the guest in the same sentence. Anything unrecognised
 *  is shown as-is rather than mapped to a guess — an unknown code is information, and inventing a friendly
 *  name for it would hide that the product learned a new one. */
const END_REASONS: Record<string, string> = {
  TIME: "The time allowance ran out",
  DATA: "The data allowance was used up",
  QUOTA: "The data allowance was used up",
  EXPIRED: "The access period ended",
  CHECKOUT: "The guest checked out",
  CHECKED_OUT: "The guest checked out",
  REVOKED: "Access was withdrawn by staff",
  DISCONNECTED: "The device disconnected",
  SUPERSEDED: "Replaced by newer access",
  TERMINATED: "Access was ended",
  IDLE: "The device went idle",
};

export function endReasonWords(code?: string | null): string | null {
  if (!code) return null;
  return END_REASONS[code.toUpperCase()] ?? code;
}
