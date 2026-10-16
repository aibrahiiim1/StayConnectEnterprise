// Shaping the API's CURRENT configuration of a package into the authoring form's shape.
//
// Moved out of the page unchanged. Edit loads the current configuration — including the eligibility rules and
// grant tiers the form does not display prominently — because saving republishes all of it, and anything not
// loaded would be silently dropped.

import type {
  EligibilityRuleForm, GrantTierForm, DurationForm, AcquisitionForm, AcquisitionMethod, PreservedRule,
  RevisionTerms,
} from "@/lib/commerce-form";
import {
  DEFAULT_CURRENCY, bpToPct, currencyExponent, formatMajor, isAcquisitionMethod, isSupportedRuleType,
} from "@/lib/commerce-form";

export type PackageCurrent = {
  package_id: string; code: string; revision_id: string; revision_no: number;
  service_plan_revision_id: string;
  display?: Record<string, unknown> | null;
  duration_policy?: Record<string, unknown> | null;
  data_allocation_policy?: Record<string, unknown> | null;
  eligibility_rules?: { Type?: string; type?: string; Value?: Record<string, unknown>; value?: Record<string, unknown> }[] | null;
  grant_tiers?: { Order?: number; order?: number; Value?: Record<string, unknown>; value?: Record<string, unknown> }[] | null;
  visible_from?: string | null; visible_until?: string | null;
  // PRICE AND HOW CLIENTS GET IT. Loaded for the same reason as the allowance policy: saving republishes all
  // of it, so a rename must hand the price, the methods and the room-charge posting codes back unchanged.
  price_minor?: number | null;
  currency?: string | null;
  currency_exponent?: number | null;
  /** The stored acquisition methods (NOT_REQUIRED, PREPAID, ONLINE_PAYMENT, PMS_POSTING). */
  settlement_methods?: string[] | null;
  room_charge_mappings?: {
    pms_interface_id: string; posting_code: string; tax_code?: string | null; tax_rate_bp?: number | null;
  }[] | null;
  // CARRIED ON EVERY REVISION. The form does not edit these; a save hands them back exactly as loaded, so a
  // rename never resets what kind of package it is, its plan overrides, renewability or purchase limit.
  package_type?: string | null;
  plan_overrides?: Record<string, unknown> | null;
  renewable?: boolean | null;
  max_purchases_per_stay?: number | null;
};

/** The revision fields the form does not edit, as the publish body carries them back. */
export type RevisionCarry = {
  package_type?: string;
  plan_overrides?: Record<string, unknown> | null;
  renewable?: boolean;
  max_purchases_per_stay?: number | null;
};

// revisionTermsOf is what the package charges and grants today, normalised the way the form will send it, so
// the editor can tell whether a save changes any of it (and so needs the operator's password).
export function revisionTermsOf(cur: Pick<PackageCurrent,
  "price_minor" | "currency" | "currency_exponent" | "settlement_methods" | "service_plan_revision_id">): RevisionTerms {
  const currency = (cur.currency ?? "").trim().toUpperCase() || DEFAULT_CURRENCY;
  const price = typeof cur.price_minor === "number" && cur.price_minor > 0 ? cur.price_minor : 0;
  const stored = (cur.settlement_methods ?? []).filter((m) => typeof m === "string" && m !== "");
  return {
    price_minor: price,
    currency,
    currency_exponent: currencyExponent(currency, cur.currency_exponent) ?? 2,
    acquisition_methods: stored.length ? stored : price > 0 ? [] : ["NOT_REQUIRED"],
    service_plan_revision_id: cur.service_plan_revision_id ?? "",
  };
}

// revisionCarryOf returns the loaded value of each carried field that the reader returned. A field the reader
// did not return is left out, which the server reads as "carry what the current revision has".
export function revisionCarryOf(cur: Pick<PackageCurrent,
  "package_type" | "plan_overrides" | "renewable" | "max_purchases_per_stay">): RevisionCarry {
  const out: RevisionCarry = {};
  if (typeof cur.package_type === "string" && cur.package_type !== "") out.package_type = cur.package_type;
  if (cur.plan_overrides !== undefined) out.plan_overrides = cur.plan_overrides;
  if (typeof cur.renewable === "boolean") out.renewable = cur.renewable;
  if (cur.max_purchases_per_stay !== undefined) out.max_purchases_per_stay = cur.max_purchases_per_stay;
  return out;
}

// acquisitionToForm reads the stored price section back into the form. A revision published before prices
// existed carries price 0 and {NOT_REQUIRED}, or nothing at all; both read as Free, which is what it is.
// The stored exponent is kept alongside the currency so a currency this build does not list is still
// handed back with the exponent it was published with, never a guessed one.
export function acquisitionToForm(cur: Pick<PackageCurrent,
  "price_minor" | "currency" | "currency_exponent" | "settlement_methods" | "room_charge_mappings">): AcquisitionForm {
  const currency = (cur.currency ?? "").trim().toUpperCase() || DEFAULT_CURRENCY;
  const exponent = currencyExponent(currency, cur.currency_exponent) ?? 2;
  const minor = typeof cur.price_minor === "number" && cur.price_minor > 0 ? cur.price_minor : 0;
  const stored = (cur.settlement_methods ?? []).filter(isAcquisitionMethod);
  const methods: AcquisitionMethod[] = stored.length ? stored : minor > 0 ? [] : ["NOT_REQUIRED"];
  return {
    price: minor > 0 ? formatMajor(minor, exponent) : "0",
    currency,
    ...(typeof cur.currency_exponent === "number" ? { currency_exponent: cur.currency_exponent } : {}),
    methods,
    room_charge: (cur.room_charge_mappings ?? []).map((m) => ({
      pms_interface_id: m.pms_interface_id,
      posting_code: m.posting_code ?? "",
      tax_code: m.tax_code ?? "",
      tax_rate_pct: typeof m.tax_rate_bp === "number" ? bpToPct(m.tax_rate_bp) : "",
    })),
  };
}

// Go marshals these structs with capitalised keys (the fields carry no json tags), so both spellings are
// accepted rather than assuming one. Getting this wrong would drop rules on save.
const pick = <T,>(a: T | undefined, b: T | undefined): T | undefined => (a !== undefined ? a : b);

export function rulesToForm(rules: PackageCurrent["eligibility_rules"]): EligibilityRuleForm[] {
  const out: EligibilityRuleForm[] = [];
  for (const r of rules ?? []) {
    const type = pick(r.type, r.Type);
    const value = (pick(r.value, r.Value) ?? {}) as Record<string, unknown>;
    const list = (k: string) => (Array.isArray(value[k]) ? (value[k] as string[]).join(", ") : "");
    const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? String(v) : "");
    switch (type) {
      case "AUTH_METHOD": out.push({ type: "AUTH_METHOD", methods: list("methods") }); break;
      case "SUBJECT_KIND": out.push({ type: "SUBJECT_KIND", kinds: list("kinds") }); break;
      case "DATE_WINDOW": out.push({
        type: "DATE_WINDOW",
        from: toLocalInput(value.from as string | undefined) ?? "",
        until: toLocalInput(value.until as string | undefined) ?? "",
      }); break;
      // THE FREE ALLOWANCE. The window and the device switch are read back only when the stored rule names
      // them: a rule that never did keeps the server's defaults, and republishing it must not pin a value the
      // operator never set.
      case "PRIOR_PURCHASE": out.push({
        type: "PRIOR_PURCHASE", mode: value.requires_prior ? "requires_prior" : "forbids_prior",
        ...(typeof value.within_hours === "number" && Number.isFinite(value.within_hours) ? { within_hours: String(value.within_hours) } : {}),
        ...(typeof value.also_by_device === "boolean" ? { also_by_device: value.also_by_device } : {}),
      }); break;
      case "SITE_NETWORK": out.push({ type: "SITE_NETWORK", guest_network_ids: list("guest_network_ids") }); break;
      case "CLIENT_GROUP": out.push({
        type: "CLIENT_GROUP",
        group_ids: Array.isArray(value.group_ids)
          ? (value.group_ids as unknown[]).filter((x): x is string => typeof x === "string")
          : [],
        public: value.public === true,
      }); break;
      // THE STAY DIMENSIONS. A bound that is absent stays absent: reading a missing max_nights back as "0"
      // would republish "up to zero nights", i.e. a package for nobody.
      case "STAY_LENGTH": out.push({
        type: "STAY_LENGTH",
        min_nights: num(value.min_nights),
        max_nights: num(value.max_nights),
      }); break;
      case "ROOM_TYPE": out.push({ type: "ROOM_TYPE", room_types: list("room_types") }); break;
      case "RATE_PLAN": out.push({ type: "RATE_PLAN", rate_plans: list("rate_plans") }); break;
      case "VIP": out.push({ type: "VIP", is_vip: value.is_vip === false ? "false" : "true" }); break;
      // Kept as the stored list: joining it into text would split a name that contains a comma on the next save.
      case "TRAVEL_AGENT": out.push({
        type: "TRAVEL_AGENT",
        travel_agents: Array.isArray(value.travel_agents)
          ? (value.travel_agents as unknown[]).filter((x): x is string => typeof x === "string")
          : [],
      }); break;
      case "PMS_INTERFACE": out.push({
        type: "PMS_INTERFACE", pms_interface_ids: list("pms_interface_ids"),
      }); break;
      // A type the form does not edit is NOT handled here: preservedRulesOf keeps it verbatim instead, so
      // saving never drops a condition the operator could not even see (audit O-15).
      default: break;
    }
  }
  return out;
}

// preservedRulesOf returns the stored rules of a type this form does not edit (STAY_STATUS, or anything a
// newer appliance knows), exactly as loaded. They are republished unchanged and shown read-only.
export function preservedRulesOf(rules: PackageCurrent["eligibility_rules"]): PreservedRule[] {
  const out: PreservedRule[] = [];
  for (const r of rules ?? []) {
    const type = pick(r.type, r.Type);
    if (!type || isSupportedRuleType(type)) continue;
    out.push({ type, value: (pick(r.value, r.Value) ?? {}) as Record<string, unknown> });
  }
  return out;
}

export function tiersToForm(tiers: PackageCurrent["grant_tiers"]): GrantTierForm[] {
  const out: GrantTierForm[] = [];
  for (const t of tiers ?? []) {
    const value = (pick(t.value, t.Value) ?? {}) as Record<string, unknown>;
    // Everything but the two speeds the form edits -- the tier's `match` condition above all -- is kept.
    const { down_kbps: down, up_kbps: up, ...extra } = value;
    out.push({
      order: pick(t.order, t.Order) ?? 10,
      down_kbps: (down as number | undefined) ?? "",
      up_kbps: (up as number | undefined) ?? "",
      ...(Object.keys(extra).length > 0 ? { extra } : {}),
    });
  }
  return out;
}

export function durationToForm(d: PackageCurrent["duration_policy"]): DurationForm {
  const mode = (d?.end_mode as string) ?? "MANUAL_END";
  if (mode === "VALIDITY_WINDOW") return { end_mode: "VALIDITY_WINDOW", duration_seconds: Number(d?.duration_seconds ?? 0) };
  if (mode === "FIXED_AT") return { end_mode: "FIXED_AT", ends_at: toLocalInput(d?.ends_at as string | undefined) ?? "" };
  return { end_mode: "MANUAL_END" };
}

// datetime-local wants "YYYY-MM-DDTHH:mm" in local time; the API speaks RFC3339.
export function toLocalInput(iso?: string | null): string | undefined {
  if (!iso) return undefined;
  const t = new Date(iso);
  if (!Number.isFinite(t.getTime())) return undefined;
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${t.getFullYear()}-${pad(t.getMonth() + 1)}-${pad(t.getDate())}T${pad(t.getHours())}:${pad(t.getMinutes())}`;
}
