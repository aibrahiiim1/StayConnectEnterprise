// THE PACKAGE FORM'S STANDING PROPERTIES.
//
// This replaces publish-form.test.tsx, whose subject was deleted. The properties it guarded are unchanged and
// still matter — no PMS-dependent eligibility rules, no price/settlement/tax field, deterministic tier order —
// so they are re-pointed at the form that took its place rather than dropped with it.
//
// What is new is the property the old form could not have: the operator never names a service-plan revision.
// Speed and allowance are ordinary fields, and the revision that requires is worked out for them.

import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { PackageForm } from "@/app/(app)/internet-packages/package-form";
import { FORBIDDEN_RULE_TYPES, SUPPORTED_RULE_TYPES } from "@/lib/commerce-form";

const initial = {
  code: "FREEWIFI",
  name: "Free WiFi",
  planCode: "GOLD",
  planRevisionID: "rev-gold",
  plan: {
    down_kbps: 2000, up_kbps: 2000, data_quota_bytes: 100000000,
    max_concurrent_devices: 4, device_limit_policy: "REJECT_NEW_DEVICE",
    speed_allocation: "PER_DEVICE", time_accounting_mode: "VALIDITY_WINDOW",
  },
  rules: [],
  tiers: [{ order: 10 }],
  duration: { end_mode: "MANUAL_END" as const },
  planSharedWith: 1,
};

describe("PackageForm", () => {
  it("NEVER asks the operator to choose a service-plan revision", () => {
    render(<PackageForm mode="add" onSave={() => {}} />);
    expect(screen.queryByLabelText("service-plan")).toBeNull();
    expect(screen.queryByLabelText(/plan.revision.*id/i)).toBeNull();
    expect(screen.queryByText(/revision/i)).toBeNull();
    // ...it asks for the thing the operator actually means.
    expect(screen.getByLabelText("down-mbps")).toBeTruthy();
    expect(screen.getByLabelText("up-mbps")).toBeTruthy();
  });

  it("loads the current settings when editing, in operator units", () => {
    render(<PackageForm mode="edit" initial={initial} onSave={() => {}} />);
    expect((screen.getByLabelText("down-mbps") as HTMLInputElement).value).toBe("2");
    expect((screen.getByLabelText("data-gb") as HTMLInputElement).value).toBe("0.1");
    expect((screen.getByLabelText("devices") as HTMLInputElement).value).toBe("4");
    expect((screen.getByLabelText("name") as HTMLInputElement).value).toBe("Free WiFi");
    // the code identifies the package and is not editable once it exists
    expect((screen.getByLabelText("code") as HTMLInputElement).readOnly).toBe(true);
  });

  it("the eligibility rule-type dropdown offers ONLY implemented types and NO PMS types", () => {
    render(<PackageForm mode="add" onSave={() => {}} />);
    fireEvent.click(screen.getByText("Add condition"));
    const typeSelect = screen.getByLabelText("rule-type-0") as HTMLSelectElement;
    const offered = Array.from(typeSelect.options).map((o) => o.value);
    expect(offered.sort()).toEqual([...SUPPORTED_RULE_TYPES].sort());
    for (const pms of FORBIDDEN_RULE_TYPES) expect(offered).not.toContain(pms);
  });

  it("has NO price / settlement / PMS / tax input anywhere (free-only by construction)", () => {
    render(<PackageForm mode="add" onSave={() => {}} />);
    for (const forbidden of [/price/i, /settlement/i, /\bpms\b/i, /\btax\b/i, /amount/i, /currency/i]) {
      expect(screen.queryByLabelText(forbidden)).toBeNull();
    }
  });

  it("hands back the plan settings and a payload with tiers in ascending order", () => {
    const onSave = vi.fn();
    render(<PackageForm mode="edit" initial={initial} onSave={onSave} />);
    fireEvent.change(screen.getByLabelText("down-mbps"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("up-mbps"), { target: { value: "5" } });
    // tiers live under Advanced; open it and add a lower-ordered one to test the sort
    fireEvent.click(screen.getByText("Advanced"));
    fireEvent.click(screen.getByText("Add step"));
    fireEvent.change(screen.getByLabelText("tier-order-1"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    expect(onSave).toHaveBeenCalledTimes(1);
    const v = onSave.mock.calls[0][0];
    expect(v.payload.grant_tiers.map((t: { order: number }) => t.order)).toEqual([5, 10]);
    // the operator's Mbps became the wire's kbps
    expect(v.plan.down_kbps).toBe(10000);
    expect(v.plan.up_kbps).toBe(5000);
    // and nothing financial rode along
    expect(JSON.stringify(v.payload).toLowerCase()).not.toMatch(/price|settlement|pms|tax|currency/);
  });

  it("says plainly when the settings are shared with other packages", () => {
    render(<PackageForm mode="edit" initial={{ ...initial, planSharedWith: 3 }} onSave={() => {}} />);
    expect(screen.getByText(/3 active packages use/i)).toBeTruthy();
    expect(screen.getByText(/updates this package only/i)).toBeTruthy();
  });

  it("does not claim sharing when the plan is this package's alone", () => {
    render(<PackageForm mode="edit" initial={initial} onSave={() => {}} />);
    expect(screen.queryByText(/active packages use/i)).toBeNull();
  });
});
