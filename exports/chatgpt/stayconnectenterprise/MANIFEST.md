# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

- **Project:** stayconnectenterprise
- **Current synchronized documentation baseline:** `79bf3e8` (the content-decision baseline the docs are synchronized to). **SOURCE_DOCUMENTATION_SYNC_COMMIT (records this final reconciliation):** `e87d0a1` — `docs(stayconnect): reconcile provenance, next-action, and Phase-1A approval ladder`. Copied documents match `e87d0a1`.
- **Historical Phase-0 finalization provenance (not current):** contract `ffe2200`, synchronized handoff `6b4721d`.
- **PROJECT_PACK_EXPORT_COMMIT:** the commit that adds this regenerated pack (created **after** `e87d0a1`; `docs(chatgpt): regenerate synchronized StayConnect project pack`). This pack is the latest repository documentation state; prior export baselines `4a64b00`/`4a1adc5`/`0cb8ca8` are **superseded**.
- **Export date:** 2026-07-16
- **Current phase:** Phase 0 **FINAL / CLOSED**; Phase 1A is the **current** phase, **not implemented** (plan `READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL`). Next authorized action: **Product-Owner review + explicit implementation approval/rejection of the Phase 1A plan.**
- **Sanitization:** guest-linked identifiers (room number, reservation `G#`, identity fingerprints) redacted in the two files marked *(sanitized copy)*; no passwords, private keys, tokens, or guest names in any file. All technical findings preserved.

## Files

| # | Exported filename | Original repository path | Source commit | Status | Verified phase context | SHA-256 |
|---|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated for this pack)* | `e87d0a1` | Entry point | Phase 1A current | `3a7b3fd3c78454efdd54afae493cdb9764f2f0d847d241f5b57af9c949a87c6d` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated for this pack)* | `e87d0a1` | Project config | Phase 1A current | `1d8187ef51410b4a8c0ff370ef12e612b6ef27ec580f543f70c701f723b7ada8` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `e87d0a1` | **Authoritative** *(sanitized copy)* | Phase 0 FINAL/CLOSED (folio amendment applied) | `bbf74be5df8419ecc689cc60b32d43dccc7ac0a431eb3052052ff67e536ca5ea` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `e87d0a1` | **Authoritative** | Phase 1A current | `dcb5698a9f03e4f6c10853b492947b22578e2d3ece7945fb5091f556dc2c8588` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `e87d0a1` | **Authoritative** | Phase 1A current (READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL) | `8652dd7c703bb673922917275ac4fb38de95c7cebad59f46c8fcf3d3fad42ac7` |
| 6 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `e87d0a1` | **Authoritative** *(sanitized copy)* | Phase 0 evidence — Gate 3A PASS | `1a304fecbeb54c1ff2a37ff7f96aa5ca10813438f7ee362af62dfd40e88de05b` |
| 7 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `e87d0a1` | Historical snapshot (2026-07-10) | Pre-refactor reference | `1c9896898401e5f38fe3e7eb3541a10c397242a4cdea36519478f3a21aa81f54` |
| 8 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `e87d0a1` | Supporting (two-NIC + HA truthfulness) | Target architecture | `2eef5c2d4b401ce374e8cffc8871b9bc5191a30a385272a79c9b509f2e3ca26a` |
| 9 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `e87d0a1` | Supporting | Current live system | `88860a1e98498d6d065a9c2f5c7441915cf9a65c693eede67dfa7a043f1ffcee` |
| 10 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `e87d0a1` | Supporting (two-NIC corrected) | Current live system | `52827913e86d5f5216e8c5cd5c8d5b82e9902f95c84526d4304b19c72545446d` |
| 11 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `e87d0a1` | Supporting (HA truthfulness) | Current live system | `55d7ece9ef58a67f6f8171510040d16e2dea9320d1e626a49236408c45a6da3b` |
| 12 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `e87d0a1` | Supporting (edge migration; NOT iam_v2) | Delivered Central→site migration | `2ad9699bb21d32707a470afd44f2a7e96da7bfa4eca57c45b0a613fb7cbbc5ff` |

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
