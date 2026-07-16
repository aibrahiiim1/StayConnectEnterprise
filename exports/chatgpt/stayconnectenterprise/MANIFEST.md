# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

- **Project:** stayconnectenterprise
- **SOURCE_DOCUMENTATION_SYNC_COMMIT:** `a28f6f6` — `docs(iam): apply binding Phase-1B decisions D1-D9; Phase 1A accepted/closed; contract §18 clarification`. Every copied document matches `a28f6f6` (supersedes `afade95`/`d4fa9be`/`22a2e15`).
- **PROJECT_PACK_EXPORT_COMMIT:** `3e4450f` — the exact commit that introduced this regenerated pack (this line is stamped with the real hash after that commit exists).
- **Export date:** 2026-07-16
- **Current phase / maturity:** Phase 0 **FINAL/CLOSED**; **Phase 1A formally Product-Owner ACCEPTED and CLOSED (2026-07-16)** at SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + **PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER** (49 tables, fingerprint `bd75026f`, dark in `stayconnect_site`, 18/18). **NOT deployed, NOT cut over, NOT a user-facing/authority-switch system, no IAM data migration, no Phase 1B implementation. Phase 1B planning is the current activity.**
- **Authoritative production evidence:** `PROD_LIVE_DARK_EVIDENCE_V2.txt` (read-only re-verification, in the Evidence Pack). The earlier `PROD_LIVE_DARK_EVIDENCE.txt` is **superseded (evidence error)** and retained for audit only.
- **Mandatory Phase-1B prerequisite:** services connect to `stayconnect_site` as superuser `stayconnect` (`rolsuper=true`); least-privilege `iam_v2` service roles do not bind them, so darkness rests on zero code refs + unchanged `search_path`. No service may route to `iam_v2` until a separately reviewed least-privilege service-role + credential-rotation plan is applied.
- **Phase 1B:** a complete production-grade Phase 1B credential/identity/auth-context implementation plan is drafted (planning-only, NOT implemented; DARK, not a cutover) — `StayConnect-IAM-Phase1B-Plan.md`, with the exact grant matrix `Phase1B-Privilege-Matrix.md`. Binding PO decisions D1–D9 applied.
- **Next authorized action:** Product-Owner approval or rejection of the complete Phase 1B plan.
- **Governance:** the permanent **Zero-Stale-Leftovers** rule is bundled in this pack as `ZERO_STALE_LEFTOVERS_RULE.md`; the enforcing validator `tools/validate-project-state.sh` is bundled in the Evidence Pack and must print `ZERO_STALE_LEFTOVERS = PASS` before any pack export.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized copy)* files; no passwords, keys, tokens, DSNs-with-credentials, or guest PII in any file.

## Files

| # | Exported filename | Original repository path | Source commit | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `a28f6f6` | Entry point | `bcd0fee5a1b2621380f3a326f9d0c811d36b836fa78f79a6ae897879d5d720a9` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `a28f6f6` | Project config | `561c239e6a7ff43504e7170512a5c74e0d2d87260ac58722b1c893bb6919d156` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `a28f6f6` | **Authoritative** *(sanitized)* | `2e780db119977cc7957ff31136048f6745fd2015e322556bfa9a4620b84fbac3` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `a28f6f6` | **Authoritative** | `dc4358bfad7e5512189a93c092ba0c66d6164a0ba942083a8f4ad843c5383ea7` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `a28f6f6` | **Authoritative** | `007ea6c8e42d5802724f62370cb426e59a48dbf26e3de8c95003cc18f62f2b40` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `a28f6f6` | **Authoritative (planning-only)** | `97d1bf69d9b1b2ae20ffedf5ec2a95bba53ef0c5110ac123113789cc181df316` |
| 7 | `Phase1B-Privilege-Matrix.md` | `docs/architecture/Phase1B-Privilege-Matrix.md` | `a28f6f6` | **Authoritative (planning-only) — exact grant matrix** | `4e3fba9a3940bc2e4b1b14509105f033aa5d379544b2e406f81f6118d7f102c2` |
| 8 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `a28f6f6` | **Authoritative (acceptance record)** | `268d38dd93fc8fcc01caab762f6485bf15265a35eff24b0d2032ef17cc80d4c3` |
| 9 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `a28f6f6` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 10 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `a28f6f6` | **Permanent rule (full text)** | `f4393729db5f264fbba585127f8b8ee70c3ea20da69e065523604100505a32ed` |
| 11 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `a28f6f6` | Historical snapshot | `3b5cc376451a8bec9907793e0cb5ef70aff231c67b52cf4d05e0e78be53f04e4` |
| 12 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `a28f6f6` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 13 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `a28f6f6` | Supporting | `37f2022028148769b861b5d446427404a3aaa9545172ada0b24a60451c36e138` |
| 14 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `a28f6f6` | Supporting | `7e76f07e06785e58683d95dd5cadbbcc3f7ccbade77df5ab452dbf1c289ed773` |
| 15 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `a28f6f6` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 16 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `a28f6f6` | Supporting | `737935641c4d8d0d5de9fd7a7d627aa634d215d2accf9867c76f5d25b658ca55` |

*(MANIFEST is not self-referential.)*

## Precedence
1. FINAL contract → 2. handoff → 3. Phase-1A plan → 4. live-dark acceptance record → 5. FIAS spike (verified evidence) → 6. permanent rule (`ZERO_STALE_LEFTOVERS_RULE.md`) → 7. system/ops docs (`TARGET_ARCHITECTURE`, ops manual, `DEPLOYMENT_APPLIANCE`, `OFFLINE_OPERATION`, `MIGRATION_RUNBOOK`; `SYSTEM_OVERVIEW` historical) → 8. historical chats → 9. superseded drafts.

## Validation summary
- ✅ Phase status consistent everywhere: Phase 0 FINAL/CLOSED; Phase 1A formally ACCEPTED/CLOSED at its DARK maturity (not cut over/deployed, not a user-facing/authority-switch system); Phase 1B plan drafted (planning-only, not implemented, DARK not a cutover); single next action = PO approval or rejection of the Phase 1B plan.
- ✅ No Phase-1A planning-only / scratch-only / plan-approval-gated / live-dark-future current-state text remains in docs or this pack (validator check 1 + within-file conflict check).
- ✅ Live-dark: `iam_v2` dark in production (0 rows, fingerprint `bd75026f`), public structural fingerprint `d86ca4c6` unchanged, no DSN/`search_path` change, services active — per read-only `PROD_LIVE_DARK_EVIDENCE_V2.txt`.
- ✅ Superuser deviation recorded as a mandatory Phase-1B prerequisite; broken V1 evidence marked superseded.
- ✅ Permanent rule bundled + links resolve inside the pack; validator bundled + checksummed in the Evidence Pack.
- ✅ No secrets/DSNs/guest PII; guest identifiers redacted in the two sanitized copies; non-packed links unwrapped to text so all pack links resolve.
- ✅ `tools/validate-project-state.sh` → `ZERO_STALE_LEFTOVERS = PASS` (all 10 checks; recorded in the acceptance evidence).
