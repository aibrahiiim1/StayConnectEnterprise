# StayConnect Internet Access Management — Phase 0 Contract

**Status: FINAL — Phase 0 CLOSED.** *(2026-07-16 — approved by the Product Owner; previously READY_FOR_FINAL_OWNER_APPROVAL, and before that CONDITIONALLY FROZEN.)*

| Finalization record | Value |
|---|---|
| Approval | **Explicit Product-Owner FINAL approval** |
| Approving role | **Product Owner** |
| Approval date | **2026-07-16** |
| Current synchronized documentation baseline | **`79bf3e8`** |
| Historical Phase-0 finalization provenance | contract `ffe2200`, synchronized handoff `6b4721d` (retained as the historical FINAL-approval baseline only) |
| FINAL documentation commit | *established by the commit that carries this status change* |
| Phase 0 | **CLOSED** |
| Next authorized activity | **Product-Owner review and explicit approval or rejection of the Phase 1A implementation plan** (see [StayConnect-IAM-Phase1A-Plan.md](StayConnect-IAM-Phase1A-Plan.md), status `READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL`). Phase 1A is **NOT started**; no implementation is currently authorized. Plan approval authorizes **scratch/test implementation only**; live-database `iam_v2` creation and cutover need later, separate approvals |

The Phase-0 **protocol and architecture** validation is complete on already-measured live evidence (see §9b/§9c and `docs/spikes/Protel-FIAS-Phase0-Spike.md`) and is now **FINAL**. The approved architecture, DDL, invariants, limitations, measured FIAS findings, and acceptance requirements below are unchanged by this finalization — only the status advanced.

Phase-0 finalization deliberately separates three distinct validation tiers (§9c):

1. **Phase-0 protocol & architecture validation** — the finalization basis; measurable now and measured (framing, LS/LD/LR/LA startup, GI/GC/GO feed, RN+G# folio targeting, PS/PA field order and AS statuses, one live end-to-end Hotel ID 3 debit with verified folio/mapping/cleanup, single-client slot, P#-not-idempotency, transmitted-without-PA risk, reversal-unsupported-in-v1, independent interface namespaces).
2. **Per-property deployment validation** — a **per-property financial-onboarding checklist** (currency/exponent, package-currency compatibility, `SO=WIFI` mapping, RN+G# targeting, one controlled debit, folio placement, approved cleanup). Aqua Club / Hotel ID 2 lives here — it is a deployment prerequisite, **not** a Phase-0 finalization blocker.
3. **Post-implementation acceptance testing** — behaviors that **cannot** be measured before their StayConnect components exist (UNKNOWN/Manual-Review posting-engine safety; Checkout & Checkout-Grace on the PMS/Entitlement phase). Preserved as **binding acceptance requirements**, not Phase-0 blockers.

**Phase 0 is FINAL and CLOSED. The next authorized activity is Phase 1A *planning only*.** No feature implementation, schema migration, portal/UI change, PMS production configuration change, cutover, or deployment is authorized until the **Phase 1A plan is separately approved** by the product owner. FINAL status closes Phase-0 architecture; it does **not** by itself unlock implementation.

**Scope:** the Internet Access Management domain on the Hotel Appliance (site DB; scd/edged/portald/acctd services). Explicitly out of scope and untouched: appliance enrollment, hardware-bound identity, PKI/mTLS, signed licensing, Central Control Plane boundaries, WAN/LAN configuration, guest VLANs, DHCP/DNS, captive-portal network interception, nftables/traffic-control foundations, updates, remote support, and audit infrastructure.

---

## 1. Glossary

| Term | Definition |
|---|---|
| **Service Plan / Plan Revision** | Technical policy template (speed, devices, timeouts, quota semantics). Plans are edited only by creating immutable revisions. Never commercial. |
| **Internet Package / Package Revision** | Commercial offer pinned to one plan revision: price, settlement behavior, eligibility, display. Guests always acquire a specific package revision. |
| **Credential** | Proof of authentication: voucher code, username/password, PMS stay verification, OTP, social login, post-stay PIN. Never a container for quota, price, or usage. |
| **Guest Principal** | Stable **tenant-wide** identity for OTP/social guests, keyed by verified identity factors (email, phone, issuer-scoped social subject). Never a MAC. |
| **Auth Context** | One-time, short-lived, server-side record of a successful authentication and its pins. Not an Internet session. |
| **Offer Quote** | One-time, short-lived server-side snapshot of the exact package revision, price, tax, settlement mapping, and grants shown to the guest; consumable by exactly one Purchase. |
| **Purchase** | Durable acquisition of a package revision (including zero-cost acquisitions); the idempotency root of all commerce. |
| **Settlement** | How a Purchase is settled: not required, prepaid, PMS posting, online payment, or manual approval. |
| **PMS Posting** | Append-only ledger record of a charge command against a PMS folio. |
| **Payment Transaction** | Typed append-only online-payment ledger row (CHARGE / REFUND / CHARGEBACK), scoped by tenant, provider, and merchant account. |
| **Entitlement** | The enforceable right to use the Internet: immutable policy snapshot, immutable end policy, monotonic usage counters, terminal state. Exactly one live entitlement per subject. |
| **Checkout Grace** | Mandatory site-level mechanism: an eligible checked-out guest is atomically superseded onto one hidden grace package with no interruption and no re-authentication. |
| **Stay / Lifecycle episode** | Durable PMS-derived occupancy record in one PMS-interface namespace; each IN_HOUSE→CHECKED_OUT cycle is one episode (`lifecycle_version`). |
| **PMS Interface** | One configured PMS connection: immutable id, immutable configuration revisions, generational secrets, measured capabilities, and its own room/reservation/folio namespace. A site may run several concurrently. |
| **Device** | Observed network identity `(tenant, site, appliance, MAC)`. MAC addresses identify devices only — never people. |
| **Session** | One runtime authorization of a Device under an Entitlement (nftables + traffic-control + accounting). |

## 2. Product Invariants

1. Guests supply normal hotel credentials only. StayConnect resolves exactly one PMS Interface and Stay in the backend. **There is no guest-facing PMS/property selector**; connector names are never shown to guests.
2. Every Room, Stay, Guest, Folio, Event, Purchase, and Posting lives in exactly one PMS-interface namespace. Identical room numbers across interfaces never collide. **Room Number is evidence, never identity or financial ownership.**
3. One authorization pipeline for all methods. Logically, signed-license capacity is the outermost gate; physically, all gates re-verify inside one transaction under the global lock order, and a capacity failure rolls back everything — no session, device, or binding row survives a failed authorization.
4. **One live data-plane entitlement per subject.** Access changes are atomic supersessions that rebind sessions without nft interruption. Supersession never changes the subject; cross-PMS movement uses an explicit typed transfer relationship.
5. Entitlement policy = immutable snapshot (plan revision + package revision overrides) + immutable end policy. Later edits to plans, packages, or mappings never affect existing entitlements; corrections are new entitlements or audited adjustments.
6. Time quota is a durable wall-clock validity window (device count, reconnects, crashes, restarts, reboots never move it). Data quota is an aggregate across all devices. First reached limit terminates atomically exactly once. Consumed usage is monotonic; late accounting is audited and never reopens access.
7. Every acquisition — including free and grace — creates a Purchase. Guest-selected purchases consume an Offer Quote and its Auth Context **atomically with Purchase creation**, and the Purchase cannot differ from the quote in any pinned dimension (DB + trigger enforced, null-safe). Renewal/upgrade creates a new Purchase and Entitlement.
8. Financial commands pin at the DB layer: PMS interface, interface revisions (authentication and posting separately), secret generation, package revision, settlement-mapping row, Stay, Folio, and the exact Settlement/Purchase pair. Retries never re-resolve anything. Cross-tenant/site/interface/revision references are unrepresentable.
9. `posting_allowed = true` requires `IN_HOUSE` (DB CHECK), but IN_HOUSE grants nothing by itself: posting permission is evaluated from PMS flags, open folios, credit policy, and administrative blocks, with recorded reason, source, and check timestamp. Checkout ends posting for the episode irreversibly (except trusted Reinstatement, which re-evaluates).
10. **Seamless Checkout Grace is mandatory and site-level:** at checkout, an eligible guest (live entitlement or recent authorization) is superseded onto the hidden grace package regardless of whether their access was free, paid, or prepaid; sessions rebind with zero nft churn and no re-authentication; devices above the grace limit are grandfathered; no future room posting can occur. A corrupt grace configuration triggers the durable emergency-grace fallback — never an outage, never a silent skip.
11. Multi-PMS resolution is **STRICT-only**, fail-closed on unmapped guest networks, complete-vector, uniform-response, and returns a one-time **Auth Context — never a session. Sessions are created only after Entitlement grant.**
12. A restored system never auto-replays financial commands. **Exactly-once FIAS posting is guaranteed only under supported, manifest-signed restore workflows**; this limitation is part of the operational and support contract (§14), not only of the architecture.
13. The appliance site DB is authoritative for this entire domain; PMS authentication and posting work with Central unavailable. Central receives telemetry and read-only visibility in v1.
14. All domain tables participate in tenant isolation and the secure cross-customer transition; financial records are compliance-archived with verified receipt **before** purge; tenant keys are crypto-shredded.
15. Guest passwords/PINs exist only as Argon2id hashes. Voucher codes are stored HMAC-indexed + AEAD-encrypted (recoverable value + last4). PMS secrets are generational AEAD ciphertext. None ever appear in logs, telemetry, exports, or audit payloads.
16. License capacity counts **distinct active guest devices per appliance**, not raw session rows.
17. **No feature implementation begins until the Protel spike artifact is merged and the contract is approved as FINAL.**

## 3. Domain Model / ERD

```
Customer ─ Site ─ Appliance ── signed-license capacity (distinct active devices)

pms_interfaces ─< pms_interface_revisions (immutable; timezone; evidence rules;
      │             folio identity strategy; MEASURED capabilities)
      │        ─< pms_interface_secret_generations (AEAD, generational)
      │        ─< guest_network_pms_map >─ guest_networks      (fail-closed routing)
      ├──< stays ──< stay_guests (one primary)  ──< stay_events
      │      ├──< stay_folios >── folios (identity-strategy aware)
      │      └──< stay_links · entitlement_transfers (typed, cycle-safe cross-PMS lineage)
      └── pms_source_conflicts

service_plans ─< service_plan_revisions (immutable)
internet_packages ─< internet_package_revisions ── pins one plan revision
       │                 ├──< package_eligibility_rules   ├──< package_grant_tiers
       │                 └──< package_settlement_mappings (append-only chains)
site_checkout_grace_config ── hidden CHECKOUT_GRACE package (validated; emergency fallback)

auth_contexts (one-time; method↔subject coherence) → offer_quotes (one-time; pin tuple)
      ↓ atomic CAS (context + quote consumed with Purchase creation)
purchases ── settlements ──┬── pms_postings ── posting_outbox (one active row per posting)
    │                      └── payment_transactions (typed; merchant-account scoped)
    └─→ entitlements ── snapshot + end policy + counters
             ├─ subject: exactly one of {stay, account, voucher, guest_principal}
             ├─ supersedes_entitlement_id (same subject only)
             ├──< entitlement_devices >── devices ─< device_network_appearances
             ├──< sessions ─< accounting_records + session_counter_watermarks
             └──< entitlement_adjustments (audited)

guest_principals (tenant-wide) ─< guest_principal_identities (issuer-scoped verified factors)
guest_access_accounts · vouchers (HMAC+AEAD, key generations) >─ batches ── package REVISION
post_stay_profiles · financial_epoch · compliance_archives · posting_review_actions · auth_resolutions
```

## 4. Canonical DDL and Constraints

Conventions: `tenant_id uuid NOT NULL` on every table; `site_id uuid NOT NULL` on every site-operational table (guest principals are deliberately tenant-wide). Parents expose namespace-carrying `UNIQUE` anchors; children reference the full tuple with composite foreign keys. `guest_networks` (existing platform table) gains the supporting anchor `UNIQUE (tenant_id, site_id, id)`. Normative core:

### 4.1 PMS interfaces, revisions, secrets, routing

```sql
CREATE TABLE pms_interfaces (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  connector_kind text NOT NULL, display_label text,          -- admin/receipt only, never guest-facing
  lifecycle_state text NOT NULL DEFAULT 'ACTIVE'
    CHECK (lifecycle_state IN ('ACTIVE','AUTH_DISABLED','DRAINING','DECOMMISSIONED')),
  current_revision_id uuid,
  UNIQUE (tenant_id, site_id, id));

CREATE TABLE pms_interface_revisions (                        -- append-only (trigger-enforced)
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  revision_no int NOT NULL,
  source_timezone text NOT NULL,                              -- the ONE timezone source for this connector
  folio_identity_strategy text NOT NULL DEFAULT 'UNSET'          -- FAIL-CLOSED: 'UNSET' blocks every
    CHECK (folio_identity_strategy IN (                          -- financial CHARGE/Posting until property
      'UNSET',                                                    -- onboarding records one concrete strategy.
      'GLOBALLY_UNIQUE','UNIQUE_PER_STAY','REUSED_SEQUENTIAL')),  -- Read-only ingestion/lookup/auth allowed.
    -- Setting a concrete strategy creates a NEW immutable interface revision; it never mutates this one.
    -- ('UNSET' is the ONLY unset sentinel here — 'UNKNOWN' is reserved as a financial Posting state, not a folio strategy.)
  config jsonb NOT NULL,   -- field maps, normalization, MEASURED capability matrix,
                           -- auth.verifier_combinations (connector-specific evidence classes and
                           -- uniqueness flags), freshness bounds, requires_live_lookup_for_financial
  normalization_version int NOT NULL DEFAULT 1, source_fingerprint text,
  UNIQUE (pms_interface_id, revision_no),
  UNIQUE (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES pms_interfaces (tenant_id, site_id, id));
ALTER TABLE pms_interfaces ADD FOREIGN KEY (tenant_id, site_id, id, current_revision_id)
  REFERENCES pms_interface_revisions (tenant_id, site_id, pms_interface_id, id);

CREATE TABLE pms_interface_secret_generations (               -- append-only AEAD ciphertext
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  generation_no int NOT NULL,
  ciphertext bytea NOT NULL, nonce bytea NOT NULL,
  encryption_key_id uuid NOT NULL, cipher_version int NOT NULL, superseded_at timestamptz,
  UNIQUE (pms_interface_id, generation_no),
  UNIQUE (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES pms_interfaces (tenant_id, site_id, id));
-- DELETE rejected while any non-terminal financial command pins the generation.

CREATE TABLE guest_network_pms_map (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  guest_network_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  is_default boolean NOT NULL DEFAULT false,
  routing_mode text NOT NULL DEFAULT 'MAPPED' CHECK (routing_mode IN ('MAPPED','ALL_ACTIVE_INTERFACES')),
  PRIMARY KEY (guest_network_id, pms_interface_id),
  FOREIGN KEY (tenant_id, site_id, guest_network_id)
    REFERENCES guest_networks (tenant_id, site_id, id) ON DELETE CASCADE,   -- tenant+site composite ownership
  FOREIGN KEY (tenant_id, site_id, pms_interface_id)
    REFERENCES pms_interfaces (tenant_id, site_id, id));
CREATE UNIQUE INDEX gnpm_one_default ON guest_network_pms_map (guest_network_id) WHERE is_default;
-- No rows for a network ⇒ PMS auth unavailable there (FAIL CLOSED) + pms_config_missing alert.
-- Save-time validation: candidate count ≤ max_candidate_interfaces (default 3, hard cap 5);
-- all mapped interfaces must share ≥ 1 common determinate verifier combination.
```

> **NORMATIVE — `folio_identity_strategy` fail-closed amendment (Product-Owner APPROVED, 2026-07-16).** The column is `NOT NULL DEFAULT 'UNSET'` with the 4-value `CHECK` `('UNSET','GLOBALLY_UNIQUE','UNIQUE_PER_STAY','REUSED_SEQUENTIAL')`. Semantics:
> - **`UNSET` is the only unset sentinel** for this field. `GLOBALLY_UNIQUE` is **no longer** the default. (`UNKNOWN` is **not** used here — it is a financial Posting state, per §9a/§16.)
> - An interface revision with `folio_identity_strategy = 'UNSET'` **may be created and used for read-only PMS ingestion, guest lookup, and authentication.**
> - While the strategy is `UNSET`, **every financial CHARGE / Posting is rejected fail-closed** — the rejection happens **before** posting-outbox creation, `P#` allocation, or any PMS transmission (see §9a rule 6 and §16 PMS-Posting preconditions).
> - **Setting a concrete strategy** (`GLOBALLY_UNIQUE` / `UNIQUE_PER_STAY` / `REUSED_SEQUENTIAL`) is done by property financial-onboarding (§9c Tier 2) and **creates a new immutable interface revision** — it never mutates an existing revision. Postings pin the revision they were built against.

### 4.2 Stays, sharers, folios, events

```sql
CREATE TABLE stays (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  external_reservation_id text NOT NULL, external_stay_identity text NOT NULL,
  normalized_room_number text,                                -- lookup attribute ONLY
  status text NOT NULL CHECK (status IN
    ('RESERVED','IN_HOUSE','CHECKED_OUT','POST_STAY_ACTIVE','CANCELLED','NO_SHOW')),
  lifecycle_version int NOT NULL DEFAULT 1,                   -- ++ on Reinstatement
  posting_allowed boolean NOT NULL DEFAULT false,
  posting_block_reason text, posting_permission_source text, posting_checked_at timestamptz,
  last_applied_event_version bigint NOT NULL DEFAULT 0,
  vip boolean, travel_agent text, room_type text, arrival date, departure date,
  UNIQUE (tenant_id, site_id, pms_interface_id, external_reservation_id, external_stay_identity),
  UNIQUE (tenant_id, site_id, pms_interface_id, id),
  UNIQUE (tenant_id, site_id, id),
  CONSTRAINT posting_only_in_house CHECK (posting_allowed = false OR status = 'IN_HOUSE'),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES pms_interfaces (tenant_id, site_id, id));
CREATE INDEX stays_room_lookup ON stays
  (tenant_id, site_id, pms_interface_id, normalized_room_number) WHERE status='IN_HOUSE';
-- Deliberately NO room-occupancy uniqueness: sharers are legal.

CREATE TABLE stay_guests (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL, stay_id uuid NOT NULL,
  external_guest_id text, first_name_norm text, last_name_norm text, display_name text,
  is_primary boolean NOT NULL DEFAULT false, date_of_birth date, pin_hash text,   -- argon2id
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id)
    REFERENCES stays (tenant_id, site_id, pms_interface_id, id) ON DELETE CASCADE);
CREATE UNIQUE INDEX one_primary_guest_per_stay ON stay_guests (stay_id) WHERE is_primary;
-- Primary-change events demote the old primary in the same transaction. A duplicate event
-- re-asserting the same primary ⇒ SKIPPED_DUPLICATE; a conflicting primary with materially
-- different identity ⇒ MANUAL_REVIEW. Never two primaries; never silent replacement.

CREATE TABLE folios (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  external_folio_id text NOT NULL,
  identity_epoch int NOT NULL DEFAULT 1,      -- ++ when a REUSED_SEQUENTIAL connector recycles the number
  folio_kind text NOT NULL DEFAULT 'GUEST' CHECK (folio_kind IN ('GUEST','COMPANY','GROUP_MASTER','OTHER')),
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN','CLOSED')),
  UNIQUE (tenant_id, site_id, pms_interface_id, external_folio_id, identity_epoch),
  UNIQUE (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES pms_interfaces (tenant_id, site_id, id));
CREATE UNIQUE INDEX folio_open_identity ON folios (tenant_id, site_id, pms_interface_id, external_folio_id)
  WHERE status='OPEN';
-- Folio identity strategy (per interface revision): the default is 'UNSET' (fail-closed: no
-- financial CHARGE until onboarding sets a concrete strategy in a new revision — §4.1 amendment).
-- GLOBALLY_UNIQUE keeps epoch=1 forever;
-- UNIQUE_PER_STAY resolves through stay_folios; REUSED_SEQUENTIAL creates a new row with
-- identity_epoch+1 when a recycled number reappears — postings pin folio ROW ids, so
-- recycled numbers can never alias history.

-- stay_folios(tenant, site, pms_interface_id, stay_id, folio_id, is_default_posting_target)
--   PK(stay_id, folio_id); composite FKs to stays and folios;
--   UNIQUE(stay_id) WHERE is_default_posting_target.
-- stay_events(id, tenant, site, pms_interface_id, stay_id NULL, external_event_identity,
--   event_type, pms_timestamp_raw, pms_timestamp_utc, source_timezone, received_at,
--   sequence_version, normalization_version, clock_suspect, payload jsonb (redacted at write),
--   processing_status PENDING|APPLIED|SKIPPED_DUPLICATE|MANUAL_REVIEW|FAILED)
--   UNIQUE(tenant, site, pms_interface_id, external_event_identity);
--   FK (tenant, site, pms_interface_id, stay_id) → stays.
-- stay_links(tenant, site, from_stay, to_stay, reason CROSS_PMS_TRANSFER|POST_STAY)
--   composite FKs both ends → stays(tenant, site, id); UNIQUE(from_stay, to_stay, reason).
```

### 4.3 Plans, packages, mappings, grace config

```sql
-- service_plans(id, tenant, site, code, current_revision_id, enabled)
--   UNIQUE(tenant,site,code), UNIQUE(tenant,site,id);
--   current_revision FK (tenant,site,id,current_revision_id)
--     → service_plan_revisions(tenant,site,service_plan_id,id).
-- service_plan_revisions(id, tenant, site, service_plan_id, revision_no, name,
--   down_kbps, up_kbps, max_concurrent_devices >= 1,
--   device_limit_policy REJECT_NEW_DEVICE|DISCONNECT_OLDEST|ADMIN_APPROVAL,
--   idle_timeout_seconds, max_continuous_session_seconds NULL (disabled by default),
--   time_accounting_mode VALIDITY_WINDOW|AGGREGATE_ONLINE_TIME (v1 implements WINDOW only),
--   time_quota_seconds, data_quota_bytes)
--   UNIQUE(service_plan_id, revision_no), UNIQUE(tenant,site,id); append-only.

-- internet_packages(id, tenant, site, code, active, is_system, current_revision_id,
--   central_template_id NULL) UNIQUE(tenant,site,code), UNIQUE(tenant,site,id);
--   current_revision FK pattern as above.
-- internet_package_revisions(id, tenant, site, package_id, revision_no,
--   service_plan_revision_id → service_plan_revisions(tenant,site,id),
--   package_type FREE_STAY|ONE_DAY|REST_OF_STAY|POST_STAY|GENERAL|CHECKOUT_GRACE,
--   price_minor bigint >= 0, currency char(3), currency_exponent smallint,   -- ISO-4217 minor units
--   settlement_methods text[] of NOT_REQUIRED|PREPAID|PMS_POSTING|ONLINE_PAYMENT|MANUAL_APPROVAL,
--   duration_policy jsonb {end_mode, duration_value, local_time_boundary…},
--   plan_overrides jsonb, renewable, max_purchases_per_stay,
--   display jsonb, visible_from, visible_until)
--   UNIQUE(package_id, revision_no), UNIQUE(tenant,site,id); append-only.
-- package_eligibility_rules / package_grant_tiers: typed constrained rules (no expressions,
--   no scripts) and ordered first-match grant tiers per package revision; composite FKs; CASCADE.

CREATE TABLE package_settlement_mappings (                    -- append-only linear chains
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  package_revision_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  mapping_revision int NOT NULL,
  posting_code text NOT NULL, tax_code text, tax_rate_bp int,
  retired_at timestamptz, replaces_mapping_id uuid,
  UNIQUE (package_revision_id, pms_interface_id, mapping_revision),
  UNIQUE (tenant_id, site_id, package_revision_id, pms_interface_id, id),   -- pin anchor
  UNIQUE (id, package_revision_id, pms_interface_id),
  UNIQUE (replaces_mapping_id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id)
    REFERENCES internet_package_revisions (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES pms_interfaces (tenant_id, site_id, id),
  FOREIGN KEY (replaces_mapping_id, package_revision_id, pms_interface_id)
    REFERENCES package_settlement_mappings (id, package_revision_id, pms_interface_id));
CREATE UNIQUE INDEX psm_active ON package_settlement_mappings
  (package_revision_id, pms_interface_id) WHERE retired_at IS NULL;
-- Trigger: replacement requires mapping_revision = replaced.mapping_revision + 1
-- (monotonic ⇒ acyclic); UPDATE permitted only to set retired_at;
-- retire-and-create is one transaction under lock class L5 (§6.5).

CREATE TABLE site_checkout_grace_config (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL PRIMARY KEY,
  grace_package_id uuid NOT NULL,
  eligibility_window_seconds int NOT NULL DEFAULT 86400,
  FOREIGN KEY (tenant_id, site_id, grace_package_id) REFERENCES internet_packages (tenant_id, site_id, id));
-- Validated at save AND re-validated at every checkout (§7.1): package must be active,
-- is_system = true, package_type = 'CHECKOUT_GRACE', price_minor = 0,
-- settlement_methods = {NOT_REQUIRED}, with a valid current revision pinning an enabled
-- plan revision. Corruption discovered at checkout ⇒ EMERGENCY GRACE FALLBACK (§7.1).
```

### 4.4 Guest identities and credentials

```sql
CREATE TABLE guest_principals (               -- TENANT-WIDE (no site_id)
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, display_name text,
  UNIQUE (tenant_id, id));

CREATE TABLE guest_principal_identities (     -- verified factors; MACs are NEVER factors
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, guest_principal_id uuid NOT NULL,
  factor_type text NOT NULL CHECK (factor_type IN ('EMAIL','PHONE','SOCIAL_SUBJECT')),
  factor_issuer text NOT NULL DEFAULT '',     -- REQUIRED for SOCIAL_SUBJECT: 'google', 'apple', …
  factor_value_norm text NOT NULL, verified_at timestamptz NOT NULL,
  CONSTRAINT gpi_social_needs_issuer CHECK (factor_type <> 'SOCIAL_SUBJECT' OR factor_issuer <> ''),
  UNIQUE (tenant_id, factor_type, factor_issuer, factor_value_norm),   -- issuer-scoped uniqueness
  FOREIGN KEY (tenant_id, guest_principal_id) REFERENCES guest_principals (tenant_id, id) ON DELETE CASCADE);

-- guest_access_accounts(id, tenant, site, username, password_hash argon2id, display_name,
--   notes, enabled, valid_from/until, assigned_package_id NULL, stay_id NULL,
--   failed_attempts, locked_until, last_login_at, login_count)
--   UNIQUE(tenant, lower(username)); UNIQUE(tenant, site, id).
--   Assigned package FOLLOWS CURRENT: the package's current revision is resolved at each
--   grant; the resulting entitlement pins that revision immutably.

CREATE TABLE voucher_code_key_generations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, generation_no int NOT NULL,
  hmac_key_ciphertext bytea NOT NULL, aead_params jsonb NOT NULL,
  encryption_key_id uuid NOT NULL, superseded_at timestamptz,
  UNIQUE (tenant_id, generation_no), UNIQUE (tenant_id, site_id, id));

CREATE TABLE vouchers (                       -- credential + redemption policy ONLY
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  batch_id uuid, package_revision_id uuid NOT NULL,
  code_hmac bytea NOT NULL,                   -- HMAC-SHA256(normalized code, tenant key generation)
  code_ciphertext bytea NOT NULL, code_nonce bytea NOT NULL,   -- AEAD-recoverable for reveal/print
  code_key_generation_id uuid NOT NULL, code_last4 text NOT NULL,
  state text NOT NULL DEFAULT 'UNUSED' CHECK (state IN ('UNUSED','REDEEMED','REVOKED','REDEMPTION_EXPIRED')),
  redemption_valid_from timestamptz, redemption_valid_until timestamptz, notes text,
  UNIQUE (code_hmac), UNIQUE (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id)
    REFERENCES internet_package_revisions (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, code_key_generation_id)
    REFERENCES voucher_code_key_generations (tenant_id, site_id, id));
-- Batches pin package_revision_id identically. Reveal/print/export require operator
-- re-authentication + audit; CSV export shows code_last4 by default; full-code export is a
-- distinct audited action with formula-injection guarding (leading = + - @ escaped).
-- Single-redemption only; reusable multi-grant codes are a separate future feature.
```

### 4.5 Auth contexts, quotes, purchases, settlements, postings, payments

```sql
CREATE TABLE auth_contexts (                  -- one-time, TTL 10 min
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  method text NOT NULL CHECK (method IN ('PMS','VOUCHER','ACCOUNT','OTP','SOCIAL','POST_STAY_PIN')),
  stay_id uuid, guest_account_id uuid, voucher_id uuid, guest_principal_id uuid,
  post_stay_profile_id uuid,
  pms_interface_id uuid, authentication_interface_revision_id uuid,
  device_id uuid NOT NULL, guest_network_id uuid NOT NULL,
  expires_at timestamptz NOT NULL, consumed_at timestamptz,
  CONSTRAINT ac_one_subject CHECK (num_nonnulls(stay_id, guest_account_id, voucher_id,
                                                guest_principal_id, post_stay_profile_id) = 1),
  CONSTRAINT ac_method_subject CHECK (
      (method = 'PMS'             AND stay_id IS NOT NULL)
   OR (method = 'VOUCHER'         AND voucher_id IS NOT NULL)
   OR (method = 'ACCOUNT'         AND guest_account_id IS NOT NULL)
   OR (method IN ('OTP','SOCIAL') AND guest_principal_id IS NOT NULL)
   OR (method = 'POST_STAY_PIN'   AND post_stay_profile_id IS NOT NULL)),
  CONSTRAINT ac_pms_pins CHECK (method <> 'PMS'
      OR (pms_interface_id IS NOT NULL AND authentication_interface_revision_id IS NOT NULL)),
  UNIQUE (tenant_id, site_id, id),
  UNIQUE (id, pms_interface_id),              -- anchor for the quote pin tuple
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id)
    REFERENCES stays (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, guest_account_id) REFERENCES guest_access_accounts (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, voucher_id)       REFERENCES vouchers (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, guest_principal_id)        REFERENCES guest_principals (tenant_id, id),
  FOREIGN KEY (tenant_id, site_id, post_stay_profile_id) REFERENCES post_stay_profiles (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, device_id)        REFERENCES devices (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, guest_network_id) REFERENCES guest_networks (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, authentication_interface_revision_id)
    REFERENCES pms_interface_revisions (tenant_id, site_id, pms_interface_id, id));

CREATE TABLE offer_quotes (                   -- one-time, TTL 5 min
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  auth_context_id uuid NOT NULL,
  package_revision_id uuid NOT NULL,
  pms_interface_id uuid, settlement_mapping_id uuid,          -- resolved active mapping if PMS-settled
  price_minor bigint NOT NULL, currency char(3), currency_exponent smallint,
  tax_code text, tax_rate_bp int, tax_amount_minor bigint,    -- HALF-UP, computed exactly once here
  grant_snapshot jsonb NOT NULL,
  expires_at timestamptz NOT NULL, consumed_at timestamptz,
  UNIQUE (tenant_id, site_id, id),
  UNIQUE (id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id),
  FOREIGN KEY (tenant_id, site_id, auth_context_id) REFERENCES auth_contexts (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id)
    REFERENCES internet_package_revisions (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id, pms_interface_id, settlement_mapping_id)
    REFERENCES package_settlement_mappings (tenant_id, site_id, package_revision_id, pms_interface_id, id));

CREATE TABLE purchases (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  package_revision_id uuid NOT NULL,
  offer_quote_id uuid UNIQUE, auth_context_id uuid,
  pms_interface_id uuid, stay_id uuid, settlement_mapping_id uuid,
  authentication_interface_revision_id uuid,
  trigger text NOT NULL CHECK (trigger IN ('GUEST_SELECTION','VOUCHER_REDEMPTION','ACCOUNT_AUTO_GRANT',
    'OTP_SOCIAL_DEFAULT','CHECKOUT_GRACE','EMERGENCY_GRACE','POST_STAY_CONVERSION',
    'CROSS_PMS_TRANSFER','ADMIN_GRANT','RENEWAL')),
  amount_minor bigint NOT NULL DEFAULT 0 CHECK (amount_minor >= 0),
  currency char(3), currency_exponent smallint,
  tax_code text, tax_rate_bp int, tax_amount_minor bigint,
  state text NOT NULL DEFAULT 'PENDING' CHECK (state IN
    ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW','GRANTED','FAILED','CANCELLED')),
  purchase_seq int NOT NULL DEFAULT 1, checkout_episode int,
  UNIQUE (tenant_id, site_id, id), UNIQUE (id, pms_interface_id),
  CONSTRAINT purchase_guest_needs_quote CHECK (trigger <> 'GUEST_SELECTION' OR offer_quote_id IS NOT NULL),
  -- Quote exactness, NULL-SAFE, two layers:
  --  (a) composite FK for the non-null pin tuple:
  FOREIGN KEY (offer_quote_id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id)
    REFERENCES offer_quotes (id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id),
  --  (b) authoritative BEFORE INSERT trigger (because SQL MATCH SIMPLE skips NULL columns):
  --      when offer_quote_id IS NOT NULL it verifies, with IS NOT DISTINCT FROM semantics,
  --      that package_revision_id, auth_context_id, pms_interface_id, settlement_mapping_id,
  --      amount_minor, currency, currency_exponent, tax_code, tax_rate_bp and tax_amount_minor
  --      all equal the quote's values — so a NULL mapping/interface can never bypass binding.
  FOREIGN KEY (tenant_id, site_id, package_revision_id)
    REFERENCES internet_package_revisions (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id)
    REFERENCES stays (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id, pms_interface_id, settlement_mapping_id)
    REFERENCES package_settlement_mappings (tenant_id, site_id, package_revision_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, authentication_interface_revision_id)
    REFERENCES pms_interface_revisions (tenant_id, site_id, pms_interface_id, id));
-- ATOMIC CONSUMPTION: one transaction performs BOTH compare-and-set updates and the insert:
--   UPDATE offer_quotes  SET consumed_at=now() WHERE id=$q AND consumed_at IS NULL AND expires_at>now();
--   UPDATE auth_contexts SET consumed_at=now() WHERE id=$c AND consumed_at IS NULL AND expires_at>now();
--   (either CAS returning 0 rows aborts) then INSERT the purchase. Races get exactly one winner.
CREATE UNIQUE INDEX purchase_once_per_stay ON purchases (stay_id, package_revision_id)
  WHERE state IN ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW','GRANTED') AND trigger='GUEST_SELECTION';
CREATE UNIQUE INDEX one_conversion_per_episode ON purchases (stay_id, checkout_episode)
  WHERE trigger IN ('CHECKOUT_GRACE','EMERGENCY_GRACE','POST_STAY_CONVERSION');

CREATE TABLE settlements (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, purchase_id uuid NOT NULL UNIQUE,
  method text NOT NULL CHECK (method IN ('NOT_REQUIRED','PREPAID','PMS_POSTING','ONLINE_PAYMENT','MANUAL_APPROVAL')),
  status text NOT NULL CHECK (status IN ('NOT_REQUIRED','REQUIRED','IN_PROGRESS','SETTLED','FAILED',
    'MANUAL_REVIEW','PARTIALLY_REVERSED','REVERSED')),
  UNIQUE (id, purchase_id), UNIQUE (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, purchase_id) REFERENCES purchases (tenant_id, site_id, id));

-- pms_postings (append-only ledger): pins settlement/purchase EXACT PAIR via
--   FK (settlement_id, purchase_id) → settlements(id, purchase_id) and
--   FK (purchase_id, pms_interface_id) → purchases(id, pms_interface_id);
--   plus composite FKs (all tenant/site/interface-scoped) to stays, folios,
--   stay_folios(stay_id, folio_id), package_settlement_mappings (incl. package_revision_id),
--   pms_interface_revisions (posting_interface_revision_id), pms_interface_secret_generations;
--   UNIQUE idempotency_key; posting_type CHARGE|REVERSAL with reverses_posting_id and
--   Σ(REVERSAL) ≤ CHARGE trigger; INSERT trigger re-reads stays (IN_HOUSE ∧ posting_allowed,
--   except REVERSAL); amount_minor/currency/exponent snapshotted; request/response evidence
--   redacted; UNIQUE(tenant, site, pms_interface_id, id) as the outbox pin anchor.
-- posting_outbox: composite FK (tenant, site, pms_interface_id, posting_id) → that anchor;
--   state QUEUED|IN_FLIGHT|DONE|HELD_RECOVERY; per-interface serialized lanes;
--   UNIQUE(posting_id) WHERE state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY')  -- one active row.

CREATE TABLE payment_transactions (           -- typed, append-only, merchant-scoped
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, settlement_id uuid NOT NULL,
  merchant_account_id uuid NOT NULL,          -- the provider account (e.g. Stripe account) used
  transaction_type text NOT NULL CHECK (transaction_type IN ('CHARGE','REFUND','CHARGEBACK')),
  parent_transaction_id uuid,
  provider text NOT NULL, provider_ref text NOT NULL, idempotency_key text NOT NULL UNIQUE,
  amount_minor bigint NOT NULL CHECK (amount_minor > 0),
  currency char(3) NOT NULL, currency_exponent smallint NOT NULL,
  status text NOT NULL CHECK (status IN ('CREATED','PENDING','CAPTURED','FAILED','EXPIRED','CANCELLED','UNKNOWN')),
  UNIQUE (tenant_id, provider, merchant_account_id, provider_ref),   -- refs scoped per merchant account
  UNIQUE (tenant_id, site_id, settlement_id, id),
  CONSTRAINT ptx_parent CHECK ((transaction_type='CHARGE') = (parent_transaction_id IS NULL)),
  FOREIGN KEY (tenant_id, site_id, settlement_id) REFERENCES settlements (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, settlement_id, parent_transaction_id)   -- parent: same tenant/site/settlement
    REFERENCES payment_transactions (tenant_id, site_id, settlement_id, id),
  FOREIGN KEY (tenant_id, merchant_account_id) REFERENCES stripe_accounts (tenant_id, id));
-- Trigger: parent must be transaction_type='CHARGE' with identical currency/exponent and the
-- same merchant_account_id; Σ(REFUND amounts per parent) ≤ parent amount. Never overwritten.

-- P# allocation: a DURABLE ATOMIC per-interface sequence (§9a rule 2). NOT a Unix timestamp.
CREATE TABLE pms_interface_pnumber_seq (    -- one row per interface; bumped transactionally
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  next_p_number bigint NOT NULL DEFAULT 1,
  PRIMARY KEY (pms_interface_id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES pms_interfaces (tenant_id, site_id, id));
-- Allocation: UPDATE ... SET next_p_number = next_p_number + 1 RETURNING (old) under the
-- posting transaction (contention serialized), so every P# is unique and monotonic per interface
-- and survives restart. The wire P# is rendered from this durable value.

CREATE TABLE posting_attempts (             -- IMMUTABLE request identity + controlled one-way state (§9a rule 2)
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  internal_posting_id uuid NOT NULL,        -- the logical Posting (pms_postings.id)
  pms_interface_id uuid NOT NULL,
  attempt_no int NOT NULL,
  -- IMMUTABLE request/transmission identity (never updated after insert; trigger-enforced):
  p_number text NOT NULL,                    -- FIAS P# — unique protocol attempt, NOT business idempotency
  rn text, g_number text,                    -- RN / G# sent on the PS (G# mandatory for guest folio)
  sent_at timestamptz NOT NULL,
  -- CONTROLLED one-way state (the ONLY mutable columns; monotonic transitions only):
  outcome text NOT NULL DEFAULT 'SENDING'
    CHECK (outcome IN ('SENDING','ACKED','UNKNOWN','FAILED')),
  response_at timestamptz,
  pa_as_status text CHECK (pa_as_status IN ('OK','NG','NA','NP','NR','RY','UR')),
  UNIQUE (tenant_id, site_id, pms_interface_id, p_number),   -- uniqueness scoped by tenant+site+interface+P#
  UNIQUE (internal_posting_id, attempt_no),
  UNIQUE (tenant_id, site_id, id),                            -- anchor for the events FK
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES pms_interfaces (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, internal_posting_id) REFERENCES pms_postings (tenant_id, site_id, id));
-- Trigger: identity columns are immutable after insert; `outcome` may advance only
-- SENDING → {ACKED|UNKNOWN|FAILED} (one-way, never back to SENDING). A PS with sent_at and no
-- matched PA past the timeout ⇒ outcome=UNKNOWN (never auto-retried). A manually-approved retry
-- inserts a NEW attempt_no (new P#) under the SAME internal_posting_id.

CREATE TABLE posting_attempt_events (       -- FULLY APPEND-ONLY audit history (no update/delete)
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  posting_attempt_id uuid NOT NULL,
  event_type text NOT NULL,                  -- SENT | PA_RECEIVED | TIMED_OUT | MARKED_UNKNOWN | REVIEW_DECISION …
  detail jsonb NOT NULL DEFAULT '{}',        -- redacted (AS status, timing, actor, decision — never secrets)
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, site_id, posting_attempt_id)
    REFERENCES posting_attempts (tenant_id, site_id, id));
-- INSERT-only (trigger rejects UPDATE/DELETE). Every state change on posting_attempts writes one
-- event here, giving a complete immutable audit trail while posting_attempts holds current state.
```

### 4.6 Entitlements, transfers, devices, sessions, accounting

```sql
CREATE TABLE entitlements (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  stay_id uuid, guest_account_id uuid, voucher_id uuid, guest_principal_id uuid,
  pms_interface_id uuid,
  purchase_id uuid NOT NULL UNIQUE,           -- exactly one entitlement per purchase
  policy_snapshot jsonb NOT NULL, snapshot_version int NOT NULL DEFAULT 1,
  service_plan_revision_id uuid NOT NULL, package_revision_id uuid NOT NULL,
  time_accounting_mode text NOT NULL,
  end_mode text NOT NULL CHECK (end_mode IN ('FIXED_AT','VALIDITY_WINDOW','AT_CHECKOUT',
    'EARLIEST_OF_FIXED_AND_CHECKOUT','GRACE_AFTER_CHECKOUT','MANUAL_END')),
  window_ends_at timestamptz,                 -- stamped ONCE at window open; immutable after
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','ACTIVE','SUSPENDED','TERMINATED')),
  terminal_reason text CHECK (terminal_reason IN ('TIME','DATA','HARD_EXPIRY','CHECKOUT','ADMIN',
    'REVOKED','SUPERSEDED','CONVERTED','TRANSFERRED','CANCELLED','OTHER')),
  consumed_data_bytes bigint NOT NULL DEFAULT 0 CHECK (consumed_data_bytes >= 0),
  consumed_online_seconds bigint NOT NULL DEFAULT 0 CHECK (consumed_online_seconds >= 0),
  usage_version bigint NOT NULL DEFAULT 0, renewal_number int NOT NULL DEFAULT 1,
  supersedes_entitlement_id uuid UNIQUE,
  is_emergency_grace boolean NOT NULL DEFAULT false,
  activated_at timestamptz, terminated_at timestamptz,
  CONSTRAINT ent_one_subject CHECK (num_nonnulls(stay_id, guest_account_id, voucher_id, guest_principal_id) = 1),
  CONSTRAINT ent_terminal CHECK ((status='TERMINATED') = (terminal_reason IS NOT NULL)),
  UNIQUE (tenant_id, id), UNIQUE (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, purchase_id) REFERENCES purchases (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, supersedes_entitlement_id) REFERENCES entitlements (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id)
    REFERENCES stays (tenant_id, site_id, pms_interface_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, guest_account_id)
    REFERENCES guest_access_accounts (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, voucher_id) REFERENCES vouchers (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, guest_principal_id)  REFERENCES guest_principals (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, service_plan_revision_id)
    REFERENCES service_plan_revisions (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id)
    REFERENCES internet_package_revisions (tenant_id, site_id, id));
-- ONE LIVE ENTITLEMENT PER SUBJECT. Guest principals are tenant-wide, so their
-- liveness is scoped PER SITE (one live entitlement per principal per site):
CREATE UNIQUE INDEX ent_live_stay      ON entitlements (stay_id)                     WHERE status IN ('PENDING','ACTIVE','SUSPENDED');
CREATE UNIQUE INDEX ent_live_account   ON entitlements (guest_account_id)            WHERE status IN ('PENDING','ACTIVE','SUSPENDED');
CREATE UNIQUE INDEX ent_live_voucher   ON entitlements (voucher_id)                  WHERE status IN ('PENDING','ACTIVE','SUSPENDED');
CREATE UNIQUE INDEX ent_live_principal ON entitlements (guest_principal_id, site_id) WHERE status IN ('PENDING','ACTIVE','SUSPENDED');
-- Triggers: (a) no transition out of TERMINATED; (b) SUPERSESSION LINEAGE — a row with
-- supersedes_entitlement_id must have the SAME subject type and id and same tenant/site as
-- the superseded row; cross-subject supersession is rejected.
-- entitlement_adjustments(id, tenant, site, entitlement_id, field, old, new, actor, reason):
-- the SOLE mechanism by which consumed counters decrease or windows move; fully audited.

CREATE TABLE entitlement_transfers (          -- typed, cycle-safe cross-PMS lineage (NOT supersession)
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  from_entitlement_id uuid NOT NULL UNIQUE,   -- each entitlement transfers out at most once
  to_entitlement_id uuid NOT NULL UNIQUE,     -- and is transfer-created at most once
  from_stay_id uuid NOT NULL, to_stay_id uuid NOT NULL,
  reason text NOT NULL DEFAULT 'CROSS_PMS_TRANSFER',
  actor uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT et_no_self  CHECK (from_entitlement_id <> to_entitlement_id),
  CONSTRAINT et_two_stays CHECK (from_stay_id <> to_stay_id),
  FOREIGN KEY (tenant_id, site_id, from_entitlement_id) REFERENCES entitlements (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, to_entitlement_id)   REFERENCES entitlements (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, from_stay_id) REFERENCES stays (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, to_stay_id)   REFERENCES stays (tenant_id, site_id, id));
-- ≤1 outgoing and ≤1 incoming edge per entitlement + no self-edges ⇒ lineage is a simple
-- acyclic chain by construction.

CREATE TABLE devices (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, appliance_id uuid NOT NULL, mac macaddr NOT NULL,
  first_seen timestamptz, last_seen timestamptz, last_ip inet,
  UNIQUE (tenant_id, site_id, appliance_id, mac), UNIQUE (tenant_id, site_id, id));
-- device_network_appearances(tenant, site, device_id, guest_network_id, first/last_seen)
--   with composite FKs to devices and guest_networks.

CREATE TABLE entitlement_devices (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  entitlement_id uuid NOT NULL, device_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'AUTHORIZED' CHECK (status IN ('AUTHORIZED','DISCONNECTED')),
  grandfathered boolean NOT NULL DEFAULT false,
  disconnected_reason text, first_authorized timestamptz, last_authorized timestamptz,
  PRIMARY KEY (entitlement_id, device_id),
  FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES entitlements (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, device_id)      REFERENCES devices (tenant_id, site_id, id));

-- sessions: tenant/site NOT NULL; entitlement_id, device_id NOT NULL with composite FKs
--   (tenant, site, entitlement_id) and (tenant, site, device_id); UNIQUE(tenant, id) and
--   UNIQUE(tenant, site, id); credential_method, ip, mac, state, started/ended,
--   end_reason (incl. 'session_max'), expires_at, bytes_up/down, ingress_interface.
-- accounting_records: append-only usage ledger + UNIQUE(session_id, sample_seq).

CREATE TABLE session_counter_watermarks (     -- idempotent accounting state
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  session_id uuid PRIMARY KEY,
  source_epoch int NOT NULL DEFAULT 1,        -- ++ on counter-reset detection (class rebuild/reboot)
  last_up bigint NOT NULL DEFAULT 0, last_down bigint NOT NULL DEFAULT 0,
  sample_seq bigint NOT NULL DEFAULT 0, updated_at timestamptz NOT NULL,
  FOREIGN KEY (tenant_id, site_id, session_id) REFERENCES sessions (tenant_id, site_id, id) ON DELETE CASCADE);
```

Auxiliary tables — all with tenant (and site where site-operational) columns and composite FKs: `post_stay_profiles` (UNIQUE(origin_stay_id, origin_lifecycle_version); UNIQUE(tenant, site, id); read-only origin lineage), `auth_resolutions` (FKs guest_network + nullable resolved-stay composite; outcome codes only, never guest data), `pms_source_conflicts` (both-interface composite FKs; CHECK interface_a < interface_b; UNIQUE pair), `posting_review_actions` (composite FK to posting; immutable), `financial_epoch`, `compliance_archives`.

## 5. Plans, Packages, and Quotes — Commercial Rules

- Revisions-only editing everywhere; `current_revision_id` composite FKs guarantee same-parent pointers. Package revisions pin plan revisions; vouchers/batches pin package revisions; account assignment resolves the current revision at each grant and pins it on the entitlement.
- Money is ISO-4217 minor units with snapshotted currency exponent. Tax is computed exactly once at quote time (HALF-UP rounding to minor units) and snapshotted through Purchase → Posting/Payment. No floating point.
- **Offer Quote flow:** eligible package list → server creates a quote (resolved revision + active mapping + price + tax + tier-resolved grants; TTL 5 min; one-time) → guest confirms → the Purchase consumes **both** the quote and its Auth Context by atomic compare-and-set in the same transaction that inserts the Purchase. The Purchase cannot differ from the quote in package revision, auth context, PMS interface, settlement mapping, price, tax, or grants — composite-FK enforced for the non-null tuple and trigger-enforced with null-safe `IS NOT DISTINCT FROM` semantics for every dimension.
- Eligibility rules are typed and constrained (VIP, stay length, room type, travel agent, PMS interface, dates, prior purchases…); grant tiers are ordered first-match rows (e.g., 1–3 nights → 2 GB). Property-configurable, never platform-hardcoded, never executable expressions.

## 6. Entitlements, Devices, Sessions, Accounting

**6.1 Quota semantics.** VALIDITY_WINDOW is v1's only implemented time mode: the window opens at first activation (or configured start); `window_ends_at` is stamped once and never moves; all allowed devices share it; new sessions receive only the remainder. AGGREGATE_ONLINE_TIME is schema-present and deferred (shared device-minute balance: 2 devices online 10 minutes consume 20; pauses when no eligible session is online; idle-reaped sessions stop consuming). Data quota is an aggregate across devices from monotonic counters. Precedence: the first reached of {window end, data cap, hard expiry, checkout, admin} triggers one atomic terminal transition; all sessions are revoked once; new sessions are refused; the portal shows eligible renewal offers.

**6.2 One live entitlement per subject; supersession.** Enforced by partial unique indexes (stay, account, voucher; guest principal **per site**). Any grant to a subject holding a live entitlement supersedes it in one transaction: old → TERMINATED(SUPERSEDED — or CHECKOUT for grace), new row records `supersedes_entitlement_id`, device bindings re-created, live sessions rebound in place (session rows re-pointed; nft entries untouched; tc rates adjusted). Supersession never crosses subjects (trigger-enforced). Cross-PMS movement uses `entitlement_transfers` (typed, cycle-safe) with old → TERMINATED(TRANSFERRED) and a no-posting grace entitlement under the destination Stay.

**6.3 Devices and license capacity.** Device identity is `(tenant, site, appliance, MAC)`; MACs identify devices only. Per-entitlement distinct-device slots under the entitlement lock; same-device reconnect replaces its own session; default limit policy REJECT_NEW_DEVICE with a device-management surface. License capacity counts **distinct active devices per appliance** and is the logically outermost gate: pre-checked cheaply, re-verified last inside the transaction; on failure the entire transaction rolls back — no session/device/binding rows survive.

**6.4 Idempotent accounting.** Traffic sources are cumulative counters. Per session, a durable watermark row carries `(tenant, site, source_epoch, last_up, last_down, sample_seq)`. Each tick, one transaction per session: insert the ledger row (`UNIQUE(session_id, sample_seq)` makes any replay a no-op), advance the watermark, update session totals, and update entitlement counters (`consumed += delta`, `usage_version++`). `current < watermark` ⇒ counter reset detected ⇒ `source_epoch++` and re-baseline (reboot/class-rebuild safe). A reconciliation job rebuilds and verifies counters from the ledger; decreases happen only through audited `entitlement_adjustments`. Late samples are ledgered and counted but never reopen a terminal entitlement.

**6.5 Global advisory-lock order.** All multi-lock transactions acquire strictly ascending, never backwards: `L1 stay (salt 13) → L2 subject/credential (salt 11) → L3 entitlement (salt 17) → L4 appliance capacity (salt 7) → L5 config chains (salt 19)`. Lock order is a deadlock-safety rule; validation order is a product rule (license capacity outermost). Both hold simultaneously because all checks re-run after all locks are held, inside one atomic transaction.

## 7. Checkout Grace, Reinstatement, Post-Stay, Cross-PMS Transfer

**7.1 Mandatory Seamless Checkout Grace (site-level).** Configuration: one hidden `CHECKOUT_GRACE` system package (validated at save **and re-validated at every checkout**: active, `is_system`, correct type, `price_minor = 0`, `settlement_methods = {NOT_REQUIRED}`, valid current revision pinning an enabled plan revision). **Eligibility:** grace is created only when, at checkout, the Stay has a live entitlement **or** at least one device authorized within `eligibility_window_seconds` (default 24 h); a PMS checkout for a guest who never used the Internet creates nothing. Processing (one transaction, L1→L4): Stay → CHECKED_OUT + `posting_allowed = false`; the live stay entitlement — free, paid, or prepaid alike — → TERMINATED(CHECKOUT); a `CHECKOUT_GRACE` Purchase (keyed `one_conversion_per_episode`) creates the grace entitlement (same Stay subject; `GRACE_AFTER_CHECKOUT`); devices and sessions rebind with **zero nft churn and no re-authentication**; devices beyond the grace limit carry over with `grandfathered = true` and no new devices are admitted until the count falls below the limit. **Emergency fallback:** if grace-config validation fails at checkout, checkout still completes using built-in conservative constants (60 min, 5/2 Mbps, 500 MB, reject-new + grandfather), recorded as `trigger='EMERGENCY_GRACE'`, `is_emergency_grace = true`, with a `CHECKOUT_GRACE_CONFIG_INVALID` critical alert — never a skip, never an outage.

**7.2 Reinstatement.** Trusted PMS event or privileged audited action: → IN_HOUSE, `lifecycle_version++`, posting permission **re-evaluated** (never blanket-restored). Duplicate checkout events within one episode are idempotent no-ops; a post-reinstatement checkout is a new episode with exactly one new grace conversion.

**7.3 Post-Stay.** Optional `POST_STAY` packages are purchasable after checkout via post-stay PIN re-authentication (`auth_contexts.post_stay_profile_id`); profiles are unique per episode with read-only origin lineage; never room-owned; fully isolated from the room's next occupant.

**7.4 Cross-PMS transfer.** Never an ordinary room move (composite FKs make that unrepresentable). v1 ships detection plus a staff-triggered atomic operation via `entitlement_transfers` (§6.2) and `stay_links`; idempotent by the from/to uniqueness.

## 8. Multi-PMS Automatic Resolution (STRICT, no guest selector)

1. Candidate set = ACTIVE interfaces mapped to the client's guest network (`MAPPED`), or all ACTIVE interfaces only under explicit `ALL_ACTIVE_INTERFACES`; **an unmapped network fails closed** (+ config alert). Cap 3 default / 5 hard. Client-supplied interface hints are discarded and re-derived.
2. Parallel fan-out to all candidates with connector-specific bounded timeouts, each evaluated under its pinned current revision (normalization, evidence rules, freshness). Decision on the **complete outcome vector** — `VERIFIED | AMBIGUOUS_LOCAL | NO_MATCH | UNAVAILABLE | STALE | UNSUPPORTED_EVIDENCE`; no first-match, no fastest-response. Cache older than the revision's `max_auth_cache_age` ⇒ STALE, never VERIFIED.
3. **STRICT (the only mode):** authenticate iff exactly one candidate returns exactly one VERIFIED stay and every other candidate returns a determinate NO_MATCH. Any UNAVAILABLE/STALE/UNSUPPORTED_EVIDENCE ⇒ INDETERMINATE. `V≥2` or any AMBIGUOUS_LOCAL (sharers) ⇒ discriminator escalation. No fallback to another PMS, including the single-candidate case.
4. All non-success outcomes return one **uniform, time-padded envelope** (fixed budget) with the same escalation prompt, computed from the intersection of candidate verifier fields (save-time validation guarantees a non-empty intersection). Layered throttles (room+IP, room+MAC, per-interface, global); `auth_resolutions` audit rows carry outcome codes only.
5. **Success mints a one-time Auth Context — never a session.** Package listing, quotes, and purchases consume the context server-side; the Internet session is created only after the Purchase reaches GRANTED and the Entitlement exists. All credential methods (voucher, account, OTP, social, post-stay PIN) mint and consume contexts identically.
6. **No folio at authentication.** Folio selection, freshness revalidation, and pinning happen inside the purchase transaction: lock Stay → verify IN_HOUSE ∧ posting_allowed → fresh stay/folio revalidation per the revision's policy → select the OPEN default posting target from `stay_folios` → pin folio + posting interface revision + secret generation atomically.
7. Duplicate-source detection: endpoint/property `source_fingerprint` equality or sustained correlated identity/event evidence ⇒ CRITICAL (PMS-settled purchases on the pair require manual approval until resolved); repeated collisions ⇒ WARN; single ⇒ INFO.

## 9. FIAS Freshness & Financial Validation — SPIKE-GATED

Four independent axes per interface (thresholds marked `*` are defaults pending the mandatory live Protel spike and are replaced by measured values before this contract becomes FINAL):

1. **Transport/heartbeat health** — link up and keepalive observed ≤ 5 min\*.
2. **Feed continuity** — judged against night-audit/database-resync markers and measured activity patterns, never a naive "no events for N minutes" rule. Suspected discontinuity ⇒ `DEGRADED_FRESHNESS`.
3. **Last successful state synchronization** — age of the last full resync/night-audit completion\*.
4. **Occupancy freshness** — age of the specific stay/room data relied upon (auth ≤ 15 min\*; financial bound tighter\*).

Authentication requires axis 4 within the auth bound. **Financial creation requires all four green plus fresh stay/folio revalidation and occupancy re-verification** (mandatory for room-only-posting connectors); otherwise the purchase is refused or routed to manual approval — never posted stale.

The live Protel spike must measure and record: missed-checkout-while-link-down behavior; timeout-after-post → UNKNOWN → manual review verified against the folio; reversal semantics; stale-occupancy abort; heartbeat/keepalive cadence; resync/night-audit behavior; folio-number reuse behavior (drives `folio_identity_strategy`). Results populate the per-revision capability matrix: `can_post, supports_idempotency, read_back, reversal, folio_identity, room_only_posting, safe_retry`. FIAS never auto-retries out of UNKNOWN. **A new interface revision starts with `folio_identity_strategy = 'UNSET'` (fail-closed): `can_post` is effectively false — every financial CHARGE is rejected — until property onboarding records one concrete strategy in a new revision (§4.1 amendment, §9a rule 6).**

### 9a. FIAS posting — grounded rules (from the accepted production-implementation review)

The legacy Coral Sea Protel wire is authoritative for existing behavior; FidServ/Protel **accounting-configuration** facts (e.g. what `SO=WIFI` maps to) remain subject to confirmation by the property's Protel administrator / Finance. Grounded wire: financial record **`PS`** with field order `RN, G#, TA, PT, SO, CT, P#, WS`; `PT=D` (debit); `SO=WIFI`; `WS=STAYCONNECT`; `CT` ≤ 20 chars; **`TA` integer minor units, exponent 2, no currency code on the wire**; `G#` mandatory (an `RN`-only `ASOK` does **not** prove a Guest-Folio posting); `PA` fields `RN, AS, P#, CT`; `AS ∈ {OK, NG, NA, NP, NR, RY, UR}`; `P#` is a **unique protocol-attempt sequence, not business idempotency**.

1. **UNKNOWN, never auto-retried.** A transmitted `PS` without a matched `PA` becomes **UNKNOWN** and is never automatically retried (the legacy "retry after 3 minutes with a new `P#`" is removed — it can double-post). Resolution is external evidence + audited MANUAL_REVIEW only.
2. **Protocol-attempt ledger (`posting_attempts` + `posting_attempt_events`).** DDL in §4.5.
   - **`posting_attempts`** holds an **immutable request/transmission identity** (`internal_posting_id`, `attempt_no`, `pms_interface_id`, `p_number`, `rn`, `g_number`, `sent_at` — never updated after insert) plus a **controlled one-way state** (`outcome` advancing `SENDING → ACKED|UNKNOWN|FAILED` only, with `response_at`/`pa_as_status`). It is therefore **not** fully append-only — it carries current state under strict one-way transitions.
   - **`posting_attempt_events`** is the **fully append-only** audit history (insert-only; every state change writes one event).
   - **`P#` allocation** — the legacy implementation was observed to use an **epoch-seeded increasing** `P#`. The **new StayConnect design requirement** (not a wire-discovered or production-confirmed fact) is a **durable atomic per-interface sequence** (`pms_interface_pnumber_seq`, bumped transactionally) — **not** a Unix timestamp. Uniqueness and ownership are scoped by **`tenant_id, site_id, pms_interface_id, p_number`**.
   - `PA` is matched to its `PS` by **PMS Interface + `P#`** — never by Room Number (legacy `RN` matching is unsafe under sharers/concurrency).
3. **Currency equality (no FX in v1).** A PMS-settled Package's currency **must equal the pinned PMS Interface base currency**. FIAS carries no currency field on the wire; the interface base currency + exponent is authoritative. **Reject the Purchase if package currency ≠ interface currency.** No implicit FX conversion in v1.
4. **`SO=WIFI` acceptance ≠ revenue correctness.** An `ASOK` on `SO=WIFI` proves wire acceptance, not that the charge hit the correct revenue/transaction account. **Property Finance/Protel must confirm the FidServ `WIFI` (`SOWIFI`) mapping before any financial testing or production enablement.**
5. **Programmatic reversal is `capability=false`** until a supervised test proves the exact `PT`/`TA`/`SO` reversal semantics. **Do not assume `PT=C` or a negative `TA`.** The first controlled debit is corrected **manually in Protel by Front Office** if explicitly approved.
6. **Fail-closed folio identity (`folio_identity_strategy = 'UNSET'`) blocks all financial CHARGE (§4.1 amendment, PO-approved 2026-07-16).** A new interface revision defaults to `UNSET`. While `UNSET`, read-only PMS ingestion, guest lookup, and authentication are permitted, but **every financial CHARGE/Posting is rejected fail-closed**. The rejection is enforced **before** posting-outbox creation, **before** `P#` allocation from `pms_interface_pnumber_seq`, and **before** any PMS transmission — nothing is queued, no `P#` is consumed, no bytes reach the wire. Financial posting becomes possible only once property onboarding (§9c Tier 2) sets a concrete strategy (`GLOBALLY_UNIQUE` / `UNIQUE_PER_STAY` / `REUSED_SEQUENTIAL`) in a **new immutable revision**; existing postings pin the revision they were built against.

### 9b. FIAS live validation — Gate 3A CLOSED: PASS (2026-07-16, production-grounded)

The supervised live financial spike executed against **Coral Sea Holiday Village (Hotel ID 3, `150.0.0.18:5003`)** and closed **PASS** with full Front Office verification of a single controlled USD 1.00 `PS` debit (`P#900002`, Room 14215). Verdict: **PROTOCOL ACCEPTED, CORRECT FOLIO VERIFIED, REVENUE MAPPING VERIFIED, MANUAL CLEANUP VERIFIED.** This retires several `*`-pending assumptions in §9/§9a for this interface class:

- **`PA ASOK` with a mandatory `G#` posts to the correct Guest Folio** — verified end-to-end (correct folio, `SO=WIFI` → intended Internet revenue account, manual removal to exact original balance). `PA` matched by **PMS Interface + `P#`** as specified (§9a rule 2).
- Full evidence and per-attempt detail: [Protel-FIAS-Phase0-Spike.md](../spikes/Protel-FIAS-Phase0-Spike.md) "Gate 3A — CLOSURE" and "Execution Attempt #5".

**Production-grounded operational requirements (binding on the FIAS connector and any test harness):**

1. **Verified link-startup sequence.** Server `LS` → Client `LS` → Client `LD` → Client `LR` → `LA` acknowledgments. The client sends `LS/LD/LR` **immediately on connect** and acks incoming `LS`/`LA` with a bare `LA|`. Do **not** gate progress on a client-side "reach `LA` first" milestone — this interface retransmits `LS` and the link stalls. (Matches the existing connector in `data-plane/internal/pms/protel_fias.go`.)
2. **Single active client slot.** Each PMS Interface allows **exactly one** active client connection at a time.
3. **Single-owner lock per PMS Interface.** Production must enforce a single-owner lock so only one connector (or authorized harness) holds a given Interface; concurrent owners are rejected, not multiplexed.
4. **Guaranteed socket/process cleanup.** Connectors and harnesses must use **bounded read/write timeouts** and a **finally/defer cleanup path** that always closes the socket and terminates the process — no unbounded reads that can orphan the connection.
5. **Orphan detection/prevention at startup.** Startup must detect and clear any **orphan** connector/harness still holding the PMS slot (verify the slot is free / reap the stale owner) before connecting.
6. **Lock-before-start.** No financial test or production connector may start while **another owner holds the Interface lock**.

*(Findings 2–6 were observed directly during Gate 3A: a stalled `LA` gate and a prior harness process that retained the single client slot until reaped. They are recorded here as hard requirements for the eventual connector/harness implementation — no feature code is written at Phase 0.)*

### 9c. Phase-0 closure matrix — three validation tiers (corrected 2026-07-16)

The earlier closure plan incorrectly gated Phase-0 finalization on product behaviors that **cannot be measured before the corresponding StayConnect components exist**. Corrected: finalization rests on **already-measured protocol/architecture evidence**; property-specific financial proof is **per-property deployment**; state-machine safety behavior is **post-implementation acceptance**.

**Tier 1 — Phase-0 protocol & architecture validation (COMPLETE; the finalization basis).** Measured live and merged into §9/§9a/§9b:

- both PMS endpoints reachable and using **independent Interface namespaces**;
- verified FIAS framing and startup sequence (`LS/LD/LR/LA`, §9b finding 1);
- live `GI`/`GC`/`GO` feed behavior and read-only `DR` resync;
- mandatory **`RN` + `G#`** folio targeting (an `RN`-only `ASOK` is not proof);
- production-grounded `PS` field order and values (§9a);
- `PA` structure and known `AS` statuses (`OK/NG/NA/NP/NR/RY/UR`);
- **one live end-to-end debit** against Hotel ID 3, correct Guest Folio verified, `SO=WIFI` revenue mapping verified, manual correction + balance restoration verified (§9b);
- **single-client Socket Server** behavior (one active slot);
- **`P#` is a protocol-attempt reference, not business idempotency**;
- **transmitted-without-`PA`** risk understood (a `PS` can reach Protel even when the client never sees the `PA`; a fresh `P#` creates an independent posting ⇒ **blind retry is unsafe**);
- **programmatic reversal unsupported in v1** (§9a rule 5, Gate 3B below).

**Do NOT generalize** the single Hotel ID 3 debit as financial validation of every Property or PMS Interface, nor of sharers, multi-folio, no-post, or error-status cases.

**Gate 3B — programmatic reversal (v1 decision: DEFERRED, non-blocking).** `programmatic_reversal` capability = **false**; `PT=C` / negative-`TA` **unverified** (assume neither); corrections are **manual Front Office** operations; **not a Phase-1A requirement**; may be added only after a separate capability spike. Non-blocking for v1 provided the manual-correction limitation is **visible, audited, operationally documented** (§9a rule 5, §15 `CREATE_REVERSAL`).

**Tier 2 — Per-property financial-onboarding checklist (deployment prerequisite, NOT a Phase-0 blocker).** Before PMS Posting is enabled for **any** Property, that Property must independently confirm: PMS Interface **currency + exponent**; **Package-currency compatibility** (§9a rule 3); **`SO=WIFI` revenue mapping**; **`RN`+`G#`** folio targeting; **one controlled debit**; **actual Folio placement**; **approved cleanup/correction**; and **record one concrete `folio_identity_strategy`** (`GLOBALLY_UNIQUE` / `UNIQUE_PER_STAY` / `REUSED_SEQUENTIAL`) — this is what moves the interface **out of fail-closed `UNSET`**, and it is applied as a **new immutable interface revision** (§4.1 amendment, §9a rule 6). Until that concrete strategy is recorded, the interface stays `UNSET` and **no financial CHARGE is permitted**. **Aqua Club / Hotel ID 2 (`120.0.0.15:5001`)** sits here: it remains **read-only capable and financially unapproved** (and `folio_identity_strategy = 'UNSET'`) until it passes this checklist. Full checklist + prerequisites: spike doc "Per-property deployment checklist".

**Tier 3 — Post-implementation acceptance (cannot be measured pre-code; preserved as binding requirements).**

| ID | Acceptance area | Requires (must exist first) | Binding requirements |
|---|---|---|---|
| 3C | Posting-Engine UNKNOWN safety | Posting Engine, `posting_attempts`/`posting_attempt_events`, `pms_interface_pnumber_seq`, Manual-Review workflow | transmitted request → **UNKNOWN** when no matching `PA`; **no auto-retry**; **no auto-allocated second `P#`**; Manual-Review; external Folio reconciliation; audited `CONFIRM_POSTED`/`RETRY_APPROVED`/`ABANDON`; **no duplicate charge** |
| 3D | Checkout & Checkout-Grace | Stay/Event persistence, Checkout handler, Post-Stay profile, Checkout-Grace Purchase+Entitlement, session reassignment, accounting cutoff, idempotent event processing | healthy-link checkout; link-down checkout; delayed checkout; **stale-cache refusal**; reconnect+resync; **mandatory Checkout Grace** (no intentional disconnect/re-auth); **effective-checkout-timestamp** accounting split; **repeated-checkout idempotency** |

**Finalization — DONE (2026-07-16):** the product owner gave **explicit FINAL approval** of this corrected contract; Phase 0 is **FINAL and CLOSED**. Tier-2 (per-property) and Tier-3 (post-implementation) items were **not** finalization blockers — they are, respectively, deployment prerequisites and binding acceptance requirements that carry forward past FINAL. Deferred limitations are listed in §9d. Next authorized activity: **Product-Owner review and explicit approval or rejection of the Phase 1A implementation plan** (the plan is complete, status `READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL`). Phase 1A is NOT started; plan approval authorizes scratch/test implementation only — live-database creation and cutover need later separate approvals.

### 9d. Deferred limitations (carried past Phase-0 FINAL)

- **Hotel ID 2 (Aqua Club) financial Posting not yet approved** — read-only until its per-property onboarding checklist passes.
- **Programmatic reversal disabled** — manual Front Office correction only in v1.
- **UNKNOWN / Manual-Review behavior pending Posting-Engine implementation** — safety design specified (§9a rules 1–2), acceptance-tested post-build (Tier 3 / 3C).
- **Checkout-Grace behavior pending PMS/Entitlement implementation** — specified (§3 invariants, §16 state machines), acceptance-tested post-build (Tier 3 / 3D).
- **Physical traffic accounting** still requires live implementation acceptance (non-zero real-device usage → accounting), which cannot be proven at Phase 0.

## 10. PMS Interface Lifecycle & Failure Isolation

States: `ACTIVE ⇄ AUTH_DISABLED → DRAINING → DECOMMISSIONED` (DRAINING → ACTIVE allowed). AUTH_DISABLED: no new guest auth; posting/events continue. DRAINING: no new auth/purchases/postings; outbox drains; events for existing stays only. DECOMMISSIONED: terminal, history preserved; requires zero PENDING/SENDING/UNKNOWN postings, zero unprocessed events, and no live entitlements requiring it — or a privileged audited override routing leftovers to MANUAL_REVIEW. Hard DELETE only for never-referenced interfaces (RESTRICT FKs enforce this naturally).
Isolation: per-interface outbox lanes and event queues; per-interface circuit breakers with backoff; connector-class timeouts; bounded in-flight (FIAS serialized); per-interface health, backlog age, and UNKNOWN-count metrics with alerts. One failed PMS never affects another's authentication or posting.

## 11. Security, Keys, PII, Retention

- **Key hierarchy:** appliance KEK (hardware-bound identity store) → per-tenant DEKs (AES-256-GCM, versioned) → PMS secret generations and voucher code keys, with AAD bound to owner tuples (ciphertext copied across owners fails authentication). Optional Central escrow of wrapped DEKs for appliance replacement. Data generations and key rewrap rotate independently. Key loss ⇒ affected secrets unrecoverable by design (interfaces → needs-credentials; pinned commands → MANUAL_REVIEW; voucher batches → reissue).
- Voucher codes: HMAC-SHA256 lookup index + AEAD-encrypted recoverable value + last4 display hint; reveal/print/export require operator re-authentication and are audited; CSV formula-injection guarding.
- Guest passwords/PINs: Argon2id only, write-only, one-time reveal at create/reset, constant-shape verification, layered throttling. PMS event/posting payloads redacted at write. Card data never stored (provider references only, merchant-account scoped).
- Secrets and keys never appear in logs, telemetry, audit payloads, exports, or plaintext backups.
- Retention defaults (configurable per class): financial 7 years; stays & PII 90 days post-checkout; stay events 1 year; sessions/accounting per licensed retention; auth resolutions 90 days.

## 12. Cross-Customer Purge with Compliance Archive

On cross-customer transition, **before** purge: financial records (purchases, settlements, postings, payments, review actions, adjustments) are exported to an encrypted compliance archive under the platform compliance key (deliberately not the tenant DEK), with a SHA-256 manifest and a `compliance_archives` record; destination is Central (online) or a sealed local export. **Purge proceeds only after verified receipt.** Failure keeps the transition fail-closed (no new-customer authentication) until the archive succeeds or a privileged audited override records the decision. Then: full domain purge + tenant DEK crypto-shred + runtime flush; idempotent; self-healing at boot.

## 13. Offline & Restore Behavior

Offline: PMS authentication + LAN posting, free/prepaid/manual packages, quotes/contexts, and all enforcement continue; card packages are hidden; telemetry queues.
Restore: every supported workflow (DB-only restore, VM snapshot, full-disk image, appliance replacement) writes a **signed restore manifest** and increments `restore_generation`, forcing **FINANCIAL_RECOVERY_MODE** at next start. Defense layers: clean-shutdown marker; DB `financial_epoch` vs management-partition marker; TPM/monotonic-counter binding where the hardware profile provides it (**required for strict offline guarantees**); Central financial high-water-mark echo verified at boot and at first reconnection. In recovery: all non-terminal financial commands are `HELD_RECOVERY`; read-back-capable connectors auto-reconcile from external evidence; FIAS commands go to MANUAL_REVIEW with ledger evidence; audited operator release establishes a new epoch. Guest access is unaffected throughout.

## 14. Operational & Support Contract — Restore Limitation

The following appears in the operations manual, support terms, and deployment checklist, not only in this architecture document: **StayConnect guarantees exactly-once PMS posting only when system state is restored through supported, manifest-signed restore workflows. Restoring the appliance from an unsupported raw disk or VM snapshot voids the exactly-once posting guarantee for commands in flight at snapshot time.** Any suspected unsupported restore requires the support runbook's recovery procedure (enter FINANCIAL_RECOVERY_MODE, reconcile all held commands against PMS folios, audited operator release) before posting resumes. Site acceptance requires operator acknowledgment of this clause; the TPM/monotonic-counter hardware profile is the prescribed option for strict offline guarantees.

## 15. RBAC & Financial Manual-Review Governance

Resource keys: `service-plans, internet-packages, pms-interfaces, stays, purchases, entitlements, devices, financial-review` plus existing credential/session keys. Roles: site_admin — all; hotel_it_manager — technical (plans, interfaces, devices); front_office_operator — stays/purchases read + guest assistance; payments_operator — financial-review write; voucher_operator — credentials; site_viewer — read.
Manual-review actions (each requires financial-review write **and password re-authentication**, a mandatory reason **and** evidence, and produces an immutable `posting_review_actions` row; there is **no generic approve action**): `CONFIRM_POSTED` (external evidence mandatory), `CONFIRM_NOT_POSTED_RETRY` (requeue once, same idempotency key), `CONFIRM_NOT_POSTED_ABANDON` (FAILED_FINAL + explicit entitlement decision), `CREATE_REVERSAL` (new ledger row referencing the original), `ESCALATE`. Optional dual approval above a per-site amount threshold (default disabled).

## 16. State Machines

- **Stay:** `RESERVED → IN_HOUSE → CHECKED_OUT → (POST_STAY_ACTIVE)`; `RESERVED → CANCELLED | NO_SHOW`; `CHECKED_OUT → IN_HOUSE` only via trusted Reinstatement or privileged audited action (`lifecycle_version++`, posting permission re-evaluated). DUE_IN/DUE_OUT are derived UI states, never stored.
- **Purchase:** `PENDING → AWAITING_SETTLEMENT → GRANTED | FAILED | CANCELLED`; `AWAITING_SETTLEMENT → MANUAL_REVIEW → GRANTED | FAILED`; zero-cost/prepaid: `PENDING → GRANTED`. The Entitlement is created exactly once inside the `→ GRANTED` transaction (`entitlements.purchase_id UNIQUE`).
- **Settlement:** `NOT_REQUIRED` (terminal at birth) | `REQUIRED → IN_PROGRESS → SETTLED | FAILED | MANUAL_REVIEW`; `SETTLED → PARTIALLY_REVERSED | REVERSED` via child rows only.
- **PMS Posting:** **precondition (fail-closed):** a financial CHARGE is admitted only when the pinned interface revision's `folio_identity_strategy ≠ 'UNSET'`; while `'UNSET'` the CHARGE is **rejected before** entering `PENDING` — no outbox row, no `P#` allocation, no transmission (§4.1 amendment, §9a rule 6). Then `PENDING → SENDING → POSTED | FAILED_RETRYABLE | FAILED_FINAL | UNKNOWN`; `UNKNOWN → MANUAL_REVIEW → POSTED | FAILED_FINAL`; reversal is a new REVERSAL row. Connectors without idempotency/read-back never auto-retry from UNKNOWN. *(Note: `UNKNOWN` here is a Posting state — distinct from the folio `'UNSET'` sentinel.)*
- **Payment Transaction:** CHARGE: `CREATED → PENDING → CAPTURED | FAILED | EXPIRED | CANCELLED | UNKNOWN`; REFUND/CHARGEBACK are child rows (same tenant/site/settlement/merchant account/currency; Σ ≤ parent).
- **Entitlement:** `PENDING → ACTIVE ⇄ SUSPENDED → TERMINATED(terminal_reason)`; `PENDING → TERMINATED(CANCELLED)`; no exit from TERMINATED; SUSPENDED revokes sessions while the window keeps running.
- **Device binding:** `AUTHORIZED ⇄ DISCONNECTED(reason)`.
- **PMS Interface:** `ACTIVE ⇄ AUTH_DISABLED → DRAINING → DECOMMISSIONED` (guarded).
- **Financial workers:** `NORMAL ⇄ FINANCIAL_RECOVERY_MODE`.

## 17. API Contracts (resource level)

**Guest (portald → scd):**
`POST /auth/pms/resolve` and `POST /auth/{voucher|credentials|otp|social|post-stay-pin}` → uniform envelope; success returns `{auth_context}` — never a session · `GET /portal/packages?ctx=` → eligible package revisions + offer quotes · `POST /portal/purchases {ctx, quote_id}` → atomic CAS consumption of quote + context; on GRANTED the server creates the session (nft/tc) and returns session state · `GET /portal/entitlement?ctx=` (remaining time/data, devices) · `POST /portal/devices/{id}/disconnect` · `POST /logout`. All non-success responses are uniform generic envelopes.
**Hotel Admin (edged `/edge/v1`):** revisioned CRUD on `service-plans` and `internet-packages` (+rules/tiers/settlement-mapping append-only operations); `pms-interfaces` (+revisions, write-only secret rotation, lifecycle actions, health/backlogs); `guest-network-pms-map` (validated saves); `checkout-grace` config (validated); `stays` (read + reinstate/posting-block/transfer); `purchases`/`settlements`/`postings` (+`/review` actions per §15); `entitlements` (suspend/resume/revoke/adjust); `guest-accounts`; `voucher-batches` (reveal/export with re-auth); `devices`; `financial-recovery` (status/release); `compliance-archives` (status). Implicit tenant scope; RBAC keys; audit on every mutation; password re-authentication for financial and destructive actions.
**Central v1:** read-only telemetry (health, backlogs, conflicts, recovery/epoch state, archive receipts).

## 18. Phased Implementation Plan

| Phase | Content | Gate | Rollback boundary |
|---|---|---|---|
| **0** | This contract signed; **live Protel FIAS spike**; mews/apaleo capability contract tests; measured values merged into §9 | spike artifact + owner approval → contract FINAL | n/a |
| **1A Core Foundation** | Clean-slate schema in the standby site DB; entitlement engine (window mode, supersession, counters, watermarks); device registry; lock-order library — dark, no user-visible change | A-series acceptance | blue/green swap-back |
| **1B Credential/Portal Cutover** | Auth contexts; voucher (HMAC/AEAD), account, OTP/social (guest principals) re-pointed; session-after-grant portal flow; controlled reset of disposable test data; cutover | B-series + reboot/offline/purge drills | blue/green swap-back |
| **2** | Packages, revisions, rules, tiers, quotes; free purchases; portal package selection; grace-config UI | C-series incl. quote races | additive down-migration |
| **3** | Stay domain: interfaces/revisions/routing, STRICT resolution, stays/sharers/folios/events, room move, **mandatory checkout grace**, reinstatement — no posting | D + F series | additive |
| **4** | Financial: settlements, postings + outbox lanes, secret generations, payments re-rail, recovery mode, manual review, compliance archive; per-tenant posting enable flag | E + G series | posting flag off |
| **5** | Post-stay (PIN re-auth); cross-PMS transfer workflow | F-series remainder | additive |
| **6** | Guest device self-service; optional AGGREGATE_ONLINE_TIME | device-management tests | additive |
| **7** | Cleanup, final docs/ops manual, full-system re-acceptance (reboot, offline, purge, restore drills) | complete matrix | last blue/green snapshot |

Phases 2 and 3 are parallelizable after 1B. The detailed **Phase 1A execution plan** (migration groups, per-object specs, lock strategy, acceptance) is in [StayConnect-IAM-Phase1A-Plan.md](StayConnect-IAM-Phase1A-Plan.md), status **READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL** — planning only, pending separate owner approval before implementation. **Owner refinement (2026-07-16):** the 1A "standby site DB / blue-green swap-back" mechanism in the table above is superseded by an **isolated `iam_v2` schema inside the existing site DB** (dark; rollback = leave dark / drop schema; a separate gated `search_path` cutover, never a whole-DB swap); reversal stays `capability=false` (no executable reversal built in 1A). See the plan for the resolved decisions.

## 19. Acceptance & Failure-Drill Matrix

**A. Engine:** A1 shared immovable window across devices · A2 device over-limit REJECT + management surface · A3 same-device reconnect replaces its session, no slot burn · A4 duplicate/concurrent closes charge usage exactly once (watermarks) · A5 aggregate data cap → one atomic terminal transition, all sessions revoked once · A6 SIGKILL/restart/reboot durability; re-auth receives remainder only · A7 no exit from TERMINATED (trigger) · A8 supersession rebind with zero nft churn; cross-subject supersession rejected · A9 suspension revokes sessions, window keeps running · A10 late samples ledgered, never reopen · A11 capacity counts distinct devices; capacity failure leaves zero session/device/binding rows · A12 counter-reset epoch handling (class rebuild/reboot) · A13 reconciliation rebuild; decreases only via audited adjustment.
**B. Credentials/identities:** B1 voucher HMAC redemption, single-use enforced · B2 reveal/export re-auth + audit + CSV guard + last4 default · B3 account attaches to its live entitlement (never fresh quota per login); assigned package follows-current-then-pins · B4 OTP/social: the same verified factor on a new MAC resolves to the same tenant-wide principal and the same per-site live entitlement; issuer-scoped social subjects (same subject value from two providers = two identities); MAC never an owner · B5 lockouts, layered throttles, generic errors, one-time password reveal · B6 auth contexts one-time/TTL; method↔subject coherence (PMS context without stay rejected; POST_STAY_PIN context requires post_stay_profile_id; etc.).
**C. Commerce:** C1 eligibility rules + grant tiers · C2 quote exactness: forged purchases with a different revision/mapping/context are rejected (FK + null-safe trigger); price edits mid-flow cannot change the charge; CAS race yields exactly one consumer; context and quote consumed atomically with the purchase · C3 once-per-stay uniqueness under concurrent purchase race · C4 revision immutability · C5 mapping retire-and-create atomicity; pinned old codes on retries · C6 money: minor units, HALF-UP tax once, Σ refunds ≤ charge, parent same settlement + merchant account + currency (cross-settlement or cross-merchant parents rejected).
**D. Resolution:** D1 PMS-A room 101 vs PMS-B room 101 resolve correctly; no selector ever shown · D2 dual-verified ambiguity → uniform escalation; reservation number resolves · D3 slow verified match beats fast no-match (complete vector) · D4 STRICT refuses on any UNAVAILABLE/STALE candidate · D5 unmapped network fails closed + alert · D6 candidate cap at save and runtime · D7 forged interface hints ignored · D8 evidence-intersection validation at save; broken-by-revision alerts + fail closed · D9 sharers authenticate independently; one-primary-per-stay enforced (primary-change demotes-then-sets in one transaction; conflicting duplicate → MANUAL_REVIEW) · D10 stale cache post-checkout: no VERIFIED; financial fresh-validation blocks · D11 zero/ambiguous/unavailable responses byte-identical and time-padded; throttles and audit fire.
**E. Financial:** E1 full pin-chain fuzz (quote tuple, payment parent scope, watermark tenancy, posting pair) rejected at the SQL layer · E2 idempotency-key race → one charge · E3 UNKNOWN → manual review; all five governance actions with re-auth/reason/evidence; dual approval threshold · E4 posting on non-IN_HOUSE/blocked stay aborts · E4b **folio `UNSET` fail-closed: a CHARGE against an interface revision with `folio_identity_strategy = 'UNSET'` is rejected with no outbox row, no `P#` allocation and no transmission; read-only ingestion/lookup/auth on that interface still succeed; recording a concrete strategy (new revision) then admits CHARGE** · E5 posting permission evaluation recorded (no-post flag, closed folio, admin block, credit policy); IN_HOUSE alone grants nothing · E6 folio change between auth and purchase handled (folio pinned at purchase) · E7 secret generations pinned; delete refused until drained · E8 outbox one-active-row; retries never change interface · E9 duplicate-source severity tiers; only CRITICAL gates posting · E10 interface outage isolation · E11 decommission guards + audited override · E12 folio-number reuse: recycled number ⇒ new identity_epoch; history unaffected; open-folio uniqueness held.
**F. Stays/grace:** F1 room move preserves entitlement/devices/quota · F2 stale events never reopen a stay · F3 checkout supersedes free AND paid AND prepaid entitlements into the site grace package; zero nft churn; no re-authentication · F4 grandfathering: devices above the grace limit carry over; new admits blocked until below limit · F5 grace eligibility: no live entitlement + no recent authorization ⇒ no grace purchase; window boundary tested · F6 grace-config corruption ⇒ emergency fallback entitlement + critical alert + audit (checkout never fails, never skips) · F7 duplicate checkout idempotent per episode; reinstatement → new episode → exactly one new grace (race-tested) · F8 post-stay PIN isolation from the next occupant · F9 cross-PMS transfer via entitlement_transfers (typed, no supersedes pointer, cycle-free), idempotent, seamless rebind.
**G. Recovery/isolation:** G1 DB-restore recovery drill (held commands; read-back reconciles; FIAS → review; audited release) · G2 snapshot-restore detection drills, including the documented unsupported-raw-snapshot limitation and support-runbook path · G3 appliance replacement via DEK escrow; missing escrow → needs-credentials + review · G4 reboot mid-flight: lanes, pins, breaker states rebuilt; zero duplicate postings · G5 compliance archive → verified receipt → purge + DEK shred; archive failure keeps the transition fail-closed · G6 cross-tenant/site/interface constraint fuzzing · G7 secrets/keys/codes absent from logs, telemetry, audit payloads, exports · G8 retention jobs respect financial minimums.

---

**End of contract.** Status: **FINAL — Phase 0 CLOSED** (Product-Owner approval, 2026-07-16). The Phase-0 protocol/architecture validation is complete and merged (§9b/§9c). Per-property financial onboarding (§9c Tier 2, incl. Aqua Club) and post-implementation acceptance (§9c Tier 3 / §9d) carry forward as deployment prerequisites and binding acceptance requirements, not finalization blockers. **Next authorized activity: Product-Owner review and explicit approval or rejection of the Phase 1A implementation plan** ([StayConnect-IAM-Phase1A-Plan.md](StayConnect-IAM-Phase1A-Plan.md)). Phase 1A is NOT started; no implementation, migration, connector, UI, config, or deployment work is authorized. Plan approval authorizes scratch/test implementation only; live-database creation and cutover need later separate approvals.
