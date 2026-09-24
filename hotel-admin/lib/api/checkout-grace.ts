// Checkout grace — types, unit conversions and the plain-language rules the Hotel Admin screen is built from.
//
// Everything here is pure so it can be tested without rendering. The server (edged `/checkout-grace`) stays the
// authority: the bounds below mirror data-plane/internal/iamv2/grace_policy_publish.go and the `grace_bounds`
// CHECK, and they only ever NARROW what the page lets an operator send, never widen it.
//
// WHAT THE FIELDS MEAN (authoritative product semantics, unchanged by this screen):
//   * Who qualifies: a stay that holds ACTIVE, valid internet access at the moment of checkout — whatever it
//     was (free, paid, included with the room). No active access at checkout ⇒ no grace. One grace per stay.
//   * grace_duration_seconds: how long the grace lasts, measured on the clock from checkout (validity window),
//     not online time.
//   * grace_down_kbps / grace_up_kbps / grace_data_quota_bytes: the speed cap and the data allowance.
//   * REJECT_NEW_DEVICE (the only supported device policy): devices already connected at checkout keep access —
//     all of them, even above the device limit — and a device that was not connected at checkout is refused
//     for the whole grace. The device limit never admits a new device; it is recorded for reporting.
//   * eligibility_window_seconds: how long after checkout the stay still counts for stay-based package rules.
//     It never removes grace from a guest who qualifies.
//   * Publishing derives a free, system grace package from these terms; the operator never picks one.

export type GraceSource = "PUBLISHED" | "EMERGENCY_FALLBACK";

/** What a guest checking out RIGHT NOW receives, whichever policy is in force. */
export type EffectiveGrace = {
  source: GraceSource;
  duration_seconds: number;
  down_kbps: number;
  up_kbps: number;
  data_quota_bytes: number;
  device_limit: number;
  device_limit_policy: string;
  eligibility_window_seconds?: number;
  config_version?: number;
  policy_version?: string;
};

export type GraceState = {
  published: boolean;
  config_version: number;
  supported_device_policies?: string[];
  effective?: EffectiveGrace;
  emergency_history?: { count: number; last_at?: string };
};

/** The policy terms in the API's own units (seconds, kbps, bytes) — the shape of a publication snapshot. */
export type GraceTerms = {
  grace_duration_seconds?: number;
  grace_down_kbps?: number;
  grace_up_kbps?: number;
  grace_data_quota_bytes?: number;
  grace_device_limit?: number;
  grace_device_limit_policy?: string;
  eligibility_window_seconds?: number;
};

export type GraceHistoryItem = {
  config_version: number;
  published_at: string;
  actor: string;
  reason_code: string;
  policy: GraceTerms | null;
};

export type GraceHistoryResp = { data?: GraceHistoryItem[]; available?: boolean };

// ---------------------------------------------------------------- bounds (server-mirrored)

export const BOUNDS = {
  durationSeconds: { min: 60, max: 604800 },
  eligibilitySeconds: { min: 60, max: 604800 },
  kbps: { min: 1, max: 10_000_000 },
  quotaBytes: { min: 1, max: 1_099_511_627_776 },
  deviceLimit: { min: 1, max: 1000 },
} as const;

export const MB = 1024 * 1024;
const GB = 1024 * MB;

// ---------------------------------------------------------------- formatting

function trim1(n: number): string {
  const s = n.toFixed(1);
  return s.endsWith(".0") ? s.slice(0, -2) : s;
}

/** Data in the units the rest of the admin uses for grace (500 MB, 1 GB, 1.5 GB). */
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

function parts(seconds: number): { d: number; h: number; m: number } {
  const total = Math.max(0, Math.round(seconds / 60));
  return { d: Math.floor(total / 1440), h: Math.floor((total % 1440) / 60), m: total % 60 };
}

/** Compact duration: "1 h", "30 min", "1 h 30 min", "7 d". */
export function fmtDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "0 min";
  if (seconds < 60) return seconds + " s";
  const { d, h, m } = parts(seconds);
  const out: string[] = [];
  if (d) out.push(d + " d");
  if (h) out.push(h + " h");
  if (m) out.push(m + " min");
  return out.join(" ");
}

/** Sentence duration: "1 hour", "30 minutes", "1 day 2 hours". */
export function fmtDurationLong(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "no time";
  if (seconds < 60) return seconds === 1 ? "1 second" : seconds + " seconds";
  const { d, h, m } = parts(seconds);
  const w = (n: number, one: string) => (n === 1 ? `1 ${one}` : `${n} ${one}s`);
  const out: string[] = [];
  if (d) out.push(w(d, "day"));
  if (h) out.push(w(h, "hour"));
  if (m) out.push(w(m, "minute"));
  return out.join(" ");
}

// ---------------------------------------------------------------- device policies

export const DEVICE_POLICY_TEXT: Record<string, { title: string; description: string }> = {
  REJECT_NEW_DEVICE: {
    title: "Keep connected devices, refuse new ones",
    description:
      "Every device that was online at checkout keeps access, even above the device limit. A device that was " +
      "not connected at checkout cannot join during grace.",
  },
};

export function devicePolicyTitle(p?: string): string {
  if (!p) return "—";
  return DEVICE_POLICY_TEXT[p]?.title ?? humanise(p);
}

function humanise(code: string): string {
  const s = code.replace(/_/g, " ").toLowerCase().trim();
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : "";
}

/** The devices fact in one line: limit first, then what the policy does. A limit of 0 (the built-in fallback)
 *  means "no extra devices", never "unlimited". */
export function devicesSummary(limit: number | undefined, policy: string | undefined): string {
  const lim =
    limit === undefined ? "—" : limit <= 0 ? "No extra devices" : `Up to ${limit} device${limit === 1 ? "" : "s"}`;
  return `${lim} · ${devicePolicyTitle(policy).toLowerCase()}`;
}

// ---------------------------------------------------------------- the sentence an operator reads

/** "What a departing guest receives", derived only from the fields. */
export function guestReceivesSentence(t: GraceTerms): string {
  const dur = t.grace_duration_seconds ?? 0;
  const quota = t.grace_data_quota_bytes ?? 0;
  let s =
    `A guest who still has internet access when they check out keeps it for ${fmtDurationLong(dur)} after ` +
    `checkout, at up to ${fmtSpeed(t.grace_down_kbps ?? 0)} down and ${fmtSpeed(t.grace_up_kbps ?? 0)} up, ` +
    (quota > 0 ? `with ${fmtData(quota)} of data.` : `with no data allowance.`);
  if (t.grace_device_limit_policy === "REJECT_NEW_DEVICE") {
    s += " Devices already connected at checkout stay online; new devices are refused.";
  } else if (t.grace_device_limit_policy) {
    s += ` Devices: ${devicePolicyTitle(t.grace_device_limit_policy).toLowerCase()}.`;
  }
  return s;
}

export function termsFromEffective(e: EffectiveGrace): GraceTerms {
  return {
    grace_duration_seconds: e.duration_seconds,
    grace_down_kbps: e.down_kbps,
    grace_up_kbps: e.up_kbps,
    grace_data_quota_bytes: e.data_quota_bytes,
    grace_device_limit: e.device_limit,
    grace_device_limit_policy: e.device_limit_policy,
    eligibility_window_seconds: e.eligibility_window_seconds,
  };
}

// ---------------------------------------------------------------- diffs

export const TERM_FIELDS: { key: keyof GraceTerms; label: string; fmt: (v: any) => string }[] = [
  { key: "grace_duration_seconds", label: "Grace time", fmt: fmtDuration },
  { key: "grace_down_kbps", label: "Download speed", fmt: fmtSpeed },
  { key: "grace_up_kbps", label: "Upload speed", fmt: fmtSpeed },
  { key: "grace_data_quota_bytes", label: "Data allowance", fmt: fmtData },
  {
    key: "grace_device_limit",
    label: "Device limit",
    fmt: (v: number) => (v <= 0 ? "No extra devices" : String(v)),
  },
  { key: "grace_device_limit_policy", label: "Device handling", fmt: devicePolicyTitle },
  { key: "eligibility_window_seconds", label: "Stay rules after checkout", fmt: fmtDuration },
];

export type TermChange = { key: keyof GraceTerms; label: string; from: string; to: string; changed: boolean };

/** Every field, old → new, with `changed` set where the value differs. A missing old value renders "—". */
export function compareTerms(prev: GraceTerms | null | undefined, next: GraceTerms): TermChange[] {
  return TERM_FIELDS.map(({ key, label, fmt }) => {
    const a = prev?.[key];
    const b = next[key];
    return {
      key,
      label,
      from: a === undefined || a === null ? "—" : fmt(a),
      to: b === undefined || b === null ? "—" : fmt(b),
      changed: a !== b,
    };
  });
}

/** Only the fields that differ between two snapshots. */
export function changedTerms(prev: GraceTerms | null | undefined, next: GraceTerms | null | undefined): TermChange[] {
  if (!next) return [];
  return compareTerms(prev, next).filter((c) => c.changed);
}

// ---------------------------------------------------------------- reason codes

/** What the server accepts: `^[A-Z][A-Z0-9_]{0,63}$` (resources_commerce.go graceReasonCode). */
export const REASON_CODE_PATTERN = /^[A-Z][A-Z0-9_]{0,63}$/;

export const REASON_CHOICES: { code: string; label: string }[] = [
  { code: "INITIAL_SETUP", label: "Setting up the hotel's first policy" },
  { code: "POLICY_CHANGE", label: "Planned policy change" },
  { code: "GUEST_FEEDBACK", label: "Guest feedback" },
  { code: "OPERATIONAL", label: "Operational need" },
  { code: "CORRECTION", label: "Correcting a mistake" },
];

export const OTHER_REASON = "__OTHER__";

/**
 * Turn anything typed into a code the server will accept, or "" if nothing usable is left.
 * "late checkout" → "LATE_CHECKOUT"; "  2nd-try!" → "ND_TRY"; "Über café" → "UBER_CAFE".
 */
export function normaliseReasonCode(input: string): string {
  let s = (input ?? "")
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .toUpperCase()
    .replace(/[\s\-./]+/g, "_")
    .replace(/[^A-Z0-9_]/g, "")
    .replace(/_+/g, "_");
  s = s.replace(/^[^A-Z]+/, ""); // must start with a letter
  s = s.slice(0, 64).replace(/_+$/, "");
  return REASON_CODE_PATTERN.test(s) ? s : "";
}

export function isValidReasonCode(code: string): boolean {
  return REASON_CODE_PATTERN.test(code);
}

export function reasonLabel(code: string): string {
  if (!code) return "No reason recorded";
  return REASON_CHOICES.find((r) => r.code === code)?.label ?? humanise(code);
}

// ---------------------------------------------------------------- the editable draft

export type DurationUnit = "min" | "h" | "d";
export const UNIT_SECONDS: Record<DurationUnit, number> = { min: 60, h: 3600, d: 86400 };

/** Strings, so an operator can clear a field while typing without it snapping to 0. */
export type GraceDraft = {
  durationValue: string;
  durationUnit: DurationUnit;
  downMbps: string;
  upMbps: string;
  allowanceMb: string;
  deviceLimit: string;
  devicePolicy: string;
  eligibilityValue: string;
  eligibilityUnit: DurationUnit;
};

export type DraftField = Exclude<keyof GraceDraft, "durationUnit" | "eligibilityUnit">;

function splitDuration(seconds: number): { value: string; unit: DurationUnit } {
  const s = Math.max(60, Math.round(seconds));
  if (s % 86400 === 0) return { value: String(s / 86400), unit: "d" };
  if (s % 3600 === 0) return { value: String(s / 3600), unit: "h" };
  return { value: String(Math.round(s / 60)), unit: "min" };
}

/** A new version starts from what is in force — never a blank form. */
export function draftFromTerms(t: GraceTerms, supportedPolicies: string[]): GraceDraft {
  const dur = splitDuration(t.grace_duration_seconds ?? 3600);
  const elig = splitDuration(t.eligibility_window_seconds ?? 86400);
  const policy =
    t.grace_device_limit_policy && supportedPolicies.includes(t.grace_device_limit_policy)
      ? t.grace_device_limit_policy
      : supportedPolicies[0] ?? "REJECT_NEW_DEVICE";
  return {
    durationValue: dur.value,
    durationUnit: dur.unit,
    downMbps: String((t.grace_down_kbps ?? 5000) / 1000),
    upMbps: String((t.grace_up_kbps ?? 2000) / 1000),
    allowanceMb: String(Math.max(1, Math.round((t.grace_data_quota_bytes ?? 500 * MB) / MB))),
    // The fallback reports 0 ("no extra devices"); a published policy needs at least 1.
    deviceLimit: String(Math.max(1, t.grace_device_limit ?? 1)),
    devicePolicy: policy,
    eligibilityValue: elig.value,
    eligibilityUnit: elig.unit,
  };
}

function num(s: string): number {
  const t = (s ?? "").trim();
  return t === "" ? NaN : Number(t);
}

/** The draft in the API's units. One conversion, used by the preview, the review and the request alike. */
export function draftToTerms(d: GraceDraft): Required<GraceTerms> {
  return {
    grace_duration_seconds: Math.round(num(d.durationValue) * UNIT_SECONDS[d.durationUnit]),
    grace_down_kbps: Math.round(num(d.downMbps) * 1000),
    grace_up_kbps: Math.round(num(d.upMbps) * 1000),
    grace_data_quota_bytes: Math.round(num(d.allowanceMb) * MB),
    grace_device_limit: num(d.deviceLimit),
    grace_device_limit_policy: d.devicePolicy,
    eligibility_window_seconds: Math.round(num(d.eligibilityValue) * UNIT_SECONDS[d.eligibilityUnit]),
  };
}

/** Per-field problems, worded for the operator. Empty object ⇒ publishable shape. */
export function validateDraft(d: GraceDraft, supportedPolicies: string[]): Partial<Record<DraftField, string>> {
  const t = draftToTerms(d);
  const e: Partial<Record<DraftField, string>> = {};
  const inRange = (v: number, b: { min: number; max: number }) => Number.isFinite(v) && v >= b.min && v <= b.max;

  if (!inRange(t.grace_duration_seconds, BOUNDS.durationSeconds))
    e.durationValue = "Enter a grace time between 1 minute and 7 days.";
  if (!inRange(t.grace_down_kbps, BOUNDS.kbps)) e.downMbps = "Enter a download speed above 0 and at most 10,000 Mbps.";
  if (!inRange(t.grace_up_kbps, BOUNDS.kbps)) e.upMbps = "Enter an upload speed above 0 and at most 10,000 Mbps.";
  if (!Number.isFinite(num(d.allowanceMb)) || num(d.allowanceMb) < 1 || !inRange(t.grace_data_quota_bytes, BOUNDS.quotaBytes))
    e.allowanceMb = "Enter a data allowance between 1 MB and 1,048,576 MB (1 TB).";
  if (!Number.isInteger(t.grace_device_limit) || !inRange(t.grace_device_limit, BOUNDS.deviceLimit))
    e.deviceLimit = "Enter a whole number of devices between 1 and 1000.";
  if (!supportedPolicies.includes(d.devicePolicy)) e.devicePolicy = "Choose how devices are handled.";
  if (!inRange(t.eligibility_window_seconds, BOUNDS.eligibilitySeconds))
    e.eligibilityValue = "Enter a window between 1 minute and 7 days.";
  return e;
}

// ---------------------------------------------------------------- publish failures in plain words

export type PublishFailure = { conflict: boolean; message: string; field?: "password" | "reason" };

/** Maps edged's bounded failure codes (putCheckoutGraceConfig / graceFailureStatus) to operator sentences. */
export function describePublishFailure(e: any): PublishFailure {
  const status: number | undefined = e?.status;
  const code: string | undefined = e?.code ?? e?.body?.error;
  const serverMsg: string = typeof e?.message === "string" ? e.message : "";
  if (status === 409 || code === "version_conflict")
    return {
      conflict: true,
      message:
        "Someone else published a newer policy while you were editing. The page now shows their policy; " +
        "yours was not published. Your changes are kept — review them against the new policy and publish again.",
    };
  switch (code) {
    case "reauth_required":
      return { conflict: false, field: "password", message: "Your password was not accepted. Nothing was published." };
    case "reason_invalid":
    case "reason_required":
      return { conflict: false, field: "reason", message: "The reason was not accepted. Choose a reason and try again." };
    case "actor_invalid":
      return {
        conflict: false,
        message: "Your operator account is not allowed to publish for this hotel. Nothing was published.",
      };
    case "package_invalid":
      return {
        conflict: false,
        message:
          "The appliance could not build a grace package that matches these terms, so nothing was published and " +
          "the policy in force is unchanged. Try again; if it keeps failing, report it to support.",
      };
    case "policy_unsupported":
      return { conflict: false, message: "That way of handling devices is not supported on this appliance." };
    case "phase2_disabled":
      return {
        conflict: false,
        message:
          "The package catalogue is not available on this appliance, so the grace package cannot be built. " +
          "Nothing was published.",
      };
    case "validation":
      if (/derived grace package/i.test(serverMsg))
        return {
          conflict: false,
          message:
            "The appliance could not build a grace package that matches these terms, so nothing was published. " +
            "Details: " + serverMsg.replace(/^.*checkout validator:\s*/i, ""),
        };
      return { conflict: false, message: "The appliance refused these terms: " + serverMsg };
  }
  if (status === 401) return { conflict: false, field: "password", message: "Your password was not accepted. Nothing was published." };
  if (status === 403) return { conflict: false, message: "Your role cannot publish the checkout grace policy." };
  return { conflict: false, message: serverMsg ? `The policy was refused: ${serverMsg}` : "The policy was refused." };
}

// ---------------------------------------------------------------- what needs the operator's attention

export type GraceWarning = {
  id: string;
  tone: "warning" | "danger" | "info" | "neutral";
  title: string;
  body: string;
  /** Where the operator can follow it up, when there is such a place. */
  href?: string;
};

/**
 * Configuration problems, derived only from what the appliance reports. Nothing here is guessed: each warning
 * names the fact it came from.
 */
export function graceWarnings(
  state: GraceState,
  history: GraceHistoryItem[],
  historyAvailable: boolean,
): GraceWarning[] {
  const out: GraceWarning[] = [];
  const e = state.effective;
  const used = state.emergency_history?.count ?? 0;
  const lastUsed = state.emergency_history?.last_at;
  const current = history.find((h) => h.config_version === state.config_version);

  if (e?.source === "EMERGENCY_FALLBACK" || !state.published) {
    out.push({
      id: "emergency-in-force",
      tone: "warning",
      title: "Departing guests are on the emergency fallback",
      body:
        "This hotel has not published a checkout grace policy, so guests who check out with active internet access " +
        "receive the built-in emergency terms. Nobody is cut off, but these terms are a safe default, not a decision " +
        "made for this hotel. Each use raises a critical alert.",
    });
  }

  if (state.published && used > 0 && lastUsed && current) {
    // The fallback is only used when the configured policy cannot be applied at checkout. A use AFTER the
    // version in force was published means that version failed at a real checkout.
    if (new Date(lastUsed).getTime() > new Date(current.published_at).getTime()) {
      out.push({
        id: "fallback-after-publish",
        tone: "danger",
        title: "The emergency fallback was used after this policy was published",
        body:
          `A guest checked out ${fmtWhen(lastUsed)} and received the emergency terms instead of version ` +
          `${state.config_version}. That happens only when the hotel policy cannot be applied at checkout. ` +
          "Check the critical alert for that checkout, then republish the policy.",
        href: "/operational-alerts",
      });
    }
  }

  if (!historyAvailable) {
    out.push({
      id: "history-unavailable",
      tone: "neutral",
      title: "Policy history cannot be read on this appliance",
      body:
        "The record of published versions exists and is append-only, but this appliance's admin service cannot read " +
        "it, so who changed the policy and when is not shown. This does not mean no policy has been published, and " +
        "it does not affect the policy in force.",
    });
  } else if (state.published && history.length > 0 && !current) {
    out.push({
      id: "version-not-in-history",
      tone: "info",
      title: `Version ${state.config_version} has no publication record`,
      body:
        "The policy in force is not in the publication history, so who set it and why is unknown. Publishing from " +
        "this page records the next version with your name and reason.",
    });
  }

  if (e && (e.duration_seconds <= 0 || e.data_quota_bytes <= 0)) {
    out.push({
      id: "zero-terms",
      tone: "danger",
      title: e.duration_seconds <= 0 ? "Grace time is zero" : "Data allowance is zero",
      body: "Departing guests would receive grace that ends immediately or carries no data. Publish corrected terms.",
    });
  }
  return out;
}

export function fmtWhen(iso: string): string {
  try {
    return new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
  } catch {
    return iso;
  }
}
