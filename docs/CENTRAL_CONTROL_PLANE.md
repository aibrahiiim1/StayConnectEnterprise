# OneGate Central — licensing, activation and fleet status

**Authoritative, current.** This file describes what Central is, how it is organised, the appliance activation and
licence lifecycle, and the API contract between the Central console (`cloud-admin`), the Central API (`ctrlapi`,
`control-plane/`) and the appliance (`scd` / `edged` / Hotel Admin). Where another document disagrees about Central,
this file wins. Product-Owner mission "Central Control Plane refactor & activation redesign" (2026-09-27).

## 1. What Central is — and is not

Central is the vendor's cloud-hosted **licensing, appliance-activation and fleet-status** system. It answers five
questions: *who is the customer, which site and appliance, is it activated, what does its licence allow, and is it
healthy right now.*

Central is **not** a hotel operations system. It holds no guest, session, usage, voucher, PMS, payment, portal or
network configuration, and it offers no remote control of an appliance. Those live on the appliance (CLAUDE.md §0E:
Central serves the appliance for licensing only).

The object model is fixed:

```
Customer ─┬─ Site ─── Appliance ─── Licence (one current, with history)
          └─ Users (customer's own Central sign-ins, optional)
```

A **Customer** is the organisation that owns hotels. A **Site** is one physical property. An **Appliance** is one
OneGate box, bound to a site by Central's signed assignment. A **Licence** is a vendor-signed document bound to one
appliance's hardware and identity key.

## 2. Console structure

There is **no global customer selector**. Every screen is either fleet-wide or reached by drilling into a customer or
appliance.

| Navigation | Route | What it is for |
|---|---|---|
| **Overview** | `/overview` | Fleet at a glance: customers, appliances, activation, connection and licence counts, and everything that needs attention (waiting for activation, licences expiring/expired/suspended, appliances offline, open security alerts, retirement not confirmed). Every number links to the filtered list. |
| **Customers** | `/customers` | Customer list with sites, appliances, licence health. |
| &nbsp;&nbsp;Customer | `/customers/[id]` | One customer: summary, **Sites** (create/edit/archive), **Appliances**, **Licences**, **Users**, **Activity**. |
| **Appliances** | `/appliances` | Every appliance across customers, filterable by activation, connection and licence state, customer and site. Appliances **waiting for activation** are listed first. |
| &nbsp;&nbsp;Appliance | `/appliances/[id]` | One appliance: status, licence (with history), activation, identity and hardware, activity; every lifecycle action. |
| **Licences** | `/licenses` | Every licence, filterable by state (active, expiring, grace, expired, suspended, revoked). |
| **System** | `/system/...` | Out of the daily workflow: **Security alerts**, **Trust & keys** (CA, appliance certificates, assignment signing keys, registry), **Audit log** (platform-wide, filterable), **Team** (Central operators), **Backup health**. |

Every route not in this table is gone. Old addresses (`/dashboard`, `/tenants`, `/sites`, `/onboarding`,
`/operators`, `/security`, `/certificates`, `/assignment-keys`, `/backup-health`, `/audit`, `/commercial`,
`/subscription`) answer with a permanent redirect to their replacement.

A customer-scoped user (a `tenant_*` role) never sees the fleet: `/overview`, `/appliances` and `/licenses` show only
their customer, and `/customers` goes straight to their own customer.

## 3. Appliance states (single source of truth, computed by ctrlapi)

The console never derives a state itself; ctrlapi returns these three fields on every appliance.

**`activation`**

| Value | Meaning |
|---|---|
| `waiting` | The appliance registered itself with Central and is waiting for an operator to activate it. |
| `activating` | Activated by an operator; the appliance has not yet collected its certificate or licence. Normally clears within a minute of the appliance's next contact. |
| `activated` | Assigned to a customer and site, certificate issued, current licence installed-able. |
| `retiring` | Retirement signed; waiting for the appliance to confirm (two-phase terminal delivery). Its credentials stay valid until it confirms, so it can collect the retirement. The detail's `retirement.state` is `terminal_delivery_pending`, or `terminal_delivery_failed` when it did not confirm within 10 minutes (overview attention `retirement_unconfirmed`, plus a security alert). |
| `retired` | Decommissioned (normal retirement, confirmed by the appliance) or revoked (emergency). Its credentials are dead. It can only be deleted, and its identity key can never register again. |

**`connection`** — from `last_seen_at` (updated by every authenticated appliance call; a healthy appliance calls every
30 s): `connected` (≤ 5 min), `recently_seen` (≤ 24 h), `offline` (> 24 h), `never`.

**`license`** — from the appliance's latest licence: `none`, `active`, `expiring` (≤ 30 days left), `grace` (past
`valid_until`, inside `grace_period_days`), `expired`, `suspended`, `revoked`.

The stored lifecycle is reduced to identity only: `pending_approval`, `assigned`, `revoked`, `decommissioned`
(migration 0046). Licence state is **never** written into the appliance lifecycle: suspending or revoking a licence
does not cut the appliance off from Central, so it can still fetch the signed suspension and the revocation list.

## 4. Lifecycle

```
power on ──► appliance registers itself (signed, hardware-bound, no token) ──► WAITING
                                                    │  operator: Activate (customer, site, licence terms)
                                                    ▼
      Central signs the assignment, issues the licence ──► ACTIVATING
                                                    │  appliance collects assignment → certificate → licence
                                                    ▼
                                                 ACTIVATED ◄── renew / change terms / suspend / resume
                                                    │  operator: Retire                (licence operations)
                                                    ▼
                          signed terminal assignment ──► RETIRING ──ack──► RETIRED ──► Delete (record)
```

* **Registration** is automatic and token-less: the appliance signs its registration with its hardware-bound identity
  key and retries until Central answers. Enrollment tokens no longer exist.
* **Activate** (one step, `waiting` only) picks the customer and site (either may be created inline) and the licence
  terms, signs the assignment and issues the licence in one transaction. The certificate is auto-issued when the
  appliance's CSR arrives.
* **Offline activation** is the same step with a file in each direction: import the appliance's signed activation
  request, then download the activation package (signed assignment + CA + licence) and upload it in Hotel Admin.
* **Licence operations** (per appliance): *Set licence* (issue, renew or change terms — always a new signed version
  that supersedes the current one), *Suspend*, *Resume*, *Revoke*, *Download offline licence*.
* **Move** re-assigns an activated appliance to another site **of the same customer**. The licence follows it: it is
  re-issued with exactly the same terms, bound to the new site, as the next version, in the same transaction as the
  new signed assignment. The move fails closed: if licensing is unavailable on Central or the licence cannot be read
  it is refused (503) and nothing changes; a licence already past its end date must be renewed first (409). A move to
  another customer is refused (409): the appliance's local data cannot be guaranteed to be cleared in place.
* **Changing customer** is: Retire → factory-reset the box (it gets a new identity key) → it registers again and
  waits → Activate for the new customer.
* **Retire** is two-phase: the licence is revoked at once; a signed terminal (decommissioned) assignment is issued;
  the appliance collects it, stands down and sends a signed acknowledgement; only then are its credentials revoked
  and it becomes `retired`. An **emergency** retire (also "retire now" while already retiring) revokes the
  credentials immediately and marks the identity revoked without waiting.
* **Replace hardware** marks an appliance for replacement; it keeps working and licensed. Activating the new
  appliance at the same site revokes the old appliance's licence and retires it through the same acknowledged
  two-phase retirement: its credentials stay valid until it confirms (or until an emergency retire).
* **Advanced** (appliance page, collapsed): reissue certificate, rebind WAN MAC after a NIC change (keeps the licence
  terms), mark for replacement.
* **Delete** removes a `waiting` or `retired` appliance's record (typed serial). Audit history is kept; its open
  security alerts are closed. A retired appliance's identity key is remembered: that key can never register or be
  imported again (`403`/`409 identity_retired`) — the box must be factory-reset first.
* **What the appliance does when its record is deleted.** Its next `hello` tells it Central no longer knows it.
  An appliance that **never held a customer** (a deleted `waiting` record) clears its identity and registers again
  as `waiting`. An appliance that **has held a customer** (any granting or terminal assignment on disk, or tenant
  data in its database — for example a retired appliance whose record was then deleted) removes its licence and
  client certificate, keeps its identity and every local record, writes `/etc/stayconnect/removed-from-central.json`
  and **never registers again**, under any key, across restarts and reboots. It admits no new guests (guests already
  online are not disconnected), refuses licence and activation uploads (`409 removed_from_central`), and Hotel Admin
  shows *Removed from OneGate Central* (§8). The only way back is the factory-clean install
  ([DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md](DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md)) and a new activation;
  there is no remote wipe.

## 5. Security invariants (unchanged by this redesign)

Factory-clean appliances carry no tenant or site; the signed assignment is the only identity authority; the licence is
vendor-signed and bound to appliance id, serial, hardware fingerprint, identity key and WAN MAC; licence versions and
assignment versions are monotonic (anti-rollback on the appliance); appliance API calls are Ed25519-signed request
tokens with replay protection, and after activation mutual TLS; assignment signing uses its own key with a signed
key registry; the root CA key stays offline; every write is audited; licence and activation writes need a platform
role and a recent password re-entry (step-up), and so does every change to a Central sign-in (Team members and
customer users: create, role, status, password, delete); login and re-authentication are rate-limited per client
address. A retired appliance's identity key is recorded (`retired_appliance_identities`, migration 0047) and refused
on registration and offline import. The appliance is local-first: guest service never waits on Central, and
a Central outage is never a guest outage.

## 6. API contract

Operator console: `/cloud/v1/*` behind the `sc_session` cookie. Authentication: `/v1/auth/*`. Appliance: `/v1/appliance*`
(unchanged paths). Nothing else is mounted. Writes marked **SU** require step-up (`403 reauth_required` → re-enter
password → retry). `customer_id` is the old `tenant_id`. Errors are `{error, message, trace_id}`: `error` is the stable
code, `message` an operator sentence the console shows verbatim. Login, re-authentication and registration are
rate-limited per client address, which ctrlapi derives itself (the TCP peer, or the loopback proxy's `X-Real-IP`).

### Authentication
`POST /v1/auth/login` · `POST /v1/auth/logout` · `GET /v1/auth/whoami` → `{operator_id,email,display_name,roles[],is_super_admin,customer_id|null,customer_name|null,permissions[]}` · `POST /v1/auth/reauth`

### Overview
`GET /cloud/v1/overview` →
```json
{ "customers": 1, "sites": 2,
  "appliances": {"total":2,"waiting":1,"activating":0,"activated":1,"retiring":0,"retired":0,
                 "connected":1,"recently_seen":0,"offline":0,"never":1},
  "licenses":   {"active":1,"expiring":0,"grace":0,"expired":0,"suspended":0,"revoked":0,"none":1},
  "attention": [ {"kind":"waiting_activation|license_expiring|license_grace|license_expired|license_suspended|appliance_offline|security_alert|retirement_unconfirmed",
                  "appliance_id":"…","serial":"…","customer_id":"…","customer_name":"…","site_name":"…",
                  "detail":"…","since":"RFC3339"} ] }
```

### Customers and sites
`GET /cloud/v1/customers?status=active|archived|all&q=` → `{items:[{id,name,slug,status,created_at,sites,appliances,activated,licenses_active,attention}]}`
`POST /cloud/v1/customers {name, slug?}` · `GET /cloud/v1/customers/{id}` (same row) · `PATCH {name}` ·
`POST …/{id}/archive|restore` · `DELETE …/{id} {confirm,reason}` **SU** (refused while it has sites/appliances)
`GET /cloud/v1/customers/{id}/sites` → `{items:[{id,customer_id,code,name,timezone,country,status,appliances}]}` ·
`POST /cloud/v1/customers/{id}/sites {name,code?,timezone,country?}` · `PATCH /cloud/v1/sites/{id}` ·
`POST /cloud/v1/sites/{id}/archive|restore` · `DELETE /cloud/v1/sites/{id} {confirm,reason}` **SU**

### Appliances
Row shape used by every list:
```json
{ "id","serial","hostname","model","version",
  "customer_id","customer_name","site_id","site_name",
  "activation":"waiting|activating|activated|retiring|retired",
  "connection":"connected|recently_seen|offline|never","last_seen_at","last_public_ip",
  "license": {"id","state":"none|active|expiring|grace|expired|suspended|revoked","valid_until","grace_ends_at",
              "max_concurrent_online_guests","license_version"} ,
  "registered_at","activated_at","open_alerts" }
```
`GET /cloud/v1/appliances?customer_id=&site_id=&activation=&connection=&license=&q=` → `{items:[row]}`
`GET /cloud/v1/appliances/{id}` → `row` + `{identity:{wan_mac,lan_mac,hardware_fingerprint,identity_key_fingerprint,cert_fingerprint,cert_not_after},
assignment:{version,state,signer_key_id,issued_at,acked_version},licenses:[history newest first],events:[recent lifecycle/audit],
replacement:{pending,deadline,replaces,replaced_by}|null,retirement:{state,deadline}|null}` — `replaces`/`replaced_by` are
appliance ids; `retirement.state` is `terminal_delivery_pending|terminal_delivery_failed|credential_revoked`
`POST /cloud/v1/appliances/{id}/activate {customer_id|new_customer{name}, site_id|new_site{name,timezone,country?},
license{max_concurrent_online_guests,valid_until|valid_days,grace_period_days}}` **SU** — `waiting` only
`POST /cloud/v1/appliances/{id}/move {customer_id, site_id, reason}` **SU** — `customer_id` must be the current customer
(`409 cross_customer_move` otherwise); `503 licensing_unavailable` when the licence cannot follow; `409 license_expired`
`POST /cloud/v1/appliances/{id}/retire {reason, emergency?:bool, confirm_serial?}` **SU** — `activated`, or `retiring`
with `emergency:true` (retire now without waiting); emergency needs `confirm_serial`
`POST /cloud/v1/appliances/{id}/replace {reason}` **SU** · `POST …/{id}/rebind-wan-mac {reason}` **SU** ·
`POST …/{id}/reissue-certificate {reason}` **SU**
`DELETE /cloud/v1/appliances/{id} {confirm_serial, reason}` **SU** — `waiting` or `retired` only
`POST /cloud/v1/offline-activation/requests` (activation-request file) → `row`
`POST /cloud/v1/appliances/{id}/offline-activation-package {valid_hours?}` **SU** → file

### Licences
`GET /cloud/v1/licenses?customer_id=&state=&q=` → `{items:[{id,appliance_id,serial,customer_id,customer_name,site_id,site_name,
state,status,valid_from,valid_until,grace_period_days,grace_ends_at,max_concurrent_online_guests,license_version,issued_at}]}`
`POST /cloud/v1/appliances/{id}/license {max_concurrent_online_guests, valid_until|valid_days, grace_period_days, reason}` **SU** — issue, renew or change terms
`POST /cloud/v1/licenses/{id}/suspend|resume|revoke {reason}` **SU**
`POST /cloud/v1/appliances/{id}/offline-license {valid_hours?}` **SU** → file

### System
`GET /cloud/v1/security-alerts?status=&appliance_id=` · `PATCH /cloud/v1/security-alerts/{id} {status,reason}`
`GET /cloud/v1/trust` → `{ca:[…],certificates:{active,revoked,expiring_30d,items:[…]},assignment_keys:[…],registry:{version,issued_at}}`
`GET /cloud/v1/audit?customer_id=&appliance_id=&action=&since=&limit=&cursor=`
`GET|POST /cloud/v1/team` · `PATCH|DELETE /cloud/v1/team/{id}` · `POST /cloud/v1/team/{id}/password` (Central operators) —
customer users: `GET|POST /cloud/v1/customers/{id}/users`, `PATCH|DELETE /cloud/v1/customers/{id}/users/{uid}`. Every write
to a sign-in (create, role, status, password, delete) is **SU**
`GET /cloud/v1/backup-health`

### Appliance (unchanged paths)
`POST /v1/appliances/register` · `GET /v1/appliance/hello|license|certificate` · `POST /v1/appliance/csr|offline-reconcile` ·
`GET /v1/appliance/assignment|assignment-registry` · `POST /v1/appliance/assignment/ack`. Registration with a retired
identity key answers `403 identity_retired`.

## 7. Roles

| Role | Can |
|---|---|
| `platform_owner`, `platform_admin` | everything |
| `platform_support` | read everything |
| `tenant_admin`, `tenant_owner` | read their customer; manage its sites and users |
| `tenant_auditor`, `viewer` | read their customer |

Licence and activation writes are platform-only. Legacy roles (`platform_billing`, `billing`, `tenant_operator`,
`site_admin`, `hotel_it`, `hotel_operator`) grant nothing in Central.

## 8. Hotel Admin ↔ Central

Hotel Admin shows one card on **Appliance & licence** (`/appliance`), fed by one appliance endpoint
`GET /edge/v1/central/status`:

```json
{ "activation": "not_registered|waiting|activating|activated|retired",
  "serial":"…","appliance_id":"…|null","customer_name":"…|null","site_name":"…|null",
  "license": {"state":"none|active|expiring|grace|expired|suspended|revoked|wrong_hardware",
              "valid_until","grace_ends_at","days_left","max_concurrent_online_guests","current_online_guests"},
  "central": {"state":"connected|unreachable|not_configured","last_contact_at","last_error"},
  "details": { "identity_key_fingerprint","cert_fingerprint","cert_not_after","assignment_version","license_version",
               "wan_mac","lan_mac","central_endpoint","assignment_status","license_id?","software_version?",
               "build_profile?","permissive_blocked?","reason?" } }
```

`license.hardware_notice` is present when the WAN adapter differs from the one the licence names (the licence stays in
force; *Rebind WAN MAC* issues a corrected one). `details.reason` qualifies a `retired` activation: the value
`removed_from_central` means Central deleted this appliance after it had held a customer (§4); Hotel Admin then shows
*Removed from OneGate Central — factory-reset it and have your vendor activate it* instead of *Retired*, and the
licence and offline-activation uploads answer `409 removed_from_central`.

Actions: **Check now** (`POST /edge/v1/central/refresh` — register if needed, fetch assignment and licence now),
**Offline activation** (`GET /edge/v1/central/offline-request`, `POST /edge/v1/central/offline-package`), **Upload
licence file** (`POST /edge/v1/license`). Protocol details (fingerprints, versions, endpoint) sit in a collapsed
*Technical details* section.

The appliance registers itself at boot and keeps retrying until Central answers; after activation it collects the
assignment (every 30 s), the certificate and the licence (immediately after activation, then every 6 h, and on *Check
now*). Losing Central changes only `central.state` to `unreachable`; the licence keeps being evaluated locally and
guests are unaffected. The terminal-assignment acknowledgement and the offline-package reconciliation are retried until
Central confirms them; neither is on a guest path.

**Every Guest Access method answers to the same licence.** Voucher, guest account, OTP, social and PMS room sign-in
are all refused while the licence does not admit new guests (no licence, expired past grace, suspended, revoked, wrong
hardware, the feature not licensed, a tenant transition pending, or removed from Central), and every new guest
session — including a room guest's second device joining the stay — takes a slot from the same atomic
`max_concurrent_online_guests` reservation. Room sign-in records these refusals as the attempt results
`LICENSE_REFUSED` and `LICENSE_CAPACITY_REACHED` (appliance migration 0092) in **Guest Sign-in Attempts**. A device
that signs in again while it still has an open session on the stay keeps that session and takes no new slot.
