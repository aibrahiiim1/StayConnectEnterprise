# Phase 2 — Least-Privilege Grant Matrix (machine-reviewable)

**As-designed for Phase 2 (commercial packages), DARK.** Phase 2 introduces domain logic, APIs and UI over the already-existing dark `iam_v2` commerce schema **behind flags that are all OFF**. It grants **zero** new production runtime privileges: while every Phase-2 flag is OFF the commerce repository is never constructed, no Phase-2 route is mounted, and no Phase-2 SQL is issued. The Phase-1B least-privilege posture (`svc_scd`/`svc_edged`/`svc_acctd`/`svc_netd`, `NOSUPERUSER`, SCRAM, `public`-only) remains in force unchanged.

<!-- MACHINE ASSERTION — validated by tools/project-state.py -->
`PRODUCTION_IAM_V2_DML: NONE`  (no production runtime service role holds any `iam_v2` INSERT/UPDATE/DELETE/SELECT/EXECUTE grant; Phase 2 adds none)

**Binding rules (unchanged from Phase 1B; Phase 2 conforms):**
- **Production** service roles keep **only** their existing `public`-schema privileges — **zero `iam_v2` DML**, **zero `iam_v2` EXECUTE**. Phase 2 requests no new grant.
- The Phase-2 commerce repository (`iamv2.PgCommerceRepository`) is constructed **only** when the Phase-2 master flag is ON. While dark the engine holds a **nil** repository and issues zero SQL (mirrors the Phase-1B dark authenticator).
- Enabling any Phase-2 flag against production `iam_v2` — and therefore any grant of `iam_v2` privilege to a runtime role — requires a **separate** later runtime-routing (cutover) authorization; it is out of scope for this DARK Phase.
- No service role receives `ALL TABLES`, owner membership, `CREATE`/`ALTER`/`DROP`, `BYPASSRLS`, or superuser.
- `iam_v2` privileges appear **only** in the scratch/test section of the Phase-1B matrix (disposable Phase-2 test DB `iamv2_p2`, loopback-only, no committed credentials).

## Phase-2 runtime privilege delta vs Phase 1B

| Role | Phase-1B grants | Phase-2 delta | net `iam_v2` runtime privilege |
|---|---|---|---|
| `svc_scd` (site DB) | `public`-only (session/auth/credential) | **none** | – (zero) |
| `svc_edged` (site DB) | `public`-only (admin config) | **none** | – (zero) |
| `svc_acctd` | `public`-only (accounting) | **none** | – (zero) |
| `svc_netd` | `public`-only (network) | **none** | – (zero) |

Rollback for the whole Phase = leave every Phase-2 flag OFF (default). No REVOKE is needed because no GRANT is performed. See `docs/architecture/StayConnect-IAM-Phase2-Plan.md` (sentinel `PHASE_2_PRODUCTION_RUNTIME: DARK`) and the Phase-1B matrix `docs/architecture/Phase1B-Privilege-Matrix.md` for the full production grant rows this Phase inherits unchanged.
