// STAY-LENGTH TIERS AND STAY-DERIVED ALLOWANCES.
//
// Two operator-facing ideas that both hang off the same fact: how many nights the guest is staying.
//
//   1-7 nights -> Package A, 8-10 -> Package B, 11+ -> Package C
//   1 GB a night, at least 5 GB, at most 20 GB
//
// Neither is arithmetic the operator should have to check by hand. Ranges that overlap produce two eligible
// packages and a chooser where the operator meant a single answer; ranges that leave a gap produce a guest
// who is offered nothing at all. Both are warnings rather than corrections — the operator may genuinely want
// a choice, and silently rewriting somebody's eligibility rules is not a thing this screen gets to do.

/** A stay-length bound as the rules carry it. Either end may be absent, meaning "open". */
export type StayLengthRange = { min?: number; max?: number };

export type PackageStayRange = {
  package_id: string;
  name: string;
  active: boolean;
  range: StayLengthRange | null; // null = no stay-length rule, so it does not participate
};

/** readStayLength pulls the bounds out of a package's eligibility rules, or null when it has no such rule. */
export function readStayLength(
  rules: { type?: string; value?: Record<string, unknown> }[] | null | undefined,
): StayLengthRange | null {
  const r = (rules ?? []).find((x) => (x.type ?? "").toUpperCase() === "STAY_LENGTH");
  if (!r) return null;
  const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? v : undefined);
  const min = num(r.value?.min_nights);
  const max = num(r.value?.max_nights);
  if (min === undefined && max === undefined) return null;
  return { min, max };
}

/** Whether two closed/open night ranges share at least one night. */
export function rangesOverlap(a: StayLengthRange, b: StayLengthRange): boolean {
  const aLo = a.min ?? 0, bLo = b.min ?? 0;
  const aHi = a.max ?? Number.POSITIVE_INFINITY, bHi = b.max ?? Number.POSITIVE_INFINITY;
  return aLo <= bHi && bLo <= aHi;
}

export function describeRange(r: StayLengthRange): string {
  if (r.min !== undefined && r.max !== undefined) {
    return r.min === r.max ? `${r.min} night${r.min === 1 ? "" : "s"}` : `${r.min}–${r.max} nights`;
  }
  if (r.min !== undefined) return `${r.min}+ nights`;
  if (r.max !== undefined) return `up to ${r.max} nights`;
  return "any length";
}

/**
 * stayLengthWarnings reports overlapping night ranges between packages that are BOTH enabled and current.
 *
 * Only enabled packages are compared: a disabled package is offered to nobody, so an overlap with it is not a
 * situation any guest can reach, and warning about it trains the operator to ignore the warning.
 *
 * This warns; it never edits. Overlapping ranges are legitimate when the operator wants the guest to choose,
 * and the product already supports exactly that — so the wording says what WILL happen rather than calling it
 * a mistake.
 */
export function stayLengthWarnings(packages: PackageStayRange[]): string[] {
  const withRange = packages.filter((p) => p.active && p.range !== null);
  const out: string[] = [];
  for (let i = 0; i < withRange.length; i++) {
    for (let j = i + 1; j < withRange.length; j++) {
      const a = withRange[i], b = withRange[j];
      if (rangesOverlap(a.range!, b.range!)) {
        out.push(
          `“${a.name}” (${describeRange(a.range!)}) and “${b.name}” (${describeRange(b.range!)}) both apply to ` +
          `the same stay lengths. Guests staying that long will be asked to choose between them.`,
        );
      }
    }
  }
  return out;
}

// ---------------------------------------------------------------------------
// THE DATA ALLOWANCE.

export type AllocationMode = "FIXED" | "PER_STAY_NIGHT";

export type AllocationForm = {
  mode: AllocationMode;
  gb_per_night: string;
  min_gb: string;
  max_gb: string;
};

export const ALLOCATION_MODE_LABELS: Record<AllocationMode, string> = {
  FIXED: "The service plan's allowance, the same for every guest",
  PER_STAY_NIGHT: "An allowance per night of the stay",
};

/** allocationFromPolicy turns a stored policy back into form fields. An absent policy is FIXED. */
export function allocationFromPolicy(policy: Record<string, unknown> | null | undefined): AllocationForm {
  const empty: AllocationForm = { mode: "FIXED", gb_per_night: "", min_gb: "", max_gb: "" };
  if (!policy || (policy.mode ?? "FIXED") === "FIXED") return empty;
  if (policy.mode !== "PER_STAY_NIGHT") return empty;
  const s = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? String(v) : "");
  return {
    mode: "PER_STAY_NIGHT",
    gb_per_night: s(policy.gb_per_night),
    min_gb: s(policy.min_gb),
    max_gb: s(policy.max_gb),
  };
}

/**
 * validateAllocation returns an operator-readable error, or null.
 *
 * A ceiling below the floor is refused rather than reordered: the two numbers describe an intent, and quietly
 * swapping them publishes a package the operator did not design onto a revision they cannot edit.
 */
export function validateAllocation(a: AllocationForm): string | null {
  if (a.mode !== "PER_STAY_NIGHT") return null;
  const per = Number(a.gb_per_night);
  if (!a.gb_per_night || !Number.isFinite(per) || per <= 0) {
    return "Enter how many GB each night of the stay should give.";
  }
  const min = a.min_gb === "" ? 0 : Number(a.min_gb);
  if (!Number.isFinite(min) || min < 0) return "The minimum allowance must be zero or more.";
  if (a.max_gb !== "") {
    const max = Number(a.max_gb);
    if (!Number.isFinite(max) || max <= 0) return "The maximum allowance must be greater than zero.";
    if (max < min) return "The maximum allowance is below the minimum, so no stay could satisfy both.";
  }
  // A per-night rate with no floor and no ceiling is legitimate, so nothing is required beyond the rate.
  return null;
}

/** serializeAllocation produces the wire policy, or undefined for FIXED (which sends no policy at all). */
export function serializeAllocation(a: AllocationForm): Record<string, unknown> | undefined {
  if (a.mode !== "PER_STAY_NIGHT") return undefined;
  const out: Record<string, unknown> = { mode: "PER_STAY_NIGHT", gb_per_night: Number(a.gb_per_night) };
  if (a.min_gb !== "") out.min_gb = Number(a.min_gb);
  if (a.max_gb !== "") out.max_gb = Number(a.max_gb);
  return out;
}

/**
 * previewAllocation is the operator's proof that the numbers do what they meant.
 *
 * The clamp order is not obvious from three input boxes, and the revision it publishes is immutable, so the
 * screen shows the actual result for a few stay lengths before the operator commits to it. Deliberately the
 * SAME arithmetic as the server, expressed the same way, so a disagreement would show up here rather than in
 * a guest's entitlement.
 */
export function previewAllocation(a: AllocationForm, nights: number[]): { nights: number; gb: number }[] {
  if (a.mode !== "PER_STAY_NIGHT" || validateAllocation(a) !== null) return [];
  const per = Number(a.gb_per_night);
  const min = a.min_gb === "" ? 0 : Number(a.min_gb);
  const max = a.max_gb === "" ? undefined : Number(a.max_gb);
  return nights.map((n) => {
    let gb = n * per;
    if (gb < min) gb = min;
    if (max !== undefined && gb > max) gb = max;
    return { nights: n, gb: Math.round(gb * 100) / 100 };
  });
}
