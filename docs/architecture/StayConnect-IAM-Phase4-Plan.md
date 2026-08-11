# StayConnect IAM — Phase 4 Plan: Financial Execution Layer (DARK)

**Status:** AUTHORIZED — PLANNING. Product-Owner decision **D18**, transition **T0029**, 2026-08-11.
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

## 3. What does not exist — the Phase-4 build

**All seven financial tables have zero Go references.** The execution runtime is greenfield:

| WS | Workstream | Primary paths |
|---|---|---|
| **WS-A** | Migration `0011_phase4_financial_execution` — additive/reversible: DB-level financial constraints, one-way outcome transitions, partial unique indexes, `P#` sequence function | `data-plane/migrations/0011_*.{up,down}.sql` |
| **WS-B** | Posting domain + fail-closed gates (identity pinning, RN+G#, folio strategy, currency equality, freshness) | `data-plane/internal/iamv2/posting_*.go` |
| **WS-C** | `P#` allocator — durable atomic per-interface sequence, **not** epoch-seeded | `posting_pnumber.go` |
| **WS-D** | Outbox + per-interface financial lanes, single-writer claim, bounded in-flight | `posting_outbox.go`, `posting_worker.go` |
| **WS-E** | PS/PA state machine + UNKNOWN (no auto-retry, no second `P#`) | `posting_state.go`, `pms/protel_fias_posting.go` |
| **WS-F** | Manual Review — the exact §15 action catalog, step-up, append-only, optimistic concurrency | `posting_review.go` |
| **WS-G** | Payment/settlement execution — idempotent CHARGE/REFUND, callback dedupe, provider boundary in DARK | `payment_*.go` |
| **WS-H** | Restore / `FINANCIAL_RECOVERY_MODE` — `financial_epoch`, `restore_generation`, `HELD_RECOVERY`, no replay | `financial_recovery.go` |
| **WS-I** | Observability — queue depth, oldest age, UNKNOWN count, review backlog, lane state, bounded codes, no PII | `observability.go` |
| **WS-J** | Operator UI — Postings, attempt history, UNKNOWN queue, Manual Review, evidence, redaction | `hotel-admin/app/(app)/financial/**` |
| **WS-K** | Test matrix — unit, disposable-PG integration, migration up/down/reapply, high-concurrency races, failure injection, protocol contract, HTTP/RBAC, UI, E2E, redaction, restore | `*_test.go`, `hotel-admin/e2e/**` |
| **WS-L** | DARK deployment — flags OFF, no outbox egress, reboot persistence, rollback rehearsal | `deploy/`, `scripts/` |

## 4. Traceability checklist — contract requirement → owner → DB enforcement → test → surface

This is the checklist the delivery must reproduce from scratch at the end. **No row may silently disappear.**
Status is the *current* state at authorization time; all are `NOT_IMPLEMENTED` because the runtime is greenfield.

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
