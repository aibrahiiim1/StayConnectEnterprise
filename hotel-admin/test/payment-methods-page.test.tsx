// INTERNET OFFERING → PAYMENT METHODS.
//
// Pinned here: the four methods and nothing else (no Cash), each module-backed method reports the one state
// that stands in its way with readiness codes in words (unknown codes verbatim), the Card payment configuration
// appears only once Card payment is manageable, credentials are write-only (no stored value is ever rendered,
// every input opens empty, and only what the operator typed is sent), LIVE is disabled where the provider does
// not allow it, and a role without write access is offered no control.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

// The role table does not carry payment-providers for every role yet (the Delivery Owner wires it), so a
// synthetic "payments_reader" stands in for a role that may read and not write. site_admin and the real
// matrix are untouched.
vi.mock("@/lib/roles", async (orig) => {
  const actual = await (orig() as Promise<typeof import("@/lib/roles")>);
  return {
    ...actual,
    canRead: (r: string, roles: string[]) => roles.includes("payments_reader") || actual.canRead(r, roles),
  };
});

import { api } from "@/lib/api";
import PaymentMethodsPage from "@/app/(app)/payment-methods/page";
import { domainProblem } from "@/app/(app)/payment-methods/card-config";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;
const put = api.put as unknown as ReturnType<typeof vi.fn>;

function mod(id: string, over: Record<string, unknown> = {}) {
  return {
    id, label: id, requires: [], deployed: true, authorized: true, licensed: true, switchable: true,
    enabled: true, ready: true, effective: true, manageable: true, reasons: [], readiness: [], ...over,
  };
}

const PROVIDERS = {
  providers: [
    {
      id: "stripe", label: "Stripe", credential_keys: [
        { key: "publishable_key", label: "Publishable key", secret: false, required: true },
        { key: "secret_key", label: "Secret key", secret: true, required: true },
        { key: "webhook_secret", label: "Webhook signing secret", secret: true, required: false },
      ],
      hosted_domains: ["checkout.stripe.com"], live_allowed: false,
    },
    {
      id: "paymob", label: "Paymob", regions: ["egypt", "uae"], credential_keys: [
        { key: "api_key", label: "API key", secret: true, required: true },
      ],
      hosted_domains: ["accept.paymob.com"], live_allowed: true,
    },
  ],
  engine_unready: "",
};

const ACCOUNT = {
  id: "acc-1", provider: "stripe", merchant_account_ref: "acct_123", display_name: "Main Stripe", currency: "USD",
  mode: "TEST", status: "ACTIVE", is_default: true, updated_at: "2026-09-20T10:00:00Z", has_credentials: true,
  credential_keys_set: ["publishable_key", "secret_key"],
};

const SETTINGS = {
  settings: { checkout_expiry_minutes: 30, reconcile_grace_minutes: 60, config_version: 1, is_default: true },
  bounds: {
    checkout_expiry_minutes: { min: 5, max: 120, default: 30 },
    reconcile_grace_minutes: { min: 10, max: 1440, default: 60 },
  },
};

function routes(roles: string[], modules: Record<string, unknown>) {
  g.mockImplementation((path: string) => {
    switch (path) {
      case "/auth/whoami": return Promise.resolve({ roles });
      case "/modules": return Promise.resolve({ site_type: "hotel", license_state: "ACTIVE", modules, deployment: {} });
      case "/payment-providers/providers": return Promise.resolve(PROVIDERS);
      case "/payment-providers/accounts":
        return Promise.resolve({ accounts: [ACCOUNT], ready: true, readiness: [], extra_domains: ["pay.example.com"] });
      case "/payment-providers/settings": return Promise.resolve(SETTINGS);
      case "/payment-providers/changes":
        return Promise.resolve({ changes: [{ kind: "settings", changed_at: "2026-09-21T08:00:00Z", changed_by: "admin@site", reason: "Longer checkout", detail: { checkout_expiry_minutes: 30 } }] });
    }
    return Promise.reject(new Error(`unexpected GET ${path}`));
  });
}

beforeEach(() => { vi.clearAllMocks(); });

describe("Payment methods — the four methods", () => {
  it("shows exactly Free, Voucher, Card payment and Room charge, and no Cash", async () => {
    routes(["site_admin"], { card_payment: mod("card_payment"), room_charge: mod("room_charge") });
    render(<PaymentMethodsPage />);
    for (const t of ["Free", "Voucher", "Card payment", "Room charge"]) {
      expect(await screen.findByTestId(`method-${t}`)).toBeInTheDocument();
    }
    expect(screen.queryByText(/cash/i)).not.toBeInTheDocument();
    expect(within(screen.getByTestId("method-Voucher")).getByRole("link", { name: /vouchers/i })).toHaveAttribute("href", "/vouchers");
    expect(within(screen.getByTestId("method-Room charge")).getByRole("link", { name: /room charge/i })).toHaveAttribute("href", "/room-charge");
  });

  it("derives each module state: not licensed, switched off, not ready in words, ready", async () => {
    routes(["site_admin"], {
      card_payment: mod("card_payment", { ready: false, effective: false, readiness: ["NO_ACTIVE_PAYMENT_ACCOUNT", "PAYMENT_KEY_MISSING", "SOMETHING_NEW"] }),
      room_charge: mod("room_charge", { licensed: false, enabled: false, ready: false, effective: false, manageable: false }),
    });
    render(<PaymentMethodsPage />);
    const card = await screen.findByTestId("method-Card payment");
    expect(within(card).getByText("Not ready")).toBeInTheDocument();
    expect(within(card).getByText("No active provider account")).toBeInTheDocument();
    expect(within(card).getByText("This appliance has no payment key")).toBeInTheDocument();
    // An unknown code is shown verbatim, in a code element.
    expect(within(card).getByText("SOMETHING_NEW").tagName).toBe("CODE");
    expect(within(screen.getByTestId("method-Room charge")).getByText("Not licensed")).toBeInTheDocument();
  });

  it("says Switched off with a link to Modules, and Ready when everything is in place", async () => {
    routes(["site_admin"], {
      card_payment: mod("card_payment", { enabled: false, effective: false }),
      room_charge: mod("room_charge"),
    });
    render(<PaymentMethodsPage />);
    const card = await screen.findByTestId("method-Card payment");
    expect(within(card).getByText("Switched off")).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: /modules/i })).toHaveAttribute("href", "/modules");
    expect(within(screen.getByTestId("method-Room charge")).getByText("Ready")).toBeInTheDocument();
  });

  it("hides the Card payment configuration until Card payment is manageable", async () => {
    routes(["site_admin"], { card_payment: mod("card_payment", { licensed: false, manageable: false }), room_charge: mod("room_charge") });
    render(<PaymentMethodsPage />);
    expect(await screen.findByText(/appear here once Card payment is licensed/i)).toBeInTheDocument();
    expect(g).not.toHaveBeenCalledWith("/payment-providers/accounts");
  });
});

describe("Payment methods — Card payment configuration", () => {
  it("never renders a stored credential, opens every credential input empty, and sends only what was typed", async () => {
    routes(["site_admin"], { card_payment: mod("card_payment"), room_charge: mod("room_charge") });
    post.mockResolvedValue({ ...ACCOUNT });
    render(<PaymentMethodsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit Main Stripe" }));

    const secret = await screen.findByLabelText(/^Secret key/);
    const publishable = screen.getByLabelText(/^Publishable key/);
    const webhook = screen.getByLabelText(/^Webhook signing secret/);
    expect(secret).toHaveAttribute("type", "password");
    expect(webhook).toHaveAttribute("type", "password");
    expect(publishable).toHaveAttribute("type", "text");
    for (const el of [secret, publishable, webhook]) expect(el).toHaveValue("");
    expect(secret).toHaveAttribute("placeholder", "Set — leave empty to keep");
    expect(publishable).toHaveAttribute("placeholder", "Set — leave empty to keep");
    expect(webhook).toHaveAttribute("placeholder", "Not set");

    fireEvent.change(webhook, { target: { value: "whsec_new" } });
    fireEvent.change(screen.getByLabelText(/^Reason/), { target: { value: "Add webhook secret" } });
    fireEvent.change(screen.getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: "Save account" }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/payment-providers/accounts");
    expect(body).toMatchObject({
      id: "acc-1", provider: "stripe", merchant_account_ref: "acct_123", display_name: "Main Stripe", currency: "USD",
      mode: "TEST", status: "ACTIVE", is_default: true, reason: "Add webhook secret", password: "pw",
    });
    expect(body.credentials).toEqual({ webhook_secret: "whsec_new" });
  });

  it("disables LIVE where the provider does not allow it, and allows it where it does", async () => {
    routes(["site_admin"], { card_payment: mod("card_payment"), room_charge: mod("room_charge") });
    render(<PaymentMethodsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /add account/i }));
    const mode = await screen.findByLabelText(/^Mode/);
    const live = within(mode).getByRole("option", { name: /LIVE/ }) as HTMLOptionElement;
    expect(live.disabled).toBe(true);
    expect(screen.getByText("LIVE mode is not authorised on this appliance.")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(/^Provider/), { target: { value: "paymob" } });
    const live2 = within(screen.getByLabelText(/^Mode/)).getByRole("option", { name: /LIVE/ }) as HTMLOptionElement;
    expect(live2.disabled).toBe(false);
    // Paymob's region is its own select.
    expect(within(screen.getByLabelText(/^Region/)).getByRole("option", { name: "egypt" })).toBeInTheDocument();
  });

  it("sends the Paymob region as the credential key region, with a new account's typed secret", async () => {
    routes(["site_admin"], { card_payment: mod("card_payment"), room_charge: mod("room_charge") });
    post.mockResolvedValue({});
    render(<PaymentMethodsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /add account/i }));
    fireEvent.change(await screen.findByLabelText(/^Provider/), { target: { value: "paymob" } });
    fireEvent.change(screen.getByLabelText(/^Region/), { target: { value: "egypt" } });
    fireEvent.change(screen.getByLabelText(/^Merchant account reference/), { target: { value: "m-9" } });
    fireEvent.change(screen.getByLabelText(/^Display name/), { target: { value: "Paymob EG" } });
    fireEvent.change(screen.getByLabelText(/^Currency/), { target: { value: "egp" } });
    fireEvent.change(screen.getByLabelText(/^API key/), { target: { value: "k-secret" } });
    fireEvent.change(screen.getByLabelText(/^Reason/), { target: { value: "New account" } });
    fireEvent.change(screen.getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: "Add account" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const body = post.mock.calls[0][1];
    expect(body.id).toBeUndefined();
    expect(body.currency).toBe("EGP");
    expect(body.credentials).toEqual({ api_key: "k-secret", region: "egypt" });
  });

  it("shows the server's refusal of a LIVE save", async () => {
    routes(["site_admin"], { card_payment: mod("card_payment"), room_charge: mod("room_charge") });
    const { ApiError } = await import("@/lib/api");
    post.mockRejectedValue(new ApiError(409, { error: "live_not_authorised", message: "LIVE is not authorised here." }));
    render(<PaymentMethodsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit Main Stripe" }));
    fireEvent.change(await screen.findByLabelText(/^Reason/), { target: { value: "Try it" } });
    fireEvent.change(screen.getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: "Save account" }));
    expect(await screen.findByText("LIVE is not authorised here.")).toBeInTheDocument();
  });

  it("tests a connection and reports the answer", async () => {
    routes(["site_admin"], { card_payment: mod("card_payment"), room_charge: mod("room_charge") });
    post.mockResolvedValue({ ok: false, error: "auth_failed", message: "Invalid API key" });
    render(<PaymentMethodsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /test connection for main stripe/i }));
    expect(await screen.findByText(/connection failed: invalid api key/i)).toBeInTheDocument();
    expect(post).toHaveBeenCalledWith("/payment-providers/accounts/acc-1/test", {});
  });

  it("saves timings with a reason and password", async () => {
    routes(["site_admin"], { card_payment: mod("card_payment"), room_charge: mod("room_charge") });
    put.mockResolvedValue({ ...SETTINGS, settings: { ...SETTINGS.settings, checkout_expiry_minutes: 45, is_default: false } });
    render(<PaymentMethodsPage />);
    fireEvent.change(await screen.findByLabelText("Checkout expiry"), { target: { value: "45" } });
    fireEvent.click(screen.getByRole("button", { name: "Save timings" }));
    fireEvent.change(await screen.findByLabelText(/^Reason/), { target: { value: "Slow guests" } });
    fireEvent.change(screen.getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Save timings" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/payment-providers/settings", {
      checkout_expiry_minutes: 45, reconcile_grace_minutes: 60, reason: "Slow guests", password: "pw",
    }));
  });

  it("offers no edit control to a role that can only read, and shows no stored credential", async () => {
    routes(["payments_reader"], { card_payment: mod("card_payment"), room_charge: mod("room_charge") });
    render(<PaymentMethodsPage />);
    expect(await screen.findByText(/can see how payments are configured but not change it/i)).toBeInTheDocument();
    expect(await screen.findByText("Main Stripe")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /add account/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^edit/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save domains" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save timings" })).not.toBeInTheDocument();
    expect(screen.getByText("pay.example.com")).toBeInTheDocument();
    expect(screen.getByText("Longer checkout")).toBeInTheDocument();
  });

  it("tells a role without read access so, and asks for nothing", async () => {
    routes(["voucher_operator"], { card_payment: mod("card_payment"), room_charge: mod("room_charge") });
    render(<PaymentMethodsPage />);
    expect(await screen.findByText(/not available to your role/i)).toBeInTheDocument();
    expect(g).not.toHaveBeenCalledWith("/modules");
  });
});

describe("hosted payment domain validation", () => {
  it("accepts domain names with at most one leading wildcard and refuses IPs and deeper wildcards", () => {
    expect(domainProblem("pay.example.com")).toBeNull();
    expect(domainProblem("*.example.com")).toBeNull();
    expect(domainProblem("10.0.0.1")).toMatch(/IP addresses/);
    expect(domainProblem("a.*.example.com")).toMatch(/wildcard/);
    expect(domainProblem("*.*.example.com")).toMatch(/wildcard/);
    expect(domainProblem("localhost")).toMatch(/full domain name/);
    expect(domainProblem("https://pay.example.com")).not.toBeNull();
  });
});
