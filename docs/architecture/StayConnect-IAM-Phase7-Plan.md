# StayConnect IAM — Phase 7 Plan
## Full-system re-acceptance: proving the built system is one working IAM domain

**Status:** **ACCEPTED AND CLOSED** at **VERIFIED FULL-SYSTEM LIVE-DARK** maturity by Product-Owner decision
**D27** (2026-08-17, transition **T0064**) at delivery head `16819aa027633b84486999451e8b689a191a15d2`. PR #15 was **MERGED** to master on 2026-08-17 under Product-Owner merge decision **D28** (transition
**T0065**), merge commit `9c57c2b5a29eb886cf317912a9eb6a6da8ccb603`; the merge introduced no content and
deployed nothing. Phase 7 was the **last numbered development phase**; no Phase 8 exists or is
authorized, and the only major next lifecycle gate is the separately authorized atomic complete-domain IAM-v2
cutover / go-live decision, which this acceptance does **not** authorize.

<sub>Historical: this plan was AUTHORIZED for planning **and execution** under Product-Owner decision **D26** (2026-08-16),
continuing from post-Phase-6 master `da6a14aba5f51300deac7ee3736f7fbbc8ab5d25`. Branch
`phase/7-full-system-reacceptance`, one pull request → `master`, approximately four substantial end-to-end
milestones. Target acceptance maturity: **VERIFIED FULL-SYSTEM LIVE-DARK** on the **development** appliance.
That target was met and accepted.</sub>

---

## 1. Where this scope comes from

Not from chat history and not from the migration runbook's deployment numbering — `docs/MIGRATION_RUNBOOK.md`
§"Phase 7 — Start edged" is a *deployment step*, and
`docs/COMMERCIAL_ONBOARDING_EXECUTION_STATUS.md` carries a *separate commercial* numbering. Neither is the
roadmap.

The roadmap is the FINAL contract, [§18 Phased Implementation Plan](StayConnect-IAM-Phase0-Contract.md):

| Phase | Content | Gate | Rollback boundary |
|---|---|---|---|
| **7** | Cleanup, final docs/ops manual, **full-system re-acceptance** (reboot, offline, purge, restore drills) | **complete matrix** | restore last verified pre-cutover backup + catalog snapshot |

`governance/project-state.json` records the same: *"Cleanup, final docs, full-system re-acceptance."*
**"Complete matrix"** means the contract's own **§19 Acceptance & Failure-Drill Matrix**, series **A–G**.
That matrix, not a new invention, is the Phase-7 specification.

The contract also places the thing that comes *after* Phase 7, and it is not a development phase:

> **1B→cutover (later, separate gate)** — the atomic complete-domain cutover … is a **later separately
> approved gate — only after Phases 2–6 and full-domain acceptance**, never a Phase-1B credential-only cutover.

So Phase 7 is the **last numbered development phase**, and it is the prerequisite for a cutover decision that
remains entirely unauthorized.

## 2. What Phase 7 is, and what it is not

**It consolidates. It does not reopen.** Phases 1A, 1B, 2, 3, 4, 5 and 6 are ACCEPTED_AND_CLOSED, each with
its own evidence and its own recorded limitations. Phase 7 does not re-litigate their internals and does not
re-derive their conclusions. It asks the one question none of them could answer alone:

> **Do these seven phases, composed, behave as one correct IAM domain — under restart, rollback, isolation,
> concurrency and a real appliance?**

A phase internal is reopened **only** where Phase 7 produces *failing evidence* about it. Then it is a defect
to fix, with a regression, not a re-review.

**Explicitly NOT in scope (fail-closed, governance-enforced):** IAM-v2 production cutover · production data
migration · dual read/write · legacy IAM removal · real guest service · real PMS financial posting · real
payment-provider traffic · paid-access activation · per-property financial enablement · programmatic reversal
· any Production contact or mutation. Each is a separate Product-Owner go-live decision that **D26 does not
supply**. If a §19 case cannot be discharged without one of these, it is reported as **NOT PROVEN with the
exact blocking authorization named** — never quietly downgraded, and never promoted.

**Capabilities stay DARK.** Where a case requires a capability to be on, it is enabled *temporarily on the
authorized DEVELOPMENT appliance only*, through the Phase-6 controlled-validation harness pattern: a captured
baseline, a restoration trap, and verification against the runtime afterwards.

## 3. The milestones

Four, each ending in evidence rather than in a checkpoint.

### M1 — Identity and acquisition, composed (contract §19 A, B, C, D)

The complete authentication → acquisition → session lifecycle, exercised as one flow rather than as four
phases' worth of unit paths:

* every credential method reaching the same durable identity — voucher, account, OTP/social, PMS stay, and
  post-stay PIN — with issuer-scoped social subjects and MAC never an owner (**B1–B6**);
* the engine invariants under composition: shared immovable window across devices, device-limit REJECT with
  its management surface, same-device reconnect without slot burn, exactly-once usage under concurrent close,
  no exit from TERMINATED (**A1–A13**);
* commerce composition: quote exactness under a forged revision/mapping/context, once-per-stay uniqueness
  under a purchase race, revision immutability, money rules (**C1–C6**);
* **multi-PMS namespace and STRICT resolution**: PMS-A room 101 versus PMS-B room 101, dual-verified
  ambiguity escalating uniformly, STRICT refusing on any UNAVAILABLE/STALE candidate, unmapped network failing
  closed, forged interface hints ignored, byte-identical and time-padded refusals (**D1–D11**).

### M2 — The stay, end to end (contract §19 F, plus Phases 5 and 6)

* room move preserving entitlement, devices and quota; stale events never reopening a stay (**F1, F2**);
* **checkout and mandatory grace**: supersession of free, paid and prepaid entitlements with zero nft churn
  and no re-authentication; grandfathering above the grace limit; eligibility at the effective-checkout
  boundary; corrupt grace config falling back with a critical alert; duplicate checkout idempotent per
  episode with reinstatement producing exactly one new grace (**F3–F7**);
* **post-stay** PIN isolation from the next occupant, and **cross-PMS transfer** — typed, cycle-free,
  idempotent, seamless rebind (**F8, F9**);
* **Guest Device Self-Service** and both time modes composed with all of the above: `VALIDITY_WINDOW`
  unchanged, `AGGREGATE_ONLINE_TIME` accruing only for active sessions and terminating on the earliest
  condition;
* **accounting, shaping and enforcement** as one pipeline: the plan derived from durable state, the sweep
  ending access at the true instant, netd forwarding nothing for an ended entitlement.

### M3 — The boundaries hold (contract §19 E, G)

The properties that only fail when something goes wrong:

* **financial core DARK and fail-closed** — folio `UNSET` refusing a CHARGE with no outbox row and no `P#`
  allocation, posting on a non-IN_HOUSE or blocked stay aborting, the pin-chain rejecting at the SQL layer,
  idempotency-key races yielding one charge (**E1–E12**), all with no provider or PMS traffic;
* **real service-role least privilege** — every proof run as `svc_scd`, `svc_acctd`, `svc_edged`,
  `svc_netd`, because Phase 6 demonstrated that a suite connecting as a schema owner cannot see the failures
  that matter;
* **concurrency, idempotency and replay** — the races each phase tested separately, run together;
* **restart and reboot** (**A6, G4**): lanes, pins and breaker states rebuilt, zero duplicate postings;
* **backup and supported restore** (**G1, G2**), including the *documented unsupported raw-snapshot
  limitation* — which stays documented, not quietly closed;
* **rollback and mixed-version safety** — every migration down and up across the whole stack, and the
  new-binary-on-old-schema case that Phase 6 proved is not hypothetical: it silently disabled all expiry;
* **local-first with Central unavailable**, for the whole domain rather than one surface.

### M4 — The system on the appliance, and the record (contract §19 gate: complete matrix)

* **Hotel Admin and Guest Portal composed behaviour** — the operator and guest surfaces over the same durable
  state, agreeing;
* the **complete Phase-3/4/5/6 regression matrix**, green in one run;
* **DEVELOPMENT-appliance full-system acceptance evidence**: a controlled, self-restoring run in the Phase-6
  harness pattern, ending with every capability OFF, verified against the runtime, across a reboot;
* **cleanup and final docs/ops manual** — the §18 wording, taken literally: the operations manual and the
  deployment/runbook surfaces reconciled with the as-built system;
* Zero-Stale, current-state parity and governance green; the Phase-7 report, manifest and PR.

## 4. Rollback boundary

The contract names it: **restore the last verified pre-cutover backup + catalog snapshot.** Phase 7 adds no
migration that is not additive and reversible, and every capability it touches returns to DARK. Because
nothing is cut over, the rollback boundary is still *Boundary A* — nothing has made a first production write.

## 5. What Phase 7 must never do to its own evidence

Recorded here because the pressure to do it increases as a roadmap nears its end:

1. **Never promote NOT PROVEN to PASS.** A case that cannot be discharged without an unauthorized action is
   reported as blocked, with the authorization named.
2. **Preserve every accepted limitation** unless new verified evidence legitimately closes it — and then say
   which evidence, and why it is sufficient.
3. **A gate that cannot fail is worse than no gate.** Phase 6 found two mutation cases that had silently
   stopped biting and a rehearsal that counted failures as passes. Every Phase-7 gate is mutation-checked:
   shown to fail when the property it protects is broken.
4. **The appliance is the arbiter for runtime claims.** Source inspection proves intent; only the running
   system proves behaviour.
