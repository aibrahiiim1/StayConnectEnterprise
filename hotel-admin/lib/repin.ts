// Moves a package onto its service plan's CURRENT revision: publishes a new package revision and no service-plan
// revision.
// Edit keeps the old pin deliberately; a repin changes what the plan hands out, so it is a step-up (password asked
// once).

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
 * ACTIVE packages using this plan whose pin is not the plan's current revision. Packages already current, unpinned or
 * on another plan are excluded (a no-op revision is audit noise).
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

export type PackageCurrentDTO = {
  code: string;
  display?: Record<string, unknown> | null;
  duration_policy?: Record<string, unknown> | null;
  eligibility_rules?: { type?: string; Type?: string; value?: Record<string, unknown>; Value?: Record<string, unknown> }[] | null;
  grant_tiers?: { order?: number; Order?: number; value?: Record<string, unknown>; Value?: Record<string, unknown> }[] | null;
  visible_from?: string | null;
  visible_until?: string | null;
  /** Absent means the plan's flat allowance; carried so a repin never flattens a per-night package. */
  data_allocation_policy?: Record<string, unknown> | null;
  // Price, currency and methods are carried as stored: a repin that omitted them republished priced packages as Free.
  price_minor?: number | null;
  currency?: string | null;
  currency_exponent?: number | null;
  settlement_methods?: string[] | null;
  room_charge_mappings?: {
    pms_interface_id: string; posting_code: string; tax_code?: string | null; tax_rate_bp?: number | null;
  }[] | null;
  package_type?: string | null;
  plan_overrides?: Record<string, unknown> | null;
  renewable?: boolean | null;
  max_purchases_per_stay?: number | null;
};

export type CommercialTerms = {
  price_minor: number;
  currency: string;
  currency_exponent: number;
  acquisition_methods: string[];
  room_charge_mappings: { pms_interface_id: string; posting_code: string; tax_code?: string; tax_rate_bp?: number }[];
};

/**
 * Reads the stored commercial terms for a republish, or names what is missing. A PRICED package is never guessed; a
 * Free one with none stored is USD/2, Free.
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
 * Rebuilds a package exactly as is, changing only the pinned service-plan revision. Publishing replaces the whole
 * specification, so everything must be carried (a package without its grant tier is offered to nobody). Go marshals
 * rule/tier structs with capitalised keys, so both spellings are accepted.
 */
export function repinPayload(cur: PackageCurrentDTO, planRevisionID: string, fallbackName?: string) {
  const pick = <T,>(a: T | undefined, b: T | undefined): T | undefined => (a !== undefined ? a : b);
  const { terms } = commercialTermsOf(cur);
  return {
    ...(terms ?? {}),
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
    // The allowance policy travels too, or a per-night package becomes flat.
    ...(cur.data_allocation_policy && Object.keys(cur.data_allocation_policy).length > 0
      ? { data_allocation_policy: cur.data_allocation_policy }
      : {}),
  };
}

export function repinSkippedMessage(skipped: { name: string; missing: string[] }[]): string | null {
  if (skipped.length === 0) return null;
  const list = skipped.map((s) => `${s.name} (missing ${s.missing.join(", ")})`).join("; ");
  return `Not moved: ${list}. Their saved price details could not be read in full, so they were left exactly as ` +
    "they are. Open each one under Internet packages, check its price and how clients get it, and save it.";
}

export function repinOutcomeMessage(n: number): string {
  if (n === 0) {
    return "Nothing was changed. Those packages keep the settings they have now.";
  }
  return `${n} package${n === 1 ? "" : "s"} updated. Existing client access is unchanged; the new settings ` +
    "apply to future grants.";
}
