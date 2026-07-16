# StayConnect IAM — Phase 1A Execution Plan (Core Domain & Persistence Foundation)

**Status: READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL.** *(Revised 2026-07-16 after owner review of the DRAFT.)* Planning only — Phase 1A is **not** implemented or in progress. No migrations, code, PMS providers, service/config changes, deployment, guest-networking changes, or PMS traffic exist. Implementation begins only after the product owner explicitly approves this plan.

**Source of truth:** the FINAL [StayConnect-IAM-Phase0-Contract.md](StayConnect-IAM-Phase0-Contract.md) (Phase 0 CLOSED 2026-07-16). This plan **implements** that contract's approved DDL (§4.1–§4.6), invariants (§2), state machines (§16), and phased decomposition (§18); it introduces **no new architectural decisions**. Owner-directed refinements applied at this review (isolation mechanism, cutover gating, reversal scope, lock strategy, and the resolved open decisions) are recorded in §§2, 5, 8–11 and supersede the corresponding implementation notes only — not the approved architecture.

**Relationship to contract §18.** §18 defines Phase 1A as *"clean-slate schema … entitlement engine (window mode, supersession, counters, watermarks); device registry; lock-order library — dark, no user-visible change; A-series acceptance."* This plan expands that into concrete migration groups, per-object specifications, and acceptance tests. **Owner refinement (2026-07-16):** the §18 note about a *"standby site DB"* build with *"blue/green swap-back"* rollback is **superseded** — Phase 1A instead builds into an **isolated `iam_v2` PostgreSQL schema inside the existing site database** (§2/§8). This keeps the appliance's unrelated proven platform state (network, licensing, assignment, security, operational) in one database that never diverges, while the new IAM model stays fully **dark** until a separately gated cutover. Behavior that lights up in later phases (credential/portal cutover 1B; packages/quotes 2; stay domain 3; financial postings 4; post-stay/transfer 5) is **schema-created now but dormant**, so the system never passes through a partial old/new hybrid.

---

## 1. Phase 1A scope

**In scope (this phase):**

1. Create the **complete clean-slate IAM schema** (every table in contract §4.1–§4.6 + auxiliaries) inside a **new isolated PostgreSQL schema `iam_v2`** within the existing site database (`stayconnect_site`). All new tables, constraints, triggers, and functions are **schema-qualified** (`iam_v2.<object>`); nothing lands in `public`. Unrelated existing platform tables are **untouched**. Approved **cross-schema foreign keys to stable platform anchors** (e.g. `public.tenants`, `public.sites`, `public.guest_networks`, `public.appliances`) are allowed; no platform table is altered except the single additive anchor in MG-0.
2. Implement the **core entitlement engine**: `VALIDITY_WINDOW` accounting, same-subject atomic supersession, monotonic usage counters, one-live-entitlement-per-subject enforcement.
3. Implement the **device registry** (`devices`, `entitlement_devices`, `device_network_appearances`) and per-credential/appliance capacity enforcement.
4. Implement the **accounting watermark** scaffolding (`session_counter_watermarks`, `accounting_records`) — idempotent sample ingestion.
5. Implement the **lock strategy** (§5): row-level transactional locks by default; documented advisory-lock **namespaces** only where unavoidable.
6. Install all **immutability / append-only / one-way-state triggers** the contract mandates.
7. Keep everything **dark**: **no** production service (portald/edged/scd/acctd) has `iam_v2` on its `search_path` or any connection routed to it; no service reads or writes `iam_v2`; no `search_path` or connection-routing cutover occurs in Phase 1A; no user-visible change.

**Isolation & rollback model (Phase 1A):** the new model is a dormant schema alongside the live one in the same database. **Old IAM tables remain unchanged and receive no dual writes.** Rollback *before cutover* means simply **leaving `iam_v2` dark** (or `DROP SCHEMA iam_v2 CASCADE` in a test database) — **no whole-database swap-back is required**, and the live platform state never diverges. The later service-level/`search_path` cutover and its rollback are described in §7a but are **not authorized or executed** here.

**Explicitly NOT in scope of Phase 1A** (later phases, per §18): credential/portal auth flows and cutover (1B); package selection/quote UI and free purchases (2); STRICT multi-PMS resolution and live stay ingestion (3); live PMS posting/settlement/payment execution and recovery mode (4); post-stay PIN and cross-PMS transfer workflow (5); guest device self-service (6). **The tables for these exist after 1A; their service behavior does not.**

---

## 2. Migration group ordering (dependency-ordered)

All objects are created **schema-qualified in `iam_v2`** inside the existing `stayconnect_site` database, in this order; each group is one reversible migration file. Order is forced by composite foreign-key dependencies. Every group is **additive and dark** (creates only new `iam_v2` objects; MG-0 is the sole, additive touch to a platform table).

| # | Migration group | Creates (all in `iam_v2` unless noted) | Depends on |
|---|---|---|---|
| MG-0 | Platform anchors (additive, `public`) | supporting `UNIQUE (tenant_id, site_id, id)` anchor on existing `public.guest_networks` (additive index only). **No** `stripe_accounts` anchor is added here — see MG-7 / §9 decision 6 (deferred). | existing platform tables |
| MG-1 | PMS interface core | `pms_interfaces`, `pms_interface_revisions`, `pms_interface_secret_generations`, `guest_network_pms_map`, `pms_interface_pnumber_seq`, `pms_source_conflicts` | MG-0 |
| MG-2 | Plans & packages | `service_plans(_revisions)`, `internet_packages(_revisions)`, `package_eligibility_rules`, `package_grant_tiers`, `package_settlement_mappings`, `site_checkout_grace_config` | MG-1 |
| MG-3 | Guest identity & credentials | `guest_principals`, `guest_principal_identities`, `guest_access_accounts`, `voucher_code_key_generations`, `voucher_batches`, `vouchers` | MG-2 |
| MG-4 | Stay domain | `stays`, `stay_guests`, `folios`, `stay_folios`, `stay_events`, `stay_links`, `post_stay_profiles` | MG-1 |
| MG-5 | Auth & commerce | `auth_contexts`, `offer_quotes`, `purchases`, `settlements` | MG-2, MG-3, MG-4 |
| MG-6 | Entitlements, devices, sessions, accounting | `entitlements`, `entitlement_adjustments`, `entitlement_transfers`, `devices`, `device_network_appearances`, `entitlement_devices`, `sessions`, `accounting_records`, `session_counter_watermarks` | MG-2, MG-3, MG-4, MG-5 |
| MG-7 | Financial postings & payments | `pms_postings`, `posting_outbox`, `payment_transactions` (merchant-account FK **deferred**, §9), `posting_attempts`, `posting_attempt_events`, `posting_review_actions`, `financial_epoch`, `compliance_archives` | MG-1, MG-4, MG-5, MG-6 |
| MG-8 | Resolution/audit aux | `auth_resolutions` | MG-1, MG-4 |
| MG-9 | Engine components (not tables) | immutability/append-only/one-way triggers; entitlement-engine functions; lock strategy helpers; watermark ingestion | MG-1…MG-8 |

**Per-group requirements (applied to every MG-0…MG-9):** each migration file states (a) **exact objects created**; (b) that it is **additive and dark**; (c) its **platform-anchor dependencies** (cross-schema FK targets in `public`); (d) **transaction & lock requirements** (each migration runs in one transaction; DDL takes only schema-local locks; no lock on live platform tables beyond MG-0's `CREATE UNIQUE INDEX CONCURRENTLY`-eligible anchor); (e) **rollback before cutover** (drop the group's `iam_v2` objects in reverse FK order, or `DROP SCHEMA iam_v2 CASCADE` wholesale — MG-0's additive index is dropped last); (f) **acceptance tests** (§10); (g) **proof no current production service can accidentally use the objects** — the objects live only in `iam_v2`, which is absent from every production role's `search_path` and from every service DSN; a grant audit (§10) proves no production role has write access.

**Rollback before cutover** is applied in **reverse** order (MG-9 → MG-0). Because the model is a **dark isolated schema**, rollback is `DROP SCHEMA iam_v2 CASCADE` (test DB) or simply never routing to it (live DB) plus dropping the MG-0 anchor index — **no whole-database swap-back and no destructive change to live data**.

---

## 3. Shared conventions (apply to every object unless overridden)

To avoid restating identical facts per table, these conventions hold for **all** Phase 1A objects; per-object sections in §4 state only what differs.

- **Schema qualification:** every object is created in the `iam_v2` schema and referenced fully-qualified (`iam_v2.<object>`); no migration relies on `search_path`, and no object is created in `public`. Cross-schema FKs point only at approved stable platform anchors in `public`.
- **Tenant/site ownership keys:** every table carries `tenant_id uuid NOT NULL`; every **site-operational** table also carries `site_id uuid NOT NULL`. The sole tenant-wide exceptions (no `site_id`) are `guest_principals` and `guest_principal_identities`. Parents expose `UNIQUE (tenant_id, site_id, id)` (or `(tenant_id, id)` for tenant-wide) as the namespace anchor; children reference the full tuple via composite FKs. This is the mechanism that makes identical room/folio numbers across PMS interfaces non-colliding.
- **Immutable-revision pattern:** `*_revisions` and generational tables are **append-only**, enforced by a `BEFORE UPDATE/DELETE` trigger that raises. New state = new row with `revision_no+1`/`generation_no+1`; the parent's `current_revision_id` FK is repointed.
- **Audit requirements:** every mutation of a governed object writes an audit row (financial/credential/interface mutations → the relevant append-only audit/event table; entitlement counter changes → `entitlement_adjustments`; posting state → `posting_attempt_events`; manual-review → `posting_review_actions`). Secrets/PII are redacted at write and never appear in audit payloads, logs, or telemetry.
- **Transaction boundaries:** each guest-facing state change (grant, supersession, session start, posting attempt) is **one** DB transaction; partial effects are impossible (constraints + CAS). No cross-request open transactions.
- **Locking:** prefer **row-level transactional locks**; where an advisory lock is unavoidable, acquire in the single documented order defined in §5 using stable non-secret namespaces. Releasing is implicit at COMMIT. No transaction acquires locks in a different order.
- **Rollback strategy (uniform, before cutover):** leave `iam_v2` **dark** (live DB) or `DROP SCHEMA iam_v2 CASCADE` (test DB); each migration also ships a tested `down` dropping its `iam_v2` objects in reverse FK order. No whole-database swap-back; no live-data destructive step; the platform state never diverges.
- **Idempotency (uniform baseline):** natural-key `UNIQUE` constraints + `INSERT … ON CONFLICT DO NOTHING`/CAS make replays safe; per-object idempotency keys are noted where they exist.

---

## 4. Per-object specifications

Each block lists what is **not** already covered by §3. Fields: **Purpose · PK · Important FKs · Uniqueness · Immutable/Mutable · Lifecycle · Idempotency · Locking · Acceptance.** (Tenant/site ownership, audit, tx-boundary, migration order, and rollback follow §3/§2.)

### MG-1 — PMS interface core

**`pms_interfaces`** — Purpose: the namespace root; one physical PMS connection per site. PK `id`. FKs: `current_revision_id → pms_interface_revisions`. Uniqueness: `UNIQUE (tenant_id, site_id, id)`. Immutable: identity; Mutable: `lifecycle_state` (`ACTIVE⇄AUTH_DISABLED→DRAINING→DECOMMISSIONED`, guarded), `current_revision_id`. Lifecycle: §10/§16 interface state machine — Phase 1A creates the table + guard trigger; DRAINING/DECOMMISSION *enforcement* is exercised in phase 4. Idempotency: none (admin-created). Locking: `SELECT … FOR UPDATE` **row lock** on the interface row when rotating revision/secret. Acceptance: create/rotate revision keeps history; illegal state jump rejected.

**`pms_interface_revisions`** — Purpose: immutable config/capability snapshot (timezone, folio-identity strategy, measured capability matrix, verifier combinations, freshness bounds). PK `id`. FKs: composite → `pms_interfaces`. Uniqueness: `UNIQUE (pms_interface_id, revision_no)`, `UNIQUE (tenant_id, site_id, pms_interface_id, id)`. Immutable: **all columns** (append-only trigger). Lifecycle: create-only; superseded by newer revision_no. **`folio_identity_strategy` has NO unsafe global default** (§9 decision 5): a revision whose strategy is UNSET/UNKNOWN (not yet set by property onboarding) is valid to store but **blocks all financial CHARGE posting** for that interface until onboarding sets an approved strategy. Idempotency: `revision_no` natural key. Locking: row lock on the interface + append; no advisory lock needed. Acceptance: UPDATE/DELETE rejected; capability matrix round-trips; repoint atomic; **UNSET/UNKNOWN folio strategy blocks posting**.

**`pms_interface_secret_generations`** — Purpose: AEAD-encrypted interface credentials, generational. PK `id`. FKs: composite → `pms_interfaces`. Uniqueness: `UNIQUE (pms_interface_id, generation_no)`. Immutable: ciphertext/nonce/key-id; Mutable: `superseded_at` only. Lifecycle: append + supersede; **DELETE rejected while any non-terminal financial command pins the generation** (enforced when postings arrive in phase 4; trigger installed now). Idempotency: `generation_no`. Locking: row lock on the interface row. Acceptance: write-only secret; ciphertext never selectable in plaintext; delete-guard fires.

**`guest_network_pms_map`** — Purpose: fail-closed routing from a guest network to candidate interfaces. PK `(guest_network_id, pms_interface_id)`. FKs: composite → `guest_networks`, `pms_interfaces`. Uniqueness: `gnpm_one_default` partial unique (one default per network). Immutable: none; Mutable: `is_default`, `routing_mode`. Lifecycle: admin-maintained; **no rows ⇒ PMS auth unavailable there (fail closed)**. Idempotency: PK. Locking: none (admin). Acceptance: save-time validation (candidate ≤ max, shared verifier combination); zero-rows fails closed with alert (validated in phase 3, rule installed now).

**`pms_interface_pnumber_seq`** — Purpose: **durable atomic per-interface P# sequence** (contract §9a rule 2; NOT a Unix timestamp). PK `pms_interface_id`. FKs: composite → `pms_interfaces`. Uniqueness: PK (one row/interface). Immutable: keys; Mutable: `next_p_number` (monotonic increment only). Lifecycle: one row per interface, created with the interface. Idempotency: the allocation `UPDATE … SET next_p_number=next_p_number+1 RETURNING (old)` is the idempotency-free unique source; each caller gets a distinct value. Locking: the row `UPDATE` serializes contenders (this is the P# allocation point in the posting lock order). Acceptance: concurrent allocations yield unique monotonic values; survives restart.

**`pms_source_conflicts`** — Purpose: record two-interface source conflicts. PK `id`. FKs: composite to both interfaces. Uniqueness: `CHECK interface_a < interface_b` + `UNIQUE` pair. Immutable: identity; Mutable: severity/resolution. Lifecycle: created on conflict detection (phase 3+). Acceptance: ordered-pair constraint prevents duplicate mirrored rows.

### MG-2 — Plans & packages

**`service_plans` / `service_plan_revisions`** — Purpose: speed/device/time/data policy; immutable revisions. PK `id` each. FKs: `current_revision_id` composite; revision → plan composite. Uniqueness: `UNIQUE (tenant,site,code)`, `UNIQUE (service_plan_id, revision_no)`. Immutable: revision columns (down/up kbps, `max_concurrent_devices≥1`, device-limit policy, idle/continuous timeouts, `time_accounting_mode` — **v1 WINDOW only**, quotas); Mutable: plan `enabled`, `current_revision_id`. Lifecycle: create → revise (append) → repoint. Idempotency: `(plan,revision_no)`. Locking: row lock on the plan during append+repoint (no advisory lock). Acceptance: UPDATE of a revision rejected; **`AGGREGATE_ONLINE_TIME` is capability-disabled and behaviorally inert in v1** — the enum value exists for forward-compatibility but no code path implements it and **no partial functionality is exposed** (a plan set to it is rejected/blocked until the capability is delivered in a later phase).

**`internet_packages` / `internet_package_revisions`** — Purpose: sellable offer pinning one plan revision; immutable revisions with price/currency/settlement methods/duration policy. PK `id` each. FKs: revision → `service_plan_revisions` composite; `current_revision_id`. `central_template_id` is a **nullable opaque reference column only** — **Central-side template schema is OUTSIDE Phase 1A** (§9 decision 4); no Central table, FK, or template-sync flow is created in 1A, and the column stays NULL/inert until a later phase adds it under separate approval. Uniqueness: `UNIQUE (tenant,site,code)`, `UNIQUE (package_id, revision_no)`. Immutable: revision columns (`price_minor≥0`, `currency`, `currency_exponent`, `settlement_methods[]`, `duration_policy`, `package_type`); Mutable: package `active`, `current_revision_id`. Lifecycle: create → revise → repoint; `is_system` packages (CHECKOUT_GRACE) hidden. Idempotency: `(package,revision_no)`. Locking: row lock on the package during append+repoint. Acceptance: immutability; currency-equality rule wiring point present (enforced at quote/purchase in phase 2/4); `central_template_id` inert/NULL in 1A.

**`package_eligibility_rules` / `package_grant_tiers`** — Purpose: typed constrained eligibility (no expressions/scripts) and ordered first-match grant tiers, per package revision. PK `id` each. FKs: composite → package revision; CASCADE. Uniqueness: per-revision ordering key. Immutable: bound to an immutable revision. Lifecycle: created with the revision. Acceptance: rules are data, not code; ordering deterministic.

**`package_settlement_mappings`** — Purpose: append-only linear chains mapping (package revision × interface) → posting/tax codes. PK `id`. FKs: composite → package revision, interface. Uniqueness: `UNIQUE (package_revision_id, pms_interface_id, mapping_revision)`. Immutable: mapping fields; Mutable: `retired_at` (retire-and-create). Lifecycle: create → retire → replace (`replaces_mapping_id`). Idempotency: `(package_revision,interface,mapping_revision)`. Locking: row lock on the chain head (`SELECT … FOR UPDATE`) during retire+create atomicity. Acceptance: retire-and-create atomic; retries pin the old code (validated in phase 2/4).

**`site_checkout_grace_config`** — Purpose: site-level config for the hidden CHECKOUT_GRACE package + emergency fallback. PK `site_id` (or `id` with `UNIQUE(tenant,site)`). FKs: → hidden grace package revision. Immutable: none; Mutable: config with validation. Lifecycle: one per site; corruption ⇒ emergency-grace fallback (enforced in phase 3). Acceptance: invalid config rejected at save; fallback path present.

### MG-3 — Guest identity & credentials

**`guest_principals`** — Purpose: **tenant-wide** stable identity for OTP/social. PK `id`. Ownership: `tenant_id` only (no site). Uniqueness: `UNIQUE (tenant_id, id)`. Immutable: identity; Mutable: `display_name`. Lifecycle: created on first verified factor; one live entitlement **per principal per site** (enforced by partial index on entitlements, MG-6). Acceptance: same principal reused across sites; never carries `site_id`.

**`guest_principal_identities`** — Purpose: issuer-scoped verified factors (email/phone/social). PK `id`. FKs: `(tenant_id, guest_principal_id)` → `guest_principals`, CASCADE. Uniqueness: `UNIQUE (tenant_id, factor_type, factor_issuer, factor_value_norm)`; `CHECK` social requires issuer. Immutable: factor identity + `verified_at`. Lifecycle: append; a verified factor on a new MAC resolves to the same principal. Idempotency: the issuer-scoped unique key. Acceptance: same subject value from two providers = two identities; MAC never a factor.

**`guest_access_accounts`** — Purpose: username/password guest credential; assigned package **follows-current-then-pins**. PK `id`. FKs: `assigned_package_id` NULL, `stay_id` NULL. Uniqueness: `UNIQUE (tenant, lower(username))`, `UNIQUE (tenant,site,id)`. Immutable: none; Mutable: password_hash (argon2id, write-only, one-time reveal at create/reset), enabled, validity, lockout counters. Lifecycle: enable/disable/lock; login throttling. Idempotency: username unique. Locking: row lock on the account row for login-attempt accounting. Acceptance: 1-char user/pass allowed; hash never in list/get; lockout + layered throttle.

**`voucher_code_key_generations`** — Purpose: generational HMAC+AEAD keys for voucher codes. PK `id`. Uniqueness: `UNIQUE (tenant, generation_no)`. Immutable: key ciphertext/params; Mutable: `superseded_at`. Lifecycle: append + supersede. Acceptance: reveal path uses the pinned generation.

**`voucher_batches` / `vouchers`** — Purpose: single-redemption credential pinning a package revision; HMAC-indexed + AEAD-recoverable code + last4. PK `id` each. FKs: composite → `internet_package_revisions`, `voucher_code_key_generations`; `batch_id`. Uniqueness: `UNIQUE (code_hmac)`, `UNIQUE (tenant,site,id)`. Immutable: code material, `package_revision_id`; Mutable: `state` (`UNUSED→REDEEMED|REVOKED|REDEMPTION_EXPIRED`), redemption window, notes. Lifecycle: single-redemption; batches pin the revision identically. Idempotency: `code_hmac`. Locking: row lock on the voucher row at redemption (CAS on `state`); device admission uses the §5 `LN_DEVICE_SLOT` namespace. Acceptance: HMAC redemption single-use; reveal/export re-auth + audit + CSV formula-guard; last4 default.

### MG-4 — Stay domain

**`stays`** — Purpose: reservation/stay pinned to one interface namespace; room number is a **lookup attribute only**. PK `id`. FKs: composite → `pms_interfaces`. Uniqueness: `UNIQUE (tenant,site,pms_interface_id, external_reservation_id, external_stay_identity)`, plus `(tenant,site,pms_interface_id,id)`, `(tenant,site,id)`. Immutable: external identity; Mutable: `status`, `posting_allowed` (`CHECK posting_allowed=false OR status='IN_HOUSE'`), `lifecycle_version` (++ on reinstatement), `last_applied_event_version`, posting-permission fields. Lifecycle: §16 stay machine. Idempotency: `last_applied_event_version` gates event application (phase 3). Locking: row lock on the stay row (`SELECT … FOR UPDATE`) during event application. Acceptance: **no** room-occupancy uniqueness (sharers legal); posting-only-IN_HOUSE CHECK; room lookup index only on IN_HOUSE.

**`stay_guests`** — Purpose: guests on a stay; exactly one primary. PK `id`. FKs: composite → `stays`, CASCADE. Uniqueness: `one_primary_guest_per_stay` partial unique. Immutable: none; Mutable: primary flag, normalized names, `pin_hash`. Lifecycle: primary-change demotes-old-in-same-tx; duplicate re-assert ⇒ SKIPPED_DUPLICATE; conflicting identity ⇒ MANUAL_REVIEW (phase 3). Locking: parent stay lock. Acceptance: never two primaries; never silent replacement.

**`folios`** — Purpose: folio identity, strategy-aware. PK `id`. FKs: composite → `pms_interfaces`. Uniqueness: `UNIQUE (tenant,site,pms_interface_id, external_folio_id, identity_epoch)`, `folio_open_identity` partial unique. Immutable: external id; Mutable: `status` (OPEN→CLOSED), `identity_epoch` (++ on REUSED_SEQUENTIAL recycle → **new row**). Lifecycle: resolved by the interface revision's `folio_identity_strategy`; while that strategy is **UNSET/UNKNOWN, financial CHARGE posting is blocked** for the interface (§9 decision 5) — there is no unsafe global default. Acceptance: recycled number → new epoch row; postings pin folio **row id** so history can't alias; posting blocked under UNSET strategy.

**`stay_folios`** — Purpose: stay↔folio link + default posting target. PK `(stay_id, folio_id)`. FKs: composite to both. Uniqueness: `UNIQUE(stay_id) WHERE is_default_posting_target`. Acceptance: one default posting target per stay.

**`stay_events`** — Purpose: append-only normalized PMS event log with timezone + clock-suspect handling. PK `id`. FKs: composite → `stays` (nullable stay_id). Uniqueness: `UNIQUE (tenant,site,pms_interface_id, external_event_identity)`. Immutable: raw/normalized event; Mutable: `processing_status` (PENDING→APPLIED|SKIPPED_DUPLICATE|MANUAL_REVIEW|FAILED). Lifecycle: idempotent application via `external_event_identity` + `sequence_version` (phase 3). Idempotency: the event-identity unique key. Acceptance: duplicate event no-op; payload redacted at write.

**`stay_links` / `post_stay_profiles`** — Purpose: typed cross-stay lineage (CROSS_PMS_TRANSFER/POST_STAY) and read-only post-stay origin lineage. PK `id`/composite. Uniqueness: `UNIQUE(from_stay,to_stay,reason)`; `UNIQUE(origin_stay_id, origin_lifecycle_version)`. Immutable: lineage. Acceptance: lineage acyclic; post-stay profile isolated from next occupant (phase 5).

### MG-5 — Auth & commerce

**`auth_contexts`** — Purpose: **one-time** (TTL 10 min) authentication result binding method↔subject↔device↔network; **never a session**. PK `id`. FKs: composite to the single non-null subject (stay/account/voucher/principal/post-stay) + device + network + (PMS) interface revision. Uniqueness: `UNIQUE (tenant,site,id)`, `UNIQUE (id, pms_interface_id)` (quote anchor). Immutable: all binding columns; Mutable: `consumed_at` (one-way, CAS). Lifecycle: created at auth, consumed atomically with purchase. Idempotency: CAS `consumed_at IS NULL AND expires_at>now()`. Locking: consumed in the purchase transaction (see §5). Acceptance: `ac_one_subject`, `ac_method_subject`, `ac_pms_pins` CHECKs; expired/consumed context can't create a purchase.

**`offer_quotes`** — Purpose: **one-time** (TTL 5 min) exact price/grant snapshot pinning (context, package revision, interface, settlement mapping); tax computed HALF-UP exactly once here. PK `id`. FKs: composite → `auth_contexts`, `internet_package_revisions`, `package_settlement_mappings`. Uniqueness: `UNIQUE (tenant,site,id)`, `UNIQUE (id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id)` (the purchase pin tuple). Immutable: price/tax/grant snapshot; Mutable: `consumed_at`. Lifecycle: consumed atomically with purchase. Idempotency: CAS. Acceptance: a purchase with a different revision/mapping/context is rejected by FK + trigger.

**`purchases`** — Purpose: the commercial event; atomic CAS consumption of context+quote at creation. PK `id`. FKs: NULL-safe pin tuple → `offer_quotes`; composite → package revision, stay, settlement mapping, interface revision. Uniqueness: `UNIQUE (tenant,site,id)`, `UNIQUE (id, pms_interface_id)`, `offer_quote_id UNIQUE`; partial `purchase_once_per_stay`, `one_conversion_per_episode`. Immutable: pinned tuple + amounts (trigger enforces IS-NOT-DISTINCT-FROM equality to the quote); Mutable: `state` (§16 purchase machine). Lifecycle: `PENDING→AWAITING_SETTLEMENT→GRANTED|FAILED|CANCELLED`; entitlement created exactly once inside `→GRANTED`. Idempotency: quote/context CAS → exactly one winner; once-per-stay partial unique. Locking: §5 — row locks (quote/context CAS → superseded-entitlement row lock → insert); no advisory lock needed. Acceptance: C-series — forged pins rejected; price edit mid-flow can't change the charge; race → one consumer.

**`settlements`** — Purpose: one settlement per purchase; separates purchase from posting/payment. PK `id`. FKs: `purchase_id UNIQUE` composite. Uniqueness: `UNIQUE (id, purchase_id)`. Mutable: `status` machine (`NOT_REQUIRED` terminal-at-birth | `REQUIRED→IN_PROGRESS→SETTLED|FAILED|MANUAL_REVIEW`; `SETTLED→PARTIALLY_REVERSED|REVERSED` via child rows). Lifecycle: §16. Acceptance: one settlement per purchase; reversal only via child rows (phase 4).

### MG-6 — Entitlements, devices, sessions, accounting (the core engine)

**`entitlements`** — Purpose: the single live grant per subject; policy snapshot + end policy + monotonic counters. PK `id`. FKs: `purchase_id UNIQUE` composite; exactly one of stay/account/voucher/principal; `supersedes_entitlement_id UNIQUE`; plan+package revisions. Uniqueness: `UNIQUE (tenant,id)`, `(tenant,site,id)`; **partial unique** `ent_live_{stay|account|voucher}` and `ent_live_principal(guest_principal_id, site_id)` — one live entitlement per subject (per site for principals). Immutable: `policy_snapshot`, plan/package revision ids, `window_ends_at` (stamped once at window open), subject; Mutable: `status` (`PENDING→ACTIVE⇄SUSPENDED→TERMINATED`), counters (`consumed_data_bytes`, `consumed_online_seconds` — increase freely; **decrease only via `entitlement_adjustments`**), `usage_version`. Lifecycle: §16 entitlement machine; supersession = atomic same-subject swap. Idempotency: counter updates guarded by `usage_version`; watermark-driven accrual (below). Locking: §5 — row lock on the superseded entitlement (`SELECT … FOR UPDATE`) + partial-unique guard; device/capacity admission via the `LN_DEVICE_SLOT`/`LN_CAPACITY` namespaces only when admitting a device. Acceptance: A-series — window immovable across devices/reboot; no exit from TERMINATED (trigger); cross-subject supersession rejected; suspension revokes sessions, window keeps running.

**`entitlement_adjustments`** — Purpose: the **sole** audited mechanism to decrease counters or move windows. PK `id`. FKs: composite → `entitlements`. Immutable: append-only (field, old, new, actor, reason). Acceptance: counters never decrease except via an adjustment row.

**`entitlement_transfers`** — Purpose: typed cycle-safe cross-PMS lineage (NOT supersession). PK `id`. FKs: composite → from/to entitlements + from/to stays. Uniqueness: `from_entitlement_id UNIQUE`, `to_entitlement_id UNIQUE`; `CHECK` no-self, two-stays. Lifecycle: ≤1 in / ≤1 out edge ⇒ acyclic by construction (workflow lit in phase 5; schema + constraints now). Acceptance: self-edge and reused edge rejected.

**`devices`** — Purpose: the device registry; a MAC identifies a device, never a person. PK `id`. FKs: `appliance_id`. Uniqueness: `UNIQUE (tenant,site,appliance_id,mac)`, `(tenant,site,id)`. Mutable: last_seen/last_ip. Idempotency: MAC unique upsert. Acceptance: same MAC reconnect = same device row.

**`device_network_appearances`** — Purpose: where/when a device appeared. PK composite. FKs: composite → devices, guest_networks. Acceptance: append/update appearance windows.

**`entitlement_devices`** — Purpose: entitlement↔device binding + grandfathering. PK `(entitlement_id, device_id)`. FKs: composite → entitlements (CASCADE), devices. Mutable: `status` (`AUTHORIZED⇄DISCONNECTED`), `grandfathered`. Idempotency: PK; reconnect replaces same-MAC binding (no second slot). Locking: §5 — the `LN_DEVICE_SLOT` advisory namespace (device admission) acquired **before** `LN_CAPACITY`, then row locks. Acceptance: A2/A3/A11 — over-limit REJECT; same-device reconnect no slot burn; capacity counts distinct devices.

**`sessions`** — Purpose: created only **after** entitlement grant; the live data-plane session. PK `id`. FKs: composite → entitlements, devices. Uniqueness: `UNIQUE (tenant,id)`, `(tenant,site,id)`. Mutable: state/ended/end_reason/bytes. Lifecycle: start (post-grant) → active → ended (incl. `session_max`). Idempotency: duplicate/concurrent close charges usage exactly once (watermarks). Locking: §5 — row locks on the session + watermark rows. Acceptance: A4/A6 — idempotent close; SIGKILL/reboot durability, remainder-only on re-auth.

**`accounting_records`** — Purpose: append-only usage ledger. PK `id`. FKs: → sessions. Uniqueness: `UNIQUE (session_id, sample_seq)`. Immutable: append-only. Idempotency: `(session_id, sample_seq)`. Acceptance: A10 — late samples ledgered, never reopen a terminated entitlement.

**`session_counter_watermarks`** — Purpose: idempotent accounting state per session; counter-reset epoch handling. PK `session_id`. FKs: composite → sessions, CASCADE. Mutable: `last_up/last_down/sample_seq/source_epoch` (monotonic; epoch++ on reset detection). Idempotency: the watermark is the idempotency mechanism — a sample ≤ watermark is ignored; delta added once. Locking: per-session (implicit via PK row). Acceptance: A12 — class rebuild/reboot epoch handling; no double-charge on replay.

### MG-7 — Financial postings & payments (ledger schema + integrity triggers now; posting EXECUTION is phase 4)

> **Reversal scope (FINAL v1: `programmatic_reversal=false`).** Phase 1A builds **only** the passive ledger/audit representation of a reversal: the `posting_type=REVERSAL` **row kind**, the `reverses_posting_id` **linkage** to the original posting, and the `Σ(REVERSAL)≤CHARGE` **integrity constraint**. Phase 1A builds **NO** FIAS `PT=C` sender, **NO** negative-`TA` logic, **NO** automatic reversal, **NO** reversal API/UI action, and **NO** dormant executable reversal code path. A REVERSAL row in v1 is written **only** as the audited ledger record of a **manual Front Office correction** (created through the manual-review/correction workflow with RBAC + reason + evidence), never emitted to a PMS. A programmatic reversal sender requires a **later, separately approved capability spike** (contract §9d).

**`pms_postings`** — Purpose: append-only posting ledger; pins settlement/purchase exact pair + folio + both revisions + secret generation. PK `id`. FKs: `(settlement_id, purchase_id)`, `(purchase_id, pms_interface_id)`, composite → stays/folios/stay_folios/mappings/interface-revision/secret-generation. Uniqueness: `UNIQUE idempotency_key`, `UNIQUE (tenant,site,pms_interface_id,id)` (outbox anchor). Immutable: append-only; snapshotted amount/currency. Mutable: none (state lives in attempts/outbox). Lifecycle: `posting_type CHARGE|REVERSAL`; a REVERSAL row is a **manual-correction ledger record** linked via `reverses_posting_id`, constrained by `Σ(REVERSAL)≤CHARGE` — **not** a PMS-emitted reversal in v1 (see box above). INSERT trigger re-reads stay IN_HOUSE∧posting_allowed for CHARGE; **financial CHARGE posting is blocked for any interface whose `folio_identity_strategy` is UNSET/UNKNOWN** (§9 decision 5). Idempotency: `idempotency_key`. Locking: §5 (row locks; `pnumber_seq` row → outbox row). Acceptance: E-series — pin-chain fuzz rejected at SQL; CHARGE on non-IN_HOUSE aborts; **no executable reversal sender exists** (§10 check); UNSET folio strategy blocks CHARGE.

**`posting_outbox`** — Purpose: per-interface serialized delivery lane; one active row per posting. PK `id`. FKs: composite `(tenant,site,pms_interface_id,posting_id)` → posting anchor. Uniqueness: `UNIQUE(posting_id) WHERE state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY')`. Mutable: `state` (QUEUED→IN_FLIGHT→DONE|HELD_RECOVERY). Idempotency: one-active-row partial unique. Locking: row lock on the outbox row; per-interface serialization via the one-active-row partial unique. Acceptance: E8 — one active row; retries never change interface.

**`payment_transactions`** — Purpose: typed, append-only, merchant-scoped charges/refunds. PK `id`. FKs: composite → settlements; self-parent (same tenant/site/settlement). **`merchant_account_id → stripe_accounts` FK is DEFERRED** — the canonical composite `(tenant_id, id)` anchor does **not** exist on the platform `stripe_accounts` table today (it has `PRIMARY KEY (id)` + a partial unique on `tenant_id` only); adding that anchor mutates a platform table and belongs to the payment phase, so in Phase 1A `merchant_account_id` is a plain `uuid` column with **no** FK, and no placeholder table is invented (§9 decision 6). Uniqueness: `idempotency_key UNIQUE`, `UNIQUE (tenant, provider, merchant_account_id, provider_ref)`. Immutable: append-only; `ptx_parent` CHECK. Lifecycle: CHARGE `CREATED→…`; REFUND/CHARGEBACK child rows Σ≤parent. Idempotency: `idempotency_key`. Acceptance: E2 — idempotency-key race → one charge; cross-merchant parent rejected; **no cross-schema Stripe FK present in 1A**.

**`posting_attempts`** — Purpose: **immutable request identity + controlled one-way state** (contract §9a rule 2). PK `id`. FKs: composite → `pms_interfaces`, `pms_postings`. Uniqueness: `UNIQUE (tenant,site,pms_interface_id,p_number)`, `UNIQUE (internal_posting_id, attempt_no)`, `(tenant,site,id)`. Immutable: `p_number/rn/g_number/sent_at` (trigger-locked); Mutable: `outcome` (`SENDING→ACKED|UNKNOWN|FAILED`, one-way), `response_at`, `pa_as_status`. Lifecycle: no-PA-past-timeout ⇒ UNKNOWN (never auto-retried); approved retry = new attempt_no + new P#. Idempotency: `p_number` unique per interface. Locking: §5 — row lock on the `pms_interface_pnumber_seq` row (P# allocation), then outbox row. Acceptance: 3C acceptance (phase 4) — UNKNOWN, no auto-retry, no auto second P#.

**`posting_attempt_events`** — Purpose: fully append-only attempt audit history. PK `id`. FKs: composite → `posting_attempts`. Immutable: INSERT-only (trigger rejects UPDATE/DELETE). Acceptance: every state change writes one event; no mutation possible.

**`posting_review_actions` / `financial_epoch` / `compliance_archives`** — Purpose: immutable manual-review decisions (§15, five actions, re-auth + reason + evidence); financial recovery epoch marker; pre-purge encrypted archive with SHA-256 manifest + verified receipt (§12). PK `id`/singleton. Immutable: append-only / epoch-advance-only. Acceptance: no generic approve action; purge blocked until verified receipt (phases 4/7).

### MG-8 — Resolution/audit aux

**`auth_resolutions`** — Purpose: STRICT multi-PMS resolution outcomes (codes only, never guest data). PK `id`. FKs: composite → guest_network + nullable resolved stay. Immutable: append-only outcome codes. Acceptance: D-series (phase 3) — uniform time-padded non-success; never stores guest data.

### MG-9 — Engine components (not tables)

- **Immutability/append-only/one-way triggers** for every `*_revisions`, generational, ledger, attempt, event, and adjustment table (per §3).
- **Entitlement-engine functions:** grant-inside-purchase-GRANTED; atomic same-subject supersession (rebind sessions with zero nft churn — dark in 1A, no live sessions); window stamping; counter accrual from watermarks; terminal-transition guard.
- **Lock strategy helpers** (§5): row-lock-first helpers; the documented advisory-namespace order + collision test for the device/capacity admission path; every engine path uses them.
- **Watermark ingestion:** idempotent sample apply (delta once; epoch-reset detection).
- **Reversal:** only the passive REVERSAL ledger-row + linkage + `Σ(REVERSAL)≤CHARGE` constraint and the manual-correction audit path (§MG-7 box). **No** executable/dormant reversal sender.

---

## 5. Lock strategy (row locks first; advisory namespaces only where unavoidable)

**Prefer row-level transactional locks.** Most Phase-1A atomicity is achievable with ordinary row locks and the CAS/partial-unique constraints already in the DDL, and that is the default:

- **One-live-entitlement / supersession:** the partial unique indexes (`ent_live_{stay|account|voucher|principal+site}`) make a second live grant fail at the constraint; the supersession transaction takes a `SELECT … FOR UPDATE` **row lock** on the superseded entitlement (and inserts the successor) — no advisory lock needed.
- **Quote/context consumption:** the atomic `UPDATE … SET consumed_at=now() WHERE … IS NULL AND expires_at>now()` CAS is itself the row lock; races get exactly one winner.
- **P# allocation:** `UPDATE iam_v2.pms_interface_pnumber_seq SET next_p_number=next_p_number+1 … RETURNING (old)` — the row `UPDATE` serializes contenders.
- **Posting outbox:** the `one active row per posting` partial unique + a row lock on the outbox row.

**Advisory locks only where a row lock cannot express the critical section** — specifically per-credential/appliance **device-slot capacity admission**, where the contended resource is a *count across rows* that does not yet have a single row to lock. When used, advisory locks follow one documented acquisition order using **stable, non-secret lock namespaces** (named constants, not "salts" — they carry no security property):

1. **`LN_DEVICE_SLOT`** (per-credential device admission) — acquired **before** capacity, matching the shipped `reserveDeviceSlot` ordering.
2. **`LN_CAPACITY`** (per-appliance capacity admission).

Then, still inside the same transaction, the **row locks** above are taken in FK-topological order (quote/context CAS → superseded entitlement → `pms_interface_pnumber_seq` → outbox). No transaction ever acquires these in a different order (deadlock-freedom).

The namespace constants are centralized in one library module with a **collision test** (§10) proving distinct namespaces never hash-collide and that the values match the shipped device/capacity admission in `data-plane/internal/session/session.go`. **Remaining decision (§9):** confirm the final namespace constants and whether device/appliance admission migrates fully to row-lock advisory-free form in a later phase.

---

## 6. Existing IAM tables/code: replace / retain / migrate / remove

Phase 1A is **clean-slate in the isolated `iam_v2` schema**; nothing in the live IAM tables is mutated, dual-written, or dropped in 1A. The disposition below governs the *later, separately gated* cutover (§7a) and is stated now so the plan avoids a hybrid.

| Existing artifact | Disposition | Notes |
|---|---|---|
| `vouchers`, `ticket_templates`, voucher accrual/window logic (`data-plane/internal/voucher`) | **Replace** | New `iam_v2.vouchers` + `internet_package_revisions` supersede templates; the deployed validity-window voucher model already matches contract semantics. Old table retired only in a later cleanup phase. |
| `guest_access_accounts` (current) | **Migrate** | Schema-compatible; usernames already unique on `lower(username)`; argon2id hashes carried forward at cutover (never dual-written before it). |
| `sessions`, `accounting_records` (current acctd/scd) | **Replace** | New entitlement-scoped sessions + watermark model; disposable live test sessions are reset at cutover (not migrated). |
| `reserveDeviceSlot`, capacity/device advisory admission (`session.go`) | **Retain (absorb)** | Device/capacity admission semantics and namespace constants are lifted into the §5 lock strategy unchanged (renamed from "salts"). |
| Max-devices / plan-edit / rate-limit logic | **Retain (re-home)** | Behavior preserved; re-expressed against new plan/package revisions. |
| PMS lookup connector (`data-plane/internal/pms/protel_fias.go`) | **Retain, extend later** | Lookup-only today; posting engine is a **new** component in phase 4. Verified FIAS startup/single-slot/cleanup findings (contract §9b) become connector requirements. |
| Portal/edged/scd/acctd services | **Retain, re-point at cutover** | 1A adds no service code path to `iam_v2` (dark). |

**Removed in 1A:** nothing. **The old IAM schema and code remain fully in place and available for rollback during the entire initial cutover window.** Destructive removal of the old IAM tables/code happens **only** in a later, **separately approved cleanup phase** (§7a gate 8) — never during 1A and never during the initial cutover window.

---

## 7. Untouched foundations (explicitly out of scope, unchanged)

Appliance enrollment, hardware-bound identity, PKI/mTLS, signed licensing and entitlement counting, Central Control Plane boundaries, WAN/LAN configuration and netplan, guest VLANs, DHCP/DNS, captive-portal network interception, nftables/traffic-control foundations, backup/restore retention tooling, updates, remote support, and audit infrastructure. Phase 1A touches **only** the new `iam_v2` schema (plus MG-0's single additive index on `public.guest_networks`) and dark engine code — all other `public` platform tables in `stayconnect_site` are unchanged. (Cross-refs: network/topology, licensing, PKI subsystems remain as-is.)

---

## 7a. Cutover & rollback mechanism (DESCRIBED ONLY — not authorized or executed)

Build completion does **not** promote the new IAM model. Promotion is a separate, explicitly gated event. **Completion of Phase 1B does not auto-promote anything.** The gates, in order:

1. **Phase 1A implementation approval** (this plan).
2. **Phase 1A dark-schema acceptance** (§10 passes in a test DB and dark in the appliance).
3. **Phase 1B vertical-slice implementation** (one credential path end-to-end against `iam_v2`, still dark / behind a flag).
4. **End-to-end and reboot-persistence acceptance** of that slice.
5. **Explicit Product-Owner cutover approval.**
6. **IAM-only service routing / `search_path` switch** — only the IAM services are re-pointed at `iam_v2`; no other subsystem, DB, or network change.
7. **Observability and rollback window** — the old schema/code stay live and reversible; rollback = flip routing/`search_path` back (no data destruction).
8. **Separate legacy-cleanup approval** — only after a clean rollback window does a later phase drop the old IAM tables/code.

**Mechanism:** cutover is a per-service `search_path`/DSN change (routing IAM services from the old tables to `iam_v2`), reversible by flipping it back. There is **no whole-database swap**. **Phase 1A executes none of this** — it is documented so the end-state is unambiguous.

---

## 8. Disposable legacy IAM data & anti-hybrid strategy

- **Inventory first:** before any 1A build against real data, take a **schema-only backup** and a **row-count inventory** of the current IAM tables (accounts, packages/templates, purchases, sessions, entitlements, vouchers) — a documented baseline, no data copied into `iam_v2`.
- **No migration of disposables:** disposable Accounts, Packages, Purchases, Sessions, and Entitlements are **not** migrated (test fixtures — `opadmin@test.local`, `guest1`, throwaway plans/sessions/vouchers — are treated as compromised). Real carry-forward data (e.g. account hashes) is copied **only at cutover**, never before.
- **No dual-write:** old and new models are **never** written simultaneously. `iam_v2` stays dark until the gated cutover; the old model keeps serving until routing flips.
- **Rollback availability:** the current IAM implementation stays available for rollback **through the entire initial cutover window**; destructive removal is a later, separately approved cleanup phase (§7a gate 8).
- **No hybrid:** the **entire** schema is built in `iam_v2` up front and kept dark, so the system never runs half-old/half-new. The switch is a single routing flip, not a table-by-table dual-write; if acceptance fails, `iam_v2` is dropped (test DB) or left dark (live DB) and the live model is untouched.

---

## 9. Open decisions — resolved at this review + remaining for Product-Owner

**Resolved (applied in this plan):**

1. **Locking** — **prefer row-level transactional locks** (§5). Advisory locks only for device/appliance capacity admission, using documented stable **non-secret namespaces** (`LN_DEVICE_SLOT`, `LN_CAPACITY`) with a collision test — **not** called "salts".
2. **`AGGREGATE_ONLINE_TIME`** — **capability-disabled and behaviorally inert** in v1; the enum value exists but no code path implements it and **no partial functionality is exposed**.
3. **Central template schema** — **outside Phase 1A**; `central_template_id` is a nullable inert column, no Central table/FK/sync flow created.
4. **`folio_identity_strategy`** — **no unsafe global default**; an UNSET/UNKNOWN strategy is storable but **blocks financial CHARGE posting** for that interface until property onboarding sets an approved strategy.
5. **Stripe FK** — **verified**: the canonical composite `(tenant_id, id)` anchor does **not** exist on `stripe_accounts` today (it has `PRIMARY KEY (id)` + a partial unique on `tenant_id`, both in the site DB `data-plane/migrations/0001_edge_init.up.sql` and control-plane `0018_stripe_payments`). Therefore the `payment_transactions.merchant_account_id → stripe_accounts` FK is **deferred to the payment phase**; in 1A `merchant_account_id` is a plain `uuid` column with **no** FK and **no** placeholder table invented.
6. **Isolation mechanism** — **isolated `iam_v2` schema** in the existing DB (not a cloned/standby whole-database blue/green).

**Remaining genuine decisions requiring Product-Owner input:**

- **A. 1A build database** — build/test `iam_v2` in a **dedicated scratch/test database** first (recommended), then create the dark `iam_v2` schema in the live `stayconnect_site` only when acceptance passes? Confirm the build/test target.
- **B. Lock-namespace constants** — approve the final `LN_DEVICE_SLOT` / `LN_CAPACITY` numeric namespace values (must equal the shipped `session.go` admission values) and whether any later phase removes advisory admission entirely in favor of pure row locks.
- **C. Legacy-cleanup timing** — how long the old IAM schema/code must remain post-cutover before the separate cleanup phase may drop it (rollback-window length).
- **D. Reversal capability spike** — schedule (or explicitly defer) the separate capability spike that would later enable programmatic reversal; until then v1 stays manual-only.

---

## 10. Phase 1A acceptance tests (A-series + isolation/ownership; dark)

Run in a **clean test database** and then dark in the appliance's `iam_v2` schema; **no user-visible change**; rollback = drop `iam_v2` / leave dark (§8).

**Isolation, ownership & anti-hybrid checks (new):**

- **Schema ownership & least privilege:** `iam_v2` is owned by a dedicated role; **no production service role has INSERT/UPDATE/DELETE** on `iam_v2` (grant audit proves it).
- **No unqualified IAM references:** every migration/object reference is `iam_v2.`-qualified; a lint proves no reliance on `search_path`.
- **No accidental `public` objects:** the migration set creates **zero** objects in `public` except MG-0's single additive index; a catalog check enforces it.
- **No writes from running services:** with all production services running, a catalog/stat check shows **zero** writes to `iam_v2`.
- **No old/new dual-write:** static + runtime check that no code path writes both the old IAM tables and `iam_v2`.
- **Composite tenant/site isolation:** every child insert with a mismatched `(tenant_id, site_id)` tuple is rejected by the composite FKs; cross-namespace room/folio numbers never collide.
- **Migration idempotency:** applying the full migration set twice on a clean test DB is a no-op after the first (guards + `IF NOT EXISTS`), with identical catalog state.
- **Full rebuild from zero:** `DROP SCHEMA iam_v2 CASCADE` then re-apply MG-0…MG-9 reproduces the exact schema (deterministic build).
- **Reboot with no production behavior change:** appliance reboot with `iam_v2` present changes nothing user-visible; production services behave identically.
- **No executable reversal:** a check proves there is **no** FIAS `PT=C`/negative-`TA`/automatic reversal code path or API/UI action (only the passive REVERSAL ledger row + linkage + constraint exist).
- **Folio-strategy gate:** an interface revision with UNSET/UNKNOWN `folio_identity_strategy` blocks financial CHARGE posting.

**Engine (contract §19 A-series):**

- **Schema integrity:** every append-only/immutable/one-way trigger rejects UPDATE/DELETE (revisions, generations, postings, attempts, events, adjustments).
- **A1** shared immovable window across devices · **A2** device over-limit REJECT + surface · **A3** same-device reconnect replaces session, no slot burn · **A4** duplicate/concurrent closes charge once (watermarks) · **A5** aggregate data cap → one atomic terminal transition, all sessions revoked once · **A6** SIGKILL/restart/reboot durability; re-auth gets remainder only · **A7** no exit from TERMINATED · **A8** supersession rebind with zero churn; cross-subject supersession rejected · **A9** suspension revokes sessions, window keeps running · **A10** late samples ledgered, never reopen · **A11** capacity counts distinct devices; capacity failure leaves zero session/device/binding rows · **A12** counter-reset epoch handling · **A13** reconciliation rebuild; decreases only via audited adjustment.
- **One-live-entitlement:** each partial unique index (`ent_live_stay|account|voucher|principal+site`) rejects a second live grant under concurrency.
- **Lock strategy:** row-lock paths verified; the device/capacity advisory path uses the §5 namespaces in the documented order; a deliberate reversed-order test is rejected by the helper; the namespace collision test passes.

---

**End of Phase 1A plan.** Status **READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL** — planning only; Phase 1A is not implemented or in progress. No migrations, code, providers, services, config, deployment, or PMS traffic are authorized by this document.
