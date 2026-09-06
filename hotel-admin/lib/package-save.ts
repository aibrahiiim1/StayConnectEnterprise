// SAVING A PACKAGE, WITHOUT THE OPERATOR EVER MEETING A REVISION.
//
// The revision model is correct and stays exactly as it is: package revisions and service-plan revisions are
// immutable, and everything a guest was ever granted stays pinned to the revision it was granted under. What
// was wrong was the ADMINISTRATION of it. To change a package's speed an operator had to publish a new
// service-plan revision, remember its id, open the package, publish a new package revision and re-select that
// id from a dropdown. Miss the last step and nothing happens to guests at all — which is exactly what
// happened: a new 10/5 Mbps plan revision was published, no package was ever repinned to it, and the next
// guest was still shaped to 2 Mbps because the package's current revision still pointed at the old plan.
//
// So this module holds the decision the operator should never have had to make: given what the package is
// now and what the form says it should be, WHICH revisions have to exist. It is pure and deterministic so it
// can be tested directly — the two API calls it describes are made by the page.

export type PlanFields = {
  down_kbps?: number | null;
  up_kbps?: number | null;
  max_concurrent_devices?: number | null;
  device_limit_policy?: string | null;
  idle_timeout_seconds?: number | null;
  max_continuous_session_seconds?: number | null;
  time_quota_seconds?: number | null;
  data_quota_bytes?: number | null;
  time_accounting_mode?: string | null;
  speed_allocation?: string | null;
};

// The plan fields an operator can change from the package screen, and the ONLY ones a difference in which
// forces a new service-plan revision. Listed explicitly rather than derived from Object.keys so that adding a
// plan field is a decision about this screen rather than something that silently starts triggering revisions.
export const PLAN_FIELDS: (keyof PlanFields)[] = [
  "down_kbps",
  "up_kbps",
  "max_concurrent_devices",
  "device_limit_policy",
  "idle_timeout_seconds",
  "max_continuous_session_seconds",
  "time_quota_seconds",
  "data_quota_bytes",
  "time_accounting_mode",
  "speed_allocation",
];

// norm collapses the three ways "not set" arrives — null, undefined, empty string — to one value, so that a
// field the operator never touched does not read as a change and publish a pointless revision.
function norm(v: unknown): string | number | null {
  if (v === null || v === undefined || v === "") return null;
  if (typeof v === "number") return Number.isFinite(v) ? v : null;
  const n = Number(v);
  return Number.isFinite(n) && String(v).trim() !== "" ? n : String(v);
}

/** planFieldsDiffer reports which plan fields the form changes relative to the pinned revision. */
export function changedPlanFields(current: PlanFields, next: PlanFields): (keyof PlanFields)[] {
  return PLAN_FIELDS.filter((k) => norm(current[k]) !== norm(next[k]));
}

export type SavePlanIntent = {
  /** The plan CODE to publish under. Publishing against an existing code adds a revision to that plan. */
  code: string;
  fields: PlanFields;
};

export type SaveDecision = {
  /** Set when a new service-plan revision must be published first; its id becomes the package's new pin. */
  plan?: SavePlanIntent;
  /** Which plan revision the new package revision pins when no new plan revision is needed. */
  reusePlanRevisionID?: string;
  changedPlanFields: (keyof PlanFields)[];
  /** True when a package revision is published — which is always, because saving means republishing. */
  publishesPackageRevision: true;
};

/**
 * decideSave works out what saving this form has to create.
 *
 * ALWAYS a package revision: the package's own fields (name, duration, eligibility, tiers, sale window) live
 * on the package revision, and a revision is immutable, so "save" means "publish the next one". Republishing
 * an unchanged package is harmless and keeps the rule simple — one save, one revision, no branch where the
 * operator's click quietly did nothing.
 *
 * A plan revision ONLY when a plan field actually changed. Publishing one every time would fork the plan on
 * every unrelated edit, and a plan shared by three packages would sprout revisions nobody asked for.
 */
export function decideSave(args: {
  planCode: string;
  pinnedPlanRevisionID: string;
  currentPlan: PlanFields;
  nextPlan: PlanFields;
}): SaveDecision {
  const changed = changedPlanFields(args.currentPlan, args.nextPlan);
  if (changed.length === 0) {
    return {
      reusePlanRevisionID: args.pinnedPlanRevisionID,
      changedPlanFields: [],
      publishesPackageRevision: true,
    };
  }
  return {
    plan: { code: args.planCode, fields: args.nextPlan },
    changedPlanFields: changed,
    publishesPackageRevision: true,
  };
}

/**
 * saveOutcomeMessage is what the operator is told afterwards. It says the one thing that is easy to get
 * wrong: existing guests keep what they already have.
 */
export function saveOutcomeMessage(d: SaveDecision): string {
  const base =
    "Changes saved. Existing guest access is unchanged; the new settings apply to future grants.";
  return d.plan ? `${base} The speed and allowance settings were updated too.` : base;
}
