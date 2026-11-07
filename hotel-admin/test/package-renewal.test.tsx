import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));

import { PackageForm } from "@/app/(app)/internet-packages/package-form";

const plans = [{ plan_id: "plan-gold", code: "GOLD", name: "Gold", current_revision_id: "rev-1", data_quota_bytes: null }] as never;
const on = { id: "x", label: "x", licensed: true, enabled: true, ready: true, effective: true, readiness: [], reasons: [] };
const hotelModules = { modules: { hospitality: { ...on, id: "hospitality" }, paid_access: { ...on, id: "paid_access" } } } as never;
const plainModules = { modules: { paid_access: { ...on, id: "paid_access" } } } as never;

const oneDay = {
  code: "DAY", name: "One Day", planID: "plan-gold", planRevisionID: "rev-1", rules: [], tiers: [{ order: 10 }],
  duration: { end_mode: "VALIDITY_WINDOW", duration_seconds: 86400 },
  acquisition: { price: "0", currency: "USD", methods: ["NOT_REQUIRED"], room_charge: [] },
};

function save() {
  fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
}

describe("Package renewal (room stays)", () => {
  it("is off by default and sends renewable=false with no limit", () => {
    const onSave = vi.fn();
    render(<PackageForm mode="edit" initial={oneDay as never} plans={plans} onSave={onSave} modules={hotelModules} />);
    expect(screen.getByTestId("renewal")).toBeInTheDocument();
    expect(screen.queryByTestId("max-per-stay")).toBeNull();
    save();
    expect(onSave).toHaveBeenCalledTimes(1);
    const p = onSave.mock.calls[0][0].payload;
    expect(p.renewable).toBe(false);
    expect(p.max_purchases_per_stay).toBeNull();
  });

  it("loads an existing setting and saves a changed maximum; empty is no limit", () => {
    const onSave = vi.fn();
    render(<PackageForm mode="edit" initial={{ ...oneDay, renewable: true, maxPurchasesPerStay: 3 } as never} plans={plans}
      onSave={onSave} modules={hotelModules} />);
    const max = screen.getByTestId("max-per-stay") as HTMLInputElement;
    expect(max.value).toBe("3");
    fireEvent.change(max, { target: { value: "5" } });
    save();
    expect(onSave.mock.calls[0][0].payload).toMatchObject({ renewable: true, max_purchases_per_stay: 5 });
    fireEvent.change(max, { target: { value: "" } });
    save();
    expect(onSave.mock.calls[1][0].payload).toMatchObject({ renewable: true, max_purchases_per_stay: null });
  });

  it("refuses a maximum below 1", () => {
    const onSave = vi.fn();
    render(<PackageForm mode="edit" initial={{ ...oneDay, renewable: true } as never} plans={plans} onSave={onSave} modules={hotelModules} />);
    fireEvent.change(screen.getByTestId("max-per-stay"), { target: { value: "0" } });
    save();
    expect(onSave).not.toHaveBeenCalled();
    expect((screen.getByTestId("max-per-stay") as HTMLInputElement).validity.rangeUnderflow).toBe(true);
  });

  it("without the hotel module the form neither shows nor sends it, so the server keeps the current values", () => {
    const onSave = vi.fn();
    render(<PackageForm mode="edit" initial={{ ...oneDay, renewable: true, maxPurchasesPerStay: 2 } as never} plans={plans}
      onSave={onSave} modules={plainModules} />);
    expect(screen.queryByTestId("renewal")).toBeNull();
    save();
    const p = onSave.mock.calls[0][0].payload;
    expect(p.renewable).toBeUndefined();
    expect(p.max_purchases_per_stay).toBeUndefined();
  });
});
