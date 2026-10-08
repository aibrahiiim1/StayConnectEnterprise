// THE PACKAGE FORM'S STANDING PROPERTIES.
//
// A package is the guest OFFER and a service plan is the technical SERVICE. The form must make that
// relationship explicit — the operator chooses a plan by name and sees what it grants — while the revision
// underneath stays invisible. An earlier version hid the plan itself and silently authored plan revisions
// from this screen, which let a package change technical settings belonging to a plan other packages share.
//
// The older properties are unchanged and still guarded: deterministic tier order and no revision id anywhere
// in the normal flow. The price section replaced "free-only by construction"; its own properties are in
// package-acquisition.test.tsx, and here a new package is still Free unless the operator says otherwise.

import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { PackageForm, type PlanOption } from "@/app/(app)/internet-packages/package-form";
import { FORBIDDEN_RULE_TYPES, SUPPORTED_RULE_TYPES } from "@/lib/commerce-form";

// The Free access device switch is a Radix Switch, which measures itself; jsdom has no ResizeObserver.
class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const plans: PlanOption[] = [
  {
    plan_id: "plan-gold", code: "GOLD", name: "Gold", current_revision_id: "rev-gold-4",
    down_kbps: 10000, up_kbps: 5000, data_quota_bytes: 100_000_000, max_concurrent_devices: 1,
  },
  {
    plan_id: "plan-silver", code: "SILVER", name: "Silver", current_revision_id: "rev-silver-1",
    down_kbps: 2000, up_kbps: 2000, max_concurrent_devices: 4,
  },
];

const initial = {
  code: "FREEWIFI",
  name: "Free WiFi",
  planID: "plan-gold",
  planRevisionID: "rev-gold-3",
  rules: [],
  tiers: [{ order: 10 }],
  duration: { end_mode: "MANUAL_END" as const },
};

describe("PackageForm", () => {
  it("ADD requires an existing service plan, chosen by name", () => {
    render(<PackageForm mode="add" plans={plans} onSave={() => {}} />);
    const sel = screen.getByTestId("service-plan") as HTMLSelectElement;
    expect(sel.required).toBe(true);
    const labels = Array.from(sel.options).map((o) => o.textContent);
    expect(labels).toContain("Gold");
    expect(labels).toContain("Silver");
    // chosen by name — the option VALUE is a plan, never a revision
    expect(Array.from(sel.options).map((o) => o.value)).toContain("plan-gold");
    expect(Array.from(sel.options).map((o) => o.value)).not.toContain("rev-gold-4");
  });

  it("shows what the selected plan grants, read-only, in the Product Owner's wording", () => {
    render(<PackageForm mode="add" plans={plans} onSave={() => {}} />);
    fireEvent.change(screen.getByTestId("service-plan"), { target: { value: "plan-gold" } });
    expect(screen.getByTestId("plan-summary").textContent)
      .toBe("10 Mbps down · 5 Mbps up · 100 MB · 1 device");
    // ...and the technical values are NOT editable here
    for (const gone of ["down-mbps", "up-mbps", "data-gb", "time-hours", "devices", "speed-allocation"]) {
      expect(screen.queryByTestId(gone)).toBeNull();
    }
  });

  it("ADD refuses to save without a plan, and cannot create one", () => {
    const onSave = vi.fn();
    render(<PackageForm mode="add" plans={plans} onSave={onSave} />);
    fireEvent.change(screen.getByTestId("code"), { target: { value: "X" } });
    fireEvent.click(screen.getByRole("button", { name: /add package/i }));

    // Nothing is saved, so nothing downstream can create a Service Plan from a Package.
    expect(onSave).not.toHaveBeenCalled();
    // The field itself is what refuses: it is required and currently invalid.
    const sel = screen.getByTestId("service-plan") as HTMLSelectElement;
    expect(sel.required).toBe(true);
    expect(sel.checkValidity()).toBe(false);

    // ...and choosing one makes the same click succeed, so the refusal is about the plan and nothing else.
    fireEvent.change(sel, { target: { value: "plan-gold" } });
    fireEvent.click(screen.getByRole("button", { name: /add package/i }));
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].selectedPlanID).toBe("plan-gold");
  });

  it("explains what to do when the site has no service plans, instead of an empty dropdown", () => {
    render(<PackageForm mode="add" plans={[]} onSave={() => {}} />);
    expect(screen.queryByTestId("service-plan")).toBeNull();
    expect(screen.getByText(/no service plans yet/i)).toBeTruthy();
    expect(screen.getByRole("link", { name: /create a service plan/i })).toBeTruthy();
  });

  it("EDIT loads the plan the package currently uses", () => {
    render(<PackageForm mode="edit" initial={initial} plans={plans} onSave={() => {}} />);
    expect((screen.getByTestId("service-plan") as HTMLSelectElement).value).toBe("plan-gold");
    expect(screen.getByTestId("plan-summary").textContent).toContain("10 Mbps down");
    expect((screen.getByTestId("code") as HTMLInputElement).readOnly).toBe(true);
  });

  it("EDIT lets the operator switch to another existing plan, and reports which", () => {
    const onSave = vi.fn();
    render(<PackageForm mode="edit" initial={initial} plans={plans} onSave={onSave} />);
    fireEvent.change(screen.getByTestId("service-plan"), { target: { value: "plan-silver" } });
    expect(screen.getByTestId("plan-summary").textContent).toContain("2 Mbps down");
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].selectedPlanID).toBe("plan-silver");
  });

  it("NO revision id appears anywhere in the normal flow", () => {
    const { container } = render(<PackageForm mode="edit" initial={initial} plans={plans} onSave={() => {}} />);
    const html = container.innerHTML;
    for (const rev of ["rev-gold-3", "rev-gold-4", "rev-silver-1"]) {
      expect(html).not.toContain(rev);
    }
    expect(html.toLowerCase()).not.toContain("revision");
  });

  it("the eligibility rule-type dropdown offers ONLY implemented types and NO PMS types", () => {
    render(<PackageForm mode="add" plans={plans} onSave={() => {}}
      modules={{ modules: { hospitality: { id: "hospitality", label: "Hospitality", licensed: true, enabled: true, ready: true, readiness: [], reasons: [] } as never } }} />);
    fireEvent.click(screen.getByText("Add condition"));
    const typeSelect = screen.getByTestId("rule-type-0") as HTMLSelectElement;
    const offered = Array.from(typeSelect.options).map((o) => o.value);
    // The audience is a dedicated control, not a row; every other supported type is a row.
    expect(offered.sort()).toEqual([...SUPPORTED_RULE_TYPES].filter((t) => t !== "CLIENT_GROUP").sort());
    for (const pms of FORBIDDEN_RULE_TYPES) expect(offered).not.toContain(pms);
  });

  // THE AUDIENCE AND THE FREE ALLOWANCE (ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §4, §5). Each is one
  // ordinary eligibility rule presented as a named control, and each must round-trip through Edit: a stored
  // audience read back, shown, and republished unchanged by a rename.
  describe("Audience", () => {
    const groups = [
      { id: "g-emp", name: "Employees", enabled: true, priority: 10 },
      { id: "g-par", name: "Partners", enabled: true, priority: 50 },
    ];
    const ready = () => {
      fireEvent.change(screen.getByTestId("code"), { target: { value: "X" } });
      fireEvent.change(screen.getByTestId("service-plan"), { target: { value: "plan-gold" } });
    };

    it("starts at Everyone and publishes no audience rule", () => {
      const onSave = vi.fn();
      render(<PackageForm mode="add" plans={plans} onSave={onSave} clientGroups={groups} />);
      expect((screen.getByTestId("audience") as HTMLSelectElement).value).toBe("everyone");
      ready();
      fireEvent.click(screen.getByRole("button", { name: /add package/i }));
      expect(onSave.mock.calls[0][0].payload.eligibility_rules).toEqual([]);
    });

    it("Only these groups publishes one CLIENT_GROUP rule with the ticked ids", () => {
      const onSave = vi.fn();
      render(<PackageForm mode="add" plans={plans} onSave={onSave} clientGroups={groups} />);
      fireEvent.change(screen.getByTestId("audience"), { target: { value: "groups" } });
      fireEvent.click(screen.getByTestId("audience-group-g-emp"));
      ready();
      fireEvent.click(screen.getByRole("button", { name: /add package/i }));
      expect(onSave.mock.calls[0][0].payload.eligibility_rules).toEqual([
        { type: "CLIENT_GROUP", value: { group_ids: ["g-emp"] } },
      ]);
    });

    it("refuses Only these groups with nothing ticked, in words", () => {
      const onSave = vi.fn();
      render(<PackageForm mode="add" plans={plans} onSave={onSave} clientGroups={groups} />);
      fireEvent.change(screen.getByTestId("audience"), { target: { value: "groups" } });
      ready();
      fireEvent.click(screen.getByRole("button", { name: /add package/i }));
      expect(onSave).not.toHaveBeenCalled();
      expect(screen.getByRole("alert").textContent).toMatch(/at least one client group/);
    });

    it("Public clients only and These groups and public clients map to the public flag", () => {
      const onSave = vi.fn();
      render(<PackageForm mode="add" plans={plans} onSave={onSave} clientGroups={groups} />);
      fireEvent.change(screen.getByTestId("audience"), { target: { value: "public" } });
      ready();
      fireEvent.click(screen.getByRole("button", { name: /add package/i }));
      expect(onSave.mock.calls[0][0].payload.eligibility_rules).toEqual([{ type: "CLIENT_GROUP", value: { public: true } }]);

      fireEvent.change(screen.getByTestId("audience"), { target: { value: "groups_public" } });
      fireEvent.click(screen.getByTestId("audience-group-g-par"));
      fireEvent.click(screen.getByRole("button", { name: /add package/i }));
      expect(onSave.mock.calls[1][0].payload.eligibility_rules).toEqual([
        { type: "CLIENT_GROUP", value: { group_ids: ["g-par"], public: true } },
      ]);
    });

    it("EDIT reads a stored audience back and republishes it unchanged, without a row for it", () => {
      const onSave = vi.fn();
      render(<PackageForm mode="edit" plans={plans} onSave={onSave} clientGroups={groups} initial={{
        ...initial,
        rules: [{ type: "CLIENT_GROUP", group_ids: ["g-emp", "g-par"], public: true }, { type: "AUTH_METHOD", methods: "voucher" }],
      }} />);
      expect((screen.getByTestId("audience") as HTMLSelectElement).value).toBe("groups_public");
      expect((screen.getByTestId("audience-group-g-emp") as HTMLInputElement).checked).toBe(true);
      expect((screen.getByTestId("audience-group-g-par") as HTMLInputElement).checked).toBe(true);
      // The audience is not also a generic condition row; the sign-in condition still is.
      expect(screen.queryByTestId("rule-type-0")).toBeNull();
      expect((screen.getByTestId("rule-type-1") as HTMLSelectElement).value).toBe("AUTH_METHOD");
      fireEvent.change(screen.getByTestId("name"), { target: { value: "Renamed" } });
      fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
      expect(onSave.mock.calls[0][0].payload.eligibility_rules).toEqual([
        { type: "CLIENT_GROUP", value: { group_ids: ["g-emp", "g-par"], public: true } },
        { type: "AUTH_METHOD", value: { methods: ["voucher"] } },
      ]);
    });

    it("says where to create a group when the site has none", () => {
      render(<PackageForm mode="add" plans={plans} onSave={() => {}} clientGroups={[]} />);
      fireEvent.change(screen.getByTestId("audience"), { target: { value: "groups" } });
      expect(screen.getByRole("link", { name: /Create one under Client access/ })).toHaveAttribute("href", "/client-groups");
    });
  });

  describe("Free access", () => {
    const ready = () => {
      fireEvent.change(screen.getByTestId("code"), { target: { value: "X" } });
      fireEvent.change(screen.getByTestId("service-plan"), { target: { value: "plan-gold" } });
    };

    it("Once per client, ever publishes forbids_prior and counts the device on a Free package", () => {
      const onSave = vi.fn();
      render(<PackageForm mode="add" plans={plans} onSave={onSave} />);
      expect((screen.getByTestId("free-access") as HTMLSelectElement).value).toBe("unlimited");
      fireEvent.change(screen.getByTestId("free-access"), { target: { value: "once" } });
      expect(screen.getByRole("switch", { name: "Count the device too" })).toHaveAttribute("aria-checked", "true");
      ready();
      fireEvent.click(screen.getByRole("button", { name: /add package/i }));
      expect(onSave.mock.calls[0][0].payload.eligibility_rules).toEqual([
        { type: "PRIOR_PURCHASE", value: { forbids_prior: true, also_by_device: true } },
      ]);
    });

    it("Once every N hours publishes within_hours, and the device switch can be turned off", () => {
      const onSave = vi.fn();
      render(<PackageForm mode="add" plans={plans} onSave={onSave} />);
      fireEvent.change(screen.getByTestId("free-access"), { target: { value: "every" } });
      expect((screen.getByTestId("free-access-hours") as HTMLInputElement).value).toBe("24");
      fireEvent.change(screen.getByTestId("free-access-hours"), { target: { value: "12" } });
      fireEvent.click(screen.getByRole("switch", { name: "Count the device too" }));
      ready();
      fireEvent.click(screen.getByRole("button", { name: /add package/i }));
      expect(onSave.mock.calls[0][0].payload.eligibility_rules).toEqual([
        { type: "PRIOR_PURCHASE", value: { forbids_prior: true, within_hours: 12, also_by_device: false } },
      ]);
    });

    it("refuses a window outside one hour to one year", () => {
      // The field carries the bounds itself, so the browser refuses before the form does (and the message the
      // form would give is held in commerce-form.test.ts, validateRule).
      const onSave = vi.fn();
      render(<PackageForm mode="add" plans={plans} onSave={onSave} />);
      fireEvent.change(screen.getByTestId("free-access"), { target: { value: "every" } });
      const hours = screen.getByTestId("free-access-hours") as HTMLInputElement;
      expect(hours.min).toBe("1");
      expect(hours.max).toBe("8760");
      fireEvent.change(hours, { target: { value: "9000" } });
      ready();
      fireEvent.click(screen.getByRole("button", { name: /add package/i }));
      expect(onSave).not.toHaveBeenCalled();
      expect(hours.checkValidity()).toBe(false);
    });

    it("EDIT reads a stored allowance back, and requires_prior stays an ordinary row", () => {
      const onSave = vi.fn();
      render(<PackageForm mode="edit" plans={plans} onSave={onSave} initial={{
        ...initial,
        rules: [
          { type: "PRIOR_PURCHASE", mode: "forbids_prior", within_hours: "24", also_by_device: false },
          { type: "PRIOR_PURCHASE", mode: "requires_prior" },
        ],
      }} />);
      expect((screen.getByTestId("free-access") as HTMLSelectElement).value).toBe("every");
      expect((screen.getByTestId("free-access-hours") as HTMLInputElement).value).toBe("24");
      expect(screen.getByRole("switch", { name: "Count the device too" })).toHaveAttribute("aria-checked", "false");
      expect(screen.queryByTestId("rule-type-0")).toBeNull();
      expect((screen.getByTestId("rule-mode-1") as HTMLSelectElement).value).toBe("requires_prior");
      fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
      expect(onSave.mock.calls[0][0].payload.eligibility_rules).toEqual([
        { type: "PRIOR_PURCHASE", value: { forbids_prior: true, within_hours: 24, also_by_device: false } },
        { type: "PRIOR_PURCHASE", value: { requires_prior: true } },
      ]);
    });

    it("a stored allowance that never named the device switch is republished without pinning one", () => {
      // The server's default (true on a free package) stays the server's: the form shows it as on, and does
      // not write a value the operator never set.
      const onSave = vi.fn();
      render(<PackageForm mode="edit" plans={plans} onSave={onSave} initial={{
        ...initial, rules: [{ type: "PRIOR_PURCHASE", mode: "forbids_prior" }],
      }} />);
      expect((screen.getByTestId("free-access") as HTMLSelectElement).value).toBe("once");
      expect(screen.getByRole("switch", { name: "Count the device too" })).toHaveAttribute("aria-checked", "true");
      fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
      expect(onSave.mock.calls[0][0].payload.eligibility_rules).toEqual([
        { type: "PRIOR_PURCHASE", value: { forbids_prior: true } },
      ]);
    });
  });

  // A SITE WITHOUT HOSPITALITY IS NOT OFFERED HOTEL CONDITIONS OR A PER-NIGHT ALLOWANCE.
  it("without Hospitality: no hotel conditions and no per-night allowance", () => {
    render(<PackageForm mode="add" plans={plans} onSave={() => {}} modules={{ modules: {} }} />);
    fireEvent.click(screen.getByText("Add condition"));
    const offered = Array.from((screen.getByTestId("rule-type-0") as HTMLSelectElement).options).map((o) => o.value);
    expect(offered).not.toContain("STAY_LENGTH");
    expect(offered).not.toContain("ROOM_TYPE");
    expect(screen.queryByRole("group", { name: "Hotel (PMS stay)" })).toBeNull();
    const modes = Array.from((screen.getByTestId("allocation-mode") as HTMLSelectElement).options).map((o) => o.value);
    expect(modes).not.toContain("PER_STAY_NIGHT");
  });

  it("a new package starts Free, and saving it untouched publishes a Free package", () => {
    const onSave = vi.fn();
    render(<PackageForm mode="add" plans={plans} onSave={onSave} />);
    // No Paid access here, so there is no price to set.
    expect(screen.queryByLabelText("Price")).toBeNull();
    expect((screen.getByTestId("method-check-NOT_REQUIRED") as HTMLInputElement).checked).toBe(true);
    fireEvent.change(screen.getByTestId("code"), { target: { value: "X" } });
    fireEvent.change(screen.getByTestId("service-plan"), { target: { value: "plan-gold" } });
    fireEvent.click(screen.getByRole("button", { name: /add package/i }));
    const payload = onSave.mock.calls[0][0].payload;
    expect(payload.price_minor).toBe(0);
    expect(payload.acquisition_methods).toEqual(["NOT_REQUIRED"]);
    expect(payload).not.toHaveProperty("room_charge_mappings");
  });

  it("carries the existing rules and tiers through a save unchanged", () => {
    const onSave = vi.fn();
    render(<PackageForm mode="edit" plans={plans} onSave={onSave} initial={{
      ...initial,
      rules: [{ type: "AUTH_METHOD", methods: "pms, voucher" }],
      tiers: [{ order: 10, down_kbps: 5000 }, { order: 5, down_kbps: 1000 }],
    }} />);
    fireEvent.change(screen.getByTestId("name"), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    const payload = onSave.mock.calls[0][0].payload;
    expect(payload.eligibility_rules).toEqual([
      { type: "AUTH_METHOD", value: { methods: ["pms", "voucher"] } },
    ]);
    expect(payload.grant_tiers.map((t: { order: number }) => t.order)).toEqual([5, 10]);
    // A package loaded with no price section is Free, and is saved as Free.
    expect(payload.acquisition_methods).toEqual(["NOT_REQUIRED"]);
    expect(payload.price_minor).toBe(0);
  });
});
