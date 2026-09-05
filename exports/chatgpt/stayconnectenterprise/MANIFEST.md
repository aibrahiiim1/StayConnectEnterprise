# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0117 -->
**Current phase:** 7 — Cleanup, final docs, full-system re-acceptance
**Current activity:** `FRESH_PRODUCTION_APPLIANCE_ONBOARDED_PRE_LIVE`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B ACCEPTED_AND_CLOSED (DARK — accepted & closed; no cutover; no production iam_v2 use) · 2 ACCEPTED_AND_CLOSED · 3 ACCEPTED_AND_CLOSED · 4 ACCEPTED_AND_CLOSED · 5 ACCEPTED_AND_CLOSED · 6 ACCEPTED_AND_CLOSED · 7 ACCEPTED_AND_CLOSED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**Fresh Production baseline:** 74 iam_v2 tables, 37 public tables, ZERO identity rows. Factory-clean and current-only: the superseded guest-IAM runtime and schema are ABSENT, so IAM-v2 is structurally the sole guest-IAM authority from first operation. Not a cutover.
**Fresh Production appliance (172.21.60.25, sce):** DEPLOYED factory-clean from `c72b49e`; every service binary now runs repository head `29a6b21f`, each digest verified on the appliance against the build host. PRE-LIVE. Enrollment: enrolled; claim: claimed; signed assignment: signed and pinned; licence: licensed. ens192 is configured. Guest traffic: proven end to end through the real captive portal; no session is now live. four controlled real room logins have been performed: purchases=4, entitlements=4 (all active), sessions=4 - all four ended. the fourth, on 2026-09-05t14:59:54z for room 1104, is the first to prove the guest-visible browser -> portald -> scd path rather than the resolve socket, and it was enforced in nft and tc within about a second: one authorization element for (br-g-00d1fa1a . 192.168.77.105) and one per_device htb class at 2mbit. it carried real internet traffic - 15,183,911 bytes down and 20,508,810 bytes up across 359 accounting records - on a zero-price package whose settlement method is not_required and which carries no settlement mapping. it ran 1h39m and ended by itself at 16:38:38.804z with address_no_longer_owned, 804 milliseconds after its dhcp lease on 192.168.77.105 expired - the address-ownership rule working as designed. final totals for that session: 359 accounting records, 15,183,911 bytes down and 20,508,810 bytes up. its entitlement remains active (ending a session does not consume one). all four sessions are now ended, the kernel is empty and netd is converged with 0 managed classes. nothing was deleted or edited to tidy up. accounting_records=1396 in total. no pms posting, payment, settlement or financial egress has ever occurred.; PMS: read-only pms feed, live and healthy. the product owner restored the protel socket on 2026-09-05 and pmsd recovered automatically in-process: interface ddff5d07 connected at 14:50:03.827z, a resync self-requested 2ms later, and generation 184 published complete at 14:50:41.086z with a claimable pending backlog of zero. runtime reads connected / in_sync / continuous, stage complete, roster 1141 stays and 829 in_house. no full resync was requested and no pms configuration was changed. no pms posting, payment, reversal or fx traffic has ever occurred.; payment/financial: none. Go-Live: not performed and not authorized. Hotel Admin: https://172.21.60.25/
**Development reference appliance (172.21.60.23):** UNTOUCHED by this work and NOT cut over. Retains the historical live-dark runtime its accepted evidence records, including its superseded guest-IAM schema (68 iam_v2 tables live). Reference and evidence only, never an installation source.
**Lifecycle:** PRE-LIVE (D24): no real hotel guest or staff depends on StayConnect for live service yet.
**Single next authorized action:** NONE is authorized. The controlled real-device Room Login this action previously reserved has been PERFORMED and ACCEPTED (2026-09-05T14:59:54Z, room 1104, recorded at T0115), so no further authentication, Room Login or guest test may be performed without a new Product-Owner decision. The PMS feed is live and healthy at generation 184 and Room authentication is READY rather than blocked. A separate Product-Owner Go-Live decision remains required and no current authorization supplies it.
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D39`.
<!-- END GENERATED PROJECT STATE -->

## Provenance
- **SOURCE_COMMIT (clean source this pack was built from):** `a9335553`
- **State transition:** `T0117`  ·  **schema:** `1.0.0`  ·  **build timestamp:** `2026-09-05T23:27:41Z`
- **PROJECT_PACK_EXPORT_COMMIT:** *external* — the commit that commits this pack (recorded in the execution report; a pack never contains the commit that commits it). Verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized)* files; no secrets/DSNs/guest PII.

## Files

| # | Exported filename | Original repository path | Source | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `a9335553` | Entry point | `d4f760fa62ab91073d28eb0f8d8c8f3c2d8b620190bdd1e7a6a3d6835244858e` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `a9335553` | Project config | `76163628b4b90bb3c9ed083dd66968263df4361bb989d5513945b95aeaeacd6c` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `a9335553` | **Authoritative** *(sanitized)* | `b2234baa173c965eb315cc19ebb3330c280d28e68106c6e05fe7e2ac05bc5173` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `a9335553` | **Authoritative** | `00fa377e86e49eda1c23e2e57246604351b172fe55b3f91c8354caf9735b3d64` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `a9335553` | **Authoritative (closed phase)** | `66f3739c12f298d95e4b7e27c4888f2fb50f6663696674d462ea7e016baafe48` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `a9335553` | **Authoritative — ACCEPTED_AND_CLOSED at DARK maturity (D11/T0011); PR #2 merged** | `29795fddb53330b7bbcc4fc769960a808a556db9cccb78c637d9cab1a6c0d970` |
| 7 | `Phase1B-Privilege-Matrix.md` | `docs/architecture/Phase1B-Privilege-Matrix.md` | `a9335553` | **Authoritative — as-built grant matrix (Gate P deployed)** | `d7ffc726816e4ed6a677d35cf0b645b79278ea2a48ed4eb561008fcebb640e3d` |
| 8 | `StayConnect-IAM-Phase2-Plan.md` | `docs/architecture/StayConnect-IAM-Phase2-Plan.md` | `a9335553` | **Authoritative — Phase 2 ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014); PR #4 authorized to merge** | `9ac52c7878618125b5e67dd3561f82b273ae979b8d29427b1445d6b359f8f09c` |
| 9 | `Phase2-Privilege-Matrix.md` | `docs/architecture/Phase2-Privilege-Matrix.md` | `a9335553` | **Authoritative — zero new Phase-2 runtime privilege (live-verified)** | `c4306f39f6aeba8e1b3b86504807f6f22be5a183c0993e240706f6ab8cef3229` |
| 10 | `StayConnect-IAM-Phase2-Software-Gate.md` | `docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md` | `a9335553` | **Authoritative — Phase 2 software-gate evidence (Go + 45 UI tests + build)** | `9cac79718cfedd6ef9d8351c3ffab27af998b5ef3c582919f986591184590cba` |
| 11 | `StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md` | `a9335553` | **Authoritative — Phase 2 live-dark + two-reboot darkness evidence** | `c21e3671a2ae9452298f34b00174ecb47c59ebfa4d422853757fe3eb69163c36` |
| 12 | `StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | `a9335553` | **Acceptance record — PRODUCT-OWNER ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014)** | `fafbe27b5cd44ab4e84aac1bd2a66679c919626dc8e86128dc8c53ba5e050c79` |
| 13 | `StayConnect-IAM-Phase2-Final-Report.md` | `docs/reports/StayConnect-IAM-Phase2-Final-Report.md` | `a9335553` | **Authoritative — Phase 2 final report (accepted)** | `369cd9fdbe3b410532078ab296a40d3c82ac3f171c6d3f17b3eece5f4da1dbb3` |
| 14 | `Phase2-change-manifest.md` | `docs/manifests/Phase2-change-manifest.md` | `a9335553` | **Generated — complete Phase 2 changed-file manifest (base..delivery_head; inventory_head provenance)** | `942fa3084e0df8f3d53315a3793be3d5b70e91326cc6cc15eb155a821c3f2008` |
| 15 | `StayConnect-IAM-Phase3-Plan.md` | `docs/architecture/StayConnect-IAM-Phase3-Plan.md` | `a9335553` | **Authoritative — Phase 3 plan (D14/T0015; ACCEPTED_AND_CLOSED at DARK maturity, D16/T0024; merged D17/T0025)** | `0e5facf374ef7de11568f5e674478bfc82d4c79df920f0fb84e2e09eb4cf9cc3` |
| 16 | `Phase3-Privilege-Matrix.md` | `docs/architecture/Phase3-Privilege-Matrix.md` | `a9335553` | **Authoritative — Phase 3 privilege matrix (PRODUCTION_IAM_V2_DML: NONE; DARK)** | `801ab8f97460ac757ef593ca1e3ecf5676749d3f7b8257440e66bf36e0a3ee25` |
| 17 | `Phase3-change-manifest.md` | `docs/manifests/Phase3-change-manifest.md` | `a9335553` | **Generated — complete Phase 3 changed-file manifest (base..delivery_head; inventory_head provenance)** | `eadebad5aabf83285aeb962e87371e4a80fd1fd2fc4921ac5461fdc7e8d54823` |
| 18 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `a9335553` | **Authoritative (acceptance record)** | `268d38dd93fc8fcc01caab762f6485bf15265a35eff24b0d2032ef17cc80d4c3` |
| 19 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `a9335553` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 20 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `a9335553` | **Permanent rule** | `903c225c2eb4402d923f9f387200d79193d22da26e14f4e6c059a83f80accd2a` |
| 21 | `GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | `a9335553` | **Permanent rule** | `f1d467e1d1bc697dc046cc00ffe80f48858951b05a23ce24a75f4654a984dacb` |
| 22 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `a9335553` | Historical snapshot | `d4181dac51deda09092043192aca8049e0f6293f5169359a0aaa2afd231a2e12` |
| 23 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `a9335553` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 24 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `a9335553` | Supporting | `52da8310e31562fe75b8354aa0c9e39bd1829236cfab83ffa2b37f38a4cd8665` |
| 25 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `a9335553` | Supporting | `4afe74fb71023532c78f2e015e2375e474cfeab26d1bceb7b445ee0c223568f7` |
| 26 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `a9335553` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 27 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `a9335553` | Supporting | `4b7346d028992205104071ab66b2c4a75b6c5be01eed566efe6305205001a913` |

*(MANIFEST is not self-referential.)*

## Content checksum
- pack_content_sha256 is the SHA-256 over the sorted `sha256(file)` lines of all non-MANIFEST pack files (see PACK_SHA256SUMS in the Evidence/Planning packs for physical lists).
