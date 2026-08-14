# StayConnect IAM Phase 4 — Live-Dark Acceptance

**Status: PRODUCT-OWNER ACCEPTED_AND_CLOSED — VERIFIED LIVE-DARK / NO-FINANCIAL-TRAFFIC (decision D19, closure transition T0044, 2026-08-13). The single Phase-4 pull request has been MERGED to master on 2026-08-14 under the separate Product-Owner merge decision **D20** (transition **T0048**), merge commit `210154b5ba72178bae715e7c8e4a1398ca629257`.**

- Phase: 4 (Financial Execution Layer) — one end-to-end Phase.
- **Authorized** under Product-Owner decision **D18**, authorization transition **T0029**.
- **Live WS-L deployment** recorded by transition **T0043** (controlled live-DARK deployment, reboot, recovery and rollback drill).
- **Product-Owner ACCEPTED and CLOSED** by decision **D19**, closure transition **T0044** (`transition_accepted: true`) at verified **LIVE-DARK / NO-FINANCIAL-TRAFFIC** maturity.
- Branch: `phase/4-financial-execution`; **PR #12 — MERGED to master on 2026-08-14 under the separate Product-Owner merge decision **D20** (transition **T0048**), merge commit `210154b5ba72178bae715e7c8e4a1398ca629257`**.
- Appliance: `radius` / `172.21.60.23`, site `7acf26a7-5ad2-4c65-aef7-651107484636`, serial `APP-DEV-0001` — the **development** appliance.

---

## The accepted basis, in three separable parts

These are kept apart on purpose. Collapsing them is how a green test run starts being read as a business decision.

### 1. Software-CI evidence — *does the delivered code pass its own gate?*

| | |
|---|---|
| Accepted software candidate | **`b94112d8cb0ab63938b60f829ddd465c14491f97`** |
| Phase 4 Financial Core CI | **31690016483 — SUCCESS**, 36/36 steps |
| Evidence artifact | **9177140558** |
| Artifact digest | `sha256:aacb6e9f687f872632b36a1dfe5c4598340d2108d1e70f31ee6c48cae93ba52d` |
| Pre-acceptance delivery head | **`105af49b9a0b44e6eda131b08f7d5fa6a37a2bbc`** |
| Delivery-head CI | **31691250489 — SUCCESS**, 36/36 steps |
| Delivery-head artifact | **9177645571**, `sha256:1b03e9863d5a8b7ec79a01b89574ad82e8fc9822d06c5561bc29366f6d098200` |

This says the code is internally correct against disposable PostgreSQL 16 and its own regression suites. It says nothing about any appliance.

### 2. T0043 live evidence — *did that code behave on a real appliance?*

Migrations **0011 through 0026** applied to the appliance's `iam_v2` through the controlled path, each version recorded **exactly once**; 62 → **68 base tables**, 1 → 7 views, 60 → 111 functions, all owned by `iam_v2_owner`, **0 rows before and after**. The `public` legacy schema's column fingerprint `a8fec747…` is **identical** before and after and legacy IAM remains the sole authentication authority.

Five **NOLOGIN** restricted roles created (`sc_payment_runtime`, `sc_payment_outcome`, `sc_commerce_runtime`, `sc_financial_operator`, `sc_financial_readonly`); **no Phase-4 login role and no Phase-4 DSN** — the design needs none while DARK.

Phase-4 binaries and the Hotel-Admin bundle deployed; an **authorized reboot** survived with every criterion persisted; the safe portions of the supported backup/recovery procedure exercised, including a **real restore into a scratch database**; the documented rollback **rehearsed and reversed**, returning the appliance to the intended candidate.

Throughout: every Phase-4 flag **absent** from every env file and unit, every Phase-4 route **404** on the running `edged` while `/edge/v1/health` returned 200, and **zero financial egress**.

### 3. Product-Owner decision — *is that enough?*

**D19.** The judgement that (1) and (2) together are sufficient for LIVE-DARK maturity. It is not derivable from the evidence and was not taken by the implementer.

---

## Accepted limitations — preserved, NOT promoted to PASS

1. **C35 external archival receipt authority does not exist.** The implemented archive, its digest and the purge gate are accepted **because they fail closed**: `p4_assert_compliance_archived` refuses without a verified receipt, and the flag cannot be set without external evidence even by the database superuser. **Cross-customer purge remains unavailable** until a real external receipt authority exists.
2. **C38 real-financial acceptance remains outside Phase 4** and requires a separate future Product-Owner authorization.
3. **The financial restore management marker is not installed** on the development appliance. This is a **pre-financial-enable prerequisite**: it must be initialized/installed and verified through the supported process **before any future real financial traffic is authorized**. It is not a Phase-4 defect and nothing in this closure satisfies it.
4. **Legacy live-session continuity remains NOT PROVEN** from Phase 3. It must **not** be inferred as PASS from WS-L row counts — the public-schema row growth observed during WS-L is ordinary legacy activity and says nothing about continuity across a change.
5. **No real payment-provider adapter or real-provider behaviour is accepted** by this DARK closure. The payment domain was verified against deterministic in-process doubles only.
6. **Per-property Tier-2 financial onboarding remains mandatory** before any property can be financially enabled. Hotel ID 2 remains financially **UNAPPROVED**.

---

## One wording correction, made forward

T0043 and the WS-L report stated that **"no role at all"** can execute `iam_v2.p4_record_compliance_receipt`. That overstates a measurement into a permanent impossibility, and it makes a deliberate capability read as dead code.

**The accurate statement:** no *deployed runtime, service or PUBLIC* role is **authorized** to call it, and it is **unreachable from the current runtime**. Measured on the appliance: 0 of `sc_payment_runtime`, `sc_payment_outcome`, `sc_commerce_runtime`, `sc_financial_operator`, `sc_financial_readonly`, `svc_edged`, `svc_scd`, `svc_acctd`, `svc_netd` and `PUBLIC` hold EXECUTE. The controlled function **exists deliberately** so that the day a real external archival authority exists, the shape is already correct and auditable.

Corrected **forward** under D19. `governance/transitions/T0043.json` is **not** rewritten — it records what was said when it was written; T0044 records what is accurate now.

---

## Explicitly NOT authorized by this acceptance

No further appliance mutation · no Production migration or Production database contact · no Phase-4 flag enablement · no IAM-v2 authentication cutover · no real PMS PS or PA · no real payment-provider CHARGE or REFUND · no folio debit · no paid guest access · no Hotel ID 2 financial onboarding · no implicit FX · no executable PMS reversal · no C38 execution · no Phase 5/6/7 work · **no pull-request merge**.

## Evidence records

- Live acceptance record: [`docs/evidence/Phase4-Final-Live-Acceptance-Record.md`](../evidence/Phase4-Final-Live-Acceptance-Record.md)
- Final report: [`docs/reports/StayConnect-IAM-Phase4-Final-Report.md`](../reports/StayConnect-IAM-Phase4-Final-Report.md)
- Live evidence receipt: `governance/transitions/T0043.json` · Closure receipt: `governance/transitions/T0044.json`
- Gap audit: [`docs/architecture/Phase4-Financial-Schema-Gap-Audit.md`](../architecture/Phase4-Financial-Schema-Gap-Audit.md) · Plan: [`docs/architecture/StayConnect-IAM-Phase4-Plan.md`](../architecture/StayConnect-IAM-Phase4-Plan.md)

## Product-Owner decision (recorded)

Phase 4 is **ACCEPTED at verified LIVE-DARK / NO-FINANCIAL-TRAFFIC maturity and CLOSED** by Product-Owner decision **D19** / closure transition **T0044** (2026-08-13). Acceptance is at LIVE-DARK maturity only. The Phase-4 pull request has been MERGED to master on 2026-08-14 under the separate Product-Owner merge decision **D20** (transition **T0048**), merge commit `210154b5ba72178bae715e7c8e4a1398ca629257`.
