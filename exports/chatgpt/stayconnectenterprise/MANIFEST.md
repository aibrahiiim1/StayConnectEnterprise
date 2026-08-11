# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0023 -->
**Current phase:** 3 — PMS Stay Domain, STRICT Multi-PMS Resolution, Room Movement, Checkout Grace and Reinstatement
**Current activity:** `PHASE_3_DARK_ACCEPTANCE_CANDIDATE`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B ACCEPTED_AND_CLOSED (DARK — accepted & closed; no cutover; no production iam_v2 use) · 2 ACCEPTED_AND_CLOSED · 3 IN_PROGRESS · 4 NOT_STARTED · 5 NOT_STARTED · 6 NOT_STARTED · 7 NOT_STARTED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**iam_v2:** 63 tables, 0 rows, dark; no service routed; no data migration; legacy public schema is the sole production authority.
**Single next authorized action:** Obtain the Product-Owner FINAL ACCEPTANCE decision for Phase 3 at DARK maturity and the separate decision on merging PR #6, which remains open and unmerged. The blocked Live Increment-9 subset was re-validated on 2026-08-11 and the two findings it produced (the pre-nftconverge rollback boundary and the missing pmsd DARK service) are closed and live-verified. Legacy live-session continuity remains NOT PROVEN because it requires a real legacy guest online and nothing was fabricated to close it. Phase 3 is IN_PROGRESS, DARK, NOT accepted and NOT closed until that decision.
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D15`.
<!-- END GENERATED PROJECT STATE -->

## Provenance
- **SOURCE_COMMIT (clean source this pack was built from):** `1c1761d`
- **State transition:** `T0023`  ·  **schema:** `1.0.0`  ·  **build timestamp:** `2026-08-11T10:53:20Z`
- **PROJECT_PACK_EXPORT_COMMIT:** *external* — the commit that commits this pack (recorded in the execution report; a pack never contains the commit that commits it). Verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized)* files; no secrets/DSNs/guest PII.

## Files

| # | Exported filename | Original repository path | Source | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `1c1761d` | Entry point | `df36aa7c8d6c7da17b028ab969480624eda432a2feba0f2f0e06ec9420a01b99` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `1c1761d` | Project config | `1a2f24668517a7e19c726b00c3eaeff8e49566ad90138ff5e159c024bccf162b` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `1c1761d` | **Authoritative** *(sanitized)* | `15a5291e538394bac0f2ab22360c288f00527d9d17eb508613aff98d0485ac0a` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `1c1761d` | **Authoritative** | `e3d031791a027c48ad7799242c1ff811bea055d738107e9dd0df39e2bd631820` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `1c1761d` | **Authoritative (closed phase)** | `bfc46e52941f71b55436c24a5f9a9a4e5cc3ff6d4d66d97d7045d484b61898af` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `1c1761d` | **Authoritative — ACCEPTED_AND_CLOSED at DARK maturity (D11/T0011); PR #2 merged** | `ec749136bbc37f7c9b5c019a547fa92685d67ca7046f26801da1e174547a51f3` |
| 7 | `Phase1B-Privilege-Matrix.md` | `docs/architecture/Phase1B-Privilege-Matrix.md` | `1c1761d` | **Authoritative — as-built grant matrix (Gate P deployed)** | `d7ffc726816e4ed6a677d35cf0b645b79278ea2a48ed4eb561008fcebb640e3d` |
| 8 | `StayConnect-IAM-Phase2-Plan.md` | `docs/architecture/StayConnect-IAM-Phase2-Plan.md` | `1c1761d` | **Authoritative — Phase 2 ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014); PR #4 authorized to merge** | `9ac52c7878618125b5e67dd3561f82b273ae979b8d29427b1445d6b359f8f09c` |
| 9 | `Phase2-Privilege-Matrix.md` | `docs/architecture/Phase2-Privilege-Matrix.md` | `1c1761d` | **Authoritative — zero new Phase-2 runtime privilege (live-verified)** | `c4306f39f6aeba8e1b3b86504807f6f22be5a183c0993e240706f6ab8cef3229` |
| 10 | `StayConnect-IAM-Phase2-Software-Gate.md` | `docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md` | `1c1761d` | **Authoritative — Phase 2 software-gate evidence (Go + 45 UI tests + build)** | `9cac79718cfedd6ef9d8351c3ffab27af998b5ef3c582919f986591184590cba` |
| 11 | `StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `1c1761d` | **Authoritative — Phase 2 live-dark + two-reboot darkness evidence** | `c21e3671a2ae9452298f34b00174ecb47c59ebfa4d422853757fe3eb69163c36` |
| 12 | `StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `1c1761d` | **Acceptance record — PRODUCT-OWNER ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014)** | `fafbe27b5cd44ab4e84aac1bd2a66679c919626dc8e86128dc8c53ba5e050c79` |
| 13 | `StayConnect-IAM-Phase2-Final-Report.md` | `docs/reports/StayConnect-IAM-Phase2-Final-Report.md` | `1c1761d` | **Authoritative — Phase 2 final report (accepted)** | `369cd9fdbe3b410532078ab296a40d3c82ac3f171c6d3f17b3eece5f4da1dbb3` |
| 14 | `Phase2-change-manifest.md` | `docs/manifests/Phase2-change-manifest.md` | `1c1761d` | **Generated — complete Phase 2 changed-file manifest (base..delivery_head; inventory_head provenance)** | `942fa3084e0df8f3d53315a3793be3d5b70e91326cc6cc15eb155a821c3f2008` |
| 15 | `StayConnect-IAM-Phase3-Plan.md` | `docs/architecture/StayConnect-IAM-Phase3-Plan.md` | `1c1761d` | **Authoritative — Phase 3 plan (D14/T0015; IMPLEMENTATION IN PROGRESS, DARK)** | `84af5c21b8af2b982597ab0b5cd44c53066ef2e0c12d4d415c45bcae5efbfac5` |
| 16 | `Phase3-Privilege-Matrix.md` | `docs/architecture/Phase3-Privilege-Matrix.md` | `1c1761d` | **Authoritative — Phase 3 privilege matrix (PRODUCTION_IAM_V2_DML: NONE; DARK)** | `9acebf6066b95114580a16aec87ee6e6e2c487dc4adaf71fdc46c2ba5f000726` |
| 17 | `Phase3-change-manifest.md` | `docs/manifests/Phase3-change-manifest.md` | `1c1761d` | **Generated — complete Phase 3 changed-file manifest (base..delivery_head; inventory_head provenance)** | `41b9580a663c511d9b6beee922d59f2a60a1107b3eb07290ab813276afa7e82f` |
| 18 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `1c1761d` | **Authoritative (acceptance record)** | `268d38dd93fc8fcc01caab762f6485bf15265a35eff24b0d2032ef17cc80d4c3` |
| 19 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `1c1761d` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 20 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `1c1761d` | **Permanent rule** | `903c225c2eb4402d923f9f387200d79193d22da26e14f4e6c059a83f80accd2a` |
| 21 | `GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `1c1761d` | **Permanent rule** | `f1d467e1d1bc697dc046cc00ffe80f48858951b05a23ce24a75f4654a984dacb` |
| 22 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `1c1761d` | Historical snapshot | `3b5cc376451a8bec9907793e0cb5ef70aff231c67b52cf4d05e0e78be53f04e4` |
| 23 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `1c1761d` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 24 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `1c1761d` | Supporting | `37f2022028148769b861b5d446427404a3aaa9545172ada0b24a60451c36e138` |
| 25 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `1c1761d` | Supporting | `7e76f07e06785e58683d95dd5cadbbcc3f7ccbade77df5ab452dbf1c289ed773` |
| 26 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `1c1761d` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 27 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `1c1761d` | Supporting | `737935641c4d8d0d5de9fd7a7d627aa634d215d2accf9867c76f5d25b658ca55` |

*(MANIFEST is not self-referential.)*

## Content checksum
- pack_content_sha256 is the SHA-256 over the sorted `sha256(file)` lines of all non-MANIFEST pack files (see PACK_SHA256SUMS in the Evidence/Planning packs for physical lists).
