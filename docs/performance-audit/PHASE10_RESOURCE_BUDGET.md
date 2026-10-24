# Phase 10: Appliance resource budget

**Audited:** master `653363d7`, fetched at the start of the phase. Code and local measurements only: nothing was run against PRE-LIVE or Central.

**Covers:** every appliance process together, on the reference appliance (2 vCPU, 4 GB, 40 GB disk, Phase 0 §2):
- memory per process, and what limits it;
- CPU, threads, open files;
- log volume and how long logs are really kept;
- disk growth, including WAL and backups;
- startup and crash recovery;
- backup and restore time, and what a backup costs the guest paths while it runs.

It also takes the items earlier phases handed on (see [Items from earlier phases](#items-from-earlier-phases)).

**How:**
- **Code:** the systemd units in `deploy/systemd` and `deploy/caddy`, `deploy/compose/infra.yml`, the journald, tmpfiles and sysctl files, the install scripts, and scd's backup, restore and log-retention code.
- **Database:** a throwaway full-schema database (`scripts/lib-fullschema-testdb.sh`, migrations to 0150), limited to 2 CPUs and 4 GB. It used the settings Phase 1 recommends: `shared_buffers` 1 GB, `effective_cache_size` 2 GB, `maintenance_work_mem` 256 MB, `autovacuum_work_mem` 128 MB, `work_mem` 8 MB.
- **Generated data only:**
  - 5.8 million accounting rows over a year;
  - 732,000 sessions;
  - 400,000 devices with locally administered MAC addresses (`02:…`) and `10.10.x.x` addresses;
  - 1 million audit rows;
  - 370,000 PMS events with placeholder payloads.

  The database is 2.3 GB, close to Phase 5's 2.9 GB one-year database.
- **Backups:** the steps of scd's `takeBackup` were run inside the database container, which shares its 2 CPUs the way scd shares the appliance's: `pg_dump`, then gzip, then a load of the same SQL into a scratch database on the same server. A fixed load of 50 transactions a second ran alongside, to see what guests and staff would feel. Each transaction is a device lookup, a device update and an accounting insert.
- **Services:** scd, portald, edged, acctd and pmsd were built for Linux and started against that database, in a container limited to 2 CPUs and 4 GB, for 15 minutes.
  - scd had no assignment, so it ran in its "awaiting assignment" mode with its tenant loops off.
  - netd was not run: it needs the kernel's network stack.
  - Memory, threads and open files were read from `/proc`.
- **Reproductions:** a short Go program copying edged's health-check client measured what each call leaves behind. journald 252 (Debian bookworm), in a container, measured the bytes one request log line costs.
- **Caveat:** the workstation's disk and kernel are not the appliance's. Times are for comparison. Phase 11 repeats the key ones on a reference-size VM.
- **Compliance:** checked against the [README](README.md#compliance) rules.

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 1 | 4 | 3 | 0 |

**Main result.** Each service is small when idle: 8–22 MB resident, 7–8 threads, 6–12 open files. Postgres's private memory is about 2 MB per active connection. The budget is broken by five things:

1. **edged's health loop leaks a connection on every check (D10-H1).** Every 10 s it builds a new HTTP client for scd's and netd's sockets and never closes it. Each call leaves 3 goroutines, 2 open files and about 43 KB behind, across edged and the service it called. On an appliance, edged makes 4 such calls every 10 s, which is **about 1.5 GB a day**. A 4 GB appliance runs out of memory in a day or two unless a service restarts.
2. **A verified backup loads a whole second copy of the database into the live Postgres (D10-M1).** For the 2.3 GB database that wrote 2.0 GB of WAL, held both CPUs at 100% for about a minute, and stalled the fixed load twice for 10–15 s each. A plain dump took 37 s, and the worst delay during it was 1.2 s.
3. **System logs are bounded by size, not by the hotel's log period (D10-M2).** Every HTTP request writes a journal entry of about 670 bytes. journald's default cap on a 40 GB disk is 4 GB, which holds 14 days only at about 5 entries a second across all services. The DHCP log rotates by Kea's own default of about 20 MB. A hotel whose law needs a year of connection logs gets days.
4. **Nothing on the appliance has a memory limit or a kill order, except hotel-admin (D10-M3).** With the fixes from Phases 1, 2 and this one, design load fits 4 GB with about a quarter left. Without them, it does not.
5. **The disk fits today, but not once accounting is fixed and backups are on (D10-M4).** About 21 GB in normal running and 26 GB during a restore at today's accounting rate. 29 GB, and 41 GB during a restore, once Phase 1's accounting fix lands at 10% of devices moving.

## Measured

### Each service, idle

On 2 CPUs and 4 GB, against the database above. "Peak after start" is `VmHWM`; "settled" is resident memory after five minutes.

| Service | Peak after start | Settled | Of which anonymous | Threads | Open files | CPU over 14 min |
|---|---|---|---|---|---|---|
| scd (awaiting assignment) | 150 MB | 21 MB | 6 MB | 7 | 12, then growing (D10-H1) | 0.42 s |
| edged | 147 MB | 21–23 MB | 9–11 MB | 7–8 | 10, then growing (D10-H1) | 1.85 s |
| portald | 107 MB | 18 MB | 8.6 MB | 7 | 9 | 0.11 s |
| acctd (paused, no assignment) | 74 MB | 8.6 MB | 2.3 MB | 6–8 | 6 | 0.58 s |
| pmsd (PMS flags off) | exited cleanly by design | | | | | |
| Redis (`redis:7-alpine`, empty) | | 8.6 MB | | | | |

- **Idle CPU is negligible:** 0.2% of one CPU at most.
- **The start peak is one Argon2 hash** (64 MiB) that every binary runs at start (D10-L1). Go returns it to the system within about five minutes.
- **Log lines:** idle, scd wrote about 0.2 lines a second, mostly the request line for the health checks other services make to it.

### edged's health-check client

A copy of `unixGet` (`cmd/edged/health_checks.go:17-34`) against a server built like scd's and netd's (`ReadHeaderTimeout` only):

| Calls | Goroutines left | Open files left | Memory left |
|---|---|---|---|
| 1,000 (one process) | 3,000 | 2,000 | 44.1 MB |
| 5,000 (one process) | 15,000 | 10,000 | 214.8 MB |
| 3,000, client only | 6,000 | 3,000 | 73.8 MB (24.6 KB a call) |

The rest, about 18 KB a call, stays in the called service. The real edged, started against the real scd, gained one open file every 10 s on each side: from 10 and 12 to 95 and 96 in 14 minutes.

### Backup, restore and recovery

Database 2.3 GB on 2 CPUs, the fixed load running (50 transactions a second).

| Run | Time | WAL written | Also |
|---|---|---|---|
| Verified backup as scd takes it (dump, gzip, load into scratch) | 128 s and 178 s | 1.97–1.99 GB | scratch database 2.1 GB until dropped; backup file 398 MB |
| Dump and gzip only | 37 s | none | |
| Restore load (gunzip into a new database, no other load) | 103 s and 115 s | | restored database 2.1 GB |
| Crash recovery: Postgres killed during a load, 684 MB of WAL since the last checkpoint | ready in 8.5 s | | redo took 1.65 s |

**The fixed load, per 10 s:**

| Phase | p50 | p95 | Worst |
|---|---|---|---|
| Before the backup | 4–5 ms | 6–8 ms | 96 ms |
| During the verified backup, outside the stalls | 7–26 ms | 33–529 ms | 759 ms |
| **During the two stalls (about 30 s each)** | **3.1–10.5 s** | **6.1–14.1 s** | **14.9 s** |
| After the backup | 5 ms | 8–19 ms | 142 ms |
| During dump and gzip only (whole window) | 5 ms | 102 ms | 1.2 s |

**What caused the stalls.** The scratch load wrote WAL faster than `max_wal_size` (1 GB) allowed. Postgres ran two checkpoints back to back, of 39 s and 45 s, each writing 31% of the shared buffers. Both CPUs read 200% for about a minute.

### Postgres memory

| State | Anonymous | Shared |
|---|---|---|
| Idle | 10 MB | 1.07 GB (shared buffers) |
| 26 active connections | 60 MB, so about 2 MB each | same |
| Peak while loading a dump | 47 MB | same |

The page cache filled the container's 4 GB during loads, which is expected.

### Journal

journald 252 stored 100,000 lines of portald's request format (190 bytes of JSON each) in 67 MB, which is **about 670 bytes an entry**. One request a second, sustained, is 58 MB a day.

## Findings

### D10-H1: edged's health loop leaks a connection to scd and netd on every check (High, Measured)

**Where.**
- **The client:** `cmd/edged/health_checks.go:17-34`. `unixGet` builds a new `http.Client` with a new `http.Transport` for every call, and never closes its idle connections.
- **The calls:** `checkSCD` (`:52`), `checkNetd` (`:72`), `keaFromNetd` (`:186`) and `checkKea` (`:218`).
- **How often:** the health monitor runs them every 10 s (`health.go:144`).
- **The servers:** scd (`cmd/scd/main.go:975`) and netd (`cmd/netd/main.go:234-236`) set `ReadHeaderTimeout` only. With no `IdleTimeout` and no `ReadTimeout`, an idle keep-alive connection is never closed.

**Problem.**
- After each response, the connection goes back to the throwaway transport's idle pool.
- The transport's read and write goroutines keep it reachable, so it is never collected.
- The server keeps its end open too, because nothing times out an idle connection.
- Every check therefore adds one connection that lives until a service restarts.

**Measured.** See [edged's health-check client](#edgeds-health-check-client): 2 goroutines, 1 file and 24.6 KB stay in edged; 1 goroutine, 1 file and about 18 KB stay in the called service.

**Impact** (rate Confirmed; memory per call Measured).

| | Calls a day | Memory a day | Open files a day |
|---|---|---|---|
| edged | 34,560 (1 to scd and 3 to netd every 10 s) | about 850 MB | 34,560 |
| netd | 25,920 | about 480 MB | 25,920 |
| scd | 8,640 | about 160 MB | 8,640 |
| **Together** | | **about 1.5 GB** | |

- **Memory runs out in a day or two.** On the reference appliance the services' share of memory is gone within one to two days of uptime. The kernel's out-of-memory killer then picks a process, and nothing tells it which (D10-M3). netd, which carries enforcement, is among the candidates.
- **Open files.** Go raises its soft limit to the hard limit (524,288 by systemd's default), so the file limit is reached after about 15 days, later than memory.
- **Why it has not shown.** PRE-LIVE restarts its services at every deployment, and each restart clears the leak.

**Recommended fix.**
1. **One client per socket,** built once when edged starts and reused by every check, like `newNetdClient` (`netd_client.go:18`) already is.
2. **Read the body to the end** before closing it, so the connection is reused.
3. **Set `IdleTimeout`** (for example 60 s) and `ReadTimeout` on scd's and netd's socket servers, so a client that misbehaves cannot hold connections forever. This is backend audit pattern P1, the shared server helper (Phase 0 B0-2).

**Proof.**
- A test calls the health checks 1,000 times and shows the goroutine count back at its start value.
- On a test appliance, edged's, scd's and netd's open-file counts stay flat over an hour (`ls /proc/<pid>/fd | wc -l`).

**Batch:** B1. **Do first.** FAST.

### D10-M1: A verified backup loads a second copy of the database into the live Postgres (Medium, Measured)

**Where.** `cmd/scd/backups.go:121-255`, `takeBackup`.
- It streams `pg_dump` through gzip and encryption into the backup file.
- At the same time it loads the same SQL into a scratch database `verify_<stamp>`, on the same Postgres server that serves the appliance (`:155-188`).
- This applies to every kind of backup:
  - scheduled (off by default, 02:00 when on);
  - manual, from Hotel Admin, at any time of day;
  - pre-change;
  - the safety backup that starts every restore.

**Measured** (2.3 GB database, see [Backup, restore and recovery](#backup-restore-and-recovery)):
- **Time:** 128–178 s, against 37 s for the dump alone.
- **WAL:** 2.0 GB, about as much as the database itself.
- **CPU:** both CPUs at 100% for about a minute.
- **Stalls:** two of about 30 s each, during which the fixed load's p95 was 6–14 s.
- **Disk:** the scratch database is a full copy, 2.1 GB, until it is dropped.

**Problem.**
- **The load writes as much WAL as the database is large.** It forces checkpoints back to back. Every other write on the appliance waits behind them.
- **The work shares the guest path's server.** It uses the same CPUs, the same WAL and the same shared buffers as guest sign-in, accounting and the Admin Console.
- **It grows with the data.** On a 6 GB database (D10-M4) it is about three times as long and writes about 6 GB of WAL.

**Impact.**
- **During a daytime backup, a sign-in can wait 10–15 s** for the database. That is far beyond the 2 s goal. The same applies to accounting writes and Admin Console pages.
- **Before every restore,** the safety backup runs while the appliance is still serving.

**Recommended fix.**
1. **Keep the proof, move where it runs.** Load the SQL into a separate, disposable Postgres container started for the check.
   - Give it its own small memory limit, lower CPU and I/O weight, and `fsync=off`: it is thrown away.
   - Its data directory goes on the same disk but outside the live cluster, so its WAL and checkpoints never touch the live server's.
   - Remove the container and its volume when the check ends, pass or fail.
2. **Stream with back-pressure.** If the check runs behind, the dump waits rather than buffering.
3. **Until then,** say in Hotel Admin, next to the manual backup button, that a backup slows the appliance for a few minutes. Suggest a quiet time.

**Proof.** With the fixed load running, a verified backup of the 2.3 GB database leaves the load's p95 under 50 ms and its worst under 1 s. It writes no WAL on the live server beyond the dump's own. The backup is still proven by a load that ends in the same table count and ledger check.

**Batch:** B2. CONTROLLED (appliance deployment: a second database container).

### D10-M2: System logs are kept by size, not by the hotel's log period (Medium, Confirmed; volume partly Suspected)

**Where.**
- **The promise:** `docs/privacy/DPA_ANNEX_II_SECURITY_MEASURES.md` §9. The web access log, the address-assignment log and the service journal are deleted after the hotel's log period, 14 days by default and 1–730 days by setting. The hotel sets it longer "only if its national law requires connection logs to be kept".
- **What enforces it:** scd's hourly job (`cmd/scd/log_retention.go`) deletes what is older than the period. Nothing makes sure the logs last that long.
- **The journal:** `deploy/journald/stayconnect-retention.conf` sets only `MaxFileSec=1day`. journald's own defaults therefore apply: `SystemMaxUse` 10% of the file system, at most 4 GB, and `SystemKeepFree` 15%.
- **Request logging:** `internal/reqlog/reqlog.go:24` writes one journal line for every HTTP request scd, portald and edged answer. That includes captive-portal probes, health checks and the Admin Console's polling.
- **The DHCP log:** `deploy/kea/kea-dhcp4.conf:69-75` and `internal/netcfg/render_kea.go:123-126` set no `maxsize` or `maxver`. Kea's defaults apply: it rotates at about 10 MB and keeps one old file.

**Measured.** About 670 bytes of journal per request line. Idle, scd alone wrote about 0.2 lines a second.

**Problem.** The oldest log kept is decided by how fast logs are written, not by the setting.

| Log period | Average journal entries a second that fit in 4 GB |
|---|---|
| 14 days (default) | 4.9 |
| 30 days | 2.3 |
| 365 days | 0.19 |

- **The journal fills faster than the period at design load.** Ten consoles polling the dashboard alone add about one request a second (Phase 8). Each portal page adds a portald line and scd lines. Unsigned devices' traffic adds more (Phase 3 D3-M1). A busy day therefore uses the 4 GB in days, and journald then deletes the oldest entries, whatever the period says.
- **A year is already exceeded idle.** scd's idle health-check lines alone (0.2 a second) are more than a 365-day period can hold.
- **The DHCP log keeps about a day** (Suspected: about 17 MB a day at 2,000 devices with 1-hour leases, renewing every 30 minutes).
- **A full disk shrinks the journal further.** `SystemKeepFree` makes journald give space back as the disk fills (D10-M4).

**Impact.**
- **Compliance.** A hotel that lengthens the period because its law requires connection logs does not get them. The DPA annex says it does. The shortfall is silent: nothing shows the date of the oldest log kept.
- **Disk writes.** Request lines are most of the journal's writes, and they carry only a route, a status and a time.

**Recommended fix.**
1. **Log what is worth keeping.** Keep the request line for errors (status 500 and above, and refusals that matter for security), slow requests (over 1 s) and requests that change something. Count the rest per route in the request histogram (Phase 0 B0-1) instead of logging each one.
2. **Size the journal on purpose.** Set `SystemMaxUse` and `SystemKeepFree` in the journald drop-in from the disk budget (D10-M4).
3. **Make the DHCP log follow the period.** Set Kea's `maxsize` to 0 so scd's daily cut is the only rotation, or size `maxsize` and `maxver` from the period.
4. **Show it.** Hotel Admin shows the date of the oldest journal entry and DHCP log next to the log period. It warns when either is younger than the period.

**Proof.**
- With design-load traffic generated for a day, the journal grows by less than 50 MB.
- With a 365-day period, the projected journal use fits the configured `SystemMaxUse`.
- The DHCP log keeps every day since it started.

**Batch:** B3. FAST for steps 1, 2 and 4. Step 3 changes netd's rendered Kea configuration: CONTROLLED (networking).

### D10-M3: No appliance service has a memory limit or a kill order, except hotel-admin (Medium, Confirmed)

**Where.**
- `deploy/systemd/stayconnect-{scd,portald,edged,netd,acctd,pmsd}.service` and `deploy/caddy/stayconnect-caddy.edge.service` set none of `MemoryHigh`, `MemoryMax` or `OOMScoreAdjust`, and no `GOMEMLIMIT`.
- Postgres and Redis (`deploy/compose/infra.yml`) have no memory limit.
- Only hotel-admin has `MemoryMax=768M` and `MemoryHigh=640M`.

This finding carries Phase 1 D1-M2 steps 2–3 and Phase 2 D2-H1 step 4 into one budget, so they are fixed together. It does not count them again.

**The budget at design load,** on 4 GB:

| Item | With the fixes | Today, worst case | Source |
|---|---|---|---|
| Kernel, systemd, journald, sshd, Docker | 350 MB | 350 MB | allowance; measure on the reference VM (Phase 11) |
| Postgres shared buffers | 1 GB | 1.25 GB (auto-tuned) | Phase 1 D1-M2 |
| Postgres connections (about 30) | 60–300 MB | 60–300 MB | 2 MB each idle, up to `work_mem` per sort (measured) |
| Postgres autovacuum | 384 MB (3 × 128 MB) | up to 5 GB (10 × 512 MB) | Phase 1 D1-M2 |
| scd | 520 MB (4 hashes at once) | 3.7 GB at 30 hashes at once | Phase 2 D2-H1 |
| portald, edged, acctd, netd, pmsd | 150 MB | 150 MB, plus D10-H1's 1.5 GB a day | measured idle, with an allowance for load |
| hotel-admin (Node) | 290 MB | 290 MB, capped at 768 MB | Phase 8 |
| Caddy, Kea, Unbound | 100 MB | 100 MB | allowance |
| Redis | 0 (removed, D10-L2) | 9 MB | measured |
| Connection tracking, full table | 80 MB | 80 MB | `nf_conntrack_max` 262,144 |
| **Total** | **about 2.9–3.2 GB** | **over 4 GB** | |
| **Left for the page cache** | **0.9–1.2 GB (22–28%)** | none | target: 15% headroom |

**Problem.**
- **Without limits, one process can take everything.** A burst of hashes, a long autovacuum or D10-H1's leak can use all the memory.
- **No kill order.** The kernel's out-of-memory killer then chooses by size. Postgres's shared memory and scd's hash memory make them the likeliest victims. Those are the two the guest path cannot lose.
- **The start peak is added at boot.** All six Go services hold 74–150 MB each for their first five minutes (D10-L1), at the same time as Postgres's recovery and Node's start.

**Impact.**
- **With the fixes,** design load fits with 22–28% left. That meets the 15% goal, but leaves at most 1.2 GB of page cache for a database of several GB, so cold reads go to disk.
- **Without them,** the appliance has no headroom.
- **At stress load** (5,000 devices, 1,000 sign-ins a minute) the margin is gone either way.

**Recommended fix.**
1. **Give each service a limit.** Set `MemoryHigh` and `MemoryMax` in each unit, from the "with the fixes" column with about 50% margin. Set `GOMEMLIMIT` for each Go service a little under its `MemoryHigh`.
2. **Set the kill order.** Use `OOMScoreAdjust`: Postgres, netd and scd last; hotel-admin, edged and pmsd first.
3. **Limit the Postgres container** (`mem_limit` in `infra.yml`) to shared buffers plus connections plus autovacuum plus a margin, with Phase 1's settings.
4. **Measure the budget for real.** Repeat it on the reference VM under the design load (Phase 11), and write the limits from that run.

**Proof.** The design-load run on the reference VM keeps total memory under 3.5 GB, uses no swap, and has no out-of-memory kill. A test that forces an out-of-memory condition kills hotel-admin or edged first.

**Batch:** B4, with Phase 1 B4 and Phase 2 B1. CONTROLLED (appliance deployment).

### D10-M4: The disk fits today, but not once accounting is fixed and backups are on (Medium, Measured parts)

**Where.**
- **Backups:** `/opt/stayconnect/backups/db`, on the appliance's only disk. `deploy/scripts/backup-retention.conf` keeps 7 regular and 3 protective backups.
- **Transient copies:** the verify scratch database during every backup (D10-M1); the staging database during a restore.
- **The set-aside database:** after a restore it is kept for `SET_ASIDE_DAYS` (14 by default, `docs/BACKUP_AND_RESTORE.md`).
- **The rest:** WAL up to `max_wal_size`; the journal up to 4 GB (D10-M2); 5 binary sets and 5 hotel-admin releases.

**The budget on 40 GB.** Backup size is 17–19% of the database, measured here and in Phase 5.

| Item | Today's accounting rate, 1 year | Accounting fixed, 10% of devices moving | Same, 30% moving |
|---|---|---|---|
| Operating system, packages, Docker images | 6 GB | 6 GB | 6 GB |
| Binaries (5 sets) and hotel-admin releases (5 × 64 MB) | 0.9 GB | 0.9 GB | 0.9 GB |
| Database (accounting kept 1 year) | 2.3–2.9 GB | about 6 GB | about 15 GB |
| WAL (`max_wal_size` 1 GB) | 1 GB | 1 GB | 1 GB |
| Backups, 10 kept (when switched on) | 4.0–5.5 GB | about 11 GB | about 28 GB |
| Journal (default cap) | 4 GB | 4 GB | 4 GB |
| Container logs, Kea, Caddy | 0.3 GB | 0.3 GB | 0.3 GB |
| **Normal running** | **about 19–21 GB (48–52%)** | **about 29 GB (73%)** | **about 55 GB: does not fit** |
| During a backup (+1 × database) | about 21–24 GB | about 35 GB (88%) | |
| During a restore (+2 × database: staging, and the safety backup's scratch) | about 23–26 GB | **about 41 GB: does not fit** | |
| For 14 days after a restore (+1 × database set aside) | about 21–24 GB | about 35 GB | |

The accounting rates are Phase 1 D1-H3's. `stay_events` also grows without limit: it is never deleted (Phase 1 D1-L5), and every PMS resync adds a whole roster (Phase 6).

**Problem.**
- **Backups scale with the database.** They are kept on the same disk, and their count does not depend on size.
- **Every backup and restore makes a temporary full copy** of the database.
- **The appliance warns too late.** It warns at 80% and 90% (`DISK_WARN`, `DISK_CRIT`), but D10-M1's scratch copy pushes past 80% for the length of each backup once accounting is fixed.

**Impact.**
- **At today's accounting rate** the 40 GB disk is enough.
- **Once Phase 1's accounting fix lands,** a restore cannot complete on 40 GB at 10% of devices moving. Normal running does not fit at 30%. A disk that fills stops Postgres, and guest sign-in with it (Phase 1 D1-H3).

**Recommended fix.**
1. **Size backups by bytes as well as by count.** Keep at most N backups, and never more than a share of the disk. The share is a setting (CLAUDE.md §0C).
2. **Check the space before a backup or restore starts.** Need 1 × the database for a backup, or 2 × for a restore, plus the backup itself. Refuse with a clear message rather than filling the disk.
3. **Off-box copies.** Let the administrator download the encrypted backup (it exists today), and say on the Backups screen that copies kept on the appliance do not survive a disk loss.
4. **The reference disk** (Decision D10-D1).

**Proof.** A restore on a disk with too little space is refused before maintenance starts. On the reference VM with one year of fixed-rate accounting, normal running stays under 70% of the disk.

**Batch:** B5. FAST for steps 1–3.

### D10-L1: Every service runs a 64 MiB Argon2 hash at start (Low, Measured)

**Where.**
- `internal/passwordhash/passwordhash.go:80-85`: `var dummy = func() string { … argon2.IDKey(…) }()`.
- `internal/poststay/poststay.go:94-102`: `var dummyHash = mustDummy()`.
- Both are package-level variables. They run when a binary that imports the package starts.
- All six appliance binaries import `passwordhash`; scd and edged also import `poststay`. netd, acctd and pmsd never check a password.

**Measured.** Peak memory after start of 74–150 MB per service (see [Each service, idle](#each-service-idle)), returned after about five minutes. About 28 ms of CPU per hash (Phase 2).

**Problem.** The value is a constant: a fixed salt and a fixed password give the same hash every time. Computing it at start buys nothing.

**Impact.**
- **At boot,** about 620 MB is held at once, while Postgres and Node also start.
- **A service that restarts in a loop** pays it on every attempt.

**Recommended fix.** Write the two hashes as string constants. A unit test checks that each constant equals what the hash function produces with the same inputs and parameters. The constant-work check against unknown accounts keeps running the full hash on verify, so its timing does not change (ASVS V2.2).

**Proof.** The start peak of acctd drops under 20 MB, and the existing timing-equality tests still pass.

**Batch:** B1. FAST.

### D10-L2: The appliance runs a Redis that nothing uses (Low, Confirmed)

**Where.** `deploy/compose/infra.yml:42-51` starts `redis:7-alpine` on `127.0.0.1:6379`, saving a snapshot every 60 s after any write. No appliance service, script or web app connects to it: the only reference is an old development bootstrap for Central (`scripts/phase3-bootstrap.sh:15`).

**Impact.**
- **Resources:** 8.6 MB of memory and a container log of up to 250 MB.
- **Upkeep:** an image to patch and a local port that someone could later start using without review.

**Recommended fix.**
1. Remove the service and its volume from the appliance's compose file.
2. Check the install and health scripts (`check-appliance-units.sh`, `provision-fresh-appliance.sh`) for any expectation that it runs.
3. On existing appliances, stop it and remove it at the next deployment.

**Batch:** B4. CONTROLLED (appliance deployment).

### D10-L3: A manual backup gives up after 20 minutes, and that limit comes into reach as the database grows (Low, Measured)

**Where.**
- **Manual and pre-change backups:** `cmd/scd/backup.go:97` gives them 20 minutes.
- **Scheduled backups:** `backups.go:589` gives them 2 hours.
- **The restore:** its own budget is 45 minutes before the retention step (`restore_retention.go:22`), and its safety backup is part of that.

**Measured.** The verified backup took 55–77 s per GB of database with the fixed load running.

**Impact.**
- **Manual backups:** at about 15–20 GB of database (D10-M4's 30% column), they start failing at the deadline. That is the case most likely to need one, before a change.
- **Restores:** the 45-minute budget is reached at about 25–30 GB.

**Recommended fix.** Size the deadlines from the database size: for example 2 minutes per GB, with a floor. Show the estimated time before starting. D10-M1 shortens the backup itself.

**Batch:** B2. FAST.

## Measured clean

| Area | Result |
|---|---|
| Startup | Each service listened within a second of starting. Postgres was ready 8.5 s after being killed mid-load, with 684 MB of WAL to replay. `wait-for-site-db.sh` waits 60 s. |
| Restart behaviour | Every unit restarts on failure with no start limit (`StartLimitIntervalSec=0`). scd's adaptive backoff (`startupbackoff.Guard`) stops a crash loop from becoming a storm. |
| Threads and open files | 6–8 threads and 6–12 files per Go service when idle, without D10-H1. Go raises its own file limit to the hard limit; Caddy sets 1,048,576. |
| Idle CPU | 0.2% of one CPU at most per service (scd in its awaiting mode). |
| Postgres private memory | About 2 MB per connection; 47 MB at most while loading a dump. The memory that matters is the shared buffers and autovacuum, which Phase 1 sizes. |
| Restore time | 103–115 s to load the 2.3 GB database (45–50 s per GB). The appliance stops serving only for the load and the checks, not for the safety backup. |
| Set-aside database | Removed after `SET_ASIDE_DAYS` (14). It is counted in D10-M4's disk budget, and its expiry already answers the erasure question (`docs/BACKUP_AND_RESTORE.md`). |
| Container logs | Postgres's Docker log is capped at 30 MB, the others at 250 MB (`infra.yml`). |
| Connection tracking | `nf_conntrack_max` 262,144 is about 80 MB of kernel memory when full, which is room for many times the design load's flows. |

## Items from earlier phases

| Item | Outcome |
|---|---|
| Phase 0 H3, D2: is 4 GB enough? | With D10-H1, D2-H1, D1-M2 and D10-M3 fixed, design load fits with 22–28% left. Without them it does not. Decision D10-D2. |
| Phase 1: memory for D1-M2; disk for D1-H3, WAL and backups | The memory budget is D10-M3; the disk budget is D10-M4. The WAL a backup writes is D10-M1. |
| Phase 2: scd's memory limit and `GOMEMLIMIT` | In D10-M3, at 520 MB with D2-H1's limiter of 4 hashes at once. |
| Phase 3: portald at the D3-M1 load | 18 MB idle. Its load cost is CPU, not memory: 35–45% of the CPUs at 100 requests a second until D3-M1 is fixed. Its request lines are part of D10-M2. |
| Phase 4: CPU of process starts | Phase 4's batched `tc` and `nft` changes are the fix. edged's health loop also starts one `systemctl show` per service every 10 s (Phase 7 D7-L2). |
| Phase 5: 84 GB of sweep WAL a day at a 1 s tick (D5-M1); backups of 551 MB taking 92 s (D5-M2) | The backup's own cost is larger than its file: D10-M1 adds a full load and as much WAL as the database. D5-M2's re-reading of stored backups is unchanged. D5-M1's WAL does not fill the disk (it is recycled within `max_wal_size`), but it is most of the disk's writes. |
| Phase 6: `stay_events` growth | Never deleted, one roster per resync. It is in D10-M4's database line. The measurement for the Phase 1 B3 retention batch: rows added per resync at 600 and 1,500 stays. |
| Phase 7: edged's CPU and connection share | Idle edged uses 0.2% of a CPU. Its health loop is D10-H1 and D7-L2. Its pool of 4 is Phase 1 D1-M3. |
| Phase 8: Node memory and the 64 MB package | 290 MB after load in D10-M3, under its 768 MB cap. 5 releases of 64 MB are in D10-M4. |
| Phase 9: Central's disk | The fetch log is 3.4 GB at 200 appliances and about 17 GB at 1,000. Central keeps 7 daily compressed dumps (`central-backup.sh`, `KEEP_DB=7`), so it carries 7 compressed copies of the log too. D9-M1's fix removes both, and its proof should include one dump's size before and after. |

## Handed on

| To | What |
|---|---|
| Phase 11 | The memory budget measured on the reference VM at the design load, with scd assigned (its tenant loops on) and netd running. The backup stall repeated on the reference VM's disk. A day of design-load traffic to measure the journal's real growth (D10-M2). The DHCP log's volume at 2,000 devices. |

## Batches

| Batch | Findings | Notes |
|---|---|---|
| **B1** | D10-H1, D10-L1 | edged's health client, server idle timeouts, the constant hashes. **Do first.** FAST. |
| **B2** | D10-M1, D10-L3 | The backup's check moves to a disposable container; deadlines sized from the database. CONTROLLED (deployment). |
| **B3** | D10-M2 | Request logging, the journald size, Kea's rotation, the oldest-log date in Hotel Admin. FAST, except Kea's configuration (CONTROLLED, networking). |
| **B4** | D10-M3, D10-L2 | Unit limits, kill order, the Postgres container limit, Redis removed. With Phase 1 B4 and Phase 2 B1. CONTROLLED (deployment). |
| **B5** | D10-M4 | Backup size limit, the space check, the off-box message. FAST. The disk size is Decision D10-D1. |

## Decisions for the Product Owner

| # | Decision | Recommended |
|---|---|---|
| D10-D1 | What disk should the reference appliance have once Phase 1's accounting fix lands? | 80 GB. 40 GB stays enough only if accounting detail is rolled up (Phase 1's tick decision) and at most 5 backups are kept. |
| D10-D2 (answers Phase 0 D2) | Is 4 GB still the minimum? | Yes for the design load, after B1 and B4 and Phase 2 B1. 8 GB for the stress load (5,000 devices), or for sites that use the Admin Console exports heavily. Confirm with Phase 11's reference-VM run. |

## Compliance

| Finding | Rules | What the fix must respect |
|---|---|---|
| D10-H1 | GDPR Art. 32(1)(b), resilience | No change to what is checked or logged. Only how the connection is held. |
| D10-M1 | GDPR Art. 32(1)(c), (d): the ability to restore, and regular testing | Every backup stays proven by a real load before it counts. The check moves; it is not dropped or sampled. The disposable container holds personal data while it runs: it is on the appliance only, it is removed with its volume when the check ends, pass or fail, and it is never backed up itself. |
| D10-M2 | National connection-log laws; DPA Annex II §9; GDPR Art. 5(1)(c), (e); ISO/IEC 27001 A.8.15 | Logs last the hotel's period, no shorter and no longer. Dropping per-request route lines removes no personal data, because they hold none (`reqlog`). Security events and errors stay logged. The DHCP log holds MAC and IP addresses, so its longer life must still end at the period (scd's daily cut does that). |
| D10-M3 | GDPR Art. 32(1)(b) | Limits are set so that an out-of-memory kill never takes netd, enforcement, or Postgres first. Argon2's parameters are unchanged (Phase 2). |
| D10-M4 | GDPR Art. 5(1)(e), 32(1)(c) | A size limit on backups may remove an older backup sooner than the count would. It never removes the newest verified backup, and it never removes a protective one before the restore it protects is done. The set-aside database keeps its 14-day expiry. |
| D10-L1 | OWASP ASVS V2.2 | Unknown accounts still cost a full hash on verify. Only the precomputed reference value becomes a constant. |
| Test data | README rule 7 | Generated data only: placeholder identifiers, locally administered MAC addresses, private addresses, placeholder payloads. |

## Outside this audit

- **Unix-socket servers:** scd's and netd's socket servers set `ReadHeaderTimeout` only. D10-H1 step 3 fixes the idle side. Read and write timeouts on these sockets are a robustness matter for the backend audit, together with its portald and edged findings.

## Tests run

| Test | Result |
|---|---|
| Full-schema database (migrations to 0150), 2 CPUs and 4 GB, Phase 1's settings; one year of generated history, 2.3 GB | Built |
| Fixed load, 50 transactions a second, 60 s, twice | p95 16 ms; worst 126 ms |
| scd's verified backup (dump, gzip, scratch load), three times with the fixed load running | 128–178 s; 2.0 GB of WAL; two stalls of 6–14 s p95 per run. The first run's load failed at the end on a foreign key the generated data broke; its numbers are not used. |
| Dump and gzip alone with the fixed load running | 37 s; worst 1.2 s |
| Restore load into a new database, twice | 103 s and 115 s |
| Postgres killed during a load, then started | Ready in 8.5 s |
| Postgres anonymous memory, idle, with 26 connections, and while loading | 10 MB, 60 MB, 47 MB |
| scd, portald, edged, acctd and pmsd built for Linux and run idle for 15 minutes; `/proc` read at start, at 5 minutes and every minute after | Each service, idle |
| Reproduction of `unixGet`: 1,000 and 5,000 calls in one process; 3,000 calls with client and server in separate processes | D10-H1 |
| journald 252 in a container, 100,000 request lines | 67 MB |
| Redis 7 (alpine), empty, as the appliance starts it | 8.6 MB |

**Test-only changes, made inside the throwaway database and never in the repository:**
- The three foreign keys on `sessions` and `stay_events` that the generated rows broke were dropped, so that the dump would load.

**Cleanup.**
- **Docker:** the database, service and journald containers were removed with their volumes (`docker rm -f -v`), and so was the Redis container. No audit containers remain.
- **Not mine:** during the phase the `sop` project's containers and one `nudge` container stopped and were removed, with four volumes, by something outside this audit. This audit removed only its own `perfaudit-p10*` containers.
- **Scratch:** the built binaries, the reproduction program, the scripts and the samples were deleted.
- **Images:** the TimescaleDB, Debian and Redis images were already present and were kept. The packages installed in the journald container went with it.
