# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

- **Project:** stayconnectenterprise
- **SOURCE_DOCUMENTATION_SYNC_COMMIT:** `afade95` — `docs(iam): add complete Phase 1B implementation plan (planning-only)`. Every copied document matches `afade95` (supersedes `d4fa9be`/`d0eabc2`/`22a2e15`).
- **PROJECT_PACK_EXPORT_COMMIT:** `5b36bab` — the exact commit that introduced this regenerated pack (this line is stamped with the real hash after that commit exists).
- **Export date:** 2026-07-16
- **Current phase / maturity:** Phase 0 **FINAL/CLOSED**; **Phase 1A** = SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + **PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED** (49 tables, fingerprint `bd75026f`, dark in `stayconnect_site`, 18/18 acceptance). **NOT deployed, NOT cut over, NOT live accepted, no IAM data migration, no Phase 1B.**
- **Authoritative production evidence:** `PROD_LIVE_DARK_EVIDENCE_V2.txt` (read-only re-verification, in the Evidence Pack). The earlier `PROD_LIVE_DARK_EVIDENCE.txt` is **superseded (evidence error)** and retained for audit only.
- **Mandatory Phase-1B prerequisite:** services connect to `stayconnect_site` as superuser `stayconnect` (`rolsuper=true`); least-privilege `iam_v2` service roles do not bind them, so darkness rests on zero code refs + unchanged `search_path`. No service may route to `iam_v2` until a separately reviewed least-privilege service-role + credential-rotation plan is applied.
- **Phase 1B:** a complete production-grade Phase 1B credential/portal implementation plan is drafted (planning-only, NOT implemented) — `StayConnect-IAM-Phase1B-Plan.md`. Phase 1A acceptance status is unchanged.
- **Next authorized action:** Product-Owner approval or rejection of the complete Phase 1B plan.
- **Governance:** the permanent **Zero-Stale-Leftovers** rule is bundled in this pack as `ZERO_STALE_LEFTOVERS_RULE.md`; the enforcing validator `tools/validate-project-state.sh` is bundled in the Evidence Pack and must print `ZERO_STALE_LEFTOVERS = PASS` before any pack export.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized copy)* files; no passwords, keys, tokens, DSNs-with-credentials, or guest PII in any file.

## Files

| # | Exported filename | Original repository path | Source commit | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `afade95` | Entry point | `4edc95f4e6b0b9707e5c2d6f833ba1a70959afd522e572e3b1541facbffdbdc9` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `afade95` | Project config | `08c6b812aa3c98bd5720f93e777de4c89a606c234302a92fd1fc748701684851` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `afade95` | **Authoritative** *(sanitized)* | `b6dc5f6dd80af26a42e63cb9b1767dfced8956562a5697af7cd594b831cd816e` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `afade95` | **Authoritative** | `283c3b418cf25746a8b6880ecef2648759849174248926ca0a30bb9c8776fafa` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `afade95` | **Authoritative** | `d930b9696153502148f13aae3862409a35e0ccc3796c870e86c2b0edc3715bff` |
| 6 | `StayConnect-IAM-Phase1B-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | `afade95` | **Authoritative (planning-only)** | `3fb4fc7c84d5bfa8fb36251882ee2331403606e6c00a34c841f2b69e346dc65d` |
| 7 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `afade95` | **Authoritative (acceptance record)** | `dc1f511322271af5382ae0c023c6c6a0295915d407dde422170f4ab60212e945` |
| 8 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `afade95` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 9 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `afade95` | **Permanent rule (full text)** | `f4393729db5f264fbba585127f8b8ee70c3ea20da69e065523604100505a32ed` |
| 10 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `afade95` | Historical snapshot | `3b5cc376451a8bec9907793e0cb5ef70aff231c67b52cf4d05e0e78be53f04e4` |
| 11 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `afade95` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 12 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `afade95` | Supporting | `37f2022028148769b861b5d446427404a3aaa9545172ada0b24a60451c36e138` |
| 13 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `afade95` | Supporting | `7e76f07e06785e58683d95dd5cadbbcc3f7ccbade77df5ab452dbf1c289ed773` |
| 14 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `afade95` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 15 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `afade95` | Supporting | `737935641c4d8d0d5de9fd7a7d627aa634d215d2accf9867c76f5d25b658ca55` |

*(MANIFEST is not self-referential.)*

## Precedence
1. FINAL contract → 2. handoff → 3. Phase-1A plan → 4. live-dark acceptance record → 5. FIAS spike (verified evidence) → 6. permanent rule (`ZERO_STALE_LEFTOVERS_RULE.md`) → 7. system/ops docs (`TARGET_ARCHITECTURE`, ops manual, `DEPLOYMENT_APPLIANCE`, `OFFLINE_OPERATION`, `MIGRATION_RUNBOOK`; `SYSTEM_OVERVIEW` historical) → 8. historical chats → 9. superseded drafts.

## Validation summary
- ✅ Phase status consistent everywhere: Phase 0 FINAL/CLOSED; Phase 1A PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED (not cut over/deployed/live-accepted, unchanged); Phase 1B plan drafted (planning-only, not implemented); single next action = PO approval or rejection of the Phase 1B plan.
- ✅ No Phase-1A planning-only / scratch-only / plan-approval-gated / live-dark-future current-state text remains in docs or this pack (validator check 1 + within-file conflict check).
- ✅ Live-dark: `iam_v2` dark in production (0 rows, fingerprint `bd75026f`), public structural fingerprint `d86ca4c6` unchanged, no DSN/`search_path` change, services active — per read-only `PROD_LIVE_DARK_EVIDENCE_V2.txt`.
- ✅ Superuser deviation recorded as a mandatory Phase-1B prerequisite; broken V1 evidence marked superseded.
- ✅ Permanent rule bundled + links resolve inside the pack; validator bundled + checksummed in the Evidence Pack.
- ✅ No secrets/DSNs/guest PII; guest identifiers redacted in the two sanitized copies; non-packed links unwrapped to text so all pack links resolve.
- ✅ `tools/validate-project-state.sh` → `ZERO_STALE_LEFTOVERS = PASS` (all 10 checks; recorded in the acceptance evidence).
