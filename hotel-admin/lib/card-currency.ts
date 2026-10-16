// WHICH PACKAGE CURRENCIES CAN BE PAID BY CARD HERE — the console's copy of the rule scd applies when it
// lists packages (data-plane/cmd/scd/modules.go siteAcquisitionMethods), so the editor and the Payment
// methods screen warn about exactly the packages clients will not be offered by card.
//
// ONLY THE DEFAULT ACTIVE ACCOUNT COUNTS. A checkout always uses the site's default active account
// (p4_resolve_payment_account_v2); any other active account settles nothing. The editor used to compare a
// package's currency against every ACTIVE account, so a second account in the right currency silenced a
// warning that was true (audit O-12).
//
// A package is offered by card when:
//   - its currency is the default account's currency, or
//   - the default account is marked provider-converts AND its provider converts that currency (Stripe, from a
//     conservative list; Paymob converts nothing).

import type { PaymentAccount } from "@/lib/payment-admin";

/**
 * The presentment currencies OneGate offers for Stripe conversion. Mirrors stripePresentment in
 * data-plane/internal/payment/stripe/stripe.go; keep the two in step.
 */
export const STRIPE_CONVERTS = new Set([
  "USD", "EUR", "GBP", "AED", "SAR", "QAR", "EGP", "KWD", "BHD", "OMR", "JOD", "CHF", "CAD", "AUD", "NZD", "JPY",
  "CNY", "HKD", "SGD", "INR", "TRY", "MAD", "ZAR", "SEK", "NOK", "DKK", "PLN", "CZK", "HUF", "RON",
]);

/** The default active account, as far as the card-currency rule needs it. */
export type CardAccountView = { currency: string; provider: string; provider_converts: boolean };

const up = (s: string | null | undefined) => (s ?? "").trim().toUpperCase();

/**
 * defaultCardAccount picks the account checkouts use: ACTIVE and default. "none" when there is no such
 * account (no package is offered by card then), null when the accounts are unknown (no claim is made).
 */
export function defaultCardAccount(accounts: PaymentAccount[] | null | undefined): CardAccountView | "none" | null {
  if (!accounts) return null;
  const a = accounts.find((x) => x.status === "ACTIVE" && x.is_default);
  if (!a || !up(a.currency)) return "none";
  return { currency: up(a.currency), provider: (a.provider ?? "").trim().toLowerCase(), provider_converts: !!a.provider_converts };
}

/** providerConverts reports whether this provider converts `presentment` into `settlement` at all. */
export function providerConverts(provider: string, presentment: string, settlement: string): boolean {
  const p = up(presentment);
  const s = up(settlement);
  if (p === s) return true;
  const id = provider.trim().toLowerCase();
  if (id === "stripe" || id.startsWith("stripe:")) return STRIPE_CONVERTS.has(p) && STRIPE_CONVERTS.has(s);
  return false;
}

/** cardAccepts answers the scd question: is a package priced in `currency` offered by card? */
export function cardAccepts(acct: CardAccountView, currency: string): boolean {
  const cur = up(currency);
  if (!cur) return false;
  if (cur === acct.currency) return true;
  return acct.provider_converts && providerConverts(acct.provider, cur, acct.currency);
}

/**
 * cardCurrencyProblem is the sentence the editor shows beside a card-priced package's currency, or null when
 * clients will be offered card for it (or nothing is known).
 */
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
