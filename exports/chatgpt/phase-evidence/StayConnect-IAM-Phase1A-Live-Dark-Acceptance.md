# StayConnect IAM — Phase 1A LIVE-DARK Acceptance Record

**Verdict: PASS.** The isolated `iam_v2` schema was created in the **production** `stayconnect_site` database, **dark** (no service reads/writes, no DSN/`search_path` change), and passed live-dark acceptance 18/18. **NOT deployed, NOT cut over, NOT live accepted as a user-facing system; no IAM data migration; no Phase 1B.**

## Execution date & environment
- **Date:** 2026-07-16 ~16:37 UTC.
- **Appliance:** `radius` (172.21.60.23), reached via SSH (root, key auth).
- **Database engine:** container `stayconnect-pg`, image `timescale/timescaledb:2.16.1-pg16`, **PostgreSQL 16.3**, extensions `plpgsql, pgcrypto, uuid-ossp, timescaledb`.
- **Target database:** `stayconnect_site` (**primary**, `pg_is_in_recovery()=false`), size 23 MB, 42 public base tables.
- **Runner:** `iam_v2_scratch/prod_live_dark.sh` (copied to appliance `/root/iam_v2_live/`) + the reviewed migration files `iam_v2_scratch/migrations/mg1..mg9_*.sql` from commit `b2a715f`.

## Authorization scope
Product-Owner authorized **only**: production read-only preflight; backup + verification; create isolated `iam_v2`; execute MG-0..MG-9; run live-dark acceptance; roll back on failure; sync docs; commit. **Not** authorized (and not done): any DSN/`search_path` change; any service reading/writing `iam_v2`; dual-write; Phase 1B; portal/auth behavior change; binary deployment; service restart; PMS/FIAS traffic; PMS config/`pms_providers`; financial posting; WAN/LAN/VLAN/DHCP/firewall/routing/HA change; Central change; IAM data migration; cutover; legacy cleanup.

## Target-safety preflight (server-side, read-only)
- `current_database()=stayconnect_site`; primary; **`iam_v2` absent**; no `iam_v2_*` roles; no partial Phase-1A objects.
- `public.guest_networks` columns include `(id, tenant_id, site_id, …)`; **0 duplicate `(tenant,site,id)`**; **1 row**; no pre-existing anchor.
- 0 long-running transactions (>5 min); 0 blocked locks; 5.4 GB free disk.
- **Material-difference gate:** production platform schema is compatible with the offline-verified schema (`guest_networks` shape matches; the MG-0 anchor is representable). Production has TimescaleDB (scratch used plain PG16), but `iam_v2` uses **no** hypertables, so this is immaterial to the dark schema.

## Backup & rollback evidence
- **Pre-Phase-1A backups (on appliance `/root/backups/`, no secrets):**
  - `phase1a_predark_20260716T163702Z_full.dump` — custom-format full dump, **495 957 B**, sha256 `6b3567b2da76ba9b962468abd66be3d858a34662e96b0e5988262e61819324f4`, `pg_restore --list` = **319 TOC entries** (validity check passed).
  - `phase1a_predark_20260716T163702Z_schema.sql` — schema-only dump, **77 966 B**, sha256 `34b0172ac427c8c71cf0bdfdc9ea3c1d3d87767e222353e197ad6177f9636346`, **109 DDL statements**.
  - A recent nightly full backup also pre-existed (`/root/backups/…20260716T031701Z…`).
- **Rollback commands:** `DROP SCHEMA iam_v2 CASCADE;` + `DROP INDEX CONCURRENTLY IF EXISTS public.guest_networks_tsi_anchor;` + drop the phase-created roles.
- **Rollback PROVEN:** during a first attempt an acceptance-query typo (stray `)` in AC-14) triggered the automatic rollback — it dropped all 60 `iam_v2` objects + the anchor and **restored the public fingerprint to `d86ca4c6…` (identical to pre-state)**. The subsequent clean run then created and accepted the schema.

## MG-0 through MG-9 results
- **MG-0** (non-transactional): duplicate pre-check 0; `CREATE UNIQUE INDEX CONCURRENTLY guest_networks_tsi_anchor ON public.guest_networks (tenant_id, site_id, id)`; **`indisvalid=true`**, exact definition verified, build **≈59 ms**, no bare `IF NOT EXISTS`. (1-row table ⇒ negligible lock/application impact; services stayed active.)
- **MG-1..MG-9** applied as `iam_v2_owner` (each file logged with its SHA-256):
  `mg1_pms_interface_core a3a0e72b…` · `mg2_plans_packages 6f29d90d…` · `mg3_identities_credentials 85673fc5…` · `mg4_stay_domain 25fbcfc1…` · `mg5_auth_commerce 408de439…` · `mg6_entitlements_devices_sessions bf09ada3…` · `mg7_postings_payments 37531288…` · `mg8_resolution_aux a47a3e0f…` · `mg9_engine e403e313…`.

## Catalog fingerprint
`iam_v2` catalog fingerprint = **`bd75026ff6ea5835a1ca8d19051eb257`** — **byte-identical** to the scratch build and the offline-real-schema build. 49 tables, **252 constraints, 12 triggers, 11 functions**. This proves every PK/unique/CHECK/composite-FK/partial-index/trigger/function in production matches the scratch build that passed the full 99-test functional suite.

## Live-dark acceptance matrix (18/18 PASS)
| # | Check | Result |
|---|---|---|
| AC-01 | exactly 49 `iam_v2` tables | PASS |
| AC-02 | `iam_v2` catalog fingerprint == verified scratch (`bd75026f`) | PASS |
| AC-03 | public schema unchanged except the MG-0 anchor (`d86ca4c6` before==after) | PASS |
| AC-04 | public live-row totals unchanged **at the atomic acceptance window** (8278) — *the public platform is live and grows normally (8345 at V2 re-verification); the durable invariant is the public **structural** fingerprint `d86ca4c6`, which is unchanged* | PASS |
| AC-05 | public base-table count unchanged (42) | PASS |
| AC-06 | no `iam_v2*` objects leaked into `public` | PASS |
| AC-07 | zero rows in `iam_v2` (dark) | PASS |
| AC-08 | schema owned by `iam_v2_owner` | PASS |
| AC-09 | all `iam_v2` tables owned by `iam_v2_owner` | PASS |
| AC-10 | `folio_identity_strategy DEFAULT 'UNSET'` | PASS |
| AC-11 | folio-`UNSET` `charge_gate` trigger present | PASS |
| AC-12 | no executable reversal function | PASS |
| AC-13 | default `search_path` excludes `iam_v2` | PASS |
| AC-14 | service role `stayconnect` has no `iam_v2` in a role-level `search_path` | PASS |
| AC-15 | PUBLIC has no privileges on `iam_v2` | PASS |
| AC-16 | non-superuser service-equivalent role denied SELECT on `iam_v2` | PASS |
| AC-17 | immutable-revision trigger fires (UPDATE rejected) — functional probe, rolled back | PASS |
| AC-18 | folio-`UNSET` fail-closed CHARGE rejected before outbox/`P#` — functional probe, rolled back | PASS |

## Proof of darkness / no service or config change
- **`iam_v2` live rows = 0** (no service has written to it).
- **No DSN change; no `search_path` change** — default `search_path` is `"$user", public`; the `stayconnect` service role has no role-level `search_path` referencing `iam_v2`.
- **Zero `iam_v2` references in production code** (`data-plane`/`control-plane`) — no code path routes to it.
- **Services unaffected:** `stayconnect-scd/edged/acctd/portald` all `active`; `nftables`/`unbound` active; 0 scd errors in the window; guest-path networking untouched (no WAN/LAN/VLAN/DHCP/nft/tc change).

## Security, ownership & isolation
- Schema + all 49 objects owned by `iam_v2_owner`; `iam_v2_migrator` is a member of the owner; service-equivalent roles `iam_v2_svc_{scd,edged,acctd,portald,hoteladm}` created NOLOGIN with **no** privileges; PUBLIC has none; default privileges deny future access.
- **DEVIATION (recorded):** the actual production services connect as role **`stayconnect`, which is the DB superuser** — a superuser bypasses grants, so privilege-based isolation does **not** bind the real service role. Darkness of `iam_v2` therefore rests on: (a) **zero** `iam_v2` references in service code, and (b) `search_path` excluding `iam_v2` (both verified), plus (c) the authorization forbidding any DSN/`search_path` change. The owner/service-role model is created for the eventual dedicated-role model but is not the current isolation mechanism against the superuser service role.

## Limitations & NOT-YET-AUTHORIZED runtime tests
Not proven here (require later, separately-authorized phases): appliance reboot re-attach, real `scd`/`acctd` integration, nft/tc zero-churn, real-traffic accounting, service session-revocation side effects, live DSN/`search_path` cutover, IAM data migration, cutover. These are **NOT YET AUTHORIZED**, not PASS.

## Authoritative commits
- Implementation/migrations + scratch evidence: `b2a715f`.
- This live-dark evidence + documentation sync: recorded in the commit that carries this file (see git log).
- **Authoritative sanitized production evidence: `iam_v2_scratch/review/prod/PROD_LIVE_DARK_EVIDENCE_V2.txt`** (clean read-only `psql -f prod_verify.sql`, `PSQL_EXIT=0`, 0 verification fails). The earlier `PROD_LIVE_DARK_EVIDENCE.txt` had shell-escaped-quote corruption and is retained **`[SUPERSEDED — EVIDENCE ERROR]`** for audit only — **not** authoritative.
- **Superuser deviation (Phase-1B prerequisite):** production services connect as PostgreSQL **superuser `stayconnect`** (`rolsuper=true`), so grant-based schema isolation does not bind them. This is **not** a blocker to the already-created dark schema, but it **is a blocker to Phase 1B runtime integration and any service write to `iam_v2`**: Phase 1B must not route any service to `iam_v2` until a **separately reviewed least-privilege service-role migration + credential-rotation plan** exists (with rollback, per-service DSNs, secret handling, connection testing, reboot persistence). Not performed now.

## Zero-Stale-Leftovers validation
- **`tools/validate-project-state.sh` → `ZERO_STALE_LEFTOVERS = PASS`** (all checks: no stale Phase-1A planning-only/scratch-only/plan-approval-gated/live-dark-future current-state text; single maturity + consistent next action; no within-file maturity conflict; acceptance record present and references the V2 evidence with V1 marked superseded; exact SOURCE and PROJECT_PACK_EXPORT commit provenance; permanent rule bundled with resolving links; validator bundled + checksummed in the Evidence Pack; MANIFEST checksums match packaged files; all pack links resolve; no secrets/guest-PII/credential-DSNs in exports). Run against **both** the repository source and the generated Project Pack, per the permanent rule `docs/ZERO_STALE_LEFTOVERS_RULE.md`.

## Final maturity & next action
- **Maturity:** `PHASE1A_PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED` (dark, isolated, additive, reversible) — on top of SCRATCH_IMPLEMENTED + SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED.
- **Single next authorized action:** **Product-Owner review of this live-dark acceptance before any Phase 1B authorization.** No cutover, DSN/`search_path` change, data migration, or service routing is authorized.
