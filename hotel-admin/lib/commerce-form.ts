// Pure, deterministic form logic for the Phase-2 commercial-packages publish form. Kept separate from
// the React components so it is unit-testable in isolation and shared by the editors. Everything here
// enforces the Phase-2 DARK constraints on the client (the edged API re-validates authoritatively):
// free-only, non-PMS, capability-disabled duration modes.

// The ONLY eligibility rule types an operator may select in Phase 2. PMS/Stay-dependent rule types
// (ROOM_TYPE, RATE_PLAN, STAY_*, etc.) are deliberately absent — they cannot be selected in the UI.
export const SUPPORTED_RULE_TYPES = [
  "AUTH_METHOD",
  "SUBJECT_KIND",
  "DATE_WINDOW",
  "PRIOR_PURCHASE",
  "SITE_NETWORK",
] as const;
export type RuleType = (typeof SUPPORTED_RULE_TYPES)[number];

// Rule types that must never be selectable in Phase 2 (PMS/Stay dependent). Exported for tests.
export const FORBIDDEN_RULE_TYPES = [
  "ROOM_TYPE",
  "RATE_PLAN",
  "STAY_STATUS",
  "STAY_NIGHTS",
  "LOYALTY_TIER",
] as const;

export function isSupportedRuleType(t: string): t is RuleType {
  return (SUPPORTED_RULE_TYPES as readonly string[]).includes(t);
}

// The ONLY duration end-modes selectable in Phase 2. AT_CHECKOUT / GRACE_AFTER_CHECKOUT /
// REST_OF_STAY / EARLIEST_OF_FIXED_AND_CHECKOUT (PMS/checkout) are capability-disabled and absent.
export const SUPPORTED_END_MODES = ["MANUAL_END", "VALIDITY_WINDOW", "FIXED_AT"] as const;
export type EndMode = (typeof SUPPORTED_END_MODES)[number];

export type EligibilityRuleForm =
  | { type: "AUTH_METHOD"; methods: string }
  | { type: "SUBJECT_KIND"; kinds: string }
  | { type: "DATE_WINDOW"; from: string; until: string }
  | { type: "PRIOR_PURCHASE"; mode: "requires_prior" | "forbids_prior" }
  | { type: "SITE_NETWORK"; guest_network_ids: string };

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
