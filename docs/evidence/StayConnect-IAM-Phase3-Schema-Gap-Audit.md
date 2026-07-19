# StayConnect IAM — Phase 3 Schema Gap Audit (machine-grounded)

> `PHASE_3_SCHEMA_GAP_AUDIT: MACHINE_GROUNDED` · `PHASE_3_MIGRATION: 0010_phase3_stay_resolution`

**Authorization:** D14 / T0015. **Increment 2.** This audit is generated from the **actual accepted schema built in a disposable PostgreSQL** — not from a narrative-only agent audit. It drives the exact scope of migration `0010`.

## Method (reproducible)

1. Disposable container `iamv2-scratch` = `postgres:16-alpine` (**PostgreSQL 16.14**), published only on `127.0.0.1:55432`, DB `iam_scratch`, marker-guarded by `iam_v2_scratch/lib.sh` (refuses every non-disposable target).
2. Accepted iam_v2 schema built via `iam_v2_scratch/run.sh fresh` (platform fixture + MG-0 anchor + `mg1..mg9`), then Phase-2 `data-plane/migrations/0009_phase2_commerce.up.sql` applied → the exact **pre-0010** accepted baseline (49 iam_v2 tables + Phase-2 triggers `purchase_quote_pin_equal`, `offer_quote_immutable`).
3. Catalog queried directly (`information_schema` + `pg_catalog`) for every Phase-3 requirement.
4. Migration `0010` designed to the gaps, then verified by `iam_v2_scratch/phase3_0010_lifecycle.sh`.

**Catalog fingerprints** (md5 over iam_v2 columns+triggers+indexes+constraints):
- pre-0010 = `ead4a4de465f9a8b23a604ac52ff8622`
- post-0010 (final hardened design, incl. Part-A secret-generation pin + event-id immutability) = `b67b581189701193909649044bf5aa6c`
- after rollback == pre-0010 (verified); after reapply == post-0010 (verified).

**Final hardened design (Increment-2 corrections):** episode = `stays.lifecycle_version` (strict counter — changes ONLY on a CHECKED_OUT→IN_HOUSE reinstatement); freshness = four independent axes with **no stored `derived_freshness`** (removed; the resolver derives availability from the axes + revision thresholds); `stay_events` gains `processed_at`+`review_code` and a full lineage guard (`stay_id` may go NULL→same-interface Stay only in the tx that makes the event terminal; terminal rows never repointed/cleared); occupancy evidence is composite-FK-pinned to the SAME interface's (possibly historical) revision, all-or-none, `normalization_version>0`; grace quota is **bytes** (`grace_data_quota_bytes bigint`) and `grace_device_limit_policy` reuses the canonical `service_plan_revisions.device_limit_policy` vocabulary `('REJECT_NEW_DEVICE','DISCONNECT_OLDEST','ADMIN_APPROVAL')`; the typed grace columns are the authoritative policy (the pre-existing `config jsonb` is NOT a second source of truth for duration/rates/quota/device-limit/policy/eligibility). Post-Stay (`POST_STAY_ACTIVE`) has NO executable transition (Phase 5). The two triggers are **structural state-machine guards ONLY** — a raw `status='IN_HOUSE' + lifecycle_version+1` update is NOT proof of a trusted source; the authorization boundary (trusted normalized PMS event / privileged Hotel-Admin Reinstatement with RBAC+step-up+reason+audit+version-check) is Increment 4. Trigger functions are SECURITY INVOKER with `EXECUTE` revoked from PUBLIC; no runtime grants (dark).

## Gap matrix (live catalog evidence)

| # | Phase-3 requirement | Exists pre-0010? | Live catalog proof | Source | Gap | 0010 action | Rollback |
|---|---|---|---|---|---|---|---|
| 1 | PMS Interfaces / Revisions / Secrets | YES | `pms_interfaces`, `pms_interface_revisions` (immutable `imm_pms_rev`), `pms_interface_secret_generations` (`sg_guard`) present | mg1, mg9 | none | — | — |
| 2 | Guest-network routing | YES | `guest_network_pms_map` + partial-unique `gnpm_one_default` present | mg1 | none | — | — |
| 3 | Stays / Guests / Folios / Stay-Folios | YES | `stays`, `stay_guests` (one-primary partial-unique), `folios` (open-identity), `stay_folios` present | mg4 | none (base) | — | — |
| 4 | Stay Events identity/idempotency | YES | `stay_events(external_event_identity UNIQUE, sequence_version, normalization_version, clock_suspect, processing_status)` | mg4 | none (base) | — | — |
| 5 | Stay Events **append-only** | **NO** | `pg_trigger` on `iam_v2.stay_events` = **(none)** | — | **GAP** | trigger `p3_stay_event_guard` (immutable identity/normalization; one-way terminal `processing_status`) | drop trigger+function |
| 6 | Stays **one-way status + monotonic versions** | **NO** | `pg_trigger` on `iam_v2.stays` = **(none)**; `status/lifecycle_version/last_applied_event_version` are plain columns | — | **GAP** | trigger `p3_stay_lifecycle_guard` (transition matrix; monotonic; reinstatement rule) | drop trigger+function |
| 7 | Checkout episode identity | YES (reuse) | `purchases.checkout_episode` + partial-unique `one_conversion_per_episode` present; `stays.checkout_episode` = **absent (by design)** | mg5 | none | **NO new column** — episode = `stays.lifecycle_version`; `purchases.checkout_episode` populated from the locked Stay's `lifecycle_version` | — |
| 8 | Auth Context Stay pin | YES | `auth_contexts.stay_id` present | mg5 | none | — | — |
| 9 | Auth Resolution idempotency | **NO** | `auth_resolutions(id,tenant_id,site_id,guest_network_id,resolved_stay_id,outcome_code,resolved_at)` — no request key | mg8 | **GAP** | add `resolution_request_id uuid` + partial-unique `auth_resolutions_req_idem`; Phase-3 writes must supply it | drop index+column |
| 10 | Entitlement / Session rebinding | YES | `entitlements.supersedes_entitlement_id`, 4 one-live partial-uniques, `ent_guard`; `sessions.entitlement_id`, `close_session()` | mg6 | none | — | — |
| 11 | Grace configuration (typed scalars) | **NO** | `site_checkout_grace_config(tenant_id,site_id,grace_package_revision_id,config jsonb)` — no scalar policy fields | mg2 | **GAP** | add `eligibility_window_seconds` (default 86400) + duration/rate/quota/device-policy columns + `grace_bounds` CHECK | drop constraint+columns |
| 12 | Effective-checkout boundary | **NO** | `stays` has no `effective_checkout_at` | mg4 | **GAP** | add `stays.effective_checkout_at` + `stays_effco_only_after_checkout` CHECK + index | drop index+constraint+column |
| 13 | Per-Stay occupancy-evidence freshness (axis 4) | PARTIAL | `stays.last_applied_event_version` + `stay_events` timestamps exist, but no per-Stay occupancy-evidence pin on `stays` | mg4 | **GAP** | add `stays.occupancy_evidence_at/ingested_at/revision_id/normalization_version/clock_suspect` | drop columns |
| 14 | PMS connector runtime freshness/cursor (axes 1–3) | **NO** | no `pms_interface_runtime`; `pms_interfaces` has only `lifecycle_state`+`current_revision_id` | — | **GAP** | new `pms_interface_runtime` table with **four independent axes** (transport / feed-continuity / complete-sync / + occupancy from #13) | drop table |
| 15 | Source conflicts | YES | `pms_source_conflicts` (CHECK a<b) present | mg1 | none | — | — |
| 16 | Alerts / audit structures | YES (reuse) | existing `audit_log` (edged) + connector alerts emitted at the app layer; source conflicts persisted | — | none | — | — |

## Decisions grounded by the evidence

- **Episode = `stays.lifecycle_version`.** The catalog shows `purchases.checkout_episode` + `one_conversion_per_episode` already exist and `stays.checkout_episode` does not; adding a second mutable counter is rejected (no ADR needed — the single-source design is simpler and the lifecycle test proves the one-conversion-per-episode uniqueness holds on `lifecycle_version`).
- **Freshness = four independent axes.** Axes 1–3 (transport/feed/sync) live on `pms_interface_runtime`; axis 4 (occupancy) is per-Stay on `stays`. A `derived_freshness` convenience enum exists but is not the stored replacement.
- **No unvalidated JSON blob** for runtime state — all axes are typed columns with CHECK-constrained enums.
- **Ownership / privileges:** 0010 objects inherit `iam_v2_owner` (as with 0009); no `SECURITY DEFINER`; zero runtime grants while dark.

## Verification (`iam_v2_scratch/phase3_0010_lifecycle.sh`) — 98/98 PASS

Part-A hardening additions (this pass):
- **Pinned Secret Generation (§1):** `pms_interface_runtime.pinned_secret_generation_id uuid` with a composite FK to `pms_interface_secret_generations(tenant,site,pms_interface_id,id)` (same tenant/site/interface). `CONNECTED` now requires **both** `pinned_revision_id` AND `pinned_secret_generation_id`. Cross-interface secret-gen pin rejected by the FK; a historical (disconnected) row may retain a now-superseded pin. Only the generation **identity** is stored — never ciphertext/nonce/key material.
- **Event id immutable (§2):** the stay_events identity guard now also freezes `NEW.id` (`id` mutation rejected). Full column classification below.
- **Atomic runner locking (§3):** two real concurrent runner processes against one fresh disposable DB — both exit 0, exactly one applies **under the advisory lock**, the other reports `SKIP_AFTER_LOCK`, exactly one ledger row, **no** `already exists`, catalog == expected post-fingerprint (no partial DDL). The ledger decision happens only under the lock (no pre-lock decision).
- **Target-identity fail-closed (§4):** `--expect-db` mismatch refused; absent `public.schema_migrations` refused (no silent create) unless the separate explicit `--bootstrap-ledger` mode is used.
- **Deployment-parity ownership (§5):** 0010 applied by a **NOSUPERUSER** `iam_v2_owner`; every new/altered iam_v2 relation and every `p3_*` function is owned by `iam_v2_owner`; no unexpected PUBLIC grants; migration needs no superuser.

### `stay_events` column classification (catalog-grounded, §2)
| column | class | mutability |
|---|---|---|
| `id` | immutable identity (PK) | frozen (Part-A §2) |
| `tenant_id`, `site_id`, `pms_interface_id` | immutable identity | frozen |
| `external_event_identity`, `event_type` | immutable identity | frozen |
| `pms_timestamp_raw`, `pms_timestamp_utc`, `source_timezone`, `received_at` | immutable normalized source evidence | frozen |
| `sequence_version`, `normalization_version`, `clock_suspect` | immutable normalized source evidence | frozen |
| `payload` | immutable normalized source evidence | frozen |
| `stay_id` | one-time result | NULL→same-interface Stay only in the tx that makes the row terminal; then frozen |
| `processing_status` | one-time terminal | PENDING→terminal exactly once; then frozen |
| `processed_at` | one-time result | NULL until terminal, set in that tx, then frozen |
| `review_code` | terminal immutable | set only for MANUAL_REVIEW/FAILED (bounded `^[A-Z][A-Z0-9_]{0,63}$`), then frozen |

No stay_events column is accidentally mutable: identity/source/payload are frozen on UPDATE, result columns move once (PENDING→terminal) and then freeze.

Final-invariant additions (prior pass):
- **Event append-first (INSERT):** direct-`APPLIED`/`FAILED` insert rejected; PENDING-with-`stay_id`/`processed_at`/`review_code` rejected; clean PENDING accepted. The base composite FK `stay_events(tenant,site,pms_interface_id,stay_id)→stays` already exists (mg4) — proven, not duplicated.
- **Event terminal results:** PENDING→APPLIED without `stay_id` or without `processed_at` rejected; cross-interface Stay rejected (FK + trigger); MANUAL_REVIEW/FAILED without `review_code` rejected; PII-shaped `review_code` rejected (`^[A-Z][A-Z0-9_]{0,63}$`); APPLIED-with-`review_code` rejected; valid same-interface APPLIED + `processed_at` accepted; MANUAL_REVIEW + bounded code accepted; terminal `stay_id`/`processed_at`/`review_code`/status all immutable.
- **Grace (§4/§5):** `DISCONNECT_OLDEST`/`ADMIN_APPROVAL` rejected (only `REJECT_NEW_DEVICE`); partial policy rejected (all-or-none); fully-configured (bytes) accepted; unconfigured accepted; `config` jsonb duplicate authoritative key rejected.
- **Runner scope (§6):** no `--only`/`--all` refused; invalid version-name refused; absent migration refused; SHA-256 printed on apply (advisory-lock concurrency guard).

Prior coverage (retained):

Self-contained gate (fresh disposable PG16 → accepted schema → gate → teardown):
- **Runner idempotency (`scripts/edge-migrate.sh --only 0010`, twice):** run#1 applies (ledger records 0010, `applied=1`); run#2 skips (`applied=0`); ledger has exactly **one** 0010 record; catalog identical between invocations.
- **Raw re-apply:** errors `already exists`; transaction rolls back; catalog unchanged.
- **Removals/units:** no stored `derived_freshness`, no derived-freshness index; `grace_data_quota_bytes` present, `grace_data_quota_mb` absent; no `stays.checkout_episode`; `stay_events.processed_at`+`review_code` present.
- **Privilege (§3):** PUBLIC has NO EXECUTE on either `p3_*` function; no non-owner grants on `pms_interface_runtime`; no `SECURITY DEFINER` on any `p3_*` function.
- **Runtime constraints (§8):** `runtime_generation>=0`; CONNECTED requires pinned revision; heartbeat ≤ updated_at; resync coherence; bounded error-code/cursor lengths; the four axes are independently settable (no contradictory stored-HEALTHY possible).
- **Occupancy (§6):** partial tuple rejected (all-or-none); `normalization_version>0`; cross-interface revision rejected (composite FK); full same-interface tuple allowed.
- **Lifecycle/status (§2/§4):** RESERVED→IN_HOUSE→CHECKED_OUT allowed; IN_HOUSE→IN_HOUSE + `lifecycle++` rejected; Room Move + `lifecycle++` rejected; Room Move (no version change) allowed; CHECKED_OUT→CHECKED_OUT + `lifecycle++` rejected; CHECKED_OUT→POST_STAY_ACTIVE and POST_STAY_ACTIVE→CHECKED_OUT rejected; reinstatement without `lifecycle++` rejected; reinstatement CHECKED_OUT→IN_HOUSE + `lifecycle++` allowed **(structural-only; trust is Increment 4)**.
- **Event lineage (§5):** identity immutable; DELETE rejected; PENDING `stay_id` set-without-terminal rejected; PENDING→APPLIED with a cross-interface Stay rejected; PENDING→APPLIED with a same-interface Stay + `processed_at` allowed; terminal `stay_id` substitution/`NULL` rejected; terminal status immutable.
- **Grace (§9):** non-canonical device policy rejected; `REJECT_NEW_DEVICE` + byte quota accepted; eligibility window >0.
- **Lifecycle:** rollback catalog == pre-0010; ledger 0010 removed on down; reapply catalog == first post-0010.

**No Production database or appliance was accessed;** all evidence is from the disposable PG16 container, destroyed after the run.
