# StayConnect IAM — Phase 1A Execution Plan (Core Domain & Persistence Foundation)

**Status: DRAFT — planning only. Not authorized to implement.** No migrations, code, PMS providers, service/config changes, deployment, guest-networking changes, or PMS traffic. Implementation begins only after the product owner separately approves this plan.

**Source of truth:** the FINAL [StayConnect-IAM-Phase0-Contract.md](StayConnect-IAM-Phase0-Contract.md) (Phase 0 CLOSED 2026-07-16). This plan **implements** that contract's approved DDL (§4.1–§4.6), invariants (§2), state machines (§16), and phased decomposition (§18); it introduces **no new architectural decisions**. Where a detail is underspecified it is raised in §Risks & Open Decisions, not resolved unilaterally.

**Relationship to contract §18.** §18 defines Phase 1A as: *"Clean-slate schema in the standby site DB; entitlement engine (window mode, supersession, counters, watermarks); device registry; lock-order library — dark, no user-visible change. A-series acceptance. Rollback: blue/green swap-back."* This plan expands that into concrete migration groups, per-object specifications, and acceptance tests. Behavior that lights up in later phases (credential/portal cutover 1B; packages/quotes 2; stay domain 3; financial postings 4; post-stay/transfer 5) is **schema-created now but dormant**, so the database never passes through a partial old/new hybrid.

---

## 1. Phase 1A scope

**In scope (this phase):**

1. Create the **complete clean-slate IAM schema** (every table in contract §4.1–§4.6 + auxiliaries) in the **standby site DB** (`stayconnect_site_b`), isolated from the live `stayconnect_site`.
2. Implement the **core entitlement engine**: `VALIDITY_WINDOW` accounting, same-subject atomic supersession, monotonic usage counters, one-live-entitlement-per-subject enforcement.
3. Implement the **device registry** (`devices`, `entitlement_devices`, `device_network_appearances`) and per-credential/appliance capacity enforcement.
4. Implement the **accounting watermark** scaffolding (`session_counter_watermarks`, `accounting_records`) — idempotent sample ingestion.
5. Implement the **lock-order library** (single canonical acquisition order for every multi-lock transaction).
6. Install all **immutability / append-only / one-way-state triggers** the contract mandates.
7. Keep everything **dark**: no portald/edged/scd/acctd code path reads or writes the new schema in production; no user-visible change.

**Explicitly NOT in scope of Phase 1A** (later phases, per §18): credential/portal auth flows and cutover (1B); package selection/quote UI and free purchases (2); STRICT multi-PMS resolution and live stay ingestion (3); live PMS posting/settlement/payment execution and recovery mode (4); post-stay PIN and cross-PMS transfer workflow (5); guest device self-service (6). **The tables for these exist after 1A; their service behavior does not.**

---

## 2. Migration group ordering (dependency-ordered)

Migrations are applied to `stayconnect_site_b` in this order; each group is one reversible migration file. Order is forced by composite foreign-key dependencies.

| # | Migration group | Creates | Depends on |
|---|---|---|---|
| MG-0 | Platform anchors (additive) | supporting `UNIQUE (tenant_id, site_id, id)` anchor on existing `guest_networks`; confirm `stripe_accounts (tenant_id, id)` anchor | existing platform tables |
| MG-1 | PMS interface core | `pms_interfaces`, `pms_interface_revisions`, `pms_interface_secret_generations`, `guest_network_pms_map`, `pms_interface_pnumber_seq`, `pms_source_conflicts` | MG-0 |
| MG-2 | Plans & packages | `service_plans(_revisions)`, `internet_packages(_revisions)`, `package_eligibility_rules`, `package_grant_tiers`, `package_settlement_mappings`, `site_checkout_grace_config` | MG-1 |
| MG-3 | Guest identity & credentials | `guest_principals`, `guest_principal_identities`, `guest_access_accounts`, `voucher_code_key_generations`, `voucher_batches`, `vouchers` | MG-2 |
| MG-4 | Stay domain | `stays`, `stay_guests`, `folios`, `stay_folios`, `stay_events`, `stay_links`, `post_stay_profiles` | MG-1 |
| MG-5 | Auth & commerce | `auth_contexts`, `offer_quotes`, `purchases`, `settlements` | MG-2, MG-3, MG-4 |
| MG-6 | Entitlements, devices, sessions, accounting | `entitlements`, `entitlement_adjustments`, `entitlement_transfers`, `devices`, `device_network_appearances`, `entitlement_devices`, `sessions`, `accounting_records`, `session_counter_watermarks` | MG-2, MG-3, MG-4, MG-5 |
| MG-7 | Financial postings & payments | `pms_postings`, `posting_outbox`, `payment_transactions`, `posting_attempts`, `posting_attempt_events`, `posting_review_actions`, `financial_epoch`, `compliance_archives` | MG-1, MG-4, MG-5, MG-6 |
| MG-8 | Resolution/audit aux | `auth_resolutions` | MG-1, MG-4 |
| MG-9 | Engine components (not tables) | immutability/append-only/one-way triggers; entitlement-engine functions; lock-order library; watermark ingestion | MG-1…MG-8 |

Rollback is applied in **reverse** order (MG-9 → MG-0). Because the whole schema lives in the standby DB and is dark, the ultimate rollback is a **blue/green swap-back** (never promote `stayconnect_site_b`), which requires no destructive DDL against live data.

---

## 3. Shared conventions (apply to every object unless overridden)

To avoid restating identical facts per table, these conventions hold for **all** Phase 1A objects; per-object sections in §4 state only what differs.

- **Tenant/site ownership keys:** every table carries `tenant_id uuid NOT NULL`; every **site-operational** table also carries `site_id uuid NOT NULL`. The sole tenant-wide exceptions (no `site_id`) are `guest_principals` and `guest_principal_identities`. Parents expose `UNIQUE (tenant_id, site_id, id)` (or `(tenant_id, id)` for tenant-wide) as the namespace anchor; children reference the full tuple via composite FKs. This is the mechanism that makes identical room/folio numbers across PMS interfaces non-colliding.
- **Immutable-revision pattern:** `*_revisions` and generational tables are **append-only**, enforced by a `BEFORE UPDATE/DELETE` trigger that raises. New state = new row with `revision_no+1`/`generation_no+1`; the parent's `current_revision_id` FK is repointed.
- **Audit requirements:** every mutation of a governed object writes an audit row (financial/credential/interface mutations → the relevant append-only audit/event table; entitlement counter changes → `entitlement_adjustments`; posting state → `posting_attempt_events`; manual-review → `posting_review_actions`). Secrets/PII are redacted at write and never appear in audit payloads, logs, or telemetry.
- **Transaction boundaries:** each guest-facing state change (grant, supersession, session start, posting attempt) is **one** DB transaction; partial effects are impossible (constraints + CAS). No cross-request open transactions.
- **Locking order:** all multi-lock transactions acquire locks in the single canonical order defined in §5 (the lock-order library); releasing is implicit at COMMIT. No transaction ever acquires these in a different order.
- **Rollback strategy (uniform):** blue/green swap-back — the standby DB is never promoted until 1A acceptance passes; each migration also ships a tested `down` that drops its objects in reverse FK order. No live-data destructive step exists in 1A.
- **Idempotency (uniform baseline):** natural-key `UNIQUE` constraints + `INSERT … ON CONFLICT DO NOTHING`/CAS make replays safe; per-object idempotency keys are noted where they exist.

---

## 4. Per-object specifications

Each block lists what is **not** already covered by §3. Fields: **Purpose · PK · Important FKs · Uniqueness · Immutable/Mutable · Lifecycle · Idempotency · Locking · Acceptance.** (Tenant/site ownership, audit, tx-boundary, migration order, and rollback follow §3/§2.)

### MG-1 — PMS interface core

**`pms_interfaces`** — Purpose: the namespace root; one physical PMS connection per site. PK `id`. FKs: `current_revision_id → pms_interface_revisions`. Uniqueness: `UNIQUE (tenant_id, site_id, id)`. Immutable: identity; Mutable: `lifecycle_state` (`ACTIVE⇄AUTH_DISABLED→DRAINING→DECOMMISSIONED`, guarded), `current_revision_id`. Lifecycle: §10/§16 interface state machine — Phase 1A creates the table + guard trigger; DRAINING/DECOMMISSION *enforcement* is exercised in phase 4. Idempotency: none (admin-created). Locking: interface-scoped advisory lock when rotating revision/secret. Acceptance: create/rotate revision keeps history; illegal state jump rejected.

**`pms_interface_revisions`** — Purpose: immutable config/capability snapshot (timezone, folio-identity strategy, measured capability matrix, verifier combinations, freshness bounds). PK `id`. FKs: composite → `pms_interfaces`. Uniqueness: `UNIQUE (pms_interface_id, revision_no)`, `UNIQUE (tenant_id, site_id, pms_interface_id, id)`. Immutable: **all columns** (append-only trigger). Lifecycle: create-only; superseded by newer revision_no. Idempotency: `revision_no` natural key. Locking: interface advisory lock during append+repoint. Acceptance: UPDATE/DELETE rejected; capability matrix round-trips; repoint is atomic.

**`pms_interface_secret_generations`** — Purpose: AEAD-encrypted interface credentials, generational. PK `id`. FKs: composite → `pms_interfaces`. Uniqueness: `UNIQUE (pms_interface_id, generation_no)`. Immutable: ciphertext/nonce/key-id; Mutable: `superseded_at` only. Lifecycle: append + supersede; **DELETE rejected while any non-terminal financial command pins the generation** (enforced when postings arrive in phase 4; trigger installed now). Idempotency: `generation_no`. Locking: interface advisory lock. Acceptance: write-only secret; ciphertext never selectable in plaintext; delete-guard fires.

**`guest_network_pms_map`** — Purpose: fail-closed routing from a guest network to candidate interfaces. PK `(guest_network_id, pms_interface_id)`. FKs: composite → `guest_networks`, `pms_interfaces`. Uniqueness: `gnpm_one_default` partial unique (one default per network). Immutable: none; Mutable: `is_default`, `routing_mode`. Lifecycle: admin-maintained; **no rows ⇒ PMS auth unavailable there (fail closed)**. Idempotency: PK. Locking: none (admin). Acceptance: save-time validation (candidate ≤ max, shared verifier combination); zero-rows fails closed with alert (validated in phase 3, rule installed now).

**`pms_interface_pnumber_seq`** — Purpose: **durable atomic per-interface P# sequence** (contract §9a rule 2; NOT a Unix timestamp). PK `pms_interface_id`. FKs: composite → `pms_interfaces`. Uniqueness: PK (one row/interface). Immutable: keys; Mutable: `next_p_number` (monotonic increment only). Lifecycle: one row per interface, created with the interface. Idempotency: the allocation `UPDATE … SET next_p_number=next_p_number+1 RETURNING (old)` is the idempotency-free unique source; each caller gets a distinct value. Locking: the row `UPDATE` serializes contenders (this is the P# allocation point in the posting lock order). Acceptance: concurrent allocations yield unique monotonic values; survives restart.

**`pms_source_conflicts`** — Purpose: record two-interface source conflicts. PK `id`. FKs: composite to both interfaces. Uniqueness: `CHECK interface_a < interface_b` + `UNIQUE` pair. Immutable: identity; Mutable: severity/resolution. Lifecycle: created on conflict detection (phase 3+). Acceptance: ordered-pair constraint prevents duplicate mirrored rows.

### MG-2 — Plans & packages

**`service_plans` / `service_plan_revisions`** — Purpose: speed/device/time/data policy; immutable revisions. PK `id` each. FKs: `current_revision_id` composite; revision → plan composite. Uniqueness: `UNIQUE (tenant,site,code)`, `UNIQUE (service_plan_id, revision_no)`. Immutable: revision columns (down/up kbps, `max_concurrent_devices≥1`, device-limit policy, idle/continuous timeouts, `time_accounting_mode` — **v1 WINDOW only**, quotas); Mutable: plan `enabled`, `current_revision_id`. Lifecycle: create → revise (append) → repoint. Idempotency: `(plan,revision_no)`. Locking: plan advisory lock on append+repoint. Acceptance: UPDATE of a revision rejected; `AGGREGATE_ONLINE_TIME` present in enum but not enforced (deferred to phase 6).

**`internet_packages` / `internet_package_revisions`** — Purpose: sellable offer pinning one plan revision; immutable revisions with price/currency/settlement methods/duration policy. PK `id` each. FKs: revision → `service_plan_revisions` composite; `current_revision_id`; `central_template_id` NULL. Uniqueness: `UNIQUE (tenant,site,code)`, `UNIQUE (package_id, revision_no)`. Immutable: revision columns (`price_minor≥0`, `currency`, `currency_exponent`, `settlement_methods[]`, `duration_policy`, `package_type`); Mutable: package `active`, `current_revision_id`. Lifecycle: create → revise → repoint; `is_system` packages (CHECKOUT_GRACE) hidden. Idempotency: `(package,revision_no)`. Locking: package advisory lock. Acceptance: immutability; currency-equality rule wiring point present (enforced at quote/purchase in phase 2/4).

**`package_eligibility_rules` / `package_grant_tiers`** — Purpose: typed constrained eligibility (no expressions/scripts) and ordered first-match grant tiers, per package revision. PK `id` each. FKs: composite → package revision; CASCADE. Uniqueness: per-revision ordering key. Immutable: bound to an immutable revision. Lifecycle: created with the revision. Acceptance: rules are data, not code; ordering deterministic.

**`package_settlement_mappings`** — Purpose: append-only linear chains mapping (package revision × interface) → posting/tax codes. PK `id`. FKs: composite → package revision, interface. Uniqueness: `UNIQUE (package_revision_id, pms_interface_id, mapping_revision)`. Immutable: mapping fields; Mutable: `retired_at` (retire-and-create). Lifecycle: create → retire → replace (`replaces_mapping_id`). Idempotency: `(package_revision,interface,mapping_revision)`. Locking: mapping-chain advisory lock during retire+create atomicity. Acceptance: retire-and-create atomic; retries pin the old code (validated in phase 2/4).

**`site_checkout_grace_config`** — Purpose: site-level config for the hidden CHECKOUT_GRACE package + emergency fallback. PK `site_id` (or `id` with `UNIQUE(tenant,site)`). FKs: → hidden grace package revision. Immutable: none; Mutable: config with validation. Lifecycle: one per site; corruption ⇒ emergency-grace fallback (enforced in phase 3). Acceptance: invalid config rejected at save; fallback path present.

### MG-3 — Guest identity & credentials

**`guest_principals`** — Purpose: **tenant-wide** stable identity for OTP/social. PK `id`. Ownership: `tenant_id` only (no site). Uniqueness: `UNIQUE (tenant_id, id)`. Immutable: identity; Mutable: `display_name`. Lifecycle: created on first verified factor; one live entitlement **per principal per site** (enforced by partial index on entitlements, MG-6). Acceptance: same principal reused across sites; never carries `site_id`.

**`guest_principal_identities`** — Purpose: issuer-scoped verified factors (email/phone/social). PK `id`. FKs: `(tenant_id, guest_principal_id)` → `guest_principals`, CASCADE. Uniqueness: `UNIQUE (tenant_id, factor_type, factor_issuer, factor_value_norm)`; `CHECK` social requires issuer. Immutable: factor identity + `verified_at`. Lifecycle: append; a verified factor on a new MAC resolves to the same principal. Idempotency: the issuer-scoped unique key. Acceptance: same subject value from two providers = two identities; MAC never a factor.

**`guest_access_accounts`** — Purpose: username/password guest credential; assigned package **follows-current-then-pins**. PK `id`. FKs: `assigned_package_id` NULL, `stay_id` NULL. Uniqueness: `UNIQUE (tenant, lower(username))`, `UNIQUE (tenant,site,id)`. Immutable: none; Mutable: password_hash (argon2id, write-only, one-time reveal at create/reset), enabled, validity, lockout counters. Lifecycle: enable/disable/lock; login throttling. Idempotency: username unique. Locking: per-account advisory lock on login attempt accounting. Acceptance: 1-char user/pass allowed; hash never in list/get; lockout + layered throttle.

**`voucher_code_key_generations`** — Purpose: generational HMAC+AEAD keys for voucher codes. PK `id`. Uniqueness: `UNIQUE (tenant, generation_no)`. Immutable: key ciphertext/params; Mutable: `superseded_at`. Lifecycle: append + supersede. Acceptance: reveal path uses the pinned generation.

**`voucher_batches` / `vouchers`** — Purpose: single-redemption credential pinning a package revision; HMAC-indexed + AEAD-recoverable code + last4. PK `id` each. FKs: composite → `internet_package_revisions`, `voucher_code_key_generations`; `batch_id`. Uniqueness: `UNIQUE (code_hmac)`, `UNIQUE (tenant,site,id)`. Immutable: code material, `package_revision_id`; Mutable: `state` (`UNUSED→REDEEMED|REVOKED|REDEMPTION_EXPIRED`), redemption window, notes. Lifecycle: single-redemption; batches pin the revision identically. Idempotency: `code_hmac`. Locking: per-voucher advisory lock at redemption (device-slot lock order, §5). Acceptance: HMAC redemption single-use; reveal/export re-auth + audit + CSV formula-guard; last4 default.

### MG-4 — Stay domain

**`stays`** — Purpose: reservation/stay pinned to one interface namespace; room number is a **lookup attribute only**. PK `id`. FKs: composite → `pms_interfaces`. Uniqueness: `UNIQUE (tenant,site,pms_interface_id, external_reservation_id, external_stay_identity)`, plus `(tenant,site,pms_interface_id,id)`, `(tenant,site,id)`. Immutable: external identity; Mutable: `status`, `posting_allowed` (`CHECK posting_allowed=false OR status='IN_HOUSE'`), `lifecycle_version` (++ on reinstatement), `last_applied_event_version`, posting-permission fields. Lifecycle: §16 stay machine. Idempotency: `last_applied_event_version` gates event application (phase 3). Locking: per-stay advisory lock on event application. Acceptance: **no** room-occupancy uniqueness (sharers legal); posting-only-IN_HOUSE CHECK; room lookup index only on IN_HOUSE.

**`stay_guests`** — Purpose: guests on a stay; exactly one primary. PK `id`. FKs: composite → `stays`, CASCADE. Uniqueness: `one_primary_guest_per_stay` partial unique. Immutable: none; Mutable: primary flag, normalized names, `pin_hash`. Lifecycle: primary-change demotes-old-in-same-tx; duplicate re-assert ⇒ SKIPPED_DUPLICATE; conflicting identity ⇒ MANUAL_REVIEW (phase 3). Locking: parent stay lock. Acceptance: never two primaries; never silent replacement.

**`folios`** — Purpose: folio identity, strategy-aware. PK `id`. FKs: composite → `pms_interfaces`. Uniqueness: `UNIQUE (tenant,site,pms_interface_id, external_folio_id, identity_epoch)`, `folio_open_identity` partial unique. Immutable: external id; Mutable: `status` (OPEN→CLOSED), `identity_epoch` (++ on REUSED_SEQUENTIAL recycle → **new row**). Lifecycle: per interface folio-identity strategy. Acceptance: recycled number → new epoch row; postings pin folio **row id** so history can't alias.

**`stay_folios`** — Purpose: stay↔folio link + default posting target. PK `(stay_id, folio_id)`. FKs: composite to both. Uniqueness: `UNIQUE(stay_id) WHERE is_default_posting_target`. Acceptance: one default posting target per stay.

**`stay_events`** — Purpose: append-only normalized PMS event log with timezone + clock-suspect handling. PK `id`. FKs: composite → `stays` (nullable stay_id). Uniqueness: `UNIQUE (tenant,site,pms_interface_id, external_event_identity)`. Immutable: raw/normalized event; Mutable: `processing_status` (PENDING→APPLIED|SKIPPED_DUPLICATE|MANUAL_REVIEW|FAILED). Lifecycle: idempotent application via `external_event_identity` + `sequence_version` (phase 3). Idempotency: the event-identity unique key. Acceptance: duplicate event no-op; payload redacted at write.

**`stay_links` / `post_stay_profiles`** — Purpose: typed cross-stay lineage (CROSS_PMS_TRANSFER/POST_STAY) and read-only post-stay origin lineage. PK `id`/composite. Uniqueness: `UNIQUE(from_stay,to_stay,reason)`; `UNIQUE(origin_stay_id, origin_lifecycle_version)`. Immutable: lineage. Acceptance: lineage acyclic; post-stay profile isolated from next occupant (phase 5).

### MG-5 — Auth & commerce

**`auth_contexts`** — Purpose: **one-time** (TTL 10 min) authentication result binding method↔subject↔device↔network; **never a session**. PK `id`. FKs: composite to the single non-null subject (stay/account/voucher/principal/post-stay) + device + network + (PMS) interface revision. Uniqueness: `UNIQUE (tenant,site,id)`, `UNIQUE (id, pms_interface_id)` (quote anchor). Immutable: all binding columns; Mutable: `consumed_at` (one-way, CAS). Lifecycle: created at auth, consumed atomically with purchase. Idempotency: CAS `consumed_at IS NULL AND expires_at>now()`. Locking: consumed in the purchase transaction (see §5). Acceptance: `ac_one_subject`, `ac_method_subject`, `ac_pms_pins` CHECKs; expired/consumed context can't create a purchase.

**`offer_quotes`** — Purpose: **one-time** (TTL 5 min) exact price/grant snapshot pinning (context, package revision, interface, settlement mapping); tax computed HALF-UP exactly once here. PK `id`. FKs: composite → `auth_contexts`, `internet_package_revisions`, `package_settlement_mappings`. Uniqueness: `UNIQUE (tenant,site,id)`, `UNIQUE (id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id)` (the purchase pin tuple). Immutable: price/tax/grant snapshot; Mutable: `consumed_at`. Lifecycle: consumed atomically with purchase. Idempotency: CAS. Acceptance: a purchase with a different revision/mapping/context is rejected by FK + trigger.

**`purchases`** — Purpose: the commercial event; atomic CAS consumption of context+quote at creation. PK `id`. FKs: NULL-safe pin tuple → `offer_quotes`; composite → package revision, stay, settlement mapping, interface revision. Uniqueness: `UNIQUE (tenant,site,id)`, `UNIQUE (id, pms_interface_id)`, `offer_quote_id UNIQUE`; partial `purchase_once_per_stay`, `one_conversion_per_episode`. Immutable: pinned tuple + amounts (trigger enforces IS-NOT-DISTINCT-FROM equality to the quote); Mutable: `state` (§16 purchase machine). Lifecycle: `PENDING→AWAITING_SETTLEMENT→GRANTED|FAILED|CANCELLED`; entitlement created exactly once inside `→GRANTED`. Idempotency: quote/context CAS → exactly one winner; once-per-stay partial unique. Locking: §5 order (subject lock → quote/context CAS → insert). Acceptance: C-series — forged pins rejected; price edit mid-flow can't change the charge; race → one consumer.

**`settlements`** — Purpose: one settlement per purchase; separates purchase from posting/payment. PK `id`. FKs: `purchase_id UNIQUE` composite. Uniqueness: `UNIQUE (id, purchase_id)`. Mutable: `status` machine (`NOT_REQUIRED` terminal-at-birth | `REQUIRED→IN_PROGRESS→SETTLED|FAILED|MANUAL_REVIEW`; `SETTLED→PARTIALLY_REVERSED|REVERSED` via child rows). Lifecycle: §16. Acceptance: one settlement per purchase; reversal only via child rows (phase 4).

### MG-6 — Entitlements, devices, sessions, accounting (the core engine)

**`entitlements`** — Purpose: the single live grant per subject; policy snapshot + end policy + monotonic counters. PK `id`. FKs: `purchase_id UNIQUE` composite; exactly one of stay/account/voucher/principal; `supersedes_entitlement_id UNIQUE`; plan+package revisions. Uniqueness: `UNIQUE (tenant,id)`, `(tenant,site,id)`; **partial unique** `ent_live_{stay|account|voucher}` and `ent_live_principal(guest_principal_id, site_id)` — one live entitlement per subject (per site for principals). Immutable: `policy_snapshot`, plan/package revision ids, `window_ends_at` (stamped once at window open), subject; Mutable: `status` (`PENDING→ACTIVE⇄SUSPENDED→TERMINATED`), counters (`consumed_data_bytes`, `consumed_online_seconds` — increase freely; **decrease only via `entitlement_adjustments`**), `usage_version`. Lifecycle: §16 entitlement machine; supersession = atomic same-subject swap. Idempotency: counter updates guarded by `usage_version`; watermark-driven accrual (below). Locking: §5 — subject lock → capacity/device → row. Acceptance: A-series — window immovable across devices/reboot; no exit from TERMINATED (trigger); cross-subject supersession rejected; suspension revokes sessions, window keeps running.

**`entitlement_adjustments`** — Purpose: the **sole** audited mechanism to decrease counters or move windows. PK `id`. FKs: composite → `entitlements`. Immutable: append-only (field, old, new, actor, reason). Acceptance: counters never decrease except via an adjustment row.

**`entitlement_transfers`** — Purpose: typed cycle-safe cross-PMS lineage (NOT supersession). PK `id`. FKs: composite → from/to entitlements + from/to stays. Uniqueness: `from_entitlement_id UNIQUE`, `to_entitlement_id UNIQUE`; `CHECK` no-self, two-stays. Lifecycle: ≤1 in / ≤1 out edge ⇒ acyclic by construction (workflow lit in phase 5; schema + constraints now). Acceptance: self-edge and reused edge rejected.

**`devices`** — Purpose: the device registry; a MAC identifies a device, never a person. PK `id`. FKs: `appliance_id`. Uniqueness: `UNIQUE (tenant,site,appliance_id,mac)`, `(tenant,site,id)`. Mutable: last_seen/last_ip. Idempotency: MAC unique upsert. Acceptance: same MAC reconnect = same device row.

**`device_network_appearances`** — Purpose: where/when a device appeared. PK composite. FKs: composite → devices, guest_networks. Acceptance: append/update appearance windows.

**`entitlement_devices`** — Purpose: entitlement↔device binding + grandfathering. PK `(entitlement_id, device_id)`. FKs: composite → entitlements (CASCADE), devices. Mutable: `status` (`AUTHORIZED⇄DISCONNECTED`), `grandfathered`. Idempotency: PK; reconnect replaces same-MAC binding (no second slot). Locking: §5 device-slot lock (salt-per-credential) acquired **before** capacity lock. Acceptance: A2/A3/A11 — over-limit REJECT; same-device reconnect no slot burn; capacity counts distinct devices.

**`sessions`** — Purpose: created only **after** entitlement grant; the live data-plane session. PK `id`. FKs: composite → entitlements, devices. Uniqueness: `UNIQUE (tenant,id)`, `(tenant,site,id)`. Mutable: state/ended/end_reason/bytes. Lifecycle: start (post-grant) → active → ended (incl. `session_max`). Idempotency: duplicate/concurrent close charges usage exactly once (watermarks). Locking: §5. Acceptance: A4/A6 — idempotent close; SIGKILL/reboot durability, remainder-only on re-auth.

**`accounting_records`** — Purpose: append-only usage ledger. PK `id`. FKs: → sessions. Uniqueness: `UNIQUE (session_id, sample_seq)`. Immutable: append-only. Idempotency: `(session_id, sample_seq)`. Acceptance: A10 — late samples ledgered, never reopen a terminated entitlement.

**`session_counter_watermarks`** — Purpose: idempotent accounting state per session; counter-reset epoch handling. PK `session_id`. FKs: composite → sessions, CASCADE. Mutable: `last_up/last_down/sample_seq/source_epoch` (monotonic; epoch++ on reset detection). Idempotency: the watermark is the idempotency mechanism — a sample ≤ watermark is ignored; delta added once. Locking: per-session (implicit via PK row). Acceptance: A12 — class rebuild/reboot epoch handling; no double-charge on replay.

### MG-7 — Financial postings & payments (schema + triggers now; execution phase 4)

**`pms_postings`** — Purpose: append-only posting ledger; pins settlement/purchase exact pair + folio + both revisions + secret generation. PK `id`. FKs: `(settlement_id, purchase_id)`, `(purchase_id, pms_interface_id)`, composite → stays/folios/stay_folios/mappings/interface-revision/secret-generation. Uniqueness: `UNIQUE idempotency_key`, `UNIQUE (tenant,site,pms_interface_id,id)` (outbox anchor). Immutable: append-only; snapshotted amount/currency. Mutable: none (state lives in attempts/outbox). Lifecycle: `posting_type CHARGE|REVERSAL` with `reverses_posting_id` and `Σ(REVERSAL)≤CHARGE` trigger; INSERT trigger re-reads stay IN_HOUSE∧posting_allowed (except REVERSAL). Idempotency: `idempotency_key`. Locking: §5 posting order (pnumber_seq → outbox). Acceptance: E-series — pin-chain fuzz rejected at SQL; posting on non-IN_HOUSE aborts.

**`posting_outbox`** — Purpose: per-interface serialized delivery lane; one active row per posting. PK `id`. FKs: composite `(tenant,site,pms_interface_id,posting_id)` → posting anchor. Uniqueness: `UNIQUE(posting_id) WHERE state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY')`. Mutable: `state` (QUEUED→IN_FLIGHT→DONE|HELD_RECOVERY). Idempotency: one-active-row partial unique. Locking: per-interface lane lock. Acceptance: E8 — one active row; retries never change interface.

**`payment_transactions`** — Purpose: typed, append-only, merchant-scoped charges/refunds. PK `id`. FKs: composite → settlements; self-parent (same tenant/site/settlement); `merchant_account_id → stripe_accounts`. Uniqueness: `idempotency_key UNIQUE`, `UNIQUE (tenant, provider, merchant_account_id, provider_ref)`. Immutable: append-only; `ptx_parent` CHECK. Lifecycle: CHARGE `CREATED→…`; REFUND/CHARGEBACK child rows Σ≤parent. Idempotency: `idempotency_key`. Acceptance: E2 — idempotency-key race → one charge; cross-merchant parent rejected.

**`posting_attempts`** — Purpose: **immutable request identity + controlled one-way state** (contract §9a rule 2). PK `id`. FKs: composite → `pms_interfaces`, `pms_postings`. Uniqueness: `UNIQUE (tenant,site,pms_interface_id,p_number)`, `UNIQUE (internal_posting_id, attempt_no)`, `(tenant,site,id)`. Immutable: `p_number/rn/g_number/sent_at` (trigger-locked); Mutable: `outcome` (`SENDING→ACKED|UNKNOWN|FAILED`, one-way), `response_at`, `pa_as_status`. Lifecycle: no-PA-past-timeout ⇒ UNKNOWN (never auto-retried); approved retry = new attempt_no + new P#. Idempotency: `p_number` unique per interface. Locking: §5 posting order. Acceptance: 3C acceptance (phase 4) — UNKNOWN, no auto-retry, no auto second P#.

**`posting_attempt_events`** — Purpose: fully append-only attempt audit history. PK `id`. FKs: composite → `posting_attempts`. Immutable: INSERT-only (trigger rejects UPDATE/DELETE). Acceptance: every state change writes one event; no mutation possible.

**`posting_review_actions` / `financial_epoch` / `compliance_archives`** — Purpose: immutable manual-review decisions (§15, five actions, re-auth + reason + evidence); financial recovery epoch marker; pre-purge encrypted archive with SHA-256 manifest + verified receipt (§12). PK `id`/singleton. Immutable: append-only / epoch-advance-only. Acceptance: no generic approve action; purge blocked until verified receipt (phases 4/7).

### MG-8 — Resolution/audit aux

**`auth_resolutions`** — Purpose: STRICT multi-PMS resolution outcomes (codes only, never guest data). PK `id`. FKs: composite → guest_network + nullable resolved stay. Immutable: append-only outcome codes. Acceptance: D-series (phase 3) — uniform time-padded non-success; never stores guest data.

### MG-9 — Engine components (not tables)

- **Immutability/append-only/one-way triggers** for every `*_revisions`, generational, ledger, attempt, event, and adjustment table (per §3).
- **Entitlement-engine functions:** grant-inside-purchase-GRANTED; atomic same-subject supersession (rebind sessions with zero nft churn — dark in 1A, no live sessions); window stamping; counter accrual from watermarks; terminal-transition guard.
- **Lock-order library** (§5): the single canonical acquisition order + helper wrappers; every engine path uses it.
- **Watermark ingestion:** idempotent sample apply (delta once; epoch-reset detection).

---

## 5. Lock-order library (canonical acquisition order)

Every transaction that takes more than one lock acquires them **in this order** (release at COMMIT). This is the deadlock-freedom guarantee.

1. **Subject/identity advisory lock** — `pg_advisory_xact_lock(hashtextextended(subject_key, SALT_SUBJECT))` (for same-subject supersession / one-live-entitlement atomicity).
2. **Per-credential device-slot advisory lock** — `SALT_DEVICE` (acquired **before** capacity; matches the shipped `reserveDeviceSlot` ordering).
3. **Appliance capacity advisory lock** — `SALT_CAPACITY`.
4. **Row locks, in FK-topological order:** `offer_quotes` CAS → `auth_contexts` CAS → `entitlements` (superseded row, then new) → `pms_interface_pnumber_seq` row (P# allocation) → `posting_outbox` row.

Salts are centralized as named constants in the lock-order library (exact numeric values inherited from the shipped implementation — the device/capacity advisory-lock salts already live in production `reserveDeviceSlot` in `data-plane/internal/session/session.go`). **Open decision:** confirm final salt constants and whether the subject lock reuses an existing salt (Risks §9).

---

## 6. Existing IAM tables/code: replace / retain / migrate / remove

Phase 1A is **clean-slate in the standby DB**; nothing in the live DB is mutated in 1A. The disposition below governs the 1B cutover, and is stated now so the plan avoids a hybrid.

| Existing artifact | Disposition | Notes |
|---|---|---|
| `vouchers`, `ticket_templates`, voucher accrual/window logic (`data-plane/internal/voucher`) | **Replace** | New `vouchers` + `internet_package_revisions` supersede templates; the deployed validity-window voucher model already matches contract semantics. Old table retired at 1B cutover. |
| `guest_access_accounts` (current) | **Migrate** | Schema-compatible; usernames already unique on `lower(username)`; argon2id hashes carried forward at 1B. |
| `sessions`, `accounting_records` (current acctd/scd) | **Replace** | New entitlement-scoped sessions + watermark model; disposable live test sessions are reset at 1B (not migrated). |
| `reserveDeviceSlot`, capacity/device advisory locks (`session.go`) | **Retain (absorb)** | Lock semantics/salts are lifted into the Phase-1A lock-order library unchanged. |
| Max-devices / plan-edit / rate-limit logic | **Retain (re-home)** | Behavior preserved; re-expressed against new plan/package revisions. |
| PMS lookup connector (`data-plane/internal/pms/protel_fias.go`) | **Retain, extend later** | Lookup-only today; posting engine is a **new** component in phase 4. Verified FIAS startup/single-slot/cleanup findings (contract §9b) become connector requirements. |
| Portal/edged/scd/acctd services | **Retain, re-point at 1B** | 1A adds no service code path to the new schema (dark). |

**Removed:** nothing in 1A. Old IAM tables are dropped only after the 1B blue/green cutover proves green (their drop is a separate, later migration).

---

## 7. Untouched foundations (explicitly out of scope, unchanged)

Appliance enrollment, hardware-bound identity, PKI/mTLS, signed licensing and entitlement counting, Central Control Plane boundaries, WAN/LAN configuration and netplan, guest VLANs, DHCP/DNS, captive-portal network interception, nftables/traffic-control foundations, backup/restore retention tooling, updates, remote support, and audit infrastructure. Phase 1A touches **only** the site DB IAM schema in the standby database and dark engine code. (Cross-refs: network/topology, licensing, PKI subsystems remain as-is.)

---

## 8. Disposable test-data handling & anti-hybrid strategy

- **Test data:** all disposable acceptance fixtures (test vouchers, `opadmin@test.local`, `guest1`, throwaway plans/sessions) are treated as **compromised** and are **not** migrated; the 1B cutover performs a **controlled reset** of disposable test data (contract §18 1B) in the live DB before re-pointing services. 1A itself creates fixtures only in the standby DB.
- **No hybrid:** 1A builds the **entire** schema in `stayconnect_site_b` and keeps it dark; services never read/write both old and new models simultaneously. The switch is a single **blue/green promotion** at 1B, not a table-by-table dual-write. If acceptance fails, swap-back leaves the live DB untouched. This is why the full persistence foundation is created in 1A even though most behavior activates later — the database never exists in a half-migrated state.

---

## 9. Risks & open decisions (product-owner review)

1. **Lock-salt constants** — confirm the final centralized salt values and whether the new subject-lock reuses or adds a salt (must not collide with the shipped capacity/device salts).
2. **Standby DB divergence** — `stayconnect_site_b` is currently the streaming standby; building a clean-slate schema there requires either detaching it or using a separate scratch database for 1A. **Decision needed:** dedicated 1A build database vs. temporarily detached standby.
3. **`time_accounting_mode`** — v1 implements `VALIDITY_WINDOW` only; `AGGREGATE_ONLINE_TIME` schema-present but inert until phase 6. Confirm acceptable.
4. **Central template linkage** — `internet_packages.central_template_id` implies a Central→appliance package template flow; confirm whether any Central-side schema is in 1A scope (proposed: no — appliance-local only in 1A).
5. **Folio-identity strategy defaults per interface** — Hotel ID 3 measured behavior should seed `folio_identity_strategy`; confirm the default (`GLOBALLY_UNIQUE`) pending per-property onboarding.
6. **Merchant accounts** — `payment_transactions.merchant_account_id → stripe_accounts` assumes the existing `stripe_accounts` table; confirm it exposes the `(tenant_id, id)` anchor (MG-0).
7. **Reversal capability** — remains `capability=false` (contract §9d); the `pms_postings` REVERSAL path and `Σ(REVERSAL)≤CHARGE` trigger are built but not exercised until a separate reversal capability spike.

---

## 10. Phase 1A acceptance tests (A-series; dark, standby DB only)

Mapped to contract §19 A-series (engine) plus schema-integrity checks:

- **Schema integrity:** every append-only/immutable/one-way trigger rejects UPDATE/DELETE (revisions, generations, postings, attempts, events, adjustments); every composite FK rejects cross-namespace rows.
- **A1** shared immovable window across devices · **A2** device over-limit REJECT + surface · **A3** same-device reconnect replaces session, no slot burn · **A4** duplicate/concurrent closes charge once (watermarks) · **A5** aggregate data cap → one atomic terminal transition, all sessions revoked once · **A6** SIGKILL/restart/reboot durability; re-auth gets remainder only · **A7** no exit from TERMINATED · **A8** supersession rebind with zero churn; cross-subject supersession rejected · **A9** suspension revokes sessions, window keeps running · **A10** late samples ledgered, never reopen · **A11** capacity counts distinct devices; capacity failure leaves zero session/device/binding rows · **A12** counter-reset epoch handling · **A13** reconciliation rebuild; decreases only via audited adjustment.
- **One-live-entitlement:** each partial unique index (`ent_live_stay|account|voucher|principal+site`) rejects a second live grant under concurrency.
- **Lock-order:** static check that every multi-lock path uses the library; a deliberate reversed-order test is rejected by the wrapper.

All A-series run against the **standby DB**, dark, with **no user-visible change** and **blue/green swap-back** as the rollback.

---

**End of Phase 1A plan (DRAFT).** Awaiting product-owner approval before any implementation. No migrations, code, providers, services, config, deployment, or PMS traffic are authorized by this document.
