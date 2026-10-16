// WHICH PACKAGE CURRENCIES ARE OFFERED BY CARD — the console's copy of scd's rule (audit O-12).
//
// Only the DEFAULT active account takes checkouts. The editor used to compare against every ACTIVE account, so a
// second, non-default account in the package currency silenced a warning that was true.

import { describe, it, expect } from "vitest";
import { cardAccepts, cardCurrencyProblem, defaultCardAccount, providerConverts } from "@/lib/card-currency";
import type { PaymentAccount } from "@/lib/payment-admin";

const acct = (x: Partial<PaymentAccount>): PaymentAccount => ({
  id: "a", provider: "stripe", merchant_account_ref: "m", display_name: "d", currency: "USD", mode: "TEST",
  status: "ACTIVE", is_default: false, has_credentials: true, ...x,
});

describe("defaultCardAccount", () => {
  it("picks the ACTIVE default account, never another active one", () => {
    const d = defaultCardAccount([
      acct({ id: "1", currency: "USD", is_default: false }),
      acct({ id: "2", currency: "gbp", is_default: true }),
    ]);
    expect(d).toEqual({ currency: "GBP", provider: "stripe", provider_converts: false });
  });
  it("is 'none' when the default account is disabled or there is none, and null when unknown", () => {
    expect(defaultCardAccount([acct({ is_default: true, status: "DISABLED" }), acct({ is_default: false })])).toBe("none");
    expect(defaultCardAccount([])).toBe("none");
    expect(defaultCardAccount(null)).toBeNull();
  });
});

describe("cardAccepts", () => {
  it("accepts the default account's own currency", () => {
    expect(cardAccepts({ currency: "GBP", provider: "stripe", provider_converts: false }, "gbp")).toBe(true);
    expect(cardAccepts({ currency: "GBP", provider: "stripe", provider_converts: false }, "USD")).toBe(false);
  });
  it("with provider-converts, Stripe takes currencies on its conservative list and nothing else", () => {
    const s = { currency: "GBP", provider: "stripe", provider_converts: true };
    expect(cardAccepts(s, "USD")).toBe(true);
    expect(cardAccepts(s, "EGP")).toBe(true);
    expect(cardAccepts(s, "XAF")).toBe(false);
  });
  it("Paymob converts nothing, whatever the flag says", () => {
    expect(cardAccepts({ currency: "EGP", provider: "paymob", provider_converts: true }, "USD")).toBe(false);
    expect(providerConverts("paymob", "EGP", "EGP")).toBe(true);
  });
});

describe("cardCurrencyProblem", () => {
  it("makes no claim when the account is unknown, and none when card is offered", () => {
    expect(cardCurrencyProblem(null, "USD")).toBeNull();
    expect(cardCurrencyProblem({ currency: "USD", provider: "stripe", provider_converts: false }, "USD")).toBeNull();
  });
  it("names the default account's currency when it does not match", () => {
    expect(cardCurrencyProblem({ currency: "EGP", provider: "paymob", provider_converts: false }, "USD"))
      .toMatch(/default payment account settles EGP, not USD/);
    expect(cardCurrencyProblem("none", "USD")).toMatch(/no default active payment account/);
  });
});
