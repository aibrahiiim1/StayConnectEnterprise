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
};

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
  return {
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
  };
}

/** repinOutcomeMessage states the two things an operator needs after applying. */
export function repinOutcomeMessage(n: number): string {
  if (n === 0) {
    return "Nothing was changed. Those packages keep the settings they have now.";
  }
  return `${n} package${n === 1 ? "" : "s"} updated. Existing guest access is unchanged; the new settings ` +
    "apply to future grants.";
}
