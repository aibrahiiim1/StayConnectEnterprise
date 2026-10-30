# Capacity and first-time offline activation acceptance (2026-10-29 / 2026-10-30)

Closure round of the AD-1..AD-68 audit remediation mission. Tools: [`tools/oglab`](../../tools/oglab/README.md) (the lab) and
[`tools/ogload`](../../tools/ogload) (the load generator). Related evidence: [`reviews/NETD_ENFORCEMENT_SCALING.md`](../reviews/NETD_ENFORCEMENT_SCALING.md),
[`reviews/PR272_ROLLUP_REVIEW.md`](../reviews/PR272_ROLLUP_REVIEW.md), [`reviews/USAGE_AFTER_RAW_PURGE_FIX.md`](../reviews/USAGE_AFTER_RAW_PURGE_FIX.md),
[`reviews/OFFLINE_ACTIVATION_REGISTRY_FIX.md`](../reviews/OFFLINE_ACTIVATION_REGISTRY_FIX.md), [`reviews/AD36_NEW_DEVICE_IDENTITY_REVIEW.md`](../reviews/AD36_NEW_DEVICE_IDENTITY_REVIEW.md).

## 1. What this is, and what it is not

**Simulated acceptance in an isolated lab, not field acceptance.** The lab is a set of privileged systemd containers on one Docker Desktop
host (no hypervisor rights were available), installed with the product's own installers, with the appliance capped at 2 CPUs and 4 GB by cgroup
(the approved reference). 2,000 *simulated* clients (one network namespace, MAC and DHCP lease each, real portal sign-ins and real HTTP transfers) stand in
for devices. Nothing was registered on the production Central; the lab Central is new (new vendor key, assignment keys, registry root, appliance CA)
and the production name is blackholed. A container shares the host kernel and CPUs, so absolute numbers are indicative, not a hardware benchmark.

**Not performed (needs equipment or authority this mission does not have):** 2,000 physical devices and a real Wi-Fi/switch fabric; the approved
hardware or a VM at 2 vCPU / 4 GB; real PMS traffic (post-stay, FIAS, posting); payment providers.

## 2. Capacity results

Release under test: `f20faa171ea7` (appliance), 1,700 long sessions plus up to 300 cycling clients on two client networks (one untagged, one VLAN),
Kea leases for 2,000 clients, accounting, shaping and the kernel authorization set all live.

| Item | Result |
|---|---|
| Ramp: 1,700 sign-ins at 300/min (release `885b6ac9`, before the churn fixes) | 98% succeeded; 1,700 sessions shaped and authorized |
| Idle-load admission with 1,700 sessions live (release `f20faa17`) | a fresh sign-in is online in 3.5 to 6.6 s |
| 1,700 sessions held for the whole run | 1,700 shaped; the kernel authorization set stayed full except during an acctd outage (below) |
| Memory | cgroup peak 3,039 MB of 4,096 MB; resident: scd 37-108 MB, acctd 29-136 MB, netd 36-42 MB, edged up to 153 MB, portald up to 142 MB; all PostgreSQL processes together 2.7-4.8 GB resident (shared buffers counted per process) |
| CPU | 10-48% average of 2 CPUs by phase, 77% max |
| Database | 33-34 connections max (scd 20, the per-role limit); accounting rows grew +8,377 (10 min steady) and +1,343 (5 min stress) |
| **300 successful sign-ins/minute sustained with 60-second sessions (steady phase)** | **NOT MET**: 19 of 3,000 attempts succeeded; p50 8.3 s, p95 16.8 s; the refusals are "could not bring your device online" (enforcement not confirmed in time). |
| Traffic phase success | not a valid forwarding measure at this concurrency (see 2.2) |

### 2.1 Why the churn target is not met (measured, not guessed)

* The first netd defect (a pass that grew with the square of the sessions) was fixed earlier; the ramp then reached 98%.
* Under sustained churn the netd pass budget was consumed by queueing, lease renewal waited behind other work, ended sessions were re-recorded every pass and the plan
  listed them for an hour. Fixed and measured (`NETD_ENFORCEMENT_SCALING.md` section 10): in the simulator 268 of 300 long sessions lost authorization before and 0 after, longest pass 127.8 s to 4.3 s, database calls 40,209 to 3,114.
* **What remains:** new sign-ins queue on the appliance-wide licence-slot advisory lock and a saturated `svc_scd` connection limit, and every admission waits for a netd pass whose database calls
  (`activate_session_enforcement` hit its 3 s bound repeatedly under churn) are slowed by the same load. In this run the lock is now taken only for the count and the insert
  (`iamv2_session_activate.go`), which is correct but did not change the outcome: p50 sign-in latency under churn is 8 s with most attempts finishing after the 10 s target.
  Closing the gap needs further change in scd (connection limit, admission path), acctd (per-session commits, long expiry transaction) and possibly a schema or role decision, which is a controlled change and is **not** made here.
* An earlier voucher-exhaustion artefact (spent 60-second voucher codes) was found and removed from the measurement by issuing fresh codes; the numbers above are with fresh codes.

### 2.2 Honest limits of the traffic numbers

`ogload traffic` success was 914 of 6,972 (steady) and 9,953 of 33,320 (stress): the failed connections never reached the bridge (a burst of 850 clients fetching from the appliance's own portal failed 43% while 100 clients at once
succeeded 100%), so these figures describe the generator and the container network, not forwarding health. They are reported, not claimed as a pass.

### 2.3 Failure behaviour (single clean run, 1,700 sessions live)

| Test | Result |
|---|---|
| netd stop/start | authorization set refilled within 5 s; 1,700 sessions still active |
| PostgreSQL restart | listening again in 2 s; authorization set never emptied; 1,700 sessions active. (An earlier attempt left the lab's published database port unprogrammed after a Docker daemon restart in the container; recreating the container through the product's compose file restored it. Lab plumbing, not a product change.) |
| acctd stopped for 4 minutes | the authorization set emptied (a lease lives only as long as the plan that renews it, 90 s), so guests go offline after about 90 s without a plan; sessions stayed active in the database; the set refilled within 6 s of acctd starting. **Whether that bound should be longer is a product decision.** |
| roll-up catch-up after the outage | raw, minute and hour byte totals equal (7,161,476,120) after catch-up: nothing lost, nothing counted twice |
| Central disconnect | **not run in this lab.** By construction no Central call is on the sign-in path (licence from disk), but this was not exercised here. |
| Retention (migration 0180) | proven with a real PostgreSQL in `usage_after_raw_purge_integration_test.go` (usage unchanged after the raw purge); not repeated at lab scale |

## 3. First-time offline activation acceptance

Fresh factory-clean appliance container installed with `--offline-activation-only` and only the lab Central's public trust files; the lab Central is isolated.

* **Defect found and fixed:** a genuine, correctly signed package was refused (`no assignment trust registry on this appliance`) because the signed registry only arrived online. The package now carries the signed registry; it is verified against the pinned root and applied all-or-nothing with undo ([`OFFLINE_ACTIVATION_REGISTRY_FIX.md`](../reviews/OFFLINE_ACTIVATION_REGISTRY_FIX.md)). Central must run this release before an appliance on it can take a package; an appliance on the older code fails closed.
* Request generated on the appliance (signed by its own identity key), imported on the lab Central, activated, package minted, uploaded to the appliance byte for byte: **activated**, assigned to its customer and site, licence bound (30 days, 50 concurrent guests), Central connected afterwards.
* **Single use:** re-applying the consumed package -> 403 "appliance is already assigned".
* **Invalid packages (all refused, nothing applied):** modified signature, serial, appliance id or expiry -> 403 "signature invalid"; truncated file -> 400.
* **Recovery:** interrupted-apply recovery and undo of the registry are covered by the scd tests (journal, single-use ledger, identity adopted last).
* The identity of the existing appliance was never cloned or altered; no appliance was registered on the production Central.

## 4. Roll-up review (PR #272)

Reviewed in [`PR272_ROLLUP_REVIEW.md`](../reviews/PR272_ROLLUP_REVIEW.md): a poisoned two-hour window stalled catch-up forever; fixed with an adaptive window. Usage that depended on raw rows before the purge lost data; fixed by migration 0180 (`accounting_usage_entitlement`). Late-commit loss analysed, not reachable at current settings, documented.

## 5. Decisions and limits left to the Product Owner

1. The churn target (300 sign-ins/min sustained) is not met on this lab; the fix needs scd/acctd/database work as in 2.1.
2. Lease/plan bound: 90 s without a plan takes every guest offline (fail-closed). Longer?
3. Kernel limit: at most 2,047 shaped sessions per bridge or IFB; a site above that needs a second client network.
4. AD-36: the entitlement continuity PO clarification is not met by the new conversion (a new Entitlement is created); refusals after check-out are logged but not audited. See the AD-36 review. Not changed.
5. Field acceptance still required: physical devices at scale, reference hardware, real PMS post-stay traffic.

(Item 1, the unmet churn target, is answered by section 6 below; sections 2 to 5 stand as the history of the first round. Raw evidence of the new run: [`evidence/signin-capacity-2026-10-30/`](evidence/signin-capacity-2026-10-30/).)

## 6. Sign-in capacity closure round (2026-10-30): 300 enforced sign-ins a minute for one hour, met in the lab

Full account, with the measurements behind every cause: [`reviews/SIGNIN_CAPACITY_CLOSURE.md`](../reviews/SIGNIN_CAPACITY_CLOSURE.md). Lab: [`tools/oglab`](../../tools/oglab/README.md), rebuilt
(Central and appliance containers installed from release tarballs, 2,400 simulated clients). Release under test `2f687f2406dee6763986b3f5b59c5470d7444501`, installed by
`appliance-install.sh --upgrade` from `5ac9cc5c9851`. Same simulated-acceptance caveats as section 1; nothing was pushed, merged or deployed to a real host.

**Run: 04:05:37 to 05:05:39 UTC, 1,700 long sessions, 10 percent of them moving 256 KB every 5 s, 300 sign-ins a minute of 60-second vouchers for 60 minutes.**

| Item | Result |
|---|---|
| Enforced sign-ins | **17,998 of 18,000 (99.989 percent)**; 303 to `/success` and the session `active` with its kernel authorisation; the two others refused at the 8 s wait (minute 58) |
| Latency | p50 3.21 s, p95 4.73 s, p99 5.31 s, max 8.07 s; 17,996 inside 8.0 s |
| 1,700 long sessions | kernel authorisation set never below 1,700 in 651 samples (every 5 s); 0 duplicate live sessions per device |
| Licence | live count peaked at 2,038 of 2,500; an at-limit run (licence 1,990) peaked at exactly 1,990 with 102 refusals, 1,098 enforced, licence restored |
| Data quota | ten 8 MiB-quota sessions ended `DATA`, billed 8.55 MB (157 to 181 KB over: one accounting period of the guest's traffic), session bytes equal entitlement bytes |
| Accounting | raw, per-minute, per-hour, per-session and per-entitlement byte totals equal (76,072,981,815) once the roll-up caught up |
| CPU / memory | mean 78.8 percent of 2 vCPUs, p95 93.9, max 102; cgroup 4 GiB limit with 0.56 GB anonymous, 0 OOM, no memory pressure |
| Database | 0 deadlocks, 0 lock timeouts, 0 ERROR lines in any service; scd pool waited 5.2 s in total (was 2,741 s in 40 s) |
| Forwarding | 119,520 of 119,520 downloads succeeded |

Corrections to this document's first round, from the new measurements:

* **2.2 ("the traffic failures are the generator's")** was the lab VM's kernel neighbour table, limit 1,024, overflowing (`neighbour: arp_cache: neighbor table overflow!`). Real forwarding was not failing in the way the numbers said, but the same limit applies to an
  appliance with a client network of 1,000 or more devices; the product now sets it (`deploy/sysctl/99-stayconnect.conf`, installed by `--upgrade` as well as a fresh install).
* **2.1 ("needs further change in scd, acctd and possibly a schema or role decision")**: no role limit was changed. Two additive migrations were made: 0191 (two indexes on `accounting_checkpoints`) and 0192 (the writer guard's janitor no longer scans, locks and deadlocks every writer), both reversible,
  with the production baseline regenerated through 0192.
* The earlier "19 of 3,000" was the scd database pool, the licence lock held across commits, and every waiting sign-in polling the pool (a cascade that left the database idle and 53 of 64 request goroutines waiting for a connection), not the database or the CPU.

**Decisions and limits left to the Product Owner** (additions to section 5): (6) the CPU budget at 300 sign-ins a minute is thin (p95 94 percent); the next consumers are netd's per-pass `nft`/`tc` listings and two accounting-owner database functions identified in the closure document;
(7) the baseline file was regenerated, so branches that add migrations must regenerate it again at merge; (8) the lab VM's neighbour table was raised by hand and is not persistent.

## 7. Final release acceptance (2026-10-30)

The same representative tests repeated on the **exact final candidate release `dbf86d288f65b093211232569cb102e203f2cf79`** (`delivery/closure-engineering`, which
integrates accounting migration 0181, the AD-36 closure and 0190, the flower shaping classifier, the sign-in capacity fixes 0191/0192 and the acctd watchdog), in the same
isolated lab (`tools/oglab`, appliance container capped at 2 vCPU / 4 GB). Nothing was pushed, merged or deployed to a real host; the production Central, the test
appliance and every retired host were not contacted. Raw evidence: [`evidence/final-release-2026-10-30/`](evidence/final-release-2026-10-30/). The availability analysis is
[`reviews/AVAILABILITY_DURING_ACCTD_INTERRUPTION.md`](../reviews/AVAILABILITY_DURING_ACCTD_INTERRUPTION.md).

### 7.1 Release under test, read back

| Item | Result |
|---|---|
| Build | `deploy/scripts/appliance-build.sh` from a clean clone of `dbf86d28`: every binary reads back `revision=dbf86d288f65 modified=false tags=stayconnect_production`; tarball sha256 `ff3afc191ec9514ef41a2a1c4f2a68a52e712c424e8aa75198fa1f95ecc05dbb` |
| Central | Not rebuilt: `git diff 2f687f24 dbf86d28` touches no Central code (no `ctrlapi`, `cloud` or Central deploy file); the lab Central is unchanged |
| Install | `appliance-install.sh --upgrade --yes` over the previous candidate (`2f687f24`); `RELEASE.json source_commit` = `dbf86d288f65b093...`; the running `scd`/`netd`/`acctd` executables hash equal the installed files, which embed `vcs.revision=dbf86d288f65...` (`go version -m` is not available on the appliance; the embedded string was read from the binaries) |
| Migrations | 0181, 0190, 0191, 0192 applied **in order by the installer's own runner** (each behind its pre-change backup, gap check passed), `.up` and `.down` files present in the release. Applied with 1,700 sessions live |
| acctd unit | `WatchdogSec=30`, `NotifyAccess=main`, `Restart=always`, `RestartSec=2` (systemd `show` read back) |
| Neighbour-table file | `/etc/sysctl.d/99-stayconnect.conf` installed by `--upgrade` (4,096 / 8,192 / 16,384). The live value cannot be read inside the lab container (no `/proc/sys/net/ipv4/neigh/default` in that namespace); no `neighbor table overflow` line was written to the kernel log during this round (the last one is from before the previous round's raise) |
| Shaping classifier | The lab kernel (6.6.87.2 WSL2) has `cls_flower`. After the 1,700 long sessions were signed in again on this release the client bridges carry **one flower filter per session and direction** (br-g-1e89de3f: 1,000 filters for 1,000 classes; br-g102: 700 for 700; the same on both IFBs), 0 u32. The `NETD_SHAPE_CLASSIFIER=u32` fallback was therefore not needed and was not run in the lab (it is covered by the `internal/shape` unit and real-kernel tests) |

Two facts found on the way, neither a product change. (1) The lab database carried 0191/0192 in its ledger (applied by hand in the previous round, before 0181/0190 existed on that
branch), so the installer reported "at 0192, pending 0" and would have skipped 0181 and 0190. I rolled 0192 and 0191 back with their reviewed `.down` files and removed their ledger rows so that the upgrade ran as it does from 0180
(empty ledger plus baseline marker 0180). The installer's gap check covers the versions it is applying, not a hole beneath the highest applied one; an appliance upgraded linearly cannot be in that state. (2) The worktree for this round was created from `master` and was moved to `dbf86d28` before anything was built.

### 7.2 Results

| # | Test | Result |
|---|---|---|
| 2 | Re-establish the 1,700 long sessions on the final release | all 1,700 admin-disconnected, then 1,700 of 1,700 signed in at 300 a minute in 341 s (p50 1.12 s, p99 2.23 s); authorisation set 1,700, 0 pending; **3,400 of 3,400** forwarded downloads (891 MB, p50 108 ms) from all 1,700 |
| 3 | **Sustained**: 300 offered sign-ins a minute for 60 minutes, 1,700 long sessions held, 60 s churn (06:36:33 to 07:36:36 UTC) | **17,998 of 18,000 enforced (99.989 percent)**: 303 to `/success` and the session `active` with its kernel authorisation; the 2 others were answered "could not bring your device online" (minute 52). Offered 18,000 in 3,603 s = 299.8 a minute. Per ten-minute window: 100, 100, 100, 100, 100, 99.93 percent. Per full minute the enforced count was 295 to 313 (median 301; the offer's own minute-bucket jitter). Refused and pending are counted as failures |
| 3 | Latency (portal request to success) | p50 **3.40 s**, p95 **5.08 s**, p99 **5.77 s**, max **9.91 s**; 17,985 of 17,998 inside 8.0 s (13 successes took 8.0 to 9.9 s). Slightly slower than the previous round (3.21 / 4.73 / 5.31 / 8.07 s) |
| 3 | The 1,700 long sessions | kernel authorisation set read every 5 s (628 samples): **never below 1,720**, active sessions never below 1,714; 0 devices with a second live session in any sample; 1,700 active and 1,700 authorised at the end |
| 3 | Licence | live count (active plus pending) peaked at 2,036 of 2,500 |
| 3 | CPU | cgroup of the 2 vCPUs: mean **82.5 percent**, p50 81.6, p95 **97.4**, max 109.2 (5 s windows); load average mean 4.7, max 6.6. Higher than the previous round (78.8 / 93.9 / 102.3). I did not attribute the difference (flower, the acctd watchdog and run-to-run variation are all candidates). The CPU budget is thin; Decision 6 stands |
| 3 | Memory | cgroup limit 4 GiB, **0 OOM kills**, page-cache reclaim at the limit 21,601 times; anonymous 0.56 GB, shared 1.10 GB, kernel 0.24 GB, file cache 3.47 GB. Resident scd 31-36 MB, netd 33-94, acctd 18-43, portald 26-39, edged 27-28 |
| 3 | Database | **0 deadlocks, 0 lock timeouts, 0 FATAL**; 0 ERROR-level lines in any service journal; connections at most 32 (scd 16, acctd 5, netd 7); idle-in-transaction at most 2, lock waiters at most 6. scd pool: 451,705 acquisitions, 15 waited, 2.0 s waited in total. 20 PostgreSQL `ERROR` lines, all `ACCT_SESSION_OUT_OF_SCOPE` for 10 sessions that had ended 13 s earlier (acctd's last sample of each was refused by the database guard: correct, and the totals still reconcile) |
| 3 | Forwarding of the moving 10 percent | **122,400 of 122,400** downloads succeeded (32.1 GB, p50 108 ms) |
| 3 | Accounting totals after the run | raw samples = per-minute = per-hour = per-session = per-entitlement = **112,397,972,573 bytes**, 1,046,075 rows in each tier: nothing lost, nothing counted twice. netd logged 18,021 sessions confirmed enforced (the 17,998, 2 enforced after the guest's wait ran out, 20 data-quota sessions and 1 not attributed) |
| 4 | **Data-quota termination**: two batches of ten 8 MiB sessions under the 300/min load (clients 2400-2409, fresh vouchers, clients reset between batches) | **20 of 20 ended `TERMINATED` / `DATA`**, session bytes equal entitlement bytes in all 20. Billed 9,647,017 to 9,651,377 bytes: **overshoot 1,258,083 to 1,262,769 bytes** (about 5 s of the 256 KB a second each client downloads, the time from crossing to termination under this load). The previous round's run-time batch overshot 157 to 181 KB and its 30-minute rehearsal 0.99 to 1.04 MB, so this is inside the range seen before but not tighter than it. The overshoot is termination latency, not quota arithmetic |
| 4 | `accounting_integrity_summary(tenant, site)` (migration 0181) | **empty** (0 events of any kind, 0 inexact accumulator rows) after the sustained run, both quota batches and the fold below |
| 4 | Raw-retention purge fold on a live quota entitlement | 7.3 |
| 5 | acctd interruptions and service restarts with the 1,700 live | 7.4 |
| 6 | Central disconnect, 22 minutes | 7.5 |
| 7 | Tests | 7.6 |

### 7.3 Purge fold on a live quota entitlement (what I did)

1. Signed client 2400 in with a fresh 8 MiB data-quota voucher and downloaded 3 MiB (consumed 3,310,050 bytes; `entitlement_usage_bytes` 3,310,050; 4 raw samples; minute and hour tiers equal).
2. Because the lab is hours old and the shortest raw retention is one day, I **backdated that one session's raw samples, its minute and hour rows and its binding by three days** (lab data, as the database superuser), then ran the product's `iam_v2.purge_accounting_detail(tenant, site, 1000)` until it returned 0 (it folded the 4 samples).
3. After the purge: raw rows 0; accumulator (`accounting_usage_entitlement`) 3,310,050; `consumed_data_bytes` 3,310,050; `p3_entitlement_data_usage` 3,310,050; `entitlement_usage_bytes` 3,310,050. **Not decreased.**
4. Drove the same session on: it ended `DATA` at 9,927,364 bytes (quota 8,388,608, 1,538,756 over). Had the purge given the 3.3 MB back, the session would have lived to about 11.7 MB. Session bytes equal entitlement bytes.

A first attempt at the same test, on the previous session, backdated the samples **but not the binding**. The fold attributes a sample to the binding in force at the sample's instant, found none (the samples now predated the binding) and `p3_entitlement_data_usage` read 0 after the purge. That is an impossible state (a sample cannot predate the binding that records it) created by my fixture, not by the product; I discarded that session and redid the test with the binding backdated too. It is reported because the figure fell. The 3,310,116 bytes of the discarded session are the whole difference between the raw table and the other tiers in the later reconcile (raw plus both purged sessions equals per-minute, per-hour, per-session and per-entitlement).

### 7.4 acctd interruptions and service restarts (1,700 long sessions live, 60 sign-ins a minute)

Timings are from `evidence/final-release-2026-10-30/acctd-interruptions.txt` and `service-restarts.txt`. The authorisation set also holds about 57 churn sessions (60 s vouchers), so "never below 1,700" is read against the set minimum, and against the database: across all of this and the disconnect test only one long session ended (my deliberate admin disconnect in (d)), so 1,699 remained from then on.

| Event | Time to restored service | Authorisation set | Sign-ins in the event window |
|---|---|---|---|
| (a) `kill -STOP` acctd (hang) | watchdog fired at **+23.1 s**; acctd active again **+25.4 s**; set refilled +28.4 s | min 1,731 (+27 s); no long session lost it | 21 of 41 refused, 20 ok |
| (b) `kill -9` acctd | active again **+2.5 s** | min 1,755 | 19 of 19 ok |
| (c) `systemctl stop` acctd for 240 s | started by hand at +240 s | **empties at +90 s** (the lease is the 90 s baseline, unchanged); 1,700 back **1.9 s** after the start, 1,758 after 7.4 s; sessions stayed `active` in the database | 237 of 264 refused (fail closed), 27 ok around the edges |
| (d) revocation with acctd running | n/a | an admin-disconnected session left the set 2.6 s after the call; ten quota-exhausted sessions left 8.9 to 9.2 s after the database ended them; **none was renewed in the following 100 s** (longer than the lease) | n/a |
| (e1) netd stop 20 s, start | active 0.4 s after the start command | min 1,732 | 20 of 47 refused |
| (e2) PostgreSQL container restart | accepting connections **4.8 s**, queries answered 4.9 s | min 1,748 | 5 of 39 refused |
| (e3) scd stop/start | active **2.2 s**, metrics 2.9 s | min 1,751 | 3 of 25 refused |
| (e4) every service and the database container stopped together, held 10 s, started | database accepting 1.4 s, all five services active **2.1 s** after the start command (outage 18.5 s) | min 1,734 | 21 of 57 not completed (connection errors while portald was down) |

Outside the event windows 1,908 of 1,908 sign-ins succeeded; 0 devices had two live sessions. No attempt was reported successful without enforcement: in the events window netd confirmed 1,855 enforcements against 1,640 reported successes plus 10 quota sessions, the surplus being enforcements that finished after the guest had been told "not online" (a pending session is enforced when acctd or netd comes back; that is the numbers' implication, the individual sessions were not traced).
The ERROR lines in the journals are the interrupted calls themselves (`context canceled`, `scd call` refused while scd was down, `guest method switch unreadable; refusing sign-in` during the database restart).
**Not done:** a reboot of the container host, so netd's boot-time reconcile after a kernel restart was not exercised here; the whole-stack stop and start keeps the kernel state.

### 7.5 Central disconnect (lab Central only; 08:29:21 to 08:51:33 UTC, 22 minutes)

The appliance's output hook dropped 172.30.10.10 on 443 and 9443 (its own nft table; 1,075 packets dropped; probes timed out at 5 s while the cut was on); the Central container was not touched. 150 sign-ins a minute of 60 s vouchers, 10 percent of the long sessions moving data.

| Check | Result |
|---|---|
| Sign-ins during the cut | **3,291 of 3,300 (99.73 percent)** enforced at normal latency (p50 2.68 s, p95 4.25 s, p99 5.11 s); the 9 refusals are all in the minute scd was restarted |
| Existing sessions | the 1,699 long sessions stayed `active` and authorised throughout; 0 duplicate live sessions |
| Licence state | `active`, valid until 2027-01-28, grace until 2027-02-04, 2,500 concurrent guests, throughout (Hotel Admin `/central/status`, read every few minutes). The Central link reads `unreachable` ("OneGate Central did not answer in time"); `cloud_stale` stayed false |
| Logs | `assignment: fetch failed` (43), `hello: call failed` (5, one every 5 minutes), `appliancecert: reconcile failed` (5), `licstate: cloud license fetch failed (offline-safe)` (1); no sign-in path error. The hello failures are timeouts, never a definitive "unknown appliance" reply, so the orphan self-reset does not trigger (`cmd/scd/main.go`: only an HTTP 401 saying "not enrolled", "unknown appliance" or "not found" counts) |
| Restarts during the cut | scd at +300 s (active in 3.2 s), acctd at +600 s (0.4 s), netd at +840 s (2.0 s). Each started and kept admitting guests from the licence on disk (`licstate: license evaluated state=Active ... cloud_stale=false` right after the scd start). Sign-ins in the 40 s around each: 9 of 111 refused in the scd window, 0 of 113 for acctd, 0 of 109 for netd |
| Restore (08:51:33) | `/central/status` read **connected** within 31 s (the 60 s hello cadence), last contact then moving every minute; the same 1,699 sessions, 0 duplicates, no lost session |
| Observation | right after the scd restart `last_contact_at` moved to the restart instant (08:34:34) although the network was cut: `assignment: waiting for the mTLS client certificate` is recorded as a successful conversation (`ErrCertPending`, `cmd/scd/assignment.go`). The link state read `unreachable` again once a later call failed, so the display was wrong for seconds. Not changed (a status-semantics decision) |

**Licence grace boundary: code-proven, not lab-proven** (no licence expiry was touched and 30 days were not waited). `license.Evaluate` (`license/verify.go`): Active until `valid_until`, GracePeriod until `valid_until + grace_period_days` (7 days on the lab licence), Restricted or Expired after.
`AllowsNewSessions()` is true in Active, GracePeriod and Restricted and false in Expired, Suspended, Revoked and Unlicensed (`license/doc.go`). **Central silence alone never changes the state**: `cloud_stale` (more than `offline_grace_days` without a cloud validation) is a warning flag that "does not interrupt a valid license". The lab proves 22 minutes of silence with the licence on disk; the boundary behaviour is proven by the code above and `license/*_test.go`.

### 7.6 Tests (Linux, golang:1.26.10, worktree at `dbf86d28`)

* Unit and service packages `cmd/netd cmd/scd cmd/acctd cmd/edged cmd/portald internal/{enforce,shape,nft,poststay,throttle,signinattempt,metrics,sdwatchdog,iamv2,netcfg,writerguard,licstate,grace,sysnet}`: **all pass** (`writerguard` has no non-integration tests).
* Real PostgreSQL 16 / TimescaleDB 2.16.1 with the full appliance schema, fresh database, `-tags integration -p 1 -count=1`: `cmd/scd` 415 pass / 18 skip; `internal/poststay` and `cmd/scd` with `-tags "integration phase5"` (AD-36 closure, migration 0190 limits) 393 pass / 66 skip; `internal/enforce` (accounting integrity 0181, usage after raw purge), `internal/writerguard` (janitor 0192) and `cmd/acctd`: all pass (163 passed in that run).
* **One failure, reported:** in the first combined integration run (`enforce`, `writerguard`, `poststay`, `scd`, `acctd` in sequence on one database) `cmd/scd` `TestActivationRefusedWithoutARegisteredAccountingOrigin` failed (`no pending session to activate`). It passes alone and passes in the whole `cmd/scd` package on a fresh database, and a later run on the same polluted database logged `no free 10.77.x fixture subnet remains in this database`: the packages share a database and leave state behind. I read this as test isolation, not a product defect, and did not investigate further.
* The skips are tests that need an external fixture (`LEDGER_DRILL_DSN`, a second database) that was not provided.

### 7.7 Simulated versus not

Simulated: the clients (network namespaces with real DHCP, the real portal and the real forwarding path, one generator, no radios), the sink as "the internet", the cut (an nft drop, not a failed WAN), the appliance (a 2 vCPU / 4 GB cgroup on a shared kernel and shared disk, not the reference hardware); the Central is a genuine Central but a lab one on the same Docker host.
Not run: a host reboot and boot-time reconcile; the u32 fallback in the lab; a licence crossing its expiry or grace boundary; physical devices; real PMS post-stay traffic; the production Central and the test appliance. The numbers describe the mechanism and compare with the previous round; they are not a service level on hardware.
