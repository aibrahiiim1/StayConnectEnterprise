// WHICH ALLOWANCE A GUEST ACTUALLY RECEIVES.
//
// The Data allowance section can show two numbers at once: the service plan's flat quota and the package's
// per-night one. Nothing on the screen said which of them wins, and the honest reading was a coin toss --
// the plan is the technical service, the package is the offer, either could plausibly take precedence.
//
// It is settled by where the number is written: a PER_STAY_NIGHT package computes its allowance at grant time
// and freezes it onto the entitlement, and every quota reader prefers the entitlement's own value. So the
// package wins FOR ITS OWN GUESTS, and the plan is not touched, so packages using the plan's allowance are
// unaffected. Both halves are asserted here, because an operator who reads only the first will believe
// editing a package changed the plan.

import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { PackageForm, type PlanOption } from "@/app/(app)/internet-packages/package-form";
import { allowanceNotice, NOTICE_EXAMPLE_NIGHTS } from "@/lib/stay-packages";

const GB = 1_000_000_000;

const plans: PlanOption[] = [
  { plan_id: "plan-5gb", code: "FIVE", name: "Five GB", current_revision_id: "r1",
    down_kbps: 10000, data_quota_bytes: 5 * GB },
  { plan_id: "plan-20gb", code: "TWENTY", name: "Twenty GB", current_revision_id: "r2",
    down_kbps: 10000, data_quota_bytes: 20 * GB },
  // A plan that limits speed but sets no data allowance at all.
  { plan_id: "plan-none", code: "NOQUOTA", name: "No quota", current_revision_id: "r3", down_kbps: 10000 },
];

function openForm() {
  return render(<PackageForm mode="add" plans={plans} onSave={() => {}} />);
}
const setMode = (m: string) => fireEvent.change(screen.getByLabelText("allocation-mode"), { target: { value: m } });
const setPlan = (p: string) => fireEvent.change(screen.getByLabelText("service-plan"), { target: { value: p } });
const notice = () => screen.queryByTestId("allowance-notice");

function perNight(gb: string, min = "", max = "") {
  setMode("PER_STAY_NIGHT");
  fireEvent.change(screen.getByLabelText("gb-per-night"), { target: { value: gb } });
  if (min !== "") fireEvent.change(screen.getByLabelText("min-gb"), { target: { value: min } });
  if (max !== "") fireEvent.change(screen.getByLabelText("max-gb"), { target: { value: max } });
}

describe("the data-allowance precedence notice", () => {
  it("says the package allowance wins, and that the plan is untouched, when both set a quota", () => {
    openForm();
    setPlan("plan-5gb");
    perNight("1", "3", "20");

    const n = notice()!;
    expect(n).toBeTruthy();
    expect(n.textContent).toContain("Service plan allowance");
    expect(n.textContent).toContain("5 GB");
    expect(n.textContent).toContain("1 GB per stay night");
    expect(n.textContent).toContain("Minimum 3 GB");
    expect(n.textContent).toContain("Maximum 20 GB");
    // The half that stops an operator believing they edited the plan:
    expect(n.textContent).toContain("takes precedence");
    expect(n.textContent).toContain("service plan itself is unchanged");
    expect(n.textContent).toMatch(/still applies to other packages/i);
  });

  it("shows the worked example so the arithmetic is visible rather than inferred", () => {
    openForm();
    setPlan("plan-5gb");
    perNight("1", "3", "20");
    // 8 nights x 1 GB = 8 GB, above the 3 GB floor and below the 20 GB ceiling.
    expect(screen.getByTestId("allowance-example").textContent)
      .toMatch(new RegExp(`For an ${NOTICE_EXAMPLE_NIGHTS}-night stay the guest receives\\s*8 GB`));
  });

  it("FIXED says plainly that the plan's allowance is what applies", () => {
    openForm();
    setPlan("plan-5gb");
    // FIXED is the default mode; assert it explicitly rather than relying on that.
    setMode("FIXED");
    const n = notice()!;
    expect(n.textContent).toContain("uses the service plan");
    expect(n.textContent).toContain("5 GB");
    // No override language anywhere: nothing is being overridden.
    expect(n.textContent).not.toMatch(/precedence|instead|overrid/i);
  });

  it("never claims an override when the plan sets no allowance to override", () => {
    openForm();
    setPlan("plan-none");
    perNight("2");
    const n = notice()!;
    expect(n.textContent).toMatch(/sets no data allowance of its own/i);
    expect(n.textContent).toContain("2 GB per stay night");
    // SCOPED TO DATA, DELIBERATELY. "the only allowance a guest receives" read as though it also covered the
    // time, session and device limits the plan still imposes -- it does not; it is the DATA allowance only.
    expect(n.textContent).toMatch(/data allowance for this package/i);
    expect(n.textContent).not.toMatch(/only allowance a guest receives/i);
    expect(n.textContent).not.toMatch(/precedence|takes precedence|instead of/i);
    // ...and it must not invent a plan quota figure.
    expect(n.textContent).not.toMatch(/Service plan allowance:/i);
  });

  it("FIXED on a plan with no allowance says so without implying a limit", () => {
    openForm();
    setPlan("plan-none");
    setMode("FIXED");
    // "does not limit data" read as though the whole package were unrestricted. The limit that is absent is
    // the DATA-VOLUME one; the plan's speed, time, session and device limits are untouched by this notice.
    expect(notice()!.textContent)
      .toMatch(/has no data-usage quota, so this package does not impose a\s+data-volume limit/i);
    expect(notice()!.textContent).not.toMatch(/does not limit data/i);
  });

  it("follows the selected service plan immediately", () => {
    openForm();
    setPlan("plan-5gb");
    perNight("1");
    expect(notice()!.textContent).toContain("5 GB");

    setPlan("plan-20gb");
    expect(notice()!.textContent).toContain("20 GB");
    expect(notice()!.textContent).not.toContain("5 GB");
  });

  it("follows the allocation mode immediately, in both directions", () => {
    openForm();
    setPlan("plan-5gb");
    perNight("1");
    expect(notice()!.textContent).toContain("takes precedence");

    setMode("FIXED");
    expect(notice()!.textContent).toContain("uses the service plan");
    expect(notice()!.textContent).not.toMatch(/precedence/i);

    setMode("PER_STAY_NIGHT");
    expect(notice()!.textContent).toContain("takes precedence");
  });

  it("follows the per-night, minimum and maximum values as they are typed", () => {
    openForm();
    setPlan("plan-5gb");
    perNight("1");
    expect(notice()!.textContent).toContain("1 GB per stay night");

    fireEvent.change(screen.getByLabelText("gb-per-night"), { target: { value: "2" } });
    expect(notice()!.textContent).toContain("2 GB per stay night");
    // 8 nights x 2 GB, no clamps configured.
    expect(screen.getByTestId("allowance-example").textContent).toContain("16 GB");

    fireEvent.change(screen.getByLabelText("max-gb"), { target: { value: "10" } });
    expect(notice()!.textContent).toContain("Maximum 10 GB");
    // ...and the example now shows the ceiling doing its work.
    expect(screen.getByTestId("allowance-example").textContent).toContain("10 GB");
  });

  it("says nothing at all until there is something true to say", () => {
    openForm();
    // No plan chosen yet: the notice would have to invent an allowance.
    expect(notice()).toBeNull();
    setPlan("plan-5gb");
    setMode("PER_STAY_NIGHT");
    // Per-night chosen but no rate typed: still nothing truthful to state.
    expect(notice()).toBeNull();
  });

  it("is informational and never blocks saving", () => {
    // Both configurations are legitimate, so the notice must not gate Save. Proven by the notice being a
    // note rather than an alert, and by the form having no validation tied to it.
    openForm();
    setPlan("plan-5gb");
    perNight("1", "3", "20");
    expect(notice()!.getAttribute("role")).toBe("note");
    expect(screen.getByRole("button", { name: /add package/i })).toBeEnabled();
  });
});

describe("allowanceNotice (the decision, independent of rendering)", () => {
  const per = (gb: string, min = "", max = "") =>
    ({ mode: "PER_STAY_NIGHT" as const, gb_per_night: gb, min_gb: min, max_gb: max });
  const fixed = { mode: "FIXED" as const, gb_per_night: "", min_gb: "", max_gb: "" };

  it("emphasises only the case where two allowances actually disagree", () => {
    expect(allowanceNotice(per("1"), 5 * GB, true)!.emphasis).toBe(true);
    expect(allowanceNotice(per("1"), null, true)!.emphasis).toBe(false);
    expect(allowanceNotice(fixed, 5 * GB, true)!.emphasis).toBe(false);
  });

  it("returns nothing without a plan, and nothing on an unusable per-night configuration", () => {
    expect(allowanceNotice(per("1"), 5 * GB, false)).toBeNull();
    expect(allowanceNotice(per(""), 5 * GB, true)).toBeNull();
    expect(allowanceNotice(per("1", "10", "5"), 5 * GB, true)).toBeNull(); // ceiling below floor
  });

  it("reports the plan quota in GB, not bytes", () => {
    expect(allowanceNotice(fixed, 5 * GB, true)!.planQuotaGB).toBe(5);
    expect(allowanceNotice(fixed, 500_000_000, true)!.planQuotaGB).toBe(0.5);
    // A zero or absent quota is "no allowance", never "0 GB".
    expect(allowanceNotice(fixed, 0, true)!.kind).toBe("FIXED_PLAN_HAS_NO_QUOTA");
  });
});
