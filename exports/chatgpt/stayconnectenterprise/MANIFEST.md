# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0043 -->
**Current phase:** 4 — Financial: settlements, postings + outbox, payments, recovery, manual review
**Current activity:** `PHASE_4_LIVE_DARK_DEPLOYED_ON_DEVELOPMENT_APPLIANCE__AWAITING_PRODUCT_OWNER_ACCEPTANCE`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B ACCEPTED_AND_CLOSED (DARK — accepted & closed; no cutover; no production iam_v2 use) · 2 ACCEPTED_AND_CLOSED · 3 ACCEPTED_AND_CLOSED · 4 IN_PROGRESS · 5 NOT_STARTED · 6 NOT_STARTED · 7 NOT_STARTED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**iam_v2:** 63 tables, 0 rows, dark; no service routed; no data migration; legacy public schema is the sole production authority.
**Single next authorized action:** Obtain the Product Owner's acceptance decision on the WS-L controlled live-DARK result recorded in governance/transitions/T0043.json: WS-L was executed on the development appliance under the existing D18/T0029 authorization with every Phase-4 flag OFF and zero financial egress, and Phase 4 remains NEITHER accepted NOR closed until that decision is made, with no further live mutation, no flag enablement, no IAM-v2 cutover, no Production migration and no real financial traffic authorized in the meantime.
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D18`.
<!-- END GENERATED PROJECT STATE -->

## Provenance
- **SOURCE_COMMIT (clean source this pack was built from):** `f71b6d2`
- **State transition:** `T0043`  ·  **schema:** `1.0.0`  ·  **build timestamp:** `2026-08-13T10:24:42Z`
- **PROJECT_PACK_EXPORT_COMMIT:** *external* — the commit that commits this pack (recorded in the execution report; a pack never contains the commit that commits it). Verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized)* files; no secrets/DSNs/guest PII.

## Files

| # | Exported filename | Original repository path | Source | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `f71b6d2` | Entry point | `b6cbe672eca7480ae3423cb3b693702b76c7382bf7d219f5d57137408a6d3a7d` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `f71b6d2` | Project config | `9a6842b48d16622b5a50543f5042798aae58e5a7da6f8f2e56465c61f7f4e714` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `f71b6d2` | **Authoritative** *(sanitized)* | `137e440a9e1f7583bc8988e7d76c166b6aa906a0c8f43f4c7d8d7373b1d3988b` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `f71b6d2` | **Authoritative** | `966e6d72392f315c02b54bac4a0d574aa3c768a3ca8f77d147832cdedc72d33e` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `f71b6d2` | **Authoritative (closed phase)** | `15ba0063fb56544772577b536f9fbe9979cc594a259e9f5bfec0d4d3c85ed11f` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `f71b6d2` | **Authoritative — ACCEPTED_AND_CLOSED at DARK maturity (D11/T0011); PR #2 merged** | `f0ac45f59dfa09d7e69da441393599780870e3cb6045706693c8f39e59e86449` |
| 7 | `Phase1B-Privilege-Matrix.md` | `docs/architecture/Phase1B-Privilege-Matrix.md` | `f71b6d2` | **Authoritative — as-built grant matrix (Gate P deployed)** | `d7ffc726816e4ed6a677d35cf0b645b79278ea2a48ed4eb561008fcebb640e3d` |
| 8 | `StayConnect-IAM-Phase2-Plan.md` | `docs/architecture/StayConnect-IAM-Phase2-Plan.md` | `f71b6d2` | **Authoritative — Phase 2 ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014); PR #4 authorized to merge** | `9ac52c7878618125b5e67dd3561f82b273ae979b8d29427b1445d6b359f8f09c` |
| 9 | `Phase2-Privilege-Matrix.md` | `docs/architecture/Phase2-Privilege-Matrix.md` | `f71b6d2` | **Authoritative — zero new Phase-2 runtime privilege (live-verified)** | `c4306f39f6aeba8e1b3b86504807f6f22be5a183c0993e240706f6ab8cef3229` |
| 10 | `StayConnect-IAM-Phase2-Software-Gate.md` | `docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md` | `f71b6d2` | **Authoritative — Phase 2 software-gate evidence (Go + 45 UI tests + build)** | `9cac79718cfedd6ef9d8351c3ffab27af998b5ef3c582919f986591184590cba` |
| 11 | `StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `f71b6d2` | **Authoritative — Phase 2 live-dark + two-reboot darkness evidence** | `c21e3671a2ae9452298f34b00174ecb47c59ebfa4d422853757fe3eb69163c36` |
| 12 | `StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `f71b6d2` | **Acceptance record — PRODUCT-OWNER ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014)** | `fafbe27b5cd44ab4e84aac1bd2a66679c919626dc8e86128dc8c53ba5e050c79` |
| 13 | `StayConnect-IAM-Phase2-Final-Report.md` | `docs/reports/StayConnect-IAM-Phase2-Final-Report.md` | `f71b6d2` | **Authoritative — Phase 2 final report (accepted)** | `369cd9fdbe3b410532078ab296a40d3c82ac3f171c6d3f17b3eece5f4da1dbb3` |
| 14 | `Phase2-change-manifest.md` | `docs/manifests/Phase2-change-manifest.md` | `f71b6d2` | **Generated — complete Phase 2 changed-file manifest (base..delivery_head; inventory_head provenance)** | `942fa3084e0df8f3d53315a3793be3d5b70e91326cc6cc15eb155a821c3f2008` |
| 15 | `StayConnect-IAM-Phase3-Plan.md` | `docs/architecture/StayConnect-IAM-Phase3-Plan.md` | `f71b6d2` | **Authoritative — Phase 3 plan (D14/T0015; IMPLEMENTATION IN PROGRESS, DARK)** | `ab2b691992057e184c61a6990d5ba03f202f963ae6be3a9c1a96584f3cfaa7bf` |
| 16 | `Phase3-Privilege-Matrix.md` | `docs/architecture/Phase3-Privilege-Matrix.md` | `f71b6d2` | **Authoritative — Phase 3 privilege matrix (PRODUCTION_IAM_V2_DML: NONE; DARK)** | `9acebf6066b95114580a16aec87ee6e6e2c487dc4adaf71fdc46c2ba5f000726` |
| 17 | `Phase3-change-manifest.md` | `docs/manifests/Phase3-change-manifest.md` | `f71b6d2` | **Generated — complete Phase 3 changed-file manifest (base..delivery_head; inventory_head provenance)** | `4cb252db8894ea6ebab6ed19e592ecbb102112f43b8fd36fb289fdcad992af90` |
| 18 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `f71b6d2` | **Authoritative (acceptance record)** | `268d38dd93fc8fcc01caab762f6485bf15265a35eff24b0d2032ef17cc80d4c3` |
| 19 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `f71b6d2` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 20 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `f71b6d2` | **Permanent rule** | `903c225c2eb4402d923f9f387200d79193d22da26e14f4e6c059a83f80accd2a` |
| 21 | `GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `f71b6d2` | **Permanent rule** | `f1d467e1d1bc697dc046cc00ffe80f48858951b05a23ce24a75f4654a984dacb` |
| 22 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `f71b6d2` | Historical snapshot | `46ef42e87144eb6df1c0d89db6e72e96c9fda2a8ec3f423fbf91aedc5be029ef` |
| 23 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `f71b6d2` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 24 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `f71b6d2` | Supporting | `37f2022028148769b861b5d446427404a3aaa9545172ada0b24a60451c36e138` |
| 25 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `f71b6d2` | Supporting | `7e76f07e06785e58683d95dd5cadbbcc3f7ccbade77df5ab452dbf1c289ed773` |
| 26 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `f71b6d2` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 27 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `f71b6d2` | Supporting | `737935641c4d8d0d5de9fd7a7d627aa634d215d2accf9867c76f5d25b658ca55` |

*(MANIFEST is not self-referential.)*

## Content checksum
- pack_content_sha256 is the SHA-256 over the sorted `sha256(file)` lines of all non-MANIFEST pack files (see PACK_SHA256SUMS in the Evidence/Planning packs for physical lists).
