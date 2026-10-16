import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";

// THE DEFAULT LIMIT FOLLOWS THE PRICE UNTIL THE OPERATOR DECIDES: a Free package starts at once per client, a
// priced one at repeatable, and an explicit choice survives whatever the price does afterwards. And a package
// priced in a currency no active account settles says so beside the price.

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));

import { PackageForm } from "@/app/(app)/internet-packages/package-form";

const plans = [{ plan_id: "plan-gold", code: "GOLD", name: "Gold", current_revision_id: "rev-1", data_quota_bytes: null }] as never;
const on = { id: "x", label: "x", licensed: true, enabled: true, ready: true, effective: true, readiness: [], reasons: [] };
const modules = { modules: { paid_access: { ...on, id: "paid_access" }, card_payment: { ...on, id: "card_payment" } } } as never;

const usdAccount = { currency: "USD", provider: "stripe", provider_converts: false };

function ready() {
  fireEvent.change(screen.getByTestId("code"), { target: { value: "X" } });
  fireEvent.change(screen.getByTestId("service-plan"), { target: { value: "plan-gold" } });
}

describe("Package defaults", () => {
  it("a new Free package is once per client; giving it a price makes it repeatable; free again restores the limit", () => {
    const onSave = vi.fn();
    render(<PackageForm mode="add" plans={plans} onSave={onSave} modules={modules} cardAccount={usdAccount} />);
    expect((screen.getByTestId("free-access") as HTMLSelectElement).value).toBe("once");
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "5" } });
    expect((screen.getByTestId("free-access") as HTMLSelectElement).value).toBe("unlimited");
    expect(screen.getByTestId("free-access-repeatable-note")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "0" } });
    expect((screen.getByTestId("free-access") as HTMLSelectElement).value).toBe("once");
  });

  it("an explicit choice is the operator's, whatever the price does", () => {
    render(<PackageForm mode="add" plans={plans} onSave={() => {}} modules={modules} cardAccount={usdAccount} />);
    fireEvent.change(screen.getByTestId("free-access"), { target: { value: "every" } });
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "5" } });
    expect((screen.getByTestId("free-access") as HTMLSelectElement).value).toBe("every");
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "0" } });
    expect((screen.getByTestId("free-access") as HTMLSelectElement).value).toBe("every");
  });

  it("an existing package keeps exactly its rules, priced or not", () => {
    const initial = {
      code: "DAY", name: "Day", planID: "plan-gold", planRevisionID: "rev-1", rules: [], tiers: [{ order: 10 }],
      duration: { end_mode: "MANUAL_END" }, acquisition: { price: "5", currency: "USD", methods: ["ONLINE_PAYMENT"], room_charge: [] },
    } as never;
    render(<PackageForm mode="edit" initial={initial} plans={plans} onSave={() => {}} modules={modules} cardAccount={usdAccount} />);
    expect((screen.getByTestId("free-access") as HTMLSelectElement).value).toBe("unlimited");
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "0" } });
    expect((screen.getByTestId("free-access") as HTMLSelectElement).value).toBe("unlimited");
  });

  it("says when the DEFAULT account does not settle the package's currency, and nothing when it is unknown", () => {
    const { unmount } = render(<PackageForm mode="add" plans={plans} onSave={() => {}} modules={modules}
      cardAccount={{ currency: "GBP", provider: "stripe", provider_converts: false }} />);
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "5" } });
    fireEvent.click(screen.getByTestId("method-check-ONLINE_PAYMENT"));
    expect(screen.getByTestId("card-currency-note").textContent).toMatch(/default payment account settles GBP, not USD/);
    unmount();
    render(<PackageForm mode="add" plans={plans} onSave={() => {}} modules={modules} cardAccount={null} />);
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "5" } });
    fireEvent.click(screen.getByTestId("method-check-ONLINE_PAYMENT"));
    expect(screen.queryByTestId("card-currency-note")).toBeNull();
    ready();
  });

  it("a Stripe default account set to convert takes USD with no warning; Paymob set to convert does not", () => {
    const { unmount } = render(<PackageForm mode="add" plans={plans} onSave={() => {}} modules={modules}
      cardAccount={{ currency: "GBP", provider: "stripe", provider_converts: true }} />);
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "5" } });
    fireEvent.click(screen.getByTestId("method-check-ONLINE_PAYMENT"));
    expect(screen.queryByTestId("card-currency-note")).toBeNull();
    unmount();
    render(<PackageForm mode="add" plans={plans} onSave={() => {}} modules={modules}
      cardAccount={{ currency: "EGP", provider: "paymob", provider_converts: true }} />);
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "5" } });
    fireEvent.click(screen.getByTestId("method-check-ONLINE_PAYMENT"));
    expect(screen.getByTestId("card-currency-note").textContent).toMatch(/its provider does not convert USD/);
  });

  it("says when there is no default active account at all", () => {
    render(<PackageForm mode="add" plans={plans} onSave={() => {}} modules={modules} cardAccount="none" />);
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "5" } });
    fireEvent.click(screen.getByTestId("method-check-ONLINE_PAYMENT"));
    expect(screen.getByTestId("card-currency-note").textContent).toMatch(/no default active payment account/);
  });
});
