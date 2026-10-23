// Stay-length tiers and stay-derived allowances. Overlaps and gaps are warnings, never silent rewrites of the
// operator's rules.

export type StayLengthRange = { min?: number; max?: number };

export type PackageStayRange = {
  package_id: string;
  name: string;
  active: boolean;
  range: StayLengthRange | null; // null = no stay-length rule, so it does not participate
};

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
 * Reports overlapping night ranges between packages that are BOTH enabled and current (a disabled one is unreachable).
 * It warns and never edits: overlap is legitimate when the guest should choose.
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

export type AllocationMode = "FIXED" | "PER_STAY_NIGHT";

export type AllocationForm = {
  mode: AllocationMode;
  gb_per_night: string;
  min_gb: string;
  max_gb: string;
};

export const ALLOCATION_MODE_LABELS: Record<AllocationMode, string> = {
  FIXED: "The service plan's allowance, the same for every client",
  PER_STAY_NIGHT: "An allowance per night of the stay",
};

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

/** Reject a ceiling below the floor rather than swapping values in an immutable revision. */
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
  return null;
}

export function serializeAllocation(a: AllocationForm): Record<string, unknown> | undefined {
  if (a.mode !== "PER_STAY_NIGHT") return undefined;
  const out: Record<string, unknown> = { mode: "PER_STAY_NIGHT", gb_per_night: Number(a.gb_per_night) };
  if (a.min_gb !== "") out.min_gb = Number(a.min_gb);
  if (a.max_gb !== "") out.max_gb = Number(a.max_gb);
  return out;
}

/** Shows the actual result for a few stay lengths before commit; deliberately the SAME arithmetic as the server. */
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

// Which allowance applies: a PER_STAY_NIGHT package computes its allowance at grant time and FREEZES it onto the
// entitlement,
// and quota readers prefer the entitlement's value, so the package wins. The Service Plan is not modified; FIXED
// packages keep the plan's quota.

export type AllowanceNoticeKind =
  | "PER_STAY_NIGHT_OVERRIDES_PLAN"
  | "PER_STAY_NIGHT_PLAN_HAS_NO_QUOTA"
  | "FIXED_USES_PLAN"
  | "FIXED_PLAN_HAS_NO_QUOTA";

export type AllowanceNotice = {
  kind: AllowanceNoticeKind;
  emphasis: boolean;
  planQuotaGB?: number;
  gbPerNight?: number;
  minGB?: number;
  maxGB?: number;
  exampleNights?: number;
  exampleGB?: number;
};

/** Eight nights sits above a typical floor and below a ceiling, so the example shows the calculation, not a clamp. */
export const NOTICE_EXAMPLE_NIGHTS = 8;

/**
 * Which notice the Data allowance section shows, or null when nothing truthful can be said. Never claims an override
 * when the plan sets no quota.
 */
export function allowanceNotice(
  alloc: AllocationForm,
  planDataQuotaBytes: number | null | undefined,
  planSelected: boolean,
): AllowanceNotice | null {
  if (!planSelected) return null;
  const planGB = planDataQuotaBytes && planDataQuotaBytes > 0
    ? Math.round((planDataQuotaBytes / 1_000_000_000) * 100) / 100
    : undefined;

  if (alloc.mode !== "PER_STAY_NIGHT") {
    return planGB === undefined
      ? { kind: "FIXED_PLAN_HAS_NO_QUOTA", emphasis: false }
      : { kind: "FIXED_USES_PLAN", emphasis: false, planQuotaGB: planGB };
  }
  if (validateAllocation(alloc) !== null) return null;

  const per = Number(alloc.gb_per_night);
  const min = alloc.min_gb === "" ? undefined : Number(alloc.min_gb);
  const max = alloc.max_gb === "" ? undefined : Number(alloc.max_gb);
  const example = previewAllocation(alloc, [NOTICE_EXAMPLE_NIGHTS])[0];

  return {
    kind: planGB === undefined ? "PER_STAY_NIGHT_PLAN_HAS_NO_QUOTA" : "PER_STAY_NIGHT_OVERRIDES_PLAN",
    emphasis: planGB !== undefined,
    planQuotaGB: planGB,
    gbPerNight: per,
    minGB: min,
    maxGB: max,
    exampleNights: example?.nights,
    exampleGB: example?.gb,
  };
}
