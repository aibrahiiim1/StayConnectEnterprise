// INTERNET PACKAGES, SERVICE PLANS AND PACKAGE ACTIVITY — the API shapes and the helpers every screen in this
// area shares. Kept in one file so the price formatting, the source words and the status words cannot drift
// between the Packages list, the activity view and the record sheets.

import { api } from "@/lib/api";

export type PackageSummary = {
  package_id: string; code: string; active: boolean;
  current_revision_id: string; revision_count: number;
  name?: string | null; price_minor?: number | null; currency?: string | null;
  currency_exponent?: number | null;
  package_type?: string | null;
  visible_from?: string | null; visible_until?: string | null;
  service_plan_id?: string | null; service_plan_revision_id?: string | null;
  service_plan_code?: string | null; service_plan_revision_no?: number | null;
  down_kbps?: number | null; up_kbps?: number | null;
  max_concurrent_devices?: number | null; device_limit_policy?: string | null;
  time_quota_seconds?: number | null; data_quota_bytes?: number | null;
  speed_allocation?: string | null;
  plan_has_newer_revision?: boolean;
};

export type RevisionInfo = {
  revision_id: string; revision_no: number; is_current: boolean;
  label?: string; price_minor?: number; currency?: string; currency_exponent?: number | null; package_type?: string;
};

export type DeletabilityReason = { code: string; message: string; count?: number | null };
export type Deletability = { deletable: boolean; reasons: DeletabilityReason[] };

// ------------------------------------------------------------------------------------------ money --------

/**
 * A price in minor units, formatted with the exponent it was recorded with.
 *
 * The exponent comes from the package revision (or the purchase). When the record genuinely carries none, 2 is
 * used — the exponent of every currency this product has published with — and `assumed` says so, so a screen
 * can mark it rather than present a guess as a fact. Price 0 is "Free": a product fact, not a zero.
 */
export function formatMoney(
  minor?: number | null,
  currency?: string | null,
  exponent?: number | null,
): { text: string; assumed: boolean } {
  if (minor === null || minor === undefined) return { text: "—", assumed: false };
  if (minor === 0) return { text: "Free", assumed: false };
  const assumed = exponent === null || exponent === undefined;
  const exp = assumed ? 2 : Math.max(0, Math.min(6, Math.trunc(exponent)));
  const cur = (currency || "").trim().toUpperCase();
  const value = (minor / Math.pow(10, exp)).toFixed(exp);
  return { text: `${value}${cur ? " " + cur : ""}`, assumed };
}

/** formatMoney's text, with a marker when the exponent had to be assumed. */
export function priceText(minor?: number | null, currency?: string | null, exponent?: number | null): string {
  const m = formatMoney(minor, currency, exponent);
  return m.assumed ? `${m.text} (assumed 2 decimals)` : m.text;
}

// ------------------------------------------------------------------------------------- activity ---------

export type ActivitySource =
  | "GUEST_SELECTION" | "VOUCHER_REDEMPTION" | "ACCOUNT_AUTO_GRANT" | "OTP_SOCIAL_DEFAULT" | "CHECKOUT_GRACE"
  | "EMERGENCY_GRACE" | "POST_STAY_CONVERSION" | "CROSS_PMS_TRANSFER" | "ADMIN_GRANT" | "RENEWAL";

/** The same words the server sends as source_label, for the filter menu before any row has loaded. */
export const SOURCE_LABELS: Record<ActivitySource, string> = {
  GUEST_SELECTION: "Chosen on the portal",
  VOUCHER_REDEMPTION: "Voucher",
  ACCOUNT_AUTO_GRANT: "Guest account",
  OTP_SOCIAL_DEFAULT: "Email, phone or social sign-in",
  CHECKOUT_GRACE: "After check-out grace",
  EMERGENCY_GRACE: "Emergency grace",
  POST_STAY_CONVERSION: "After-stay access",
  CROSS_PMS_TRANSFER: "Moved between property systems",
  ADMIN_GRANT: "Granted by staff",
  RENEWAL: "Renewal",
};

export type ActivityRow = {
  purchase_id: string; entitlement_id?: string;
  package_id: string; package_code: string; package_name: string; system_package?: boolean;
  package_revision_id: string; revision_no: number; package_type?: string;
  price_minor: number; currency?: string; currency_exponent?: number | null;
  source: ActivitySource | string; source_label: string; purchase_state: string;
  status: "PENDING" | "ACTIVE" | "SUSPENDED" | "TERMINATED" | "NOT_GRANTED" | string;
  end_reason?: string; emergency_grace?: boolean;
  sign_in_kind?: "STAY" | "GUEST_ACCOUNT" | "VOUCHER" | "SIGN_IN" | string;
  stay_id?: string; room?: string; pms_interface?: string; reservation?: string;
  had_offer: boolean; offer_taken_at?: string; offer_expires_at?: string;
  started_at?: string; ended_at?: string; occurred_at?: string;
  service_plan?: string; quota_bytes?: number | null;
  consumed_data_bytes?: number | null; consumed_online_seconds?: number | null;
  sessions: number; devices: number; bytes_down: number; bytes_up: number;
  first_session_at?: string; last_session_at?: string; online_now: boolean; sign_in_method?: string;
  usage_href?: string;
};

export type ActivitySummary = {
  in_range: number; started_in_range: number;
  status_counts: { active: number; ended: number; other: number };
  data_bytes: number; active_now: number; undated: number;
  by_package: { package_id: string; code: string; name: string; is_system: boolean; grants: number }[];
  by_source: { source: string; label: string; grants: number }[];
  active_by_package: { package_id: string; grants: number }[];
};

export type ActivityResponse = {
  data: ActivityRow[];
  meta: { total: number; limit: number; offset: number; has_more: boolean };
  summary: ActivitySummary;
  range: { from: string; to: string };
};

export type ActivityRange = "24h" | "7d" | "30d" | "custom";
export type ActivityStatus = "all" | "active" | "ended" | "other";

export type ActivityQuery = {
  range: ActivityRange; from?: string; to?: string;
  package_id?: string; source?: string; status?: ActivityStatus; q?: string;
  limit?: number; offset?: number;
};

export function activityPath(q: ActivityQuery): string {
  const p = new URLSearchParams();
  p.set("range", q.range);
  if (q.range === "custom") {
    if (q.from) p.set("from", q.from);
    if (q.to) p.set("to", q.to);
  }
  if (q.package_id) p.set("package_id", q.package_id);
  if (q.source) p.set("source", q.source);
  if (q.status && q.status !== "all") p.set("status", q.status);
  if (q.q?.trim()) p.set("q", q.q.trim());
  p.set("limit", String(q.limit ?? 25));
  p.set("offset", String(q.offset ?? 0));
  return `/commercial-packages/activity?${p.toString()}`;
}

export const getActivity = (q: ActivityQuery) => api.get<ActivityResponse>(activityPath(q));
export const getPackageDeletability = (id: string) =>
  api.get<Deletability>(`/commercial-packages/${id}/deletability`);
export const getPlanDeletability = (id: string) =>
  api.get<Deletability>(`/commercial-packages/plans/${id}/deletability`);
// Permanent deletion of an item nothing has ever used. A 409 carries `deletability` with the reasons.
export const deletePackage = (id: string, reason: string, password: string) =>
  api.del<{ deleted: boolean }>(`/commercial-packages/${id}`, { reason, password });
export const deletePlan = (id: string, reason: string, password: string) =>
  api.del<{ deleted: boolean }>(`/commercial-packages/plans/${id}`, { reason, password });

/** The status of one grant, in words, with the badge tone that goes with it. */
export function statusWords(status: string): { label: string; tone: "ok" | "warn" | "err" | "info" | "default" } {
  switch (status) {
    case "ACTIVE": return { label: "In use", tone: "ok" };
    case "PENDING": return { label: "Not started yet", tone: "info" };
    case "SUSPENDED": return { label: "Paused", tone: "warn" };
    case "TERMINATED": return { label: "Ended", tone: "default" };
    case "NOT_GRANTED": return { label: "No access given", tone: "err" };
    default: return { label: status.replace(/_/g, " ").toLowerCase(), tone: "default" };
  }
}

/** Who the grant was for, as far as the record honestly says — never a name, account or voucher code. */
export function whoWords(r: Pick<ActivityRow, "room" | "pms_interface" | "sign_in_kind">): string {
  if (r.room) return `Room ${r.room}`;
  switch (r.sign_in_kind) {
    case "GUEST_ACCOUNT": return "A guest-account guest";
    case "VOUCHER": return "A voucher guest";
    case "SIGN_IN": return "An email, phone or social sign-in";
    case "STAY": return "A stay with no room recorded";
    default: return "A guest";
  }
}

/** Why access ended, in words. */
export function endReasonText(code?: string | null): string | null {
  switch (code) {
    case undefined: case null: case "": return null;
    case "TIME": return "Time allowance used up";
    case "DATA": return "Data allowance used up";
    case "HARD_EXPIRY": return "Reached its end time";
    case "CHECKOUT": return "Guest checked out";
    case "ADMIN": return "Ended by staff";
    case "REVOKED": return "Revoked";
    case "SUPERSEDED": return "Replaced by a newer grant";
    case "CONVERTED": return "Converted to after-stay access";
    case "TRANSFERRED": return "Moved to another stay";
    case "CANCELLED": return "Cancelled";
    default: return code.replace(/_/g, " ").toLowerCase();
  }
}
