// After Check-out Access: types and pure helpers for /edge/v1/after-checkout-access (contract:
// docs/architecture/ONEGATE_AFTER_CHECKOUT_ACCESS.md section 6). One policy, one grant, one place.
// Bounds come from the API (`bounds`); DEFAULT_BOUNDS only seeds the form before/if the API omits them.

export type AccessSource = "PUBLISHED" | "EMERGENCY_FALLBACK" | "DISABLED";

export type AccessPolicy = {
  duration_hours: number;
  duration_seconds: number;
  down_kbps: number;
  up_kbps: number;
  data_quota_bytes: number;
  device_limit: number;
  device_policy: string;
};

export type Bound = { min: number; max: number; default: number; unit: string };

export type AccessBounds = {
  duration_hours: Bound;
  down_kbps: Bound;
  up_kbps: Bound;
  data_quota_bytes: Bound;
  device_limit: Bound;
};

export type AccessState = {
  enabled: boolean;
  configured: boolean;
  config_version: number;
  policy: AccessPolicy | null;
  effective?: { source: AccessSource; policy?: AccessPolicy };
  bounds?: Partial<AccessBounds>;
  supported_device_policies?: string[];
  emergency_history?: { count: number; last_at?: string };
  eligibility?: string;
  sign_in?: string;
};

/** A publication snapshot. Terms keep the ledger's historical key names; a switch change carries `change`. */
export type AccessSnapshot = {
  grace_duration_seconds?: number;
  grace_down_kbps?: number;
  grace_up_kbps?: number;
  grace_data_quota_bytes?: number;
  grace_device_limit?: number;
  grace_device_limit_policy?: string;
  change?: string;
  after_checkout_enabled?: boolean;
};

export type AccessHistoryItem = {
  config_version: number;
  published_at: string;
  actor: string;
  reason_code: string;
  policy: AccessSnapshot | null;
};

export type AccessHistoryResp = { data?: AccessHistoryItem[]; available?: boolean };

export const MB = 1024 * 1024;
const GB = 1024 * MB;

export const DEFAULT_BOUNDS: AccessBounds = {
  duration_hours: { min: 1, max: 72, default: 12, unit: "hours" },
  down_kbps: { min: 64, max: 1_000_000, default: 5000, unit: "kbps" },
  up_kbps: { min: 64, max: 1_000_000, default: 2000, unit: "kbps" },
  data_quota_bytes: { min: 10 * MB, max: 100 * GB, default: GB, unit: "bytes" },
  device_limit: { min: 1, max: 20, default: 5, unit: "devices" },
};

export function boundsOf(s?: AccessState | null): AccessBounds {
  const b = s?.bounds ?? {};
  return {
    duration_hours: b.duration_hours ?? DEFAULT_BOUNDS.duration_hours,
    down_kbps: b.down_kbps ?? DEFAULT_BOUNDS.down_kbps,
    up_kbps: b.up_kbps ?? DEFAULT_BOUNDS.up_kbps,
    data_quota_bytes: b.data_quota_bytes ?? DEFAULT_BOUNDS.data_quota_bytes,
    device_limit: b.device_limit ?? DEFAULT_BOUNDS.device_limit,
  };
}

function trim1(n: number): string {
  const s = n.toFixed(1);
  return s.endsWith(".0") ? s.slice(0, -2) : s;
}

export function fmtData(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "no data";
  if (bytes >= GB) return trim1(bytes / GB) + " GB";
  if (bytes >= MB) return Math.round(bytes / MB) + " MB";
  return Math.max(1, Math.round(bytes / 1024)) + " KB";
}

export function fmtSpeed(kbps: number): string {
  if (!Number.isFinite(kbps) || kbps <= 0) return "no speed";
  return kbps >= 1000 ? trim1(kbps / 1000) + " Mbps" : kbps + " kbps";
}

/** "12 hours", "1 hour", "90 minutes" (fractional hours exist only on a policy published before the whole-hour rule). */
export function fmtHours(hours: number): string {
  if (!Number.isFinite(hours) || hours <= 0) return "no time";
  if (hours < 1) return Math.round(hours * 60) + " minutes";
  const h = Number.isInteger(hours) ? String(hours) : trim1(hours);
  return h === "1" ? "1 hour" : h + " hours";
}

export function fmtWhen(iso: string): string {
  try {
    return new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
  } catch {
    return iso;
  }
}

export const DEVICE_POLICY_TEXT: Record<string, { title: string; description: string }> = {
  REJECT_NEW_DEVICE: {
    title: "Refuse new devices",
    description:
      "Only devices that were already authorised on the stay before check-out can reconnect. A device that is new " +
      "to the stay is refused.",
  },
};

function humanise(code: string): string {
  const s = code.replace(/_/g, " ").toLowerCase().trim();
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : "";
}

export function devicePolicyTitle(p?: string): string {
  if (!p) return "—";
  return DEVICE_POLICY_TEXT[p]?.title ?? humanise(p);
}

export function sourceWords(source: AccessSource | undefined): { label: string; tone: "ok" | "warn" | "neutral"; hint: string } {
  switch (source) {
    case "PUBLISHED":
      return { label: "Site policy", tone: "ok", hint: "Terms this site published" };
    case "EMERGENCY_FALLBACK":
      return { label: "Emergency fallback", tone: "warn", hint: "Built-in default, not chosen for this site" };
    case "DISABLED":
      return { label: "Disabled", tone: "neutral", hint: "Nobody receives access after check-out" };
    default:
      return { label: "Unknown", tone: "neutral", hint: "" };
  }
}

/** What a departing guest receives, from the terms alone. */
export function guestReceivesSentence(p: AccessPolicy): string {
  return (
    `Every device that was authorised on the stay before check-out can reconnect for ${fmtHours(p.duration_hours)} ` +
    `after the verified check-out, at up to ${fmtSpeed(p.down_kbps)} down and ${fmtSpeed(p.up_kbps)} up. All of ` +
    `those devices share ${fmtData(p.data_quota_bytes)} of data in total, and up to ${p.device_limit} ` +
    `device${p.device_limit === 1 ? "" : "s"} can be connected at once. The guest types only the room number and the ` +
    `reservation number. New devices are refused.`
  );
}

/** The terms in the ledger's snapshot shape, for comparing history entries. */
export type TermKey = "hours" | "down" | "up" | "quota" | "limit" | "policy" | "enabled";
export type TermChange = { key: TermKey; label: string; from: string; to: string; changed: boolean };

export function snapshotOfPolicy(p: AccessPolicy | null | undefined, enabled?: boolean): AccessSnapshot | null {
  if (!p) return null;
  return {
    grace_duration_seconds: p.duration_seconds,
    grace_down_kbps: p.down_kbps,
    grace_up_kbps: p.up_kbps,
    grace_data_quota_bytes: p.data_quota_bytes,
    grace_device_limit: p.device_limit,
    grace_device_limit_policy: p.device_policy,
    after_checkout_enabled: enabled,
  };
}

const hoursOf = (s: AccessSnapshot | null | undefined) =>
  s?.grace_duration_seconds === undefined ? undefined : s.grace_duration_seconds / 3600;

const FIELDS: { key: TermKey; label: string; get: (s: AccessSnapshot) => string | undefined }[] = [
  { key: "hours", label: "Duration after check-out", get: (s) => (hoursOf(s) === undefined ? undefined : fmtHours(hoursOf(s)!)) },
  { key: "down", label: "Download speed", get: (s) => (s.grace_down_kbps === undefined ? undefined : fmtSpeed(s.grace_down_kbps)) },
  { key: "up", label: "Upload speed", get: (s) => (s.grace_up_kbps === undefined ? undefined : fmtSpeed(s.grace_up_kbps)) },
  { key: "quota", label: "Shared data quota", get: (s) => (s.grace_data_quota_bytes === undefined ? undefined : fmtData(s.grace_data_quota_bytes)) },
  { key: "limit", label: "Device allowance", get: (s) => (s.grace_device_limit === undefined ? undefined : String(s.grace_device_limit)) },
  { key: "policy", label: "New devices", get: (s) => (s.grace_device_limit_policy === undefined ? undefined : devicePolicyTitle(s.grace_device_limit_policy)) },
];

/** True when the entry is a switch event, not a terms publication. */
export const isSwitchEvent = (h: AccessHistoryItem) => h.policy?.change === "AFTER_CHECKOUT_ENABLED";

/** Every term, old to new; `changed` where they differ. A missing side renders an em dash. */
export function compareSnapshots(prev: AccessSnapshot | null | undefined, next: AccessSnapshot | null | undefined): TermChange[] {
  return FIELDS.map(({ key, label, get }) => {
    const a = prev ? get(prev) : undefined;
    const b = next ? get(next) : undefined;
    return { key, label, from: a ?? "—", to: b ?? "—", changed: a !== b };
  });
}

/**
 * The terms in force after entry `idx` (newest first): a switch event carries no terms, so look back to the
 * newest older entry that does.
 */
export function termsAt(history: AccessHistoryItem[], idx: number): AccessSnapshot | null {
  for (let i = idx; i < history.length; i++) {
    const h = history[i];
    if (!isSwitchEvent(h) && h.policy) return h.policy;
  }
  return null;
}

export function changeLine(history: AccessHistoryItem[], idx: number): string {
  const h = history[idx];
  if (isSwitchEvent(h)) {
    return h.policy?.after_checkout_enabled ? "After Check-out Access turned on" : "After Check-out Access turned off";
  }
  const prev = termsAt(history, idx + 1);
  const changes = compareSnapshots(prev, h.policy).filter((c) => c.changed);
  if (!prev) return `First recorded terms: ${compareSnapshots(null, h.policy).filter((c) => c.to !== "—").map((c) => c.to).slice(0, 3).join(" · ")}`;
  if (changes.length === 0) return "No term changes";
  return changes.map((c) => `${c.label} ${c.from} → ${c.to}`).join(", ");
}

/** What the server accepts: `^[A-Z][A-Z0-9_]{0,63}$`. */
export const REASON_CODE_PATTERN = /^[A-Z][A-Z0-9_]{0,63}$/;

export const REASON_CHOICES: { code: string; label: string }[] = [
  { code: "INITIAL_SETUP", label: "Setting up the site's first policy" },
  { code: "POLICY_CHANGE", label: "Planned policy change" },
  { code: "SEASON_CHANGE", label: "Seasonal change" },
  { code: "GUEST_FEEDBACK", label: "Guest feedback" },
  { code: "OPERATIONAL", label: "Operational need" },
  { code: "CORRECTION", label: "Correcting a mistake" },
];

export const OTHER_REASON = "__OTHER__";

export function normaliseReasonCode(input: string): string {
  let s = (input ?? "")
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .toUpperCase()
    .replace(/[\s\-./]+/g, "_")
    .replace(/[^A-Z0-9_]/g, "")
    .replace(/_+/g, "_");
  s = s.replace(/^[^A-Z]+/, "");
  s = s.slice(0, 64).replace(/_+$/, "");
  return REASON_CODE_PATTERN.test(s) ? s : "";
}

export const isValidReasonCode = (code: string) => REASON_CODE_PATTERN.test(code);

export function reasonLabel(code: string): string {
  if (!code) return "No reason recorded";
  if (code === "ADMIN_CONSOLE_UPDATE" || code === "HOTEL_ADMIN_UPDATE") return "Admin Console update";
  return REASON_CHOICES.find((r) => r.code === code)?.label ?? humanise(code);
}

/** Strings, so an operator can clear a field while typing without it snapping to 0. */
export type AccessDraft = {
  enabled: boolean;
  hours: string;
  downMbps: string;
  upMbps: string;
  quotaMb: string;
  deviceLimit: string;
  devicePolicy: string;
};

export type DraftField = Exclude<keyof AccessDraft, "enabled">;

export function draftFromState(s: AccessState, supported: string[]): AccessDraft {
  const b = boundsOf(s);
  const p = s.policy;
  const policy = p?.device_policy && supported.includes(p.device_policy) ? p.device_policy : supported[0] ?? "REJECT_NEW_DEVICE";
  return {
    enabled: s.enabled,
    hours: String(p ? Math.round(p.duration_hours) || b.duration_hours.default : b.duration_hours.default),
    downMbps: String((p?.down_kbps ?? b.down_kbps.default) / 1000),
    upMbps: String((p?.up_kbps ?? b.up_kbps.default) / 1000),
    quotaMb: String(Math.round((p?.data_quota_bytes ?? b.data_quota_bytes.default) / MB)),
    deviceLimit: String(p?.device_limit ?? b.device_limit.default),
    devicePolicy: policy,
  };
}

function num(s: string): number {
  const t = (s ?? "").trim();
  return t === "" ? NaN : Number(t);
}

/** The draft in the API's units: one conversion for the preview, the review and the request. */
export function draftToTerms(d: AccessDraft) {
  return {
    duration_hours: num(d.hours),
    down_kbps: Math.round(num(d.downMbps) * 1000),
    up_kbps: Math.round(num(d.upMbps) * 1000),
    data_quota_bytes: Math.round(num(d.quotaMb) * MB),
    device_limit: num(d.deviceLimit),
    device_policy: d.devicePolicy,
  };
}

/** Per-field problems against the API's own bounds. Empty object means publishable. */
export function validateDraft(d: AccessDraft, bounds: AccessBounds, supported: string[]): Partial<Record<DraftField, string>> {
  const t = draftToTerms(d);
  const e: Partial<Record<DraftField, string>> = {};
  const within = (v: number, b: Bound) => Number.isFinite(v) && v >= b.min && v <= b.max;
  const hb = bounds.duration_hours;
  if (!Number.isInteger(t.duration_hours) || !within(t.duration_hours, hb))
    e.hours = `Enter a whole number of hours between ${hb.min} and ${hb.max}.`;
  const mbps = (k: "down_kbps" | "up_kbps") => `between ${fmtSpeed(bounds[k].min)} and ${fmtSpeed(bounds[k].max)}`;
  if (!within(t.down_kbps, bounds.down_kbps)) e.downMbps = `Enter a download speed ${mbps("down_kbps")}.`;
  if (!within(t.up_kbps, bounds.up_kbps)) e.upMbps = `Enter an upload speed ${mbps("up_kbps")}.`;
  const qb = bounds.data_quota_bytes;
  if (!within(t.data_quota_bytes, qb)) e.quotaMb = `Enter a data quota between ${fmtData(qb.min)} and ${fmtData(qb.max)}.`;
  const lb = bounds.device_limit;
  if (!Number.isInteger(t.device_limit) || !within(t.device_limit, lb))
    e.deviceLimit = `Enter a whole number of devices between ${lb.min} and ${lb.max}.`;
  if (!supported.includes(d.devicePolicy)) e.devicePolicy = "Choose how new devices are handled.";
  return e;
}

export type PublishFailure = { conflict: boolean; message: string; field?: "password" | "reason" };

/** Maps the API's bounded failure codes to operator sentences. */
export function describePublishFailure(e: any): PublishFailure {
  const status: number | undefined = e?.status;
  const code: string | undefined = e?.code ?? e?.body?.error;
  const serverMsg: string = typeof e?.message === "string" ? e.message : "";
  if (status === 409 || code === "version_conflict")
    return {
      conflict: true,
      message:
        "Someone else changed this setting while you were editing. The page now shows their version; yours was not " +
        "saved. Your entries are kept, so review them and save again.",
    };
  switch (code) {
    case "reauth":
    case "reauth_required":
      return { conflict: false, field: "password", message: "Your password was not accepted. Nothing was saved." };
    case "reason_invalid":
    case "reason_required":
      return { conflict: false, field: "reason", message: "The reason was not accepted. Choose a reason and try again." };
    case "actor_invalid":
      return { conflict: false, message: "Your operator account is not allowed to change this site. Nothing was saved." };
    case "nothing_to_change":
      return { conflict: false, message: "Nothing differs from the current setting, so nothing was saved." };
    case "package_invalid":
      return { conflict: false, message: "The appliance could not apply these terms, so nothing was saved and the current setting is unchanged. Try again; if it keeps failing, report it to support." };
    case "policy_unsupported":
      return { conflict: false, message: "That way of handling new devices is not supported on this appliance." };
    case "phase2_disabled":
      return { conflict: false, message: "The package catalogue is not available on this appliance, so nothing was saved." };
    case "validation":
      return { conflict: false, message: "The appliance refused these terms: " + serverMsg };
  }
  if (status === 401) return { conflict: false, field: "password", message: "Your password was not accepted. Nothing was saved." };
  if (status === 403) return { conflict: false, message: "Your role cannot change After Check-out Access." };
  return { conflict: false, message: serverMsg ? `The change was refused: ${serverMsg}` : "The change was refused." };
}

export type AccessWarning = { id: string; tone: "warning" | "danger" | "info" | "neutral"; title: string; body: string; href?: string };

export function accessWarnings(state: AccessState, historyAvailable: boolean): AccessWarning[] {
  const out: AccessWarning[] = [];
  const src = state.effective?.source;
  if (src === "EMERGENCY_FALLBACK") {
    out.push({
      id: "emergency-in-force",
      tone: "warning",
      title: "Departing guests are on the emergency fallback",
      body:
        "After Check-out Access is on, but this site has not published its terms. Guests receive the built-in " +
        "emergency terms: a safe default, not a decision made for this site. Each use raises a critical alert.",
    });
  }
  if (src === "DISABLED") {
    out.push({
      id: "disabled",
      tone: "info",
      title: "After Check-out Access is turned off",
      body: "Nobody is given access at check-out. Guests who are already in their reconnection period keep the terms they were given.",
    });
  }
  if (!historyAvailable) {
    out.push({
      id: "history-unavailable",
      tone: "neutral",
      title: "The change history cannot be read on this appliance",
      body: "The record exists and is append-only, but this appliance's admin service cannot read it. It does not affect the setting in force.",
    });
  }
  return out;
}
