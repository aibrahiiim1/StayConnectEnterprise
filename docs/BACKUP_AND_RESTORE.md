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

A backup agent (cron/systemd timer on the appliance) runs:

```sh
OUT=/var/backups/stayconnect/site-$(date +%Y%m%d-%H%M%S).dump
pg_dump -Fc -U stayconnect_site stayconnect_site > "$OUT"
tar czf "${OUT%.dump}-etc.tgz" /etc/stayconnect
```

Every run is recorded in the site DB's **`backup_records`** table
(`status running→ok/failed`, `kind scheduled|manual|pre_migration`, path,
size, error) — which is what Hotel Admin's backups page and the `backup`
telemetry kind report, so both hotel staff and cloud fleet view can see a
site whose backups are failing. Manual runs: `POST /edge/v1/backups`.

### Policy

- Schedule: nightly, low-traffic hour in the site's timezone.
- Retention: keep 7 daily + 4 weekly on the appliance; prune by age and by
  the license's retention limits for the underlying data.
- Off-box copies go to **hotel-controlled** storage (NAS/SFTP on the hotel
  network) — never to StayConnect cloud storage (PII boundary,
  [DATA_OWNERSHIP.md](DATA_OWNERSHIP.md)).
- HA pairs: back up on the current primary only (replication covers the
  secondary); the agent checks VRRP state before running.

### Restore (site)

```sh
systemctl stop stayconnect-edged stayconnect-acctd stayconnect-portald stayconnect-scd
dropdb  -U postgres stayconnect_site
createdb -U postgres -O stayconnect_site stayconnect_site
pg_restore -U stayconnect_site -d stayconnect_site /var/backups/stayconnect/site-<stamp>.dump
tar xzf site-<stamp>-etc.tgz -C /            # identity + license + env
systemctl start stayconnect-scd stayconnect-portald stayconnect-acctd stayconnect-edged
```

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
appliance license fetch succeeds; telemetry ingest resumes — appliances will
re-drain anything unacked and `fleet_telemetry_dedupe` (restored with the
dump) drops what already landed. If the dedupe table was restored *older*
than `fleet_telemetry`, some duplicates may land; dedupe rows can be rebuilt:
`INSERT INTO fleet_telemetry_dedupe SELECT appliance_id, seq, now() FROM
fleet_telemetry ON CONFLICT DO NOTHING`.

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
