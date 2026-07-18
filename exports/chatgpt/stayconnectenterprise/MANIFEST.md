# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0013 -->
**Current phase:** 2 — Packages, revisions, rules, tiers, quotes; free purchases; portal package selection
**Current activity:** `PHASE_2_LIVE_DARK_DEPLOYED_PENDING_PO_ACCEPTANCE`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B ACCEPTED_AND_CLOSED (DARK — accepted & closed; no cutover; no production iam_v2 use) · 2 IN_PROGRESS · 3 NOT_STARTED · 4 NOT_STARTED · 5 NOT_STARTED · 6 NOT_STARTED · 7 NOT_STARTED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**iam_v2:** 49 tables, 0 rows, dark; no service routed; no data migration; legacy public schema is the sole production authority.
**Single next authorized action:** Return the single final Phase-2 report for one Product-Owner acceptance decision at verified DARK maturity. No paid access, no PMS settlement, no iam_v2 cutover, no Phase 3.
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D12`.
<!-- END GENERATED PROJECT STATE -->

## Provenance
- **SOURCE_COMMIT (clean source this pack was built from):** `98df0aa`
- **State transition:** `T0013`  ·  **schema:** `1.0.0`  ·  **build timestamp:** `2026-07-18T14:54:10Z`
- **PROJECT_PACK_EXPORT_COMMIT:** *external* — the commit that commits this pack (recorded in the execution report; a pack never contains the commit that commits it). Verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized)* files; no secrets/DSNs/guest PII.

## Files

| # | Exported filename | Original repository path | Source | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `98df0aa` | Entry point | `7d43996ba057fa48cb22b56dd15b54b3b383837704ae2749af84baec6fa3eceb` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `98df0aa` | Project config | `834c4569c4736efb8a951950aaae10f8631a7cf364bc4c445898abbe15a8230a` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `98df0aa` | **Authoritative** *(sanitized)* | `f2f4e904ed7683fad7b12cab6d8f552418e5968107b1b8679f2127916fa351a5` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `98df0aa` | **Authoritative** | `8602d18bc8398a971438edf9567de1b2c39d6d67aa3e008460c284bae4b06307` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `98df0aa` | **Authoritative (closed phase)** | `f7cd65f6ffbca88010d517f6b4e2424763da5f74b565df2db6a7d674896e1b07` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `98df0aa` | **Authoritative — ACCEPTED_AND_CLOSED at DARK maturity (D11/T0011); PR #2 merged** | `8d2540a2b6e30cbba100d580138fac413344a6b144f3e128c9b53cdc9c8c816a` |
| 7 | `Phase1B-Privilege-Matrix.md` | `docs/architecture/Phase1B-Privilege-Matrix.md` | `98df0aa` | **Authoritative — as-built grant matrix (Gate P deployed)** | `d7ffc726816e4ed6a677d35cf0b645b79278ea2a48ed4eb561008fcebb640e3d` |
| 8 | `StayConnect-IAM-Phase2-Plan.md` | `docs/architecture/StayConnect-IAM-Phase2-Plan.md` | `98df0aa` | **Authoritative — Phase 2 implemented + live-dark deployed + reboot-verified; pending PO acceptance (D12/T0012, T0013)** | `f0855b7febb619f636af48c058d74df4acb912e3e9c5a72e53d2f29f2c1cee06` |
| 9 | `Phase2-Privilege-Matrix.md` | `docs/architecture/Phase2-Privilege-Matrix.md` | `98df0aa` | **Authoritative — zero new Phase-2 runtime privilege (live-verified)** | `c4306f39f6aeba8e1b3b86504807f6f22be5a183c0993e240706f6ab8cef3229` |
| 10 | `StayConnect-IAM-Phase2-Software-Gate.md` | `docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md` | `98df0aa` | **Authoritative — Phase 2 software-gate evidence (Go + 45 UI tests + build)** | `9cac79718cfedd6ef9d8351c3ffab27af998b5ef3c582919f986591184590cba` |
| 11 | `StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `98df0aa` | **Authoritative — Phase 2 live-dark + two-reboot darkness evidence** | `c21e3671a2ae9452298f34b00174ecb47c59ebfa4d422853757fe3eb69163c36` |
| 12 | `StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `98df0aa` | **Acceptance CANDIDATE — pending one PO decision (not accepted)** | `e98eafdcfeaa91fa2b507be5c11a03e4a05cd9a8dc8a49e447d8aa41f41a73cf` |
| 13 | `StayConnect-IAM-Phase2-Final-Report.md` | `docs/reports/StayConnect-IAM-Phase2-Final-Report.md` | `98df0aa` | **Authoritative — Phase 2 final report** | `4ca0f5d45a7f8c3e0636fff34abba12a58d8d42d0bb0366278d7dabd53b01fb7` |
| 14 | `Phase2-change-manifest.md` | `docs/manifests/Phase2-change-manifest.md` | `98df0aa` | **Generated — Phase 2 changed-file manifest (acceptance-candidate HEAD)** | `edec2bd4e2fbbcf35a64dcd3b0a61438c8aab0402a81b6bf3c386277eeb90dc6` |
| 15 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `98df0aa` | **Authoritative (acceptance record)** | `268d38dd93fc8fcc01caab762f6485bf15265a35eff24b0d2032ef17cc80d4c3` |
| 16 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `98df0aa` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 17 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `98df0aa` | **Permanent rule** | `35a4f1d368ade486dff1172b6d4f48355fdc9422bbfcf1e9e0b8f997c1f54a87` |
| 18 | `GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `98df0aa` | **Permanent rule** | `78ca0e52167890fe6ffd23a48cf27a08072783dd0c43c1c15a8dd096c8fc6820` |
| 19 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `98df0aa` | Historical snapshot | `3b5cc376451a8bec9907793e0cb5ef70aff231c67b52cf4d05e0e78be53f04e4` |
| 20 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `98df0aa` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 21 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `98df0aa` | Supporting | `37f2022028148769b861b5d446427404a3aaa9545172ada0b24a60451c36e138` |
| 22 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `98df0aa` | Supporting | `7e76f07e06785e58683d95dd5cadbbcc3f7ccbade77df5ab452dbf1c289ed773` |
| 23 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `98df0aa` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 24 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `98df0aa` | Supporting | `737935641c4d8d0d5de9fd7a7d627aa634d215d2accf9867c76f5d25b658ca55` |

*(MANIFEST is not self-referential.)*

## Content checksum
- pack_content_sha256 is the SHA-256 over the sorted `sha256(file)` lines of all non-MANIFEST pack files (see PACK_SHA256SUMS in the Evidence/Planning packs for physical lists).
