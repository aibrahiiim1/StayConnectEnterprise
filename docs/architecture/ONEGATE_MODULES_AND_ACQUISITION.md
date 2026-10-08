# OneGate — Site Type, Licence Modules and Internet Package Acquisition

**Status:** authoritative architecture contract for the Site-Modules-Acquisition delivery (decisions D44 and D45).
**Scope:** how a site's type, its licensed modules and its Internet Package acquisition methods work, end to end,
from OneGate Central to the appliance and the Client Portal. Where this document and older wording disagree about
these subjects, this document wins. It does not change any other accepted contract.

Product boundaries that stay in force: no Go-Live; no real PMS financial posting; no LIVE-mode provider
transactions; no Root-CA or trust change; Central remains the licensing authority only (§0E of `CLAUDE.md`).

---

## 1. Four gates, one resolver

Every optional capability is decided by four separate gates. They are never collapsed.

| Gate | Question | Authority | Where |
|---|---|---|---|
| **Deployment ceiling** | Can the deployed software run it here at all? | whoever deploys the appliance (a controlled redeploy) | `data-plane/internal/deployment` — the only reader of the historical `STAYCONNECT_PHASE*` flags for new logic |
| **Licence authorisation** | Is this site commercially entitled to it? | OneGate Central, signed licence v4 `modules` | `license/modules.go`, `internal/licstate` |
| **Local enablement** | Has the Site Admin chosen to use it? | the site | `iam_v2.site_module_settings` (migration 0094); the Sign-in methods screen for SMS / email / social |
| **Readiness / applicability** | Can it execute now, for this operation and client? | the appliance, derived per request | readiness probes registered with the resolver; applicability in the acquisition engine |

The single resolver is `data-plane/internal/modules`, hosted by scd (which holds the licence) and exposed on scd's
admin socket as `GET /v1/modules`. edged reads it; the portal never needs it because every guest route enforces
the gates itself.

**Execution vs management.** Offering a method to a client, creating a quote, starting a checkout or posting a
charge requires all four gates. Configuration, diagnostics, history, reconciliation and recovery require only
that the module is deployed and either licensed or already holding records. **Readiness never hides a management
surface**: an unreachable provider or PMS stops new execution and leaves everything needed to restore service.
A module that is withdrawn from the licence keeps its history and recovery screens while records exist.

**Fail closed.** An unreadable local state means "not enabled"; an unknown module is not deployed; a module whose
dependency is not effective is not effective; an Admin Console that cannot read module state shows only the core.

## 2. Site Type

* **Owner:** Central, as descriptive site metadata (`sites.site_type`, Central migration 0049).
* **Values:** `HOTEL, CAFE, OFFICE, CLINIC, CAMPUS, VENUE, COMPOUND, BEACH_CLUB, OTHER` and `UNSPECIFIED`
  (the default). Storage is a pattern-checked string, not an enum, so a future type needs no schema change.
* **Delivery:** the signed assignment carries `site_type` as its **last signed field, `omitempty`**. An untyped
  site produces byte-identical signed bytes to the previous layout (golden tests in both assignment twins). An
  appliance that receives a type it does not know stores and shows it verbatim.
* **Appliance:** stored on the local `sites` mirror, reported in `/v1/central/status` and `/v1/modules`, shown
  read-only in the Admin Console.
* **Uses:** Central's licence dialog pre-ticks a recommended module preset (HOTEL → `hospitality`; every other
  type → none); Admin Console defaults and presentation; fleet segmentation.
* **Never:** authorises, enables or disables a module. The resolver has no Site Type input (enforced by test).
  A hotel without PMS, a hotel on vouchers only, a compound with a PMS and future verticals are all expressed by
  the licence and the site's switches, not by the type.
* Editing a site in Central re-issues the signed assignment of its assigned appliances, so a rename or type
  change reaches the appliance.

## 3. Module catalogue

The registry lives in the shared `license` module so Central and the appliance use one definition.

| Module | Requires | Local switch | What it unlocks |
|---|---|---|---|
| `hospitality` | — | Modules screen | Hotel section: PMS interfaces, Room sign-in, stays, Grace Period, post-stay, stay transfers, reconciliation |
| `paid_access` | — | Modules screen | priced Internet Packages |
| `card_payment` | `paid_access` | Modules screen | Card payment through a provider-hosted page |
| `room_charge` | `hospitality`, `paid_access` | Modules screen | PMS Room Charge, per-interface financial onboarding, financial review |
| `email_otp`, `sms_otp`, `whatsapp_otp`, `social_login` | — | Sign-in methods | identity add-ons (§12); WhatsApp is its own channel, never an SMS variant |
| `white_label`, `ha` | — | none | licence-only |

**Permanent core (never licensed):** client networks, Client Portal and branding, walled garden, **Free
packages**, **Vouchers**, **open package selection**, client accounts, sessions, usage, operators, audit,
backups, health, licence page, network settings, certificates.

`paid_access` authorises priced packages. It introduces no settlement method of its own. There is no Cash
settlement concept anywhere in OneGate: a site that sells access for cash sells Vouchers (§6.2).

## 4. Licence v4

* `Document.Modules map[string]ModuleGrant` (`json:"modules"`, always present on v4, `{}` for core-only),
  `CurrentSchemaVersion = 4`. A `ModuleGrant` carries optional per-module limits; a licence authorises and never
  configures.
* **`modules` is the sole authorisation source of a v4 licence.** `features` is emitted only as a compatibility
  projection computed by `license.ProjectFeatures(modules)`; `Validate` rejects a v4 document whose `features`
  disagree with that projection, and no code reads `features` from a v4 licence as authority.
* **Unknown module ids** are tolerated and ignored (the signature covers raw bytes). A module with an unmet
  dependency is treated as not authorised. Central refuses to issue unknown ids or unmet dependencies.
* **Legacy licences (v1–v3)** map conservatively: `pms → hospitality`; `sms_otp`, `email_otp`, `social_login`,
  `ha`, `white_label` by name; **`paid_wifi` is not mapped** (Central hard-coded it, so it never recorded a
  commercial grant); **no financial module is ever derived**.
* Central persists `licenses.modules`, and every re-issue path (suspend, resume, rebind, move) carries it.
* Rollout order: appliances accept v4 and the new assignment field first; Central issues them afterwards.

## 5. Internet Packages are the canonical offer

A Package Revision (immutable) defines the access policy and, where applicable, its price, currency and the
acquisition methods it supports, in `internet_package_revisions.settlement_methods`, now CHECK-constrained:

| Stored value | Product name | Settlement / authority | Grant entry (SQL) |
|---|---|---|---|
| `NOT_REQUIRED` | **Free** | none; price must be 0 | `p4_grant_quoted_entitlement` |
| `PREPAID` | **Voucher** | a valid voucher, atomically burned in the grant | `p4_grant_voucher_entitlement` |
| `ONLINE_PAYMENT` | **Card payment** | provider-verified CAPTURED (`apply_payment_callback_v2`) | `p4_grant_paid_entitlement` |
| `PMS_POSTING` | **Room charge** | an ACKED posting attempt with PA `OK` | `p4_grant_paid_entitlement` |

* Price 0 ⇒ methods ⊆ {`NOT_REQUIRED`, `PREPAID`}. Price > 0 ⇒ `paid_access` licensed and methods a non-empty
  subset of {`PREPAID`, `ONLINE_PAYMENT`, `PMS_POSTING`}.
* `MANUAL_APPROVAL` **remains in the settlement contract and DB CHECK, dormant and never offered**; retiring it is
  a separate Product-Owner decision.
* Vouchers have no catalogue of their own: a voucher is always backed by one package revision.

**Offered methods.** For each package and client:

```
offered(pkg, client) = resolver.effective(module of method)      -- four gates, site-wide
                     ∧ method ∈ revision.settlement_methods     -- the package allows it
                     ∧ applicable(method, pkg, client)          -- this client, this package, now
```

A package with no applicable method is not listed. Every gate is re-checked when the quote is created **and**
when it is confirmed or executed; the earlier page state is never trusted.

## 6. Acquisition flows

### 6.1 Free
Client (signed in, or via open selection) → package → quote → confirm → purchase `PENDING` + settlement
`NOT_REQUIRED/NOT_REQUIRED` → `p4_grant_quoted_entitlement` → entitlement `ACTIVE` → session. (Existing path.)

### 6.2 Voucher (core, offline/manual)
* **Issue:** the operator selects an Internet Package; issuance pins its current immutable revision and is
  refused unless the package is active and the revision lists `PREPAID` (an INSERT trigger on `iam_v2.vouchers`
  enforces it). A batch is one transaction and is recorded in `iam_v2.voucher_batches`.
* **Distribute:** the site sells or gives the code away by any offline process, including cash. OneGate records
  no cash, no receipt, no queue and no reconciliation.
* **Redeem:** the client enters the code → scd verifies it (HMAC lookup, scope, state `UNUSED`, redemption window)
  → quotes and confirms **the pinned revision automatically** (trigger `VOUCHER_REDEMPTION`) → purchase
  `PENDING → GRANTED` (the contract's prepaid path) with settlement `PREPAID/SETTLED` →
  `p4_grant_voucher_entitlement` → the kernel burns the voucher (`UNUSED → REDEEMED`, row count must be 1) in the
  same transaction that creates the entitlement.
* **Survival:** an issued, valid voucher keeps redeeming its pinned revision through package republish **and**
  normal package deactivation. Deactivation stops new open offers and new issuance only. A voucher stops working
  only by its own state or window, or by an explicit audited revocation (`voucher_revoke`, `voucher_batch_revoke`).
* Redemption never depends on the revision's current method list; issuance is the gate.

### 6.3 Card payment
```
package → immutable quote → purchase AWAITING_SETTLEMENT → settlement ONLINE_PAYMENT/REQUIRED
→ payment intent (client ref sc_…) → begin_payment_execution (txn PENDING, settlement IN_PROGRESS)
→ provider hosted checkout (idempotency key = client ref) → client pays ONLY on the provider page
→ OneGate queries the provider (outbound, authenticated) → CAPTURED → settlement SETTLED
→ p4_grant_paid_entitlement → entitlement ACTIVE → session
```
* **Authority:** a browser redirect or a client-side success proves nothing. Only a provider status query,
  applied through the outcome authority (`p4_apply_provider_outcome` on the outcome role), settles.
* **Abandoned browser:** a background reconciler in scd queries every PENDING checkout until it is terminal; a
  client who paid and never returned is granted anyway and rejoins the entitlement from the same device, or
  through the resume cookie / recovery code (§6.5).
* **Repetition:** status queries are read-only and repeat safely. A checkout is never created twice for one
  settlement (one live charge per settlement is a DB invariant); an ambiguous creation is resolved by querying
  the client reference, never by creating again.
* **Terminal outcomes:** expired/cancelled/declined-at-expiry → `FAILED`; still ambiguous after expiry plus the
  reconciliation grace → `UNKNOWN` → settlement `MANUAL_REVIEW` (existing financial review). Never a second
  charge.
* **Provider-originated refunds and chargebacks** reported by a status query are recorded in the existing ledger
  (`payment_transactions` REFUND/CHARGEBACK rows through `apply_payment_callback_v2`, which moves the settlement
  to `PARTIALLY_REVERSED`/`REVERSED`). OneGate never initiates a refund and has no refund feature.
* **Adapters:** Stripe Checkout and Paymob (Intention + Unified Checkout), behind
  `payment.HostedCheckoutProvider`. Card data never touches OneGate.
* **Configuration is site-local:** `iam_v2.payment_provider_accounts` (definer writer, change log, `mode`
  `TEST|LIVE`) and sealed secrets (AES-256-GCM, owner-bound AAD, key in `/etc/stayconnect/secrets`). Secrets are
  write-only in every API. Central holds none of it.
* **LIVE mode** is refused by the deployment ceiling unless `STAYCONNECT_PAYMENT_LIVE_ALLOWED` is set, which
  requires a separate Product-Owner authorisation. The legacy plaintext `public.stripe_accounts` path is removed.
* **Walled garden (least privilege):** each adapter declares its hosted-payment domains in code. Only the
  domains of providers that have an ACTIVE account at the site are added, and only while Card payment is
  deployed, licensed and switched on (readiness is deliberately not required: reaching the provider is part of
  what readiness checks). A Site Admin may add a bounded, audited list of extra FQDNs (for example a bank's
  3-D Secure domain): FQDN only, no IP/CIDR, no bare TLD, at most one leading wildcard label, at most 20.
  Payment domains are ordinary walled-garden domain entries: like every domain entry they are not restricted
  to a port. The whole payment set is withdrawn when Card payment is switched off or loses its licence.

### 6.4 PMS Room Charge
Available only when `hospitality`, `paid_access` and `room_charge` are licensed, enabled and deployed **and**:
* the client authenticated by Room sign-in with **verified RN + G#** on the stay;
* the package has a live **settlement mapping on the pinned PMS Interface** (posting code, tax);
* the interface is **financially onboarded** (posting target `RESERVATION` recorded with the vendor's confirmation that
  reservation numbers are never reused, base currency and exponent set, approval recorded) and the **package
  currency equals the interface currency** (no implicit FX);
* the stay is `IN_HOUSE` and `posting_allowed` (it has a reservation number and no posting block), it has no
  unresolved room charge, and the interface is financially fresh (D48): connected with a recent PMS link-alive,
  CONTINUOUS, IN_SYNC, the revision pinned, and a successful complete resync within the interface's financial mirror
  maximum age (default 4 hours; pmsd refreshes it at half the bound). A quiet feed is not stale;
* the charge **targets the reservation**: posting identity `(interface, G#)`, pinned at purchase; the room (RN) is the
  reservation's current room, refreshed before each attempt. There is no folio model and no folio-window selection
  ([Phase-0 Amendment A1, D46](StayConnect-IAM-Phase0-Amendment-A1.md));
* PMS posting transmission is within the deployment ceiling.

```
package → quote (pins interface, mapping, stay, currency) → purchase AWAITING_SETTLEMENT
→ settlement PMS_POSTING/REQUIRED → posting (gate + DB triggers) → outbox → posting worker (scd) → pmsd (transport, §11)
→ PS → PA OK (authoritative ACK) → p4_posting_settlement_outcome → settlement SETTLED → grant → access
```
* No entitlement is granted because a posting was sent. A `PA` other than `OK` fails the charge only when the
  vendor has confirmed that code for the interface; an unconfirmed code (and `UR`, always) is UNKNOWN. Confirmed
  `NP` blocks room charge for the stay until Protel data allows posting again; confirmed `NG`/`NR` mark the stay
  data suspect, request a resync and block until fresh valid data; `NA`/`RY` place no stay block. None is retried
  automatically, and no operator can lift a Protel block.
* **UNKNOWN** → attempt `UNKNOWN`, outbox `HELD_RECOVERY`, settlement `MANUAL_REVIEW`. Never retried
  automatically. The accepted Phase-0 review actions remain: `CONFIRM_POSTED` (settle and grant),
  `CONFIRM_NOT_POSTED_ABANDON` (fail), and **`CONFIRM_NOT_POSTED_RETRY`** — authorised staff who verified from
  external evidence that nothing was posted authorise one exact attempt, consumed once, blocked against a posted
  charge.
* No programmatic reversal; corrections remain a Front Office task.
* Real posting remains prohibited until the Product Owner separately authorises it: without the transmit
  ceiling, Room charge is configurable and reviewable but never offered.

### 6.5 Open package selection and the anonymous access subject
* A core Sign-in methods switch: "Clients may choose a package without signing in" (default off).
* The entitlement subject is a **server-generated opaque anonymous access subject**
  (`iam_v2.anonymous_access_subjects`). It is not a `guest_principals` row — that concept keeps its meaning of a
  verified identity — and it carries no MAC. **MAC identifies a Device only.**
* `auth_contexts`, `entitlements` carry `anonymous_subject_id`; the one-subject CHECKs include it; auth method
  `OPEN` requires it; one live entitlement per anonymous subject is a unique index, like every other subject.
* **Resume and recovery:** a random 256-bit resume token (HttpOnly portal cookie) and a short recovery code shown
  on the pending and success pages, both stored only as HMACs (`iam_v2.anonymous_subject_credentials`) and
  rate-limited by the existing sign-in protection.
* **Device bindings stay separate:** a returning device first joins the live entitlement it is bound to (purchase
  → auth context → device). Only a device with no live entitlement gets a new anonymous subject, and free-package
  limits (`PRIOR_PURCHASE`) are evaluated against the device's history, so a new subject never resets quota.

## 7. Financial and grant invariant

Paid Internet access is never granted because a client claims payment succeeded:

* Free → no settlement required (quote price 0, settlement `NOT_REQUIRED`).
* Voucher → a valid voucher atomically consumed in the grant transaction.
* Card → a provider-verified capture applied by the outcome authority.
* Room charge → an authoritative PMS `PA=OK`.

Each is enforced in the SQL grant entry point, which is the only path to the entitlement kernel.

## 8. Admin Console

* **System → Modules:** available / licensed / switched on / ready per module, reasons in plain language,
  Site Type (read-only), switch (site administrator, password step-up and reason; recorded in
  `iam_v2.site_module_changes`).
* **Internet offering → Payment methods:** Free and Voucher (core), Card payment (provider accounts, write-only
  secrets, test connection, readiness, extra payment domains), Room charge (readiness per interface, link to
  onboarding).
* **Internet Packages editor:** price and currency; acquisition-method checkboxes limited to site-effective
  methods (others shown with the reason); room-charge settlement mapping per interface.
* **Vouchers:** issue against a package that supports Voucher (the picker lists only those); revoke a code, or
  cancel every unused card of a batch (audited, step-up, reason).
* **Hotel → Room charge:** financial onboarding per FIAS interface — posting target `RESERVATION` (RN + G#), base
  currency and the vendor attestation, approved by a site administrator with step-up — the vendor-confirmed answer
  meanings per interface, and readiness per interface. A stay's posting permission and block history are shown on
  the stay; only `ADMIN_BLOCK` is operator-controlled.
* **Client Portal → Sign-in methods:** "Choose a package without signing in" (open package selection).
* Navigation follows `/capabilities`, which reports module-owned surfaces only while their module is manageable
  and falls back to the core when module state is unreadable.

## 9. Migration of an existing site

* Existing free revisions keep `{NOT_REQUIRED}`. Revisions that already back issued, still-valid vouchers get
  `PREPAID` added once (a documented data correction), so those packages keep issuing and their vouchers keep
  redeeming. Every other package gets Voucher support only when the Site Admin chooses it.
* A site that already runs PMS interfaces has `hospitality` enabled locally, attributed to the migration.
* No financial module is enabled or derived for anyone. A v3 licence authorises no financial module.
* Open package selection is off.

## 10. What stays inactive until separately authorised

LIVE-mode provider transactions (`STAYCONNECT_PAYMENT_LIVE_ALLOWED`), Go-Live, and real guest-production cutover.
Real PMS posting (`STAYCONNECT_PHASE4_PMS_TRANSMIT`) is enabled on the PRE-LIVE appliance by D47 (2026-09-30); on
any other appliance it stays off until separately authorised.

## 11. Room charge on the one FIAS connection (decision D45)

**Product-Owner decision D45 (2026-09-28):** room-charge postings use the property's existing FIAS connection,
which pmsd owns. There is no second FIAS connection, no second PMS interface for posting and no property-side
PMS change. pmsd remains the sole owner of the link and is **transport only** for the financial command; the
financial execution path keeps every financial decision.

```
scd posting worker (svc_posting, internal/posting)          pmsd (svc_pmsd)                      PMS
  claim QUEUED posting on its lane (one IN_FLIGHT per interface)
  lock the stay; re-read it by G#: same reservation, IN_HOUSE, not blocked, current room (A1), currency, freshness
  (a stay no longer postable ends the charge here: ABORTED, definitely not posted)
  allocate P#, build PS, record attempt SENDING + sha256(PS)
  commit  ──── immutable command ────►  unix socket /run/stayconnect-pmsd/pmsd-posting.sock (0600, pmsd-owned; scd connects as root)
                                          own flags on? (MASTER + OUTBOX_WORKER + PMS_TRANSMIT)
                                          shape: PS only, bounded, wire-safe, P# = command P#, hash
                                          DB: p4_posting_command_authorised(iface, P#, sha256)
                                          DB: stay still postable, same G#, RN = the stay's current room (A1)
                                          link connected, between resyncs, not busy, P# not carried
                                          IN the writer, before the first byte: RN = the room this link last
                                          read for the G#, not departed -- else NOT_TRANSMITTED (NOT_SENT)
                                          single serialized writer ── PS (verbatim) ──────────────►
                                          read loop: PA with the same P# ◄──────────────────────────
  ◄──── ANSWERED (PA verbatim) | NOT_TRANSMITTED | UNKNOWN ────
  parse PA, verify P#; record ACKED / NOT_SENT / UNKNOWN (an unconfirmed answer code is UNKNOWN)
  p4_posting_settlement_outcome: PA=OK → SETTLED → grant; other PA → FAILED; UNKNOWN → MANUAL_REVIEW
```

**What pmsd may do:** carry the exact bytes it was handed, once, on a steady link, and hand back the PA whose
P# matches, verbatim. **What it cannot do, by construction:** build or edit a PS; choose or change the amount,
RN, G#, currency or posting code; create a posting; retry anything; interpret AS; decide a settlement; reverse;
grant access. pmsd holds `EXECUTE` on the read-only authorisation check and **no privilege on any posting
table**. The ordinary FIAS writer still refuses `PS` and `PA`; the one `PS` path (`writeFinancialFrame`) accepts
a `PS` and nothing else and is reachable only from the relay.

**Safeguards kept or added:**

* **Immutable authorised command.** The attempt records `ps_sha256` in the same transaction as the attempt,
  before any byte exists; the hash is part of the attempt's immutable identity (`pa_oneway`). pmsd carries
  bytes only when the database confirms a *SENDING, recent* attempt for that interface and P# with *that* hash,
  on an in-flight CHARGE, on an interface still financially ready (migration 0098). Tampered, stale or replayed
  commands are refused before anything is written.
* **Two independent switches.** Transmission needs `STAYCONNECT_PHASE4_PMS_TRANSMIT` (with master and outbox) on
  scd *and* on pmsd. Without it on scd, the DARK guard refuses and there is no hand-off client; without it on
  pmsd, there is no socket at all.
* **The UNKNOWN contract is preserved end to end.** `NOT_TRANSMITTED` is answered only when pmsd provably wrote
  nothing (refused before its writer took the command) or pmsd could not be reached. Anything after the writer
  took it — an incomplete write, no PA within the bound, a lost link, a P# already carried — is `UNKNOWN`:
  outbox `HELD_RECOVERY`, settlement `MANUAL_REVIEW`, never resent. An attempt left `SENDING` by a worker that
  stopped mid-send is concluded `UNKNOWN` by orphan recovery; nothing resends it.
* **No duplicate execution.** One `IN_FLIGHT` posting per interface (database); one command in flight per link
  (pmsd); a P# is never carried twice (pmsd) and a concluded attempt is never authorised again (database).
* **Unchanged:** verified RN + G#, pinned stay/interface/mapping, exact currency equality and no FX,
  PA=OK before SETTLED, audited manual review including `CONFIRM_NOT_POSTED_RETRY` under the existing
  single-use authorisation, and no programmatic reversal.

Real PMS financial posting needs a Product-Owner authorisation per appliance. D47 (2026-09-30) authorised it on
PRE-LIVE: `STAYCONNECT_PHASE4_PMS_TRANSMIT` (with master and outbox) is on for scd and pmsd there, the relay
listens on `/run/stayconnect-pmsd/pmsd-posting.sock`, and Room charge is offered to eligible verified room
guests. Without that authorisation readiness reports `PMS_POSTING_NOT_AUTHORISED` and Room charge is configurable
and reviewable but never offered.

## 12. Module visibility and the optional sign-in methods (PR #202 corrections, 2026-09-29)

**A site looks like what it is licensed for.** Site Type never decides this; the licence modules do.

* Without `hospitality` the day-to-day Admin Console shows no hotel concept: no PMS card or Room sign-in tile on
  the Overview, no Room sign-in method or sign-in protection, no Hotel conditions or per-night allowance in the
  package editor, no room wording or Room preview in Portal Settings, no Room charge, and **no Hotel section in the
  navigation**. The Hotel history surfaces (stays, PMS activity, sign-in checks and attempts, charge review and
  recovery) stay served while records exist and are reached from the module's card on System → Modules
  ("Records kept here"), not from day-to-day navigation.
* System → Modules lists every module the appliance reports (including `whatsapp_otp` and any it does not know
  yet). For an unlicensed module, "switched on" and "ready" show "—" and no configuration link is offered.
* Without `card_payment` nothing suggests the site accepts cards; without `paid_access` there is no price field.
* Without an identity module its sign-in method, its provider screen entries and its portal preview are absent.
* A page does not call a module-owned API for a module the site does not have, so "not licensed" is never shown
  on a generic page. edged refuses to switch on a sign-in method whose module is not licensed and deployed
  (`409 module_not_licensed`; `503` when module state is unreadable).
* A module the site has already used keeps its stored configuration visible where it must remain removable (for
  example a package that already offers Room charge).

**Optional sign-in methods.** Each is offered to a client only when all four gates pass:

| Method | Module | Deployed when | Provider readiness |
|---|---|---|---|
| Email one-time code | `email_otp` | `STAYCONNECT_IAMV2_MASTER` and `STAYCONNECT_IAMV2_OTP` | an enabled `email` sender |
| Phone code by SMS | `sms_otp` | same | an enabled `sms` sender |
| Phone code by WhatsApp | `whatsapp_otp` | same | an enabled `whatsapp` sender (Meta Cloud API or Twilio, approved authentication template) |
| Google, Facebook, Apple, Microsoft | `social_login` | `STAYCONNECT_IAMV2_MASTER` and `STAYCONNECT_IAMV2_SOCIAL` | an enabled application for that provider |

* scd removes every method that fails a gate from `/v1/tenant/auth-methods`, so the portal cannot draw it, and
  refuses to issue a code when no sender exists for the channel.
* WhatsApp codes are sent only as the parameter of an approved authentication template (Meta `template_name` +
  `language`, Twilio `content_sid`). A verified WhatsApp code proves the phone number exactly as SMS does.
* Social: Google, Microsoft and Apple are verified OpenID Connect `id_token`s (signature, issuer, audience,
  expiry; Microsoft tenant rules; Apple ES256 client secret minted per exchange from the `.p8` key). Facebook uses
  the Graph API with `appsecret_proof`. The provider's stable subject is the identity and a trusted issuer's
  verified email is a second factor on the same Client (`ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md` §2); a
  Facebook sign-in that returns no email still signs the client in by its subject. Apple returns by `form_post`.
* **Redirect URI.** The redirect URI saved on a social provider is the one registered with that provider and is
  used for both the authorize request and the code exchange. It must be `https://<portal name>/auth/social/callback`
  (edged refuses anything else). Only when a provider has none does the portal derive its own URL; behind the
  appliance's proxy it takes the scheme from `X-Forwarded-Proto`, which it believes from a loopback peer only.
  Real providers also need that portal name to reach the portal with a certificate the guest's browser trusts.
* **Deferred by the Product Owner (2026-09-29):** the public Client Portal domain and its publicly trusted
  certificate are a future decision. The portal hostname (`portal.stayconnect.local`), the Root CA and the
  certificate architecture are unchanged. Real social-provider validation (Google, Facebook, Apple, Microsoft)
  is **pending that decision and the provider credentials**; the providers are verified by deterministic fixture
  tests meanwhile. This is not a blocker for PR #202.
* **Secrets are write-only.** Sender API keys/tokens and social client secrets/keys are never returned by the API,
  never shown in the UI after storage and never logged; logs carry the provider, the HTTP status and at most the
  last four digits of a phone number.
* "Test only" senders are an explicit operator choice for deterministic verification; they count as ready.

