# Backup & Rollback Artifact Retention (permanent production rule)

Deployment/rollback artifacts on **both** Central and the Appliance are pruned by
one fail-safe tool — `stayconnect-backup-cleanup` — driven by a single policy.
Accumulation can no longer silently fill the disk, and required rollback/recovery
material can never be deleted.

## Tool

`/opt/stayconnect/bin/stayconnect-backup-cleanup` (source:
`deploy/scripts/stayconnect-backup-cleanup.sh`).

- **Default is DRY-RUN** — prints the KEEP / DELETE / PIN / PROTECTED plan, deletes
  nothing. `--apply` performs deletions. `--json` prints the status document.
- **Idempotent** — re-running after apply deletes nothing more.
- **Fail-safe** — if the host role is unknown or the current/previous rollback
  symlinks don't resolve, it refuses to delete anything.
- **Auditable** — every run appends to `/var/log/stayconnect/backup-cleanup.log`
  and writes `/opt/stayconnect/backup-retention-status.json` (last run, disk %,
  alert, rollback validity, failures, retained/pinned/protected/delete lists).
- Role is detected by the UI symlink each host owns (`cloud-admin-current` →
  Central, `hotel-admin` → Appliance).

## Runs automatically

- **After every deployment** — the deploy paths call `--apply`
  (`deploy/scripts/deploy-hotel-admin.sh`, Makefile `ctrlapi-install`).
- **Daily safety net** — `stayconnect-backup-cleanup.timer` (03:30, `Persistent=true`,
  enabled on both hosts, survives reboot).

Install on a host: `make backup-cleanup-install`.

## Retention policy (per artifact type)

| Artifact | Location | Retention |
|---|---|---|
| Binary rollback backups (ctrlapi / scd / edged / acctd `*.bak-*`, `*.prev*`) | `/opt/stayconnect/bin` | newest **5 per binary** |
| UI releases (cloud-admin / hotel-admin) | `/opt/stayconnect/releases/*` | newest **5** + current + previous |
| DB dumps | `/root/backups/*.sql*` | newest **7**; newest is never deleted |
| Config backups (`*.bak*`) | `/etc/netplan`, `/etc/stayconnect` | newest **3** |

Tunable in `/etc/stayconnect/backup-retention.conf` (`KEEP_BINARIES`, `KEEP_RELEASES`,
`KEEP_DB`, `KEEP_CONFIG`, `DISK_WARN`, `DISK_CRIT`).

## Never deleted (hard guarantees)

1. The current deployed version (live binary + current release symlink target).
2. The previous known-good rollback version (`*.previous` symlink target).
3. The newest successful full DB backup.
4. PKI / custody / recovery material — Central `ca-ceremony-backup`,
   `nats-migration-backup`, all `/etc/stayconnect/*.key|*.pub|*.crt`; Appliance
   `/etc/stayconnect/{identity,certs,tls,assignment,license,generated}` and
   `vendor-license.key`. These are classified **PROTECTED** and never enumerated
   for deletion.
5. Operator-pinned artifacts — any path listed in
   `/etc/stayconnect/backup-retention.pins` (one per line) or with a `.pinned`
   sibling file.

Because current + previous are always retained, a **compatible rollback** (see
`ROLLBACK_POLICY.md`) is always possible after cleanup.

## Disk thresholds & alerts

`DISK_WARN` (80%) and `DISK_CRIT` (90%) of `/`. Each run records the level in the
status JSON, logs it to journald (`logger -t stayconnect-backup`), and writes a
Prometheus textfile metric (`stayconnect_backup_disk_pct`,
`stayconnect_backup_rollback_valid`, `stayconnect_backup_cleanup_failures`) if a
node-exporter textfile collector directory exists. A critical disk level or any
failure makes the run exit non-zero so the systemd timer surfaces it.

## Central visibility

`GET /cloud/v1/backup-health` and the **Backup health** page (Administration →
Operations) show Central's last cleanup, disk usage + alert, rollback-path
validity, failures, and the retained / pinned / protected / delete-candidate
artifact lists. The Appliance writes the same status file locally
(`/opt/stayconnect/backup-retention-status.json`).
