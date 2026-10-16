// MOVING A PACKAGE ONTO ITS SERVICE PLAN'S CURRENT SETTINGS.
//
// A package pins a specific service-plan revision, and package Edit deliberately keeps that pin when the
// selected plan has not changed — otherwise renaming a package would quietly hand future guests different
// speeds. The consequence is a state the operator could see but not act on: "Free Internet" publishes new
// settings as revision 4, and the "Freee" package goes on giving revision 3's 2 Mbps until somebody moves it.
//
// Before this, the only way to move it was to publish ANOTHER plan revision, because the repin prompt only
// appeared straight after publishing one. That is a worse fix than the problem: it adds an immutable revision
// nobody wanted, purely to be offered a button.
//
// So the repin is available on its own terms. It publishes a new PACKAGE revision pinned to the plan's
// EXISTING current revision, and creates no service-plan revision at all.
//
// A repin changes the service plan a package hands out, so it is a step-up: the operator's password is asked
// once and sent with every package it moves.

import { DEFAULT_CURRENCY, currencyExponent } from "@/lib/commerce-form";

export type PlanRef = {
  plan_id: string;
  code: string;
  name?: string | null;
  current_revision_id: string;
};

export type PackageRef = {
  package_id: string;
  code: string;
  name?: string | null;
  active: boolean;
  service_plan_id?: string | null;
  service_plan_revision_id?: string | null;
};

/**
 * stalePackagesFor returns the ACTIVE packages using this plan whose pinned revision is not the plan's
 * current one.
 *
 * A package already on the current revision is not stale and must not be offered: republishing it would
 * create an immutable revision that changes nothing, which is noise in an audit trail that exists to be read.
 * A package with no pin yet, or one belonging to another plan, is not this plan's business either.
 */
export function stalePackagesFor(plan: PlanRef, packages: PackageRef[]): PackageRef[] {
  if (!plan.current_revision_id) return [];
  return packages.filter(
    (p) =>
      p.active &&
      p.service_plan_id === plan.plan_id &&
      !!p.service_plan_revision_id &&
      p.service_plan_revision_id !== plan.current_revision_id,
  );
}

/** The shape the scoped reader (migration 0063) returns for a package's current configuration. */
export type PackageCurrentDTO = {
  code: string;
  display?: Record<string, unknown> | null;
  duration_policy?: Record<string, unknown> | null;
  eligibility_rules?: { type?: string; Type?: string; value?: Record<string, unknown>; Value?: Record<string, unknown> }[] | null;
  grant_tiers?: { order?: number; Order?: number; value?: Record<string, unknown>; Value?: Record<string, unknown> }[] | null;
  visible_from?: string | null;
  visible_until?: string | null;
  /** Absent means the plan's flat allowance. Carried across so a repin never silently flattens a
   *  per-night package back to a fixed one. */
  data_allocation_policy?: Record<string, unknown> | null;
  // WHAT THE PACKAGE COSTS AND HOW CLIENTS GET IT. A repin that left these out republished every chosen
  // package as Free -- card, room-charge and voucher packages alike (audit O-1). The server now refuses a
  // revision without them; they are carried from here, exactly as stored.
  price_minor?: number | null;
  currency?: string | null;
  currency_exponent?: number | null;
  /** The stored acquisition methods; the publish body calls them acquisition_methods. */
  settlement_methods?: string[] | null;
  room_charge_mappings?: {
    pms_interface_id: string; posting_code: string; tax_code?: string | null; tax_rate_bp?: number | null;
  }[] | null;
  // Revision fields with no control anywhere in the console, carried as loaded.
  package_type?: string | null;
  plan_overrides?: Record<string, unknown> | null;
  renewable?: boolean | null;
  max_purchases_per_stay?: number | null;
};

/** The commercial terms a revision of an existing package must state in full. */
export type CommercialTerms = {
  price_minor: number;
  currency: string;
  currency_exponent: number;
  acquisition_methods: string[];
  room_charge_mappings: { pms_interface_id: string; posting_code: string; tax_code?: string; tax_rate_bp?: number }[];
};

/**
 * commercialTermsOf reads the package's stored commercial terms for a republish, or names what is missing.
 *
 * Nothing is guessed for a PRICED package: a missing currency, exponent or method list on one is reported, and
 * the package is not republished, because the server would otherwise be handed a price nobody set. A Free
 * package with no stored currency or methods is what every revision published before prices existed carries;
 * it is stated the way the server stores it (USD/2, Free).
 */
export function commercialTermsOf(cur: PackageCurrentDTO): { terms?: CommercialTerms; missing: string[] } {
  const missing: string[] = [];
  const price = cur.price_minor;
  if (typeof price !== "number" || !Number.isSafeInteger(price) || price < 0) missing.push("price_minor");
  const free = price === 0;
  let currency = (cur.currency ?? "").trim().toUpperCase();
  if (!currency && free) currency = DEFAULT_CURRENCY;
  if (!/^[A-Z]{3}$/.test(currency)) missing.push("currency");
  const exponent = currency ? currencyExponent(currency, cur.currency_exponent) : null;
  if (exponent === null) missing.push("currency_exponent");
  let methods = (cur.settlement_methods ?? []).filter((m): m is string => typeof m === "string" && m !== "");
  if (methods.length === 0 && free) methods = ["NOT_REQUIRED"];
  if (methods.length === 0) missing.push("acquisition_methods");
  // The reader always returns the key; null means "no mappings". Only a reader that omits it is a gap.
  if (cur.room_charge_mappings === undefined) missing.push("room_charge_mappings");
  if (missing.length > 0) return { missing };
  return {
    missing,
    terms: {
      price_minor: price as number,
      currency,
      currency_exponent: exponent as number,
      acquisition_methods: methods,
      room_charge_mappings: (cur.room_charge_mappings ?? []).map((m) => ({
        pms_interface_id: m.pms_interface_id,
        posting_code: m.posting_code,
        ...(m.tax_code ? { tax_code: m.tax_code } : {}),
        ...(typeof m.tax_rate_bp === "number" ? { tax_rate_bp: m.tax_rate_bp } : {}),
      })),
    },
  };
}

/**
 * repinPayload rebuilds a package EXACTLY as it is, changing only which service-plan revision it pins.
 *
 * Publishing replaces the whole specification, so everything the package already had has to be carried
 * across: its display name, duration policy, sale window, eligibility rules and grant tiers. Anything omitted
 * would be silently dropped — and a package that lost its only grant tier is offered to nobody. That is why
 * the conditions come from the scoped reader rather than from whatever the list happened to show.
 *
 * Go marshals the rule/tier structs with capitalised keys, so both spellings are accepted rather than one
 * being assumed.
 */
export function repinPayload(cur: PackageCurrentDTO, planRevisionID: string, fallbackName?: string) {
  const pick = <T,>(a: T | undefined, b: T | undefined): T | undefined => (a !== undefined ? a : b);
  const { terms } = commercialTermsOf(cur);
  return {
    // The price, currency, methods and room-charge codes, as stored. Absent only when they cannot be read in
    // full -- and then the caller does not post (commercialTermsOf names what is missing).
    ...(terms ?? {}),
    // Fields no console screen edits, carried as loaded so a repin is not also a change of package kind.
    ...(typeof cur.package_type === "string" && cur.package_type !== "" ? { package_type: cur.package_type } : {}),
    ...(cur.plan_overrides !== undefined ? { plan_overrides: cur.plan_overrides } : {}),
    ...(typeof cur.renewable === "boolean" ? { renewable: cur.renewable } : {}),
    ...(cur.max_purchases_per_stay !== undefined ? { max_purchases_per_stay: cur.max_purchases_per_stay } : {}),
    code: cur.code,
    service_plan_revision_id: planRevisionID,
    display: cur.display ?? { name: fallbackName ?? cur.code },
    duration_policy: cur.duration_policy ?? { end_mode: "MANUAL_END" },
    eligibility_rules: (cur.eligibility_rules ?? []).map((r) => ({
      type: pick(r.type, r.Type),
      value: pick(r.value, r.Value) ?? {},
    })),
    grant_tiers: (cur.grant_tiers ?? []).map((t) => ({
      order: pick(t.order, t.Order) ?? 10,
      grant: pick(t.value, t.Value) ?? {},
    })),
    visible_from: cur.visible_from ?? undefined,
    visible_until: cur.visible_until ?? undefined,
    // The allowance policy is part of the package's terms, so it travels with everything else. Omitting it
    // would turn a per-night package into a flat one on a revision that claims only to have moved plan.
    ...(cur.data_allocation_policy && Object.keys(cur.data_allocation_policy).length > 0
      ? { data_allocation_policy: cur.data_allocation_policy }
      : {}),
  };
}

/** repinSkippedMessage says which packages were NOT moved because their stored terms could not be read in full. */
export function repinSkippedMessage(skipped: { name: string; missing: string[] }[]): string | null {
  if (skipped.length === 0) return null;
  const list = skipped.map((s) => `${s.name} (missing ${s.missing.join(", ")})`).join("; ");
  return `Not moved: ${list}. Their saved price details could not be read in full, so they were left exactly as ` +
    "they are. Open each one under Internet packages, check its price and how clients get it, and save it.";
}

/** repinOutcomeMessage states the two things an operator needs after applying. */
export function repinOutcomeMessage(n: number): string {
  if (n === 0) {
    return "Nothing was changed. Those packages keep the settings they have now.";
  }
  return `${n} package${n === 1 ? "" : "s"} updated. Existing client access is unchanged; the new settings ` +
    "apply to future grants.";
}
