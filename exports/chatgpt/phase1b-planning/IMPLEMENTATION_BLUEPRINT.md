# Phase 1B — Proposed Product-Owner Implementation Authorization (BLUEPRINT — DO NOT EXECUTE)

Extracted from `StayConnect-IAM-Phase1B-Plan.md` §14. This is a **proposal** for a future single PO implementation authorization; it is **not** executed by the planning task.

> "APPROVED — implement Phase 1B (credential/portal integration, DARK) end-to-end in one controlled execution, per `docs/architecture/StayConnect-IAM-Phase1B-Plan.md`.
>
> **Authorized:** create least-privilege site-DB roles (`svc_scd/edged/acctd/netd`, `iam_v2_migrator`) + grants + per-service 0600 DSN env files, and move those services off superuser `stayconnect` (Gate P), with negative-permission acceptance; implement W0–W9 (iam_v2 data-access, voucher/account/OTP/social validators, auth_context service, shadow session/entitlement adapter, feature flags default OFF, observability) in `data-plane/`; run full [IMPL] acceptance in a disposable scratch DB; deploy to production with **all flags OFF**; run [DARK] acceptance (roles, flags-OFF non-regression, zero unauthorized iam_v2 access, guest-plane non-regression, secret/PII scan); back up before changes; sync docs + regenerate export packs; run `tools/validate-project-state.sh` (repository + extracted-pack) → `ZERO_STALE_LEFTOVERS = PASS`.
>
> **NOT authorized (hard stops):** guest-visible activation or `search_path`/DSN cutover to iam_v2; any guest-visible decision taken from iam_v2; dual-read/dual-write; production credential-data migration into iam_v2; PMS/post-stay/paid auth; packages/quotes/pricing/purchases-as-commerce (Phase 2); financial posting (Phase 4); programmatic reversal; network/HA/deployment-topology change; Central changes; legacy cleanup; Phase 2 or any later Phase.
>
> **Acceptance:** Phase-1B matrix [IMPL]+[DARK] green; **zero guest-visible change**; services non-superuser; no secret/PII leak. **Rollback:** flags OFF and/or DSN revert to legacy; roles are additive/droppable; no data divergence. **Do not continue to Phase 2.** Stop and report on any negative-permission failure, guest-visible regression, unauthorized iam_v2 write, secret/PII leak, or acceptance red."

## Approval ladder (plan §13)
1 plan approval · 2 read-only preflight · 3 backup/rollback prep · 4 least-privilege roles + acceptance (Gate P) · 5 app/db implementation flags-OFF · 6 scratch acceptance · 7 production dark deploy · 8 dark acceptance · 9 docs/export sync + ZERO_STALE_LEFTOVERS=PASS · 10 PO review before any guest-visible activation. Separate authorization still required for: guest-visible activation/cutover, financial posting, legacy cleanup, later Phases.
