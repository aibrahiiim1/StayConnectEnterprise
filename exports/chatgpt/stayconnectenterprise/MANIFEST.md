# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0007 -->
**Current phase:** 1B — Credential/identity/auth-context (DARK)
**Current activity:** `PHASE_1B_PLAN_CORRECTION_PENDING_APPROVAL`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B PLANNING (NOT implemented) · 2 NOT_STARTED · 3 NOT_STARTED · 4 NOT_STARTED · 5 NOT_STARTED · 6 NOT_STARTED · 7 NOT_STARTED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**iam_v2:** 49 tables, 0 rows, dark; no service routed; no data migration; legacy public schema is the sole production authority.
**Single next authorized action:** Product-Owner approval or rejection of the corrected Phase 1B plan
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D9`.
<!-- END GENERATED PROJECT STATE -->

## Provenance
- **SOURCE_COMMIT (clean source this pack was built from):** `956608a`
- **State transition:** `T0007`  ·  **schema:** `1.0.0`  ·  **build timestamp:** `2026-07-16T21:51:54Z`
- **PROJECT_PACK_EXPORT_COMMIT:** *external* — the commit that commits this pack (recorded in the execution report; a pack never contains the commit that commits it). Verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized)* files; no secrets/DSNs/guest PII.

## Files

| # | Exported filename | Original repository path | Source | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `956608a` | Entry point | `daa6bed54c6cdc77f44269079b4bc9ccc897a6b37e309d524ef21086757f4b01` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `956608a` | Project config | `2fb3b077c9c79d45bbc7fb2d9d5a3faff54a9653eeceabcef0388a67bbd27107` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `956608a` | **Authoritative** *(sanitized)* | `d8881f424604c7b396649b8fc7a6b596487b0c45bc0e95797e7db2094f25ab39` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `956608a` | **Authoritative** | `2015c61908436c8cb31875a08b16d83d9f3fe96b05ea88e4d242d12bd5f29117` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `956608a` | **Authoritative (closed phase)** | `3a03cd33897f44be66c5e1fb4201742245c9f33412e0dbe9a3d2d6974158271e` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `956608a` | **Authoritative (planning-only)** | `5ba7c282fc53c20f05fbdbcc1df53578567693882515eeddc911256c74609f2e` |
| 7 | `Phase1B-Privilege-Matrix.md` | `docs/architecture/Phase1B-Privilege-Matrix.md` | `956608a` | **Authoritative (planning-only) — grant matrix** | `ebec050db07010ca998b2ddf6680aea76ed70fe5df7483615e1495835ff7d215` |
| 8 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `956608a` | **Authoritative (acceptance record)** | `268d38dd93fc8fcc01caab762f6485bf15265a35eff24b0d2032ef17cc80d4c3` |
| 9 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `956608a` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 10 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `956608a` | **Permanent rule** | `c5e6f62a814b3c404c276bc2e1dffeb77673eb2f92822802e05ea65fca6f9bfe` |
| 11 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `956608a` | Historical snapshot | `3b5cc376451a8bec9907793e0cb5ef70aff231c67b52cf4d05e0e78be53f04e4` |
| 12 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `956608a` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 13 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `956608a` | Supporting | `37f2022028148769b861b5d446427404a3aaa9545172ada0b24a60451c36e138` |
| 14 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `956608a` | Supporting | `7e76f07e06785e58683d95dd5cadbbcc3f7ccbade77df5ab452dbf1c289ed773` |
| 15 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `956608a` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 16 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `956608a` | Supporting | `737935641c4d8d0d5de9fd7a7d627aa634d215d2accf9867c76f5d25b658ca55` |

*(MANIFEST is not self-referential.)*

## Content checksum
- pack_content_sha256 is the SHA-256 over the sorted `sha256(file)` lines of all non-MANIFEST pack files (see PACK_SHA256SUMS in the Evidence/Planning packs for physical lists).
