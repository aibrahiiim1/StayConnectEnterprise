# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

- **Project:** stayconnectenterprise
- **SOURCE_DOCUMENTATION_SYNC_COMMIT (content baseline):** `79bf3e8` — `docs(stayconnect): apply PO-approved folio fail-closed amendment; HA/cutover truthfulness`. All copied documents match this commit.
- **PROJECT_PACK_EXPORT_COMMIT:** the commit that adds this regenerated pack (created **after** `79bf3e8`; `docs(chatgpt): regenerate synchronized StayConnect project pack`). This pack is the latest repository documentation state; the prior export baselines `4a64b00`/`4a1adc5` are **superseded**.
- **Export date:** 2026-07-16
- **Current phase:** Phase 0 **FINAL / CLOSED**; Phase 1A is the **current** phase, **not implemented** (plan `READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL`). Next authorized action: **Product-Owner review + explicit implementation approval/rejection of the Phase 1A plan.**
- **Sanitization:** guest-linked identifiers (room number, reservation `G#`, identity fingerprints) redacted in the two files marked *(sanitized copy)*; no passwords, private keys, tokens, or guest names in any file. All technical findings preserved.

## Files

| # | Exported filename | Original repository path | Source commit | Status | Verified phase context | SHA-256 |
|---|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated for this pack)* | `79bf3e8` | Entry point | Phase 1A current | `301e0c19280d70e2003dbfc7a3292a7c5aab02e444b483b71db952d135ea98d0` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated for this pack)* | `79bf3e8` | Project config | Phase 1A current | `1d8187ef51410b4a8c0ff370ef12e612b6ef27ec580f543f70c701f723b7ada8` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `79bf3e8` | **Authoritative** *(sanitized copy)* | Phase 0 FINAL/CLOSED (folio amendment applied) | `4294cbec50eef54e9f85779091abf6ba6f68ea3063b074e9c79ca482517d30eb` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `79bf3e8` | **Authoritative** | Phase 1A current | `ab0fae99b9d30946f7117b731258fc4aa877fadf6c0fe3824a658757254d196a` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `79bf3e8` | **Authoritative** | Phase 1A current (READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL) | `47607882cfeac35091ee53a26272ccdf5941eb5e538a90338c87128bf8a8686e` |
| 6 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `79bf3e8` | **Authoritative** *(sanitized copy)* | Phase 0 evidence — Gate 3A PASS | `0e80be1699ce53ff88f4bebeabc4933dad9969ef2113e1955a3e54b81fbc1b1d` |
| 7 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `79bf3e8` | Historical snapshot (2026-07-10) | Pre-refactor reference | `1c9896898401e5f38fe3e7eb3541a10c397242a4cdea36519478f3a21aa81f54` |
| 8 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `79bf3e8` | Supporting (two-NIC + HA truthfulness) | Target architecture | `2eef5c2d4b401ce374e8cffc8871b9bc5191a30a385272a79c9b509f2e3ca26a` |
| 9 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `79bf3e8` | Supporting | Current live system | `88860a1e98498d6d065a9c2f5c7441915cf9a65c693eede67dfa7a043f1ffcee` |
| 10 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `79bf3e8` | Supporting (two-NIC corrected) | Current live system | `52827913e86d5f5216e8c5cd5c8d5b82e9902f95c84526d4304b19c72545446d` |
| 11 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `79bf3e8` | Supporting (HA truthfulness) | Current live system | `55d7ece9ef58a67f6f8171510040d16e2dea9320d1e626a49236408c45a6da3b` |
| 12 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `79bf3e8` | Supporting (edge migration; NOT iam_v2) | Delivered Central→site migration | `2ad9699bb21d32707a470afd44f2a7e96da7bfa4eca57c45b0a613fb7cbbc5ff` |

*(This MANIFEST is not self-referential; its own checksum is omitted by definition.)*

## Precedence (authoritative order)

1. `StayConnect-IAM-Phase0-Contract.md` (FINAL contract) → 2. `StayConnect-IAM-Handoff.md` → 3. `StayConnect-IAM-Phase1A-Plan.md` → 4. `Protel-FIAS-Phase0-Spike.md` (verified evidence) → 5. system/ops docs (`TARGET_ARCHITECTURE`, operations manual, `DEPLOYMENT_APPLIANCE`, `OFFLINE_OPERATION`, `MIGRATION_RUNBOOK`; `SYSTEM_OVERVIEW` is a dated historical snapshot) → 6. historical chats → 7. superseded drafts.

## Validation summary (at export)

- ✅ No file states Phase 0 is CONDITIONALLY FROZEN as a live status.
- ✅ Phase 0 FINAL/CLOSED consistent; Phase 1A `READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL`, not implemented; single next action.
- ✅ **Folio fail-closed amendment applied:** `folio_identity_strategy NOT NULL DEFAULT 'UNSET'` (4-value CHECK); `UNSET` blocks CHARGE before outbox/`P#`/transmission; read-only ingestion/lookup/auth allowed; concrete strategy = new immutable revision; `UNSET` is the only unset sentinel. No `GLOBALLY_UNIQUE` default remains.
- ✅ **Cutover rollback boundaries:** safe flip-back only before the first production write; after it, forward-fix/reconcile-before-return; no-return boundary explicit.
- ✅ **Two-NIC topology** (WAN=management + LAN=guest) consistent; third `hasync` NIC superseded; HA-sync transport OPEN; **single-appliance offline supported, HA failover NOT implemented/accepted.**
- ✅ `iam_v2` isolation consistent; cutover = atomic complete-domain switch; MG-0 non-transactional with an invalid-index guard (no silent `IF NOT EXISTS`).
- ✅ Hotel ID 2 read-only + financially unapproved; `programmatic_reversal = false`; Gate 3C/3D post-implementation.
- ✅ No secrets and no guest PII/identifiers present.
- ✅ Core/authoritative pack links resolve inside the pack; migration runbook headings unique (Phase 0–9).
- ℹ️ `SYSTEM_OVERVIEW.md` (historical snapshot) and other supporting docs retain repo-relative links to the broader documentation set intentionally not in this minimal pack.

## Intentionally excluded (and why)

- **Superseded/duplicate contract drafts** — none as separate files; only the current FINAL contract is exported.
- **Broader supporting docs** referenced by copied files (edge/cloud architecture, HA specifics, DHCP, security, licensing, data ownership, user guides) — outside the minimal pack; in the full repository.
- **Secrets & credentials** — none present in source docs (only field labels/schema descriptions).
- **Guest PII** — none in docs; guest room/reservation identifiers + fingerprints redacted from the two sanitized copies.
- **Code, migrations, binaries, deploy/build artifacts** — out of scope for a documentation pack.
