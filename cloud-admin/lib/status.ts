/*
  THE ONE PLACE A STATE BECOMES WORDS AND A COLOUR.

  ctrlapi computes every appliance and license state (docs/CENTRAL_CONTROL_PLANE.md §3); the console only
  translates. Every badge, filter chip, overview row and sentence in the console reads from here, so "Expiring"
  cannot be amber on one screen and red on the next, and no screen invents its own label.
*/

import type { Activation, ApplianceLicense, ApplianceRow, AttentionKind, Connection, LicenseState } from "./api";

export type Tone = "ok" | "warn" | "err" | "info" | "default";

export type StateInfo = {
  /** The word on a badge. */
  label: string;
  tone: Tone;
  /** One sentence for someone who does not know the system. */
  explain: string;
};

export const ACTIVATION: Record<Activation, StateInfo> = {
  waiting: {
    label: "Waiting for activation",
    tone: "info",
    explain: "The appliance has registered itself and is waiting for someone to activate it.",
  },
  activating: {
    label: "Activating",
    tone: "info",
    explain: "Activated here; the appliance picks up its license the next time it contacts Central, normally within a minute.",
  },
  activated: {
    label: "Activated",
    tone: "ok",
    explain: "Assigned to a customer and site, with its own certificate and license.",
  },
  retiring: {
    label: "Retiring",
    tone: "warn",
    explain: "Retirement is signed; waiting for the appliance to confirm. Its credentials stay valid until it does.",
  },
  // A normal retirement (decommissioned) and an emergency one (revoked) both read as Retired.
  retired: {
    label: "Retired",
    tone: "default",
    explain: "Taken out of service. It can no longer connect; only its record remains.",
  },
};

export const CONNECTION: Record<Connection, StateInfo> = {
  connected: { label: "Connected", tone: "ok", explain: "Contacted Central in the last 5 minutes." },
  recently_seen: { label: "Recently seen", tone: "default", explain: "Contacted Central in the last 24 hours." },
  offline: { label: "Offline", tone: "err", explain: "Has not contacted Central for more than a day. Clients are not affected." },
  never: { label: "Never connected", tone: "default", explain: "Has not contacted Central since it was registered." },
};

export const LICENSE: Record<LicenseState, StateInfo> = {
  none: { label: "No license", tone: "default", explain: "No license has been issued for this appliance." },
  active: { label: "Active", tone: "ok", explain: "Licensed and valid." },
  expiring: { label: "Expiring", tone: "warn", explain: "Valid, with 30 days or fewer left." },
  grace: { label: "In grace period", tone: "warn", explain: "Past its end date; still working until the grace period ends." },
  expired: { label: "Expired", tone: "err", explain: "Past its grace period. Client access stops until it is renewed." },
  suspended: { label: "Suspended", tone: "warn", explain: "Paused by an operator. Resume it to restore service." },
  revoked: { label: "Revoked", tone: "err", explain: "Permanently cancelled. Set a new license to restore service." },
};

const SUPERSEDED: StateInfo = { label: "Replaced", tone: "default", explain: "Replaced by a newer version." };

export const ACTIVATION_ORDER: Activation[] = ["waiting", "activating", "activated", "retiring", "retired"];
export const CONNECTION_ORDER: Connection[] = ["connected", "recently_seen", "offline", "never"];
export const LICENSE_ORDER: LicenseState[] = ["active", "expiring", "grace", "expired", "suspended", "revoked", "none"];

function unknown(v: string | null | undefined): StateInfo {
  return { label: sentenceCase(v), tone: "default", explain: "" };
}

export function activationInfo(v: string | null | undefined): StateInfo {
  return ACTIVATION[v as Activation] ?? unknown(v);
}
export function connectionInfo(v: string | null | undefined): StateInfo {
  return CONNECTION[v as Connection] ?? unknown(v);
}
export function licenseInfo(v: string | null | undefined): StateInfo {
  if (v === "superseded") return SUPERSEDED;
  return LICENSE[v as LicenseState] ?? unknown(v);
}

/** Customer and site records. */
export function recordStatusInfo(v: string | null | undefined): StateInfo {
  if (v === "active") return { label: "Active", tone: "ok", explain: "" };
  if (v === "archived") return { label: "Archived", tone: "default", explain: "Hidden from new activations; nothing is deleted." };
  return unknown(v);
}

/** Central sign-ins (Team, customer users). */
export function userStatusInfo(v: string | null | undefined): StateInfo {
  if (v === "active") return { label: "Active", tone: "ok", explain: "" };
  if (v === "disabled") return { label: "Disabled", tone: "err", explain: "Cannot sign in." };
  if (v === "invited") return { label: "Invited", tone: "warn", explain: "" };
  return unknown(v);
}

/** Security-alert triage status. */
export function alertStatusInfo(v: string | null | undefined): StateInfo {
  switch (v) {
    case "open": return { label: "Open", tone: "err", explain: "Blocks activation of this appliance." };
    case "investigating": return { label: "Investigating", tone: "warn", explain: "" };
    case "acknowledged": return { label: "Acknowledged", tone: "warn", explain: "" };
    case "resolved": return { label: "Resolved", tone: "ok", explain: "" };
    case "false_positive": return { label: "False positive", tone: "default", explain: "" };
    default: return unknown(v);
  }
}

/** Certificates and signing keys, read-only on Trust & keys. */
export function credentialStatusInfo(v: string | null | undefined): StateInfo {
  switch (v) {
    case "active": return { label: "Active", tone: "ok", explain: "" };
    case "verify_only": return { label: "Verify only", tone: "warn", explain: "Checks old signatures; signs nothing new." };
    case "revoked": return { label: "Revoked", tone: "err", explain: "" };
    case "expired": return { label: "Expired", tone: "err", explain: "" };
    case "superseded": return { label: "Replaced", tone: "default", explain: "" };
    case "retired": return { label: "Retired", tone: "default", explain: "" };
    default: return unknown(v);
  }
}

/** Sentence-case a machine word ("false_positive" → "False positive"). */
export function sentenceCase(s?: string | null): string {
  if (!s) return "—";
  const t = s.replace(/[_.-]+/g, " ").trim();
  return t.charAt(0).toUpperCase() + t.slice(1);
}

// ---- time, in words ----

const DAY = 86_400_000;

/** Whole days from now until `iso` (negative when past). */
export function daysUntil(iso: string | null | undefined, now: number = Date.now()): number | null {
  if (!iso) return null;
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return null;
  return Math.ceil((t - now) / DAY);
}

export function formatDay(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return "—";
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}

// A licence ends at the END of a calendar day, sent as 23:59:59Z. Shown in the viewer's own time zone that
// day would read as the next one east of UTC ("Dec 2" for a licence chosen to end on Dec 1), so licence dates
// are shown as the UTC calendar day they were issued for.
export function formatLicenseDay(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return "—";
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
}

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return "—";
  return d.toLocaleString(undefined, { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" });
}

/** "just now", "12 min ago", "3 h ago", "2 days ago". */
export function ago(iso: string | null | undefined, now: number = Date.now()): string {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return "—";
  const diff = now - t;
  if (diff < 0) return formatDateTime(iso);
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins} min ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs} h ago`;
  const days = Math.floor(hrs / 24);
  return days === 1 ? "1 day ago" : `${days} days ago`;
}

function inDays(n: number): string {
  if (n === 0) return "today";
  if (n === 1) return "tomorrow";
  return `in ${n} days`;
}

/** "Connected", "Last seen 3 h ago", "Offline · last seen 4 days ago", "Never connected". */
export function connectionSentence(a: Pick<ApplianceRow, "connection" | "last_seen_at">, now: number = Date.now()): string {
  switch (a.connection) {
    case "connected": return "Connected";
    case "recently_seen": return `Last seen ${ago(a.last_seen_at, now)}`;
    case "offline": return a.last_seen_at ? `Offline · last seen ${ago(a.last_seen_at, now)}` : "Offline";
    case "never": return "Never connected";
    default: return connectionInfo(a.connection).label;
  }
}

/** The one line under a license badge: "Expires in 12 days", "Valid until 3 Mar 2027", "Grace ends in 5 days". */
export function licenseSentence(l: ApplianceLicense | null | undefined, now: number = Date.now()): string {
  if (!l || l.state === "none") return "No license issued";
  const left = daysUntil(l.valid_until, now);
  switch (l.state) {
    case "active": return `Valid until ${formatLicenseDay(l.valid_until)}`;
    case "expiring": return left === null ? "Expiring soon" : `Expires ${inDays(Math.max(0, left))}`;
    case "grace": {
      const g = daysUntil(l.grace_ends_at, now);
      return g === null ? "In grace period" : `Grace period ends ${inDays(Math.max(0, g))}`;
    }
    case "expired": return l.grace_ends_at ? `Expired ${ago(l.grace_ends_at, now)}` : "Expired";
    case "suspended": return "Suspended by an operator";
    case "revoked": return "Revoked";
    default: return licenseInfo(l.state).label;
  }
}

// ---- needs attention ----

export type AttentionInfo = {
  /** What is wrong, in words. */
  title: string;
  tone: Tone;
  /** The action button for someone who can act, and for someone who can only look. */
  action: string;
  viewAction: string;
};

export const ATTENTION: Record<AttentionKind, AttentionInfo> = {
  waiting_activation: { title: "Waiting for activation", tone: "info", action: "Activate", viewAction: "View" },
  license_expiring: { title: "License expiring", tone: "warn", action: "Renew", viewAction: "View" },
  license_grace: { title: "License in grace period", tone: "warn", action: "Renew", viewAction: "View" },
  license_expired: { title: "License expired", tone: "err", action: "Renew", viewAction: "View" },
  license_suspended: { title: "License suspended", tone: "warn", action: "Review", viewAction: "View" },
  appliance_offline: { title: "Appliance offline", tone: "err", action: "View", viewAction: "View" },
  security_alert: { title: "Security alert", tone: "err", action: "Review", viewAction: "View" },
  retirement_unconfirmed: { title: "Retirement not confirmed", tone: "warn", action: "Review", viewAction: "View" },
};

export function attentionInfo(kind: string): AttentionInfo {
  return ATTENTION[kind as AttentionKind] ?? { title: sentenceCase(kind), tone: "default", action: "View", viewAction: "View" };
}

// ---- audit actions, in words ----

const ACTION_WORDS: Record<string, string> = {
  "appliance.registered": "Appliance registered",
  "appliance.activated": "Appliance activated",
  "appliance.moved": "Appliance moved",
  "appliance.retire_started": "Retirement started",
  "appliance.retired": "Appliance retired",
  "appliance.retired_emergency": "Appliance retired without waiting",
  "appliance.retire_acknowledged": "Retirement confirmed by the appliance",
  "appliance.terminal_adopted": "Retirement confirmed by the appliance",
  "appliance.terminal_delivery_failed": "Retirement not confirmed",
  "appliance.credentials_revoked": "Credentials revoked",
  "appliance.deleted": "Appliance record deleted",
  "appliance.replace_marked": "Marked for replacement",
  "appliance.replacement_started": "Marked for replacement",
  "appliance.replacement_completed": "Replaced by a new appliance",
  "appliance.replacement_window_expired": "Replacement not completed in time",
  "appliance.wan_mac_rebound": "WAN MAC rebound",
  "appliance.certificate_reissued": "Certificate reissued",
  "license.issued": "License issued",
  "license.renewed": "License renewed",
  "license.changed": "License changed",
  "license.suspended": "License suspended",
  "license.resumed": "License resumed",
  "license.revoked": "License revoked",
  "customer.created": "Customer created",
  "customer.updated": "Customer renamed",
  "customer.renamed": "Customer renamed",
  "customer.archived": "Customer archived",
  "customer.restored": "Customer restored",
  "customer.deleted": "Customer deleted",
  "site.created": "Site created",
  "site.updated": "Site edited",
  "site.archived": "Site archived",
  "site.restored": "Site restored",
  "site.deleted": "Site deleted",
};

/** "license.suspended" → "License suspended". Unknown actions are sentence-cased, never hidden. */
export function actionWords(action: string): string {
  return ACTION_WORDS[action] ?? sentenceCase(action);
}

/** Tone of an audit action, from its verb. */
export function actionTone(action: string): Tone {
  const verb = action.split(".").pop() ?? "";
  if (/deleted|revoked|retired|failed/.test(verb)) return "err";
  if (/suspended|archived|disabled|moved|rebound|replace|retire|expired/.test(verb)) return "warn";
  if (/created|issued|activated|renewed|resumed|restored|adopted|acknowledged/.test(verb)) return "ok";
  return "default";
}
