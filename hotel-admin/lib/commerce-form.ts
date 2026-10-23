// Pure form logic for the Internet-package publish form. Enforces the supported contract client-side; edged
// re-validates authoritatively.

// Eligibility rule types an operator may select. LOYALTY_TIER has no evidence behind it and STAY_NIGHTS is not a rule
// type (use STAY_LENGTH).
export const SUPPORTED_RULE_TYPES = [
  "AUTH_METHOD",
  "SUBJECT_KIND",
  "DATE_WINDOW",
  "PRIOR_PURCHASE",
  "SITE_NETWORK",
  "CLIENT_GROUP",
  "STAY_LENGTH",
  "ROOM_TYPE",
  "RATE_PLAN",
  "VIP",
  "TRAVEL_AGENT",
  "PMS_INTERFACE",
] as const;
export type RuleType = (typeof SUPPORTED_RULE_TYPES)[number];

// CLIENT_GROUP and PRIOR_PURCHASE with forbids_prior get dedicated controls; on the wire they are still ordinary
// eligibility rules.
export const DEDICATED_RULE_TYPES = ["CLIENT_GROUP"] as const;
export function isDedicatedRule(r: { type: string; mode?: string }): boolean {
  if ((DEDICATED_RULE_TYPES as readonly string[]).includes(r.type)) return true;
  return r.type === "PRIOR_PURCHASE" && r.mode === "forbids_prior";
}

// Rule types recognised nowhere in the engine (a package carrying one is ineligible for everybody). Exported for tests.
export const FORBIDDEN_RULE_TYPES = [
  "STAY_NIGHTS",
  "LOYALTY_TIER",
] as const;

// Rule types needing authoritative Stay evidence; a guest who did not sign in through the PMS has none, so the package
// is not offered to them.
export const PMS_RULE_TYPES = [
  "STAY_LENGTH", "ROOM_TYPE", "RATE_PLAN", "VIP", "TRAVEL_AGENT", "PMS_INTERFACE",
] as const;
export function isPMSRuleType(t: string): boolean {
  return (PMS_RULE_TYPES as readonly string[]).includes(t);
}

export function isSupportedRuleType(t: string): t is RuleType {
  return (SUPPORTED_RULE_TYPES as readonly string[]).includes(t);
}

// The only selectable duration end-modes; PMS/checkout-driven modes are capability-disabled.
export const SUPPORTED_END_MODES = ["MANUAL_END", "VALIDITY_WINDOW", "FIXED_AT"] as const;
export type EndMode = (typeof SUPPORTED_END_MODES)[number];

export type EligibilityRuleForm =
  | { type: "AUTH_METHOD"; methods: string }
  | { type: "SUBJECT_KIND"; kinds: string }
  | { type: "DATE_WINDOW"; from: string; until: string }
  // forbids_prior: `within_hours` (1..8760, empty = once ever) and `also_by_device` are sent only when set; a
  // switched-off one is an explicit false.
  | { type: "PRIOR_PURCHASE"; mode: "requires_prior" | "forbids_prior"; within_hours?: string; also_by_device?: boolean }
  | { type: "SITE_NETWORK"; guest_network_ids: string }
  // Group ids are a list of uuids from a picker; `public` widens the rule to Clients in no group.
  | { type: "CLIENT_GROUP"; group_ids: string[]; public: boolean }
  // Either bound may be empty (open end); both empty constrains nothing and is refused.
  | { type: "STAY_LENGTH"; min_nights: string; max_nights: string }
  | { type: "ROOM_TYPE"; room_types: string }
  | { type: "RATE_PLAN"; rate_plans: string }
  | { type: "VIP"; is_vip: "true" | "false" }
  // A list, not comma text: PMS travel-agent names may contain a comma ("Sun Tours, Ltd").
  | { type: "TRAVEL_AGENT"; travel_agents: string[] }
  | { type: "PMS_INTERFACE"; pms_interface_ids: string };

// `extra` is the rest of the stored tier's grant (above all its `match`), handed back unchanged on save; dropping it
// would make a conditional step unconditional.
export type GrantTierForm = {
  order: number | string; down_kbps?: number | string; up_kbps?: number | string;
  extra?: Record<string, unknown>;
};

/** A stored eligibility rule of a type this console does not edit, kept and republished exactly as loaded. */
export type PreservedRule = { type: string; value: Record<string, unknown> };

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
  /** Rules of a type the form does not edit (e.g. STAY_STATUS), republished verbatim. */
  preserved_rules?: PreservedRule[];
  /** The package's loaded display object. The edited name is merged INTO it, so any other key survives. */
  display?: Record<string, unknown> | null;
};

// nameList keeps names whole, trimmed, de-duplicated case-insensitively (the engine matches that way).
export const nameList = (names: readonly string[]): string[] => {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of names) {
    const n = raw.trim();
    if (n && !seen.has(n.toLowerCase())) { seen.add(n.toLowerCase()); out.push(n); }
  }
  return out;
};

const asList = (s: string): string[] =>
  s.split(",").map((x) => x.trim()).filter(Boolean);

// serializeRule maps a form row to the { type, value } wire shape; a forbidden/unknown type throws as a defensive
// guard.
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
    case "PRIOR_PURCHASE": {
      const value: Record<string, unknown> = { [r.mode]: true };
      if (r.mode === "forbids_prior") {
        if (r.within_hours !== undefined && r.within_hours !== "") value.within_hours = Number(r.within_hours);
        if (typeof r.also_by_device === "boolean") value.also_by_device = r.also_by_device;
      }
      return { type: "PRIOR_PURCHASE", value };
    }
    case "SITE_NETWORK":
      return { type: "SITE_NETWORK", value: { guest_network_ids: asList(r.guest_network_ids) } };
    case "CLIENT_GROUP": {
      const value: Record<string, unknown> = {};
      const ids = nameList(r.group_ids);
      if (ids.length) value.group_ids = ids;
      if (r.public) value.public = true;
      return { type: "CLIENT_GROUP", value };
    }
    case "STAY_LENGTH": {
      // An empty bound is omitted, never 0: {min_nights: 0} looks like a rule but constrains nothing.
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
      return { type: "TRAVEL_AGENT", value: { travel_agents: nameList(r.travel_agents) } };
    case "PMS_INTERFACE":
      return { type: "PMS_INTERFACE", value: { pms_interface_ids: asList(r.pms_interface_ids) } };
    default:
      throw new Error(`unsupported rule type: ${(r as { type: string }).type}`);
  }
}

// Bounds of the free-allowance window, mirroring edged (1 hour to one year).
export const WITHIN_HOURS_MIN = 1;
export const WITHIN_HOURS_MAX = 8760;

// validateRule explains why a dedicated-control rule cannot be published as it stands; the server re-validates.
export function validateRule(r: EligibilityRuleForm): string | null {
  if (r.type === "CLIENT_GROUP") {
    if (nameList(r.group_ids).length === 0 && !r.public) {
      return "Audience: choose at least one client group, or set the audience to Everyone.";
    }
  }
  if (r.type === "PRIOR_PURCHASE" && r.mode === "forbids_prior" && r.within_hours !== undefined && r.within_hours !== "") {
    const h = Number(r.within_hours);
    if (!Number.isInteger(h) || h < WITHIN_HOURS_MIN || h > WITHIN_HOURS_MAX) {
      return `Free access: the number of hours must be a whole number between ${WITHIN_HOURS_MIN} and ${WITHIN_HOURS_MAX}.`;
    }
  }
  return null;
}

// validateDuration returns an error string or null; PMS/checkout modes are not representable in DurationForm.
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

export function validateSaleWindow(from?: string, until?: string): string | null {
  if (from && until && new Date(from).getTime() >= new Date(until).getTime()) {
    return "sale window start must be before end";
  }
  return null;
}

// Speed steps were never applied: grant tiers are a first-match list and the network always uses the service plan's
// speed, so the appliance and this form refuse more than one step or a step with speeds.
export const SPEED_STEPS_REFUSAL =
  "This package has speed steps, which were never applied: clients always get the speed of the package's service " +
  "plan. Remove the speed steps, and choose a service plan with the speed you want.";

/** Whether the tiers are the one plain step a package may carry: one step, no speeds of its own. */
export function tiersArePlain(tiers: GrantTierForm[]): boolean {
  const set = (v: number | string | undefined) => v !== undefined && v !== "";
  return tiers.length <= 1 && tiers.every((t) => !set(t.down_kbps) && !set(t.up_kbps));
}

/** The one plain step that replaces speed steps: the lowest order, nothing else. */
export function plainTiers(tiers: GrantTierForm[]): GrantTierForm[] {
  const orders = tiers.map((t) => Number(t.order)).filter((n) => Number.isFinite(n));
  return [{ order: orders.length ? Math.min(...orders) : 10 }];
}

export function tiersInOrder(tiers: GrantTierForm[]): { order: number; grant: Record<string, unknown> }[] {
  return tiers
    .map((t) => {
      const grant: Record<string, unknown> = { ...(t.extra ?? {}) };
      if (t.down_kbps !== undefined && t.down_kbps !== "") grant.down_kbps = Number(t.down_kbps);
      if (t.up_kbps !== undefined && t.up_kbps !== "") grant.up_kbps = Number(t.up_kbps);
      return { order: Number(t.order), grant };
    })
    .sort((a, b) => a.order - b.order);
}

export type PublishPayload = {
  code: string;
  service_plan_revision_id: string;
  display: Record<string, unknown> & { name: string };
  duration_policy: Record<string, unknown>;
  eligibility_rules: { type: string; value: Record<string, unknown> }[];
  grant_tiers: { order: number; grant: Record<string, unknown> }[];
  visible_from?: string;
  visible_until?: string;
  data_allocation_policy?: Record<string, unknown>;
  price_minor?: number;
  currency?: string;
  currency_exponent?: number;
  acquisition_methods?: AcquisitionMethod[];
  room_charge_mappings?: RoomChargeMappingWire[];
  package_type?: string;
  plan_overrides?: Record<string, unknown> | null;
  renewable?: boolean;
  max_purchases_per_stay?: number | null;
  /** Step-up: required when a revision changes the price, currency, methods or service plan. */
  password?: string;
};

// Free-access limit and "only clients who already had a package" contradict each other (offered to nobody); the server
// refuses the pair.
export const CONTRADICTORY_PRIOR_PURCHASE =
  "Free access is limited to clients who have not had this package before, and a condition also requires an earlier purchase, so no client can meet both. Remove the \"Only clients who already had a package here\" condition, or set Free access to No limit.";

/** validateRuleSet checks the conditions against each other; validateRule checks each one alone. */
export function validateRuleSet(rules: EligibilityRuleForm[]): string | null {
  const prior = rules.filter((r): r is Extract<EligibilityRuleForm, { type: "PRIOR_PURCHASE" }> => r.type === "PRIOR_PURCHASE");
  if (prior.some((r) => r.mode === "forbids_prior") && prior.some((r) => r.mode === "requires_prior")) {
    return CONTRADICTORY_PRIOR_PURCHASE;
  }
  return null;
}

/** Two speed steps with the same order have no defined order between them; the server refuses them. */
export function validateTiers(tiers: GrantTierForm[]): string | null {
  const seen = new Set<number>();
  for (const t of tiers) {
    const o = Number(t.order);
    if (!Number.isInteger(o)) return "Speed steps: each step's order must be a whole number.";
    if (seen.has(o)) return `Speed steps: two steps have the order ${o}. Give each step its own order.`;
    seen.add(o);
  }
  return null;
}

// buildPublishPayload emits no price/acquisition field; alone it publishes a Free package. The package form adds the
// price section from buildAcquisition.
export function buildPublishPayload(s: PublishFormState): { payload?: PublishPayload; error?: string } {
  if (!s.code.trim()) return { error: "code is required" };
  if (!s.service_plan_revision_id) return { error: "a service plan is required" };
  if (s.tiers.length === 0) return { error: "at least one grant tier is required" };
  if (!tiersArePlain(s.tiers)) return { error: SPEED_STEPS_REFUSAL };
  const durErr = validateDuration(s.duration);
  if (durErr) return { error: durErr };
  const winErr = validateSaleWindow(s.visible_from, s.visible_until);
  if (winErr) return { error: winErr };
  for (const r of s.rules) {
    if (!isSupportedRuleType(r.type)) return { error: `unsupported rule type: ${r.type}` };
    const err = validateRule(r);
    if (err) return { error: err };
  }
  const setErr = validateRuleSet(s.rules);
  if (setErr) return { error: setErr };
  const tierErr = validateTiers(s.tiers);
  if (tierErr) return { error: tierErr };
  const payload: PublishPayload = {
    code: s.code.trim(),
    service_plan_revision_id: s.service_plan_revision_id,
    display: { ...(s.display ?? {}), name: s.name.trim() || s.code.trim() },
    duration_policy: serializeDuration(s.duration),
    eligibility_rules: [
      ...s.rules.map(serializeRule),
      ...(s.preserved_rules ?? []).map((r) => ({ type: r.type, value: r.value ?? {} })),
    ],
    grant_tiers: tiersInOrder(s.tiers),
  };
  if (s.visible_from) payload.visible_from = new Date(s.visible_from).toISOString();
  if (s.visible_until) payload.visible_until = new Date(s.visible_until).toISOString();
  return { payload };
}


// Operator wording for the enum values above; the wire values are unchanged. Kept beside the enums so a new value
// forces a label.
export const END_MODE_LABELS: Record<EndMode, string> = {
  MANUAL_END: "Until the client disconnects or an operator ends it",
  VALIDITY_WINDOW: "For a fixed length of time after the client connects",
  FIXED_AT: "Until a specific date and time",
};

export const RULE_TYPE_LABELS: Record<RuleType, string> = {
  AUTH_METHOD: "How the client signed in",
  SUBJECT_KIND: "Type of client credential",
  DATE_WINDOW: "Only between two dates",
  PRIOR_PURCHASE: "Whether they already had a package",
  SITE_NETWORK: "Only on certain client networks",
  CLIENT_GROUP: "Client group (audience)",
  STAY_LENGTH: "How many nights they are staying",
  ROOM_TYPE: "Room type",
  RATE_PLAN: "Rate plan",
  VIP: "VIP guest",
  TRAVEL_AGENT: "Travel agent",
  PMS_INTERFACE: "Which PMS the stay came from",
};


// Price and acquisition. The server is authoritative (invalid_acquisition_methods, invalid_currency,
// invalid_room_charge_mapping, 409 module_not_enabled);
// these mirror it so the form never offers a combination the server will refuse.

export const ACQUISITION_METHODS = ["NOT_REQUIRED", "PREPAID", "ONLINE_PAYMENT", "PMS_POSTING"] as const;
export type AcquisitionMethod = (typeof ACQUISITION_METHODS)[number];

export const ACQUISITION_METHOD_LABELS: Record<AcquisitionMethod, string> = {
  NOT_REQUIRED: "Free",
  PREPAID: "Voucher",
  ONLINE_PAYMENT: "Card payment",
  PMS_POSTING: "Room charge",
};

export function isAcquisitionMethod(m: string): m is AcquisitionMethod {
  return (ACQUISITION_METHODS as readonly string[]).includes(m);
}

// The exponent is the currency's ISO-4217 one (the server refuses others); JPY is known so a yen package still loads.
export const CURRENCY_EXPONENTS: Record<string, number> = {
  EGP: 2, USD: 2, EUR: 2, SAR: 2, AED: 2, QAR: 2, GBP: 2,
  OMR: 3, KWD: 3, BHD: 3, JOD: 3,
  JPY: 0,
};

/** The currencies offered in the picker. A package already in another currency keeps it. */
export const OFFERED_CURRENCIES = ["EGP", "USD", "EUR", "SAR", "AED", "OMR", "KWD", "QAR", "BHD", "JOD", "GBP"] as const;

// A free package with no currency is stored as USD/2, so a new form starts there too.
export const DEFAULT_CURRENCY = "USD";

export type RoomChargeMappingForm = {
  pms_interface_id: string;
  posting_code: string;
  tax_code: string;
  tax_rate_pct: string;
};

export type AcquisitionForm = {
  price: string;
  currency: string;
  currency_exponent?: number;
  methods: AcquisitionMethod[];
  room_charge: RoomChargeMappingForm[];
};

export type RoomChargeMappingWire = {
  pms_interface_id: string; posting_code: string; tax_code?: string; tax_rate_bp?: number;
};

export type AcquisitionPayload = {
  price_minor: number;
  currency: string;
  currency_exponent: number;
  acquisition_methods: AcquisitionMethod[];
  room_charge_mappings?: RoomChargeMappingWire[];
};

export function newAcquisitionForm(): AcquisitionForm {
  return { price: "0", currency: DEFAULT_CURRENCY, methods: ["NOT_REQUIRED"], room_charge: [] };
}

export function currencyExponent(currency: string, fallback?: number | null): number | null {
  const e = CURRENCY_EXPONENTS[currency.trim().toUpperCase()];
  if (e !== undefined) return e;
  return typeof fallback === "number" && Number.isInteger(fallback) && fallback >= 0 ? fallback : null;
}

// parsePrice converts typed text to minor units without floating point; excess precision ("0.005" in 2 decimals) is
// refused, not rounded.
export function parsePrice(price: string, exponent: number): { minor?: number; error?: string } {
  const s = price.trim();
  if (s === "") return { minor: 0 };
  const m = /^(\d+)(?:\.(\d*))?$/.exec(s);
  if (!m) return { error: "Enter the price as a number, for example 25 or 12.50." };
  const frac = (m[2] ?? "").replace(/0+$/, "");
  if (frac.length > exponent) {
    return {
      error: exponent === 0
        ? "This currency has no decimal places. Enter a whole number."
        : `This currency has ${exponent} decimal places. Enter at most ${exponent} after the point.`,
    };
  }
  const minor = Number(m[1]) * 10 ** exponent + (frac ? Number(frac.padEnd(exponent, "0")) : 0);
  if (!Number.isSafeInteger(minor)) return { error: "That price is too large." };
  return { minor };
}

export function formatMajor(minor: number, exponent: number): string {
  if (exponent <= 0) return String(minor);
  const s = String(Math.abs(minor)).padStart(exponent + 1, "0");
  return `${minor < 0 ? "-" : ""}${s.slice(0, -exponent)}.${s.slice(-exponent)}`;
}

export function isPaidPrice(price: string, currency: string, fallbackExponent?: number | null): boolean {
  const e = currencyExponent(currency, fallbackExponent);
  if (e === null) return false;
  const p = parsePrice(price, e);
  return (p.minor ?? 0) > 0;
}

// Price crossing zero changes which methods apply: a priced package cannot be Free, a free one cannot be charged to a
// card or room.
export function methodsForPriceChange(
  methods: AcquisitionMethod[], wasPaid: boolean, isPaid: boolean,
): AcquisitionMethod[] {
  if (!wasPaid && isPaid) return methods.filter((m) => m !== "NOT_REQUIRED");
  if (wasPaid && !isPaid) return methods.filter((m) => m !== "ONLINE_PAYMENT" && m !== "PMS_POSTING");
  return methods;
}

export function pctToBp(pct: string): { bp?: number; error?: string } {
  const m = /^(\d+)(?:\.(\d{0,2}))?$/.exec(pct.trim());
  if (!m) return { error: "enter the tax rate as a percentage with at most two decimals, for example 14 or 12.5." };
  const bp = Number(m[1]) * 100 + Number((m[2] ?? "").padEnd(2, "0"));
  if (bp > 10000) return { error: "the tax rate must be between 0 and 100%." };
  return { bp };
}

export function bpToPct(bp: number): string {
  if (bp % 100 === 0) return String(bp / 100);
  const s = (bp / 100).toFixed(2);
  return s.endsWith("0") ? s.slice(0, -1) : s;
}

const PRINTABLE_1_20 = /^[\x20-\x7E]{1,20}$/;

export function buildAcquisition(f: AcquisitionForm): { fields?: AcquisitionPayload; error?: string } {
  const currency = f.currency.trim().toUpperCase();
  if (!/^[A-Z]{3}$/.test(currency)) return { error: "Choose a currency." };
  const exponent = currencyExponent(currency, f.currency_exponent);
  if (exponent === null) return { error: `${currency} is not a currency this console can price in. Choose another.` };
  const price = parsePrice(f.price, exponent);
  if (price.error || price.minor === undefined) return { error: price.error ?? "Enter a price." };
  const minor = price.minor;

  const methods = ACQUISITION_METHODS.filter((m) => f.methods.includes(m));
  if (methods.length === 0) {
    return { error: minor > 0
      ? "Choose how clients pay for this package: Voucher, Card payment or Room charge."
      : "Choose how clients get this package: Free or Voucher." };
  }
  if (minor === 0 && (methods.includes("ONLINE_PAYMENT") || methods.includes("PMS_POSTING"))) {
    return { error: "Card payment and Room charge need a price. Set a price, or untick them." };
  }
  if (minor > 0 && methods.includes("NOT_REQUIRED")) {
    return { error: "A package with a price cannot also be free. Untick Free, or set the price to 0." };
  }

  const fields: AcquisitionPayload = {
    price_minor: minor, currency, currency_exponent: exponent, acquisition_methods: methods,
  };
  if (methods.includes("PMS_POSTING")) {
    if (f.room_charge.length === 0) {
      return { error: "Room charge needs a posting code for at least one PMS interface. Add one, or untick Room charge." };
    }
    const seen = new Set<string>();
    const out: RoomChargeMappingWire[] = [];
    for (const [i, r] of f.room_charge.entries()) {
      const n = `Room charge mapping ${i + 1}`;
      const id = r.pms_interface_id.trim();
      if (!id) return { error: `${n}: choose the PMS interface.` };
      if (seen.has(id)) return { error: `${n}: that PMS interface already has a mapping. Use one row per interface.` };
      seen.add(id);
      const code = r.posting_code.trim();
      if (!PRINTABLE_1_20.test(code) || code.includes("|")) {
        return { error: `${n}: the posting code must be 1 to 20 printable characters, without |.` };
      }
      const w: RoomChargeMappingWire = { pms_interface_id: id, posting_code: code };
      const tax = r.tax_code.trim();
      if (tax) {
        if (!PRINTABLE_1_20.test(tax) || tax.includes("|")) {
          return { error: `${n}: the tax code must be at most 20 printable characters, without |.` };
        }
        w.tax_code = tax;
      }
      if (r.tax_rate_pct.trim() !== "") {
        const bp = pctToBp(r.tax_rate_pct);
        if (bp.error || bp.bp === undefined) return { error: `${n}: ${bp.error}` };
        w.tax_rate_bp = bp.bp;
      }
      out.push(w);
    }
    fields.room_charge_mappings = out;
  }
  return { fields };
}

// A refused publish answers "code: reason"; the operator sees which part of the form it concerns plus the server's
// explanation. Other messages are left alone (null).
const REFUSAL_PREFIXES: Record<string, string> = {
  invalid_acquisition_methods: "How clients get this package",
  invalid_currency: "Currency",
  invalid_room_charge_mapping: "Room charge",
  invalid_duration_policy: "How long access lasts",
  invalid_eligibility_rules: "Conditions",
  invalid_grant_tiers: "Speed steps",
  invalid_data_allocation_policy: "Data allowance",
};
const REFUSAL_RE = new RegExp(`^(${Object.keys(REFUSAL_PREFIXES).join("|")})(?::\\s*([\\s\\S]*))?$`);
export function describePublishRefusal(message: string): string | null {
  const m = REFUSAL_RE.exec(message.trim());
  if (!m) return null;
  const rest = (m[2] ?? "").trim();
  const sentence = rest ? rest.charAt(0).toUpperCase() + rest.slice(1) : "The server refused this setting";
  return `${REFUSAL_PREFIXES[m[1]]}: ${sentence}${/[.!?]$/.test(sentence) ? "" : "."}`;
}

// Free and Voucher are always selectable; other methods and any price above zero need a licensed AND switched-on
// module.
// "Ready" is a separate gate: a card method with no active card account may be configured but is not offered until
// ready.

export type ModuleState = {
  id: string; label: string; licensed: boolean; enabled: boolean; ready: boolean;
  reasons?: string[]; readiness?: string[];
};
export type ModulesReport = { modules?: Record<string, ModuleState> | null };

export type RoomChargeInterface = {
  pms_interface_id: string; display_label: string; connector_kind?: string;
  financial_base_currency: string | null; ready: boolean; reason: string | null;
};

export const METHOD_MODULE: Partial<Record<AcquisitionMethod, string>> = {
  ONLINE_PAYMENT: "card_payment",
  PMS_POSTING: "room_charge",
};
export const PAID_ACCESS_MODULE = "paid_access";

export type Availability = {
  selectable: boolean;
  reason?: string;
  notReady?: string[];
};

/** Whether a module-gated option can be chosen. `report` null (loading) or "error" fails closed. */
export function moduleAvailability(moduleID: string, report: ModulesReport | "error" | null): Availability {
  if (report === "error") return { selectable: false, reason: "Module state could not be read" };
  if (report === null) return { selectable: false, reason: "Checking modules…" };
  const m = report.modules?.[moduleID];
  if (!m || !m.licensed) return { selectable: false, reason: "Not licensed" };
  if (!m.enabled) return { selectable: false, reason: "Switched off in System › Modules" };
  if (!m.ready) {
    const why = (m.readiness?.length ? m.readiness : m.reasons) ?? [];
    return { selectable: true, notReady: why.length ? why : ["Not ready yet"] };
  }
  return { selectable: true };
}

export function moduleLicensedIn(moduleID: string, report: ModulesReport | "error" | null): boolean {
  if (!report || report === "error") return false;
  const m = report.modules?.[moduleID];
  return !!m && !!m.licensed;
}

/**
 * Free and Voucher always; a module-owned method only when its module is licensed, unless the package already uses it
 * (so it can be removed).
 */
export function methodShown(method: AcquisitionMethod, report: ModulesReport | "error" | null, checked: boolean): boolean {
  const mod = METHOD_MODULE[method];
  return !mod || checked || moduleLicensedIn(mod, report);
}

export function methodAvailability(method: AcquisitionMethod, report: ModulesReport | "error" | null): Availability {
  const mod = METHOD_MODULE[method];
  return mod ? moduleAvailability(mod, report) : { selectable: true };
}

export const ROOM_CHARGE_NOT_READY: Record<string, string> = {
  NOT_ONBOARDED: "Not approved for room charge yet",
  ONBOARDING_NOT_APPROVED: "Room-charge approval is not complete",
  CONNECTOR_NOT_FINANCIAL: "This connection cannot post charges",
};
export function roomChargeNotReadyText(reason: string | null | undefined): string {
  return (reason && ROOM_CHARGE_NOT_READY[reason]) || "Not ready for room charge";
}

// Revising what clients pay or receive is a step-up action (401 reauth_required without the operator's password);
// renames, conditions and sale windows are not.

export type RevisionTerms = {
  price_minor: number;
  currency: string;
  currency_exponent: number;
  acquisition_methods: readonly string[];
  service_plan_revision_id: string;
};

export function revisionNeedsPassword(before: RevisionTerms | null | undefined, after: RevisionTerms): boolean {
  if (!before) return false;
  const set = (xs: readonly string[]) => Array.from(new Set(xs)).sort().join(",");
  return before.price_minor !== after.price_minor
    || before.currency.trim().toUpperCase() !== after.currency.trim().toUpperCase()
    || before.currency_exponent !== after.currency_exponent
    || set(before.acquisition_methods) !== set(after.acquisition_methods)
    || before.service_plan_revision_id !== after.service_plan_revision_id;
}
