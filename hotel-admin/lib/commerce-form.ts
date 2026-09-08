// Pure, deterministic form logic for the Internet-package publish form. Kept separate from the React
// components so it is unit-testable in isolation and shared by the editors. Everything here enforces the
// currently-supported contract on the client (the edged API re-validates authoritatively): free-only,
// non-PMS eligibility, and only the duration modes this build actually implements.

// The eligibility rule types an operator may select.
//
// THE PMS ONES WERE ABSENT ON PURPOSE, AND THAT PURPOSE HAS EXPIRED. They were withheld while the engine
// recognised them but could not evaluate them — a control that silently does nothing is worse than an absent
// one. The engine now evaluates every one of them against server-pinned Stay evidence, and refuses when that
// evidence is missing rather than guessing, so withholding them only hides working behaviour.
//
// What is still absent is anything the engine cannot answer: LOYALTY_TIER has no evidence behind it, and
// STAY_NIGHTS was never a rule type — stay length is STAY_LENGTH, with min/max night bounds.
export const SUPPORTED_RULE_TYPES = [
  "AUTH_METHOD",
  "SUBJECT_KIND",
  "DATE_WINDOW",
  "PRIOR_PURCHASE",
  "SITE_NETWORK",
  "STAY_LENGTH",
  "ROOM_TYPE",
  "RATE_PLAN",
  "VIP",
  "TRAVEL_AGENT",
  "PMS_INTERFACE",
] as const;
export type RuleType = (typeof SUPPORTED_RULE_TYPES)[number];

// Rule types that must never be selectable: recognised nowhere in the engine, so a package carrying one would
// be permanently ineligible for everybody. Exported for tests.
export const FORBIDDEN_RULE_TYPES = [
  "STAY_NIGHTS",
  "LOYALTY_TIER",
] as const;

// The rule types that need authoritative Stay evidence. A guest who did not sign in through the PMS has none,
// so a package carrying any of these is not offered to them at all — which is a product decision worth
// stating on screen rather than leaving the operator to discover.
export const PMS_RULE_TYPES = [
  "STAY_LENGTH", "ROOM_TYPE", "RATE_PLAN", "VIP", "TRAVEL_AGENT", "PMS_INTERFACE",
] as const;
export function isPMSRuleType(t: string): boolean {
  return (PMS_RULE_TYPES as readonly string[]).includes(t);
}

export function isSupportedRuleType(t: string): t is RuleType {
  return (SUPPORTED_RULE_TYPES as readonly string[]).includes(t);
}

// The ONLY duration end-modes selectable here. AT_CHECKOUT / GRACE_AFTER_CHECKOUT / REST_OF_STAY /
// EARLIEST_OF_FIXED_AND_CHECKOUT (PMS/checkout-driven) are capability-disabled and absent.
export const SUPPORTED_END_MODES = ["MANUAL_END", "VALIDITY_WINDOW", "FIXED_AT"] as const;
export type EndMode = (typeof SUPPORTED_END_MODES)[number];

export type EligibilityRuleForm =
  | { type: "AUTH_METHOD"; methods: string }
  | { type: "SUBJECT_KIND"; kinds: string }
  | { type: "DATE_WINDOW"; from: string; until: string }
  | { type: "PRIOR_PURCHASE"; mode: "requires_prior" | "forbids_prior" }
  | { type: "SITE_NETWORK"; guest_network_ids: string }
  // Either bound may be left empty: "8 nights or more" and "up to 7 nights" are both real rules, and the
  // backend contract permits an open end. What it does not permit is BOTH empty, which constrains nothing.
  | { type: "STAY_LENGTH"; min_nights: string; max_nights: string }
  | { type: "ROOM_TYPE"; room_types: string }
  | { type: "RATE_PLAN"; rate_plans: string }
  | { type: "VIP"; is_vip: "true" | "false" }
  | { type: "TRAVEL_AGENT"; travel_agents: string }
  | { type: "PMS_INTERFACE"; pms_interface_ids: string };

export type GrantTierForm = { order: number | string; down_kbps?: number | string; up_kbps?: number | string };

export type DurationForm = { end_mode: EndMode; duration_seconds?: number | string; ends_at?: string };

export type PublishFormState = {
  code: string;
  name: string;
  service_plan_revision_id: string;
  rules: EligibilityRuleForm[];
  tiers: GrantTierForm[];
  duration: DurationForm;
  visible_from?: string;
  visible_until?: string;
};

const asList = (s: string): string[] =>
  s.split(",").map((x) => x.trim()).filter(Boolean);

// serializeRule maps a typed form row to the { type, value } wire shape. A forbidden/unknown type throws
// (the UI never offers one, so this is a defensive guard).
export function serializeRule(r: EligibilityRuleForm): { type: string; value: Record<string, unknown> } {
  switch (r.type) {
    case "AUTH_METHOD":
      return { type: "AUTH_METHOD", value: { methods: asList(r.methods) } };
    case "SUBJECT_KIND":
      return { type: "SUBJECT_KIND", value: { kinds: asList(r.kinds) } };
    case "DATE_WINDOW": {
      const value: Record<string, unknown> = {};
      if (r.from) value.from = new Date(r.from).toISOString();
      if (r.until) value.until = new Date(r.until).toISOString();
      return { type: "DATE_WINDOW", value };
    }
    case "PRIOR_PURCHASE":
      return { type: "PRIOR_PURCHASE", value: { [r.mode]: true } };
    case "SITE_NETWORK":
      return { type: "SITE_NETWORK", value: { guest_network_ids: asList(r.guest_network_ids) } };
    case "STAY_LENGTH": {
      // An empty bound is OMITTED, never sent as 0. {min_nights: 0} reads as "at least zero nights", which
      // is a rule that constrains nothing while looking like one that does.
      const value: Record<string, unknown> = {};
      if (r.min_nights !== "" && r.min_nights !== undefined) value.min_nights = Number(r.min_nights);
      if (r.max_nights !== "" && r.max_nights !== undefined) value.max_nights = Number(r.max_nights);
      return { type: "STAY_LENGTH", value };
    }
    case "ROOM_TYPE":
      return { type: "ROOM_TYPE", value: { room_types: asList(r.room_types) } };
    case "RATE_PLAN":
      return { type: "RATE_PLAN", value: { rate_plans: asList(r.rate_plans) } };
    case "VIP":
      return { type: "VIP", value: { is_vip: r.is_vip === "true" } };
    case "TRAVEL_AGENT":
      return { type: "TRAVEL_AGENT", value: { travel_agents: asList(r.travel_agents) } };
    case "PMS_INTERFACE":
      return { type: "PMS_INTERFACE", value: { pms_interface_ids: asList(r.pms_interface_ids) } };
    default:
      throw new Error(`unsupported rule type: ${(r as { type: string }).type}`);
  }
}

// validateDuration returns a client-side error string (or null). PMS/checkout modes are not representable
// in DurationForm, so they cannot reach here; this validates the three supported modes.
export function validateDuration(d: DurationForm): string | null {
  if (!SUPPORTED_END_MODES.includes(d.end_mode)) return "unsupported end mode";
  if (d.end_mode === "VALIDITY_WINDOW") {
    const s = Number(d.duration_seconds);
    if (!Number.isFinite(s) || s <= 0) return "validity window requires a positive duration (seconds)";
  }
  if (d.end_mode === "FIXED_AT") {
    if (!d.ends_at) return "fixed end requires an end date/time";
    const t = new Date(d.ends_at).getTime();
    if (!Number.isFinite(t)) return "invalid end date/time";
  }
  return null;
}

export function serializeDuration(d: DurationForm): Record<string, unknown> {
  const out: Record<string, unknown> = { end_mode: d.end_mode };
  if (d.end_mode === "VALIDITY_WINDOW") out.duration_seconds = Number(d.duration_seconds);
  if (d.end_mode === "FIXED_AT" && d.ends_at) out.ends_at = new Date(d.ends_at).toISOString();
  return out;
}

// validateSaleWindow enforces from < until when both are set.
export function validateSaleWindow(from?: string, until?: string): string | null {
  if (from && until && new Date(from).getTime() >= new Date(until).getTime()) {
    return "sale window start must be before end";
  }
  return null;
}

// tiersInOrder returns the tiers sorted by ascending order (deterministic), each with numeric fields.
export function tiersInOrder(tiers: GrantTierForm[]): { order: number; grant: Record<string, number> }[] {
  return tiers
    .map((t) => {
      const grant: Record<string, number> = {};
      if (t.down_kbps !== undefined && t.down_kbps !== "") grant.down_kbps = Number(t.down_kbps);
      if (t.up_kbps !== undefined && t.up_kbps !== "") grant.up_kbps = Number(t.up_kbps);
      return { order: Number(t.order), grant };
    })
    .sort((a, b) => a.order - b.order);
}

export type PublishPayload = {
  code: string;
  service_plan_revision_id: string;
  display: { name: string };
  duration_policy: Record<string, unknown>;
  eligibility_rules: { type: string; value: Record<string, unknown> }[];
  grant_tiers: { order: number; grant: Record<string, number> }[];
  visible_from?: string;
  visible_until?: string;
  /** Absent means FIXED: the pinned service plan's allowance, used unchanged. */
  data_allocation_policy?: Record<string, unknown>;
};

// buildPublishPayload validates the whole form and returns { payload } or { error }. It NEVER emits any
// price/settlement/PMS field: the published revision is free-only + NOT_REQUIRED by construction (the
// edged writer sets price_minor=0 / {NOT_REQUIRED}); the operator cannot turn it into a paid/PMS package.
export function buildPublishPayload(s: PublishFormState): { payload?: PublishPayload; error?: string } {
  if (!s.code.trim()) return { error: "code is required" };
  if (!s.service_plan_revision_id) return { error: "a service plan is required" };
  if (s.tiers.length === 0) return { error: "at least one grant tier is required" };
  const durErr = validateDuration(s.duration);
  if (durErr) return { error: durErr };
  const winErr = validateSaleWindow(s.visible_from, s.visible_until);
  if (winErr) return { error: winErr };
  for (const r of s.rules) {
    if (!isSupportedRuleType(r.type)) return { error: `unsupported rule type: ${r.type}` };
  }
  const payload: PublishPayload = {
    code: s.code.trim(),
    service_plan_revision_id: s.service_plan_revision_id,
    display: { name: s.name.trim() || s.code.trim() },
    duration_policy: serializeDuration(s.duration),
    eligibility_rules: s.rules.map(serializeRule),
    grant_tiers: tiersInOrder(s.tiers),
  };
  if (s.visible_from) payload.visible_from = new Date(s.visible_from).toISOString();
  if (s.visible_until) payload.visible_until = new Date(s.visible_until).toISOString();
  return { payload };
}


// OPERATOR WORDING for the enum values above.
//
// The form used to render the raw constants — an operator picking a duration policy chose between
// "MANUAL_END", "VALIDITY_WINDOW" and "FIXED_AT", and picking an eligibility rule chose between
// "AUTH_METHOD" and "SUBJECT_KIND". Those are the wire values and they stay the wire values; these are what
// the person reading the screen sees. Kept beside the enums so a new value cannot be added without the
// label question being asked.
export const END_MODE_LABELS: Record<EndMode, string> = {
  MANUAL_END: "Until the guest disconnects or an operator ends it",
  VALIDITY_WINDOW: "For a fixed length of time after the guest connects",
  FIXED_AT: "Until a specific date and time",
};

export const RULE_TYPE_LABELS: Record<RuleType, string> = {
  AUTH_METHOD: "How the guest signed in",
  SUBJECT_KIND: "Type of guest credential",
  DATE_WINDOW: "Only between two dates",
  PRIOR_PURCHASE: "Whether they already had a package",
  SITE_NETWORK: "Only on certain guest networks",
  STAY_LENGTH: "How many nights they are staying",
  ROOM_TYPE: "Room type",
  RATE_PLAN: "Rate plan",
  VIP: "VIP guest",
  TRAVEL_AGENT: "Travel agent",
  PMS_INTERFACE: "Which PMS the stay came from",
};
