# StayConnect IAM — Phase 1B Planning Evidence Pack

**Planning-only. Phase 1B is NOT implemented.** This pack bundles the evidence behind the Phase 1B implementation plan.

- `StayConnect-IAM-Phase1B-Plan.md` — the complete plan (scope §1, least-privilege prerequisite §2, current inventory §3, target architecture §4, dark/flag rollout §5, data/migration decision §6, migration/app breakdown §7/§8, threat model §9, offline/failure §10, observability §11, **acceptance matrix §12**, approval ladder §13, **implementation blueprint §14**, risks + open PO decisions §16).
- `inventory/DB_ACCESS_MAP.md` — per-service PostgreSQL access (all services connect as superuser `stayconnect` today).
- `inventory/AUTH_ENTRY_POINTS.md` — the six auth paths + shared portald→scd pipeline + legacy tables.
- `inventory/IAM_V2_OBJECTS.md` — the verified `iam_v2` credential/portal object set (no new DDL needed).
- `Phase1B-Privilege-Matrix.md` — the exact machine-reviewable least-privilege grant matrix (production = zero `iam_v2` DML; scratch-only `iam_v2` grants; migration-executor model).
- `IMPLEMENTATION_BLUEPRINT.md` — the proposed single PO implementation-authorization prompt (§14 extract).
- `PACK_SHA256SUMS.txt` — SHA-256 of every file physically in this pack.
- `REPOSITORY_ARTIFACT_SHA256SUMS.txt` — SHA-256 of the committed source files the inventories cite (not packaged).
- `MANIFEST.md` — provenance (source sync commit; export commit).

**Provenance:** built from source sync commit `afade95`. Scope boundary: Phase 1B = voucher/account/OTP/social credential + auth_context (dark); PMS=Phase 3, packages=Phase 2, posting=Phase 4, paid=absent/out. Single next authorized action: **Product-Owner approval or rejection of the Phase 1B plan.**
