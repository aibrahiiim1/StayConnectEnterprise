# Data Ownership Matrix

> Which database owns each table, what crosses the boundary, and the PII rule.
> Cloud DB = `stayconnect` (central Postgres). Edge DB = `stayconnect_site`
> (one isolated database per site).

## 1. The rule

**The cloud owns the licensing relationship; the site owns its clients' data.**
Client PII — including any PMS guest data on a site with the Hotel module — is
created, used and retained exclusively at the site. Central serves the appliance
for licensing only (CLAUDE.md §0E): what flows up is the appliance's own identity, certificate request and acknowledgements; what flows
down is the signed assignment, certificate and signed licence. Neither
direction ever carries a client identity, and no operational telemetry is sent.

## 2. Cloud-owned tables

Customers (`tenants`), `sites`, `appliances` and their assignment, PKI,
retirement, security-alert and offline-activation tables, `licenses` (one
current per appliance), `retired_appliance_identities` (identity keys that may
never register again), `operators`/`operator_roles` (Central sign-ins only) and
`audit_log`. The full list is [CLOUD_ARCHITECTURE.md §2](CLOUD_ARCHITECTURE.md#2-central-database-ownership).

Central holds no client or commercial history. Subscriptions, plan views,
enrollment tokens and fleet telemetry tables were dropped by migrations 0045 and
0046; the `legacy_archive` schema (retired plans and subscription events, and
older guest/session/voucher/accounting archives) and the operator-SSO and
commercial remnant columns were dropped by migration 0047.

## 3. Edge-owned tables (never leave the site)

| Table | PII? | Notes |
|---|---|---|
| `tenants` (1 row) | — | this site's identity + `auth_methods` + `branding` |
| `sites` (1 row), `appliances` | — | local mirror of this site's identity |
| `operators`, `operator_roles` | staff emails | the seven site roles; site staff accounts live here, not in the cloud |
| `ticket_templates` (**GuestAccessPlan**) | — | |
| `voucher_batches`, `vouchers` | codes | voucher codes are treated as secrets (PII-adjacent) |
| `iam_v2.guest_access_accounts` | **YES** | username (the email address for self-registered accounts), display name, notes, creating device's MAC |
| `iam_v2.guest_principals`, `iam_v2.guest_principal_identities` | **YES** | display name; email, phone and social-provider subject |
| `iam_v2.stays`, `iam_v2.stay_guests`, `iam_v2.stay_events` | **YES** | PMS mirror: reservation, room, names, date of birth, VIP, travel agent; raw PMS event payloads |
| `iam_v2.sessions`, `iam_v2.devices` | **YES** | IP, MAC, per-client timing and byte totals |
| `iam_v2.accounting_records` | **YES** | per-sample usage detail |
| `iam_v2.sign_in_attempts` | **YES** | submitted room, device IP/MAC; names sealed |
| `accounting_records` | **YES** | legacy per-session byte counters (no current writer) |
| `auth_otps` | **YES** | hashed codes, destinations (email/phone) |
| `social_oauth_states` | **YES** | IP+MAC-bound OAuth state |
| `pms_providers` | credentials | PMS hosts/keys — site infrastructure secrets |
| `pms_attempts` | **YES** | room numbers, attempt IPs |
| `walled_garden_rules` | — | |
| `notification_providers`, `social_oauth_providers`, `stripe_accounts` | credentials | provider secrets stay on-site |
| `iam_v2.payment_transactions`, `stripe_events` | **YES** | payment references tied to a guest's purchase |
| `audit_log` | staff + client refs | local compliance record |
| `tenant_effective_limits` (plain TABLE) | — | derived from the signed license; local bridge |
| `edge_offline_packages` | — | single-use ledger of imported offline activation packages |
| `backup_records` | — | |

## 4. What syncs (and what never does)

### Edge → Cloud (HTTPS, appliance-initiated)

| Item | Endpoint |
|---|---|
| Self-signed registration (serial, hardware fingerprint, identity public key, MACs) | `POST /v1/appliances/register` |
| Certificate signing request | `POST /v1/appliance/csr` |
| Signed hello (detects a deleted record) | `GET /v1/appliance/hello` |
| Assignment acknowledgement (adopted version; a terminal ack is retried until confirmed) | `POST /v1/appliance/assignment/ack` |
| Offline-activation reconciliation (the consumed package id; retried until confirmed) | `POST /v1/appliance/offline-reconcile` |
| Client Portal hostname and certificate (portal address, hotel domain; CSR) | `POST /v1/appliance/portal-tls/sync`, `POST /v1/appliance/portal-tls/orders` |

**No telemetry.** The appliance has no telemetry outbox, producer or message-bus
client (CLAUDE.md §0E); the subsystem and its tables (`sync_outbox`,
`sync_checkpoints` and the cloud-mode / cloud-sync settings) were removed on
2026-09-27 by appliance migration 0093.

### Cloud → Edge (pulled by the appliance)

| Item | Endpoint |
|---|---|
| Signed assignment (customer, site, state, version) + signed key registry | `GET /v1/appliance/assignment`, `GET /v1/appliance/assignment-registry` |
| Client certificate | `GET /v1/appliance/certificate` |
| Signed license envelope + revoked license IDs + `server_time` | `GET /v1/appliance/license` |

### Never syncs, in either direction

- Client identities, MACs, IPs, emails, phones, names, room numbers,
  reservation data, OTP codes, voucher codes, session rows, accounting rows.
- Local operator password hashes.
- PMS / Stripe / SendGrid / Twilio / OAuth credentials.
- The vendor **private** signing key (cloud-only, never leaves).

## 5. The PII boundary, precisely

```
        SITE (edge DB)                       │            CLOUD
  guests · sessions · accounting · OTP       │   customers · sites · appliances
  PMS attempts · payments · vouchers         │   assignments · certificates
  local operators · local audit              │   licenses · Central operators · audit
                                             │
        ──── registration · CSR · ack ───────▶   (appliance identity only)
        ◀── signed assignment · cert · license    (no client data ever crosses)
```

Consequences:

- A cloud compromise cannot expose a site's clients — the data simply is not there.
- GDPR/data-locality: client data stays on the site's own premises as far as
  Central is concerned. Optional providers the hotel enables (email, SMS,
  WhatsApp, social sign-in, payments) do receive the data they need to work;
  see [privacy/GDPR_REMEDIATION_PLAN.md](privacy/GDPR_REMEDIATION_PLAN.md).
- Retention: only sign-in attempts (30 days), throttle and OTP rows, and
  accounting detail (opt-in, unlimited by default) are purged automatically
  today. Retention for the rest of the client data is planned work (GDPR plan
  C1).
- Cloud support staff see only activation, connection and licence state; to
  diagnose a site they ask site staff to act in Admin Console.

## 6. Duplicated-by-design rows

The edge DB's `tenants`/`sites`/`appliances` rows mirror Central's registry
for this one site (same UUIDs). This duplication is intentional: FK integrity
for the client domain without any runtime cloud dependency. Tenant and site are
**not** authoritative locally — the only authority is the verified signed
assignment, and entitlement truth is the signed license.

## 7. Central client-domain tables are gone

The historical client-domain tables on Central (guests, sessions, vouchers,
accounting, OTP, PMS, payments, walled garden, networks, …) were verified empty
and dropped by migration 0046, together with the deprecated `/v1` adapters that
read them ([API_DEPRECATIONS.md](API_DEPRECATIONS.md)). The `legacy_archive`
schema, which on the live Central still held an older archived copy of
guest/session/voucher/accounting history, was dropped with everything in it by
migration 0047 (Product-Owner authorized, 2026-09-27).

## 8. Whose data a guest request can reach (decision D-R5, migration 0145)

A PMS may name only the reservation holder while several people use the same room and its credentials. A room
number, a reservation credential, a MAC address or a shared internet access identifies nobody, and one PMS guest
row is never taken to mean one occupant. Guest data on the appliance therefore falls into three classes, and a
Hotel Admin *Guest data request* (export or erasure) reaches only the class its anchor owns:

| Class | What it is | Reached by | Never reached by |
|---|---|---|---|
| **PMS reservation data** | the stay, its guest rows, PMS messages, folio (room charges), post-stay access | a *stay* or *stay guest* request: the guest the PMS names | an account or identity request |
| **Individual data** | a client account the guest registered themselves, an OTP/social identity with its remembered devices, their own entitlements and purchases, and the devices **only they** used | an *account* or *identity* request: the person who registered or verified it | a stay request |
| **Shared access** | access used through a room (stay) credential, a voucher, an anonymous subject or a staff-issued account, and any device with a link to anyone else | no automatic request: the system never discloses or changes it, counts it, and lets its liveness block an erasure. A request about it is the hotel's to handle by hand (below) | every automatic request |

**A device is one person's only when** something that person did binds it to them (a remembered-device tick, the
self-registration made on it, or authentication of their own identity or self-registered account on it) **and**
nothing else touches it: no other entitlement, session, credential, sign-in or account. One foreign link makes it
shared. A person's own phone that was also used with the room's credentials is shared, because the MAC address
does not say who held it. The same rule decides which MAC-keyed records (sign-in restrictions, verification
grants, code bindings, sign-in redirects) a request can reach. A search by MAC address offers accounts and
identities, never a reservation.

A PMS guest number identifies a person only **within one PMS interface**. Across interfaces (a transfer between two
PMSs) only a unique full name can tie a guest, and an ambiguous name is refused, never guessed (D-R4). Staff-issued
accounts may be handed to a whole party, so their devices are never attributed to one person.

Some records carry no person but name a one-time code: a verification access and a code-device binding. They are
tied to a person by **the address the code went to** (an identity's email or phone, a self-registered account's
email), whatever device it happened on, and the same codes decide whether a device is shared: a verification on it
for anyone else's address, or a sign-in with an access that is not theirs, makes it shared.

A staff-issued account's own record and its usage totals are reachable by a request about that account; what
other people typed or paid through it (sign-in attempts, purchases, payments, sign-ins) is not attributed to its
holder, because the credential may be shared by a party.

Attempts to enter a reservation's room credentials are the typist's, not the holder's: erasing the reservation
removes what was typed (the holder's name, the room) and keeps who typed it. The export tells the requester that
shared records exist (`not_included`: counts only) without listing them.

### Automatic handling, and the hotel's own responsibility

The product handles by itself only what it can attribute to one person. Everything else is the hotel's manual and
legal responsibility as the controller. These are engineering notes for the hotel's legal review (plan D-F3), not
legal advice.

| | The system does | The hotel does |
|---|---|---|
| **Data attributable to one person**: the reservation's own data for the guest the PMS names; an account or identity the person registered, their own access and purchases, and the devices only they used | finds it, exports it and anonymises it on a staff request, with the operator's password and a reason, audited, leaving a reference to give the person | checks who is asking, hands the export over, and answers within one month (GDPR Art. 12(3)) |
| **Shared access**: room, voucher, anonymous or staff-issued-account access, and any device with another link | nothing. It never discloses it and never changes it, and it tells the requester only that such records exist (counts, no content) | decides by hand, under its own legal responsibility, what it can lawfully disclose or erase and to whom, and answers the requester |
| **Retention** | anonymises shared records when the hotel's retention period ends | does not count that as an answer: a request is answered within one month whatever the retention setting says |

**Retention expiry is not a response to a rights request.** The retention period limits how long data is kept
(Art. 5(1)(e)). It does not answer an individual's request for access or erasure, which has its own deadline. A
hotel that receives a request about shared access must respond to it; waiting for the records to age out is not a
response.

**What the hotel does with a request about shared access** (outside the product):

1. Verify who is asking, as for any request.
2. Ask the requester for the additional information that ties the records to them (for example the device they use
   and the times), because a room number, a reservation credential or a MAC address alone does not (Art. 11(2),
   12(6)).
3. Weigh the rights and freedoms of the other people who used the same room or device, since disclosing shared
   records can expose them (Art. 15(4)).
4. Decide, with legal advice, what to disclose or erase, and record the decision and its reason.
5. Answer within one month, or say why not and that the requester may complain to the authority (Art. 12(3)-(4)).

The product gives the hotel no tool to attribute or erase individual shared records, and none should be improvised
(no direct database edits). Hotel Admin's usage and session screens can be read to find what a decision concerns.
Whether to build a controlled manual path for this is a product decision that has not been taken (plan section 4).
