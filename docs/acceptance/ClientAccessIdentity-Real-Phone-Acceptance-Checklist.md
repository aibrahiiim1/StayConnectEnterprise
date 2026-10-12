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
| 0.1 | Admin Console → Sign-in methods: Email code is on; *Temporary internet while reading the code* is on with its defaults | 10 min · 2000/1000 kbps · 25 MB · 3 per device · 3 per address · 50 at once · wait 30 min before another window |
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
| 3.7 | Let a window run out (or use up its data) without entering the code, then ask for a new code at once | the code is sent, **no new window**, and the page says how many minutes until the device may have one again (about 30) |
| 3.8 | After 30 minutes, ask for a code again | a new window opens |
| 3.9 | End a window by *Use a different address*, then ask with the right address | a new window opens at once (changing address does not start the wait) |
| 3.10 | Request more than 3 windows from the same phone in 24 h | the code is still sent; no further window is opened |

## 3A. Staff help (Sign-in methods → Code requests)

| # | Step | Expected |
|---|---|---|
| 3A.1 | After 3.7, open Code requests in the Admin Console | the request is listed with the address, the device, "Waiting for the code" and "next window in … min" |
| 3A.2 | *Let them try again* (reason + your password) | the phone's next code request opens a window at once; the audit log shows who and why |
| 3A.3 | Use a **new** address (never signed in here). While its code is waiting, *Show code* (reason + your password) as an IT manager or front-desk user | the code is shown; typing it on the phone that asked signs in; Sessions says *Emailed code, shown by staff* |
| 3A.3b | Request a code for an address that **already signed in here before** | Code requests flags *Existing client*; *Show code* is not offered; *Let them try again* is |
| 3A.3c | Sign out and come back on the phone from 3A.3 | no *Welcome back*: a staff-assisted sign-in earns no remembered device |
| 3A.4 | Type that same code on a **different** phone | refused: requested from another device |
| 3A.5 | After the client has signed in, look for *Show code* on that row | not offered (the code is used) |
| 3A.6 | Sign in as a guest-relations or viewer user | guest relations: *Let them try again* only; viewer: the list only; both see addresses and devices masked (`g•••@example.com`) |

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
| 5.8 | Buy a package with the dispute test card `4000 0000 0000 0259` | paid and connected; within the reconciliation sweep (up to 6 h) Financial review → *Card disputes and chargebacks* shows a **Dispute** case; access stays on |
| 5.9 | Decide that case: *Keep access* (reason + password) | the case shows *Access kept* with your name; access unchanged |
| 5.10 | In the Stripe dashboard (TEST mode), **partially** refund a paid test payment | settlement *Partially reversed*; the client stays online |
| 5.11 | Refund the **rest** of the same payment | settlement *Reversed*; that client's access ends on every device within the sweep; it is listed under *Access ended by a full provider refund*; the client's other purchases are untouched |

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
| 7.2 | Any test case still open under *Card disputes and chargebacks* | decide it (*Keep* or *End access*) so no open case is left from testing |

## Known limits — not failures

* **The code link is plain HTTP.** It carries the code in clear text on the client network and in the email.
  Device binding, single use, the ten-minute lifetime, *GET never verifies* and log redaction limit what that
  exposes; serving the portal over HTTPS needs a public certificate decision. Contract §10.2.
* **Refunds act on the reconciliation sweep**, which runs every 6 hours, not instantly.
* **A dispute or chargeback never ends access by itself**; a payments operator decides each case. Contract §9.
* **Showing a code is the desk vouching for the client.** The client has not proved the address by reading their
  own mail. Contract §6.7.
* **A phone that changes its hardware address** (private / random Wi-Fi address) is a new device to the network:
  it gets its own window, within the per-address and site limits. Contract §6.6.
* **Pre-sign-in DNS is throttled, not blocked.** Contract §10.1.
