import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { PublishPackageForm } from "@/app/(app)/commercial-packages/publish-form";
import { FORBIDDEN_RULE_TYPES, SUPPORTED_RULE_TYPES } from "@/lib/commerce-form";

const plans = [
  { plan_id: "p1", code: "GOLD", current_revision_id: "rev-gold" },
  { plan_id: "p2", code: "SILVER", current_revision_id: "rev-silver" },
  { plan_id: "p3", code: "NOREV", current_revision_id: "" },
];

describe("PublishPackageForm", () => {
  it("populates the plan selector from the API (no raw UUID typing) and hides plans without a current revision", () => {
    render(<PublishPackageForm plans={plans} onPublish={() => {}} />);
    const select = screen.getByLabelText("service-plan") as HTMLSelectElement;
    const optionValues = Array.from(select.options).map((o) => o.value);
    expect(optionValues).toContain("rev-gold");
    expect(optionValues).toContain("rev-silver");
    // placeholder ("") + exactly the two plans that HAVE a current revision (NOREV excluded)
    expect(select.options.length).toBe(3);
    expect(optionValues.filter((v) => v !== "").sort()).toEqual(["rev-gold", "rev-silver"]);
    const optionText = Array.from(select.options).map((o) => o.textContent);
    expect(optionText.some((t) => t?.includes("NOREV"))).toBe(false);
    // there is no free-text plan-revision UUID input
    expect(screen.queryByLabelText(/plan.revision.*id/i)).toBeNull();
  });

  it("the eligibility rule-type dropdown offers ONLY supported Phase-2 types and NO PMS types", () => {
    render(<PublishPackageForm plans={plans} onPublish={() => {}} />);
    fireEvent.click(screen.getByText("Add rule"));
    const typeSelect = screen.getByLabelText("rule-type-0") as HTMLSelectElement;
    const offered = Array.from(typeSelect.options).map((o) => o.value);
    expect(offered.sort()).toEqual([...SUPPORTED_RULE_TYPES].sort());
    for (const pms of FORBIDDEN_RULE_TYPES) expect(offered).not.toContain(pms);
  });

  it("has NO price / settlement / PMS / tax input anywhere (free-only by construction)", () => {
    render(<PublishPackageForm plans={plans} onPublish={() => {}} />);
    for (const forbidden of [/price/i, /settlement/i, /\bpms\b/i, /\btax\b/i, /amount/i, /currency/i]) {
      expect(screen.queryByLabelText(forbidden)).toBeNull();
    }
  });

  it("submits a payload with tiers in deterministic ascending order", () => {
    const onPublish = vi.fn();
    render(<PublishPackageForm plans={plans} onPublish={onPublish} />);
    fireEvent.change(screen.getByLabelText("code"), { target: { value: "FREEWIFI" } });
    fireEvent.change(screen.getByLabelText("service-plan"), { target: { value: "rev-gold" } });
    // default tier order 10; add a second tier and give it a LOWER order (5) to test sorting
    fireEvent.change(screen.getByLabelText("tier-down-0"), { target: { value: "5000" } });
    fireEvent.click(screen.getByText("Add tier"));
    fireEvent.change(screen.getByLabelText("tier-order-1"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("tier-down-1"), { target: { value: "1000" } });
    fireEvent.click(screen.getByRole("button", { name: /publish/i }));
    expect(onPublish).toHaveBeenCalledTimes(1);
    const payload = onPublish.mock.calls[0][0];
    expect(payload.grant_tiers.map((t: { order: number }) => t.order)).toEqual([5, 10]);
    expect(payload.service_plan_revision_id).toBe("rev-gold");
    // no forbidden money/PMS keys
    expect(JSON.stringify(payload).toLowerCase()).not.toMatch(/price|settlement|pms|tax|currency/);
  });

  it("shows a validation error for a VALIDITY_WINDOW with no duration and does not submit", () => {
    const onPublish = vi.fn();
    render(<PublishPackageForm plans={plans} onPublish={onPublish} />);
    fireEvent.change(screen.getByLabelText("code"), { target: { value: "X" } });
    fireEvent.change(screen.getByLabelText("service-plan"), { target: { value: "rev-gold" } });
    fireEvent.change(screen.getByLabelText("end-mode"), { target: { value: "VALIDITY_WINDOW" } });
    fireEvent.click(screen.getByRole("button", { name: /publish/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/positive/i);
    expect(onPublish).not.toHaveBeenCalled();
  });

  it("shows a validation error for an inverted sale window and does not submit", () => {
    const onPublish = vi.fn();
    render(<PublishPackageForm plans={plans} onPublish={onPublish} />);
    fireEvent.change(screen.getByLabelText("code"), { target: { value: "X" } });
    fireEvent.change(screen.getByLabelText("service-plan"), { target: { value: "rev-gold" } });
    fireEvent.change(screen.getByLabelText("visible-from"), { target: { value: "2026-08-02T00:00" } });
    fireEvent.change(screen.getByLabelText("visible-until"), { target: { value: "2026-08-01T00:00" } });
    fireEvent.click(screen.getByRole("button", { name: /publish/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/before/i);
    expect(onPublish).not.toHaveBeenCalled();
  });

  it("the end-mode dropdown offers only capability-enabled modes (no AT_CHECKOUT / REST_OF_STAY)", () => {
    render(<PublishPackageForm plans={plans} onPublish={() => {}} />);
    const modes = Array.from((screen.getByLabelText("end-mode") as HTMLSelectElement).options).map((o) => o.value);
    expect(modes).toEqual(["MANUAL_END", "VALIDITY_WINDOW", "FIXED_AT"]);
    for (const bad of ["AT_CHECKOUT", "GRACE_AFTER_CHECKOUT", "REST_OF_STAY", "EARLIEST_OF_FIXED_AND_CHECKOUT"]) {
      expect(modes).not.toContain(bad);
    }
  });

  it("serializes a DATE_WINDOW rule via the typed editor", () => {
    const onPublish = vi.fn();
    render(<PublishPackageForm plans={plans} onPublish={onPublish} />);
    fireEvent.change(screen.getByLabelText("code"), { target: { value: "X" } });
    fireEvent.change(screen.getByLabelText("service-plan"), { target: { value: "rev-gold" } });
    fireEvent.click(screen.getByText("Add rule"));
    fireEvent.change(screen.getByLabelText("rule-type-0"), { target: { value: "DATE_WINDOW" } });
    const row = screen.getByTestId("rule-0");
    fireEvent.change(within(row).getByLabelText("rule-from-0"), { target: { value: "2026-08-01T00:00" } });
    fireEvent.click(screen.getByRole("button", { name: /publish/i }));
    const payload = onPublish.mock.calls[0][0];
    expect(payload.eligibility_rules[0].type).toBe("DATE_WINDOW");
    // serialized to a valid UTC ISO instant (exact value depends on the runner's timezone)
    expect(payload.eligibility_rules[0].value.from).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    expect(new Date(payload.eligibility_rules[0].value.from).getUTCFullYear()).toBe(2026);
  });
});
