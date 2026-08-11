# Phase 4 — Financial Schema-Gap Audit (machine-grounded, pre-0011)

**Measured, not assumed.** Every statement below was produced by rebuilding the authoritative chain in a
disposable PostgreSQL 16 container and querying `pg_catalog` / `information_schema`, then by *executing* the
forbidden writes. Catalog presence alone is never recorded as acceptance evidence.

**Audit baseline:** branch `phase/4-financial-execution`, commit `3ea775c`.
**Authorization:** D18 / T0029. Target maturity: DARK / NO-FINANCIAL-TRAFFIC.

---

## 1. Reproducible pre-0011 chain

```
postgres:16-alpine   (disposable, loopback 127.0.0.1:55432, db iam_scratch)
  public   0001_edge_init, 0002_edge_networking, 0005_appliance_service_health,
           0007_auth_throttle_buckets, 0008_otp_hmac_keys, 0009_phase2_commerce
           (the exact set present in the live ledger — 0003/0004/0006 are NOT applied on the site DB)
  MG-0     public.guest_networks_tsi_anchor  UNIQUE(tenant_id,site_id,id) CONCURRENTLY
  iam_v2   mg1_pms_interface_core, mg2_plans_packages, mg3_identities_credentials, mg4_stay_domain,
           mg5_auth_commerce, mg6_entitlements_devices_sessions, mg7_postings_payments,
           mg8_resolution_aux, mg9_engine                                            -> 49 tables
  public   0010_phase3_stay_resolution                                               -> 63 tables
```

**Result: 63 `iam_v2` tables — identical to Production.** Reproduced twice, deterministically.
Harness: `iam_v2_scratch/run.sh fresh` + `data-plane/migrations/0010_*.up.sql` + `iam_v2_scratch/seed.sql`.

## 2. Existing enforcement — measured catalog

| Table | Triggers | Key constraints / indexes |
|---|---|---|
| `pms_postings` | `ao_postings` → `trg_reject_update_delete`; `charge_gate` → `trg_posting_charge_gate` | UNIQUE `idempotency_key`; UNIQUE `(id,pms_interface_id)`, `(tenant,site,id)`, `(tenant,site,interface,id)`; CHECK `posting_type ∈ {CHARGE,REVERSAL}`; CHECK `posting_reversal_link`; composite FKs to purchases, settlements, folios, stays, revisions, secret generations |
| `posting_attempts` | `pa_oneway` → `trg_posting_attempt_oneway` | CHECK `outcome ∈ {SENDING,ACKED,UNKNOWN,FAILED}`; CHECK `pa_as_status ∈ {OK,NG,NA,NP,NR,RY,UR}`; UNIQUE `(tenant,site,interface,p_number)`; UNIQUE `(internal_posting_id,attempt_no)` |
| `posting_attempt_events` | `ao_pa_events` → `trg_reject_update_delete` | FK to attempts |
| `posting_review_actions` | `ao_review` → `trg_reject_update_delete` | CHECK `action ∈ {CONFIRM_POSTED, CONFIRM_NOT_POSTED_RETRY, CONFIRM_NOT_POSTED_ABANDON, CREATE_REVERSAL, ESCALATE}` |
| `posting_outbox` | — | partial UNIQUE `outbox_one_active (posting_id) WHERE state IN (QUEUED,IN_FLIGHT,HELD_RECOVERY)`; CHECK `state ∈ {QUEUED,IN_FLIGHT,DONE,HELD_RECOVERY}` |
| `pms_interface_pnumber_seq` | — | PK `(pms_interface_id)`, `next_p_number bigint NOT NULL DEFAULT 1`, FK to interface |
| `stays` (financial-relevant) | `p3_stay_lifecycle_guard` | CHECK `posting_only_in_house (posting_allowed=false OR status='IN_HOUSE')`; CHECK `stays_checkedout_needs_boundary` |

`charge_gate` body (measured): rejects a `CHARGE` when the pinned revision's `folio_identity_strategy` is
`NULL` or `'UNSET'` (`FOLIO_STRATEGY_UNSET`), and when the pinned stay is not `IN_HOUSE` or not
`posting_allowed` (`POSTING_NOT_ALLOWED`).

## 3. Behavioural proof — `iam_v2_scratch/phase4_db_invariants.sh`, **30/30**

Each check performs the forbidden write and asserts rejection, against a freshly rebuilt chain.

| C-row | Proven behaviourally |
|---|---|
| C1 | CHARGE accepted when all pinned objects are in scope; a folio outside the pinned tenant/site/interface is **rejected** |
| C5 | `UNSET` folio strategy **blocks** CHARGE (`FOLIO_STRATEGY_UNSET`) |
| C8 | duplicate `attempt_no` for one posting **rejected** |
| C9 | first `P#` accepted; duplicate `P#` within the same interface **rejected** |
| C12 | invented PA `AS` status **rejected** |
| C15 | `SENDING→UNKNOWN` allowed; `UNKNOWN→SENDING` **rejected**; DELETE **rejected** |
| C16 | attempt-event INSERT accepted; UPDATE and DELETE **rejected** |
| C17 | `CONFIRM_POSTED` accepted; generic `APPROVE` **rejected** |
| C19 | review-action UPDATE and DELETE **rejected** |
| C22 | second ACTIVE outbox row **rejected**; a `DONE` row may coexist |
| C26 | duplicate `idempotency_key` **rejected** |
| C32 | `posting_allowed=false` **blocks** CHARGE; a **genuinely** `CHECKED_OUT` stay (reached through the approved lifecycle transition) **blocks** CHARGE |
| C-AO1 | `pms_postings` UPDATE and DELETE **rejected** |
| P3-LC | checkout leaving `posting_allowed=true` rejected; checkout without `effective_checkout_at` rejected; approved checkout and approved reinstatement both succeed |
| GUARDS | every `iam_v2` trigger still ENABLED (0 disabled) — nothing was weakened to make the suite pass |

## 4. The three genuine DB gaps — re-confirmed by measurement

| # | Gap | Measured evidence | 0011 action |
|---|---|---|---|
| **G1** | **RN + G# not mandatory** | `posting_attempts.rn` `is_nullable=YES`; `g_number` `is_nullable=YES`; **0** CHECK constraints mention `g_number` | Add a CHECK requiring both non-empty on every attempt |
| **G2** | **No interface base currency exists at all** | **0** columns matching `%currenc%` on `pms_interfaces` or `pms_interface_revisions`; `config` jsonb carries no currency key; **0** CHECKs on `pms_postings` mention currency | Add `base_currency` + `base_currency_exponent` to `pms_interface_revisions` (revision-pinned and immutable, matching §9c Tier 2 "PMS Interface currency + exponent"), then enforce posting currency = pinned revision currency |
| **G3** | **No posting status projection** | `pms_postings` has **no** status/state column; **0** views named `%posting%` | Derive posting status from the attempt ledger — see §5 |

**G2 is larger than the Phase-4 Plan assumed.** The contract requires "package currency must equal the pinned
PMS Interface base currency", but the interface has **no currency to compare against**. Currency equality
cannot be enforced until the base currency is recorded on the revision.

## 5. Design note — G3 must not become a second writer

`pms_postings` is append-only and a posting's true state lives in its attempt ledger. Adding a mutable
`status` column would create a second writer for the same fact and a new way for it to disagree with the
attempts. The projection is therefore **derived**, not stored: a view over the latest attempt per posting
plus the outbox row, exposing PENDING / IN_FLIGHT / ACKED / UNKNOWN / FAILED / REVIEWED. This is confirmed
as a genuine gap (no such view exists) but it is a **read model**, not new mutable state.

## 6. C1–C38 — layered status

Legend: **DB-OK** `DB_ALREADY_PRESENT_AND_VERIFIED` · **DB-GAP** · **RT** `RUNTIME_GAP` · **TEST** `TEST_GAP`
· **UI** `OPERATOR_SURFACE_GAP` · **N/A** `NOT_APPLICABLE` · **PO** `BLOCKED_BY_PRODUCT_OWNER_DECISION`

No row is PASS overall: **the entire Go runtime is absent** (all seven financial tables have zero Go
references), so every requirement retains at least `RUNTIME_GAP`.

| # | Requirement | DB | Runtime | Test | Operator |
|---|---|---|---|---|---|
| C1 | Posting pinned to tenant/site/interface/revisions/secret gen/stay/folio/purchase/settlement/amount | **DB-OK** | RT | DB proven; RT TEST | UI |
| C2 | Never re-resolve pinned objects on retry | **DB-OK** (append-only + immutable attempt identity) | RT | TEST | — |
| C3 | Room number is evidence, never identity | **DB-OK** (no RN index/unique) | RT | TEST | UI |
| C4 | RN + G# verified before outbox/transmission | **DB-GAP (G1)** | RT | TEST | UI |
| C5 | `UNSET` strategy blocks charge before outbox/P#/wire | **DB-OK** | RT | DB proven; RT TEST | UI |
| C6 | Package currency = pinned interface currency | **DB-GAP (G2)** | RT | TEST | UI |
| C7 | ISO-4217 minor units, integer `TA`, no currency on wire | **DB-OK** (integer minor units) | RT | TEST | — |
| C8 | `P#` is a protocol-attempt reference, not idempotency | **DB-OK** | RT | DB proven | UI |
| C9 | `P#` unique per interface | **DB-OK** | RT | DB proven; concurrency TEST | — |
| C10 | `PA` matched by interface + `P#`, never RN | **DB-OK** (unique key exists) | RT | TEST | — |
| C11 | `PS` field order and fixed values | N/A (wire, not DB) | RT | TEST | — |
| C12 | `AS` catalog only | **DB-OK** | RT | DB proven | UI |
| C13 | Transmitted `PS` without `PA` ⇒ UNKNOWN | **DB-OK** (outcome enum + one-way) | RT | TEST | UI |
| C14 | UNKNOWN never auto-retried, no auto second `P#` | **DB-OK** (one-way; no path exists) | RT | TEST | UI |
| C15 | Immutable attempt identity + one-way outcome | **DB-OK** | RT | DB proven | — |
| C16 | Attempt events fully append-only | **DB-OK** | RT | DB proven | UI |
| C17 | Exact §15 review catalog, no generic approve | **DB-OK** | RT | DB proven | UI |
| C18 | `financial-review` write + step-up + reason + evidence | partial (reason/evidence NOT NULL) | RT | TEST | UI |
| C19 | Review actions immutable | **DB-OK** | RT | DB proven | — |
| C20 | `CONFIRM_NOT_POSTED_RETRY` requeues once, same key | **DB-OK** (`outbox_one_active` + idempotency) | RT | TEST | UI |
| C21 | Concurrent reviewers cannot both win | **DB-GAP** (no version column) | RT | TEST | UI |
| C22 | Per-interface lanes; no duplicate attempts | **DB-OK** | RT | concurrency TEST | UI |
| C23 | Interfaces are independent namespaces | **DB-OK** (all keys interface-scoped) | RT | TEST | UI |
| C24 | Interface lifecycle / decommission guard | partial | RT | TEST | UI |
| C25 | Programmatic reversal disabled | **N/A** — capability false in v1 by contract | N/A | N/A | N/A |
| C26 | No duplicate CHARGE/REFUND/callback | **DB-OK** (idempotency unique) | RT | DB proven (posting); payment TEST | UI |
| C27 | No cross-tenant merchant reuse | partial | RT | TEST | — |
| C28 | Server-pinned totals | **DB-OK** (amount on posting) | RT | TEST | — |
| C29 | Entitlement only via approved atomic grant | **DB-OK** (Phase 2) | RT | TEST | — |
| C30 | Restore ⇒ FINANCIAL_RECOVERY_MODE, HELD_RECOVERY | **DB-OK** (`financial_epoch`, outbox state) | RT | TEST | UI |
| C31 | Restore never auto-replays | **DB-OK** (state machine) | RT | TEST | — |
| C32 | Freshness / stay eligibility before charge | **DB-OK** (stay gate proven); freshness axes RT | RT | DB proven | UI |
| C33 | Metrics: queue depth, oldest age, UNKNOWN count, backlog | — | RT | TEST | UI |
| C34 | No PII/card/credentials/secrets in logs or evidence | — | RT | TEST | — |
| C35 | Compliance archive before cross-customer purge | **DB-OK** (`compliance_archives`) | RT | TEST | — |
| C36 | Per-property onboarding gates posting | **DB-OK** (per-revision strategy) | RT | TEST | UI |
| C37 | Flags OFF, no egress while DARK | — | RT | TEST | — |
| C38 | Real-financial acceptance (Tier-3 3C live) | — | — | — | **PO** |

**Totals:** DB already present and verified on **24** rows · genuine DB gaps on **4** (C4, C6, C21, plus the
C3/G3 projection) · `RUNTIME_GAP` on **36** · `NOT_APPLICABLE` **1** (C25) ·
`BLOCKED_BY_PRODUCT_OWNER_DECISION` **1** (C38).

## 7. Migration 0011 scope — additive only

0011 must contain **only** G1, G2, G3 and the C21 concurrency column. It must **not** create, replace,
rename or weaken anything in §2 — those constraints and triggers are present, behaviourally proven, and
carry Production data semantics already applied under 0010.
