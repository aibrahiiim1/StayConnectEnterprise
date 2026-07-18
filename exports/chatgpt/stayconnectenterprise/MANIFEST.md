# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0014 -->
**Current phase:** 2 — Packages, revisions, rules, tiers, quotes; free purchases; portal package selection
**Current activity:** `PHASE_2_ACCEPTED_AND_CLOSED`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B ACCEPTED_AND_CLOSED (DARK — accepted & closed; no cutover; no production iam_v2 use) · 2 ACCEPTED_AND_CLOSED · 3 NOT_STARTED · 4 NOT_STARTED · 5 NOT_STARTED · 6 NOT_STARTED · 7 NOT_STARTED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**iam_v2:** 49 tables, 0 rows, dark; no service routed; no data migration; legacy public schema is the sole production authority.
**Single next authorized action:** Merge PR #4 to master with provenance preserved and run post-merge governance verification (Phase 2 accepted and closed at DARK maturity per D13/T0014). Phase 3 remains NOT_STARTED and unauthorized.
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D13`.
<!-- END GENERATED PROJECT STATE -->

## Provenance
- **SOURCE_COMMIT (clean source this pack was built from):** `17899b8`
- **State transition:** `T0014`  ·  **schema:** `1.0.0`  ·  **build timestamp:** `2026-07-18T17:02:38Z`
- **PROJECT_PACK_EXPORT_COMMIT:** *external* — the commit that commits this pack (recorded in the execution report; a pack never contains the commit that commits it). Verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized)* files; no secrets/DSNs/guest PII.

## Files

| # | Exported filename | Original repository path | Source | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `17899b8` | Entry point | `ce8e922d2bd11fd4b03238b966cb5a99d3a32c147fd1f57ccb1d5fc121bcdf40` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `17899b8` | Project config | `d17529bf991780af605d4807e9e6a79d20b785d3e00ce4c60e650903b7fa7669` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `17899b8` | **Authoritative** *(sanitized)* | `561c8d9971cd720bba7e9115e3cd6d20465bb7c17fce7743b6d6fd2d2f8a38ea` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `17899b8` | **Authoritative** | `3f8f6d5828e98cb73ce5093bd18516c2b7c816ed7c9c816bdd4f5d791c845191` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `17899b8` | **Authoritative (closed phase)** | `d973b0a64967921f057bd956d494c83a872a313c56099aa36aad06c4627d5d05` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `17899b8` | **Authoritative — ACCEPTED_AND_CLOSED at DARK maturity (D11/T0011); PR #2 merged** | `e8e3cd232b9d815e0764714e4d41ce2240b0aef17ea7ac2f8c5346599442b77f` |
| 7 | `Phase1B-Privilege-Matrix.md` | `docs/architecture/Phase1B-Privilege-Matrix.md` | `17899b8` | **Authoritative — as-built grant matrix (Gate P deployed)** | `d7ffc726816e4ed6a677d35cf0b645b79278ea2a48ed4eb561008fcebb640e3d` |
| 8 | `StayConnect-IAM-Phase2-Plan.md` | `docs/architecture/StayConnect-IAM-Phase2-Plan.md` | `17899b8` | **Authoritative — Phase 2 ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014); PR #4 authorized to merge** | `88d96742cb5e4c9099b1f8d1c7a77e900ad425b96de39c5f682b0cbe835e5b99` |
| 9 | `Phase2-Privilege-Matrix.md` | `docs/architecture/Phase2-Privilege-Matrix.md` | `17899b8` | **Authoritative — zero new Phase-2 runtime privilege (live-verified)** | `c4306f39f6aeba8e1b3b86504807f6f22be5a183c0993e240706f6ab8cef3229` |
| 10 | `StayConnect-IAM-Phase2-Software-Gate.md` | `docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md` | `17899b8` | **Authoritative — Phase 2 software-gate evidence (Go + 45 UI tests + build)** | `9cac79718cfedd6ef9d8351c3ffab27af998b5ef3c582919f986591184590cba` |
| 11 | `StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `17899b8` | **Authoritative — Phase 2 live-dark + two-reboot darkness evidence** | `c21e3671a2ae9452298f34b00174ecb47c59ebfa4d422853757fe3eb69163c36` |
| 12 | `StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `17899b8` | **Acceptance record — PRODUCT-OWNER ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014)** | `100f189d332601c0b7b752bac91e6a66e7c1082fac4a54ab402673666779ebe2` |
| 13 | `StayConnect-IAM-Phase2-Final-Report.md` | `docs/reports/StayConnect-IAM-Phase2-Final-Report.md` | `17899b8` | **Authoritative — Phase 2 final report (accepted)** | `ae25395907065907be2504c0b6a5a69084826e8601ac46be01bbd245e1fa9fcb` |
| 14 | `Phase2-change-manifest.md` | `docs/manifests/Phase2-change-manifest.md` | `17899b8` | **Generated — complete Phase 2 changed-file manifest (base..delivery_head; inventory_head provenance)** | `0473ac8b984e36470c51357b989e041e18bf49b981bdb656820cae88a39dac3f` |
| 15 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `17899b8` | **Authoritative (acceptance record)** | `268d38dd93fc8fcc01caab762f6485bf15265a35eff24b0d2032ef17cc80d4c3` |
| 16 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `17899b8` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 17 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `17899b8` | **Permanent rule** | `903c225c2eb4402d923f9f387200d79193d22da26e14f4e6c059a83f80accd2a` |
| 18 | `GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `17899b8` | **Permanent rule** | `f1d467e1d1bc697dc046cc00ffe80f48858951b05a23ce24a75f4654a984dacb` |
| 19 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `17899b8` | Historical snapshot | `3b5cc376451a8bec9907793e0cb5ef70aff231c67b52cf4d05e0e78be53f04e4` |
| 20 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `17899b8` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 21 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `17899b8` | Supporting | `37f2022028148769b861b5d446427404a3aaa9545172ada0b24a60451c36e138` |
| 22 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `17899b8` | Supporting | `7e76f07e06785e58683d95dd5cadbbcc3f7ccbade77df5ab452dbf1c289ed773` |
| 23 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `17899b8` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 24 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `17899b8` | Supporting | `737935641c4d8d0d5de9fd7a7d627aa634d215d2accf9867c76f5d25b658ca55` |

*(MANIFEST is not self-referential.)*

## Content checksum
- pack_content_sha256 is the SHA-256 over the sorted `sha256(file)` lines of all non-MANIFEST pack files (see PACK_SHA256SUMS in the Evidence/Planning packs for physical lists).
