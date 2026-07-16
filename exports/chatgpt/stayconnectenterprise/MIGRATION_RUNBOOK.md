# Migration Runbook — Central Schema → Site-Local DB

> **Scope banner:** this runbook describes the **already-delivered Central-to-site *edge*
> migration** (moving a site's guest rows from the shared central Postgres into its isolated
> `stayconnect_site` database via `cmd/sitemigrate`). **It is NOT the future IAM `iam_v2`
> migration.** Do not confuse this with Phase-1A `iam_v2` implementation — that is a separate,
> not-yet-started effort governed by `docs/architecture/StayConnect-IAM-Phase1A-Plan.md`.

> Phased cutover of one site from the shared central Postgres to its isolated
> `stayconnect_site` database, using `cmd/sitemigrate` (idempotent, dry-run,
> per-table count reports, rollback package). Written for the pilot VM
> (cloud + edge on one Postgres instance, separate databases/DSNs) — the same
> steps apply per-site in production.
>
> Ground rules (from CURRENT_STATE_ASSESSMENT.md §9):
> never leave the VM half-migrated — every phase ends with health checks and
> the relevant phase suites; scd re-pointing is the riskiest step and is done
> by DSN swap only (identical schema shape).

## Phase 0 — Preconditions

- [ ] Migration `0019_licensing_fleet` code deployed (ctrlapi with licensing
      service; `CTRLAPI_VENDOR_KEY` generated and configured).
- [ ] `edged` binary and `hotel-admin` bundle built and staged.
- [ ] A license **issued for the site** (`POST /cloud/v1/licenses`) — the edge
      limit bridge needs it at first start.
- [ ] Maintenance window agreed (guest sessions survive; admin UI blips).

## Phase 1 — Backup (rollback package, part 1)

```sh
STAMP=$(date +%Y%m%d-%H%M%S); mkdir -p /root/backups/sitemigrate-$STAMP
pg_dumpall -U stayconnect > /root/backups/sitemigrate-$STAMP/pg_dumpall.sql
tar czf /root/backups/sitemigrate-$STAMP/etc-stayconnect.tgz /etc/stayconnect
cp /etc/stayconnect/{scd,ctrlapi}.env /root/backups/sitemigrate-$STAMP/
psql -U stayconnect -c "\dt+" > /root/backups/sitemigrate-$STAMP/table-inventory.txt
```

Verify the dump is restorable size-wise and record row counts of the guest
tables (the baseline for count reports).

## Phase 2 — Apply cloud migration 0019

```sh
psql -U stayconnect -d stayconnect -f control-plane/migrations/0019_licensing_fleet.up.sql
```

Verify: `commercial_plans` view exists; `licenses`, `fleet_telemetry`,
`fleet_telemetry_dedupe` created; new `plan_limits` keys
(`feature.paid_wifi`, `max_guest_access_plans`) seeded. Restart ctrlapi;
`GET /cloud/v1/version` and `GET /cloud/v1/licenses/` respond.

## Phase 3 — Create the site DB

```sh
psql -U postgres -c "CREATE ROLE stayconnect_site LOGIN PASSWORD '<generated>'"
psql -U postgres -c "CREATE DATABASE stayconnect_site OWNER stayconnect_site"
psql -U stayconnect_site -d stayconnect_site \
     -f data-plane/migrations/0001_edge_init.up.sql
```

Notes: **separate credentials** — the cloud role must have no grants on the
site DB and vice versa. On the pilot (TimescaleDB present) the migration's DO
block converts `accounting_records`/`audit_log` to hypertables automatically.

## Phase 4 — `sitemigrate --dry-run`

```sh
sitemigrate --site <site-uuid> \
  --from "postgres://stayconnect:...@127.0.0.1/stayconnect" \
  --to   "postgres://stayconnect_site:...@127.0.0.1/stayconnect_site" \
  --dry-run
```

Review the per-table count report. Expected behaviors:

- copies only rows belonging to the site's tenant/site scope;
- `tenants`/`sites` land as exactly one row each; `appliances` = the site's;
- `tenant_subscriptions` are **not** copied (cloud domain) — remember the 65
  test-debris rows centrally; entitlements arrive via the license instead;
- operator rows copy verbatim (legacy roles admitted during the window —
  ROLE_AND_SCOPE_MATRIX.md §5);
- hypertable timestamps preserved 1:1.

Do not proceed until dry-run counts match expectations against Phase 1's
baseline numbers.

## Phase 5 — Run `sitemigrate` (real)

Same command without `--dry-run`. It is idempotent — safe to re-run after an
interruption (upserts by primary key). Afterwards compare source vs
destination counts per table (the tool prints both); investigate any drift
before continuing. Record a `backup_records` row of kind `pre_migration` via
psql for the audit trail.

## Phase 6 — Re-point scd/acctd at the site DB

The riskiest step; it is a two-line env change:

```sh
# /etc/stayconnect/scd.env    : SCD_DB_URL   = postgres://stayconnect_site:...@127.0.0.1/stayconnect_site
# /etc/stayconnect/acctd.env  : ACCTD_DB_URL = postgres://stayconnect_site:...@127.0.0.1/stayconnect_site
systemctl restart stayconnect-scd stayconnect-acctd
```

Immediately verify: scd health on the socket; a **new voucher login** from the
netns client (proves voucher/session/guest writes hit the site DB); acctd tick
writing `accounting_records` in the site DB; concurrency limit still enforced
(proves the license → `tenant_effective_limits` bridge is populated — if scd
logs a missing license, upload/fetch it first).

## Phase 7 — Start edged (+ Hotel Admin)

```sh
systemctl enable --now stayconnect-edged
# Caddy: add the mgmt-IP vhost for hotel-admin + /edge/v1, reload caddy
```

Verify: `GET https://<mgmt-ip>/edge/v1/health`; log in with a migrated
site_admin; license page shows Active; create/revoke a test voucher batch.
Confirm the vhost is bound **only** to the management IP.

## Phase 8 — Verification suites

```sh
for s in scripts/phase{1,2}*-test.sh scripts/phase4*-test.sh scripts/phase6-test.sh; do bash "$s"; done
```

plus the new isolation/offline/license suites and the cloud-outage drill in
[OFFLINE_OPERATION.md](OFFLINE_OPERATION.md) §4. All green = cutover done;
legacy `/v1` guest routes now serve frozen data for this site
(API_DEPRECATIONS.md).

Known test gotchas that are *not* regressions: netns fixture doesn't survive
reboot; SIGPIPE with `grep -q` under pipefail; partial-index `ON CONFLICT`
(see SYSTEM_OVERVIEW §16).

## Phase 9 — post-cutover cloud cleanup (do NOT skip)

`sitemigrate` is deliberately **non-destructive**: it copies the site's guest
rows into the site DB but leaves the originals in the cloud `stayconnect`
schema so a rollback has a source. That means immediately after cutover the
cloud DB still holds historical guest PII (guests, sessions, accounting,
auth_otps, pms_attempts, vouchers). The *ongoing* data flow is already
correct — scd/acctd write only to the site DB, and fleet telemetry is
PII-free — but the residue must be purged once the migration is confirmed and
the rollback window has closed.

Preconditions before purging: (a) the site has run guest traffic against the
site DB for the agreed soak period; (b) the rollback package is archived
off-box; (c) `docs/API_DEPRECATIONS.md` legacy `/v1` guest routes are
scheduled for removal or already pointed at the site DB via
`CTRLAPI_GUEST_COMPAT_DB_URL`.

Purge (per migrated site, inside a transaction, after a fresh `pg_dump` of the
guest tables):

```sql
BEGIN;
DELETE FROM accounting_records WHERE tenant_id = :tenant;
DELETE FROM sessions            WHERE tenant_id = :tenant;
DELETE FROM auth_otps           WHERE tenant_id = :tenant;
DELETE FROM pms_attempts        WHERE tenant_id = :tenant;
DELETE FROM social_oauth_states WHERE tenant_id = :tenant;
DELETE FROM vouchers            WHERE tenant_id = :tenant;
DELETE FROM voucher_batches     WHERE tenant_id = :tenant;
DELETE FROM guests              WHERE tenant_id = :tenant;
DELETE FROM pms_providers       WHERE tenant_id = :tenant;
DELETE FROM walled_garden_rules WHERE tenant_id = :tenant;
COMMIT;
```

Keep `tenants`, `sites`, `appliances`, `plans`, `tenant_subscriptions`,
`licenses`, `fleet_telemetry`, `operators` (platform/group), `audit_log` — those
are cloud-owned. **Status on the pilot (2026-07-11): NOT yet purged** — the
dev tenant's historical guest rows still exist in the cloud schema pending the
soak window. This is expected and safe (no new guest PII is written to cloud),
but it is the one open cleanup item from the pilot cutover.

## Rollback procedure

Trigger: guest auth failing after Phase 6/7 and not diagnosable within the
window.

1. **Revert DSNs**: restore the saved `scd.env`/`acctd.env` from the rollback
   package (central DSN); `systemctl restart stayconnect-scd stayconnect-acctd`.
2. Stop edged; remove/disable the mgmt vhost (`systemctl disable --now
   stayconnect-edged`).
3. The central DB was never written to as part of cutover — rows created in
   the site DB during the failed window (new sessions/vouchers) are exported
   with `sitemigrate --reverse` (or pg_dump of the affected tables) and
   replayed centrally if the window carried real traffic.
4. Only if the central DB itself was damaged: restore from
   `/root/backups/sitemigrate-$STAMP/pg_dumpall.sql`.
5. Re-run phase 1/2 suites to confirm the pre-migration state; file the
   failure before retrying.

Keep the rollback package until the legacy routes are removed.
