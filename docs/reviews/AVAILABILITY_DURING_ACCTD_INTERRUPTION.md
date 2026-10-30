# Availability during an acctd interruption: why the 90 s lease is unchanged, and what makes recovery fast (2026-10-30)

Question put to the closure round: guests lost access about 90 seconds after acctd stopped (section 2.3 of
`runbooks/CAPACITY_AND_OFFLINE_ACCEPTANCE.md`). Should that be longer, or should something else keep them online?

Answer: **the lease stays at 90 seconds, and nothing in the appliance keeps the authorisation set alive without acctd.** The availability
goal is met by making an acctd interruption short instead: a crash is back in 2.5 s, a hang in about 25 s (measured below), both far
inside the lease, so guests do not notice either. Only an acctd that stays down for more than 90 s costs guests their access, and that is
deliberate. Measurements: final release `dbf86d288f65`, lab described in `runbooks/CAPACITY_AND_OFFLINE_ACCEPTANCE.md` section 7;
raw timings in `runbooks/evidence/final-release-2026-10-30/acctd-interruptions.txt`.

## 1. Why the lease cannot be lengthened or bridged without acctd

The kernel authorisation set (`phase3_auth_ipv4`, one element per online guest) is the enforcement. netd writes an element with a 90 s
timeout (`phase3LeaseTTL`, `cmd/netd/phase3_lease.go`) and re-writes it every pass from the plan acctd derives from the database
and submits. An element that is not re-written lapses. That is the fail-closed property: **the only thing that keeps a guest online is a fresh
statement from the database, made by acctd, that the guest is still entitled**.

What the database knows only through acctd:

* **Revocation.** An operator ends a session (disconnect, checkout, device removal, licence deletion). The database row changes; the kernel
  learns of it because the next plan omits the guest and the lease is not renewed. Measured with acctd running: an admin-disconnected session
  left the set 2.6 s after the call and was not renewed in the following 100 s.
* **Quota exhaustion.** Byte counters are read, rolled into the entitlement, compared with the quota, and the session is ended, all by acctd.
  Ten 8 MiB sessions that crossed their quota left the set about 9 s after the database ended them and were not renewed in 100 s.
  With acctd down nothing counts bytes, so a guest whose quota is exhausted in the outage would keep their access for as long as the lease
  was bridged: a longer lease or a "keep the last plan" mode would hand out unmetered traffic for the length of the bridge.
* **Licence and capacity changes** (a licence suspended, expired or reduced; `max_concurrent_online_guests`) reach the plan through the same derivation.
* **Time-limited sessions** end when acctd's expiry sweep says so (the lab's 60-second sessions are removed by it).

Any scheme that lets netd keep authorisations without acctd either (a) keeps revoked or quota-exhausted guests online, or (b) re-implements
acctd's derivation inside netd (a second source of truth for who may be online). Both are product decisions with a security cost, not
availability fixes, and neither was taken. The lease also bounds a worse failure: if netd or the host is wedged, the worst case is 90 s of stale access, never more.

## 2. What makes recovery fast (all in the shipped code)

| Mechanism | Where | Effect |
|---|---|---|
| `Restart=always`, `RestartSec=2`, `StartLimitIntervalSec=0` | `deploy/systemd/stayconnect-acctd.service` | A crash or `kill -9` is restarted 2 s later, forever. |
| `WatchdogSec=30`, `NotifyAccess=main` | same unit | A hung acctd is killed and restarted by systemd. |
| Liveness ping only while the plan submitter is turning | `internal/sdwatchdog`, `cmd/acctd/loops.go` | The ping (every WatchdogSec/3 = 10 s) is withheld the moment the plan submitter stops making progress, so a stall that is not a crash still ends in a restart. |
| 15 s submission deadline | `submissionDeadline` in `cmd/acctd/loops.go` | One slow database call cannot hold the plan loop for longer than 15 s; the watchdog sees a stuck loop within `WatchdogSec`. |
| Event-driven admission and renewal on start | `cmd/acctd/pending_listener.go`, plan submitter | A restarted acctd submits a plan at once; netd refills the set on its next pass (seconds). |
| No start-up handshake | `Type=simple` | A start is not delayed by `READY=1` logic. |

Worst case for a hang: up to 30 s of silence before the watchdog fires (the last ping can be up to 10 s old when the stall begins, so detection is
20 to 30 s after it), then 2 s restart and a few seconds to refill: about 35 s, 55 s under the 90 s lease.

## 3. Measured (lab, release `dbf86d288f65`, 1,700 long sessions live, about 57 churn sessions, 60 sign-ins a minute)

| Interruption | Time to acctd running again | Authorisation set | Guests |
|---|---|---|---|
| `kill -STOP` (hang) | watchdog fired **23.1 s** after the stop (`Watchdog timeout (limit 30s)`, SIGABRT, `Failed with result 'watchdog'`), acctd active again **25.4 s** after | lowest 1,731 at +27 s (1,700 long sessions plus churn sessions whose 60 s ended meanwhile), back to 1,758 at +28.4 s | no long session lost authorisation |
| `kill -9` | active again **2.5 s** after | lowest 1,755, back to 1,758 at +2.6 s | not affected |
| `systemctl stop` held 4 minutes | started by hand after 240 s | element count decays and reaches **0 at +90 s** (the lease), 1,700 back **1.9 s** after the start, 1,758 after 7.4 s | offline from about +90 s to +242 s; sessions stayed `active` in the database; sign-ins during the outage were refused (not reported as success): 237 of 264 attempts |
| revocation, acctd running | n/a | admin-disconnected session left the set 2.6 s after the call; not renewed in 100 s | removed as intended |
| quota exhaustion, acctd running | n/a | ten sessions left 8.9 to 9.2 s after the database ended them; not renewed in 100 s | removed as intended |

Sign-ins around the hang and the crash: during the 30 s after `kill -STOP` 21 of 41 attempts were refused ("could not bring your device online"; none was reported as success); during
`kill -9` 19 of 19 succeeded. Outside every event window 1,908 of 1,908 succeeded.

## 4. What is and is not claimed

* The 90 s lease is **not** weakened or lengthened. A guest loses access when acctd has been unable to renew for 90 s: **a hang or crash is recovered inside that
  bound in every measured case (2.5 s and 25.4 s)**. An acctd that cannot start (a database that stays down, a broken binary) still ends access at 90 s. That is the design.
* The hang test used `SIGSTOP`, which stops every thread, including the watchdog goroutine. A hang in which the process keeps sending pings but the plan loop is stuck is
  covered by the liveness predicate (the ping is withheld), exercised in `cmd/acctd/loops_watchdog_test.go` and `internal/sdwatchdog`, not by a second lab case.
* If the Product Owner wants a **longer outage tolerance for the case that acctd itself cannot be recovered** (for example during a planned database restore), the safe shape
  is an explicit, operator-started, time-boxed maintenance mode that records who started it and ends at a deadline; it would still not apply to quota or revocation. Not built; a product decision.
* Lab, not hardware: `systemd` ran in a privileged container on one Docker host. The numbers describe the mechanism, not a measured service level on the reference appliance.
