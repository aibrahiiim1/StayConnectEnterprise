# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

- **Project:** stayconnectenterprise
- **SOURCE_DOCUMENTATION_SYNC_COMMIT:** `d4fa9be` — `docs(iam): remove residual Phase-1A planning-only / scratch-only current-state contradictions; harden validator`. Every copied document matches `d4fa9be` (supersedes `d0eabc2`/`22a2e15`).
- **PROJECT_PACK_EXPORT_COMMIT:** `b0cf662` — the exact commit that introduced this regenerated pack (this line is stamped with the real hash after that commit exists).
- **Export date:** 2026-07-16
- **Current phase / maturity:** Phase 0 **FINAL/CLOSED**; **Phase 1A** = SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + **PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED** (49 tables, fingerprint `bd75026f`, dark in `stayconnect_site`, 18/18 acceptance). **NOT deployed, NOT cut over, NOT live accepted, no IAM data migration, no Phase 1B.**
- **Authoritative production evidence:** `PROD_LIVE_DARK_EVIDENCE_V2.txt` (read-only re-verification, in the Evidence Pack). The earlier `PROD_LIVE_DARK_EVIDENCE.txt` is **superseded (evidence error)** and retained for audit only.
- **Mandatory Phase-1B prerequisite:** services connect to `stayconnect_site` as superuser `stayconnect` (`rolsuper=true`); least-privilege `iam_v2` service roles do not bind them, so darkness rests on zero code refs + unchanged `search_path`. No service may route to `iam_v2` until a separately reviewed least-privilege service-role + credential-rotation plan is applied.
- **Next authorized action:** Product-Owner acceptance of Phase 1A (review of the live-dark acceptance) before any Phase 1B authorization.
- **Governance:** the permanent **Zero-Stale-Leftovers** rule is bundled in this pack as `ZERO_STALE_LEFTOVERS_RULE.md`; the enforcing validator `tools/validate-project-state.sh` is bundled in the Evidence Pack and must print `ZERO_STALE_LEFTOVERS = PASS` before any pack export.
- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized copy)* files; no passwords, keys, tokens, DSNs-with-credentials, or guest PII in any file.

## Files

| # | Exported filename | Original repository path | Source commit | Status | SHA-256 |
|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated)* | `d4fa9be` | Entry point | `27d92de640ac3b103f35f699a932bbb149ba339fa325a6b0edda6d00944411fb` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated)* | `d4fa9be` | Project config | `08c6b812aa3c98bd5720f93e777de4c89a606c234302a92fd1fc748701684851` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `d4fa9be` | **Authoritative** *(sanitized)* | `b6dc5f6dd80af26a42e63cb9b1767dfced8956562a5697af7cd594b831cd816e` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `d4fa9be` | **Authoritative** | `2e1f3299a889e1fc32542db3bc4a876ede0d100a271c3d18d36ea4dfcbe6b1b0` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `d4fa9be` | **Authoritative** | `d930b9696153502148f13aae3862409a35e0ccc3796c870e86c2b0edc3715bff` |
| 6 | `StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md` | `d4fa9be` | **Authoritative (acceptance record)** | `dc1f511322271af5382ae0c023c6c6a0295915d407dde422170f4ab60212e945` |
| 7 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `d4fa9be` | **Authoritative** *(sanitized)* | `a55039b86e098f67a8e92c0f6e14b903a5195f0fe7053701cc6001589b135486` |
| 8 | `ZERO_STALE_LEFTOVERS_RULE.md` | `docs/ZERO_STALE_LEFTOVERS_RULE.md` | `d4fa9be` | **Permanent rule (full text)** | `f4393729db5f264fbba585127f8b8ee70c3ea20da69e065523604100505a32ed` |
| 9 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `d4fa9be` | Historical snapshot | `3b5cc376451a8bec9907793e0cb5ef70aff231c67b52cf4d05e0e78be53f04e4` |
| 10 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `d4fa9be` | Supporting | `dd5b653ade4fbf1bffde1fc97e7f4e2d7fc3d3c9131bd05517b06c6430aa2dda` |
| 11 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `d4fa9be` | Supporting | `37f2022028148769b861b5d446427404a3aaa9545172ada0b24a60451c36e138` |
| 12 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `d4fa9be` | Supporting | `7e76f07e06785e58683d95dd5cadbbcc3f7ccbade77df5ab452dbf1c289ed773` |
| 13 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `d4fa9be` | Supporting | `3232e52f03e7a07089929703e261c27da879258f1dcac67b9c597b8942b69f20` |
| 14 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `d4fa9be` | Supporting | `737935641c4d8d0d5de9fd7a7d627aa634d215d2accf9867c76f5d25b658ca55` |

*(MANIFEST is not self-referential.)*

## Precedence
1. FINAL contract → 2. handoff → 3. Phase-1A plan → 4. live-dark acceptance record → 5. FIAS spike (verified evidence) → 6. permanent rule (`ZERO_STALE_LEFTOVERS_RULE.md`) → 7. system/ops docs (`TARGET_ARCHITECTURE`, ops manual, `DEPLOYMENT_APPLIANCE`, `OFFLINE_OPERATION`, `MIGRATION_RUNBOOK`; `SYSTEM_OVERVIEW` historical) → 8. historical chats → 9. superseded drafts.

## Validation summary
- ✅ Phase status consistent everywhere: Phase 0 FINAL/CLOSED; Phase 1A PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED (not cut over/deployed/live-accepted); single next action = PO acceptance of Phase 1A before 1B.
- ✅ No Phase-1A planning-only / scratch-only / plan-approval-gated / live-dark-future current-state text remains in docs or this pack (validator check 1 + within-file conflict check).
- ✅ Live-dark: `iam_v2` dark in production (0 rows, fingerprint `bd75026f`), public structural fingerprint `d86ca4c6` unchanged, no DSN/`search_path` change, services active — per read-only `PROD_LIVE_DARK_EVIDENCE_V2.txt`.
- ✅ Superuser deviation recorded as a mandatory Phase-1B prerequisite; broken V1 evidence marked superseded.
- ✅ Permanent rule bundled + links resolve inside the pack; validator bundled + checksummed in the Evidence Pack.
- ✅ No secrets/DSNs/guest PII; guest identifiers redacted in the two sanitized copies; non-packed links unwrapped to text so all pack links resolve.
- ✅ `tools/validate-project-state.sh` → `ZERO_STALE_LEFTOVERS = PASS` (all 10 checks; recorded in the acceptance evidence).
