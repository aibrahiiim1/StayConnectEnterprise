# StayConnect IAM Phase 5 — Live-Dark Acceptance

**Status: PRODUCT-OWNER ACCEPTED_AND_CLOSED — VERIFIED LIVE-DARK / NO-FINANCIAL-TRAFFIC (decision D22, closure transition T0053, 2026-08-15). The single Phase-5 pull request #13 remains OPEN and UNMERGED by design; merging it is a separate Product-Owner decision that has not been taken.**

- Phase: 5 (Post-Stay PIN re-authentication + the Cross-PMS Transfer workflow) — one end-to-end Phase.
- **Authorized** under Product-Owner decision **D21**, authorization transition **T0050**.
- **Acceptance candidate complete** recorded by transition **T0051**; **acceptance-evidence fix-forward** recorded by transition **T0052**.
- **Product-Owner ACCEPTED and CLOSED** by decision **D22**, closure transition **T0053** (`transition_accepted: true`) at verified **LIVE-DARK / NO-FINANCIAL-TRAFFIC** maturity.
- Branch: `phase/5-poststay-transfer`; **PR #13 — OPEN and UNMERGED**.
- Appliance: `radius` / `172.21.60.23` — the **development** appliance. Production was never contacted.

---

## The accepted basis, in three separable parts

These are kept apart on purpose. Collapsing them is how a green test run starts being read as a business decision.

### 1. Software-CI evidence — *does the delivered code pass its own gate?*

| | |
|---|---|
| Accepted software/runtime candidate | **`aef848d253e2c6efebe4f036b0369a22530a5a25`** |
| Final verified delivery/evidence head | **`4142f5fe857787a745b186d8ac38edaad7b4d268`** |
| Provenance (`inventory_head`) | **`8877c47a6f1df25fab075f1dfda6f01e32846a75`** |
| Phase 5 Post-Stay and Transfer CI | **31836130617 — SUCCESS** · artifact **9232578379** · `sha256:ed9da35202574f6c93713753153c1cc53ff7084e9e5765a69c34adf232d7383b` |
| Phase 4 Financial Core CI | **31836130580 — SUCCESS** · artifact **9232732340** · `sha256:6d0e343f16f51f51e47214c6049580abf6e1379a9879e825fbd5d46358163404` |
| Phase 3 Software CI | **31836130394 — SUCCESS** · artifact **9232717163** · `sha256:2bde46c2c54461d3a7d2f937cec76f8920f3f3cdca963035d926fa001cf7be7c` |
| Project Governance | **31836130544 — SUCCESS**, re-confirmed as **31837196633** after the PR body update |

The Phase-5 digest is not a number copied from a web page: the artifact was downloaded and hashed locally, and the computed SHA-256 is byte-identical to the digest GitHub reports.

A push to the phase branch with an open pull request fires both `push` and `pull_request` runs. The push copies (Phase 4 `31836122047`, Phase 5 `31836122169`) and a manual Phase-3 dispatch (`31836130899`) are also SUCCESS on the same head; the `pull_request` set above is the authoritative one.

This says the code is internally correct against disposable PostgreSQL 16 and its own regression suites. It says nothing about any appliance.

### 2. Live evidence — *did that code behave on a real appliance?*

Development appliance `radius`, database `stayconnect_site`. A backup was taken and verified **before any change** (`6,260,737` bytes, sha256 `cf1bda43…`). Migrations **0027, 0028, 0029** each applied once and recorded exactly once. iam_v2 base tables **68 → 68** — Phase 5 creates no tables — and public tables **44 → 44**; structural fingerprint `71dde7dc871b935ae555bcab2e5c1252` → `07e08329beebef509e811a147524cdc5`.

Every Phase-5 table holds **0 rows**; 13 Phase-5 objects present; **no role besides the schema owner** holds any privilege on a Phase-5 table; **no Phase-5 flag** appears in any env file or unit; all three scd Phase-5 routes return **404** — absent, not present-and-refusing. `scd`, `edged`, `netd` and `acctd` are active.

**pmsd** — the one daemon whose healthy state is *not running* — was verified against its established DARK contract in six arms under T0052: installed and enabled; `inactive (dead)` by way of `Result=success` and exit `0`; `NRestarts=0` with exactly one start this boot and zero restart-storm indicators; its own dark statement and clean stop present in this boot's journal; zero processes and zero sockets; zero attributable database backends. The criterion deliberately does **not** require a live PMS worker, because demanding one would either fail a correct appliance or be satisfied by starting a live connector — real PMS traffic that nothing here authorizes.

The appliance was **rebooted** (2026-08-14T17:41:53Z) with migrations, services and darkness intact afterwards; the backup was **really restored** into `phase5_restore_drill`, showing the pre-deployment state (68 base tables, 0 Phase-5 migrations); and the rollback was **rehearsed on the live database**, returning the structural fingerprint to exactly `71dde7dc…` with 0 Phase-5 objects, 0 ledger rows and the Phase-3 guard restored **including its refusal message**, then re-applied and re-verified.

### 3. Product-Owner decision — *is that enough?*

**D22.** The judgement that (1) and (2) together are sufficient for LIVE-DARK maturity. It is not derivable from the evidence and was not taken by the implementer.

---

## Defects found and fixed before acceptance

| # | Found by | Fix |
|---|---|---|
| D5-1 | the contractual F9-i race | the transfer left the source's device **authorization intervals open** against a terminated entitlement — closed in the same transaction that opens the destination's, and proven load-bearing by mutation |
| D5-2 | the same race | a transfer accepted a source Stay that had **already checked out**, moving checkout *grace* to a property that never granted it — the source must now be `IN_HOUSE`, re-checked under the lock |
| D5-3 | Phase-3 portal CI | the post-stay tab displaced the room form as the portal's default panel; fourteen tests timed out on an invisible field |
| D5-4 | the appliance itself | the darkness grant check excluded `current_user` rather than the table's **owner**, so on the Gate-P-separated appliance the owner's own rights read as 21 "non-owner" grants and condemned a correct deployment |
| E5-1 | the T0052 evidence review | the Phase-5 CI gate was green for four milestones and **published no artifact at all**; publication is now fail-closed and self-tested |
| E5-2 | the same review | the LIVE-DARK health check never covered **pmsd**; the DARK-contract criterion above closes it |

---

## Accepted limitations — preserved, NOT promoted to PASS

1. **`POST_STAY_ACTIVE` has no exit transition.** The FINAL contract draws no arrow out of it. A reinstatement for a converted Stay is refused by the guard and lands in the operator queue. (**L5-1**)
2. **Post-Stay v1 is zero-price only.** A priced or settlement-requiring revision is refused rather than granted free. Paid post-stay access remains outside this phase. (**L5-2**)
3. **Post-stay requires a device authorized during the stay.** A wholly new device receives the uniform non-success. This is what removes the client-supplied-subject parameter altogether — a deliberate trade, not a gap to close silently. (**L5-3**)
4. **`stay_links(reason='POST_STAY')` is never written.** Both ends are `NOT NULL` Stays and post-stay has one; writing it would require synthetic PMS state. Enforced by the database guard. (**L5-4**)
5. **A transfer is not reversible by an inverse transfer.** The source entitlement is terminated and `from_entitlement_id` is UNIQUE; reversal is an operator/PMS-level correction. (**L5-5**)
6. **The destination grant is a bounded window**, not an equivalence between source and destination access. (**L5-6**)
7. **Several Phase-3/Phase-4 integration tests are not repeatable against a reused database.** Two were fixed; the rest are unchanged. CI builds a fresh database per run, which is the documented contract. (**L5-7**)
8. **`cmd/scd` integration tests need migrations 0001–0006** that the local Phase-5 harness does not build; they run in CI, which builds the full chain. (**L5-8**)
9. **Four pmsd DARK-contract arms were proven at the predicate level, not end-to-end.** No enabled-and-inactive substitute unit exists on the development appliance, and manufacturing one would have meant installing a unit on it. The installed/enabled and clean-disabled-exit arms were proven end-to-end. (**L5-9**)

None of the nine is promoted. Each remains a limitation of the accepted Phase-5 delivery.

---

## The closure round introduces zero product-runtime change

Measured, not asserted: `git diff --name-only` between the accepted delivery/evidence head `4142f5f` and the closure delivery head returns **no path** under `data-plane/`, `hotel-admin/`, `scripts/` or `.github/workflows/`. Only `governance/`, `docs/`, `exports/` and one governance-validator file under `tools/` change — `validate-current-state-parity.py`, which gains the Phase-5 plan in its phase-plan map so the accepted-phase-semantics rule actually reads that surface. *Product runtime* here means the software the appliance runs and the gates that build it: `data-plane/`, `hotel-admin/`, `scripts/` and `.github/workflows/`. An acceptance round is exactly the moment at which a quiet code change is least likely to be noticed, because attention is on the paperwork — so the proof is derived from Git.

---

## Explicitly NOT authorized by this acceptance

No pull-request merge · no deployment or further appliance mutation · no Phase-5 or Phase-4 feature-flag enablement · no IAM-v2 authentication cutover · no Production migration or Production database contact · no PMS, provider or real financial traffic · no paid guest access · no Phase 6 or Phase 7 work.

Phases 0, 1A, 1B, 2, 3 and 4 are preserved unchanged, and the six accepted Phase-4 limitations are not promoted.

## Evidence records

- Execution evidence: [`docs/evidence/StayConnect-IAM-Phase5-Evidence.md`](../evidence/StayConnect-IAM-Phase5-Evidence.md)
- Final report: [`docs/reports/StayConnect-IAM-Phase5-Final-Report.md`](../reports/StayConnect-IAM-Phase5-Final-Report.md)
- Plan: [`docs/architecture/StayConnect-IAM-Phase5-Plan.md`](../architecture/StayConnect-IAM-Phase5-Plan.md)
- Authorization receipt: `governance/transitions/T0050.json` · Candidate-complete receipt: `governance/transitions/T0051.json` · Evidence fix-forward: `governance/transitions/T0052.json` · Closure receipt: `governance/transitions/T0053.json`

## Product-Owner decision (recorded)

Phase 5 is **ACCEPTED at verified LIVE-DARK / NO-FINANCIAL-TRAFFIC maturity and CLOSED** by Product-Owner decision **D22** / closure transition **T0053** (2026-08-15). Acceptance is at LIVE-DARK maturity only. Pull request #13 remains **OPEN and UNMERGED**; merging it is a separate Product-Owner decision.
