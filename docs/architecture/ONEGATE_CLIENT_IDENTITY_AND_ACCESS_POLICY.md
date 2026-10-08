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

The editor exposes this as **Free access: once per client / once every N hours**, with the device switch
underneath. `max_purchases_per_stay` and `renewable` remain stored-only; the editor no longer implies they act.

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
| Sign-in methods | which methods are on; **Primary method**; **Remember devices (days)** |
| **Client groups** (new) | groups, priority, membership rules, with the two assurance levels explained |
| Identity providers (was *Social login*) | provider applications |
| Delivery (was *Email & SMS*) | email (SendGrid / your own SMTP), SMS, WhatsApp; test; health |
| Portal settings | unchanged |
| Allowed sites | unchanged; lists what providers added automatically |

**Internet packages** gains *Audience* and *Free access* in the package editor (§4, §5). Nothing moves
between sections; the four acquisition methods stay on *Payment methods*.

Settings follow the operational-settings rule: persisted, defaulted, bounded, audited. Group changes are
recorded in `iam_v2.client_group_changes` (append-only) and in the audit log; `Primary method` and `Remember
devices` live in `tenants.auth_methods.portal` and are audited with the other switches.

---

## 10. Security, privacy and failure

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

1. **Public Client Portal hostname and certificate** — blocks every real IdP.
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
