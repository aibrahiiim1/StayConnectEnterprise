// Operator units (Mbps, GB, hours) converted once, both ways, to the stored kbps/bytes/seconds. Plan revisions are
// immutable, so a slipped zero is permanent.

export function formatSpeed(kbps?: number | null): string {
  if (kbps === null || kbps === undefined) return "Unlimited";
  if (kbps <= 0) return "Unlimited";
  if (kbps >= 1000) {
    const mbps = kbps / 1000;
    return `${Number.isInteger(mbps) ? mbps : mbps.toFixed(1)} Mbps`;
  }
  return `${kbps} kbps`;
}

export function mbpsToKbps(mbps: string | number | undefined | null): number | undefined {
  if (mbps === "" || mbps === null || mbps === undefined) return undefined;
  const n = Number(mbps);
  if (!Number.isFinite(n) || n < 0) return undefined;
  return Math.round(n * 1000);
}

export function kbpsToMbps(kbps?: number | null): string {
  if (kbps === null || kbps === undefined || kbps <= 0) return "";
  return String(kbps / 1000);
}

export function formatData(bytes?: number | null): string {
  if (bytes === null || bytes === undefined || bytes <= 0) return "Unlimited";
  const gb = bytes / 1_000_000_000;
  if (gb >= 1) return `${Number.isInteger(gb) ? gb : gb.toFixed(2)} GB`;
  return `${Math.round(bytes / 1_000_000)} MB`;
}

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
  if (seconds >= 3600) return `${(seconds / 3600).toFixed(1)} hours`;
  if (seconds >= 60) return `${(seconds / 60).toFixed(0)} minutes`;
  return `${seconds} seconds`;
}

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

/**
 * seconds to the {value, unit} pair a duration field is pre-filled with, in the largest unit that divides exactly
 * (inverse of durationToSeconds).
 */
export function secondsToDurationField(
  seconds: number | null | undefined,
  units: readonly ("minutes" | "hours" | "days")[],
): { value: string; unit: "minutes" | "hours" | "days" } {
  const fallback = units[units.length - 1];
  if (seconds === null || seconds === undefined || seconds <= 0) return { value: "", unit: fallback };
  const size = { days: 86400, hours: 3600, minutes: 60 } as const;
  for (const u of ["days", "hours", "minutes"] as const) {
    if (units.includes(u) && seconds % size[u] === 0) return { value: String(seconds / size[u]), unit: u };
  }
  // Not a whole number of any unit: use the smallest offered one for precision.
  const smallest = units.reduce((a, b) => (size[a] <= size[b] ? a : b));
  return { value: String(+(seconds / size[smallest]).toFixed(4)), unit: smallest };
}

export function formatPrice(minor?: number | null, currency?: string | null): string {
  if (minor === null || minor === undefined) return "—";
  if (minor === 0) return "Free";
  const cur = (currency || "").toUpperCase();
  // Exponent 2 is the contract for supported currencies; the PMS interface revision carries the authoritative one.
  return `${(minor / 100).toFixed(2)}${cur ? " " + cur : ""}`;
}

export function formatDevices(n?: number | null): string {
  if (n === null || n === undefined || n <= 0) return "—";
  return `${n} device${n === 1 ? "" : "s"}`;
}

/**
 * Device-limit policy codes. ADMIN_APPROVAL stays only so older plans read correctly; it is not offered and the
 * appliance refuses it.
 */
export const DEVICE_LIMIT_POLICIES: Record<string, string> = {
  REJECT_NEW_DEVICE: "Refuse the new device",
  DISCONNECT_OLDEST: "Disconnect the oldest device",
  ADMIN_APPROVAL: "Ask an operator to approve (not available)",
};

export const SELECTABLE_DEVICE_LIMIT_POLICIES = ["REJECT_NEW_DEVICE", "DISCONNECT_OLDEST"] as const;

export const TIME_ACCOUNTING_MODES: Record<string, string> = {
  VALIDITY_WINDOW: "Validity window — time runs from purchase, whether or not the client is online",
  ACTIVE_USAGE: "Active usage — time is consumed only while the client is connected",
  AGGREGATE_ONLINE_TIME: "Online time — the time allowance is used only while the client is online",
};
