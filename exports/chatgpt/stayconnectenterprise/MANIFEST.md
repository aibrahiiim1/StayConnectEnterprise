# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0120 -->
**Current phase:** 7 — Cleanup, final docs, full-system re-acceptance
**Current activity:** `FRESH_PRODUCTION_APPLIANCE_ONBOARDED_PRE_LIVE`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B ACCEPTED_AND_CLOSED (DARK — accepted & closed; no cutover; no production iam_v2 use) · 2 ACCEPTED_AND_CLOSED · 3 ACCEPTED_AND_CLOSED · 4 ACCEPTED_AND_CLOSED · 5 ACCEPTED_AND_CLOSED · 6 ACCEPTED_AND_CLOSED · 7 ACCEPTED_AND_CLOSED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**Fresh Production baseline:** 74 iam_v2 tables, 37 public tables, ZERO identity rows. Factory-clean and current-only: the superseded guest-IAM runtime and schema are ABSENT, so IAM-v2 is structurally the sole guest-IAM authority from first operation. Not a cutover.
**Fresh Production appliance (172.21.60.25, sce):** DEPLOYED factory-clean from `c72b49e`; every service binary now runs repository head `480fd213`, each digest verified on the appliance against the build host. PRE-LIVE. Enrollment: enrolled; claim: claimed; signed assignment: signed and pinned; licence: licensed. ens192 is configured. Guest traffic: proven end to end through the real captive portal, and data quota is now proven too; no session is live. five controlled real room logins: purchases=5, entitlements=5 - one terminated for data quota exhaustion and the other four still live; sessions=5 - all five ended. the fifth, on 2026-09-06t01:33:09z for room 3108, was the authorized data-quota acceptance: it carried 100,108,666 bytes across 412 accounting records and its entitlement terminated at the crossing sample 01:40:12.423805z for reason data, ending the session and clearing the kernel in the same instant. accounting_records=1808 in total. no pms posting, payment, settlement or financial egress has ever occurred.; PMS: read-only pms feed, live and healthy. the product owner restored the protel socket on 2026-09-05 and pmsd recovered automatically in-process; the feed has published normally ever since, most recently generation 195 at 2026-09-06t00:41:40.824553z. runtime reads connected / in_sync / continuous, stage complete, roster 1159 stays and 829 in_house. no full resync was ever requested - the runtime carries no resync command id - and no pms configuration was changed. no pms posting, payment, reversal or fx traffic has ever occurred.; payment/financial: none. Go-Live: not performed and not authorized. Hotel Admin: https://172.21.60.25/
**Development reference appliance (172.21.60.23):** UNTOUCHED by this work and NOT cut over. Retains the historical live-dark runtime its accepted evidence records, including its superseded guest-IAM schema (68 iam_v2 tables live). Reference and evidence only, never an installation source.
**Lifecycle:** PRE-LIVE (D24): no real hotel guest or staff depends on StayConnect for live service yet.
**Single next authorized action:** NONE is authorized. The package-eligibility / per-stay-night PRE-LIVE milestone is accepted and closed (D40/T0120) and no further action follows from it: no Room Login, no PMS traffic, no PMS configuration change, no financial traffic, no redeployment and no Go-Live. ONE operational action is RESERVED and explicitly NOT yet authorized: before Protel/PMS is intentionally returned to operational use, the Product Owner must separately authorize deploying/restarting the staged pmsd runtime. The pmsd binary built from the accepted delivery HEAD is INSTALLED on the appliance but the RUNNING pmsd process was deliberately not restarted, because restarting the PMS connector was out of scope while Protel is intentionally stopped. Until that restart, checkout-boundary processing runs the previous binary, which reads the pinned plan quota directly instead of COALESCE(entitlement snapshot, pinned plan quota). That difference is inert today -- every existing entitlement carries a NULL snapshot, so both forms return the same number -- and becomes material only once a PER_STAY_NIGHT entitlement exists AND reaches a checkout boundary.
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D40`.
<!-- END GENERATED PROJECT STATE -->

## Provenance
- **SOURCE_COMMIT (clean source this pack was built from):** `c5438ef7`
- **State transition:** `T0120`  ·  **schema:** `1.0.0`  ·  **build timestamp:** `2026-09-08T06:57:54Z`
- **PROJECT_PACK_EXPORT_COMMIT:** *external* — the commit that commits this pack (recorded in the execution report; a pack never contains the commit that commits it). Verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized)* files; no secrets/DSNs/guest PII.

## Files

| # | Exported filename | Original repository path | Source | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `c5438ef7` | Entry point | `5fde8320e3269a4b6cc97e8dc4dc6f7dac5653d47c6b3c0d9945c43c98f1c9b6` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `c5438ef7` | Project config | `3336524155e23aad0d88eb7c1ea1adc29f11c48ce5185ea54dde0d2458d3aeea` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `c5438ef7` | **Authoritative** *(sanitized)* | `b468dc0d0dde0c94ac7dd5cddf61b7f4216ae0aaa9afad9ad2412e92433a2618` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `c5438ef7` | **Authoritative** | `c82bec44b7e604cacd7d04d87b6fe416a3e7db0e92fc15475f1d782e018f6f67` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `c5438ef7` | **Authoritative (closed phase)** | `36e2b7cc8587252e9b50d367ca1b287e9996f975ef0b1a89f08dfb08b91824f9` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `c5438ef7` | **Authoritative — ACCEPTED_AND_CLOSED at DARK maturity (D11/T0011); PR #2 merged** | `fa7823dcd0cde038d5d5a92ac47a315616bcec7f79fa770297325ec99869c4ac` |
| 7 | `Phase1B-Privilege-Matrix.md` | `docs/architecture/Phase1B-Privilege-Matrix.md` | `c5438ef7` | **Authoritative — as-built grant matrix (Gate P deployed)** | `d7ffc726816e4ed6a677d35cf0b645b79278ea2a48ed4eb561008fcebb640e3d` |
| 8 | `StayConnect-IAM-Phase2-Plan.md` | `docs/architecture/StayConnect-IAM-Phase2-Plan.md` | `c5438ef7` | **Authoritative — Phase 2 ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014); PR #4 authorized to merge** | `9ac52c7878618125b5e67dd3561f82b273ae979b8d29427b1445d6b359f8f09c` |
| 9 | `Phase2-Privilege-Matrix.md` | `docs/architecture/Phase2-Privilege-Matrix.md` | `c5438ef7` | **Authoritative — zero new Phase-2 runtime privilege (live-verified)** | `c4306f39f6aeba8e1b3b86504807f6f22be5a183c0993e240706f6ab8cef3229` |
| 10 | `StayConnect-IAM-Phase2-Software-Gate.md` | `docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md` | `c5438ef7` | **Authoritative — Phase 2 software-gate evidence (Go + 45 UI tests + build)** | `9cac79718cfedd6ef9d8351c3ffab27af998b5ef3c582919f986591184590cba` |
| 11 | `StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `c5438ef7` | **Authoritative — Phase 2 live-dark + two-reboot darkness evidence** | `c21e3671a2ae9452298f34b00174ecb47c59ebfa4d422853757fe3eb69163c36` |
| 12 | `StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `c5438ef7` | **Acceptance record — PRODUCT-OWNER ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014)** | `fafbe27b5cd44ab4e84aac1bd2a66679c919626dc8e86128dc8c53ba5e050c79` |
| 13 | `StayConnect-IAM-Phase2-Final-Report.md` | `docs/reports/StayConnect-IAM-Phase2-Final-Report.md` | `c5438ef7` | **Authoritative — Phase 2 final report (accepted)** | `369cd9fdbe3b410532078ab296a40d3c82ac3f171c6d3f17b3eece5f4da1dbb3` |
| 14 | `Phase2-change-manifest.md` | `docs/manifests/Phase2-change-manifest.md` | `c5438ef7` | **Generated — complete Phase 2 changed-file manifest (base..delivery_head; inventory_head provenance)** | `942fa3084e0df8f3d53315a3793be3d5b70e91326cc6cc15eb155a821c3f2008` |
| 15 | `StayConnect-IAM-Phase3-Plan.md` | `docs/architecture/StayConnect-IAM-Phase3-Plan.md` | `c5438ef7` | **Authoritative — Phase 3 plan (D14/T0015; ACCEPTED_AND_CLOSED at DARK maturity, D16/T0024; merged D17/T0025)** | `0e5facf374ef7de11568f5e674478bfc82d4c79df920f0fb84e2e09eb4cf9cc3` |
| 16 | `Phase3-Privilege-Matrix.md` | `docs/architecture/Phase3-Privilege-Matrix.md` | `c5438ef7` | **Authoritative — Phase 3 privilege matrix (PRODUCTION_IAM_V2_DML: NONE; DARK)** | `801ab8f97460ac757ef593ca1e3ecf5676749d3f7b8257440e66bf36e0a3ee25` |
| 17 | `Phase3-change-manifest.md` | `docs/manifests/Phase3-change-manifest.md` | `c5438ef7` | **Generated — complete Phase 3 changed-file manifest (base..delivery_head; inventory_head provenance)** | `eadebad5aabf83285aeb962e87371e4a80fd1fd2fc4921ac5461fdc7e8d54823` |
| 18 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `c5438ef7` | **Authoritative (acceptance record)** | `268d38dd93fc8fcc01caab762f6485bf15265a35eff24b0d2032ef17cc80d4c3` |
| 19 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `c5438ef7` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 20 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `c5438ef7` | **Permanent rule** | `dcb3529504c2f7dca0a36f003190641957a19d1fba15d8e08d8e143f96173530` |
| 21 | `GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `c5438ef7` | **Permanent rule** | `9792c04b49f765ee83a1edf5b2c6be0c5967be3f9e07a960f3e4cb0a65ccd0fd` |
| 22 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `c5438ef7` | Historical snapshot | `d4181dac51deda09092043192aca8049e0f6293f5169359a0aaa2afd231a2e12` |
| 23 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `c5438ef7` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 24 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `c5438ef7` | Supporting | `52da8310e31562fe75b8354aa0c9e39bd1829236cfab83ffa2b37f38a4cd8665` |
| 25 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `c5438ef7` | Supporting | `4afe74fb71023532c78f2e015e2375e474cfeab26d1bceb7b445ee0c223568f7` |
| 26 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `c5438ef7` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 27 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `c5438ef7` | Supporting | `4b7346d028992205104071ab66b2c4a75b6c5be01eed566efe6305205001a913` |

*(MANIFEST is not self-referential.)*

## Content checksum
- pack_content_sha256 is the SHA-256 over the sorted `sha256(file)` lines of all non-MANIFEST pack files (see PACK_SHA256SUMS in the Evidence/Planning packs for physical lists).
