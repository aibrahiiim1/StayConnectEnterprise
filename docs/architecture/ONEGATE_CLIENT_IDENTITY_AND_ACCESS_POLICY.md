# OneGate — Client Identity, Client Groups and Access Policy

**Status:** architecture contract for the Client-Access delivery (branch `delivery/client-access-identity`).
Proposed for Product-Owner acceptance; it changes no other accepted contract. Where this document and older
wording disagree about these subjects, this document wins once accepted.

Product boundaries that stay in force: no Go-Live; no real payment charge; no PMS financial posting; no
Root-CA or trust change; no destructive data migration; Central remains licensing only (§0E of `CLAUDE.md`).

---

## 0. The four questions, kept apart

OneGate answers four questions about every person who connects, and answers each one in exactly one place:

| Question | Answered by | Where |
|---|---|---|
| **Who is the Client?** | a verified identity factor → one `guest_principals` row | `internal/iamv2` identity resolution (§2) |
| **What is this Client eligible for?** | the Client's effective **Client Group**, pinned on the auth context | group evaluation at sign-in (§3) + package eligibility rules (§4) |
| **Which Internet Package is offered or granted?** | the existing catalogue: active packages → eligibility rules → quote | `ListEligiblePackages` / `CreateQuote` (unchanged pipeline) |
| **How is a paid Package settled?** | the revision's acquisition methods: Free, Voucher, Card, Room charge | `ONEGATE_MODULES_AND_ACQUISITION.md` §5–7 (unchanged) |

The authentication **method** answers only the first question. It never decides the offer: a site that wants
"employees free, everyone else paid" expresses that with a Client Group and a package audience, not with
"Google = free". The `AUTH_METHOD` eligibility rule stays available for the rare site that genuinely wants it;
the Admin Console no longer leads with it.

Nothing new is built beside the existing spine. Free access is a Purchase with settlement `NOT_REQUIRED`;
a "trusted device" is a credential that mints an ordinary auth context; a Client Group is an input to the
eligibility engine. There is no second entitlement engine and no "free session" model.

---

## 1. What was found on master, and what this corrects

| Finding | Consequence today | Correction |
|---|---|---|
| OTP and social sign-ins returned the legacy `session_id` reply shape to the portal, which redirected to `/success?s=&t=0` | Email, SMS, WhatsApp and social could authenticate but never reached package selection | both paths now go through `tryIAMv2Auth` exactly like voucher, account and open selection |
| Social identity stored the **email string** as the `SOCIAL_SUBJECT` value; the provider's `sub` was discarded | contradicts the contract (`issuer-scoped social subject`); an email change at the provider = a new Client | the subject is `(issuer, sub)`; a verified email is a *second* factor on the same principal (§2.2) |
| No identity linking of any kind | a Client who used email OTP and then Google is two Clients with two quotas | provider-attested verified email links the social subject to the email-keyed Client (§2.2) |
| `PRIOR_PURCHASE` keyed on the package **revision** id | every republish of a free package reset everyone's free allowance | keyed on the package (its whole revision lineage) (§5) |
| `max_purchases_per_stay`, `renewable`, `plan_overrides` stored but never enforced | operators could set numbers that did nothing | not enforced by this delivery either; the free-allowance policy lives on the rule (§5) and the editor says so |
| Notification providers resolved once at scd boot | an SMTP change in the Admin Console took effect at the next restart | scd reloads on every provider write and on demand (§6) |
| A failed email/SMS send was logged and the client was told the code was sent | silent dead end | the issue call answers `DELIVERY_FAILED` and the portal says so (§6) |
| Provider secrets (`notification_providers.api_key`, `social_oauth_providers.client_secret`) are plaintext columns | at odds with the sealed-secret standard set by card payment | notification secrets are sealed by scd (§6.3); social secrets are recorded as a follow-up |
| No identity-provider domain is reachable before sign-in; `walled_garden_ip` is reset by every netd re-render until scd's next minute | OAuth cannot complete pre-auth; a network apply breaks a sign-in in progress | enabled providers declare their pre-auth domains (§7); netd carries the garden set across a converge |
| Email-channel kind `ses` is accepted by edged and silently falls back to the stub in scd | an operator could save a sender that never sends | `ses` is refused on create until an adapter exists |

---

## 2. Client identity

### 2.1 One Client, many verified factors

A **Client** is one `iam_v2.guest_principals` row (tenant-wide, as before). It is reached only through a
**verified factor** in `iam_v2.guest_principal_identities`:

| `factor_type` | `factor_issuer` | `factor_value_norm` | Verified by |
|---|---|---|---|
| `EMAIL` | `''` | lower-cased address | an email one-time code, **or** a trusted issuer's `email_verified` claim (§2.2) |
| `PHONE` | `''` | E.164 number | an SMS or WhatsApp one-time code |
| `SOCIAL_SUBJECT` | provider name (`google`, `apple`, `microsoft`, `facebook`) | the provider's stable subject (`sub`; Microsoft `tid:oid`) | a verified OIDC `id_token` / Graph response |

Each identity row now carries `attrs jsonb` (migration 0107): verified claims the issuer asserted about this
factor and nothing else — Google `hd`, Microsoft `tid`, the email that an IdP verified, and `source`
(`otp` or the issuer). These are what Client Group rules evaluate (§3). Nothing in `attrs` is ever supplied by
the browser.

**MAC is not identity.** A device (`iam_v2.devices`) is still linked to a Client only through auth contexts,
entitlement device slots and sessions. No table added here references a MAC.

### 2.2 Linking: only on trustworthy evidence

Resolution is deterministic and runs inside the sign-in transaction (`ResolvePrincipalByFactors`):

1. **Primary factor first.** Look up the factor the Client just proved: the email for an email code, the
   phone for SMS/WhatsApp, `(issuer, sub)` for a social sign-in. A match is the Client.
2. **Then the secondary factors the same proof carried.** A social sign-in from a **trusted issuer** that
   asserts a verified email also proves control of that mailbox. If `(EMAIL, '', email)` already belongs to a
   Client, that Client is the answer and the social subject is added to it.
3. **Otherwise a new Client is created** with every factor the proof carried.
4. Missing factors are inserted `ON CONFLICT DO NOTHING`. A secondary factor that already belongs to a
   *different* Client is **not** moved and the two Clients are **not** merged: the primary factor wins, the
   conflict is recorded (`identity_link_conflict` event) and the Client continues with the identity they
   proved. Automatic merging of two Clients is a destructive operation and is out of scope by design.

**Trusted issuers for email linking** are declared in code: Google (`email_verified`), Apple (always verified;
a private-relay address is simply the address Apple issued), Microsoft (only when the provider's own
verification rules in `internal/social/microsoft.go` report verified). Facebook asserts no verification and is
**not** a trusted issuer: a Facebook sign-in is a Client keyed by its app-scoped `sub` and nothing else.

What this gives, and what it does not:

* email code ↔ Google/Apple/Microsoft with the same verified address → **the same Client, the same quota**;
* SMS ↔ WhatsApp with the same number → the same Client (already true);
* email ↔ phone → **different Clients**. Two factors with no proof that one person holds both are never joined
  on a name, a string or a device. An explicit "add my phone to this identity" step is a recommended follow-up,
  not part of this delivery.

**Backward compatibility.** Rows written by the previous code — `SOCIAL_SUBJECT` whose value is an email
address — are honoured as a match for a social sign-in that asserts that verified email on that issuer, so an
existing Client keeps their identity. No row is rewritten or deleted.

### 2.3 A remembered device: "Welcome back"

A one-time code every time a phone reconnects is the single biggest source of friction and support calls,
and it adds no security once the device has already proven the factor. After every successful verified
sign-in (OTP or social), scd issues a **device-bound resume credential**:

* 256 random bits, stored only as a keyed HMAC in `iam_v2.principal_device_credentials` under the key
  `anonymous_access.key` with its own domain string; bound to `(tenant, site, principal, device)`;
* set as the HttpOnly, `SameSite=Lax` portal cookie `og_client`;
* valid for the site's **Remember devices** setting (default 30 days; `0` disables the feature);
* **usable only from the device it was issued to**: the MAC is resolved from the kernel neighbour table on
  every use, exactly as on every other sign-in path; a cookie presented from another device is refused and
  revoked;
* revoked by sign-out, by expiry, and — all of a Client's — when an operator disconnects the Client.

On the landing page a valid credential renders **"Welcome back, a•••@example.com — Connect"**. Connect mints
an ordinary auth context for the Client (method `OTP` or `SOCIAL` as originally proven, recorded in the
context's `evidence`), after which the existing join / package / purchase logic runs unchanged. "Not you?"
clears the cookie. Knowing an email address is never sufficient: the credential exists only because a code
or an IdP was verified on this device before.

---

## 3. Client Groups and organisations

### 3.1 The model

A **Client Group** is a site-level policy object (`iam_v2.client_groups`): `name`, `priority` (lower wins),
`enabled`, `description`, and one or more **membership rules** (`iam_v2.client_group_rules`). A Client is a
member when **any** rule matches (OR within a group). Rules:

| `rule_type` | `rule_value` | Satisfied by | Assurance |
|---|---|---|---|
| `EMAIL_DOMAIN` | `{"domains":["company.com","company.ae"], "include_subdomains": false}` | a verified `EMAIL` factor whose domain matches | verified mailbox (OTP or trusted issuer) |
| `IDP_TENANT` | `{"provider":"microsoft","tenant_ids":["<GUID>"]}` | a `SOCIAL_SUBJECT` factor from that provider whose `attrs.tid` matches | organisation membership asserted by the IdP |
| `IDP_HOSTED_DOMAIN` | `{"provider":"google","domains":["company.com"]}` | a Google factor whose `attrs.hd` matches | Google Workspace organisation |

`IDP_GROUP` (IdP directory groups / roles) is **reserved** in the contract and refused at publication: it
needs a groups claim configured on the provider application, which is a provider-side setup this delivery
does not include. It is the natural next rule type and is listed under recommendations.

The **Public** group is implicit: a Client who matches no enabled group. It is never stored.

### 3.2 Evaluation and determinism

Membership is a pure function of the Client's **verified identity rows** (§2.1), evaluated by scd when it
creates the auth context — for a code, a social sign-in and a remembered device alike — and pinned on the
context as `client_group_id` with `client_group_evidence` (which rule of which group matched, and the factor
that satisfied it). Eligibility reads the pin and never re-derives it, so a decision can be reconstructed.

* **Conflicts:** a Client who matches several groups gets the one with the lowest `priority`; ties are broken
  by group name, then id. Every matched group is listed in the evidence.
* A disabled group matches nobody. A group with no rules matches nobody.
* Voucher, client-account, open-selection and PMS room subjects have no identity factors and are never in a
  group (`client_group_id` NULL). Vouchers and accounts are already the site's own hand-issued credentials;
  giving them group semantics is a recommendation, not scope.
* **Nothing is obtainable without the verification the rule names.** An `EMAIL_DOMAIN` group needs a verified
  mailbox; `IDP_TENANT` / `IDP_HOSTED_DOMAIN` need the claim from a verified `id_token`. A Client cannot type
  their way into a group.

### 3.3 Why not just "email domain"

Email-domain matching is right for low-risk commercial benefits (a partner discount). It is wrong for
employee-only access: anyone who can receive mail at `company.com` qualifies, including an ex-employee whose
mailbox is still forwarded. `IDP_TENANT` and `IDP_HOSTED_DOMAIN` answer "is this person in the organisation's
directory *now*", which is the question an employee benefit actually asks. The Admin Console labels the two
levels plainly ("verified email address" vs "organisation account") so a Site Admin can choose without being
an IAM engineer.

---

## 4. Eligibility: audience, not method

A new eligibility rule type:

| `rule_type` | `rule_value` | Passes when |
|---|---|---|
| `CLIENT_GROUP` | `{"group_ids":[uuid…], "public": bool}` | the context's `client_group_id` ∈ `group_ids`, or `public` is true and the context has no group |

With it a site expresses every pattern in the brief without touching the authentication layer:

| Site wants | Packages |
|---|---|
| Free only | one Free package, no audience rule |
| Paid only | priced packages, no audience rule |
| Free + paid | both, no audience rule |
| Limited free then upgrade | Free package with a free-allowance rule (§5) + priced packages |
| Employees free, partners discounted, public normal | Free package `CLIENT_GROUP{Employees}`, cheaper packages `CLIENT_GROUP{Partners}`, normal packages `CLIENT_GROUP{public:true}` (or no rule) |
| VIP premium | premium package `CLIENT_GROUP{VIP}` |
| Hotel residents | the existing PMS-stay rules on packages offered by Room sign-in (unchanged) |

A "discount" is a package with a lower price and a narrower audience. There is no discount engine, coupon
table or price override: the revision is immutable and its price is the price.

The Internet Packages editor presents this as **Audience** — *Everyone* / *Only these groups* / *Public clients
only* — instead of the raw rule.

---

## 5. Free allowance and repeat access

The `PRIOR_PURCHASE` rule is the free-allowance policy, extended rather than replaced:

```json
{"forbids_prior": true, "within_hours": 24, "also_by_device": true}
```

* **Keyed on the package, not the revision.** The history lookup now joins revisions to their package, so
  republishing a Free package (a wording change, a plan repin) no longer hands every Client a fresh allowance.
* `within_hours` (optional, 1–8760): "once per Client every N hours" — a recurring allowance. Absent means
  once per Client, ever.
* `also_by_device` (default **true** for a rule on a price-0 package): the device's own acquisition history
  counts as well as the Client's. This is what stops a Client from signing in with a second email on the same
  phone for a second free allowance. It is deliberately a per-rule switch: a shared family tablet at a hotel is
  a real case, and the operator may turn it off.
* The Client key is the principal, so every linked factor (§2.2) shares one history; the anonymous subject's
  history is its device's (unchanged, §6.5 of the acquisition contract).
* **A voucher is its own allowance** (2026-10-16, audit O-10). Each card is a separate prepaid credential, so
  for a voucher redemption the device is not counted: a guest's second voucher on the same phone redeems. The
  voucher's own history still counts, and every other subject is bound exactly as above.
* **Room guests are held to the same limits** (Product-Owner decision D-J, 2026-10-16). A Room sign-in offer
  applies the package's `PRIOR_PURCHASE` rule against the STAY's history and — under the device switch — the
  device's, keyed on the package. Choosing the package behind the stay's live access is a join, not a second
  acquisition, so a room's other devices are never locked out of access the stay already holds. Client groups
  stay principal-based; a room guest is in no group. A room guest is evaluated as subject kind `STAY` (a
  `SUBJECT_KIND` rule naming `PRINCIPAL` no longer matches room guests), and `AUTH_METHOD` may name `PMS`.
* **Contradictory rules are refused at save**: the free-access limit together with "requires an earlier
  purchase" tests the same history both ways and would offer the package to nobody.

The editor exposes this as **Free access: once per client / once every N hours**, with the device switch
underneath. `max_purchases_per_stay` and `renewable` remain stored-only; the editor no longer implies they act.

**A new Free package starts at "once per client"; a new priced package starts repeatable** (2026-10-11). A
Free package with no limit is handed out again every time the same Client's allowance runs out — observed on
PRE-LIVE: a 100 MB free package taken at 07:15, exhausted at 07:24, taken again at 07:43 by the same Client on
the same phone. A second purchase of a priced package is a sale, not a loophole. The default follows the price
only while it is still the untouched default: the moment the operator chooses a limit it is theirs, whatever
the price does afterwards; an existing package keeps exactly the rules it has. The sessions screen says how
many times a Client has had a package (§9).

**No quota reset exists.** A Client's history is the entitlement ledger; nothing in this delivery deletes or
rewrites it.

---

## 6. Free-to-paid, and the portal journey

### 6.1 When the free allowance is spent

`ListEligiblePackages` now returns, beside the eligible packages, **why** each package the Client could
otherwise see was withheld — only the one reason that is safe to show: `free_allowance_used` (the Client's own
history). Every other exclusion stays silent, as before: an audience rule, a date window or a stay condition
never reveals a package the Client was not meant to see.

The package page then reads: *"You've used your free access here. Choose a package to continue."* above the
priced packages, rather than a bare list or a sign-in error. When nothing at all is offerable the page says so
and names the site team. This is the whole "upgrade journey": there is no separate funnel, countdown or
pre-selection, and the Client always sees the price before choosing.

### 6.2 The landing page

The landing page stops being two tabs of equal buttons and becomes one journey:

1. **Welcome back** (when a remembered device presents a valid credential): one primary *Connect* button and
   *Not you?*.
2. **Primary sign-in**: one method, chosen by the site (*Sign-in methods → Primary method*), rendered as the
   main form. Default order when unset: Room (hotel), Email, Phone, Client account, Voucher.
3. **Quick sign-in**: the enabled identity-provider buttons in one row ("Continue with Google / Apple /
   Microsoft / Facebook").
4. **Other ways to connect**: the remaining enabled methods, collapsed behind one link; the existing panels
   and the existing `#tabs` mechanism render them, so keyboard, RTL, `noscript` and the fixed-order tests keep
   their meaning.
5. **Continue without signing in** (open selection) and **Have a return code?** stay where they are.

Everything remains server-rendered, nonce-CSP, mobile-first, RTL-aware and translated in the six shipped
languages. The dictionary cap rises from 160 to 192 keys for the new strings; it bounds the size of a site's
translation overrides and is not a product rule.

### 6.3 After sign-in

The package page shows who the Client is signed in as (masked: `a•••@example.com`, `+20 •• ••• 1234`,
"Google account"), so a Client on a shared device can tell whose allowance they are about to use.

### 6.4 The journey survives a refusal (2026-10-11)

A refused acquisition — a card payment the site cannot take, a package withdrawn between listing and
choosing, a device limit — **returns the Client to package selection, signed in**, with the reason above the
same choices. It used to return the sign-in page, which on a phone meant another code, the mail app, and the
captive browser gone again. While the commerce session is valid (10 minutes from sign-in) the landing page
itself continues an unfinished journey at package selection; a finished one (the device is online) is left to
the success page. Captive-portal probes carry no cookie and are unaffected.

**Card is offered only in a currency the default payment account accepts.** The client pays in the package
currency (presentment). The default ACTIVE account — the one a checkout uses — accepts its own currency and,
when it is marked provider-converts and its provider converts (§9, migration 0112), the currencies the provider
presents from. Any other package currency is refused by the payment identity gate (`PAYMENT_ACCOUNT_CURRENCY`),
so the method is withheld at listing rather than refused after the choice. One function decides it,
`iamv2.SiteMethods.CardApplicable`, and every path asks it: the Room sign-in offer and choice, the listing and
quote of every other client, and again at confirm (2026-10-16, audit O-2′/O-14). A card purchase whose payment
transaction was never created (the gate refused it) is failed once it is older than the checkout expiry plus the
reconcile grace, recorded in `card_purchase_sweeps` (migration 0121), so the package is offered again.

### 6.5 Reading the code without leaving the Wi-Fi (2026-10-11)

> **SUPERSEDED 2026-10-15** for email: there is no email code to read any more. The email one-time code and the Temporary Internet window were removed by the self-service delivery ([ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md](ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md) §1). SMS and WhatsApp codes keep the shared code engine, the `/connect` link and staff help. Kept below as the record of what was built.

A Client who asked for an email code has to open their mail app before they are online. **Sign-in methods →
Reading the code** lists email services (`internal/mailreach`: Gmail / Google Workspace, Outlook / Hotmail /
Live / Microsoft 365, Yahoo, iCloud, Proton, Zoho, AOL, GMX / WEB.DE, Yandex, Mail.ru) as switches, and takes
host names for a service the presets do not know (bounded: 50 host names, FQDN only, no wildcard). The chosen
host names are opened in the walled garden only while the Email code method is on, and they are resolved
**through the guest resolver** (unbound on the appliance) so the addresses opened are the ones the clients
were handed. It is stored as `tenants.auth_methods.portal.mail_providers` / `mail_domains`, audited with the
other switches. Everything else stays closed until the Client signs in.

The pending-code behaviour of §6.2 stands: the code box survives leaving the captive browser, and the email's
*tap to connect* link completes the sign-in in the phone's real browser.

**A code is single-use AND device-bound** (2026-10-11, migration 0110). Reviewed at the Product Owner's
request: the link carries the same code the email carries, over plain HTTP on the client LAN, and a code was
single-use but verifiable from any device that obtained it. Now the challenge is bound at issue to the
kernel-resolved MAC of the device that asked (`iam_v2.otp_device_bindings`, written by scd; the challenge
store itself is a public-schema table the site runner does not alter), and `otp.VerifyFrom` refuses any other
device *before* the code is compared — the refusal counts no attempt and the portal says, in six languages,
"this code was requested from another device; enter it, or tap the link, on the phone that asked for it". The
link page still verifies nothing on GET. A challenge issued before 0110 keeps the old behaviour for the ten
minutes it lives. MAC identifies the device only, never a person.

### 6.6 Temporary internet while reading the code (Product-Owner decision, 2026-10-11)

> **SUPERSEDED 2026-10-15** for email: no pre-sign-in Internet is granted for any reason. The email one-time code and the Temporary Internet window were removed by the self-service delivery ([ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md](ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md) §1). SMS and WhatsApp codes keep the shared code engine, the `/connect` link and staff help. Kept below as the record of what was built.

Mail-service allowlisting (§6.5) cannot be made safe (a mail service's addresses serve the provider's other
services) and cannot cover every provider. So when an **email** code is issued **and sent**, the device that
asked gets a short window of **general** internet access, enforced in the kernel and separate from every
entitlement:

| Setting (Sign-in methods → *Temporary internet while reading the code*) | Default | Bounds |
|---|---|---|
| Enabled | on | — |
| How long | 10 minutes | 1–30 |
| Download / upload | 2000 / 1000 kbps | 128–20000 / 64–10000 |
| Data allowance (combined) | 25 MB | 1–200 |
| Windows per device | 3 per 24 h | 1–10 |
| Windows per email address | 3 per 24 h | 1–10 |
| Open windows at once (site) | 50 | 1–500 |
| Wait before another window (added 2026-10-12) | 30 minutes | 0–1440 |

Stored in `tenants.auth_methods.email.temporary_access`, validated by edged against
`tenantcfg.TemporaryAccessBounds`, audited with old and new values.

**Enforcement** (`internal/nft/verify.go`, render in `netcfg/render_nft.go`): a timed `(bridge, address)`
element in `verify_ipv4` (the kernel ends it at expiry, even with scd down); a per-device quota object (every
byte, both directions) and two limit objects (up, down), metered **before** the established-flow accept so no
packet escapes them. Admission is to the WAN only, after the management and client-to-client drops; an admitted
device is not captive-redirected.

**Lifecycle** (`cmd/scd/verify_access.go`, ledger `iam_v2.verification_access_grants`, migration 0111): starts
only after a successful send; ends at verification or any other successful sign-in from the device, expiry,
data exhaustion, or *use a different address*. One live window per device — a second request (another address,
a reconnection) reuses it and never resets its clock or bytes. Per-device and per-address limits are counted
from the ledger over a rolling 24 h, so they survive restarts; the address is kept only as an HMAC digest. A
reconcile every 2 s restores a live window after an scd, netd or appliance restart with **what is left** of it
(time and bytes), ends what is due, and removes strays. The code's own validity (10 minutes) is independent.

**The wait before another window** (Product Owner, 2026-10-12). A client may still have no code when a window ends
(the mail is slow, went to spam, or the data ran out). The device may be given another window once the wait has
passed since its last window ended; a window ended by choosing a different address does not start the wait, so
a mistyped address is not punished. The daily limits still apply on top. A refused window never affects the
code itself.

**What it cannot do:** a device that changes its hardware address is a new device to the network; it still
needs a fresh address within the per-address limit, a deliverable code, and a free site slot. Anonymous abuse
is bounded, not eliminated. While a window is open the phone's captive check succeeds, so the system sheet may
close; the client returns to the portal (the pending code box, §6.2) or taps the email link.

### 6.7 Staff help for a client who cannot get their code (Product-Owner decision, 2026-10-12; migration 0113)

> **SUPERSEDED 2026-10-15** for email: staff help now applies to SMS and WhatsApp codes only; the temporary-access columns and the Clear action are gone. The email one-time code and the Temporary Internet window were removed by the self-service delivery ([ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md](ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md) §1). SMS and WhatsApp codes keep the shared code engine, the `/connect` link and staff help. Kept below as the record of what was built.

**Sign-in methods → Code requests** lists the code requests of the last 24 hours: when, the address it was sent
to, the device that asked, the code's state (waiting, signed in, expired, too many wrong tries) and the device's
temporary internet (online now, data used, how it ended, the wait, the daily limit). Two actions, each behind
the operator's **password** and a **reason**, each recorded in the audit log by edged:

| Action | Permission | Effect |
|---|---|---|
| **Let them try again** | `code-requests` (write) | the device's ENDED windows are marked cleared (`cleared_at`, `cleared_by`); they stay on record but no longer count towards the per-device / per-address limits or the wait. A live window is untouched. **No access is granted**: the client still signs in with a code. |
| **Show code** | `code-reveal` (write) | reads a **live** code (unused, unexpired, not locked) so staff can tell it to the client. |

Roles: site admin, IT manager and front desk hold both; guest relations may let a device try again but not show
a code; a site viewer reads the list only; voucher and payments operators have neither.

**How a code can be shown at all.** A code is stored as a keyed hash and cannot be read back. Each code that was
**sent** also gets a sealed copy in `iam_v2.otp_code_escrow` (AES-256-GCM under the appliance's local key, its
own domain `otp-code-escrow:v1`, bound to tenant, site and challenge). Only scd can open it, only for an operator
edged has authorised; the copy is deleted when the code is used and pruned once it has expired, been used or been
locked. The code never appears in the audit record, the logs or the list.

**What showing a code means — and does not (Product Owner approval, 2026-10-12; migration 0114).** The code
still works only on the device that asked (§6.5), once, within its ten minutes. But a client signed in this way
has not proved the address by reading their own mail: **the operator is vouching for it**, and the system records
it as such:

| Rule | Where |
|---|---|
| Every reveal is recorded (`iam_v2.otp_code_reveals`, the operator) **before** the code leaves; if it cannot be recorded, the code is not shown | scd `revealOTPCode` |
| **An existing Client is never reached.** A code for an address that already belongs to a Client is not shown (`EXISTING_CLIENT`); Code requests flags the row and offers *Let them try again* instead. If a Client appears between the reveal and the verification, the verification is refused (`assisted_existing_client`) and the transaction is rolled back, so nothing on that Client changes | scd, `iamv2.resolvePrincipalAndFinalize` |
| A staff-assisted verification may create a **new** Client. Its factor carries `assurance = staff_assisted` | `iamv2.OTPFactor`, identity attrs |
| A `staff_assisted` factor satisfies **no** client-group rule (an address in a company domain earns no company group on the desk's word) | `iamv2.ruleMatches` |
| The sign-in earns **no remembered device**; the next visit proves the address again | scd `authorizeViaIAMv2` |
| The sign-in is labelled `EMAIL_OTP_ASSISTED` / `SMS_OTP_ASSISTED` / `WHATSAPP_OTP_ASSISTED` — *"…, shown by staff"* on the Sessions screen | `iamv2.AssistedCredential` |
| When the client later proves the address themselves, the same Client continues and the marker becomes `verified` (attrs merge by key) | `iamv2.OTPFactor` |
| The verification fails closed: if the reveal record cannot be read, the verification is treated as staff-assisted | scd `authorizeOTP` |

**Who sees what on Code requests.** The full address and the device's hardware address are shown only to roles
that may show a code (site admin, IT manager, front desk). Guest relations and viewers see `g•••@example.com` and
`••:••:••:••:f7:3a`; the code state, the window and the limits are the same for everyone. Masking happens in edged
and fails closed.

**The client is told when the window returns.** When a limit keeps the window closed, scd says why
(`temporary_access_withheld`) and the portal tells the client, in six languages, either how many minutes until the
device may have one again, or that it may not today — and that the desk can help.

---

## 7. Email delivery, SMS and WhatsApp

### 7.1 Custom SMTP

Channel `email` gains kind `smtp`: host, port, security (`starttls` | `tls` | `none`), optional username, a
write-only password, from address and name, a bounded timeout. SendGrid stays as the provider kind; SES is
refused on create until an adapter exists. A real relay operated by OneGate ("OneGate-managed delivery") is
a Central-side service and a separate product decision (§10); this delivery makes the appliance able to use
one by configuration the day it exists.

### 7.2 Operate it like a product

* **Validation at save**: host is a name or address, port 1–65535, TLS/STARTTLS implied by port where the
  operator left it unset, a password required for a username, a from address that parses.
* **Test**: *Send a test email* (`POST /notification-providers/{id}/test` → scd) connects, authenticates and
  delivers one message to an operator-supplied address with the stored credentials; the result and the
  provider's own error text are shown, and recorded on the row (`last_success_at` / `last_error`).
* **Health**: the Delivery page shows each channel's last success, last error and whether the Client Portal
  is currently offering the method (`method_readiness`).
* **Reload**: edged pokes scd (`POST /v1/admin/notify/reload`) after every provider write, so a change is live
  without a restart. scd also re-resolves on demand from the test route.
* **A failed send is a failure**: `/v1/auth/otp/issue` answers `502 DELIVERY_FAILED` and the portal says
  *"We couldn't send your code. Please try again or contact the site team."* The challenge is not left
  dangling as "sent".

### 7.3 Secrets

Notification secrets (SMTP password, SendGrid key, Twilio token, Meta token) are **sealed by scd** —
AES-256-GCM under `/etc/stayconnect/secrets/notify_dek.key` (created by keybootstrap), AAD bound to
`(tenant, provider id, generation)`, stored in `iam_v2.notification_provider_secret_generations`. edged never
holds the key: it forwards the secret to scd's admin socket once, and `notification_providers.api_key` is left
NULL for every row written from now on. A row that still carries a plaintext `api_key` keeps working (the
loader prefers a sealed generation, then falls back), so nothing on PRE-LIVE breaks on upgrade; re-saving the
secret seals it. Secrets are never returned by any API, shown after storage, or logged.

---

## 8. Identity providers

Google, Apple, Microsoft and Facebook keep their existing verification (`ONEGATE_MODULES_AND_ACQUISITION.md`
§12). Changes:

* `social.UserInfo` carries the verified organisation claims (`Claims`: `hd`, `tid`) alongside `Sub`, and the
  **subject is the identity** (§2.1).
* The trusted-issuer table (§2.2) is code, not configuration; it is the one place that decides whether a
  provider's "verified email" is believed.
* Every enabled provider declares the domains a browser must reach to complete its consent page
  (`RequiredPreAuthDomains`); scd adds them to the walled garden while the provider is enabled, licensed and
  deployed, exactly as card payment adds its hosted-checkout domains. Provider pages pull assets from a
  changing set of hosts; the declared sets are the documented minimum, and *Allowed sites* remains the place to
  add what a provider changes. The token exchange is server-side and needs no client access.
* netd's converge now carries `walled_garden_ip` across a re-render, as it already carried the two
  authorisation sets.
* **Still deferred by the Product Owner (2026-09-29):** the public Client Portal hostname and its publicly
  trusted certificate. Real providers refuse a redirect URI on `portal.stayconnect.local`. Social sign-in is
  verified by deterministic fixture tests and the stub provider; **it has not been validated against a real
  provider** and cannot be until that decision and the provider credentials exist.

---

## 9. Admin Console

The **Client Portal** section becomes **Client access** and is ordered as the job is done:

| Page | What it owns |
|---|---|
| Sign-in methods | which methods are on; **Primary method**; **Remember devices (days)**; **Reading the code** — the email services reachable before sign-in (§6.5) |
| **Client groups** (new) | groups, priority, membership rules, with the two assurance levels explained |
| Identity providers (was *Social login*) | provider applications |
| Delivery (was *Email & SMS*) | email (SendGrid / your own SMTP), SMS, WhatsApp; test; health |
| Portal settings | unchanged |
| Allowed sites | unchanged; lists what providers added automatically |

**Internet packages** gains *Audience* and *Free access* in the package editor (§4, §5). Nothing moves
between sections; the four acquisition methods stay on *Payment methods*.

**Sessions say how the client signed in, and how often they had the package** (2026-10-11). The auth context
records a *credential* label beside its method — `EMAIL_OTP`, `SMS_OTP`, `WHATSAPP_OTP`, `SOCIAL_GOOGLE` /
`_APPLE` / `_MICROSOFT` / `_FACEBOOK`, `VOUCHER`, `ACCOUNT`, `OPEN` (migration 0109; `iamv2/credential.go`) —
and scd copies it onto `sessions.credential_method` when it opens the session, so *Signed in with* reads
"Emailed code", "WhatsApp code" or "Google" instead of a bare "PRINCIPAL". A remembered device carries the
credential it was issued under, so a resumed sign-in is reported as what it originally was. The session row
also carries `package_acquisitions` — how many times the same subject has had that package, any revision,
counting this one — shown as a *2nd time / 3rd time* badge on the row and as a line in the session detail. The
label is never a secret and never an identity; edged keeps its column-level grant on `auth_contexts` (0083)
and reads both facts from the session.

**The currency a card can settle is said where it is decided.** Payment methods lists, under the accounts,
every active package priced in a currency no active account settles ("Not offered by card: OneDay (USD) —
your active account settles GBP"), and the package editor says the same beside the price when Card is ticked.
Both read the DEFAULT active account and its provider-converts mark, exactly as scd decides (§6.4); a
currency the provider converts on a provider-converts Stripe account is not listed as a problem.

**Provider-managed currency conversion (Product-Owner decision 2026-10-11; migration 0112).** The client sees
and pays the package price in the package's currency (presentment). An account may be marked
**provider-converts** (Payment methods → *Let the provider convert*; step-up; audited in
`payment_account_conversion_changes`), and only for a provider that converts: **Stripe** presents a Checkout
Session in a supported currency and settles into the account currency at its own rate; **Paymob** settles in
the merchant's currency and is refused by the definer (`PAYMENT_CONVERSION_UNSUPPORTED`). The payment identity
gate accepts a CHARGE in another currency only for such an account; a refund or chargeback is always recorded in
its charge's currency. After a verified capture the engine records, once and append-only, the provider's own
report from the charge's balance transaction (`payment_settlement_reports`): presentment currency and amount as
charged, settlement currency and amount, the exchange rate **as the provider sent it (text, never recomputed)**,
fee, net and the balance-transaction id. OneGate computes no rate and performs no FX. Card is offered for a
package only in the account currency, or — on a provider-converts Stripe account — in a currency from a
conservative supported list (Stripe remains the authority; a currency it refuses fails as *not created* and the
client is offered another way). Settlement, reconciliation, idempotency, UNKNOWN handling, refunds and
chargebacks are unchanged. **Room charge is unchanged:** the package currency must equal the pinned interface's,
with no conversion.

**Only approved payment methods are offered or accepted (2026-10-12).** The acquisition methods a package may
list are Free, Voucher, Card and Room charge — the database refuses any other value on a revision (0095), and a
client outside the Room sign-in path can be offered only Free, Voucher or Card (`applicableMethods`, pinned by
`TestOnlyApprovedMethodsAreEverOffered`). `MANUAL_APPROVAL` stays dormant in the settlement contract and is never
offered; there is no cash method. **Card means card**, at the provider too:

* **Stripe.** Every Checkout Session asks for `payment_method_types = [card]` and sets
  `wallet_options[link][display] = never`. On a Managed Payments account that refuses the explicit type, the
  adapter retries once with Managed Payments switched off, still card only. **There is no variant that lets
  Stripe choose the methods**: an account that refuses both is reported as *not created*, and the client is
  told Card payment is unavailable. A created session whose `payment_method_types` is anything other than
  exactly `[card]` is expired before any client sees it. Apple Pay and Google Pay remain, because Stripe treats
  them as card wallets and reports them as type `card`; Link is a separate stored-credential wallet and is not
  offered.
* **Paymob.** The checkout offers only the integration ids the operator configured as card integrations.
* **At capture, both adapters check the method actually used.** A charge whose method is not `card` (Stripe
  `payment_method_details.type`, Paymob `source_data.type`) is never reported captured: it stays unresolved, and
  after the reconciliation grace it becomes UNKNOWN and goes to financial review for a person to decide. No
  access is granted from it automatically.

Measured on PRE-LIVE (Stripe TEST mode, 2026-10-12): the site's account accepts the card-only request, and the
hosted page offers the Card form only, with the express frame for card wallets and no Link.

**Refunds, disputes and chargebacks (Product-Owner policy 2026-10-12; migration 0115).** OneGate never starts a
refund or any other reversal; it records what the provider reports and applies one policy:

| Provider reports | Ledger | Access |
|---|---|---|
| A **full** refund (refunds recorded against the charge ≥ the charge) | REFUND rows; settlement `REVERSED` | **Ends only the entitlement that charge's purchase funded** (purchase → settlement → entitlement is 1:1:1), reason `REVOKED`; its devices are disconnected and its sessions ended, as the Phase-6 expiry writer does; one append-only row in `refund_access_terminations` records the amounts and the transition. Idempotent. |
| A **partial** refund | REFUND row; settlement `PARTIALLY_REVERSED` | Unchanged |
| An open dispute, or a dispute the merchant **won** (Stripe) | Nothing — no money moved | Unchanged; a **DISPUTE case** is opened once |
| A dispute the merchant **lost** (Stripe), or any chargeback | CHARGEBACK row; settlement reversed | **Unchanged**; a **CHARGEBACK case** is opened. If a charge has both a chargeback and refunds, the case decides |
| UNKNOWN or anything not CAPTURED | Unchanged (settlement to review, as before) | Unchanged; never a blind retry |

A case is decided once by a payments operator on **Financial review → Card disputes and chargebacks**: *Keep
access* or *End access*, with the password and a reason, recorded on the case (`card_reversal_reviews`, which
refuses deletion and any change after a decision) and in the audit log. The outcome role can open a case and
never decide one. Previously every Stripe dispute that was not an inquiry was booked as a chargeback, including
open and won ones; a chargeback also counted towards a "full refund". Both are corrected. Verified in the Phase-4
harness; on PRE-LIVE only the sandbox paths a test card can drive are exercised (§ acceptance checklist).

Settings follow the operational-settings rule: persisted, defaulted, bounded, audited. Group changes are
recorded in `iam_v2.client_group_changes` (append-only) and in the audit log; `Primary method` and `Remember
devices` live in `tenants.auth_methods.portal` and are audited with the other switches.

---

## 10. Security, privacy and failure

### 10.1 What a device can reach before sign-in — measured, hardened, and what remains (2026-10-11)

Measured on PRE-LIVE from a client network namespace that had **not** signed in, with the Gmail preset on
and one operator rule (`example.com`, ports `[443]`):

| Finding | Before | Now |
|---|---|---|
| Every port was open to every garden address: port 80, UDP 443 (QUIC), any TCP port to a Google address | all ports | three sets (`walled_garden_ip` = address-only rules, every port; `walled_garden_tcp` / `walled_garden_udp` = `address . port` pairs). An email service opens TCP 443, 993, 5228, 5223 only; an identity provider or payment page TCP 443 only. |
| A rule's **Ports** were stored and ignored (a page came back on port 80 from a rule that named 443) | ignored | enforced: the named ports, TCP and UDP; "every port" only when none is named |
| The baseline resolvers (1.1.1.1, 8.8.8.8, 8.8.4.4) were open on every port — DNS-over-HTTPS to them worked before sign-in, and whatever else those addresses serve | all ports | port 53 only (TCP and UDP) |
| The guest resolver (unbound on the gateway) answers any name recursively — a DNS-tunnel channel, as at every captive portal | unlimited | **rate-limited**: a device that has not signed in is held to 20 queries/s (burst 40) per address by the firewall, to the gateway and to the public resolvers alike, counted in `dns_preauth_limited`; unbound bounds every client to 100 qps (`ip-ratelimit`) and every zone to 1000 qps upstream (`ratelimit`), and refuses ANY. Resolution still works; tunnelling is throttled, not made impossible. |
| IPv6 (corrected 2026-10-11: an earlier count looked for `ip6 ` and missed the `meta nfproto ipv6` form) | already dropped on guest input and guest-ingress forward | the guard now also covers the **return path** (`oifname @guest_interfaces`), sits **before** the established-flow accept, and counts every refused packet in `guest_ipv6_dropped`. Host forwarding and the appliance's own WAN/management IPv6 are untouched. |
| **A name behind a shared front end**: `mail.google.com` resolves to an address that also serves YouTube, Search, Drive, Docs, Translate — all reached pre-auth by sending that host name to that address | reachable | **Mail services: closed by default (2026-10-12)** — the mail allowlist is not applied while temporary internet is on. **Identity providers and the payment page** (opened only while their method is on) keep the same property: the kernel filters addresses and the service is chosen by the TLS server name, so no address/port rule can isolate a CDN-hosted domain. Accepted for those, because the sign-in or the payment cannot work without them. |

**Why port enforcement is within the accepted model:** the sets, the forward accepts and the captive-redirect
exclusion are the same mechanism, narrowed. `nftconverge` carries all three sets across a structural
re-render (`address . port` elements are read back as pairs); scd syncs the three in one transaction and keeps
syncing the address-only set on an appliance whose netd has not been re-rendered yet. Deploy order: netd (the
render) before scd (the sync).

**What the shared-front-end finding means for "Reading the code".** Opening a mail service opens the other
services its addresses serve, to a device that has not signed in, for as long as the Email code method is on.
Decided 2026-10-11: §6.6 (a bounded, per-device window of general access) replaces reliance on mail-service allowlisting for reading the code; the options considered were:

1. **Bounded, per-device verification access** — open the mail-service pairs only for the device that has a
   code in flight, for the code's lifetime (a timed `saddr` set keyed by the device, written by scd at issue,
   cleared at verify/expiry; rate-limited by the existing sign-in protection). Exposure drops from "every
   device, always" to "one device, ten minutes, once per code". This is the design recommended; it is a change
   to how the garden is applied (a new timed set and rule in the render) and therefore a controlled decision.
2. **Server-name filtering** — a TLS ClientHello inspection path (SNI) for pre-auth 443, which isolates a
   domain behind a shared address. A new enforcement component, not a narrowing of this one.

What the market does, as far as it is documented: captive-portal products allow pre-authentication
destinations by domain or address list (the same mechanism, with the same CDN limitation), and some grant a
short, time-boxed "verification" window of broader access after the code is requested.

**Correction (2026-10-12).** An earlier version of this paragraph still called the window *restricted to the
mail services* the recommended design. That was superseded the same day: the Product Owner decided a bounded
window of **general** internet access for the device that asked (§6.6), because restricting it to mail
services inherits exactly the shared-front-end limitation measured above. §6.6 is the implemented and binding
design; option 1 above is kept as the record of what was considered. The mail-service allowlist of §6.5
remains site-configured but, **since 2026-10-12, is not applied at all while temporary internet is on** (the
default): applied site-wide it opened the services behind the providers' addresses (video, search, storage) to
every device before sign-in, and the per-device window already gives the device that asked what it needs. It
applies only on a site that switches temporary internet off, with this exposure stated on the screen.

### 10.2 Residual risk: the code link travels over plain HTTP (stated 2026-10-12)

> **SUPERSEDED 2026-10-15** for email: the email code link no longer exists; the SMS link is unchanged. The email one-time code and the Temporary Internet window were removed by the self-service delivery ([ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md](ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md) §1). SMS and WhatsApp codes keep the shared code engine, the `/connect` link and staff help. Kept below as the record of what was built.

The email's *tap to connect* link is `http://<gateway>:8380/connect?c=<challenge>&k=<code>`. The portal has no
publicly trusted certificate, so the link cannot be HTTPS without a separate certificate / Root-CA decision
(the public portal hostname, deferred 2026-09-29). The code therefore appears in clear text in two places: in
the email itself, and on the client network when the phone opens the link.

| What protects it | Effect |
|---|---|
| Device binding (0110) | the code verifies only from the device that asked; any other device is refused before the code is compared, and no attempt is counted |
| Single use, ten-minute lifetime, attempt limit | a code is spent once; a stale or guessed code fails |
| GET never verifies | opening the link (by a mail scanner or a preview) spends nothing; only the page's Connect button verifies |
| No logging, no Referer, not indexed | the portal's request log prints the path with the query replaced by `[redacted]`; the page sends `Referrer-Policy: no-referrer` and `X-Robots-Tag: noindex, nofollow` |
| What it grants | a sign-in for that one device; payment, PMS and management stay behind their own checks |

**What it does not protect against:** an observer who can read traffic on the same client network *and*
present the binding device's hardware address — that is, someone already able to impersonate the device on
the LAN — could use a code captured from the link within its ten minutes. Anyone who can read the client's
mailbox can read the code in any case. **The risk remains open**: it is closed only by serving the portal over
HTTPS with a publicly trusted certificate.

**Investigated 2026-10-12 — why HTTPS cannot be delivered inside the current authorisation.** Nothing on the
appliance can obtain or serve a publicly trusted certificate for the portal today:

* A public CA issues only for a **public DNS name the operator controls**. The portal is reached as the
  gateway's private address or `portal.stayconnect.local`; no public hostname exists (deferred 2026-09-29).
* Proving control needs **HTTP-01** (public inbound :80 to the appliance — a topology change) or **DNS-01**
  (credentials for the domain's DNS provider and a Caddy build with that provider's plugin — external
  infrastructure). The appliance has neither; ACME exists only on Central.
* Serving it needs a **guest-side accept on the gateway's :443** (or 8343): today a pre-auth guest may reach only
  DHCP, DNS and :8380 on its gateway, and unauthenticated 443 is reset. The repository also records the Product
  Owner's earlier "no portal PKI / no interception" decision
  (`TestNothingHereTerminatesTLSOrCarriesACertificate`).
* Caddy can terminate TLS with an imported certificate, and portald has a dormant TLS listener, but neither has a
  path to a public certificate, and the admin certificate manager accepts only its local CA.

**The design, ready to build once decided.** A site setting *Portal hostname* (e.g. `wifi.<hotel-domain>`), the
guest resolver answering it with each network's own gateway, a publicly trusted certificate for it — either
imported by the operator (any public CA; renewed by the operator) or issued by ACME DNS-01 with the domain's
DNS-provider token — served by Caddy on the gateway's :443 to guests (one new per-gateway accept, no
interception, no DNAT of other HTTPS), the email link and the portal's own links built on `https://<hostname>`,
and DHCP option 114 kept HTTP (captive detection still starts over plain HTTP everywhere). Local-first and
outage-safe: the certificate and key live on the appliance; renewal needs the CA only every few weeks. **Decision
needed from the Product Owner:** the hostname and domain, and which issuance path (operator import, or DNS-01
with whose DNS provider) — together with lifting the recorded "no portal PKI" decision for this purpose.

### 10.3 Other properties

* **Server-derived device identity on every path**, including the remembered-device path.
* **Uniform timing** for the existing PMS path is untouched; OTP verification keeps its durable throttle and
  the resume credential inherits the recovery-code limiter (5 attempts per device per 10 minutes).
* **No enumeration**: the portal's messages for a wrong code, an unknown credential and a used allowance do
  not reveal whether an address is known to the site.
* **PII**: masked identities only on guest pages; `attrs` holds claims, never tokens; group evidence names
  rule ids and factor types, not addresses.
* **Failure isolation**: a provider that cannot be reached (IdP, SMTP, SMS, WhatsApp, payment) removes its own
  method or its own settlement option from what the portal offers (`method_readiness`, `applicableMethods`)
  and nothing else. Voucher, Client account, Room sign-in and open selection never depend on the Internet.
* **Fail closed** everywhere a lookup fails: no group, no credential, no linking.

## 11. Upgrading an existing appliance

Migration **0107** is additive. On a live site it is applied through `scripts/edge-migrate.sh` as
`iam_v2_owner` (`--target-kind live-site`, SHA pinned), which owns nothing in `public`; therefore:

1. apply `0107_a_client_is_one_identity_with_groups_and_a_remembered_device` and then
   `0108_deleting_a_client_group_clears_only_the_pin` with the runner (0108 corrects 0107's pin constraint so
   deleting a group clears only `client_group_id`);
2. run `deploy/scripts/extend-notification-kinds-smtp.sql` as the table owner (`stayconnect`) — it widens
   `public.notification_providers.kind` to accept `smtp` and refuses to run before 0107 is in the ledger;
3. reconcile Gate-P with `scripts/gatep-reconcile.sh` from the same revision (the 0107 grants are in
   `svc-scd-iamv2-guest-auth-grants.sql` and `svc-edged-phase345-admin-grants.sql`);
4. install the new `keybootstrap` and run it once (`KEYBOOTSTRAP_DSN=…`): it creates `notify_dek.key`
   (0600) and touches nothing that exists;
5. install `scd`, `edged`, `portald`, `netd` and the Admin Console bundle and restart them.

Nothing here rewrites a row. Existing senders keep working from `api_key` until their secret is re-saved;
existing social Clients keep their identity through the lookup-only legacy claim (§2.2).

## 12. What stays inactive until separately authorised

LIVE-mode provider transactions, real PMS posting on any appliance other than PRE-LIVE (D47), Go-Live, and
real social-provider validation (needs the public portal hostname decision).

## 13. Recommended next decisions (not implemented)

1. **Public Client Portal hostname and certificate** — blocks every real IdP and is the only way to close the
   plain-HTTP code-link risk (§10.2 has the investigation and the ready design).
2. **OneGate-managed email relay** — a Central service with per-site sending identity; the appliance side is
   ready (an `smtp` row pointing at it).
3. **Seal social provider secrets** the same way as notification secrets (same mechanism; not done here to
   keep the social loader change bounded).
4. **`IDP_GROUP` membership rule** once a provider application is configured to emit groups claims.
5. **Explicit factor linking** ("add my phone to this identity") for Clients who want one identity across
   email and phone.
6. **Groups for client accounts** (an operator-assigned group on a username/password account).
7. **Walled-garden port enforcement** (`ports` is stored but not enforced) and narrowing the baseline public
   resolvers that are open on every port before sign-in.
