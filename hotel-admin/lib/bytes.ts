import { formatBytes as decimalBytes } from "./utils";

// Decimal units everywhere (1 GB = 1,000,000,000 bytes), as allowances are entered.
export function formatBytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  return decimalBytes(n);
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
  CONVERTED: "Moved to after check-out access",
  ADMIN: "Ended by staff",
  TERMINATED: "Access was ended",
  IDLE: "The device went idle",
};

export function endReasonWords(code?: string | null): string | null {
  if (!code) return null;
  return END_REASONS[code.toUpperCase()] ?? code;
}
