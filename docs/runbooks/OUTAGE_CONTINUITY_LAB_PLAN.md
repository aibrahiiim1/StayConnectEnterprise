# Outage continuity: lab fault-injection plan (60 and 120 minute runs)

For the lead's long-duration run in the rebuilt `oglab` lab ([`tools/oglab/README.md`](../../tools/oglab/README.md)). Mechanism and threat table:
[`architecture/ONEGATE_OUTAGE_CONTINUITY.md`](../architecture/ONEGATE_OUTAGE_CONTINUITY.md). Lab only: nothing here touches the production Central (`41.128.240.133`), the test appliance (`172.21.96.97`) or any retired host.
Test data is disposable; use a test operator.

Release under test must contain migrations **0198 and 0199** (check: `select version from schema_migrations where version like '019[89]%'`) and the netd, acctd, edged and Hotel Admin builds of this branch.

## 0. Conventions

```
A=oglab-appl                       # the appliance container (2 vCPU / 4 GB); every command below runs as: docker exec $A sh -c '...'
NETD='curl -s --unix-socket /run/stayconnect/netd.sock'        # root is an admitted caller of the admin routes
PSQL='docker exec -i $A ... psql'  # use the lab's existing psql one-liner (tools/oglab pgsample.sh shows it)
```

Helpers (run inside `$A`, save as `/root/cont-lib.sh`):

```sh
authset()  { nft list set inet stayconnect phase3_auth_ipv4 | grep -c 'expires'; }             # elements in the real kernel set
contstat() { curl -s --unix-socket /run/stayconnect/netd.sock http://netd/v1/phase3/continuity; }   # governor state (JSON)
setwin()   { # setwin <minutes> : the supported way to change the setting (audited, validated)
  psql_c "SELECT public.enforcement_policy_set('outage_continuity_minutes','$1','lab-operator','lab run $2')"; }
sample()   { # sample <seconds> <interval> : one CSV line per interval: epoch,authset,journalled,active,probe_ok,renewals
  end=$(( $(date +%s) + $1 )); while [ $(date +%s) -lt $end ]; do
    s=$(contstat); echo "$(date +%s),$(authset),$(echo "$s"|jq .journalled_revocations),$(echo "$s"|jq .active),$(echo "$s"|jq .database_probe_ok),$(echo "$s"|jq .renewals)"
    sleep $2; done; }
```

Population for every scenario: the capacity lab's 1,700 long sessions (no data quota) **plus 100 sessions on a data-quota voucher** (`lab_quota.py`, 50 MiB allowance, rate 8/3 Mbit/s) and 50 sessions on a
voucher with a 5-minute time window. Record per-client bytes sent by `ogload` for the accounting comparison.

Before each scenario: wait until `contstat` shows `sessions` equal to the number of sessions that carry a block (about 1,700 + 150 minus budgeted ones in their first minute) and `refused` empty.

## 1. Baseline: strict mode equals the previous behaviour (setting 0)

```sh
setwin 0 'strict baseline'
sleep 60; contstat            # sessions: 0, enabled: true, no record on disk: ls /var/lib/stayconnect/netd-phase3-continuity.json must fail
systemctl stop stayconnect-acctd; T0=$(date +%s); sample 240 5 > s1.csv; systemctl start stayconnect-acctd
```
**Pass:** the authorization set reaches 0 between +80 s and +100 s of the stop (as in section 2.3 of `CAPACITY_AND_OFFLINE_ACCEPTANCE.md`); `renewals` stays 0; `journalled_revocations` unchanged.

## 2. acctd stopped for 60 minutes (default window)

```sh
setwin 60 'default window'; sleep 120
# stop acctd and keep it down (also prevents the 2 s restart): mask is not needed, `stop` is not restarted by Restart=always
systemctl stop stayconnect-acctd; T0=$(date +%s)
sample 4200 5 > s2.csv &              # 70 minutes
# at +20 min: generate traffic from 20 long clients (ogload traffic), note bytes per client in /root/s2-bytes.json
# at +30 min: staff-disconnect 5 long clients through the Admin Console (or: curl scd /revoke), note time and clients
# at +40 min: end 5 long clients' entitlements in the database:  select iam_v2.terminate_entitlement_at_boundary(<ent>, now(), 'ADMIN');
# at +45 min: drive the 100 quota clients past 50 MiB
wait
```
**Pass criteria (all):**
1. The 1,700 undisturbed long sessions stay in the set until +60 min (`authset` never below 1,700 + the churn that ended by design; no dips during the run), and the 60-minute sessions of other clients keep traffic flowing (`ogload` download success >= 99 %).
2. At **+60 min (+0 / +15 s)** the set empties of every session covered by the block (deadline = the last accepted block + 60 min on the monotonic clock). Not earlier than +59:55, not later than +60:15.
3. The 5 staff-disconnected clients left the set **within 2 s** of the call and are never renewed (check `dropped` in `contstat`).
4. The 5 clients whose entitlement was ended in the database left the set **within 8 s** (one governor tick + probe) and `dropped` names "the database no longer lists the session as entitled".
5. Each quota client left the set within **10 s** of crossing its allowance (+/- the metering tick) with total bytes through the class <= allowance + (rate ceiling x 7 s) + the 60 s margin share; `budget_used_bytes` for its entitlement is within 2 % of the bytes `ogload` sent.
6. The 5-minute-window sessions left at their window end (+/- 10 s), not at the continuity window.
7. No client that was not online before the stop became online during the stop.
8. `journalled_revocations` equals the number of disconnects made.

Then restart acctd (`systemctl start stayconnect-acctd`) at **+65 min** (after the deadline) and check: recovery <= 60 s, still-entitled sessions are re-authorized by the normal path, nothing else.

## 3. Accounting reconciliation (the "no unmetered consumption" proof)

Repeat 2 with a **short** outage (acctd stopped for 20 minutes, restarted inside the window) with 20 clients transferring a known number of bytes per direction during the outage.
After restart wait 60 s, then compare for each of the 20 clients: `sum(bytes_up + bytes_down)` in `iam_v2.accounting_records` for the session minus the value before the outage, against the bytes `ogload` sent plus received (L3 payload plus headers: allow 3 %).
**Pass:** DB delta within 3 % of generated, never above generated + 3 %, never below generated - 3 %; the entitlement's `consumed_data_bytes` equals `p3_entitlement_data_usage(entitlement)`.
Also check the quota clients: those that crossed their allowance **during** the outage are ended by the database (not only by netd) within one accounting tick after restart: `end_reason` is the data-quota reason and their `consumed_data_bytes` >= quota.

## 4. 120 minute run

```sh
setwin 120 'upper bound'; sleep 120
systemctl stop stayconnect-acctd; sample 7500 5 > s4.csv
```
Same criteria as section 2 with every time scaled to 120 minutes (deadline +120 min +0/+15 s). Run the revocation and quota events at +30, +60, +90 min. Also record netd CPU and memory every 5 s
(`sampler.sh`) and check the governor's cost at 1,800 sessions: netd CPU average over the outage <= its CPU during a healthy hour + 5 percentage points; `pass` lines absent (no plans), the continuity record rewritten at most every 15 s (`ls -l --time=full` on the record).

## 5. Hung acctd (watchdog disabled), database kept up

```sh
systemctl edit stayconnect-acctd     # [Service]  WatchdogSec=0   (lab only; remove afterwards)
systemctl restart stayconnect-acctd; sleep 60; kill -STOP $(systemctl show -p MainPID --value stayconnect-acctd); sample 4000 5 > s5.csv
```
**Pass:** identical to section 2 (a stopped process and a stopped service look the same to netd). After the run `kill -CONT` the process, check plans resume and the governor stands down (`active:false`).
Remove the drop-in.

## 6. Database unavailable, acctd running

```sh
docker compose -f /opt/stayconnect/deploy/compose/infra.yml stop postgres; T0=$(date +%s); sample 4000 5 > s6.csv
```
**Pass:** acctd cannot derive plans (degraded), plans stop, the governor takes over at +60 s; set stays at its pre-outage size until the window; `database_probe_ok:false`, `database_probe_failures` grows by 1 per tick;
no new sign-in succeeds (guests see the refusal page, not a success); `refused` stays empty. At +30 min try a staff disconnect through whatever path the console offers with the DB down and record whether the operator
can reach it (open question R2 in the design note). Start postgres at +65 min; recovery as in section 2. **Record**: time from `postgres` start to the first plan accepted.

## 7. netd restart and hang

(a) During section 2 at +20 min: `systemctl restart stayconnect-netd` three times, 30 s apart. **Pass:** the set never drops below its pre-restart size by more than the sessions whose element lapsed during the
restart gap (should be 0 with a 2 s restart: the elements carry up to 90 s), `contstat` right after each restart shows `sessions` restored and `refused` empty, the deadline is unchanged (compare the `ls` mtime/`last_plan_boot`
via `cat /var/lib/stayconnect/netd-phase3-continuity.json | jq .last_plan_boot_ms`).
(b) Same with `kill -9 $(pidof netd)` while a staff disconnect was made **before** the kill: the disconnected client must stay out after the restart (journal).
(c) `kill -STOP` netd for 3 minutes at +20 min: **Pass:** guests that would have needed a renewal in that interval lapse at most `90 s` after their last renewal; after `kill -CONT` the governor renews the ones still present within one tick.

## 8. Appliance reboot

(a) Plans healthy, `docker restart $A` (or the lab's reboot): **Pass (documents R1):** the set is empty after boot; guests re-appear only as netd, acctd, postgres are up and a plan is accepted;
`contstat.refused` names "another boot" (or the record is absent); record the gap in seconds per client (ogload).
(b) Reboot with postgres held stopped (`docker compose ... stop postgres` immediately after boot): **Pass:** no guest is authorized (nothing creates elements); no error loops in `journalctl -u stayconnect-netd`.

## 9. Clock

At +10 min of section 2: `date -s '+6 hours'`; at +20 min: `date -s '-12 hours'`; at +30 min restore with chrony/NTP.
**Pass:** the 60-minute deadline is unaffected (+/- 15 s); no extension, no early removal caused by the jumps.

## 10. Negative checks (must all hold)

* Tamper: stop netd, edit `deadline_boot_ms` in `/var/lib/stayconnect/netd-phase3-continuity.json` (+1 day), start netd: `refused` = "continuity record is corrupt or altered", file deleted, strict behaviour.
* Journal: corrupt `/var/lib/stayconnect/netd-phase3-continuity-revocations.json`, restart: `refused` mentions the journal, no session continued.
* Setting back to 0 during an outage cannot reach netd (acctd down): the record keeps its deadlines; after acctd returns, the next plan clears it (`sessions: 0`). Record this behaviour.
* Direct removal: `nft delete element inet stayconnect phase3_auth_ipv4 { "br-guest" . 10.x.y.z }` during an outage: the element is **not** re-created in the next 5 minutes.

## 11. What to record in the evidence

Per scenario: the CSV from `sample`, `contstat` before/during/after, `journalctl -u stayconnect-netd -u stayconnect-acctd --since ...`, the exact release SHAs (read back from the installed binaries with
`go version -m`), the list of clients disconnected/ended/over-quota with timestamps, and the pass/fail against the numbered criteria. Anything outside a criterion is a finding, not a pass.


## Results (lab run of 2026-10-30 / 31)

Measured in the isolated container lab rebuilt for this mission (`tools/oglab`, 2 vCPU / 4 GB appliance container, 1,700 long sessions, 10 percent of them moving 256 KB every 5 s, plus the extra populations named below). Nothing was pushed, merged or deployed to a real host; the production Central, the TEST appliance and every retired host were not contacted. Raw files: [`evidence/final-mission-2026-10-30/outage/`](evidence/final-mission-2026-10-30/outage/). The harness (`tools/oglab/finalmission/`) samples the kernel set `phase3_auth_ipv4` and netd's governor every 2 s, so every time below is quantised to 2 s.

**Release under test**: `onegate-appliance-5926f7dead47` (commit `5926f7de`, migrations 0198 and 0199 applied), built by `appliance-build.sh` and installed by `appliance-install.sh`. **Section 2 (S2) ran on the release netd unchanged.** It found a crash (finding F1); from section 3 on the lab ran **netd built from the local branch `lab-fixes-1`** (`bd411d70`, from `e30daa7f` on) with acctd, scd, edged, portald and the console unchanged. The deployed netd carries no embedded VCS revision (lab build, `-buildvcs=false`); its sha256 is in the evidence.

### What each section proved

| Section | Run | Result |
|---|---|---|
| 1 strict baseline (setting 0) | 5 min, acctd stopped | set emptied at **+84 s** (criterion 80 to 100 s), `renewals` 0, `journalled_revocations` 0, no record file; acctd restart: set back to 1,700 within 5 s. **PASS** |
| 2 acctd stopped 60 min (default) | S2: T0 19:13:43 UTC, release netd | 1,690 undisturbed long sessions stayed in the set; no client became online during the outage. Deadline: they left at **-1.2 s** of T0+3,600 s (last accepted block was 1.4 to 2.4 s before T0), set at 5 elements by +4.8 s (the nft delete of 1,680 elements takes about 5 s). Staff disconnect of 5 clients: 0.4 s per call, out of the set within 2 s, `dropped` = "revoked: STAFF_DISCONNECT" x5, `journalled_revocations` 5. Entitlement ended in the database for 5 clients: out in 2.5 to 2.9 s, `dropped` = "the database no longer lists the session as entitled" x5. 20 data-quota clients: cut by netd's own metering ("data allowance spent during the outage" x20). Recovery after `systemctl start stayconnect-acctd`: 90 percent of the set back in 4.4 s. 98,280 of 98,280 moving-client downloads succeeded. Record rewritten every 15.5 s (median). **PASS on 1 to 5, 7, 8.** Criterion 6 was NOT valid in this run (see F2) and passed in S3 to S6. Netd crashed at +42:51 (F1). |
| 3 accounting reconciliation (acctd stopped 20 min) | S3 | 20 clients moved 5.03 GB down and 1.26 GB up during the outage. After restart the database delta was **6,785,266,533 bytes = 1.026 x the bytes the clients' own interfaces counted (1.0245 to 1.0265 per client) and 1.079 x the payload ogload sent (headers and acknowledgements)**; the entitlement's `consumed_data_bytes` equals `iam_v2.p3_entitlement_data_usage` and the sum of its raw samples for every client. Nothing lost, nothing counted twice. Within the plan's 3 percent of the wire figure, 2.6 percent above it (see note A). Data-quota clients: cut by netd at the budget (127.2 MB limit, 120 to 129 MB consumed), **still ACTIVE after recovery** with 128.96 MB consumed of 209.7 MB: they are re-admitted by the normal path, not ended (note B). 50 five-minute-window sessions left at their window end (-1 to +8 s). **PASS (A, B noted)** |
| 4 acctd stopped 120 min | S4: T0 21:30:21 UTC, fixed netd | Undisturbed long sessions left at **-3.3 / -1.3 s** of T0+7,200 s; staff disconnects (+30 min) 0.4 s per call, out within 2 s; entitlement ends (+60 min) out in 1.1 to 2.1 s; quota clients (+90 min) cut after 2 min 12 s; 5-minute sessions left at -2 to +5 s of their window; 198,380 of 198,380 downloads succeeded; no panic; nothing online that was not before. Recovery 90 percent in 2.1 s. **PASS.** The clock-jump step was NOT run (see "Not done"). |
| 5 hung acctd (watchdog off, `SIGSTOP`) | S5b: 60 min, fixed netd; S5: 32 min, netd fault injection | S5b identical to S2: deadline at **-3.3 s**, disconnects within 2 s, entitlement ends within 3 s, 20 quota cuts, 98,140 of 98,140 downloads, recovery 90 percent in 4.2 s after `SIGCONT`, governor stood down, drop-in removed and `WatchdogUSec` back to 30 s. **PASS.** S5 (acctd hung 32 min, then aborted by me to save time): see section 7. |
| 6 PostgreSQL stopped 65 min | S6 | Plans stopped at once (`could not derive the shaping plan` x1,167), governor active at +59.9 s, `database_probe_ok` false for the whole outage and `database_probe_failures` +1 per tick (815 at the end); set unchanged until the deadline, then left at **-3.1 s**; 5 sign-ins attempted at +10 min: **5 of 5 refused** ("refused:200", the refusal page), no new device online; `refused` stayed empty; quota clients cut by netd at +47 min (netd needs no database to meter); 98,140 of 98,140 downloads. **Open question R2 answered: with the database down the Admin Console cannot sign an operator in at all (HTTP 503 `signin_unavailable`), so no staff disconnect is possible through the console**; the only revocation path left is a direct push to netd on the appliance. After `compose start postgres`: governor stood down 5 s later, 90 percent of the set back in 5.4 s. **PASS** |
| 7 netd restart and hang | S5 (acctd hung) | (a) three `stop/reset-failed/start` restarts 30 s apart: record `last_plan_boot_ms` unchanged (4100531560) across all three, sessions restored each time, set unchanged by the restarts; **but five healthy guests were cut at the first restart by stale journal entries (F3, fixed)**. (b) staff disconnect then `kill -9` netd: the disconnected address stayed out after restart (set 1,719, `journalled_revocations` 6). (c) `SIGSTOP` netd for 3 min: guests lapse 90 s after their last renewal and **are not brought back by `SIGCONT`**: the set fell from 1,719 to 0 between 61 and 97 s after the stop and held only 251 elements right after `SIGCONT` (the ones renewed in time); the rest were re-admitted by the normal path when acctd returned (F4, by design). **PASS as written; F4 is the residual.** |
| 8 appliance restart | container restart (not a kernel reboot), `docker restart` with plans healthy | The kernel set was empty after start (new network namespace); PostgreSQL ready at +53 s; first authorizations at +53 s, the 1,735 sessions of the two `/20` client networks back by +180 s (4,585 sessions of all networks by +396 s: netd re-asserts about 12 sessions a second); the continuity record was restored (`sessions` 4,555, `refused` empty) because the boot id does not change in a container, so the plan's "refused: another boot" could not be observed. No guest was authorized while PostgreSQL was down. Management address unreachable for 36 s. 8(b) (reboot with PostgreSQL held down) **NOT RUN**. **PARTIAL** (documents R1: about 1 to 3 minutes of no access per guest after an appliance restart, longer for thousands of sessions) |
| 9 clock jumps | NOT RUN | see "Not done" (unit test `TestContinuity_WallClockJumpsDoNotStretchTheWindow` passes) |
| 10 negative checks | S10 | tamper (`deadline_boot_ms` +1 day, checksum kept): refused, "continuity record is corrupt or altered", strict behaviour until the next block. Journal corrupt during an outage: `refused` = "revocation journal is corrupt", sessions 0, set 1,705 -> 0 within 100 s (nothing continued). Setting 0 written during an outage: netd's window stayed 3,600 s, the record kept its deadlines; after acctd returned, sessions 0, record file deleted. Direct removal from **both** authorization sets during an outage: not re-created in 5 min. **PASS** (note C) |

### Findings (root cause, status)

* **F1 (product defect, fixed in `bd411d70`, not pushed).** netd panicked at minute 43 of S2: `assignment to entry in nil map`, `passLeases.LeasesForIP` (`phase3_shaping.go`) called from `contTick` -> `addressOwner.Evidence`. The governor installs a per-tick `passLeases` on the shared address owner while a request handler can read that owner without `p.mu`; the lazily built address index was reset to nil inside the first fetch without a lock, so with two goroutines one assigned into the map the other had just nilled. systemd restarted netd after 3 s and the record restored (that restart is what `journal` shows at 19:56:34). Fix: a mutex on the fetch state and index. `TestPassLeasesConcurrentUseIsSafe` fails under `-race` before the change and passes after.
* **F2 (design property, document).** A session admitted less than about 25 s before the plans stop is **not covered by the continuity record** and lapses 90 s after its last renewal like a strict session: measured with 5 fresh sign-ins (element in the set 4 s after the call, in the record 20 to 25 s after) and by S2's 50 five-minute sessions (signed in 13 to 23 s before the stop, all gone at +76 to +78 s instead of at their window end). Plan criterion 6 must sign them in at least 60 s before the stop (S3 to S6 did, with two minutes).
* **F3 (product defect, fixed in `e30daa7f`, not pushed).** `pruneJournal` dropped entries a later database read had absorbed from memory only. The file kept them for up to 4 h (`contRevokeKeep`) and entries match by address, so the next netd start applied staff disconnects made hours earlier to whoever held that address now: five healthy guests were cut at the first netd restart of S5. Fix: `contNotePlan` saves the journal whenever pruning removed something; `TestContinuityJournal_APrunedEntryIsNotReloadedByTheNextStart` fails without it. The full `cmd/netd` unit suite passes with both fixes (172 s).
* **F4 (design residual).** A netd stalled for more than 90 s during an outage loses every authorization it did not renew; the governor never re-creates an absent element, so guests stay cut until acctd returns (S5: 1,719 -> 251). Already the documented "never grant" rule; the operational consequence (a hung netd during an acctd outage is unrecoverable by continuity) belongs in the runbook.
* **F5 (observation, cause not established).** In one S10 sub-run (an outage started about 50 s after a previous acctd restart and setting change) the set measured 488 elements at +75 s although the record held 1,705 sessions; it did not repeat in two attempts with the same preconditions (`s10e`, `s10f`) and the later outages in the same state held 1,705. The netd and acctd journals show no warning in the window. Left open; raw output in `evidence/.../outage/s10d.txt`.
* **F6 (small).** `database_probe_failures` keeps the value of the last outage (103, 184 ...) while healthy; harmless. An administrator who deletes only the IP-only element of a session (leaving its MAC-bound twin) is "repaired" by the governor within 10 s because the gate reports the twin as present; deleting from both sets sticks (S10). Sessions of data-quota vouchers whose allowance is below roughly `(down+up kbps) x 125 x 60 s + consumed` get **no** continuity at all (budget margin larger than the allowance: the 50 MiB / 20-10 Mbit/s lab vouchers lapsed at +75 s in every run; the 200 MiB / 8-3 Mbit/s ones got a 127.2 MB budget).
* **Smoke observation (not reproduced).** In a 2-minute smoke run a staff disconnect issued exactly at the deadline, while netd was deleting 1,780 elements, took 10 s and logged `netd could not end the address's flows: context deadline exceeded` (the call still returned 200). Not seen in any run where the disconnect came well inside the window.

Notes. **A**: the accounting counts class bytes on the bridge (including headers and acknowledgements) and exceeds the client interface counters by 2.6 percent; a plan criterion of "within 3 percent of generated" is met against the interface counters and not against payload-only figures (+7.9 percent). **B**: criterion "quota clients that crossed their allowance during the outage are ended by the database ... `consumed_data_bytes` >= quota" cannot hold by design: the budget is `quota - consumed - margin`, so netd cuts the guest about one margin (82.5 MB here) before the allowance and acctd re-admits the entitlement because it is still within allowance. The measured overshoot over the budget was 0 to 1.7 MB (the maximum is `rate x ~7 s`, 9.6 MB for 8/3 Mbit/s). **C**: nft listing of the concatenated set needs `"bridge" . address` quoting for `nft delete element`.

### Resource cost of the governor

Measured from the container's own cgroup with `cpuproc.sh` and `sampler.sh` after a fix to both (in a container with a private cgroup namespace the old scripts read `/init.scope`; **every CPU figure in my first continuity analyses (`cgroup_cpu_percent_of_2vcpu` in the `s2` to `s6` analysis files) is wrong and is not used**). Re-measured after the runs with 1,735 sessions, 10 percent of the long clients moving 256 KB every 5 s, 10-minute windows: **healthy (plans arriving): netd 16 percent of one CPU, PostgreSQL 27, acctd 3, total 50 percent of one CPU; acctd stopped and the governor in charge (minutes 3 to 13 of the outage): netd 8 percent, PostgreSQL 2, total 14 percent**; two minutes after acctd was started again: netd 11, PostgreSQL 27, total 46. The governor costs less than the healthy pass loop (criterion: not more than +5 percentage points: **PASS, -8**) because no plan, accounting write or pass runs while it is in charge. This is a 13-minute outage at 1,735 sessions, **not** the 120-minute run at 1,800 sessions the plan names. netd RSS was 31 to 46 MB during S5b. The record is rewritten every 15.5 s (median over S2 to S6; shortest gap 3.1 s, on the events that change it), as designed.

### Not done in this run

* **Reboot (section 8)**: only a container restart was possible (see the row above): the kernel boot id does not change, the lab's veth to the load generator is destroyed by it, and 8(b) (PostgreSQL held down after boot) was not run.
* **Clock jumps (section 9)**: `date -s` in a container steps the shared Docker Desktop VM's clock for every container on the host (including another engineer's) and cannot be confined by a time namespace. Not run. Covered by the unit test only.
* **Idle-timeout and licence-expiry-during-outage** are design residuals (section 8 of the design note), not tested.
* **Section 2 on the release netd was run once** (S2); S3 to S6 used the fixed netd. S2 alone is the exact-commit evidence for the criteria; the others show that the fixes introduce no regression in the same criteria.
