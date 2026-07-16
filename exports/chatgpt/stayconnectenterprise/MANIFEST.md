# StayConnect Enterprise — ChatGPT Project Pack MANIFEST

- **Project:** stayconnectenterprise
- **Source Git HEAD at export:** `4a64b00` (documentation baseline; all copied content matches this commit)
- **Export date:** 2026-07-16
- **Current phase:** Phase 0 **FINAL / CLOSED**; Phase 1A is the **current** phase, **not implemented** (plan status `READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL`).
- **Sanitization:** guest-linked identifiers (room number, reservation `G#`, identity fingerprints) redacted in the file marked *(sanitized copy)*; no passwords, private keys, tokens, or guest names are present in any file. All technical findings preserved.

## Files

| # | Exported filename | Original repository path | Source commit | Status | Verified phase context | SHA-256 |
|---|---|---|---|---|---|---|
| 1 | `00-START-HERE.md` | *(generated for this pack)* | `4a64b00` | Entry point | Phase 1A current | `6e5087fb0635bfd1d13ebea6788d56172dc7a3d3d4a9949e7ae61e5e1c8212a5` |
| 2 | `PROJECT-INSTRUCTIONS.md` | *(generated for this pack)* | `4a64b00` | Project config | Phase 1A current | `04139516da3f1e4b6ab2ace84df2e16212ffe2c3a1ccbee53421e6200963cb3a` |
| 3 | `StayConnect-IAM-Phase0-Contract.md` | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | `4a64b00` | **Authoritative** | Phase 0 FINAL/CLOSED | `4a3e15da073c4d0723dac2d0876812839bec99647d0694c75e3369a7ac65a4d5` |
| 4 | `StayConnect-IAM-Handoff.md` | `docs/context/StayConnect-IAM-Handoff.md` | `4a64b00` | **Authoritative** | Phase 1A current | `78106d065990d5e25570b96f5ab98cbab8f9f57956a179d5f0a0a93733054837` |
| 5 | `StayConnect-IAM-Phase1A-Plan.md` | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | `4a64b00` | **Authoritative** | Phase 1A current (READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL) | `7602f62c115b867d3f68feebc4e7da079cdc998a299d0b017476d05db94bd159` |
| 6 | `Protel-FIAS-Phase0-Spike.md` | `docs/spikes/Protel-FIAS-Phase0-Spike.md` | `4a64b00` | **Authoritative** *(sanitized copy)* | Phase 0 evidence — Gate 3A PASS | `6fcf680010be6baaf8756da9930dd2110cb528b7f8eb91f2a93860a3b59242b1` |
| 7 | `SYSTEM_OVERVIEW.md` | `docs/SYSTEM_OVERVIEW.md` | `4a64b00` | Supporting | Current live system | `76ceea20af87daa820254072efc3723a3345a0c7de1b880f918e20e90e971d99` |
| 8 | `TARGET_ARCHITECTURE.md` | `docs/TARGET_ARCHITECTURE.md` | `4a64b00` | Supporting | Target architecture | `48d24dd5ad3189f862f7d0830deb604008fc97bc3e15f08e227593dfc9c83337` |
| 9 | `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | `4a64b00` | Supporting | Current live system | `88860a1e98498d6d065a9c2f5c7441915cf9a65c693eede67dfa7a043f1ffcee` |
| 10 | `DEPLOYMENT_APPLIANCE.md` | `docs/DEPLOYMENT_APPLIANCE.md` | `4a64b00` | Supporting | Current live system | `9b49b50f5fd1e0b63f6960fe7a67a8b46857482a0eb6c804810ebd3f3a93c0d8` |
| 11 | `OFFLINE_OPERATION.md` | `docs/OFFLINE_OPERATION.md` | `4a64b00` | Supporting | Current live system | `02f0a7aaa1d76986bc18f6d03aeac13ca8a98b053639235734f8cb460d15ffb0` |
| 12 | `MIGRATION_RUNBOOK.md` | `docs/MIGRATION_RUNBOOK.md` | `4a64b00` | Supporting | Current live system | `3a0faa5500a885ee9c7447b81f0252cab1cc33f62faddde10300ad118a5f4e3c` |

*(This MANIFEST is not self-referential; its own checksum is omitted by definition.)*

## Precedence (authoritative order)

1. `StayConnect-IAM-Phase0-Contract.md` (FINAL contract) → 2. `StayConnect-IAM-Handoff.md` → 3. `StayConnect-IAM-Phase1A-Plan.md` → 4. `Protel-FIAS-Phase0-Spike.md` (verified evidence) → 5. system/ops docs (SYSTEM_OVERVIEW, TARGET_ARCHITECTURE, operations manual, deployment, offline, migration) → 6. historical chats → 7. superseded drafts.

## Validation summary (at export)

- ✅ No file states Phase 0 is CONDITIONALLY FROZEN as a live status (only historical "previously …" mentions remain).
- ✅ Phase 0 FINAL / CLOSED asserted consistently in the authoritative docs.
- ✅ Phase 1A status consistent everywhere: `READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL`, not implemented.
- ✅ No phase marked implemented without verified evidence; single next authorized action (Product-Owner approval of the Phase 1A plan).
- ✅ Hotel ID 2 (Aqua Club) remains read-only capable and **financially unapproved**.
- ✅ `programmatic_reversal = false` (v1) — manual Front Office correction only.
- ✅ No secrets (passwords/keys/tokens) and no guest PII/identifiers present.
- ✅ Core/authoritative pack links (`00-START-HERE`, the four IAM docs) resolve **inside the pack** (cross-links flattened to pack basenames).
- ℹ️ Supporting docs (SYSTEM_OVERVIEW, TARGET_ARCHITECTURE, operations manual, deployment, offline, migration) retain **repo-relative links to the broader documentation set** that is intentionally **not** part of this minimal pack (e.g. `EDGE_NETWORKING.md`, `CLOUD_ARCHITECTURE.md`, `user-guide/README.md`). These are reference pointers to the full repository, not pack-internal navigation.

## Intentionally excluded (and why)

- **Superseded/duplicate contract drafts** — none exist as separate files; the contract is maintained in place, so only the current FINAL version is exported (no old versions to duplicate).
- **Broader supporting docs** referenced by the copied files (edge networking, cloud architecture, DHCP, security hardening, licensing, data ownership, user guides, etc.) — outside the requested minimal pack; available in the full repository.
- **Secrets & credentials** — no passwords, private keys, API tokens, or Stripe/webhook secrets are present in the source docs (only field labels/schema descriptions), so none were exported.
- **Guest PII** — guest names/contact/passport data are not in the docs; guest-linked room/reservation identifiers and identity fingerprints were **redacted** from the sanitized spike copy.
- **Code, migrations, binaries, deploy artifacts** — out of scope for a documentation pack.
- **Unrelated build artifacts** (e.g. `hotel-admin-deploy.tgz`, `*.tsbuildinfo`) — not documentation.
