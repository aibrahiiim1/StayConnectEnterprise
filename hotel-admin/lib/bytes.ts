// Bytes as a hotel reads them: binary units (quotas are written in them), one decimal, no rounding that crosses a
// threshold.

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

/** Why access ended, in words. Unrecognised codes are shown as-is rather than guessed. */
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
