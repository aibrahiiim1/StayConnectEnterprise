# Current-State Assessment — Refactor Baseline (2026-07-11)

Input to the cloud-controlled / hotel-local refactor. Facts verified against the running pilot VM (`172.21.60.23`) and the repo on 2026-07-10/11, not assumed from documentation.

## 1. Current architecture (as verified)

One monolithic trust domain. A single VM runs **both planes plus all infrastructure**:

- `ctrlapi` (Go, :8080) — admin/commercial API **and** guest-domain API (vouchers, sessions, PMS config, walled garden) in one binary, one namespace (`/v1/*`).
- `scd`/`portald`/`acctd` — gateway daemons; correct edge components, but they read/write the **central** Postgres directly.
- `web-admin` (Next.js :3000) — one UI serving platform-admin, tenant-admin and hotel-operator needs indistinguishably.
- Postgres+Timescale, Redis, NATS, Caddy, Prometheus/Grafana/Alertmanager — single instances shared by everything.

## 2. Current data ownership

**Everything lives in one database (`stayconnect`), one schema.** Verified table counts at baseline (backup `refactor-baseline-20260710-233530`):

tenants 2 · sites 2 · appliances 2 · operators 26 · ticket_templates 4 · voucher_batches 24 · vouchers 311 · guests 7 · sessions 103 · accounting_records 13,356 · auth_otps 3 · pms_providers 1 · pms_attempts 11 · plans 6 · tenant_subscriptions 65 · audit_log 284 · payments 0 · walled_garden_rules 0.

Guest PII (guest names, MACs, emails, OTP hashes, PMS attempt logs, per-guest accounting) sits in the same schema as commercial data (plans, subscriptions). Nothing prevents cloud-side code from reading guest data; isolation is only per-query `tenant_id` filtering (no RLS).

## 3. Central-Postgres dependency of the data plane

Verified touchpoints in `data-plane/`:
- `internal/tenantcfg` → `SELECT auth_methods FROM tenants` (per-request).
- `internal/session` → `tenant_effective_limits` view (concurrency check) + `guests`/`sessions` writes.
- `internal/voucher`, `internal/otp`, `internal/pmsguard`, `internal/pmsloader`, `notifyloader`, `socialloader`, `cmd/scd/pms_admin.go` → all read/write central tables.
- `acctd` → `accounting_records`, `sessions`, quota joins.

**Consequence:** if the central Postgres is unreachable, guest auth *stops* — the opposite of the edge-first requirement. (NATS outage is already tolerated; DB outage is not.)

## 4. Current UI responsibilities

`web-admin` mixes: tenant CRUD + subscription management (cloud concerns) with voucher batches, sessions, PMS providers, walled garden (hotel concerns) in one app, one login, one role check layer (server-side only).

## 5. Current hierarchy and role model

- Hierarchy exists in schema: tenants → sites → appliances (verified live: `dev` → `hq`/`lobby` → `dev-appliance`/`lobby-appliance`). No group/site user-assignment model — roles are tenant-wide only (`platform_admin, tenant_admin, tenant_operator, viewer, billing`). No site-scoped operator concept.
- "Plan" is overloaded: `plans` = commercial subscription; `ticket_templates` = guest access plan. Naming collision throughout UI/docs.
- License/entitlement: none. Limits come from `tenant_effective_limits` DB view — requires central DB at runtime; no signature, no offline grace, no states.

## 6. Baseline test results (re-run 2026-07-11, not assumed)

25 suites executed on the VM after reboot: **19 PASS, 6 FAIL** on first run — all 6 failures (`phase2-quota`, `phase4-email-otp`, `phase4-pms-5b`, `phase4-pms`, `phase4-sms-otp`, `phase4-social`) share one cause: the `client1` netns test fixture does not survive reboot (`Cannot open network namespace "client1"`). Not a code regression. Re-run of the 6 after recreating the netns: see `/root/baseline-e2e-rerun.log` (expected green; verified below in the phase log).

## 7. Repo ↔ VM drift (verified by checksum sweep)

| Item | State |
|---|---|
| `deploy/compose/infra.yml`, `deploy/observability/docker-compose.yml` | VM adds docker log-rotation (`json-file`, 50m×5) — port back to repo |
| `/etc/kea/kea-dhcp4.conf` | VM has RFC 8910 option 114; repo copy lacks it — persist in repo |
| `deploy/ha/*`, `docs/*` | exist only locally (never deployed; HA not in use on single-node pilot) |
| `web-admin` runtime artifacts | `package-lock.json`, `next-env.d.ts` VM-only (expected) |
| Everything else (Go sources, scripts, units, nftables, Caddy) | **identical** |

## 8. Files/components requiring modification (refactor map)

| Component | Change |
|---|---|
| `control-plane` | Becomes **Cloud API**: keep tenants/sites/appliances/commercial-plans/subscriptions/enrollment/fleet; add licensing+entitlement issuing, `/cloud/v1` namespace, fleet telemetry ingest; deprecate guest-domain routes |
| `data-plane` | Gains **site-local DB** (own migrations), **`edged`** Hotel API (`/edge/v1`), license validator, sync outbox; `scd/acctd/tenantcfg/session/voucher/otp/pms*` re-pointed at local DB |
| new `license/` module | Shared Ed25519-signed entitlement document: sign (cloud), verify+state machine (edge) |
| `web-admin` | Split → `cloud-admin/` (commercial/fleet) + `hotel-admin/` (guest domain, served from appliance) |
| `deploy` | Second Postgres DB on appliance, edged+sync systemd units, Caddy vhost for hotel-admin on mgmt IP, nftables tightening (drop WAN :8080/:3000, IPv6 handling) |
| `scripts` | Keep 25 suites (compat layer), add isolation/offline/license suites, netns persistence fix |
| migration tool | New `cmd/sitemigrate` (central schema → site DB, dry-run, idempotent, rollback package) |

## 9. Migration risks

1. **scd re-pointing** is the riskiest step — every guest auth path touches the DB layer. Mitigation: same schema shape locally, switch by DSN, phase tests after each deploy step.
2. **65 `tenant_subscriptions` rows** are test debris (repeated plan swaps); migration must take only the latest non-terminal row per tenant.
3. Hypertables (`accounting_records`, `audit_log`) need `create_hypertable` on the site DB before data copy; timestamps must be preserved.
4. Existing E2E suites assume the shared DB + `/v1` routes; they need a compatibility window (aliases) or they all go red at once.
5. One-VM pilot means cloud and edge share a Postgres *instance* initially (separate databases). Acceptable: isolation is per-database with separate credentials; physical separation is a deploy-topology change, not a code change.
6. Live VM must never be left half-migrated: each deploy step ends with health checks + relevant phase suites.

## 10. Ports and processes (verified live)

22 SSH · 53 unbound (10.10.0.1) · 443 caddy · 3000 web-admin · 3001 grafana (lo) · 4222 NATS (lo) · 5432 pg (lo) · 6379 redis (lo) · 8080 ctrlapi (all ifaces — **WAN-exposed, to close**) · 8380/8343 portald · 9090/9093 prom/am (lo) · 9101 scd metrics (lo). Backup of record: `/root/backups/refactor-baseline-20260710-233530/` (pg_dumpall, code tar, etc-*, systemd units, table counts, hierarchy snapshot).
