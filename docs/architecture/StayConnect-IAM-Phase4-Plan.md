# StayConnect IAM — Phase 4 Plan: Financial Execution Layer (DARK)

**Status:** **ACCEPTED AND CLOSED — VERIFIED LIVE-DARK / NO-FINANCIAL-TRAFFIC** (Product-Owner decision **D19**, closure transition **T0044**, 2026-08-13). Accepted software candidate `b94112d8cb0ab63938b60f829ddd465c14491f97`; accepted live evidence **T0043**. The Phase-4 pull request **PR #12 was MERGED** to `master` on 2026-08-14 under the separate Product-Owner merge decision **D20** (transition **T0048**), merge commit `210154b5ba72178bae715e7c8e4a1398ca629257`; the merge introduced no content, deployed nothing and enabled nothing. Originally AUTHORIZED under Product-Owner decision **D18**, transition **T0029** (2026-08-11); implementation progress recorded in transition **T0030** (2026-08-12) under the SAME authorization — no new decision was created.
**Delivered so far:** WS-A (migrations **0011 through 0026**), WS-B, WS-C, WS-D, WS-E, WS-F, WS-G, WS-H, WS-I, WS-J and WS-K — the financial execution core, the Go payment domain and settlement boundary, the one entitlement grant kernel, FINANCIAL_RECOVERY_MODE, observability, and the complete operator surface (financial health, Manual Review, settlements, recovery including the zero-attempt path). All verified DARK against disposable PostgreSQL 16 and gated by `.github/workflows/phase4-financial-core.yml`.
**Work-stream status at closure:** WS-F/WS-J are DELIVERED (financial health, recovery, Manual Review, settlements). WS-G, WS-H and WS-I are complete in DARK with the restricted-role trust boundary closed, provider-outcome authority split from execution authority, one shared entitlement grant kernel, and the supported restore-generation model verified by a real pg_dump/pg_restore drill. **WS-L (the authorized controlled live-DARK deployment and the reboot/rollback drill) is EXECUTED** (T0043, 2026-08-13): migrations 0011-0026 applied to the development appliance's `iam_v2` (68 tables, 0 rows), five NOLOGIN restricted roles created with no Phase-4 login role and no Phase-4 DSN, the Phase-4 binaries and Hotel-Admin bundle deployed, an authorized reboot survived with every criterion persisted, the safe portions of the backup/recovery procedure exercised, and the documented rollback rehearsed and reversed. Every Phase-4 flag stayed OFF, every Phase-4 route returns 404, and zero financial egress occurred. **Phase 4 is ACCEPTED AND CLOSED at verified LIVE-DARK / NO-FINANCIAL-TRAFFIC maturity** (Product-Owner decision **D19**, closure transition **T0044**, 2026-08-13). **The Next.js production advisory blocker is CLOSED** (T0041): the minimum patched state was `next@15.5.21`, not a framework major — the earlier "Next 16 is the only fix" reading came from npm's `fixAvailable` rather than from the advisory ranges. `next@15.5.23` keeps React at 18.3.1 and both dependency trees now report **zero** advisories. **One external blocker remains: C35's archival receipt authority, which does not exist in this product** — the gate fails closed on it, so cross-customer purge is unavailable rather than performed on self-certified evidence.
**Accepted and closed at LIVE-DARK maturity; NOT ENABLED.** Every Phase-4 flag is OFF and no real financial traffic has ever occurred. Acceptance authorizes no flag enablement, no IAM-v2 cutover, no Production migration or database contact and no real financial traffic. The merge itself was authorized SEPARATELY: PR #12 was MERGED to master on 2026-08-14 under Product-Owner decision **D20** (transition **T0048**), merge commit `210154b5ba72178bae715e7c8e4a1398ca629257` — a delivery event that introduced no content, deployed nothing and enabled nothing.
**Verification status:** AUTHORITATIVE CI EXISTS AND IS GREEN. `Phase 4 Financial Core CI` runs on every
push to this branch and has passed on the delivered heads — see `phase4_authoritative_ci_*` in
`governance/project-state.json` for the run id, head and artifact id of the latest. It covers gofmt, build,
vet, the unit matrix, the **race detector** (which cannot run on the Windows development workstation),
migrations 0011 through 0026 with the full DB gate, the PG16 integration matrix, the Hotel-Admin unit and
browser suites, the restore drill, the dependency gate and its self-test, and the DARK assertion.
**Baseline:** master `a4e951972d8087f00a40d8b39eb1b87ea03144b6`; accepted Phase-3 runtime `7c8b8cf0…`.
**Maturity target:** IMPLEMENTED + VERIFIED AT DARK / NO-FINANCIAL-TRAFFIC. Real-financial acceptance
(Tier-3 3C live) is **out of scope** and requires separate explicit Product-Owner authorization.

Every requirement below is derived from the FINAL Phase-0 Contract — §4.5, §9, §9a, §9c Tier-3 3C, §10,
§11, §12, §13, §14, §15 — and from the verified Gate 3A production evidence. **Nothing here invents
protocol or provider behaviour.**

---

## 1. The one principle

> Money must never be guessed, silently duplicated, blindly retried, silently converted between
> currencies, or detached from the exact authenticated Stay/Purchase/Settlement evidence that authorized it.

Every design choice below is downstream of that sentence.

## 2. What already exists (measured, not assumed)

- **Schema: complete.** All ten financial tables exist in `iam_v2` from migration 0010 and are empty.
- **FIAS wire: exists.** Framing plus `LS`/`LD`/`LR`/`LA`/`DR` in `internal/pms/fias_wire.go`,
  `protel_fias.go`.
- **Commerce: exists.** Quotes, purchases, settlement mappings, atomic entitlement grant (Phase 2).
- **Stay/Folio: exists.** Stays, folios, STRICT resolution, checkout grace (Phase 3).
- **DARK host: exists.** `stayconnect-pmsd`, least-privilege, flags-OFF clean exit, reboot-verified.

## 3. HISTORICAL — what did not exist AT AUTHORIZATION TIME (D18/T0029, 2026-08-11)

> **THIS SECTION IS HISTORICAL AND IS NOT THE CURRENT STATE.** It records the pre-build position the
> plan was written against, so the delivered scope can be checked against what was promised. Phase 4 is
> now **ACCEPTED AND CLOSED at verified LIVE-DARK / NO-FINANCIAL-TRAFFIC maturity** (D19/T0044): the
> execution runtime exists, migrations `0011`–`0026` are applied on the development appliance, and the
> financial tables are referenced throughout `data-plane/internal/{payment,posting}` and
> `data-plane/cmd/edged`. Read every "does not exist" and "greenfield" below **as of 2026-08-11**.

**AS AT AUTHORIZATION TIME, all seven financial tables had zero Go references and the execution runtime was greenfield:**

| WS | Workstream | Primary paths |
|---|---|---|
| **WS-A** ✅ | Migration `0011_phase4_financial_execution` — **DELIVERED**, additive and reversible, containing ONLY the measured gaps: G1 verified RN+G# (mandatory, non-blank, wire-safe), G2 `financial_base_currency`/`financial_base_currency_exponent` on the immutable `pms_interface_revisions` plus exact three-way currency equality, G3 the DERIVED view `iam_v2.posting_execution_state`, C21 `posting_review_state` + `record_posting_review_action()`, the durable `P#` allocator, and the structural no-blind-retry gate. It creates, replaces, renames and weakens **nothing** that existed before it — the gate asserts each pre-existing trigger is still present and enabled and that `charge_gate`'s body is unchanged. See `Phase4-Financial-Schema-Gap-Audit.md`. | `data-plane/migrations/0011_*.{up,down}.sql` |
| **WS-B** ✅ | Posting domain + fail-closed gates — **DELIVERED**. Pinned evidence, the creation gate that refuses before any side effect, and re-verification before every attempt. | `data-plane/internal/posting/{evidence,gate,engine,repo}.go` |
| **WS-C** ✅ | `P#` allocator — **DELIVERED**. `iam_v2.allocate_p_number()`: transactional, durable, row-locked, per-interface, no epoch or clock. Proved gapless and duplicate-free under 200 concurrent allocations. | `0011_*.up.sql`, `data-plane/internal/posting/repo.go` |
| **WS-D** ✅ | Outbox + per-interface lanes — **DELIVERED**. `FOR UPDATE SKIP LOCKED` claiming behind the existing `outbox_one_active` index; lanes proved independent and duplicate-free. | `data-plane/internal/posting/{repo,engine}.go` |
| **WS-E** ✅ | PS/PA + UNKNOWN — **DELIVERED**. Contract-order PS construction, PA correlation by interface + `P#` only, the exact `AS` catalog, and UNKNOWN as a terminal state the database itself refuses to retry. | `data-plane/internal/posting/{fias,engine,transport}.go` |
| **WS-F** ◐ | Manual Review — **BACKEND COMPLETE**: the DB decision boundary, the §15 catalog, atomic reviewer concurrency, `financial-review` RBAC, password step-up, session-bound actor, tenant+site scope and the structured evidence contract are all delivered and tested. The **operator FRONTEND is open**. | `0011/0013_*.up.sql`, `internal/posting/repo.go`, `cmd/edged/resources_phase4_review.go`, `cmd/edged/phase4_review_evidence.go` |
| **WS-G** | Payment/settlement execution — idempotent CHARGE/REFUND, authenticated notification boundary, provider capability contract in DARK | `internal/payment/`, migrations 0014–0018 |
| **WS-H** | Restore / `FINANCIAL_RECOVERY_MODE` — `financial_epoch`, `restore_generation`, `HELD_RECOVERY`, no replay | `financial_recovery.go` |
| **WS-I** | Observability — queue depth, oldest age, UNKNOWN count, review backlog, lane state, bounded codes, no PII | `observability.go` |
| **WS-J** | Operator UI — Postings, attempt history, UNKNOWN queue, Manual Review, evidence, redaction | `hotel-admin/app/(app)/financial/**` |
| **WS-K** | Test matrix — unit, disposable-PG integration, migration up/down/reapply, high-concurrency races, failure injection, protocol contract, HTTP/RBAC, UI, E2E, redaction, restore | `*_test.go`, `hotel-admin/e2e/**` |
| **WS-L** | DARK deployment — flags OFF, no outbox egress, reboot persistence, rollback rehearsal | `deploy/`, `scripts/` |

## 4. Traceability checklist — contract requirement → owner → DB enforcement → test → surface

This is the checklist the delivery must reproduce from scratch at the end. **No row may silently disappear.**
Status is the state **as at authorization time (2026-08-11)**; all read `NOT_IMPLEMENTED` because the runtime was greenfield then. **They are NOT current** — see the Gap Audit for the authoritative delivered status.

**The C1–C38 matrix below has been SUPERSEDED by measurement and by delivery.** Its original statuses assumed the financial schema was empty; the disposable-PG16 rebuild proved otherwise, and migration 0011 plus the financial execution core have since closed every measured DB gap. The authoritative, layered matrix lives in [`Phase4-Financial-Schema-Gap-Audit.md`](Phase4-Financial-Schema-Gap-Audit.md), which records each requirement independently as DB_PRESENT_AND_BEHAVIOURALLY_VERIFIED / RUNTIME_IMPLEMENTED_AND_TESTED_DARK / RUNTIME_GAP / OPERATOR_SURFACE_GAP / NOT_APPLICABLE / BLOCKED_BY_PRODUCT_OWNER_DECISION. Read the table below as the ORIGINAL authorization-time scope list, retained so no row can silently disappear — never as the current status.

Summary of what measurement changed:

- **24 rows** have DB enforcement already present and behaviourally proven (append-only postings, attempts,
  events and review actions; the UNSET charge gate; the stay eligibility gate; `P#` uniqueness; the one-way
  outcome transition; the PA status catalog; the §15 review catalog; one-active outbox; idempotency).
- **Only four genuine DB gaps remain**: RN+G# mandatory (G1), interface base currency and posting currency
  equality (G2 — the interface has **no currency column at all**, so this is larger than planned), the derived
  posting-status projection (G3), and a review concurrency column (C21).
- **The Go runtime is entirely absent** — every requirement keeps `RUNTIME_GAP` regardless of its DB layer. An
  existing constraint is never an overall PASS.
- C25 is NOT_APPLICABLE by contract; C38 is BLOCKED_BY_PRODUCT_OWNER_DECISION.

The table below is retained only as the original pre-measurement decomposition of owners and test intent.

| # | Contract requirement | Source | Owner (WS) | DB enforcement | Test | Operator surface | Status |
|---|---|---|---|---|---|---|---|
| C1 | Posting pinned to tenant, site, interface, **auth** revision, **posting** revision, secret generation, stay, folio, purchase, settlement, package revision, mapping, exact minor-unit amount | §4.5, §9a | B | NOT NULL + RESTRICT FKs on `pms_postings` | pin-immutability test | evidence panel | NOT_IMPLEMENTED |
| C2 | Never re-resolve pinned objects on retry | §9a r2 | B/E | pinned columns immutable after insert (trigger) | retry-reuses-pins test | — | NOT_IMPLEMENTED |
| C3 | Room number is evidence only, never identity | §9a r2 | B | no unique/lookup index on RN | RN-not-identity test | UI must not present RN as sufficient | NOT_IMPLEMENTED |
| C4 | `RN` + `G#` both verified before outbox/transmission | §9a, §9c T1 | B | CHECK `g_number IS NOT NULL` on attempts | RN-only rejection test | blocked reason | NOT_IMPLEMENTED |
| C5 | `folio_identity_strategy = UNSET` blocks charge **before** outbox, **before** `P#`, **before** wire | §9a r6 | B | CHECK against pinned revision | UNSET fail-closed test (no row, no `P#`, no bytes) | blocked reason | NOT_IMPLEMENTED |
| C6 | Package currency **must equal** pinned interface currency; no implicit FX | §9a r3 | B | CHECK equality | mismatch-rejected test | blocked reason | NOT_IMPLEMENTED |
| C7 | ISO-4217 minor units, exponent 2, integer `TA`, no currency on wire | §9a | B/E | integer minor-unit column | wire-encoding contract test | amount display | NOT_IMPLEMENTED |
| C8 | `P#` = protocol-attempt reference, **not** business idempotency | §9a r2 | C | `pms_interface_pnumber_seq` atomic bump | semantics test | attempt list | NOT_IMPLEMENTED |
| C9 | `P#` unique per `(tenant, site, interface, p_number)` | §9a r2 | C | UNIQUE constraint | collision test under concurrency | — | NOT_IMPLEMENTED |
| C10 | `PA` matched by interface + `P#`, never by RN | §9a r2 | E | — | mismatched-RN correlation test | — | NOT_IMPLEMENTED |
| C11 | `PS` field order `RN, G#, TA, PT, SO, CT, P#, WS`; `PT=D`, `SO=WIFI`, `WS=STAYCONNECT`, `CT` ≤ 20 | §9a | E | — | golden-wire contract test | — | NOT_IMPLEMENTED |
| C12 | `AS ∈ {OK,NG,NA,NP,NR,RY,UR}` only; no invented statuses | §9a | E | enum/CHECK | exhaustive status test | status display | NOT_IMPLEMENTED |
| C13 | Transmitted `PS` without matched `PA` ⇒ **UNKNOWN** | §9a r1 | E | one-way outcome transition | timeout→UNKNOWN test | UNKNOWN queue | NOT_IMPLEMENTED |
| C14 | UNKNOWN **never** auto-retried; **no** auto second `P#` | §9a r1, 3C | E | no code path allocates from UNKNOWN | negative test asserting zero new attempts | — | NOT_IMPLEMENTED |
| C15 | `posting_attempts` immutable identity + one-way `SENDING → ACKED\|UNKNOWN\|FAILED` | §9a r2 | B/E | trigger enforcing one-way | illegal-transition test | — | NOT_IMPLEMENTED |
| C16 | `posting_attempt_events` fully append-only | §9a r2 | B | insert-only trigger (no UPDATE/DELETE) | append-only test | history panel | NOT_IMPLEMENTED |
| C17 | Manual-Review actions exactly `CONFIRM_POSTED`, `CONFIRM_NOT_POSTED_RETRY`, `CONFIRM_NOT_POSTED_ABANDON`, `CREATE_REVERSAL`, `ESCALATE`; **no generic approve** | §15 | F | enum CHECK | catalog test | review UI | NOT_IMPLEMENTED |
| C18 | Each action requires `financial-review` write **and** password re-auth, mandatory reason **and** evidence | §15 | F | NOT NULL reason/evidence | RBAC + step-up tests | step-up modal | NOT_IMPLEMENTED |
| C19 | `posting_review_actions` immutable | §15 | F | insert-only trigger | immutability test | — | NOT_IMPLEMENTED |
| C20 | `CONFIRM_NOT_POSTED_RETRY` requeues **once**, same idempotency key | §15 | F | partial unique index | double-requeue test | — | NOT_IMPLEMENTED |
| C21 | Concurrent reviewers cannot both win incompatible actions | prompt/§15 | F | optimistic version column | concurrent-operator test | conflict message | NOT_IMPLEMENTED |
| C22 | Per-interface outbox lanes; two workers cannot create duplicate attempts | §10 | D | `FOR UPDATE SKIP LOCKED` + partial unique active-attempt index | high-concurrency duplicate test | lane state | NOT_IMPLEMENTED |
| C23 | Interfaces are independent namespaces; one failure never affects another | §10 | D | per-interface scoping | isolation test | per-interface health | NOT_IMPLEMENTED |
| C24 | Interface lifecycle `ACTIVE ⇄ AUTH_DISABLED → DRAINING → DECOMMISSIONED`; decommission needs zero PENDING/SENDING/UNKNOWN | §10 | D | CHECK/guard | lifecycle test | admin | NOT_IMPLEMENTED |
| C25 | `programmatic_reversal = false`; no auto negative charge, no `PT=C` | §9a r5 | — | capability flag false | test asserting no reversal sender exists | documented limitation | **NOT_APPLICABLE — contractually forbidden in v1** |
| C26 | No duplicate CHARGE / REFUND / callback application | payments | G | idempotency-key unique index | duplicate-callback test | payment status | NOT_IMPLEMENTED |
| C27 | No cross-tenant merchant reuse | payments | G | tenant-scoped unique | cross-tenant test | — | NOT_IMPLEMENTED |
| C28 | Server-pinned totals; request cannot control amount | payments | G | amount from pinned purchase | tamper test | — | NOT_IMPLEMENTED |
| C29 | Payment success grants entitlement only via the approved atomic path | Phase 2 | G | single grant path | grant-path test | — | NOT_IMPLEMENTED |
| C30 | Restore forces `FINANCIAL_RECOVERY_MODE`; non-terminal commands `HELD_RECOVERY`; FIAS → MANUAL_REVIEW; audited release | §13, §14 | H | `financial_epoch`, `restore_generation` | restore-no-replay test | recovery banner | NOT_IMPLEMENTED |
| C31 | Restore must never auto-replay financial commands | §14, prompt | H | held-state guard | replay-attempt test | — | NOT_IMPLEMENTED |
| C32 | Financial creation requires all four freshness axes green + stay/folio revalidation | §9 | B | — | stale-refusal test | blocked reason | NOT_IMPLEMENTED |
| C33 | Metrics: queue depth, oldest age, UNKNOWN count, review backlog, worker/lane state, send failures, PA ambiguity | §10, prompt | I | — | metric-presence test | dashboards | NOT_IMPLEMENTED |
| C34 | No PII, card data, credentials or raw PMS secrets in logs/metrics/audit | §11 | I | redaction at write | redaction tests | — | NOT_IMPLEMENTED |
| C35 | Compliance archive before cross-customer purge | §12 | H | `compliance_archives` | archive-before-purge test | — | NOT_IMPLEMENTED |
| C36 | Per-property onboarding gates posting; one approved interface never approves another | §9c T2 | B | per-revision strategy | Hotel-ID-2-stays-blocked test | onboarding state | NOT_IMPLEMENTED |
| C37 | All Phase-4 flags OFF; no guest-visible change; no outbox egress while DARK | prompt | L | — | darkness proof | — | NOT_IMPLEMENTED |
| C38 | Real-financial acceptance (Tier-3 3C live) | §9c T3 | — | — | — | — | **BLOCKED_BY_PRODUCT_OWNER_DECISION — real financial traffic is prohibited in this round** |

## 5. Non-negotiable design decisions (fixed here so implementation cannot drift)

1. **`P#` from `pms_interface_pnumber_seq`**, bumped transactionally — never a Unix timestamp (§9a r2).
2. **Gates run before the outbox row exists.** UNSET strategy, missing `G#`, and currency mismatch must
   consume no `P#`, create no outbox row and send no bytes.
3. **UNKNOWN is terminal until a reviewed transition.** No timer, no backoff, no worker path may leave it.
4. **The database is the final authority.** Every invariant that can be a constraint, trigger or partial
   unique index is one — application checks alone are not acceptance evidence.
5. **No provider simulation is acceptance evidence.** The payment provider boundary is built and tested in
   DARK; a simulated response never proves the production path.

## 6. Definition of done

Every C-row resolves to exactly one of: `PASS`, `NOT_APPLICABLE` with contractual reason,
`BLOCKED_BY_PRODUCT_OWNER_DECISION`, `BLOCKED_BY_PROHIBITED_LIVE_ACTION`. No other label is permitted —
"mostly done", "follow-up" and "future hardening" are not valid outcomes for a row in this table.


---

## 7. Standing Phase-4 execution rules

These are not advice. They are the rules this phase has been run under, written down here so that any agent
picking up Phase 4 inherits them without having to be corrected into them again. Each one exists because
its absence produced a real defect in this phase.

1. **Fix forward.** A defect found mid-milestone is corrected inside that milestone. No separate cleanup
   branch, no deferred follow-up row.
2. **Never promote a claim beyond its evidence.** If a check proves cross-tenant isolation, it does not
   prove cross-site isolation. If a test asserts a status, it does not assert byte-identical responses.
   Write down what was measured, not what it suggests.
3. **Financial concurrency requires real concurrent PostgreSQL proof.** Two sessions, both open, both
   committing. A sequential script cannot distinguish a constraint from a lucky ordering.
4. **A sequential pre-check is not a money constraint.** `SELECT`-then-decide inside a trigger is not
   enforcement: two transactions each read the pre-state, each pass, and both commit. Use a unique index,
   an advisory lock over the right key, or a constraint.
5. **FINAL state machines are exact.** The edges in the contract are the only edges. Widening one because a
   runtime found it convenient is a contract change, and only the Product Owner makes those.
6. **Full tenant / site / financial ownership scope is mandatory.** Every financial query and every negative
   test carries tenant AND site AND the owning financial entity. A fixture that uses a fresh tenant per case
   cannot catch a same-tenant, different-site leak.
7. **Immutable audit inputs are structured, bounded and redacted BEFORE insert.** Once a row is in an
   append-only ledger it cannot be cleaned up. A comment saying "no raw payload" is not enforcement.
8. **UNKNOWN is never blindly retried.** An indeterminate provider outcome may have moved money. It routes
   to manual review and stops; nothing re-sends it.
9. **Durable intent precedes any external side effect.** The local record of what we are about to do is
   committed before the provider is contacted, so a crash can never leave money moved with no record of
   asking. This is what `begin_payment_execution` exists for.
10. **One authoritative writer.** Settlement, Purchase and Entitlement each have exactly one writer. A new
    caller becomes a new ENTRY POINT into it, never a second implementation.
11. **Least privilege is part of implementation, not a later hardening pass.** A trigger constrains how a
    row changes; a GRANT decides who may attempt it at all. Both, or neither is finished.
12. **Zero stale leftovers at milestone completion.** The current Plan, Audit, Handoff, project-state and CI
    pointers must agree with each other and with the delivered head. A superseded sentence left in a current
    document is a defect in its own right.
13. **Every new security check must be shown to FAIL against the pre-fix code before it is trusted.** Two
    DARK checks in this phase passed against a deliberately planted hole. A check that has never failed has
    not been tested.


---

## 8. The Phase-4 migration chain

The authoritative chain, in application order. Every one of them applies, reverses and re-applies to an
identical schema fingerprint under `iam_v2_scratch/phase4_0011_financial.sh`.

| Migration | What it establishes |
|---|---|
| `0011_phase4_financial_execution` | Posting execution core, the P# allocator, the review ledger and `record_posting_review_action` |
| `0012_phase4_financial_hardening` | Per-interface serialization, lane discipline, the four PMS runtime freshness axes |
| `0013_phase4_reversal_ledger` | The passive, structurally non-executable reversal ledger (`CREATE_REVERSAL` records; it never posts) |
| `0014_phase4_payment_settlement` | Payment/settlement governance: the status machine, the callback ledger, server-pinned amounts |
| `0015_phase4_payment_hardening` | Real concurrency: the duplicate-charge unique index and the advisory-locked cumulative refund bound |
| `0016_phase4_payment_coherence` | The exact CHARGE machine, settlement admission, `begin_payment_execution`, provider-reference conflict |
| `0017_phase4_least_privilege` | The three financial roles and the first cut of their grants |
| `0018_phase4_financial_identity_and_privilege` | Authoritative provider/merchant configuration, the controlled grant path, operational role grants |
| `0019_phase4_financial_recovery` | `FINANCIAL_RECOVERY_MODE`: the financial epoch, held work, operator reconciliation and release |
| `0020_phase4_financial_observability` | `posting_outbox.enqueued_at`, so backlog AGE is a real signal rather than an inference |
| `0021_phase4_trust_boundary` | High-level operations for the restricted runtime; EXECUTE on every low-level definer primitive revoked |
| `0022_phase4_recovery_closure` | The STRUCTURAL hold (existing outbox work becomes non-sendable), rail-specific reconciliation, release verified against the records, legacy identity provenance |
| `0023_phase4_restore_generation` | The supported restore-generation model: a management-partition marker the database cannot rewrite, plus the unsupported-raw-snapshot path |
| `0024_phase4_outcome_authority_and_grant_kernel` | Provider-outcome authority split into its own role, so one stolen execution credential cannot also declare a capture; and ONE entitlement grant kernel shared by the free and paid entry points |
| `0025_phase4_recovery_completion_and_compliance` | The zero-attempt restore's audited one-time retry, the marker-BEHIND case, C27 global merchant identity, and C35 archive-before-purge |
| `0026_phase4_c35_failclosed_and_operator_retry` | C35 fails CLOSED — the purge gate requires an EXTERNAL verified receipt and the flag cannot be set without the evidence that gives it meaning; plus `v_zero_attempt_recovery_queue`, the read model that makes a never-transmitted held posting visible to an operator at all |

### Authoritative CI

`.github/workflows/phase4-financial-core.yml` is the only authoritative Phase-4 gate. It runs, in order:
gofmt, `go build`, `go vet`, the pre-0011 invariant suite on both chains, the migrations 0011–0026 DB gate,
the least-privilege role proof, the payment concurrency proof, the PG16 integration matrix (posting, review
API, financial-ops API, payment runtime, recovery, restricted-role end-to-end, Phase-2 free grant, entitlement
exactly-once), the DARK static assertion, and the Hotel-Admin typecheck, unit tests, flags-OFF production
build and the FULL browser suite, the supported restore drill, the production dependency advisory gate,
and a self-test that deliberately breaks a step and fails if the gate still reports success.

The **dependency gate was rewritten at T0041**. It previously carried its own accepted-risk list inside the
script, which let the delivery agent grant itself an exception and report PASS on a production tree with
high-severity advisories in it. Acceptance now lives in `governance/dependency-acceptances.json`, names GHSA
ids rather than packages, requires a named decider / decision reference / expiry, and is the ONLY thing that
turns a production advisory into a pass; an advisory that is merely triaged fails. A self-test runs the same
judgement file CI runs against synthetic audits and proves it refuses what it claims to refuse — because a
gate observed only passing on a clean tree has not been shown to do anything.
