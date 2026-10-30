# Sign-in capacity closure: 300 enforced sign-ins a minute for an hour on the 2 vCPU / 4 GB reference (2026-10-30)

Mission (Product Owner): at least 300 successfully **enforced** sign-ins per minute, sustained for one hour, with 1,700 long sessions already established and
60-second session churn, inside the 2 vCPU / 4 GB budget. No lower target, no longer response deadline, no bypass of enforcement, no weaker licence limit, no
pending or refused session counted as a success. A sign-in counts only if the portal answers `303` to `/success` **and** the session is `active` with its
kernel authorisation.

Branch `worktree-agent-a545bfb7990bd42d5` (not pushed). Release under test: **`2f687f2406dee6763986b3f5b59c5470d7444501`** (appliance release
`onegate-appliance-2f687f2406de.tar.gz`, built by `deploy/scripts/appliance-build.sh` from a clean clone of that commit, binaries read back as that revision),
installed on the lab appliance by `appliance-install.sh --upgrade` from release `5ac9cc5c9851` (the code the previous round ended on). Commits after
it change documentation only.

Contents: 1 result - 2 lab and method - 3 root causes (measured) - 4 changes - 5 before and after - 6 tests - 7 limits and what is not claimed.

## 1. Result

**The target was met in the lab: 17,998 of 18,000 sign-ins enforced in 60 minutes (99.99 percent), 300 offered a minute, 1,700 long sessions held throughout.** One run, on
release `2f687f2406de`, installed by `appliance-install.sh --upgrade` on the lab appliance (2 vCPU, 4 GB) from the previous round's release; 04:05:37 to 05:05:39 UTC on 2026-10-30.

| Criterion | Required | Measured |
|---|---|---|
| Enforced sign-ins, sustained 60 minutes | at least 300 a minute | 18,000 attempts in 3,601 s = 299.97 a minute offered; **17,998 enforced**. The two others were answered "could not bring your device online" (minute 58). Per ten-minute window: 100.00, 100.00, 100.00, 100.00, 100.00, 99.93 percent. Per minute the enforced count was the offered count (295 to 314, median 302: the generator's minute buckets, not failures). |
| What counts | `303` to `/success` and the session `active` with its kernel authorisation | The generator counts a sign-in only for status `303` with a `Location` of `/success` (the portal answers that only after scd has read the session as `active`, which netd writes only after the class, the nft authorisation, the origin and the proof are in force). netd logged 18,010 sessions confirmed enforced in the window = 18,000 churn sessions (17,998 counted, 2 enforced after the guest's wait ran out) + 10 data-quota sessions. Pending and refused attempts are failures in the 99.99 percent. |
| Sign-in latency (portal request to success) | no longer deadline | p50 **3.21 s**, p95 **4.73 s**, p99 **5.31 s**, max 8.07 s; 17,996 of 17,998 inside 8.0 s; ten-minute p99 between 5.05 and 5.94 s. The 8-second wait for enforcement was not changed. |
| Success rate of attempts | at least 99 percent | **99.989 percent** (previous round, same load: 0.6 percent) |
| 1,700 long sessions | never lose authorisation | The kernel authorisation set was read every 5 s (651 samples): **never below 1,700**, never an active-session count below 1,700; 1,700 active with 1,700 authorised and 0 pending at the end |
| Duplicate live sessions per device | none | 0 in all 651 samples; 0 devices with a second live session at the end |
| Licence limit never exceeded | exact | live count (active plus pending) peaked at 2,038 against 2,500. **At-limit demonstration** after the run: the licence was set to 1,990 on the lab Central, the appliance refreshed it, and the same churn ran for 4 minutes: the live count peaked at **exactly 1,990**, 102 sign-ins were refused with `LICENSE_CAPACITY_REACHED` (HTTP 403 from `/v1/sessions/activate`), 1,098 enforced; licence restored to 2,500 afterwards |
| Data quota ends the session at the right count | exact | 8 MiB data-quota vouchers, ten clients downloading 256 KB a second: all ten ended with `terminal_reason = DATA`, session bytes equal entitlement bytes, bytes billed 8,545,909 to 8,569,139 against 8,388,608 (157 to 181 KB over = the traffic of the accounting period that crossed the line, 5 s at the cut-off setting). Ten more in the 30-minute rehearsal under the same load: 0.99 to 1.04 MB over (their period carried more traffic). Three batches were scheduled in the run: the first used vouchers already spent in the rehearsal (10 attempts refused, no session), the second is the evidence above, the third did not sign in (the generator still had those clients flagged as signed in). One batch of ten is the evidence from the run itself. |
| Accounting reconciles (raw / minute / hour) | equal | Immediately after the run: raw samples, per-session and per-entitlement totals all 73,873,528,183 bytes; the per-minute and per-hour rows 73,851,679,208 (21.8 MB, 1,137 samples, the last minutes, still waiting for the roll-up). Checked again after the at-limit demonstration, once the roll-up had caught up (the totals then include that demonstration's traffic): raw, per-minute, per-hour, per-session and per-entitlement byte totals **all 76,072,981,815**; raw rows, minute samples and hour samples all 701,362. Nothing lost, nothing counted twice. |
| CPU | inside 2 vCPU | appliance cgroup: mean **78.8 percent** of the 2 CPUs, p50 78.4, p95 93.9, max 102.3 (5-second windows). By service in a 80 s window: netd 73 percent of one CPU, PostgreSQL 54, scd 5, Docker 5, portald 4, acctd 4 (152 percent of 200). Load average 1.2 to 6.8. |
| Memory | inside 4 GB | `memory.max` is 4 GiB; anonymous memory 0.56 GB, shared memory 0.69 GB, kernel 0.22 GB; the rest of the cgroup's use is page cache (3.5 GB, reclaimed 2,899 times, **0 OOM events, memory pressure 0.00**). Resident: scd 29 to 35 MB, netd 27 to 89, acctd 17 to 32, portald 29 to 112, edged 24 to 161. |
| Database | no stalls | **0 deadlocks, 0 lock timeouts, 0 FATAL, 0 ERROR-level lines in any service journal** in the run. Connections 23 to 35 (scd at most 17 of its limit 20, acctd 5, netd 7). scd's pool: 448,896 acquisitions, **24 waited, 5.2 s waited in total** (before: 2,741 s in 40 s). Idle-in-transaction at most 2, lock waiters at most 6. Commit probe (a tiny write every 250 ms): p50 3.9 ms, p99 22 ms, max 103 ms. netd passes: p50 1.97 s, p95 2.78 s, max 5.4 s. |
| Forwarding | measurable | 119,520 of 119,520 downloads by the moving 10 percent of the long sessions succeeded (31.3 GB, p50 95 ms) |

Not met or not shown, said plainly: CPU p95 is 94 percent of the budget (see 7); one hour at exactly the target is one run, not a distribution; the lab is not the reference hardware.

## 2. Lab and method

A simulated acceptance in an isolated lab, as the previous round (`docs/runbooks/CAPACITY_AND_OFFLINE_ACCEPTANCE.md`), rebuilt from `tools/oglab`: systemd containers on one
Docker Desktop host, the appliance capped at `--cpus 2 --memory 4g --cpuset-cpus`, installed by the product's installer from a release tarball, a new isolated Central
(`central.oglab.test`), a generator container with 2,400 macvlan clients (real DHCP lease, real portal sign-in, real HTTP through the appliance's forwarding, NAT, shaping
and accounting to a sink), nothing registered on a real Central or appliance, no retired host contacted.

What the lab is not: a VM on the reference hardware. The container shares the host kernel and the host's disk (Docker Desktop's virtual disk on a nearly full drive:
`fdatasync` 2.2 to 3.2 ms against a PostgreSQL WAL, measured with `pg_test_fsync`). Absolute numbers are indicative; the causes below are not lab artefacts except where marked.

Two lab facts decided what the previous round's numbers meant and are corrected here:

* The host kernel's neighbour table. See 3.1. The previous round attributed failed downloads to "the generator, above about 100 simultaneous dials". It was the kernel.
* PostgreSQL's memory settings are tuned by the image from the *host's* memory (15 GB: `shared_buffers` 3,958 MB). The lab sets what the image would choose on a 4 GB VM
  (1 GB, 3 GB effective cache, 5 MB work_mem) and keeps `pg_stat_statements` on, so the appliance is not helped by memory it would not have.

Sampling: every 5 s, inside the appliance, `tools/oglab/sampler.sh` (cgroup CPU and memory of the appliance container, PostgreSQL connections by role, idle-in-transaction
and lock waiters, ungranted locks, authorisation-set size read from the kernel set, live sessions by state, devices with more than one live session, live count against
the licence, accounting row counts); every sign-in attempt of the generator is logged with its result and latency (`ogload signin -log`); `pg_stat_statements`, `pg_stat_wal`,
a commit-latency probe (one tiny write transaction every 250 ms), the PostgreSQL log (`log_lock_waits`, deadlocks) and the service journals are kept.

## 3. Root causes, found by measurement

The state at the start (release `5ac9cc5c9851`, 1,700 long sessions, 300 sign-ins a minute, 400 s runs repeated): 5 to 7 percent of sign-ins enforced, median 15 s, authorisation of
long sessions lapsing, downloads failing at random, the database apparently idle (CPU 13 to 30 percent of the 2 vCPUs).

### 3.1 The kernel's neighbour table (ARP) overflowed - a product limit as well as a lab fact

`dmesg` in the lab VM: `neighbour: arp_cache: neighbor table overflow!`, continuously during every run. Linux defaults are a soft limit of 512 and a **hard limit of 1,024**
entries (`net.ipv4.neigh.default.gc_thresh3`). One client network of 1,700 to 2,000 devices needs about that many entries on the appliance alone; past the limit the kernel
refuses to create an entry and the packet that needed it is lost. Measured: TCP SYNs retransmitted (`TcpExtTCPSynRetrans` 7,166), a TCP connect to the published PostgreSQL port
(`127.0.0.1:5432`, through `docker-proxy`, whose leg to the container needs a neighbour entry too) took **p90 2.0 s and up to 35.6 s** while a connect straight to the container
address took 3 to 8 ms, and the generator's downloads succeeded in 13 to 40 percent of requests in the previous round (914 of 6,972; 2,753 of 6,806).
After raising the limit (`gc_thresh1/2/3` = 8,192 / 32,768 / 65,536 in the lab VM) the same download load succeeded **59,760 of 59,760**.
The appliance now carries the setting itself (`deploy/sysctl/99-stayconnect.conf`: 4,096 / 8,192 / 16,384, installed by a fresh install and now also by `--upgrade`).

### 3.2 scd exhausted its 16-connection database pool with the database idle

`scd` waits for the kernel to confirm a guest (up to 8 s) inside the sign-in request, and while waiting it **read the session row from the pool ten times a second**
(`awaitIAMv2Enforcement`). At five sign-ins a second that is 400 reads a second competing with the sign-ins themselves for sixteen connections. Measured with the new pool metrics and a
goroutine dump during churn: 16 of 16 connections checked out, 0 idle, **acquire wait 1,516 s and then 2,741 s cumulative within 40 s of a run**, 53 of the 64 in-flight
request goroutines blocked in `pgxpool.Acquire` (the trivial `GET /v1/tenant/branding` took 1.5 s, `POST /v1/sessions/authorize` p50 3.7 s, `/commerce/redeem` p50 4.3 s while the database executed
each statement in 1 to 4 ms). Requests that reached their deadline cancelled their queries, pgx closed those connections, and the pool re-dialled: 16 of 17 backends were younger than ten seconds.
`pg_stat_activity` showed a dozen connections "idle in transaction" for 5 to 30 s: transactions whose goroutines were waiting for another connection or had been abandoned at a deadline.

### 3.3 Waiting holds a guest-capacity slot

The 64 in-flight guest slots of `guestCapacity` were held by sign-ins waiting up to 8 s for enforcement. At 5 sign-ins a second that is 40 slots doing nothing, and every other guest request queued for up to two seconds and then got `503 BUSY` (38 in one 20-minute run even after the other fixes).

### 3.4 The appliance-wide licence lock was held across a commit and everything queued behind it

`reserveLicensedSlot` takes `pg_advisory_xact_lock` (held to the commit) so that count-then-insert is exact. In churn: 391 lock acquisitions waited **385 ms on average, 4.3 s at most, 151 s in four minutes**,
each waiter holding one of the 16 pool connections while it waited.

### 3.5 A new session waited for the next tick before the kernel was even asked

acctd derives the plan and hands it to netd on a period (the cut-off setting, 5 s). A session created just after a submission waited on average half a period, then for the pass.

### 3.6 netd's own CPU: three costs that grow with the session count

Go CPU profile (40 s, 300 sign-ins a minute) plus the service cgroup: netd 73 percent of one CPU, of which a third was its own code. 23 percent of its Go time was `os.ReadFile` (the boot
identity and the uptime, read **once per session per pass**, about 4,000 reads a pass), 10 percent was `addressOwner.Evidence` scanning the whole DHCP lease list for each session (two thousand
sessions times two thousand leases a pass), and the rest was the commands it runs (`nft -j list set` of the 2,000-element authorisation sets, `tc -s class show`).

### 3.7 `allocate_class_generation` scanned all checkpoints; `activate_session_enforcement` too

Both read `accounting_checkpoints` without an index that fits (`max(source_epoch)` for the tenant and site; "does another session hold this bridge and class at this generation"). 23.6 ms per call on average
with 13,700 rows, growing with every session the appliance has ever kept a checkpoint for (5,953 calls, 140 s of database time in 20 minutes).

### 3.8 The writer guard's janitor scanned, locked and **deadlocked** every writer

`iam_v2.begin_controlled_operation` is called by every sign-in, accounting write and enforcement write (about 140 a second). Each call began with an unindexed
`DELETE FROM iam_v2.controlled_operation_scope WHERE opened_at < now() - interval '1 hour'` over a table that holds one hour of rows (143,000). Every call scanned it; and when rows began to
expire (one hour after the lab started traffic, and again every time the oldest rows aged out) all concurrent callers tried to delete the same rows in their own order while holding other locks:
**148 deadlocks in the PostgreSQL log in two bursts of about a minute each** (02:32-02:33 and 03:30-03:35), victims `begin_controlled_operation`, `authorize_entitlement_device`, `ingest_absolute_counters`, with a
dozen backends waiting on each other, 19 lock waiters and CPU falling to 27 percent at the sampler's reading, and 57 failed sign-ins in that minute and 15 in the next two. The rows start to expire one hour after the first
traffic on a database, so a 60-minute run on a fresh database reaches it at about minute 60; the 30-minute rehearsal met it because the lab database had been in use for longer.

### 3.9 acctd sent one transaction per live session per tick, and most were replays

`ingest_absolute_counters` takes the checkpoint's row lock (a write, a WAL record and a commit) even when the counter has not moved and the answer is "replay". 105,000 calls in 20 minutes (87 a second, 1.4 million
WAL records, 257 s of database time), the largest single database cost on the box. Nine in ten were for sessions that had not moved.

### 3.10 Test cadence: a client that signs in when its session ends is a hand-over

The earlier churn re-signed a client every 60 s, exactly when its 60-second session ended and before acctd had ended it. The new session then reused the old session's address and class, which netd handles on
its slow per-session path (0.5 to 1 s each, serial): passes of 15.8 s with 100 to 140 such sessions. That is real behaviour for a device that reconnects at the instant its access ends and is reported as a limit (7), but it is not how
300 distinct guests arrive. The run now cycles 400 clients (each idle for about 20 s between sessions).

### 3.11 Not a cause (measured, ruled out)

The write-ahead log device was not saturated by the sign-in path once the above were removed: the commit probe stayed at p50 2.9 ms, p90 6.3 ms, p99 22 ms under load against 2.9 ms idle; `fdatasync` ran at 20 a second against a capacity of about 330.
CPU was not the cause of the collapse (13 to 30 percent of the budget while it collapsed).

## 4. Changes

Invariants kept: fail-closed; the 90-second authorisation lease (not lengthened); the write-ahead journal; MAC binding; accounting-origin registration before ACTIVE; the exact licence
limit; quota termination; no duplicate session for a device and entitlement; no lost authorisation. No role limit was changed.

| # | Change | Files |
|---|---|---|
| 1 | **Neighbour table sized for the licensed guest count** (4,096 / 8,192 / 16,384, IPv6 1,024 / 4,096 / 8,192); `--upgrade` now installs `deploy/sysctl` too (a fresh install already did) | `deploy/sysctl/99-stayconnect.conf`, `deploy/scripts/appliance-install.sh` |
| 2 | The sign-in's wait for enforcement is **woken by the database notification** (`onegate_session_active`, already sent by netd's activation), with a 1 s safety poll (100 ms while the listen connection is down); no pool read per waiter per 100 ms | `cmd/scd/iamv2_session_activate.go` |
| 3 | The wait **hands back its guest-capacity slot** (`releaseGuestSlot`) | `cmd/scd/guest_capacity.go` |
| 4 | **Licence admission: shared path far from the limit, exact exclusive path near it.** Every admission takes the appliance's advisory lock: shared on the new path, exclusive on `reserveLicensedSlot` (the room sign-in and post-stay paths still use it, unchanged). The shared path counts committed live sessions and admits only below `limit - pool size` (the most transactions that can be in flight); at or above that it rolls back and runs again on the exclusive path, which is exact. Proof in the code comment; mutation-tested (6) | `cmd/scd/iamv2_session_ops.go` (`reserveLicensedSlotShared`, `admitLicensed`, `admissionConcurrency`), `iamv2_session_activate.go` |
| 5 | **Event-driven admission.** The activating transaction sends `pg_notify('onegate_session_pending')` (delivered only on commit); acctd LISTENs and kicks its plan submitter, at most one submission per 500 ms, wake-ups arriving while one is held back join it; the periodic submission is unchanged and remains the fallback | `cmd/scd/iamv2_session_activate.go`, `cmd/acctd/pending_listener.go`, `loops.go` |
| 6 | acctd's pool is explicit (6 connections plus the listener, inside `svc_acctd`'s 10) | `cmd/acctd/pending_listener.go`, `main.go` |
| 7 | **acctd sends only what moved, and batches it**: a session whose counters equal the last accepted observation is not sent; the rest go in one transaction per chunk of 100 (a refused observation at position k rolls back that chunk; the k before it are sent again as a chunk, the refused one alone, the rest continue) | `cmd/acctd/phase3_accounting.go`, `phase3.go` |
| 8 | netd: the security clock is read once per 250 ms / the boot identity once per second instead of once per session per pass (carried forward by the monotonic clock, an unreadable file is still an error); the lease list is indexed by address once per pass; the pass line says why a session took the slow per-session path (`ind_*` counters) | `cmd/netd/phase3_securitytime.go`, `phase3_shaping.go`, `phase3_address_owner.go`, `phase3_batch.go` |
| 9 | **Migration 0191**: indexes `accounting_checkpoints (tenant, site, source_epoch DESC)` and `(tenant, site, bridge, class_minor, source_epoch)`, built `CONCURRENTLY`; reversible | `data-plane/migrations/0191_*` |
| 10 | **Migration 0192**: `begin_controlled_operation` keeps its allowlist, token and scope exactly, and its janitor now deletes at most 100 expired rows per call, only rows nobody else holds (`FOR UPDATE SKIP LOCKED`), found by an index on `opened_at` (built `CONCURRENTLY`); reversible (the down file restores the previous body) | `data-plane/migrations/0192_*` |
| 11 | The production baseline was **regenerated through 0192** (the release build refuses a baseline older than the newest migration); the diff is exactly the two indexes, the third index and the new function body | `data-plane/migrations/baseline/0000_production_baseline.sql` |
| 12 | Observability: `scd_db_pool_*` metrics (acquired, idle, max, acquisitions, waited, canceled, total wait) | `internal/metrics/pool.go`, `cmd/scd/main.go` |
| 13 | Tools: `ogload signin -log` (a line per attempt), an `oglabdiag` build tag that serves pprof on loopback (never in a release), the lab scripts and README | `tools/ogload`, `tools/oglab`, `cmd/*/pprof_lab.go` |

Commits (branch `worktree-agent-a545bfb7990bd42d5`): `0b79ef4d`, `9e3ab1e0`, `7280df5c`, `65e404af`, `e6efbf1e`, `2f687f24` (the release), then documentation.

## 5. Before and after

Same lab, same load (1,700 long sessions, 10 percent moving, 300 sign-ins a minute of 60-second vouchers):

| | before (release `5ac9cc5c`, this lab) | after (release `2f687f24`) |
|---|---|---|
| Sign-ins enforced | 27 of 600, 59 of 1,050, 88 of 1,200 (4.5 to 7.3 percent); previous round 19 of 3,000 | **17,998 of 18,000** |
| p50 / p95 / p99 latency | 15 s / 24 s / 27 s (most refused at the 8 s wait) | 3.2 s / 4.7 s / 5.3 s |
| Downloads through forwarding | 914 of 6,972 and 2,753 of 6,806 (previous round); not measured separately in this lab before the neighbour table was raised | 119,520 of 119,520 |
| scd pool | 16/16 in use, 0 idle; 1,516 to 2,741 s of acquire wait in 40 s; 53 of 64 request goroutines waiting | 24 waits, 5.2 s in 60 minutes |
| `POST /v1/sessions/authorize` / `/commerce/redeem` p50 | 3.7 s / 4.3 s | 16 ms / 39 ms |
| Licence-lock wait (`pg_advisory_xact_lock`) | 385 ms mean, 4.3 s max, 151 s in 4 min | absent from the 18 statements with the most total time |
| `begin_controlled_operation` | 2.0 to 2.3 ms mean, 2.8 s max; 148 deadlocks in 30 minutes | 0.53 ms mean; 0 deadlocks (the previous run's rows, 143,000 of them, expired through the new janitor during the 60 minutes) |
| `allocate_class_generation` | 23.6 ms mean | 10.3 ms mean (6 run at once on one row; was 5.6 ms of the 24 spent scanning, growing) |
| `ingest_absolute_counters` | 105,000 calls and 257 s in 20 minutes | about 61 calls a second (the moving sessions and their first observation), 2.3 ms mean, in chunks |
| netd CPU | Go profile: `os.ReadFile` 23 percent of netd's own Go time, lease scan 10 percent; service cgroup 73 percent of one CPU | the two costs are removed in code and unit-tested, but the cgroup figure did not move (73 percent of one CPU before and after: the commands netd runs, `nft -j list set` and `tc -s class show`, dominate). **Not re-profiled after the change**; no CPU saving is claimed for it |
| Long-session authorisation lapses | set read as 0 or 712 of 1,700 in churn | never below 1,700 in 651 samples |
| Pending sessions during churn | 45 to 94 standing | at most 28, mean 15 |

How the improvement arrived (each row a lab run, nothing hidden): the scd, licence-lock, wake-up and acctd changes alone (neighbour table still overflowing): 201 of 900 (22 percent);
plus the neighbour table: 327 of 900 (36 percent), passes of 15.8 s because the churn re-signed clients at the instant their session ended (hand-over path); plus the 400-client
cadence: **1,196 of 1,200** (99.7 percent); 20-minute soak 5,938 of 6,000 (98.97 percent, a 3-minute cluster while the workstation was building and testing); 30-minute rehearsal 8,927 of 9,000
(99.19 percent; 57 failures in one minute, which is the first deadlock burst of 3.8 at the first expiry of the writer-guard rows); migration 0192; the 60-minute run on the release.

## 6. Tests

Every behaviour changed has a regression test. Linux (`golang:1.26.10`), real PostgreSQL 16 / TimescaleDB 2.16 built with the full appliance schema (`scripts/lib-fullschema-testdb.sh`, migrations through 0192) for the
`integration` tag. Nothing below was claimed without running it.

| Area | Test | What it holds |
|---|---|---|
| scd | `guest_slot_release_test.go` (2 tests) | a waiting handler hands its slot back once, another guest request is served meanwhile; capacity one still holds one request; releasing outside a guest route is harmless |
| scd, integration | `license_capacity_shared_integration_test.go` (4 tests) | 200 contenders for 60 slots, 8 in flight: **exactly 60 live, 140 refused**; shared and exclusive callers together never exceed; shared admissions do not wait for each other and an exclusive one waits for them; near the limit the shared path hands over and the last slots are admitted one by one, the next refused; small and unlimited licences |
| scd, integration | existing `license_capacity_integration_test.go` (5 tests) | unchanged, still pass (last-slot race, forced interleaving, appliances independent) |
| acctd | `pending_listener_test.go` (3 tests) | wake-ups bring the submission forward but not faster than the gap, twenty wake-ups while one is held back give one extra submission; the period is still the fallback and a zero gap is immediate; the pool is explicit and a DSN value wins |
| acctd, integration | `pending_listener_integration_test.go` | a committed `pg_notify` kicks the submitter, a rolled-back one does not |
| acctd, integration | `phase3_ingest_batch_integration_test.go` (3 tests) | seven sessions billed exactly in one pass; sessions at the first, middle and last position of a chunk refused: the others billed exactly once, the refused unbilled and reported, a replay bills nothing more; unchanged counters are not sent (a pass over twelve commits at most four times), a restarted process asks again and is answered with the replay |
| acctd, integration | existing `phase3_pass`, `phase3_ingest`, `phase3_boundary` tests (all) | still pass: baseline, delta, restart, uncertain commit, two observations in a second, epoch reset, unreadable counters, scope failures |
| netd | `phase3_pass_cost_test.go` (3 tests) | the clock and boot identity are read once not per ask, never drift more than the refresh period, an unreadable clock is still an error; the indexed lease answer equals the full scan on lapsed rows, released rows, two live rows of one address, no row, an empty table; one read of the lease list for eight asks |
| metrics | `pool_test.go` | the pool figures are exposed and follow the pool |
| writer guard, integration | `janitor_integration_test.go` (3 tests) | a caller whose lock timeout is 400 ms gets its scope at once while another transaction holds the expired rows (the previous body fails this with `canceling statement due to lock timeout`); a batch of 100 is removed per call, fresh rows are never removed (the previous body removes all 250 at once); 24 writers during expiry of 5,000 rows never fail; the allowlist and the open-scope decision are unchanged |
| migrations | 0191 and 0192 up, down, up, up again | indexes created and dropped, idempotent, `indisvalid`; the full-schema build applies both |
| mutation | margin of the shared path set to zero | `65 live sessions, want exactly the limit 60` and the near-limit test fails: the tests do catch the failure they exist for |

Also run and passing: `go test ./cmd/netd ./cmd/acctd ./cmd/scd ./internal/metrics ./internal/enforce ./internal/shape ./internal/nft` (netd alone 91 s), the whole `./cmd/acctd` and `./internal/writerguard`
integration suites, `go vet` for the same packages with and without the `integration` and `oglabdiag` tags, `gofmt`. Not run: the project-wide suites, the governance validators and the four gates (Direct Execution Mode; this is a targeted change).

## 7. Limits and what is not claimed

1. **Simulated, in one lab, once.** A container host that shares its kernel and disk, not the reference VM; 2,400 simulated clients, not devices. One 60-minute pass is evidence, not a distribution: the 30-minute rehearsal before it showed 99.19 percent because of the deadlock burst in 3.8
   (fixed afterwards), and a 20-minute soak 98.97 percent while the workstation was building and testing. The lab host must be quiet during a run.
2. **The CPU budget is thin.** Mean 78.8 percent of the 2 vCPUs, p95 93.9, peaks over 100 percent of a 5-second window. netd is 73 percent of one CPU and almost all of that is the commands it runs every pass (`nft -j list set` of the 2,000-element
   authorisation sets twice, `tc -s class show` per device), PostgreSQL 54 percent. A busier appliance (more than 300 sign-ins a minute, or more moving sessions) will not fit this budget without further work.
   Identified and **not changed**: `iam_v2.p6_suspend_over_budget` (356 ms, once per accounting tick, scans every active entitlement and calls a function per row) and the expiry candidate query (198 ms per tick), both accounting-owner code outside this mission's scope; netd's gate listing could read the
   sets less often or in a cheaper format.
3. **The latency tail is close to the response deadline**: p99 5.3 s, max 8.07 s against an unchanged 8 s wait; two of 18,000 were refused at it. Admission still waits for one full netd pass (about 2 s) plus the wake-up gap; there is no admission-only pass.
4. **A device that reconnects at the instant its previous session ends** still costs netd's per-session hand-over path (0.5 to 1 s each, one after another): 100 such sessions in a pass is a 15 s pass. The test deliberately avoids this cadence (3.10); ordinary guests do not produce it, a building full of devices set to reconnect on a timer might.
5. **Memory is full of page cache by design**: the appliance cgroup sits at its 4 GiB limit with 3.5 GB of file cache (reclaimed 2,899 times in the run, no pressure, no OOM). Anonymous memory is 0.56 GB; the database holds 1 GB of shared buffers.
6. **Only voucher sign-in was driven at this rate.** The activation, licence, enforcement and accounting paths are shared with the other methods (account, OTP, social, room), but their earlier steps were not exercised here; the room sign-in and post-stay admission still take the exclusive licence path.
   `iam_v2.p5_begin_controlled_operation` (post-stay) carries the same janitor line as 3.8; it is called rarely and was left alone (another work stream owns that code).
7. **Not tested**: Central unreachable during churn, a PostgreSQL or netd restart during churn (the previous round's failure drills stand), HA, real hardware, physical devices, real PMS traffic, the `ALL_ACTIVE_INTERFACES` and multi-network cases beyond the two client networks used.
8. **Governance and delivery**: the baseline file was regenerated; other branches adding migrations also regenerate it, so a merge needs it regenerated once more from the merged chain (`scripts/generate-production-baseline.sh`). Nothing was pushed, merged or deployed to any real host. The release tarball is in `D:/tmp/oglab3/dist` (not committed).
9. **Neighbour table**: the lab VM's own table was raised by hand (it does not survive a Docker Desktop restart); the product setting only reaches an appliance through the installer's `sysctl --system`. A deployment that sets the sysctl through other management must carry the same values.
