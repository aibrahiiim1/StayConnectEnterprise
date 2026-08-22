// Operator-facing units.
//
// The domain stores kbps, bytes and seconds because those are what the enforcement layer measures in. A hotel
// operator does not think in any of them: they think "50 Mbps", "5 GB", "24 hours". Every screen that made
// the operator do that conversion in their head was also a screen where a slipped zero silently published a
// plan ten times faster or a quota ten times smaller than intended — and plan revisions are immutable, so the
// mistake is permanent and has to be superseded rather than edited.
//
// So the conversion lives here, once, in both directions. Forms collect operator units and convert on submit;
// tables convert back on display. The wire contract is untouched.

/** kbps → a short human string. 0/undefined means "not limited", which is a real, chosen value. */
export function formatSpeed(kbps?: number | null): string {
  if (kbps === null || kbps === undefined) return "Unlimited";
  if (kbps <= 0) return "Unlimited";
  if (kbps >= 1000) {
    const mbps = kbps / 1000;
    // 50.0 reads worse than 50; 1.5 must keep its decimal.
    return `${Number.isInteger(mbps) ? mbps : mbps.toFixed(1)} Mbps`;
  }
  return `${kbps} kbps`;
}

/** Mbps (as typed by an operator, possibly fractional) → kbps for the wire. */
export function mbpsToKbps(mbps: string | number | undefined | null): number | undefined {
  if (mbps === "" || mbps === null || mbps === undefined) return undefined;
  const n = Number(mbps);
  if (!Number.isFinite(n) || n < 0) return undefined;
  return Math.round(n * 1000);
}

/** kbps → Mbps for pre-filling a form field. */
export function kbpsToMbps(kbps?: number | null): string {
  if (kbps === null || kbps === undefined || kbps <= 0) return "";
  return String(kbps / 1000);
}

/** bytes → GB/MB. Decimal GB (10^9), which is what a data allowance is sold in. */
export function formatData(bytes?: number | null): string {
  if (bytes === null || bytes === undefined || bytes <= 0) return "Unlimited";
  const gb = bytes / 1_000_000_000;
  if (gb >= 1) return `${Number.isInteger(gb) ? gb : gb.toFixed(2)} GB`;
  return `${Math.round(bytes / 1_000_000)} MB`;
}

/** GB (as typed) → bytes for the wire. */
export function gbToBytes(gb: string | number | undefined | null): number | undefined {
  if (gb === "" || gb === null || gb === undefined) return undefined;
  const n = Number(gb);
  if (!Number.isFinite(n) || n < 0) return undefined;
  return Math.round(n * 1_000_000_000);
}

export function bytesToGb(bytes?: number | null): string {
  if (bytes === null || bytes === undefined || bytes <= 0) return "";
  return String(bytes / 1_000_000_000);
}

/** seconds → the largest sensible whole unit, e.g. "24 hours", "30 minutes", "7 days". */
export function formatDuration(seconds?: number | null): string {
  if (seconds === null || seconds === undefined || seconds <= 0) return "Unlimited";
  const units: [number, string][] = [
    [86400, "day"],
    [3600, "hour"],
    [60, "minute"],
    [1, "second"],
  ];
  for (const [size, name] of units) {
    if (seconds % size === 0 && seconds >= size) {
      const n = seconds / size;
      return `${n} ${name}${n === 1 ? "" : "s"}`;
    }
  }
  // Not a whole number of any unit — report the closest sensible one rather than a bare second count.
  if (seconds >= 3600) return `${(seconds / 3600).toFixed(1)} hours`;
  if (seconds >= 60) return `${(seconds / 60).toFixed(0)} minutes`;
  return `${seconds} seconds`;
}

/** A {value, unit} duration pair from a form → seconds. */
export function durationToSeconds(
  value: string | number | undefined | null,
  unit: "minutes" | "hours" | "days",
): number | undefined {
  if (value === "" || value === null || value === undefined) return undefined;
  const n = Number(value);
  if (!Number.isFinite(n) || n < 0) return undefined;
  const mult = unit === "days" ? 86400 : unit === "hours" ? 3600 : 60;
  return Math.round(n * mult);
}

/** Minor currency units → a display string. Price 0 is FREE and says so — it is a product fact, not a zero. */
export function formatPrice(minor?: number | null, currency?: string | null): string {
  if (minor === null || minor === undefined) return "—";
  if (minor === 0) return "Free";
  const cur = (currency || "").toUpperCase();
  // Exponent 2 is the contract for the currencies this product supports; the PMS interface revision carries
  // the authoritative exponent and this display never invents a different one.
  return `${(minor / 100).toFixed(2)}${cur ? " " + cur : ""}`;
}

/** How many devices a plan allows, phrased as an operator would say it. */
export function formatDevices(n?: number | null): string {
  if (n === null || n === undefined || n <= 0) return "—";
  return `${n} device${n === 1 ? "" : "s"}`;
}

/** Device-limit policy codes → operator wording. */
export const DEVICE_LIMIT_POLICIES: Record<string, string> = {
  REJECT_NEW_DEVICE: "Refuse the new device",
  DISCONNECT_OLDEST: "Disconnect the oldest device",
  ADMIN_APPROVAL: "Ask an operator to approve",
};

/** Time-accounting modes → operator wording, with the distinction that actually matters spelled out. */
export const TIME_ACCOUNTING_MODES: Record<string, string> = {
  VALIDITY_WINDOW: "Validity window — time runs from purchase, whether or not the guest is online",
  ACTIVE_USAGE: "Active usage — time is consumed only while the guest is connected",
};
