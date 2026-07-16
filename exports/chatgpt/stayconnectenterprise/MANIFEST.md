# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

- **Project:** stayconnectenterprise
- **SOURCE_DOCUMENTATION_SYNC_COMMIT:** `d0eabc2` — `docs(iam): record ZERO_STALE_LEFTOVERS=PASS in the Phase 1A live-dark acceptance record`, the final commit of the reconciliation chain `f2d0550` (V2 evidence + zero-stale rule + validator) → `bd9ee7f` (removed residual blue/green + standby-DB terms; handoff → V2) → `d0eabc2`; supersedes `22a2e15`. Every copied document matches `d0eabc2`.
- **PROJECT_PACK_EXPORT_COMMIT:** the commit that introduces this regenerated pack (HEAD at export, created **after** `d0eabc2`; verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`). Prior export baselines are superseded.
- **Export date:** 2026-07-16
- **Current phase / maturity:** Phase 0 **FINAL/CLOSED**; **Phase 1A** = SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + **PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED** (49 tables, fingerprint `bd75026f`, dark in `stayconnect_site`, 18/18 acceptance). **NOT deployed, NOT cut over, NOT live accepted, no IAM data migration, no Phase 1B.**
- **Authoritative production evidence:** `PROD_LIVE_DARK_EVIDENCE_V2.txt` (read-only re-verification, in the Evidence Pack). The earlier `PROD_LIVE_DARK_EVIDENCE.txt` is **superseded (evidence error)** and retained for audit only.
- **Mandatory Phase-1B prerequisite:** services connect to `stayconnect_site` as superuser `stayconnect` (`rolsuper=true`); least-privilege `iam_v2` service roles do not bind them, so darkness rests on zero code refs + unchanged `search_path`. No service may route to `iam_v2` until a separately reviewed least-privilege service-role + credential-rotation plan is applied.
- **Next authorized action:** Product-Owner acceptance of Phase 1A (review of the live-dark acceptance) before any Phase 1B authorization.
- **Governance:** the permanent **Zero-Stale-Leftovers** rule (`docs/ZERO_STALE_LEFTOVERS_RULE.md`) applies; `tools/validate-project-state.sh` must print `ZERO_STALE_LEFTOVERS = PASS` before any pack export.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized copy)* files; no passwords, keys, tokens, DSNs-with-credentials, or guest PII in any file.

## Files

| # | Exported filename | Original repository path | Source commit | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `d0eabc2` | Entry point | `0c7ffb32af14c8bf7537b693fe51c6bf93f920a543c5312c647a9261966d6b99` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `d0eabc2` | Project config | `8dbc79b407a35f282750dd42ef06b06fb05469ae3c8c1dad8d316dafd976a80e` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `d0eabc2` | **Authoritative** *(sanitized)* | `9872e9c9d3c74ce843db532e584997c3ba97d6d97875887021baf4a7589fc35f` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `d0eabc2` | **Authoritative** | `ef9a7591711552b648333277eda49d93d8f43313a2302c3ea49cfed87eb9c09a` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `d0eabc2` | **Authoritative** | `46af6d6022d99e4845e7293ed5b12f4a4fa3cb84ff2f216ef4bbfbf44424b989` |
| 6 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `d0eabc2` | **Authoritative (acceptance record)** | `66f5f2a9b2c4fced2e6b42a2ff1c7c4e4ee6d3a74e4f483dbfc1c3bcad4d6ec9` |
| 7 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `d0eabc2` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 8 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `d0eabc2` | Historical snapshot | `1c9896898401e5f38fe3e7eb3541a10c397242a4cdea36519478f3a21aa81f54` |
| 9 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `d0eabc2` | Supporting | `2eef5c2d4b401ce374e8cffc8871b9bc5191a30a385272a79c9b509f2e3ca26a` |
| 10 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `d0eabc2` | Supporting | `88860a1e98498d6d065a9c2f5c7441915cf9a65c693eede67dfa7a043f1ffcee` |
| 11 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `d0eabc2` | Supporting | `52827913e86d5f5216e8c5cd5c8d5b82e9902f95c84526d4304b19c72545446d` |
| 12 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `d0eabc2` | Supporting | `55d7ece9ef58a67f6f8171510040d16e2dea9320d1e626a49236408c45a6da3b` |
| 13 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `d0eabc2` | Supporting | `2ad9699bb21d32707a470afd44f2a7e96da7bfa4eca57c45b0a613fb7cbbc5ff` |

*(MANIFEST is not self-referential.)*

## Precedence
1. FINAL contract → 2. handoff → 3. Phase-1A plan → 4. live-dark acceptance record → 5. FIAS spike (verified evidence) → 6. system/ops docs (`TARGET_ARCHITECTURE`, ops manual, `DEPLOYMENT_APPLIANCE`, `OFFLINE_OPERATION`, `MIGRATION_RUNBOOK`; `SYSTEM_OVERVIEW` historical) → 7. historical chats → 8. superseded drafts.

## Validation summary
- ✅ Phase status consistent everywhere: Phase 0 FINAL/CLOSED; Phase 1A PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED (not cut over/deployed/live-accepted); single next action = PO acceptance of Phase 1A before 1B.
- ✅ Live-dark: `iam_v2` dark in production (0 rows, fingerprint `bd75026f`), public structural fingerprint `d86ca4c6` unchanged, no DSN/`search_path` change, services active — per read-only `PROD_LIVE_DARK_EVIDENCE_V2.txt`.
- ✅ Superuser deviation recorded as a mandatory Phase-1B prerequisite; broken V1 evidence marked superseded.
- ✅ Two-NIC topology, folio `UNSET` fail-closed, `programmatic_reversal=false`, Hotel ID 2 unapproved — all consistent.
- ✅ No secrets/DSNs/guest PII; guest identifiers redacted in the two sanitized copies; core links flattened to resolve inside the pack.
- ✅ `tools/validate-project-state.sh` → `ZERO_STALE_LEFTOVERS = PASS` (recorded in the acceptance evidence).
