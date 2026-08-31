# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0107 -->
**Current phase:** 7 — Cleanup, final docs, full-system re-acceptance
**Current activity:** `FRESH_PRODUCTION_APPLIANCE_ONBOARDED_PRE_LIVE`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B ACCEPTED_AND_CLOSED (DARK — accepted & closed; no cutover; no production iam_v2 use) · 2 ACCEPTED_AND_CLOSED · 3 ACCEPTED_AND_CLOSED · 4 ACCEPTED_AND_CLOSED · 5 ACCEPTED_AND_CLOSED · 6 ACCEPTED_AND_CLOSED · 7 ACCEPTED_AND_CLOSED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**Fresh Production baseline:** 74 iam_v2 tables, 37 public tables, ZERO identity rows. Factory-clean and current-only: the superseded guest-IAM runtime and schema are ABSENT, so IAM-v2 is structurally the sole guest-IAM authority from first operation. Not a cutover.
**Fresh Production appliance (172.21.60.25, sce):** DEPLOYED factory-clean from `c72b49e`; first-bring-up fixes were then applied live and afterwards committed (`1c55d7f`), so no single commit describes what is running. PRE-LIVE. Enrollment: enrolled; claim: claimed; signed assignment: signed and pinned; licence: licensed. ens192 is configured. Guest traffic: proven end to end, and carrying real traffic. three real room logins have been performed: purchases=3, entitlements=3 (all active), sessions=3 (2 active, 1 ended superseded_on_address). the third, on 2026-08-27t07:02:42z for room 10212, is the first that succeeded from the guest's point of view: enforced in nft and tc, and it has carried 11,461,822 bytes up and 58,629,016 bytes down across 1036 accounting records. accounting_records=1037 in total. no pms posting, payment, settlement or financial egress has ever occurred.; PMS: read-only pms feed, currently disconnected. protel interface ddff5d07 is active but pmsd cannot dial the endpoint since 2026-08-27t16:48:29z (dial_failed, attempt 15,358 as at 2026-08-30t07:42z); runtime reads disconnected / resync_in_progress / continuous, stage receiving, last complete sync 2026-08-27t16:33:10z. no full resync was triggered and no agent action was taken. no pms posting, payment, reversal or fx traffic has ever occurred.; payment/financial: none. Go-Live: not performed and not authorized. Hotel Admin: https://172.21.60.25/
**Development reference appliance (172.21.60.23):** UNTOUCHED by this work and NOT cut over. Retains the historical live-dark runtime its accepted evidence records, including its superseded guest-IAM schema (68 iam_v2 tables live). Reference and evidence only, never an installation source.
**Lifecycle:** PRE-LIVE (D24): no real hotel guest or staff depends on StayConnect for live service yet.
**Single next authorized action:** NONE is authorized beyond ONE controlled real-device Room Login test, which requires its own Product-Owner decision and is BLOCKED while Protel is intentionally stopped: the feed has been DISCONNECTED since 2026-08-27T16:48Z and the 72-hour local-first cache is both elapsed and disabled by a resync attempt that never completed (T0103). The Guest 'My Access' portal view and the Hotel Admin Guest Access operational surface are the next separate feature and are NOT implemented. A separate Product-Owner Go-Live decision remains required and no current authorization supplies it.
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D39`.
<!-- END GENERATED PROJECT STATE -->

## Provenance
- **SOURCE_COMMIT (clean source this pack was built from):** `eb487d27`
- **State transition:** `T0107`  ·  **schema:** `1.0.0`  ·  **build timestamp:** `2026-08-31T23:45:12Z`
- **PROJECT_PACK_EXPORT_COMMIT:** *external* — the commit that commits this pack (recorded in the execution report; a pack never contains the commit that commits it). Verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized)* files; no secrets/DSNs/guest PII.

## Files

| # | Exported filename | Original repository path | Source | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `eb487d27` | Entry point | `170baee9665e9fc57f07ca6b010dccdc18e3578f358e2041f73267a5e3628df2` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `eb487d27` | Project config | `dc23c7696a8f11c143f986bb6dfd2da7d5a2b2ff5ecfef3f9f37bf8e5fd4bb6d` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `eb487d27` | **Authoritative** *(sanitized)* | `401f7cb1ac92765226c5bf460084418dc3d541f4f479fb5a2a5c9276719463e1` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `eb487d27` | **Authoritative** | `c9187078a9011a963bee1d07abcb533402ddd49e4fecf6d99b23610f6d98e2dc` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `eb487d27` | **Authoritative (closed phase)** | `959811eef11a5e5a124bb3948a6864b320d6cf33ea0f0d82be08099142a6f75a` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `eb487d27` | **Authoritative — ACCEPTED_AND_CLOSED at DARK maturity (D11/T0011); PR #2 merged** | `d041eb2e8912ddae137fe39d5d04f4b670afe751fde29089bd662a8aaa511563` |
| 7 | `Phase1B-Privilege-Matrix.md` | `docs/architecture/Phase1B-Privilege-Matrix.md` | `eb487d27` | **Authoritative — as-built grant matrix (Gate P deployed)** | `d7ffc726816e4ed6a677d35cf0b645b79278ea2a48ed4eb561008fcebb640e3d` |
| 8 | `StayConnect-IAM-Phase2-Plan.md` | `docs/architecture/StayConnect-IAM-Phase2-Plan.md` | `eb487d27` | **Authoritative — Phase 2 ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014); PR #4 authorized to merge** | `9ac52c7878618125b5e67dd3561f82b273ae979b8d29427b1445d6b359f8f09c` |
| 9 | `Phase2-Privilege-Matrix.md` | `docs/architecture/Phase2-Privilege-Matrix.md` | `eb487d27` | **Authoritative — zero new Phase-2 runtime privilege (live-verified)** | `c4306f39f6aeba8e1b3b86504807f6f22be5a183c0993e240706f6ab8cef3229` |
| 10 | `StayConnect-IAM-Phase2-Software-Gate.md` | `docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md` | `eb487d27` | **Authoritative — Phase 2 software-gate evidence (Go + 45 UI tests + build)** | `9cac79718cfedd6ef9d8351c3ffab27af998b5ef3c582919f986591184590cba` |
| 11 | `StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `eb487d27` | **Authoritative — Phase 2 live-dark + two-reboot darkness evidence** | `c21e3671a2ae9452298f34b00174ecb47c59ebfa4d422853757fe3eb69163c36` |
| 12 | `StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `eb487d27` | **Acceptance record — PRODUCT-OWNER ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014)** | `fafbe27b5cd44ab4e84aac1bd2a66679c919626dc8e86128dc8c53ba5e050c79` |
| 13 | `StayConnect-IAM-Phase2-Final-Report.md` | `docs/reports/StayConnect-IAM-Phase2-Final-Report.md` | `eb487d27` | **Authoritative — Phase 2 final report (accepted)** | `369cd9fdbe3b410532078ab296a40d3c82ac3f171c6d3f17b3eece5f4da1dbb3` |
| 14 | `Phase2-change-manifest.md` | `docs/manifests/Phase2-change-manifest.md` | `eb487d27` | **Generated — complete Phase 2 changed-file manifest (base..delivery_head; inventory_head provenance)** | `942fa3084e0df8f3d53315a3793be3d5b70e91326cc6cc15eb155a821c3f2008` |
| 15 | `StayConnect-IAM-Phase3-Plan.md` | `docs/architecture/StayConnect-IAM-Phase3-Plan.md` | `eb487d27` | **Authoritative — Phase 3 plan (D14/T0015; ACCEPTED_AND_CLOSED at DARK maturity, D16/T0024; merged D17/T0025)** | `948e6e185744ae5d214c671276c8f833c608613b69e847f4d1c6d593dd54efee` |
| 16 | `Phase3-Privilege-Matrix.md` | `docs/architecture/Phase3-Privilege-Matrix.md` | `eb487d27` | **Authoritative — Phase 3 privilege matrix (PRODUCTION_IAM_V2_DML: NONE; DARK)** | `801ab8f97460ac757ef593ca1e3ecf5676749d3f7b8257440e66bf36e0a3ee25` |
| 17 | `Phase3-change-manifest.md` | `docs/manifests/Phase3-change-manifest.md` | `eb487d27` | **Generated — complete Phase 3 changed-file manifest (base..delivery_head; inventory_head provenance)** | `eadebad5aabf83285aeb962e87371e4a80fd1fd2fc4921ac5461fdc7e8d54823` |
| 18 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `eb487d27` | **Authoritative (acceptance record)** | `268d38dd93fc8fcc01caab762f6485bf15265a35eff24b0d2032ef17cc80d4c3` |
| 19 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `eb487d27` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 20 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `eb487d27` | **Permanent rule** | `903c225c2eb4402d923f9f387200d79193d22da26e14f4e6c059a83f80accd2a` |
| 21 | `GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `eb487d27` | **Permanent rule** | `f1d467e1d1bc697dc046cc00ffe80f48858951b05a23ce24a75f4654a984dacb` |
| 22 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `eb487d27` | Historical snapshot | `46ef42e87144eb6df1c0d89db6e72e96c9fda2a8ec3f423fbf91aedc5be029ef` |
| 23 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `eb487d27` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 24 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `eb487d27` | Supporting | `52da8310e31562fe75b8354aa0c9e39bd1829236cfab83ffa2b37f38a4cd8665` |
| 25 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `eb487d27` | Supporting | `7e76f07e06785e58683d95dd5cadbbcc3f7ccbade77df5ab452dbf1c289ed773` |
| 26 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `eb487d27` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 27 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `eb487d27` | Supporting | `4b7346d028992205104071ab66b2c4a75b6c5be01eed566efe6305205001a913` |

*(MANIFEST is not self-referential.)*

## Content checksum
- pack_content_sha256 is the SHA-256 over the sorted `sha256(file)` lines of all non-MANIFEST pack files (see PACK_SHA256SUMS in the Evidence/Planning packs for physical lists).
