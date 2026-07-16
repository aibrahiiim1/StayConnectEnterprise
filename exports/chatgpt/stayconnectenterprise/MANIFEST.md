# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

- **Project:** stayconnectenterprise
- **SOURCE_DOCUMENTATION_SYNC_COMMIT:** `22a2e15` — `feat(iam_v2): Phase 1A LIVE-DARK created + verified in production stayconnect_site`. Copied documents match this commit.
- **PROJECT_PACK_EXPORT_COMMIT:** the commit that regenerates this pack (created **after** `22a2e15`). Prior export baselines are superseded.
- **Export date:** 2026-07-16
- **Current phase / maturity:** Phase 0 **FINAL/CLOSED**; **Phase 1A** = SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + **PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED** (49 tables, fingerprint `bd75026f`, dark in `stayconnect_site`, 18/18 acceptance). **NOT deployed, NOT cut over, NOT live accepted, no IAM data migration, no Phase 1B.**
- **Next authorized action:** Product-Owner review of the live-dark acceptance before any Phase 1B authorization.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized copy)* files; no passwords, keys, tokens, DSNs-with-credentials, or guest PII in any file.

## Files

| # | Exported filename | Original repository path | Source commit | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `22a2e15` | Entry point | `c7bc0e066e28f90def84086197f60b00bf5076d60d966675da82156486f116ca` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `22a2e15` | Project config | `1d8187ef51410b4a8c0ff370ef12e612b6ef27ec580f543f70c701f723b7ada8` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `22a2e15` | **Authoritative** *(sanitized)* | `94456ec54013f235f27abc183c84772cbda3302b0538c680df5492b68005175f` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `22a2e15` | **Authoritative** | `07d4508303e5b322bf26618d85cb3ca02295d33e06584b84216e97d782d41817` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `22a2e15` | **Authoritative** | `7f0c3e49bee534180a9e10572f729fd6611def4eacf2ea4634f5cccc08095854` |
| 6 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `22a2e15` | **Authoritative (acceptance record)** | `59c367b70a8a019af8ead6b28d09ee5bb3ba42de245cbfc77398caec0324160c` |
| 7 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `22a2e15` | **Authoritative** *(sanitized)* | `699f3dee765dc01e2ece075e9597c593cc8b1c89d83fcf9fdb4507cb77331e7f` |
| 8 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `22a2e15` | Historical snapshot | `1c9896898401e5f38fe3e7eb3541a10c397242a4cdea36519478f3a21aa81f54` |
| 9 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `22a2e15` | Supporting | `2eef5c2d4b401ce374e8cffc8871b9bc5191a30a385272a79c9b509f2e3ca26a` |
| 10 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `22a2e15` | Supporting | `88860a1e98498d6d065a9c2f5c7441915cf9a65c693eede67dfa7a043f1ffcee` |
| 11 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `22a2e15` | Supporting | `52827913e86d5f5216e8c5cd5c8d5b82e9902f95c84526d4304b19c72545446d` |
| 12 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `22a2e15` | Supporting | `55d7ece9ef58a67f6f8171510040d16e2dea9320d1e626a49236408c45a6da3b` |
| 13 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `22a2e15` | Supporting | `2ad9699bb21d32707a470afd44f2a7e96da7bfa4eca57c45b0a613fb7cbbc5ff` |

*(MANIFEST is not self-referential.)*

## Precedence
1. FINAL contract → 2. handoff → 3. Phase-1A plan → 4. live-dark acceptance record → 5. FIAS spike (verified evidence) → 6. system/ops docs (`TARGET_ARCHITECTURE`, ops manual, `DEPLOYMENT_APPLIANCE`, `OFFLINE_OPERATION`, `MIGRATION_RUNBOOK`; `SYSTEM_OVERVIEW` historical) → 7. historical chats → 8. superseded drafts.

## Validation summary
- ✅ Phase status consistent everywhere: Phase 0 FINAL/CLOSED; Phase 1A PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED (not cut over/deployed/live-accepted); single next action = PO review before 1B.
- ✅ Live-dark: `iam_v2` dark in production (0 rows, fingerprint `bd75026f`), public unchanged, no DSN/`search_path` change, services active.
- ✅ Two-NIC topology, folio `UNSET` fail-closed, `programmatic_reversal=false`, Hotel ID 2 unapproved — all consistent.
- ✅ No secrets/DSNs/guest PII; guest identifiers redacted in the two sanitized copies; core links flattened to resolve inside the pack.
