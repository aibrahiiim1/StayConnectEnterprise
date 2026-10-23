// Saving a package: the operator picks a Service Plan and the revision to pin is worked out here (revisions are
// immutable).

export type PlanChoice = {
  plan_id: string;
  current_revision_id: string;
};

export type SaveDecision = {
  pinPlanRevisionID: string;
  planChanged: boolean;
  publishesPackageRevision: true;
};

/** An unchanged plan keeps its pin so a rename cannot silently advance the service-plan revision. */
export function decideSave(args: {
  pinnedPlanRevisionID: string;
  selected: PlanChoice;
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

export function saveOutcomeMessage(d: SaveDecision): string {
  const base = "Changes saved. Existing client access is unchanged; the new settings apply to future grants.";
  return d.planChanged ? `${base} Future clients will receive the selected service plan.` : base;
}

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
