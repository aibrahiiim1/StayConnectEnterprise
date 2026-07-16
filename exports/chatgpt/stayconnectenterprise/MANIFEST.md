# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

- **Project:** stayconnectenterprise
- **SOURCE_DOCUMENTATION_SYNC_COMMIT (content baseline):** `4a1adc5` — `docs(stayconnect): reconcile architecture and project status`. All copied documents match this commit.
- **PROJECT_PACK_EXPORT_COMMIT:** the commit that adds this regenerated pack (created **after** `4a1adc5`; `docs(chatgpt): regenerate synchronized StayConnect project pack`). This pack is the latest repository documentation state — the earlier export baseline `4a64b00` is **superseded**.
- **Export date:** 2026-07-16
- **Current phase:** Phase 0 **FINAL / CLOSED**; Phase 1A is the **current** phase, **not implemented** (plan status `READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL`). Next authorized action: **Product-Owner review + explicit implementation approval/rejection of the Phase 1A plan.**
- **Sanitization:** guest-linked identifiers (room number, reservation `G#`, identity fingerprints) redacted in the two files marked *(sanitized copy)*; no passwords, private keys, tokens, or guest names in any file. All technical findings preserved.

## Files

| # | Exported filename | Original repository path | Source commit | Status | Verified phase context | SHA-256 |
|---|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated for this pack)* | `4a1adc5` | Entry point | Phase 1A current | `391146f13f412021259d12e05db6e6483feed31dc9424f042881fceb4efd6687` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated for this pack)* | `4a1adc5` | Project config | Phase 1A current | `2e9fd2c78706b5b5d36ffb06902f8e0adb7f7945cc3e257ea23e78303af77c74` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `4a1adc5` | **Authoritative** *(sanitized copy)* | Phase 0 FINAL/CLOSED | `6544aaf3037472df8932c1b6e4e57d4abf41e4beed7195e12f1f31c346a9521d` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `4a1adc5` | **Authoritative** | Phase 1A current | `0e8416dd4ac7a9733f720d97f1036a650439c529acf59e71488559e1c813e7ac` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `4a1adc5` | **Authoritative** | Phase 1A current (READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL) | `ee0465ee2589443628b33f475734286a4256f60b91e06a5fa8902e4cb6d40dfc` |
| 6 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `4a1adc5` | **Authoritative** *(sanitized copy)* | Phase 0 evidence — Gate 3A PASS | `0e80be1699ce53ff88f4bebeabc4933dad9969ef2113e1955a3e54b81fbc1b1d` |
| 7 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `4a1adc5` | Historical snapshot (2026-07-10) | Pre-refactor reference | `1c9896898401e5f38fe3e7eb3541a10c397242a4cdea36519478f3a21aa81f54` |
| 8 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `4a1adc5` | Supporting (two-NIC corrected) | Target architecture | `320b6e02e382b4fdef2da361f632541b8b6f0ddf3dc98b08301be71d67292fb6` |
| 9 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `4a1adc5` | Supporting | Current live system | `88860a1e98498d6d065a9c2f5c7441915cf9a65c693eede67dfa7a043f1ffcee` |
| 10 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `4a1adc5` | Supporting (two-NIC corrected) | Current live system | `52827913e86d5f5216e8c5cd5c8d5b82e9902f95c84526d4304b19c72545446d` |
| 11 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `4a1adc5` | Supporting | Current live system | `02f0a7aaa1d76986bc18f6d03aeac13ca8a98b053639235734f8cb460d15ffb0` |
| 12 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `4a1adc5` | Supporting (edge migration; NOT iam_v2) | Delivered Central→site migration | `2ad9699bb21d32707a470afd44f2a7e96da7bfa4eca57c45b0a613fb7cbbc5ff` |

*(This MANIFEST is not self-referential; its own checksum is omitted by definition.)*

## Precedence (authoritative order)

1. `StayConnect-IAM-Phase0-Contract.md` (FINAL contract) → 2. `StayConnect-IAM-Handoff.md` → 3. `StayConnect-IAM-Phase1A-Plan.md` → 4. `Protel-FIAS-Phase0-Spike.md` (verified evidence) → 5. system/ops docs (`TARGET_ARCHITECTURE`, operations manual, `DEPLOYMENT_APPLIANCE`, `OFFLINE_OPERATION`, `MIGRATION_RUNBOOK`; `SYSTEM_OVERVIEW` is a dated historical snapshot) → 6. historical chats → 7. superseded drafts.

## Validation summary (at export)

- ✅ No file states Phase 0 is CONDITIONALLY FROZEN as a live status (only historical "previously …" mentions).
- ✅ Phase 0 FINAL / CLOSED asserted consistently; Phase 1A status consistent everywhere (`READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL`, not implemented); single next action.
- ✅ Two-NIC topology (WAN=management + LAN=guest) consistent; third `hasync` NIC marked superseded; HA-sync transport recorded as an OPEN decision.
- ✅ `iam_v2` isolation consistent; cutover = atomic complete-domain switch (never per-flow/per-service); vertical slice does not authorize cutover.
- ✅ Hotel ID 2 (Aqua Club) read-only + financially unapproved; `programmatic_reversal = false`; Gate 3C/3D classified as post-implementation acceptance.
- ✅ `folio_identity_strategy` fail-closed gate recorded as an open contract-amendment BLOCKER (FINAL DDL unchanged); MG-0 `CREATE UNIQUE INDEX CONCURRENTLY` documented as non-transactional.
- ✅ No secrets and no guest PII/identifiers present.
- ✅ Core/authoritative pack links resolve inside the pack (cross-links flattened).
- ✅ Migration runbook heading numbering unique (Phase 0–9); no duplicate "Phase 8".
- ℹ️ Supporting docs retain repo-relative links to the broader documentation set intentionally not in this minimal pack (reference pointers to the full repository).

## Intentionally excluded (and why)

- **Superseded/duplicate contract drafts** — none exist as separate files; only the current FINAL contract is exported.
- **Broader supporting docs** referenced by copied files (edge networking, cloud architecture, HA specifics, DHCP, security hardening, licensing, data ownership, user guides) — outside the requested minimal pack; in the full repository.
- **Secrets & credentials** — none present in source docs (only field labels/schema descriptions).
- **Guest PII** — none in docs; guest-linked room/reservation identifiers + fingerprints redacted from the two sanitized copies.
- **Code, migrations, binaries, deploy/build artifacts** — out of scope for a documentation pack.
