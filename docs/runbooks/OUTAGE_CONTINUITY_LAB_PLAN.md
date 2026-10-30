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
