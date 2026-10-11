# Client access (PR #211) — real-phone acceptance checklist

For the Product Owner, on PRE-LIVE (`172.21.60.25`), with real phones. Everything here is TEST mode: the
site's Stripe account holds a sandbox key, no LIVE key exists on the appliance, and no PMS posting is
performed. The contract behind each check is
[`docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md`](../architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md).

Record each line as **Pass**, **Fail** (with what the phone showed) or **Not tested**. Use two phones where a
line says so — ideally one iPhone and one Android — and an email address you can read on the phone.

## Before you start

| # | Check | Expected |
|---|---|---|
| 0.1 | Admin Console → Sign-in methods: Email code is on; *Temporary internet while reading the code* is on with its defaults | 10 min · 2000/1000 kbps · 25 MB · 3 per device · 3 per address · 50 at once |
| 0.2 | Admin Console → Delivery: the site's email sender is enabled and *Send a test email* reaches you | message arrives |
| 0.3 | Admin Console → Payment methods: the Stripe account is ACTIVE, TEST mode | conversion shows *off* unless you are running section 6 |
| 0.4 | On each phone: forget the Wi-Fi network first, so no remembered device carries over | — |

## 1. Signing in with an email code

| # | Step | Expected |
|---|---|---|
| 1.1 | Join the guest Wi-Fi | the sign-in sheet opens by itself (iOS / Android captive pop-up) |
| 1.2 | Choose Email, enter your address, request a code | "We sent a code to a•••@…" — your address is never shown in full |
| 1.3 | Open your mail app **without** leaving the Wi-Fi | the mail app loads: the temporary window is open; the system sheet may close because the phone now sees internet |
| 1.4 | Return to the portal (reopen the sheet or a browser to any http site) | the page reopens on the pending code, with *Send again* and *Use a different address* |
| 1.5 | Type the code | signed in; the packages page appears |
| 1.6 | Admin Console → Sessions | the session says *Signed in with: Emailed code* |

## 2. The tap-to-connect link (device binding)

| # | Step | Expected |
|---|---|---|
| 2.1 | Phone A: request a new code; in the email, tap the link | a page with one **Connect** button; nothing is spent by opening it |
| 2.2 | Tap Connect on phone A | signed in, packages page |
| 2.3 | Phone A requests a code; open the **same link on phone B** (forward the email, or read it on B) and tap Connect | refused: "this code was requested from another device; enter it, or tap the link, on the phone that asked for it" |
| 2.4 | Then use the same code on phone A | still works (the refusal on B spent no attempt) |

## 3. The temporary internet window

| # | Step | Expected |
|---|---|---|
| 3.1 | Request a code and, before entering it, browse a normal website and play a short video | works, but noticeably slow (about 2 Mbps down) |
| 3.2 | Try the Admin Console address or another client's device from the phone | unreachable |
| 3.3 | Request again with another address within the 10 minutes | same window; its clock and data are **not** reset |
| 3.4 | Wait past 10 minutes without entering the code | internet stops; the portal comes back |
| 3.5 | Enter the code (or sign in any other way) | the window ends at once; normal package access takes over |
| 3.6 | *Use a different address* on the pending page | the window ends |
| 3.7 | Request more than 3 windows from the same phone in 24 h | the code is still sent; no further window is opened |

## 4. Packages and acquisition

| # | Step | Expected |
|---|---|---|
| 4.1 | Take a Free package | connected; Admin Console → Sessions shows the package and *1st time* |
| 4.2 | Sign in again from the same phone and look at the packages | the Free package is no longer offered, with an explanation (once per client) |
| 4.3 | A priced package in the account's currency | offered by **Card** only — no other payment method appears anywhere in OneGate |
| 4.4 | A voucher code | connects immediately, no extra package tap |

## 5. Card payment (Stripe TEST mode)

| # | Step | Expected |
|---|---|---|
| 5.1 | Choose a priced package by Card | Stripe's hosted page opens |
| 5.2 | Look at the payment choices on the Stripe page | **Card form only**; at most an Apple Pay / Google Pay button above it. **No Link**, no "save my information", no bank, BNPL or voucher methods |
| 5.3 | Pay with Stripe's test card `4242 4242 4242 4242`, any future date, any CVC | back on the portal; connected within a few seconds |
| 5.4 | Admin Console → Settlements | the charge is SETTLED with the provider's settlement report |
| 5.5 | Start another card purchase and press *Back* on the Stripe page | back on the portal; nothing granted; no charge |
| 5.6 | Pay with the decline test card `4000 0000 0000 0002` | Stripe shows the decline; you can try again on the same page; nothing granted |
| 5.7 | *(Optional)* Apple Pay / Google Pay on the Stripe page | in TEST mode the wallet uses a real card but Stripe charges nothing; the payment completes and access is granted like a card |

## 6. Provider-managed conversion (only if you are testing it)

| # | Step | Expected |
|---|---|---|
| 6.1 | A package priced in a currency the account does not settle, conversion **off** | not offered by card; Payment methods lists it as *Not offered by card* |
| 6.2 | Payment methods → *Let the provider convert* (asks for your password) | the package is now offered by card |
| 6.3 | Pay with the test card | Settlements shows the price as charged, the settled currency and amount, Stripe's rate, fee and net |
| 6.4 | Switch conversion back **off** | the package is withheld again |

## 7. Afterwards

| # | Check | Expected |
|---|---|---|
| 7.1 | Admin Console → Sessions: end the test sessions | ended |
| 7.2 | Any access paid in TEST mode that you refunded in Stripe | the refund is recorded and the settlement shows REVERSED; **access is not ended automatically** (open decision, contract §9) — end it by hand |

## Known limits — not failures

* **The code link is plain HTTP.** It carries the code in clear text on the client network and in the email.
  Device binding, single use, the ten-minute lifetime, *GET never verifies* and log redaction limit what that
  exposes; serving the portal over HTTPS needs a public certificate decision. Contract §10.2.
* **A refunded charge does not end access.** Contract §9; a Product-Owner decision.
* **A phone that changes its hardware address** (private / random Wi-Fi address) is a new device to the network:
  it gets its own window, within the per-address and site limits. Contract §6.6.
* **Pre-sign-in DNS is throttled, not blocked.** Contract §10.1.
