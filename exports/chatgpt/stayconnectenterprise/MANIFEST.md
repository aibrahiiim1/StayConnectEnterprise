# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0010 -->
**Current phase:** 1B — Credential/identity/auth-context (DARK)
**Current activity:** `PHASE_1B_LIVE_DARK_DEPLOYED_PENDING_PO_ACCEPTANCE`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B IN_PROGRESS (DARK — implementation in progress; no production iam_v2 use) · 2 NOT_STARTED · 3 NOT_STARTED · 4 NOT_STARTED · 5 NOT_STARTED · 6 NOT_STARTED · 7 NOT_STARTED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**iam_v2:** 49 tables, 0 rows, dark; no service routed; no data migration; legacy public schema is the sole production authority.
**Single next authorized action:** Product-Owner review/acceptance of Phase 1B live-dark deployment (transition T0010). Enabling any dark feature (throttle/OTP-HMAC/IAM-v2) or any iam_v2 cutover is a SEPARATE, explicitly-authorized step.
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D10`.
<!-- END GENERATED PROJECT STATE -->

## Provenance
- **SOURCE_COMMIT (clean source this pack was built from):** `c4d2819`
- **State transition:** `T0010`  ·  **schema:** `1.0.0`  ·  **build timestamp:** `2026-07-17T21:28:04Z`
- **PROJECT_PACK_EXPORT_COMMIT:** *external* — the commit that commits this pack (recorded in the execution report; a pack never contains the commit that commits it). Verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized)* files; no secrets/DSNs/guest PII.

## Files

| # | Exported filename | Original repository path | Source | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `c4d2819` | Entry point | `b4898d1afa48541c5bbd46861ca1afc33b3b26ad6280b597fc86ab12cd7a7bc3` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `c4d2819` | Project config | `beb6917f14735456da411dc5ad7ae9512bca3b56270ee171e2bd1f8ec45c6d3d` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `c4d2819` | **Authoritative** *(sanitized)* | `11a0ecec99e987a2a91a5dd98514941ed2047791ccb2bb9fb66d69b34ab671ed` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `c4d2819` | **Authoritative** | `f5a27f42a3c97a19f9e8183a8b59fcc36c9160a10524658368e950088cb708b7` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `c4d2819` | **Authoritative (closed phase)** | `8d5ae6bc433bda8bc9403f713e24e9954fb1cce83521cda13acb32eed71e9099` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `c4d2819` | **Authoritative (planning-only)** | `90fb6fd096c2a6b06fd5534729dd2c40dde006a10e7cb7ad5bc6e270e4cb4069` |
| 7 | `Phase1B-Privilege-Matrix.md` | `docs/architecture/Phase1B-Privilege-Matrix.md` | `c4d2819` | **Authoritative (planning-only) — grant matrix** | `5a11401603361bf9119b185e09da2d2e6d51189782eac50cff8fabaf13696220` |
| 8 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `c4d2819` | **Authoritative (acceptance record)** | `268d38dd93fc8fcc01caab762f6485bf15265a35eff24b0d2032ef17cc80d4c3` |
| 9 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `c4d2819` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 10 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `c4d2819` | **Permanent rule** | `35a4f1d368ade486dff1172b6d4f48355fdc9422bbfcf1e9e0b8f997c1f54a87` |
| 11 | `GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `c4d2819` | **Permanent rule** | `78ca0e52167890fe6ffd23a48cf27a08072783dd0c43c1c15a8dd096c8fc6820` |
| 12 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `c4d2819` | Historical snapshot | `3b5cc376451a8bec9907793e0cb5ef70aff231c67b52cf4d05e0e78be53f04e4` |
| 13 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `c4d2819` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 14 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `c4d2819` | Supporting | `37f2022028148769b861b5d446427404a3aaa9545172ada0b24a60451c36e138` |
| 15 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `c4d2819` | Supporting | `7e76f07e06785e58683d95dd5cadbbcc3f7ccbade77df5ab452dbf1c289ed773` |
| 16 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `c4d2819` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 17 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `c4d2819` | Supporting | `737935641c4d8d0d5de9fd7a7d627aa634d215d2accf9867c76f5d25b658ca55` |

*(MANIFEST is not self-referential.)*

## Content checksum
- pack_content_sha256 is the SHA-256 over the sorted `sha256(file)` lines of all non-MANIFEST pack files (see PACK_SHA256SUMS in the Evidence/Planning packs for physical lists).
