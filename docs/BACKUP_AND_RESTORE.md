# Backup & Restore

> Two independent backup domains, matching the data-ownership split: each
> site backs up its own `stayconnect_site` DB locally; the cloud backs up the
> central `stayconnect` DB. Client PII therefore stays inside the site even
> in backup form.

## 1. Site DB backup (per appliance): one backup system

An appliance has **one** backup system: the engine in `scd` (`data-plane/cmd/scd/backups.go`), configured and used from
**Hotel Admin → Backups**. There is one format, one retention policy and one restore. The nightly `stayconnect-site-backup` job
and its plain `pg_dump -Fc` sets are **retired** (see *Earlier backups* below); there is no plaintext fallback.

### What a backup is

Every backup, whatever its kind, is the same thing:

- **The database**, `db-<stamp>.sql.gz.age` (or `db-safety-<stamp>…` for the one a restore takes first): a plain-SQL dump,
  gzip-compressed and **encrypted with [age](https://age-encryption.org) to every backup key holder as it is written** -- nothing plain
  touches the disk.
- **The configuration and keys**, `etc-<stamp>.tgz.age`: `/etc/stayconnect` (identity, licence, service environment, the seal keys, the
  generated network revisions), **without** the financial restore marker (see below).
- **A note**, `db-<stamp>.meta.json` (plain, no personal data): the **kind** and the reason, who asked, the SHA-256 of what is
  stored, the number of tables, and **the ledger head the backup contains** (which a restore uses to tell whether a copy of the erasure
  ledger is complete).
- **Proof it restores.** The same SQL stream that is being encrypted is loaded into a scratch database, which must accept it without
  one error and hold the appliance's tables and an intact erasure ledger. A backup that fails is discarded, and the backup is marked
  verified (`db-….verified`) so the Backups screen can restore it.
- **Never without a key holder.** With no holder a backup is not taken (the screen says why); a backup is never written in the clear.

### The four kinds, and what depends on the schedule

| Kind | When | Depends on the *Scheduled backups* switch? |
|---|---|---|
| **Scheduled** | on the schedule you set | **Yes -- and it is OFF by default** |
| **Manual** | *Take a backup now* (password step-up) | No |
| **Before a change** (`pre-change`) | `scd backup-now`, run first by `scripts/edge-migrate.sh` for every live-site migration and rollback, and by deployments | **No -- mandatory** |
| **Before a restore** (`pre-restore`) | the first step of every restore | **No -- mandatory** |

**Scheduled backups are off until an administrator switches them on** (Hotel Admin → Backups → *Scheduled backups*). The switch controls
only the schedule. It does **not** disable any protection: a restore always takes its safety backup, a live migration or rollback
refuses to run without a fresh verified backup (`edge-migrate.sh` exits 4), the financial marker and recovery mode always apply, the
Erasure Ledger is always kept in two places, and the binary and UI rollback artefacts are always kept by the sweep.

### Settings (Hotel Admin → Backups → Scheduled backups; stored in `/etc/stayconnect/backup-retention.conf`)

| Setting | Default | Range | Meaning |
|---|---|---|---|
| `BACKUP_ENABLED` | **0 (off)** | 0–1 | scheduled backups on/off |
| `BACKUP_HOUR` | 2 | 0–23 | a scheduled backup is taken at or after this hour (appliance clock) |
| `BACKUP_EVERY_DAYS` | 1 | 1–7 | days between scheduled backups; a manual backup counts |
| `BACKUP_KEEP` | **7** | 1–30 | scheduled + manual backups kept |
| `SAFETY_BACKUP_KEEP` | 3 | 2–20 | protective backups kept: before a change, before a restore |

Saving needs the password and is audited. **Retention is counts only** -- no age rule beside them to contradict them. Rotation keeps
the newest of each pool, removes older sets (dump, configuration archive, note, marker) only after a newer backup verified, and **never
touches a file that has no note**. An erased guest can remain in the oldest backup for about `BACKUP_KEEP × BACKUP_EVERY_DAYS` days;
the restore re-applies the ledger so they are erased again ([privacy/ERASURE_LEDGER.md](privacy/ERASURE_LEDGER.md)). Each day every kept
file is re-checked against the digest in its note; a mismatch is shown on the Backups screen while other backups remain.

The retention sweep (`stayconnect-backup-cleanup`) is **not** a second retention policy: it keeps the disk from filling with binaries,
releases and config backups, reports the backups, and **never deletes a database backup**.

### Encryption to the hotel's own keys (GDPR plan C5, decision D-C5)

Hotel Admin → Backups → *Backup encryption* registers **backup key holders**. Every backup is encrypted to every active holder's public
key as it is written.

- **The appliance holds public keys only.** It can write backups and never read them back. Adding a holder generates the key pair on the
  appliance and hands the private key over **once**, as a key file and a printable recovery sheet; the appliance keeps no copy. The holder
  counts only after the **check code** printed in both is typed back. There is no escrow: the supplier holds no key either.
- **Several holders.** Any one holder's key opens a backup alone. Removing a holder stops new backups being encrypted to it; backups
  taken earlier still open with it. The last active holder cannot be removed.
- **Opening a backup on the appliance** (Verify, Restore, and the restore test) asks for a holder's key, used in memory for that one
  action and never stored or logged. A wrong key is refused before a restore touches anything.
- **A restore's own safety backup is encrypted to the registered holders and also to the restoring holder's key**, so a replacement
  appliance with no registered holder yet is still protected. There is no plaintext fallback.
- **Restore test.** Hotel Admin asks every `RESTORE_TEST_MONTHS` (default 6) for a holder to prove their key opens the newest backup.
- **Downloads stay encrypted.** The file downloaded is the encrypted file.
- **An appliance moved to another hotel** encrypts only to the holders of the site it now serves (migration 0144); a previous owner's
  holders are never used.
- **A restore keeps the current key holders.** Before the restored database is swapped in, the live register is copied into it: a holder
  removed since the backup stays removed, and a holder the live register does not know is removed by the restore. If the register cannot
  be carried, nothing is replaced.
- **If every key is lost**, the encrypted backups cannot be read by anyone. Live data is unaffected; add a new holder and new backups
  work at once.

To open a backup by hand, a holder supplies their key file (standard age format, so `age -d -i <key-file>` also works off the appliance):

```sh
/opt/stayconnect/bin/scd backup-crypt decrypt -i <key-file> /opt/stayconnect/backups/db/db-<stamp>.sql.gz.age - | gunzip | less
```

### Restore (site): Hotel Admin → Backups → Restore

Only a verified backup can be restored; the restore needs the password, the backup's name typed out, and a holder's key. The same code
serves `scd backup-restore --name <backup> --identity-file <key-file>` when Hotel Admin itself cannot be used. It runs these steps, each
shown in the result (`/run/stayconnect/last-restore.json`), and **nothing is replaced until the data has loaded, been checked and the
services are proven stopped**:

1. **privileges** -- the live database's ownership and access rules (including the database-level `GRANT CREATE ON DATABASE` Gate-P gives
   `iam_v2_owner`, which a dump never carries) are captured.
2. **ledger_prepare** -- every erasure the live database knows is made durable outside it.
3. **safety_backup** -- a `pre-restore` backup of the current state (same engine, encrypted, proven restorable).
4. **maintenance**, **load** (into a staging database), **key_holders**, **ledger_storage**, **ledger** (the Erasure Ledger is applied to
   the *staging* data -- see below).
5. **quiesce** -- every service that can write financial state (`edged`, `pmsd`, `acctd`, `portald`) is stopped and then **proven stopped**
   (`systemctl is-active`); one that is still running, or whose state cannot be read, aborts the restore with nothing replaced.
6. **financial_marker** -- the management marker `/etc/stayconnect/financial-restore-generation.json` is advanced **before** the swap
   (written by `scd marker-advance` in a transient systemd unit, because `scd` itself cannot write `/etc/stayconnect`), so a crash
   mid-restore still leaves the next start holding money movement.
7. **swap** -- the live database is renamed aside, the staging database renamed in.
8. **privileges** (again) -- everything captured in step 1 is reinstated and compared; any difference rolls the swap back.
9. **integrity**, then **hypertables** (TimescaleDB's hypertables must be *registered*, not merely shaped: an unregistered one refuses every
   write) and **financial_recovery** (`iam_v2.p4_record_supported_restore`: the site enters `FINANCIAL_RECOVERY_MODE`). A failure of any of
   these renames the original database back.
10. **ledger_synced**, **retention_reapplied**, **housekeeping**, **services**.

After a restore, client internet access runs normally and **money movement is held** until an operator reconciles every item that was in
flight when the backup was taken (Hotel Admin → Financial recovery). Nothing is replayed automatically.

> **The financial restore marker is deliberately NOT backed up.** It counts how many times THIS appliance has been restored. Its whole
> purpose is to survive a database restore and keep counting forward, so that a restored database can be recognised as older than the
> appliance knows it should be. A configuration archive containing it would roll it back and the detector would go quiet exactly when it is
> needed. `writeEtcArchive` excludes it (a test holds it there). It is `0644` and root-owned: root-owned so only root can advance it,
> world-readable because `edged` runs unprivileged and evaluates it; a `0600` marker once disarmed the detector (measured on PRE-LIVE).
> If an older configuration archive is ever restored, the marker is BEHIND the database and migration 0025 holds money movement rather than
> assuming which record is right. The marker is protected by file ownership and permissions only; there is no TPM or monotonic counter in
> this appliance profile and none is claimed.

**Authorization of a restore.** The retired command-line tool required a manifest signed at Central. The supported restore replaces that
with the password step-up, a digest-bound verified backup (the restore refuses a file whose contents no longer match what was verified)
and the **enforced** financial recovery mode. This is a documented change of the authorization model, not an omission.

### Earlier backups (what the retired mechanisms left behind)

Before there was one system there were two. The retired nightly job wrote `pg_dump -Fc` sets (`site-<stamp>.dump`, its
`.meta.json` and `site-<stamp>-etc.tgz`, optionally `.age`) to `/var/backups/stayconnect`, and older on-demand dumps and restore safety
dumps in `/opt/stayconnect/backups/db` have no note. They are **earlier backups**:

- never deleted automatically -- not by rotation, not by the retention sweep;
- listed on the Backups screen with their size and whether they are encrypted;
- an earlier **dump** that was verified can still be restored from the list; a **nightly set** is a custom-format archive the Backups
  restore cannot load, and is restored by hand (below) until it is removed;
- removed only by an administrator (**password + typed confirmation `remove earlier backups`**, audited), and only once a verified
  scheduled or manual backup exists and every stored backup still matches its checksum -- so removing the old never leaves less than there was.

By-hand restore of a nightly set (`stayconnect-financial-restore.sh` is kept only for this; it is dark and not wired to anything):

```sh
deploy/scripts/stayconnect-financial-restore.sh --dump /var/backups/stayconnect/site-<stamp>.dump \
  --manifest /var/backups/stayconnect/site-<stamp>.manifest.json --tenant <tenant-uuid> --site <site-uuid>
```

### Upgrading an appliance that predates backup encryption (migration 0129)

The mandatory pre-change backup is taken by the **running** `scd` for the **registered** key holders. An appliance older than migration
0129 has neither -- its `scd` has no `backup-now` and the database has no key register -- so the first upgrade across 0129 cannot use
them, and a plain dump is not an acceptable stand-in. The bootstrap is a one-time, **encrypted** dump with the new binary's offline
tool, to a one-time recipient whose private key never touches the appliance:

```sh
# on a trusted workstation: a one-time key pair (age-keygen); keep bootstrap.key, send only the public key line
# on the appliance, with the NEW scd binary copied to /root/scd-new (it is not installed yet):
echo "<the age1... public key>" > /root/bootstrap.pub
docker exec stayconnect-pg pg_dump -U stayconnect -d stayconnect_site --no-owner --no-privileges | gzip   | /root/scd-new backup-crypt encrypt -R /root/bootstrap.pub - /opt/stayconnect/backups/db/pre-0129-bootstrap-$(date -u +%Y%m%dT%H%M%SZ).sql.gz.age
```

Prove it restores (decrypt with the private key on the workstation and load it into a scratch database), then apply the migrations with
`scripts/edge-migrate.sh` and the pre-change command pointed at a wrapper that only checks that this fresh, proven dump exists
(`EDGE_PRECHANGE_BACKUP`); from the moment the new `scd` runs and a key holder is registered, every later change uses `scd backup-now`.
Nothing else about the runner changes, and there is no flag to skip the backup.

### Policy

- Schedule: **the hotel's choice, off by default** (see above). Protective backups are always taken.
- Retention: counts, in two pools (7 scheduled+manual, 3 protective, by default). No age rule.
- Off-box copies go to **site-controlled** storage (NAS/SFTP on the site network) -- never to StayConnect cloud storage (PII boundary,
  [DATA_OWNERSHIP.md](DATA_OWNERSHIP.md)). The Erasure Ledger's secondary storage is mandatory for erasure; backups themselves stay on
  the appliance until the hotel downloads them (decision D-B).
- ~~HA pairs~~ **NOT APPLICABLE AND NOT IMPLEMENTABLE TODAY.** There is no HA: decision ARCH-04 is ACTIVE.

### The `public` tables 0048 creates and 0093 drops -- what a restore must know

`public.edge_offline_packages` (the single-use ledger of imported offline activation packages, which the offline reconcile reads) is
created by appliance migration **0048**, owned on a live appliance by the installation superuser (`stayconnect`), and granted to `svc_scd`
by Gate P. It is in every dump and in every database rebuilt from migrations.

Migration **0093** removed the cloud telemetry subsystem and drops `public.sync_outbox`, `public.sync_checkpoints`,
`public.edge_executed_commands` and `public.edge_installed_updates` with their rows (authorized). Where the applying role cannot drop them
(a live site applies as `iam_v2_owner`) they are left for their owner, see [ROLLBACK_POLICY.md](ROLLBACK_POLICY.md) §"Appliance schema
boundaries".

- **Restoring a dump taken before 0093:** the four tables come back with the dump; applying 0093 (and the owner statement) removes them
  again. Nothing reads them and Gate P no longer grants on them.
- **Comparing a rebuilt database with an appliance:** an appliance that has applied 0093 but not yet run the owner statement still carries
  the four tables; that is the expected difference, not a failed restore.

Post-restore checks: scd health; voucher login from the netns client; licence evaluation (`GET /edge/v1/central/status`) -- note the
licence store's high-water mark restores with `/etc/stayconnect`, so clock-rollback protection stays intact. An appliance restored from a
dump of a site that Central has since deleted still finds out on its next hello: if it had held a customer it enters the *Removed from
OneGate Central* state (the marker `/etc/stayconnect/removed-from-central.json` restores with `/etc` and is honoured), and only a
factory-clean install brings it back.

## 2. Cloud DB backup

### What

- `pg_dump -Fc` of `stayconnect`: customers (`tenants`), sites, appliances and
  their assignment, certificate, retirement, security-alert and
  offline-activation tables, the assignment signing-key registry, the
  retired-identity register, **licenses incl. signed envelopes**, operators and
  roles and `audit_log`
  ([CLOUD_ARCHITECTURE.md §2](CLOUD_ARCHITECTURE.md#2-central-database-ownership)).
  Central holds no client or commercial history (migration 0047 dropped the
  `legacy_archive` schema);
- the **vendor signing key** (`CTRLAPI_VENDOR_KEY` file) — backed up
  separately, encrypted, access-restricted: losing it means no new licenses
  can be signed until a key rotation is pushed to every appliance; leaking it
  means anyone can mint licenses. Treat like a CA key.
- the **assignment signing key**, **registry root key** and the appliance CA
  keys (intermediate online, root offline) — separately, encrypted, like the
  vendor key.
- Redis is *not* backed up (operator sessions are disposable). Central runs no
  message bus.

### How

`stayconnect-central-backup.timer` (daily, 02:15) runs `deploy/scripts/central-backup.sh`: `pg_dump -Fc` of
the `sc-central-pg` container's `stayconnect` database to
`/opt/stayconnect/backups/db/central-<stamp>-scheduled.dump.age` (0600, proven readable with `pg_restore -l`
before it counts, then encrypted with `age` to every public key listed in `/etc/stayconnect/backup-recipients.txt`;
Central keeps no private key, so it cannot read its own backups; restore with
`age -d -i <key> <file> | docker exec -i sc-central-pg pg_restore ...`). Until that file lists a key the dump is a
plain `.dump` and every run warns; once it does, a dump that cannot be encrypted is a failed backup, never a plain
one. Add a key: `age-keygen -o key.txt` on the restorer's own machine, then append its `age1...` line to the file. Every deploy takes the same dump as `central-<stamp>-pre-<sha12>.dump`. Retention is
`KEEP_DB` in `/etc/stayconnect/backup-retention.conf` (the newest dump and pinned dumps are never deleted);
*System → Backup health* reports it. The dumps are **local only** — an off-host copy is the operator's
responsibility ([DEPLOYMENT_CLOUD.md](DEPLOYMENT_CLOUD.md) §9). TimescaleDB note: restore into a database with
the extension at the same version, between `timescaledb_pre_restore()` and `timescaledb_post_restore()`.

### Restore (cloud)

Use the exact commands in [DEPLOYMENT_CLOUD.md](DEPLOYMENT_CLOUD.md) ("Rolling back the database"): stop
`stayconnect-ctrlapi` and `stayconnect-cloud-admin`, recreate the database, create the extension,
`timescaledb_pre_restore()`, `pg_restore`, `timescaledb_post_restore()`, check the hypertables, then start the
matching binary. Never `pg_restore --clean` a TimescaleDB database. Moving Central to a new host is
`central-install.sh --mode restore` from an export bundle (same document, §6).

Post-restore checks: `readyz`; `GET /cloud/v1/licenses` lists the licences; an
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
> The telemetry link was built, verified (87 000 records delivered, both sides
> reconciled) and switched off by decision on 2026-09-13; the appliance side was
> removed on 2026-09-27 (appliance migration 0093). There is no telemetry
> anywhere to restore. See `current_state_facts.central_scope` in
> `governance/project-state.json`.

**Key property of the architecture: a cloud restore never interrupts sites.**
Appliances keep serving clients on their persisted licenses throughout
([OFFLINE_OPERATION.md](OFFLINE_OPERATION.md)).

## 3. Restore drills

Run quarterly, and once as part of pilot acceptance:

| Drill | Steps | Pass criteria |
|---|---|---|
| Site restore | Hotel Admin → Backups → *Verify* a recent backup (it is loaded into a throwaway database), spot-check a voucher and a session; `scripts/ledger-restore-drill.sh` for the full restore, including the financial guarantees | verified; counts plausible; every drill scenario passes |
| Full appliance rebuild | fresh VM → deploy stack → restore the latest backup + its configuration archive → run phase 1 suite | client login green without touching the cloud |
| Cloud restore | restore cloud dump to scratch; issue a test license against it | envelope signs & verifies |
| Vendor-key escrow check | decrypt the escrowed key, `LoadSigner` succeeds, key_id matches production | key_id equality |
| Outage replay | combine with the cloud-outage drill: restore cloud from a dump taken *before* an edge outage window; appliances keep serving clients throughout, and their next licence fetch and assignment poll succeed against the restored Central | clients unaffected; appliances `Connected` again |

Record every drill in the cloud audit log (`backup.drill` action).

### After a restore: the erasure ledger (migration 0148)

A restore brings back exactly what the backup held, so a guest erased after the backup would be back, and a request answered after it
would be open again. The **erasure ledger** prevents that ([privacy/ERASURE_LEDGER.md](privacy/ERASURE_LEDGER.md)): every erasure and
every closed request is recorded in the database, in a file on the appliance (`/var/lib/stayconnect/ledger/erasure-ledger.jsonl`) and
in the secondary storage the hotel set up, and the restore reapplies it **to the staging database before the swap**.

What the restore does by itself, each step shown on the restore result:

1. **ledger_prepare** -- the live database's own ledger entries are made durable in the file first. A restore that cannot do this does
   not start.
2. **ledger_storage** -- the secondary-storage setting of *this appliance* is carried across the swap.
3. **ledger** -- the copies are read and verified; **one verified, complete copy is enough** (the other need not be online). The ledger
   inside the backup, the live database and the ledger head recorded in the note of every backup on the appliance are the witnesses.
   If no copy can be trusted, the copies contradict each other, a copy was cut short, or the backup is older than the ledger while
   erasures exist, **the restore stops with nothing replaced** and says why. An encrypted secondary copy needs a backup key holder's key
   (the same key as the backup, pasted into the restore dialog).
4. The entries after the backup are applied to the staging data: guests are erased again, answered requests are closed again, the erasure
   reference the guest was given exists again with its counts.
5. After the swap, **ledger_synced** -- the missing copy is rebuilt and the two copies are brought into step. If the secondary storage
   is unreachable the restore still succeeds and the step says so; it is retried hourly.

**Check after any restore:** Backups → Erasure ledger shows *Both copies are in step*; the restore result lists `ledger` and
`ledger_synced`. **Replacement appliance:** [runbooks/ERASURE_LEDGER_RECOVERY.md](runbooks/ERASURE_LEDGER_RECOVERY.md).

**What stays open after a restore:** a request that was still *open* at the backup comes back open (the ledger records answers, not the
requests, because they hold who asked); close it again. Assisted requests expire after 30 days anyway.

**The database set aside.** The database a restore replaces is kept so the restore can be undone. It still holds data erased since, so
`scd` removes it after `SET_ASIDE_DAYS` (default 14; Hotel Admin → Backups → Retention). Undo a restore within that time or not at all.
Interrupted writes leave `*.partial` files, removed after a day.

**Drill (quarterly, and after any change to restore or the ledger):** `scripts/ledger-restore-drill.sh` runs the real restore against a
disposable database: both copies, the secondary unplugged, the secondary alone, forked copies, nothing trustworthy, an interrupted write,
a copy cut short, and a backup older than the ledger. On the appliance itself, erase a test guest, restore a backup taken before it, and
confirm the guest is anonymised again after the restore result reports `ledger` OK.
