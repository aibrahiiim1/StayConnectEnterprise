// SAVING A PACKAGE: WHICH SERVICE PLAN IT USES, AND WHICH REVISION THAT PINS.
//
// The revision model stays exactly as it is — package revisions and service-plan revisions are immutable, and
// everything a guest was granted stays pinned to what it was granted under. What an administrator should not
// have to do is name a REVISION. They pick a Service Plan; the revision that pins is worked out here.
//
// THIS REPLACED A WORSE SIMPLIFICATION. For a moment the package form carried the speed, allowance and device
// fields itself and published a service-plan revision behind the operator's back — which hid the Service Plan
// relationship rather than the revision detail, and let a package quietly author technical settings that
// belong to a plan other packages also use. Revisions are the implementation detail. The plan is not.

export type PlanChoice = {
  plan_id: string;
  /** The plan's CURRENT revision — what a package pinned to this plan today would receive. */
  current_revision_id: string;
};

export type SaveDecision = {
  /** The service-plan revision the new package revision pins. */
  pinPlanRevisionID: string;
  /** True when the operator chose a different Service Plan than the one currently pinned. */
  planChanged: boolean;
  /** Always: saving publishes a new immutable package revision. */
  publishesPackageRevision: true;
};

/**
 * decideSave works out which service-plan revision a save pins.
 *
 * UNCHANGED SELECTION KEEPS THE EXISTING PIN, and that is deliberate rather than incidental. A package pinned
 * to revision 3 of a plan whose current revision is 4 is not out of date by accident — repinning it is a
 * decision the Service Plans screen asks for explicitly, package by package. Silently advancing the pin here,
 * just because someone renamed the package, would apply a technical change nobody approved on this screen.
 *
 * CHANGING THE PLAN pins that plan's CURRENT revision, because choosing a plan means choosing what it grants
 * now.
 *
 * A package revision is published either way: the package's own fields live on it, it is immutable, so "save"
 * means "publish the next one".
 */
export function decideSave(args: {
  /** The service-plan revision this package pins today. Empty when adding. */
  pinnedPlanRevisionID: string;
  /** The plan the operator selected. */
  selected: PlanChoice;
  /** The plan the package currently uses, if any. */
  currentPlanID?: string;
}): SaveDecision {
  const planChanged = !args.currentPlanID || args.currentPlanID !== args.selected.plan_id;
  return {
    pinPlanRevisionID: planChanged || !args.pinnedPlanRevisionID
      ? args.selected.current_revision_id
      : args.pinnedPlanRevisionID,
    planChanged,
    publishesPackageRevision: true,
  };
}

/** saveOutcomeMessage says the one thing that is easy to get wrong: existing guests keep what they have. */
export function saveOutcomeMessage(d: SaveDecision): string {
  const base = "Changes saved. Existing guest access is unchanged; the new settings apply to future grants.";
  return d.planChanged ? `${base} Future guests will receive the selected service plan.` : base;
}

/** planSummary is how a plan is described wherever one is chosen — never a code or an id on its own. */
export function planSummary(p: {
  name?: string | null; code?: string;
  down_kbps?: number | null; up_kbps?: number | null;
  data_quota_bytes?: number | null; time_quota_seconds?: number | null;
  max_concurrent_devices?: number | null; speed_allocation?: string | null;
}): string {
  const bits: string[] = [];
  const speed = (k?: number | null) =>
    !k ? null : k >= 1000 ? `${+(k / 1000).toFixed(1)} Mbps` : `${k} kbps`;
  const down = speed(p.down_kbps);
  const up = speed(p.up_kbps);
  if (down) bits.push(`${down} down`);
  if (up) bits.push(`${up} up`);
  if (p.data_quota_bytes) {
    const b = p.data_quota_bytes;
    bits.push(b >= 1e9 ? `${+(b / 1e9).toFixed(2)} GB` : `${Math.round(b / 1e6)} MB`);
  }
  if (p.time_quota_seconds) {
    const s = p.time_quota_seconds;
    bits.push(s % 86400 === 0 ? `${s / 86400} day${s / 86400 === 1 ? "" : "s"}` : `${Math.round(s / 3600)} h`);
  }
  if (p.max_concurrent_devices) {
    bits.push(`${p.max_concurrent_devices} device${p.max_concurrent_devices === 1 ? "" : "s"}`);
  }
  if (p.speed_allocation === "SHARED") bits.push("shared speed");
  return bits.length ? bits.join(" · ") : "no limits set";
}
