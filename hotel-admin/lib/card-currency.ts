// Console copy of scd's rule for which package currencies can be paid by card (data-plane/cmd/scd/modules.go
// siteAcquisitionMethods).
// Only the DEFAULT active account counts (checkout uses p4_resolve_payment_account_v2). Offered by card when the
// package currency
// is the default account's currency, or the account is provider-converts and its provider converts that currency.

import type { PaymentAccount } from "@/lib/payment-admin";

/**
 * Presentment currencies OneGate offers for Stripe conversion; mirrors stripePresentment in
 * data-plane/internal/payment/stripe/stripe.go.
 */
export const STRIPE_CONVERTS = new Set([
  "USD", "EUR", "GBP", "AED", "SAR", "QAR", "EGP", "KWD", "BHD", "OMR", "JOD", "CHF", "CAD", "AUD", "NZD", "JPY",
  "CNY", "HKD", "SGD", "INR", "TRY", "MAD", "ZAR", "SEK", "NOK", "DKK", "PLN", "CZK", "HUF", "RON",
]);

export type CardAccountView = { currency: string; provider: string; provider_converts: boolean };

const up = (s: string | null | undefined) => (s ?? "").trim().toUpperCase();

/**
 * The account checkouts use (ACTIVE and default). "none" when absent (no card offer), null when accounts are unknown
 * (no claim).
 */
export function defaultCardAccount(accounts: PaymentAccount[] | null | undefined): CardAccountView | "none" | null {
  if (!accounts) return null;
  const a = accounts.find((x) => x.status === "ACTIVE" && x.is_default);
  if (!a || !up(a.currency)) return "none";
  return { currency: up(a.currency), provider: (a.provider ?? "").trim().toLowerCase(), provider_converts: !!a.provider_converts };
}

export function providerConverts(provider: string, presentment: string, settlement: string): boolean {
  const p = up(presentment);
  const s = up(settlement);
  if (p === s) return true;
  const id = provider.trim().toLowerCase();
  if (id === "stripe" || id.startsWith("stripe:")) return STRIPE_CONVERTS.has(p) && STRIPE_CONVERTS.has(s);
  return false;
}

export function cardAccepts(acct: CardAccountView, currency: string): boolean {
  const cur = up(currency);
  if (!cur) return false;
  if (cur === acct.currency) return true;
  return acct.provider_converts && providerConverts(acct.provider, cur, acct.currency);
}

/** The sentence shown beside a card-priced package's currency, or null when card is offered (or nothing is known). */
export function cardCurrencyProblem(acct: CardAccountView | "none" | null, currency: string): string | null {
  if (acct === null) return null;
  const cur = up(currency);
  if (acct === "none") {
    return "There is no default active payment account yet, so this package will not be offered by card until one is set up under Payment methods.";
  }
  if (cardAccepts(acct, cur)) return null;
  if (acct.provider_converts) {
    return `Your default payment account settles ${acct.currency}, and its provider does not convert ${cur} for it. This package will not be offered by card until the price currency or the account matches.`;
  }
  return `Your default payment account settles ${acct.currency}, not ${cur}, and it is not set to let the provider convert. This package will not be offered by card until the price currency or the account matches.`;
}
