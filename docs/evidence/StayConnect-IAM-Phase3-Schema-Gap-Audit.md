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
- post-0010 = `651cf7cba32c0cd9a482a966a50c075a`
- after rollback == pre-0010 (verified); after reapply == post-0010 (verified).

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

## Verification (`iam_v2_scratch/phase3_0010_lifecycle.sh`) — 29/29 PASS

apply 0010 · raw re-apply errors "already exists" (ledger ⇒ no-op) · catalog stable · all expected objects (runtime table, `p3_stay_event_guard` + `p3_stay_lifecycle_guard`, 6 stays columns, 7 grace columns, `resolution_request_id`, **no** `stays.checkout_episode`) · FK rejects unknown interface · RESERVED→IN_HOUSE→CHECKED_OUT allowed, backward rejected, event-version monotonic, reinstatement requires `lifecycle_version++` **and** cleared `effective_checkout_at`, lifecycle_version cannot jump >1 · stay_events identity immutable, DELETE rejected, PENDING→APPLIED allowed, terminal status immutable · grace bounds enforced · rollback catalog == pre-0010 · reapply catalog == post-0010. **No Production database was accessed;** all evidence is from the disposable PG16 container, destroyed after the run.
