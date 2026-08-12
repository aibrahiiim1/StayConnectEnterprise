# Phase 4 — Financial Schema-Gap Audit and Financial-Core Verification (machine-grounded)

**Measured, not assumed.** Every statement below was produced by rebuilding the authoritative chain in a
disposable PostgreSQL 16 container and querying `pg_catalog` / `information_schema`, then by *executing* the
forbidden writes. Catalog presence alone is never recorded as acceptance evidence.

**Original audit baseline:** branch `phase/4-financial-execution`, commit `3ea775c` (pre-0011 measurement).
**Revised twice:** after migration 0011 and the financial execution core landed, and again after the migration-0012 hardening pass, which CORRECTED five rows this document had marked RT-OK too early (see section 11). Sections 1-2
record the pre-0011 measurement unchanged; sections 3-10 record what closed the gaps and how it was verified.
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

## 3. Behavioural proof of the pre-0011 baseline — `iam_v2_scratch/phase4_db_invariants.sh`, **31/31**

Each check performs the forbidden write and asserts rejection, against a freshly rebuilt chain. The suite is
now run **twice** by the migration gate: once on the pre-0011 chain, and again on a clean rebuild carrying
0011 — so "0011 weakened nothing" is a measured result rather than a claim. Both runs are **31/31**.

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
| C32 | `posting_allowed=false` **blocks** CHARGE; a `CHECKED_OUT` stay **blocks** CHARGE |
| C-AO1 | `pms_postings` UPDATE and DELETE **rejected** |
| P3-LC | checkout leaving `posting_allowed=true` rejected; checkout without `effective_checkout_at` rejected |
| GUARDS | every `iam_v2` trigger still ENABLED (0 disabled) — nothing was weakened to make the suite pass |

### 3a. What the C32 database check does and does **not** prove

The C32 row reaches `CHECKED_OUT` with a direct SQL `UPDATE` that satisfies every structural rule
(`posting_only_in_house`, `stays_checkedout_needs_boundary`, the one-way lifecycle guard). That makes it a
sound **structural** proof: the stay genuinely is not `IN_HOUSE`, and `charge_gate` genuinely refuses the
CHARGE.

It is **not** evidence of a trusted Stay-domain transition. Migration 0010 states the separation explicitly:
the triggers are structural state-machine guards, and the *authorization* boundary — trusted normalized PMS
event application, or privileged Hotel-Admin reinstatement with RBAC, step-up, reason and immutable audit —
is a different mechanism that a raw `UPDATE` does not pass through.

The end-to-end half of that claim is therefore proved separately, in
`TestIntegrationPosting_CheckoutAfterQueueingStopsTheCharge`: a posting is authorized and queued, the stay
then checks out, and the running engine stops the charge with **no P# consumed, no attempt written and no
wire bytes produced**.

## 4. The measured DB gaps — and how 0011 closed them

| # | Gap (measured pre-0011) | 0011 |
|---|---|---|
| **G1** | `posting_attempts.rn` and `g_number` both `is_nullable=YES`; **0** CHECKs mentioned `g_number` | `attempt_rn_verified`, `attempt_gnumber_verified` (mandatory, non-blank, bounded) plus `attempt_rn_wire_safe`, `attempt_gnumber_wire_safe`, `attempt_pnumber_wire_safe` |
| **G2** | **0** columns matching `%currenc%` on `pms_interfaces` or `pms_interface_revisions`; the revision `config` jsonb carried no currency key; **0** CHECKs on `pms_postings` mentioned currency | `financial_base_currency` + `financial_base_currency_exponent` on the **immutable** `pms_interface_revisions`, plus the `p4_posting_currency_gate` trigger enforcing exact three-way equality |
| **G3** | `pms_postings` had **no** status/state column; **0** views named `%posting%` | the DERIVED view `iam_v2.posting_execution_state` |
| **C21** | no concurrency mechanism of any kind on the review path | `posting_review_state` + `record_posting_review_action()` (advisory lock → row lock → optimistic version → compatibility check) |

**G2 was larger than the Phase-4 Plan assumed.** The contract requires "package currency must equal the
pinned PMS Interface base currency", but the interface had **no currency to compare against**. Currency
equality could not be enforced at all until the base currency existed on the revision.

**Where G2 lives, and why.** The currency a property posts in is Tier-2 property configuration pinned for
the life of a revision — not guest input, not global site state. `pms_interface_revisions` is already fully
immutable (`imm_pms_rev`), so recording it there makes it historical evidence by construction: financial
onboarding means publishing a **new revision**, and every posting that pinned an older revision keeps
pointing at exactly the currency it was authorized under. `NULL` is the un-onboarded state and is
fail-closed. Nothing is defaulted, inferred or hardcoded.

## 5. G3 is a read model, and what it is actually for

`pms_postings` is append-only, and a posting's true state lives in its attempt ledger. A mutable `status`
column would create a second writer for the same fact and a new way for the two to disagree. The projection
is therefore **derived**: `execution_state` comes from the **single highest-numbered attempt**, which is
what makes its precedence unambiguous — there is never a tie to break and never a rule about which of two
states wins. Anything that cannot be derived that way is exposed as its **own** column (`outbox_state`,
`terminal_review_action`, `has_unknown_history`, `awaiting_manual_review`) rather than folded into a
composite.

There is deliberately **no generic `REVIEWED` state**. "Reviewed" would have to mean at least four different
financial situations — confirmed posted, confirmed not posted and abandoned, confirmed not posted and
requeued, reversal created — and an operator cannot act on a label that does not say which. The committed
terminal decision is exposed verbatim instead.

**Correction to the previous revision of this audit.** G3 was attributed to **C3**. That was wrong. C3 is
"Room Number is evidence, never identity", and it was already satisfied before 0011 — there is no unique
index, key or lookup path on `rn`, and 0011 adds none. G3 serves the requirements that need a *state* an
operator or a metric can read:

| Requirement | What G3 provides |
|---|---|
| C13 | `transmitted PS without PA ⇒ UNKNOWN` is readable as a state, not reconstructed per query |
| C20 | the retry decision and the attempt it authorized (`terminal_review_action`, `retry_authorized_attempt_no`) |
| C30 | the recovery posture (`outbox_state = HELD_RECOVERY`) |
| C33 | the operator/metrics surface: queue depth, oldest age, UNKNOWN count, backlog |

## 6. C21 is a concurrency invariant, not a column

A version column on its own is not evidence of anything: two concurrent writers can both read version 3,
both write version 4, and both believe they won. The mechanism is therefore layered, in this order:

1. `pg_advisory_xact_lock` on the posting — serializes every reviewer of that posting for the whole
   transaction, including the create-if-absent race on the state row itself. Different postings never contend.
2. `SELECT … FOR UPDATE` on `posting_review_state` — the row lock, held to commit.
3. an optimistic `expected_version` check — so a reviewer acting on a stale screen is refused even when the
   two decisions do not overlap in time.
4. a compatibility check — a second, *different* terminal decision is refused outright.

`posting_review_actions` remains **fully append-only** and is still the authoritative history; the new row
carries only the decision pointer and the version. A direct INSERT into the ledger is refused by
`p4_review_writer_only`, so the concurrency mechanism cannot be walked around — that guard holds even for
the schema owner, which is what makes it testable at all in a disposable database where everything runs as
one role.

**Proved under real concurrency, twice.** In the DB gate, reviewer A holds its transaction open while
reviewer B blocks and is then refused with `REVIEW_CONFLICT`. In the Go matrix, four goroutines submit four
mutually incompatible terminal decisions simultaneously; exactly one commits, the other three are refused as
conflicts, and the append-only ledger holds exactly one action.

## 7. C1–C38 — layered status after 0011 and the financial core

Legend: **DB-OK** `DB_PRESENT_AND_BEHAVIOURALLY_VERIFIED` · **RT-OK** `RUNTIME_IMPLEMENTED_AND_TESTED_DARK`
· **RT** `RUNTIME_GAP` · **UI** `OPERATOR_SURFACE_GAP` · **N/A** `NOT_APPLICABLE`
· **PO** `BLOCKED_BY_PRODUCT_OWNER_DECISION`

**RT-OK means implemented and verified DARK.** It does not mean live, deployed or accepted: no Phase-4 flag
is enabled anywhere, and no row in this table has been exercised against a real PMS or payment provider.

| # | Requirement | DB | Runtime | Operator |
|---|---|---|---|---|
| C1 | Posting pinned to tenant/site/interface/revisions/secret gen/stay/folio/purchase/settlement/amount | **DB-OK** | **RT-OK** | UI |
| C2 | Never re-resolve pinned objects on retry | **DB-OK** | **RT-OK** | — |
| C3 | Room number is evidence, never identity | **DB-OK** | **RT-OK** | UI |
| C4 | RN + G# verified before outbox/transmission | **DB-OK (G1)** | **RT-OK** | UI |
| C5 | `UNSET` strategy blocks charge before outbox/P#/wire | **DB-OK** | **RT-OK** | UI |
| C6 | Package currency = pinned interface currency | **DB-OK (G2)** | **RT-OK** | UI |
| C7 | ISO-4217 minor units, integer `TA`, no currency on wire | **DB-OK** | **RT-OK** | — |
| C8 | `P#` is a protocol-attempt reference, not idempotency | **DB-OK** | **RT-OK** | UI |
| C9 | `P#` unique per interface | **DB-OK** | **RT-OK** | — |
| C10 | `PA` matched by interface + `P#`, never RN | **DB-OK** | **RT-OK** *(0012: the core verifies the answer against the ALLOCATED `P#`; parsing alone was not correlation)* | — |
| C11 | `PS` field order and fixed values, `CT` ≤ 20, exponent 2 | N/A (wire) | **RT-OK** *(0012: `CT` was bounded at 32, and the FIAS path is exponent-2-only)* | — |
| C12 | `AS` catalog only | **DB-OK** | **RT-OK** | UI |
| C13 | Transmitted `PS` without `PA` ⇒ UNKNOWN | **DB-OK (G3)** | **RT-OK** | UI |
| C14 | UNKNOWN never auto-retried, no auto second `P#` | **DB-OK** | **RT-OK** | UI |
| C15 | Immutable attempt identity + one-way outcome | **DB-OK** | **RT-OK** | — |
| C16 | Attempt events fully append-only | **DB-OK** | **RT-OK** | UI |
| C17 | Exact §15 review catalog, no generic approve | **DB-OK** | **RT-OK** | UI |
| C18 | `financial-review` write + step-up + reason + evidence | partial *(0012: evidence now mandatory; actor/reason enforced)* | **RT** — step-up, RBAC and operator binding NOT implemented | UI |
| C19 | Review actions immutable | **DB-OK** | **RT-OK** | — |
| C20 | `CONFIRM_NOT_POSTED_RETRY` requeues once, same key | **DB-OK** *(0012: the authorization is now CONSUMED, and an ACKed-OK charge can never be retried)* | **RT-OK** | UI |
| C21 | Concurrent reviewers cannot both win | **DB-OK (C21)** | **RT-OK** | UI |
| C22 | Per-interface SERIALIZED lanes; no duplicate attempts | **DB-OK** *(0012 `outbox_one_inflight_per_interface`)* | **RT-OK** *(0012: 0011 only proved duplicate-claim protection on ONE posting)* | UI |
| C23 | Interfaces are independent namespaces | **DB-OK** | **RT-OK** | UI |
| C24 | Interface lifecycle / decommission guard | **DB-OK** *(0012)* | **RT-OK** *(0012: refusing every non-ACTIVE state was wrong; AUTH_DISABLED posts, DRAINING drains)* | UI |
| C25 | Programmatic reversal disabled | **DB-OK** *(0012 makes it structurally impossible, not merely absent)* | **RT-OK** | N/A |
| C26 | No duplicate CHARGE/REFUND/callback | **DB-OK** | **RT-OK** (posting); RT (payment) | UI |
| C27 | No cross-tenant merchant reuse | partial | RT | — |
| C28 | Server-pinned totals | **DB-OK** | **RT-OK** | — |
| C29 | Entitlement only via approved atomic grant | **DB-OK** (Phase 2) | RT | — |
| C30 | Restore ⇒ FINANCIAL_RECOVERY_MODE, HELD_RECOVERY | **DB-OK (G3)** | RT (HELD_RECOVERY reached; recovery mode pending) | UI |
| C31 | Restore never auto-replays | **DB-OK** | RT | — |
| C32 | Freshness / stay eligibility before charge | **DB-OK** *(0012: all four runtime axes)* | **RT-OK** *(0012: only the stay half existed; the four axes were never consulted)* | UI |
| C33 | Metrics: queue depth, oldest age, UNKNOWN count, backlog | **DB-OK (G3)** | RT | UI |
| C34 | No PII/card/credentials/secrets in logs or evidence | — | **RT-OK** | — |
| C35 | Compliance archive before cross-customer purge | **DB-OK** | RT | — |
| C36 | Per-property onboarding gates posting | **DB-OK (G2)** | **RT-OK** | UI |
| C37 | Flags OFF, no egress while DARK | — | **RT-OK** | — |
| C38 | Real-financial acceptance (Contract §9c Tier-3 3C) | — | — | **PO** |

**Totals after the 0012 hardening pass.** Database enforcement present and behaviourally verified: **34**
rows. Genuine DB gaps remaining: **0**. Runtime implemented and verified DARK: **28** rows, five of which
(C10, C11, C22, C24, C32) reached that status only after 0012 — see section 11. Runtime still open: **9**
(C18 step-up/RBAC/operator binding, C27, C29, C30 recovery mode, C31, C33 metrics surface, C35, and the
payment half of C26). `BLOCKED_BY_PRODUCT_OWNER_DECISION`: **1** (C38). Operator-surface gap: **21** rows.

**RT-OK still means implemented and verified DARK by LOCAL disposable runs.** No authoritative CI run
exists for the hardened HEAD yet, and the race detector cannot run on the development workstation at all.

## 8. Verification of the delivered core

| Gate | Result |
|---|---|
| `iam_v2_scratch/phase4_0011_financial.sh` | **80 PASS / 0 FAIL** — 0011 UP, raw re-apply errors and rolls back, DOWN returns the catalog byte-identical to pre-0011, DOWN→UP reproduces the first UP, and every 0011 invariant is exercised behaviourally |
| pre-0011 invariant suite, pre-0011 chain | **31 / 31** |
| pre-0011 invariant suite, after 0011 (clean rebuild) | **31 / 31** — nothing that existed before 0011 was weakened |
| `go test ./internal/posting/` | **61 PASS / 0 FAIL** |
| `go test -tags integration -run IntegrationPosting ./internal/posting/` | **32 PASS / 0 FAIL** against disposable PostgreSQL 16 |
| `scripts/ci/phase4-dark-check.sh` | **PASS (6/6)** |

**P# under contention:** 8 concurrent clients × 25 allocations on one interface produce 200 distinct values
forming one gapless range, while a second interface's sequence is untouched. A rolled-back allocation gives
its number back, proving the allocator is transactional rather than a clock or a counter file.

**Positive no-egress evidence.** "The tables stayed empty" is not evidence. The DARK test runs a real worker
against a real queued posting, five times, with an inner transport that *would* accept a send. It asserts
that the worker claimed the work, re-verified the evidence, built a complete valid PS, and was refused at
the wire — five recorded refusals — while the inner transport was reached **zero** times, **no** P# was
consumed, **no** attempt row was written, and the work stayed `QUEUED` rather than being lost.

## 9. Scope discipline

0011 contains **only** G1, G2, G3, C21, the P# allocator and the structural no-blind-retry gate. It does not
create, replace, rename or weaken anything in §2: `charge_gate`, `pa_oneway`, `ao_postings`, `ao_review`,
`ao_pa_events`, `trg_secret_gen_guard`, `outbox_one_active` and the `idempotency_key` uniqueness are all
untouched, and the gate asserts that each is still present, still enabled, and — for `charge_gate` — that
its body is unchanged. The new currency trigger is named so that it fires **after** `charge_gate`, so the
fail-closed reason an operator sees for an un-onboarded interface is exactly what it was before.

## 10. Where 0011 has been applied

Disposable PostgreSQL 16 containers only, created and destroyed on loopback by the gate scripts.
**`0010_phase3_stay_resolution` remains the latest migration applied in Production and on the appliance.**
No Phase-4 flag is enabled anywhere, no real PMS `PS` has been transmitted, no `PA` has been accepted, no
guest folio has been debited and no payment provider has been called.

## 11. The 0012 hardening pass — what section 7 claimed too early

A hardening review of the same code found five rows this document had marked `RT-OK`, and one design
property it had described as structural, that did not survive being looked at properly. They are listed
here rather than quietly corrected in the table above, because a status that was wrong once is worth being
able to find later.

| # | What was claimed | What was actually true | What 0012 does |
|---|---|---|---|
| C22 | per-interface lanes | `outbox_one_active` stops two ACTIVE rows for the SAME posting. Two DIFFERENT postings could be in flight on one interface at once. The test raced six workers over ONE posting, so it could only ever have proved duplicate-claim protection | partial unique `outbox_one_inflight_per_interface`; the test now queues six different postings and MEASURES observed peak concurrency per lane |
| C24 | lifecycle guard | the gate refused every non-`ACTIVE` state, which is not the contract — `AUTH_DISABLED` posts, `DRAINING` drains | `Gate.CheckFor(Purpose)` plus DB triggers implementing §10 exactly, and the contractual zero-pending precondition for `DECOMMISSIONED` |
| C32 | freshness before charge | only the STAY half existed. The four PMS runtime axes were never consulted | `p4_interface_freshness_block` over the SAME Phase-3 runtime state and thresholds, enforced at creation AND at transmission |
| C11 | wire contract | `CT` was bounded at 32; §9a says 20. The exponent was generalized to 0..4 where §9a and the Gate-3A evidence fix it at 2 | `maxCTLen` 20; exponent-2 enforced for `protel-fias` only, so the currency MODEL keeps its real ISO range |
| C10 | PA correlation | `ParsePA` correlated when PARSING, but the engine accepted whatever the transport returned. A valid OK answer for another `P#` would have ACKed the wrong attempt | `SendPS` takes the allocated `P#`; guard and engine both verify; a mismatch never ACKs and becomes UNKNOWN |
| — | "the DARK guard is the chokepoint" | true only by convention — `Engine` and `DarkGuard` had EXPORTED fields, so a caller could build an unwrapped engine | every field unexported; `NewEngine` is the only constructor and always wraps; the CI check asserts both |

Two further defects in the same review: the retry authorization was never consumed, and
`CONFIRM_NOT_POSTED_RETRY` could be recorded against a charge the PMS had already ACKed `OK` — the exact
duplicate debit the UNKNOWN design exists to prevent. Both are now refused, and terminal review actions
require real evidence rather than the `{}` default.
