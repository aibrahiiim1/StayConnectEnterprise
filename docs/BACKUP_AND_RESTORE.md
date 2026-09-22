# Backup & Restore

> Two independent backup domains, matching the data-ownership split: each
> site backs up its own `stayconnect_site` DB locally; the cloud backs up the
> central `stayconnect` DB. Guest PII therefore stays inside the hotel even
> in backup form.

## 1. Site DB backup (per appliance)

### What

- `pg_dump -Fc` (custom format) of `stayconnect_site`;
- `/etc/stayconnect/` (identity keypair, **license files** `current.json` /
  `state.json` / `revoked.json`, env files, and the **generated network
  revisions** under `/etc/stayconnect/generated/network/` — see below);
- optionally the Caddy internal CA material.

**Phase 19 networking is covered by the existing site-DB backup.** The site DB
is the source of truth for guest networks/VLANs/DHCP: the `network_interfaces`,
`guest_networks`, `dhcp_pools`, `dhcp_reservations`, `network_config_revisions`,
`network_apply_events` and `network_health_checks` tables are all in the
`stayconnect_site` `pg_dump`, and the added `sessions` columns
(`guest_network_id`/`vlan_id`/`ingress_interface`/`gateway_ip`) come with the
`sessions` table. The rendered artifacts under
`/etc/stayconnect/generated/network/revision-NNNNNN/` are reproducible from the
DB (netd re-renders on apply), so they need no separate backup — but the `tar` of
`/etc/stayconnect` above captures them anyway, which speeds recovery by keeping
the last active revision's bundle on hand ([EDGE_NETWORKING.md](EDGE_NETWORKING.md)).

### How

`deploy/scripts/stayconnect-site-backup.sh` is the supported backup script. It runs nightly from
`stayconnect-site-backup.timer`, and can be run by hand at any time:

```sh
deploy/scripts/stayconnect-site-backup.sh
```

> **THE NIGHTLY TIMER NOW EXISTS. It did not when this section first described it.** This text used to
> open "A backup agent (cron/systemd timer on the appliance) runs", and there was none: nothing under
> `deploy/` invoked the script and no cron entry existed. The only backup-related unit was
> `stayconnect-backup-cleanup`, which *prunes* artefacts and creates none. So every backup the appliance
> had ever had was taken by a human typing the command, while the Policy block below stated a nightly
> schedule.
>
> `deploy/systemd/stayconnect-site-backup.{service,timer}` closes that. **02:10 local time**, not UTC,
> because the policy is "a low-traffic hour in the site's timezone" and a hotel's quiet hour is a property
> of where the hotel is. That is comfortably before the 03:30 retention cleanup, so a night's backup exists
> before retention runs and the newest full backup the cleanup protects is the one just taken.
> `Persistent=true`, so an appliance powered off overnight takes its backup when it returns rather than
> skipping a day in silence.
>
> **The script remains the guard, not the unit.** It refuses on its own -- no resolvable role, a client
> older than the server, an empty dump, and an `/etc` archive containing the financial restore marker, which
> it verifies by listing the archive rather than trusting the `--exclude`. `Type=oneshot` with no `Restart=`:
> a failed backup is a fact for an operator to see in the journal, and a retry loop on a full disk makes the
> disk worse.
>
> **Installation needs no list.** `install-service-units.sh` derives every `/opt/stayconnect/bin` helper from
> the units being installed and REFUSES the whole install if one has no source -- a rule that exists because
> `stayconnect-backup-cleanup.service` was once absent from a hand-written list, failed `203/EXEC` nightly
> for weeks, and filled the root filesystem until PostgreSQL could not write `postmaster.pid` and the site
> went down. The new unit is picked up by that same mechanism, which
> `install-service-units-selftest.sh` asserts.

The script is the two commands this section always described, implemented, plus the one exclusion that only
matters once Phase 4 exists:

```sh
OUT=/var/backups/stayconnect/site-$(date +%Y%m%d-%H%M%S).dump
pg_dump -Fc -U stayconnect_site stayconnect_site -f "$OUT"
tar czf "${OUT%.dump}-etc.tgz" --exclude=financial-restore-generation.json /etc/stayconnect
```

> **The financial restore marker is deliberately NOT backed up.**
> `/etc/stayconnect/financial-restore-generation.json` counts how many times THIS appliance has been
> restored. Its whole purpose is to survive a database restore and keep counting forward, so that a restored
> database can be recognised as older than the appliance knows it should be. An `/etc` archive that
> contained it would roll it back on restore, and the restored database would then match it perfectly — the
> rollback detector would go quiet at exactly the moment it is needed. The backup script verifies the
> exclusion rather than trusting the flag, and refuses to produce an archive that contains it.
>
> Migration 0025 also detects the opposite direction. If an `/etc` archive taken before this change is ever
> restored, the marker will be BEHIND the database, the two records will disagree about this appliance's
> restore history, and money movement is held until an operator establishes which is right.

**`backup_records` is written by the OTHER backup path, not by this script.** The table exists (migration
0001) and `POST /edge/v1/backups` — the Hotel-Admin path, which produces `db-<stamp>.sql.gz` through scd —
records every run in it with `status running→ok/failed` and `kind scheduled|manual|pre_migration`. That is
what the backups page shows.

`stayconnect-site-backup.sh` never touches the database, so **a run of the sanctioned shell backup leaves no
`backup_records` row and does not appear on the backups page.** This section previously said "Every run is
recorded", which was true of one path and not of the one it was describing. The two paths are genuinely
different artefacts: the shell script produces `site-<stamp>.dump` (pg_dump custom format) plus an `/etc`
archive and a metadata file, which is what `stayconnect-financial-restore.sh` consumes; the API path
produces a gzipped SQL dump that only the scd restore endpoint accepts. Neither tool accepts the other's
output.

The reference to a `backup` telemetry kind is also historical: Central is licensing-only and the telemetry
tables were dropped by migration 0045, so no fleet view reports backup health from telemetry.

### Policy

- Schedule: nightly, low-traffic hour in the site's timezone. **IMPLEMENTED** by
  `stayconnect-site-backup.timer` at 02:10 local, `Persistent=true`.
- Retention: keep 7 daily + 4 weekly on the appliance; prune by age and by
  the license's retention limits for the underlying data.
- Off-box copies go to **hotel-controlled** storage (NAS/SFTP on the hotel
  network) — never to StayConnect cloud storage (PII boundary,
  [DATA_OWNERSHIP.md](DATA_OWNERSHIP.md)).
- ~~HA pairs: back up on the current primary only (replication covers the secondary); the agent checks VRRP
  state before running.~~ **NOT APPLICABLE AND NOT IMPLEMENTABLE TODAY.** There is no HA: decision ARCH-04
  is ACTIVE and records that HA failover under the approved two-NIC architecture is not designed,
  implemented or accepted, and the HA-sync transport is an open architecture decision. There is no
  keepalived, no VRRP and no agent anywhere in the tree, so there is no VRRP state to check and no secondary
  to skip.

### Three public tables no migration creates — expected, and what a restore must know

A restored site database is **not** built only from `data-plane/migrations/`. Three tables in the `public`
schema are created by `scd` itself, the first time the feature that needs them is used:

| Table | Created by | First used for |
|---|---|---|
| `public.edge_executed_commands` | `data-plane/cmd/scd/commands.go` | the Central command channel |
| `public.edge_installed_updates` | `data-plane/cmd/scd/updates.go` | recording an installed update |
| `public.edge_offline_packages` | `data-plane/cmd/scd/offline_import.go` | consuming an offline package |

All three are `CREATE TABLE IF NOT EXISTS`, so the behaviour is deterministic and idempotent, and on a live
appliance all three are owned by the installation superuser (`stayconnect`) and carry Gate P's grants to
`svc_scd`.

**Why this matters to a restore, and to anyone comparing two databases.** `deploy/gatep/gatep-grants.sql`
grants on all three. Applying the migration lineage to an empty database and then running Gate P therefore
**fails** — the tables do not exist yet — unless `scd` has run first or the DDL is applied alongside it. A
rebuild that reaches "the same schema" without them is not the same schema.

- **Restoring from a dump:** they are in the dump, because they exist in the source database. Nothing to do.
- **Rebuilding from migrations:** expect them to be absent until `scd` starts, and expect Gate P to fail if it
  is run before that. This is exercised by `iam_v2_scratch/phase7_reconstruct_from_sources.sh`, which applies
  the same DDL from those three source files at the point in the accepted history where it belongs.
- **Comparing a rebuilt database with an appliance:** their absence is the expected difference until `scd` has
  run, and is not evidence of a failed restore.

### Restore (site)

**Once Phase 4 is deployed, use the supported tool rather than the raw commands:**

```sh
deploy/scripts/stayconnect-financial-restore.sh   --dump     /var/backups/stayconnect/site-<stamp>.dump   --manifest /var/backups/stayconnect/site-<stamp>.manifest.json   --tenant   <tenant-uuid> --site <site-uuid>
```

It does what the raw commands below do, plus the three things that make a financial restore trustworthy:

1. **Verifies the manifest against this appliance's pinned registry root anchor**
   (`/etc/stayconnect/assignment-registry-root.pub`, the same anchor the assignment registry uses). There is
   no `--pubkey` option, and passing one is an error: a verification key supplied by whoever runs the
   restore proves only that they have a key.
2. **Proves every financial writer is stopped** — `stayconnect-edged`, `-pmsd`, `-acctd`, `-portald`,
   `-scd` — by checking `systemctl is-active` after stopping each one, and aborts if any cannot be proven
   stopped. It does not swallow a failed stop.
3. **Advances the management marker BEFORE restoring**, so that even a crash mid-restore leaves the
   appliance able to detect that its database is older than it should be, and then records the restore and
   enters `FINANCIAL_RECOVERY_MODE`.

After it completes, guest internet access runs normally and **money movement is held** until an operator
reconciles every item that was in flight when the backup was taken. Nothing is replayed automatically.

The underlying commands, for reference and for a pre-Phase-4 appliance:

```sh
systemctl stop stayconnect-edged stayconnect-pmsd stayconnect-acctd stayconnect-portald stayconnect-scd
dropdb  -U postgres stayconnect_site
createdb -U postgres -O stayconnect_site stayconnect_site
pg_restore -U stayconnect_site -d stayconnect_site /var/backups/stayconnect/site-<stamp>.dump
tar xzf site-<stamp>-etc.tgz -C /            # identity + license + env; contains NO financial marker
systemctl start stayconnect-scd stayconnect-portald stayconnect-acctd stayconnect-pmsd stayconnect-edged
```

> Restoring the `/etc` archive never rewrites `financial-restore-generation.json`, because the backup
> excludes it. If an older archive that predates that exclusion is restored, the marker ends up BEHIND the
> database; migration 0025 detects the disagreement and holds money movement rather than assuming which
> record is right.

Post-restore checks: scd health; voucher login from the netns client; license
evaluation (`GET /edge/v1/license`) — note the license store's high-water mark
restores with `/etc/stayconnect`, so clock-rollback protection stays intact;
`sync_outbox` rows restored but already-sent seqs re-published are harmless
(cloud dedupe drops them — replay-safe by design).

## 2. Cloud DB backup

### What

- `pg_dump -Fc` of `stayconnect` (tenants, sites, appliances, plans,
  subscriptions, **licenses incl. signed envelopes**, fleet telemetry,
  operators, audit);
- the **vendor signing key** (`CTRLAPI_VENDOR_KEY` file) — backed up
  separately, encrypted, access-restricted: losing it means no new licenses
  can be signed until a key rotation is pushed to every appliance; leaking it
  means anyone can mint licenses. Treat like a CA key.
- Redis is *not* backed up (operator sessions are disposable). NATS JetStream
  state is transport-level; the outbox pattern makes it recoverable.

### How

Nightly cron on the cloud host (pilot: the VM):

```sh
pg_dump -Fc -U stayconnect stayconnect > /root/backups/cloud/cloud-$(date +%Y%m%d).dump
```

Retention 14 daily + 8 weekly, copied off-host. TimescaleDB note: `-Fc` dumps
handle hypertables (`fleet_telemetry`, `audit_log`, `usage_counters`,
`accounting_records` while legacy data remains) via the timescaledb catalog;
restore into a database with the extension pre-created at the same version.

### Restore (cloud)

```sh
systemctl stop stayconnect-ctrlapi
dropdb -U postgres stayconnect && createdb -U postgres -O stayconnect stayconnect
psql -U postgres -d stayconnect -c "CREATE EXTENSION timescaledb"
pg_restore -U stayconnect -d stayconnect cloud-<stamp>.dump
systemctl start stayconnect-ctrlapi
```

Post-restore checks: `readyz`; `GET /cloud/v1/licenses/` lists envelopes; an
appliance license fetch succeeds. That is the whole list — Central serves the
appliance for **licensing only**.

> **HISTORICAL — do not perform.** This step used to continue: "telemetry
> ingest resumes — appliances will re-drain anything unacked and
> `fleet_telemetry_dedupe` (restored with the dump) drops what already landed",
> and it offered a rebuild statement `INSERT INTO fleet_telemetry_dedupe SELECT
> appliance_id, seq, now() FROM fleet_telemetry ON CONFLICT DO NOTHING`.
>
> Both tables are **gone**. Migration `0045_central_is_licensing_only_remove_telemetry`
> dropped them, `control-plane/internal/fleet` was deleted, the telemetry
> endpoints answer 404 and there are no NATS containers on Central. The
> statement would fail on a table that does not exist, and an operator following
> it after a restore would reasonably conclude the restore was incomplete.
>
> The telemetry link was built, verified and **switched off by decision** —
> 87 000 records were delivered and both sides reconciled first. A static
> outbox, a closed transport and a missing fleet view are that decision, not a
> fault, and reconnecting telemetry is **not** a repair for anything. See
> `current_state_facts.central_scope` in `governance/project-state.json`.

**Key property of the architecture: a cloud restore never interrupts hotels.**
Appliances keep serving guests on their persisted licenses throughout
([OFFLINE_OPERATION.md](OFFLINE_OPERATION.md)).

## 3. Restore drills

Run quarterly, and once as part of pilot acceptance:

| Drill | Steps | Pass criteria |
|---|---|---|
| Site restore | restore latest site dump to a scratch DB (`stayconnect_site_drill`), count rows vs `backup_records.size_bytes` era, spot-check a voucher and a session | pg_restore exit 0; counts plausible; no FK errors |
| Full appliance rebuild | fresh VM → deploy stack → restore site dump + `/etc/stayconnect` → run phase 1 suite | guest login green without touching the cloud |
| Cloud restore | restore cloud dump to scratch; issue a test license against it | envelope signs & verifies |
| Vendor-key escrow check | decrypt the escrowed key, `LoadSigner` succeeds, key_id matches production | key_id equality |
| Outage replay | combine with the cloud-outage drill: restore cloud from a dump taken *before* an edge outage window, confirm outbox re-drain and dedupe | no duplicate `(appliance_id, seq)` rows |

Record every drill in the cloud audit log (`backup.drill` action) and, for
site drills, as a `manual` row in `backup_records`.
