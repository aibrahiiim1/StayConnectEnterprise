# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0009 -->
**Current phase:** 1B — Credential/identity/auth-context (DARK)
**Current activity:** `PHASE_1B_IMPLEMENTATION_IN_PROGRESS`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B IN_PROGRESS (DARK — implementation in progress; no production iam_v2 use) · 2 NOT_STARTED · 3 NOT_STARTED · 4 NOT_STARTED · 5 NOT_STARTED · 6 NOT_STARTED · 7 NOT_STARTED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**iam_v2:** 49 tables, 0 rows, dark; no service routed; no data migration; legacy public schema is the sole production authority.
**Single next authorized action:** complete Phase 1B execution and live-dark verification
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D10`.
<!-- END GENERATED PROJECT STATE -->

## Provenance
- **SOURCE_COMMIT (clean source this pack was built from):** `c248cdb`
- **State transition:** `T0009`  ·  **schema:** `1.0.0`  ·  **build timestamp:** `2026-07-17T16:33:19Z`
- **PROJECT_PACK_EXPORT_COMMIT:** *external* — the commit that commits this pack (recorded in the execution report; a pack never contains the commit that commits it). Verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized)* files; no secrets/DSNs/guest PII.

## Files

| # | Exported filename | Original repository path | Source | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `c248cdb` | Entry point | `43008f48584b97506c9a672b4893ff33fec79e3cb1fb12c37e2f43124502561e` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `c248cdb` | Project config | `86c6259b53dc53009a443af59b6b3fa4087050ff5a93a109668ae823fb6b052e` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `c248cdb` | **Authoritative** *(sanitized)* | `47fa9ae14b36439627065666fe36d6bbabcc06d878a83812125f7b2d02449239` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `c248cdb` | **Authoritative** | `333586b11b946107bec478069a16486d5809b645f941cbdfbf90cc0360391847` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `c248cdb` | **Authoritative (closed phase)** | `80723d7bdf41274ef1a2b5f133e86e48b5318b09aee96c5aba07c5accb91ace1` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `c248cdb` | **Authoritative (planning-only)** | `85245e30703da0605d6fedfba9b7119a79716d30006aea67c641016fd4f3e4b2` |
| 7 | `Phase1B-Privilege-Matrix.md` | `docs/architecture/Phase1B-Privilege-Matrix.md` | `c248cdb` | **Authoritative (planning-only) — grant matrix** | `99517e62a20c5702767064864c118d6877b35c6e346f160bd3615b4d0765715b` |
| 8 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `c248cdb` | **Authoritative (acceptance record)** | `268d38dd93fc8fcc01caab762f6485bf15265a35eff24b0d2032ef17cc80d4c3` |
| 9 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `c248cdb` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 10 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `c248cdb` | **Permanent rule** | `35a4f1d368ade486dff1172b6d4f48355fdc9422bbfcf1e9e0b8f997c1f54a87` |
| 11 | `GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `c248cdb` | **Permanent rule** | `78ca0e52167890fe6ffd23a48cf27a08072783dd0c43c1c15a8dd096c8fc6820` |
| 12 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `c248cdb` | Historical snapshot | `3b5cc376451a8bec9907793e0cb5ef70aff231c67b52cf4d05e0e78be53f04e4` |
| 13 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `c248cdb` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 14 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `c248cdb` | Supporting | `37f2022028148769b861b5d446427404a3aaa9545172ada0b24a60451c36e138` |
| 15 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `c248cdb` | Supporting | `7e76f07e06785e58683d95dd5cadbbcc3f7ccbade77df5ab452dbf1c289ed773` |
| 16 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `c248cdb` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 17 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `c248cdb` | Supporting | `737935641c4d8d0d5de9fd7a7d627aa634d215d2accf9867c76f5d25b658ca55` |

*(MANIFEST is not self-referential.)*

## Content checksum
- pack_content_sha256 is the SHA-256 over the sorted `sha256(file)` lines of all non-MANIFEST pack files (see PACK_SHA256SUMS in the Evidence/Planning packs for physical lists).
