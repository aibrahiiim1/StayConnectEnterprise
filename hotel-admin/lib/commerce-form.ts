// Pure, deterministic form logic for the Internet-package publish form. Kept separate from the React
// components so it is unit-testable in isolation and shared by the editors. Everything here enforces the
// currently-supported contract on the client (the edged API re-validates authoritatively): the supported
// eligibility rules, only the duration modes this build actually implements, and — at the end of the file —
// the price, currency, acquisition methods and room-charge mappings a package is offered with.

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
  /** Absent together means a Free package (the server's default). See buildAcquisition. */
  price_minor?: number;
  currency?: string;
  currency_exponent?: number;
  acquisition_methods?: AcquisitionMethod[];
  room_charge_mappings?: RoomChargeMappingWire[];
};

// buildPublishPayload validates the whole form and returns { payload } or { error }. It emits no price or
// acquisition field of its own: on its own it publishes a Free package, which is what the server makes of a
// body that names none. The package form adds the price section from buildAcquisition, validated separately.
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
  STAY_LENGTH: "How many nights they are staying",
  ROOM_TYPE: "Room type",
  RATE_PLAN: "Rate plan",
  VIP: "VIP guest",
  TRAVEL_AGENT: "Travel agent",
  PMS_INTERFACE: "Which PMS the stay came from",
};


// ------------------------------------------------------------------ PRICE AND HOW CLIENTS GET IT ---------
//
// A package says what it costs and the ways a client may acquire it. The server is the authority on every rule
// below (it refuses with `invalid_acquisition_methods`, `invalid_currency`, `invalid_room_charge_mapping`, or
// 409 `module_not_enabled`); these mirror it so the operator is told before a round trip, in words, and the
// form never offers a combination the server is certain to refuse.
//
// buildPublishPayload above is deliberately left alone: a caller that names no acquisition fields still
// publishes a Free package, exactly as before, and that backward-compatible path stays testable on its own.

/** The acquisition methods, in the order they are presented. The wire values are the stored ones. */
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

// THE EXPONENT IS A PROPERTY OF THE CURRENCY, NOT A CHOICE. The server refuses any exponent that is not the
// ISO-4217 one, so the operator never types it: it is read from here. JPY is not in the offered list but is
// known, so a package already published in yen is still read and handed back correctly.
export const CURRENCY_EXPONENTS: Record<string, number> = {
  EGP: 2, USD: 2, EUR: 2, SAR: 2, AED: 2, QAR: 2, GBP: 2,
  OMR: 3, KWD: 3, BHD: 3, JOD: 3,
  JPY: 0,
};

/** The currencies offered in the picker. A package already in another currency keeps it. */
export const OFFERED_CURRENCIES = ["EGP", "USD", "EUR", "SAR", "AED", "OMR", "KWD", "QAR", "BHD", "JOD", "GBP"] as const;

// A free package with no currency is stored by the server as USD/2, so a new form starts there too: saving an
// untouched new package publishes exactly what omitting the fields would have.
export const DEFAULT_CURRENCY = "USD";

export type RoomChargeMappingForm = {
  pms_interface_id: string;
  posting_code: string;
  tax_code: string;
  /** Percent as typed ("14" = 14%). Empty means no rate is recorded. */
  tax_rate_pct: string;
};

export type AcquisitionForm = {
  /** Major units as typed ("12.50"). Empty reads as 0. */
  price: string;
  currency: string;
  /** Only consulted for a loaded currency this build does not know; otherwise the map decides. */
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
  /** Present only when Room charge is one of the methods; the server forbids it otherwise. */
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

// parsePrice turns what the operator typed into minor units WITHOUT floating point: "12.5" in a 2-decimal
// currency is 1250, and "0.005" in one is refused rather than rounded, because a rounded price is a price
// nobody set.
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

/** formatMajor is the inverse of parsePrice, for loading a stored price back into the input. */
export function formatMajor(minor: number, exponent: number): string {
  if (exponent <= 0) return String(minor);
  const s = String(Math.abs(minor)).padStart(exponent + 1, "0");
  return `${minor < 0 ? "-" : ""}${s.slice(0, -exponent)}.${s.slice(-exponent)}`;
}

/** True when the typed price parses to more than zero. An unparseable price is not "paid". */
export function isPaidPrice(price: string, currency: string, fallbackExponent?: number | null): boolean {
  const e = currencyExponent(currency, fallbackExponent);
  if (e === null) return false;
  const p = parsePrice(price, e);
  return (p.minor ?? 0) > 0;
}

// The price moving across zero changes which methods can apply at all, so the form follows it rather than
// leaving the operator with a combination that is certain to be refused: a priced package cannot be Free, and
// a free one cannot be charged to a card or a room. Voucher is valid on both sides and is left as it is.
export function methodsForPriceChange(
  methods: AcquisitionMethod[], wasPaid: boolean, isPaid: boolean,
): AcquisitionMethod[] {
  if (!wasPaid && isPaid) return methods.filter((m) => m !== "NOT_REQUIRED");
  if (wasPaid && !isPaid) return methods.filter((m) => m !== "ONLINE_PAYMENT" && m !== "PMS_POSTING");
  return methods;
}

// Tax rate in percent to basis points, without floating-point drift: "14" is 1400, "12.5" is 1250.
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

// Printable ASCII, the same test the server applies (it also refuses "|", which separates FIAS fields).
const PRINTABLE_1_20 = /^[\x20-\x7E]{1,20}$/;

// buildAcquisition validates the price section and returns the fields to add to the publish body, or an
// operator-readable error. The messages say what to change, not which rule was broken.
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

// THE SERVER'S REFUSALS, READABLE. A publish the server refuses answers with its reason verbatim, prefixed by
// a machine code ("invalid_currency: …"). The code is for logs; the operator is told which part of the form it
// concerns, followed by the server's own explanation, unchanged. Any other message is left alone (null).
const REFUSAL_PREFIXES: Record<string, string> = {
  invalid_acquisition_methods: "How clients get this package",
  invalid_currency: "Currency",
  invalid_room_charge_mapping: "Room charge",
};
export function describePublishRefusal(message: string): string | null {
  const m = /^(invalid_acquisition_methods|invalid_currency|invalid_room_charge_mapping):\s*([\s\S]*)$/.exec(message.trim());
  if (!m) return null;
  const rest = m[2].trim();
  const sentence = rest ? rest.charAt(0).toUpperCase() + rest.slice(1) : "The server refused this setting";
  return `${REFUSAL_PREFIXES[m[1]]}: ${sentence}${/[.!?]$/.test(sentence) ? "" : "."}`;
}

// ------------------------------------------------------------- WHICH METHODS THIS SITE CAN OFFER ---------
//
// Free and Voucher are the core product and always selectable. The other two, and any price above zero,
// belong to modules the site must have LICENSED AND SWITCHED ON (System › Modules) — the same test the server
// applies when it publishes. "Ready" is a further, separate gate: a card method with no active card account
// may be configured, but clients are not offered it until it is ready, and the form says so.

/** One module as GET /modules reports it (only the fields this form reads). */
export type ModuleState = {
  id: string; label: string; licensed: boolean; enabled: boolean; ready: boolean;
  reasons?: string[]; readiness?: string[];
};
export type ModulesReport = { modules?: Record<string, ModuleState> | null };

/** A PMS interface as GET /pms-financial-onboarding reports it (only the fields this form reads). */
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
  /** Why it cannot be chosen, in a few words. */
  reason?: string;
  /** Selectable, but clients are not offered it yet: what is missing. */
  notReady?: string[];
};

/**
 * moduleAvailability answers whether a module-gated option can be chosen here. `report` is null while the
 * module state is loading and "error" when it could not be read; both FAIL CLOSED — an option the site may not
 * be licensed for is never offered on a guess.
 */
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

export function methodAvailability(method: AcquisitionMethod, report: ModulesReport | "error" | null): Availability {
  const mod = METHOD_MODULE[method];
  return mod ? moduleAvailability(mod, report) : { selectable: true };
}

/** Why a PMS interface cannot take room charges yet, in the operator's words. */
export const ROOM_CHARGE_NOT_READY: Record<string, string> = {
  NOT_ONBOARDED: "Not approved for room charge yet",
  ONBOARDING_NOT_APPROVED: "Room-charge approval is not complete",
  CONNECTOR_NOT_FINANCIAL: "This connection cannot post charges",
};
export function roomChargeNotReadyText(reason: string | null | undefined): string {
  return (reason && ROOM_CHARGE_NOT_READY[reason]) || "Not ready for room charge";
}
