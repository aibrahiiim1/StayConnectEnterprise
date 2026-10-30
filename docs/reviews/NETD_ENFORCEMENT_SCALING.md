# netd enforcement pass: scaling defect and fix

Status: fix implemented on branch `worktree-agent-a35d8e49babdc6cf2` (base `2985a88e`, tip of `delivery/capacity-acceptance`), verified against a simulated and a real kernel.
Not pushed, not deployed, not merged. Scope: one defect; no schema, migration, API or plan-contract change.

## 1. The defect

In the capacity lab (one appliance at 2 vCPU / 4 GB, Kea, 2,000 simulated clients, portal voucher sign-ins at 300 per minute) enforcement stopped converging at about 380 active sessions:
1,326 sessions stayed `PENDING_ENFORCEMENT`, some for more than 30 minutes, while 377 were active. netd logged `phase3 shaping applied with problems` with `failed:1408`; the first problem was
`prepare <session>: prepare download class on br-g-...: tc [class add ...]: context deadline exceeded` (earlier `authorize <session>: nft list phase3_auth_ipv4: context deadline exceeded`).
`strace -e execve` over 12 s showed 195 execs: 105 `tc class change`, 42 `nft -j list set ... phase3_auth_ipv4`, 40 `nft delete element`, plus a few others; about 19 `session enforcement confirmed ... ALREADY_ACTIVE` per second (one database round trip per active session per pass).

## 2. Root cause

The cost of a pass is the number of forked `tc` and `nft` processes (10-25 ms each), and that number was O(N) per pass with an O(N) constant on the worst of them, so a pass over N sessions was O(N^2) work with no priority between kinds of work.
File and line references are to the base commit `2985a88e`.

1. **An unchanged active session cost two forks and one database call every pass.** `phase3_provision.go:181` re-rated the class (`tc class change`, once per device) to values already installed, and `:189` called `proveActive`, i.e. `iam_v2.activate_session_enforcement` (`phase3_enforcement.go:112`), which answered `ALREADY_ACTIVE`.
2. **Every nft element operation listed the whole set.** `nft.go:130` (`LeaseIn` -> `AuthorizedIn` -> `ListIn`), `nft.go:170` (`DenyIn`), `nft.go:198` (`AuthorizedIn`), `mac.go:47` (`LeaseMAC`) and `mac.go:67` (`DenyMAC`) each fork `nft -j list set` and parse every element. `Authorize` (`phase3_gate.go:81`) is two lists and two writes; `denyAccess` (`phase3_gate.go:366`) is `Revoke` (two lists, up to two deletes) plus `Authorized` (one or two lists). Cost per call grows with the number of guests, so the lease renewal of N sessions was O(N^2).
3. **The admission of one new session cost about 28 forks, several of them O(N).** `shape.go:223` `PrepareSessionIn` first clears the slot (`DeleteSession`, six forks that fail), then four to create the classes; `registerOrigin` (`phase3_origin.go:57`) and `classPresent` (`phase3_provision.go:278`) each run `tc -s class show` twice, a full class dump whose size grows with N; `SessionForwarding` (`:277`) runs two `tc filter show`; `Authorize` and `Authorized` (`:292`, `:296`) add four list forks.
4. **Fail-closed work was per session and unbounded.** Every `failClosed` ran `denyAccess` (`phase3_provision.go:406`) and `AbortSession` (about ten forks including two class dumps). A pass that was already late failed more sessions, which created more work: the measured spiral. `phase3_shaping.go:403/454/487` did the same for teardown, stray authorizations and stray classes, one session at a time.
5. **No ordering.** `phase3_shaping.go:526` walked sessions in class-number order; new sessions sort after the old ones, so when the 2-minute budget (`phase3PassTimeout`) was spent on routine work they were never reached. `acctd` submits again every tick, the passes are serialised by `p.mu`, and a pass that outlived the 10 s client timeout kept running detached, so the next pass started late and found even more to do.
6. **The between-walks lease timer (`phase3_fastpath.go:108`) had the same per-session `Authorize`.** It is dormant while passes report problems (as in the lab) and becomes the steady-state cost once they do not.

Why 380: with a 5 s tick, ~25 ms per fork and ~4-12 forks per session per pass, the budget of a pass is exhausted at a few hundred sessions; beyond that the pass never finishes, every session sits behind the deadline, and `context deadline exceeded` fails sessions closed.

## 3. Design

All of it lives in `cmd/netd/phase3_batch.go` plus small hooks, and is active only when the shaper and the gate implement the batch interfaces (the production ones do; the per-session test doubles do not) and `NETD_PASS_BATCHING` is not `false`. With that variable set to `false` the pass is the per-session pass of the base commit, which is also what the regression tests use as a control.

**Order of work inside a pass** (`provisionBridgeBatched`):

1. Stray authorizations and ended sessions, each as one transaction (`removeStrayAuthorizations`, `tearBatch`), then stray classes as one `tc -batch` proven with one read (`removeStrayClasses`). Same order as before: access is denied before any class goes.
2. Every desired session is examined from memory and the DHCP evidence only (`provisionPrecheck`: access boundary, address ownership, MAC binding, clock, quarantine). Sessions that fail closed queue their denial.
3. **Urgent**: the queued denials are proven in one transaction and the tc teardown of those sessions follows (`flushDenies`); then sessions whose state changed or is unproven run the per-session path; then new sessions are admitted in chunks of 32 (`admitChunk`); then the leases they need are written in one transaction (`flushRenewals`).
4. **Routine**: sessions already in force and unchanged (`classifySession`: class present on both devices with the installed parent, rate and ceiling equal to the plan's, proven this boot, no quarantine record). They cost no fork unless a lease is due, and the work stops, reported, if less than 15 s of the pass budget is left. A routine session has at least `phase3LeaseRenewBelow` (60 s) of lease left when it is skipped, so deferral cannot expire a guest.

**What an unchanged session no longer does.** The class is not rewritten when the per-pass read (`tc -s class show`, already taken once per device per pass, now parsed for `parent`, `rate`, `ceil`) shows the planned values (`ClassBytes.Params`; anything unparsable re-rates as before). The database is not asked again for 60-90 s (`provenAt`, per-session jitter), then once. The lease is written only when `needsRenewal` says so, and all due leases go in one transaction.

**Batch primitives.**

* `nft.Client.LeaseBatch` / `RevokeBatch` (`internal/nft/batch.go`): one read of each set, one `nft -f -` transaction for up to 256 elements (a refresh stays delete+add in the same transaction, so nobody is unauthorized in between; the timeout is exactly the one requested), one read to verify. If the transaction is refused the chunk is retried once on a fresh read (an element may have lapsed between read and write), then executed element by element with the unchanged per-element calls; the failing element, and only it, carries the error.
* `shape.Client.PrepareBatch` / `ActivateBatch` / `ForwardingBatch` / `DeleteClassesBatch` (`internal/shape/batch.go`): `tc -batch -`. `tc` stops at the first failing line and leaves earlier lines applied, so a refused batch is never interpreted: its sessions go through the per-session path, whose first step clears the slot, and that path attributes the failure.
* `nftGate` / `flushingGate` `AuthorizeBatch` / `RevokeBatch`: the conntrack flush of revoked addresses is one call for the batch.
* The context of the urgent flushes (denials, lease writes) is detached from the pass deadline (30 s of its own), so an exhausted pass cannot leave a revocation undone.

**Not changed**: the plan envelope (`internal/shapeplan`), any API, database object or migration; the quarantine state machine (`phase3_enforcement.go`, `phase3_journal.go`) and the durable class store; the SHARED allocation path (those sessions always take the per-session path, group re-rate once per group per pass as before).

## 4. Measurements

Harness: `cmd/netd/phase3_scale_test.go` + `phase3_fakekernel_test.go`. It runs the real `reconcileLocked`, `provisionSession`, `shape.Client` and `nft.Client`, injects the process executor, and counts every tc/nft/ip process. Two kernels: a stateful simulation of the tc and nft command lines (exact counts, no timing value), and a real kernel in a privileged container (`oglab-base:1`, bridge + IFB + the three real sets; real `tc`/`nft`, timing indicative because the host also ran other work).
`execs` = processes forked by one pass. `db` = Activate + Confirm + Ended + RegisterClassOrigin + AllocateClassGeneration calls (the harness database fake answers instantly). Pass budget in the harness is the production 2 minutes.

Scenarios: `admit` = cold admission of N sessions in one pass; `steady` = one pass over N active unchanged sessions; `renew_due` = same, 35 s later (every lease below the 60 s renewal threshold); `reprove_due` = 60 s later (every durable proof due again); `arrive300` = N active plus 300 new sessions in one pass.

### 4.1 BEFORE (base commit)

Simulated kernel, exact process counts:

| N | admit execs | steady execs (class change / db) | renew_due execs | arrive300 execs |
|---|---|---|---|---|
| 100 | 2,811 | 204 (200 / 100) | 604 | 8,604 |
| 500 | 14,011 | 1,004 (1,000 / 500) | 3,004 | 9,404 |
| 1,000 | 28,011 | 2,004 (2,000 / 1,000) | 6,004 | 10,404 |
| 2,000 | 51,951 (pass hit its budget: 145 failed) | 4,004 (4,000 / 2,000) | 12,004 | 12,404 |

Real kernel (indicative wall time):

| N | scenario | execs | wall | outcome |
|---|---|---|---|---|
| 100 | admit | 2,811 | 40.3 s | 100 active |
| 100 | steady | 356 | 6.4 s | |
| 100 | renew_due | 604 | 12.5 s | |
| 100 | arrive300 | 8,386 | 120 s (budget) | 28 failed closed, 272 of 300 promoted; first problem `prepare ...: signal: killed ... nft list phase3_auth_ipv4: context deadline exceeded` |
| 500 | admit | 12,323 | 120 s (budget) | 211 failed, 289 active |
| 500 | steady | 2,112 | 43.4 s | |
| 500 | renew_due | 3,215 | 42.1 s | |
| 500 | arrive300 | 9,138 | 120 s (budget) | 123 failed, 178 of 300 promoted |

This is the lab defect, reproduced with the same signature (deadline errors in `tc class add` and `nft list`, sessions failed closed, new sessions starved behind active ones) on a single bridge at N = 100.

### 4.2 AFTER

Simulated kernel, exact process counts (same scenarios, same N, one bridge):

| N | admit execs | steady execs (class change / db) | renew_due execs | reprove_due execs (db) | arrive300 execs |
|---|---|---|---|---|---|
| 100 | 68 | 4 (0 / 0) | 8 | 8 (100) | 144 |
| 500 | 236 | 4 (0 / 0) | 12 | 12 (500) | 144 |
| 1,000 | 460 | 4 (0 / 0) | 20 | 20 (1,000) | 144 |
| 2,000 | 894 | 4 (0 / 0) | 36 | 36 (2,000) | 144 |

The steady pass is the two set reads and the two class dumps that every pass needs, whatever N is. Renewal grows only with N / 256 (one transaction per 256 leases). Admission of new sessions costs 8 processes per 32 sessions plus the class dumps, independent of the number of active sessions: `arrive300` is the same 144 processes at N = 100 and at N = 2,000, against 8,604 and 12,404 before.

Real kernel (privileged container, 2 vCPU / 3 GB, indicative wall time):

| N (bridges) | scenario | execs | wall | outcome |
|---|---|---|---|---|
| 100 (1) | admit | 68 | 0.86 s | 100 active (was 40.3 s) |
| 100 (1) | steady | 4 | 50 ms | (was 6.4 s) |
| 100 (1) | renew_due | 8 | 111 ms | (was 12.5 s) |
| 100 (1) | arrive300 | 144 | 2.7 s | 300 of 300 promoted, 0 failed (was 120 s budget, 28 failed) |
| 500 (1) | admit | 236 | 4.0 s | 500 active (was 120 s, 211 failed) |
| 500 (1) | steady | 4 | 76 ms | (was 43.4 s) |
| 500 (1) | renew_due / reprove_due | 12 / 12 | 405 ms / 360 ms | (was 42.1 s) |
| 500 (1) | arrive300 | 144 | 3.2 s | 300 of 300 promoted, 0 failed (was 120 s budget, 123 failed) |
| 1,000 (1) | admit | 460 | 9.9 s | 1,000 active |
| 1,000 (1) | steady | 4 | 136 ms | |
| 1,000 (1) | renew_due / reprove_due | 20 / 20 | 776 ms / 738 ms | |
| 1,000 (1) | arrive300 | 144 | 4.5 s | 300 of 300 promoted, 0 failed |
| 2,000 (2) | admit | 917 | 22.7 s | 2,000 active; cold start, see limit 2 |
| 2,000 (2) | steady | 6 | 236 ms | |
| 2,000 (2) | renew_due / reprove_due | 38 / 38 | 1.8 s / 1.9 s | all 2,000 leases in 8 transactions |
| 2,000 (2) | arrive300 | 146 | 5.9 s | 300 of 300 promoted in one pass, 0 failed |

Against the targets: a steady pass over 2,000 unchanged sessions is 6 processes and about 0.24 s (target: well under 5 s, no per-session fork); 300 new sessions on top of 2,000 active are promoted in the pass that carries them (about 20 ms each), a tick's worth of arrivals (about 25 at 300 per minute) costs one chunk of 8 processes; cost is O(N) overall (the only N-dependent terms are the two class dumps per device per pass and one nft transaction per 256 leases).

Single bridge at N = 2,000 (measured once on the build before the last small optimization, which removed two class dumps per admission chunk): admit 1,020 processes / 34.5 s, renew_due 36 / 2.0 s, and `arrive300` crossed the kernel limit of 2,047 filters per device (limit 1 in section 6) and failed 253 sessions closed. That is why the 2,000 rows above are measured on two bridges.

## 5. Invariants and how each is kept and tested

| Invariant | How | Test |
|---|---|---|
| Fail closed: an address whose ownership is foreign is not authorized or renewed, its class is removed, and the denial is proven | `provisionPrecheck` is unchanged and runs for every session before any write; the denial is proven by re-reading both sets after the delete (`RevokeBatch`), then tc teardown | `TestFailClosedComesFirstAndRemovesEverything` |
| A failed deny is reported (`PACKET AUTHORIZATION NOT PROVEN REMOVED`) for that session only and retried | refused transaction -> retry -> per-element path -> error attributed; next pass repeats the denial | `TestADenialThatCannotBeProvenIsReportedForThatSessionAndRetried`, `TestRevokeBatchAttributesAnUndeletableElement`, `TestRevokeBatchReportsWhatItCouldNotRemove` |
| Open flows end with the authorization (forward chain accepts established flows first) | one conntrack pass for a revoked batch, not repeated for an address already flushed | `TestARevokedBatchEndsTheOpenFlowsOfEveryAddressInOnePass` |
| Strays (authorizations, classes) are removed, ended sessions torn down, denial before tc | batches in the same order as before; class removal is proven with a read | `TestStrayAuthorizationsAndClassesAreRemovedInBatches`, `TestEndedSessionsAreTornDownInBatches` |
| Element timeouts and renewal semantics: never longer than requested; refresh is delete+add in one transaction | `LeaseBatch` builds exactly that; lease length per session is the unchanged `leaseFor` / `provisionalOrLess` | `TestLeaseBatchIsOneTransactionWithOneReadBeforeAndAfter`, `TestDueLeasesAreRenewedInOneTransactionAndKeepTheirLength`, `TestLeaseBatchRefusesUnboundedAndSubSecondLeases` |
| A write that did not take is not mistaken for success | every lease batch is verified by a read | `TestLeaseBatchReportsAnElementThatIsNotThereAfterTheWrite` |
| Accounting continuity: a re-rate keeps counters, creates no generation or origin; an unchanged class is not touched | `class change` only for a class whose installed values differ; admission order (generation, class without filters, origin, filters, authorization) unchanged | `TestReRateKeepsCountersAndGenerationAndOnlyChangedClassesAreRewritten` |
| A class lost behind netd's back is a new series, not a renewal | classification requires the class on both devices | `TestALostClassIsReadmittedNotRenewed` |
| Quarantine / backoff / write-ahead journal | quarantined sessions always take the per-session path; the journal is written per session before the batch authorization, with the boot clock read again | existing quarantine/journal tests (`phase3_enforcement_test.go`, `phase3_durability_test.go`) pass unchanged |
| Durable ACTIVE proof | repeated every 60-90 s and after any restart, never skipped for a session that is not proven this boot or is quarantined | `TestDurableProofIsRepeatedOnSchedule` |
| MAC-binding option | `BindMAC` runs in the pre-check before any write; `LeaseBatch` writes the MAC-bound element in the same transaction when a MAC is bound; `requireMAC` follows the policy | `TestLeaseBatchIsOneTransaction...`, existing `phase3_mac_gate_test.go` |
| Every error is reported in `res.Problems` / `res.Failed`, none swallowed | refused batches are logged and fall back; pass-budget exhaustion is reported as a failure (so the pass is not "converged" and the AD-53 fast path does not skip the next walk) | `TestAnUnwritableAuthorizationFailsOnlyThatSession`, `TestAClassThatCannotBeCreatedFailsOnlyThatSession`, `TestRefusedTCBatchDoesNotFailSessionsThatCanBeAdmitted`, real-kernel `TestRealKernelBatchFallsBackOnRealTCErrors` |
| Batching keeps per-session failure attribution | nft: retry, then per element; tc: whole chunk through the per-session path | same as above |
| New work never starves behind routine work | order of the pass; routine bounded by the deadline | `TestNewSessionsAreNotStarvedByActiveOnes`, `TestNewSessionsAreAdmittedBeforeRoutineRenewal` |
| Cost property: a steady pass does not fork per session | | `TestSteadyPassCostDoesNotGrowWithTheNumberOfSessions`, `TestBetweenWalkRenewalIsOneTransaction` |

`TestSteadyPassCostDoesNotGrowWithTheNumberOfSessions` and `TestNewSessionsAreNotStarvedByActiveOnes` each contain a control run with batching off (`p.noBatch`, the base-commit pass) and require the control to violate the property (at least 100 processes and 50 database calls for 50 unchanged sessions; new sessions not all promoted inside the budget), so they are shown to discriminate rather than to pass vacuously.

## 6. Residual limits and risks

1. **Kernel limit of 2,047 u32 filters per device parent.** Each shaped session installs one `u32` filter with its own `pref` on the bridge and on its IFB. The kernel gives each `pref` a hash-table id from a space of 0x7ff, so the 2,048th filter fails with `Error: Filter already exists` (measured: exactly 2,047 installed, then every further `filter add` refused). This predates the change and is independent of it. One bridge (one guest network) therefore cannot shape more than 2,047 sessions; a site with more uses several guest networks, or the classification must move to one hashed `u32` table (a redesign of `shape.go`, not done here). At the limit the new sessions fail closed and are reported; with batching the refused chunk falls back to the per-session path, which is slow (3,739 forks, 77 s measured at N = 2,000 on one bridge). The capacity lab must spread the 2,000 + 300 sessions over at least two guest bridges to test beyond 2,047.
2. **Class dumps grow with N.** Admission reads `tc -s class show` twice per device per chunk of 32 (origin baseline needs fresh counters); at 2,000 classes that is the dominant cost of a cold admission (see the real-kernel `admit` rows). A steady pass reads each device once.
3. **The DHCP evidence lookup is a linear scan per session** (`phase3_address_owner.go` `Evidence`), O(N^2) CPU in memory, about 4 million string compares at N = 2,000 (a few ms in the harness). It is not a fork and was not changed.
4. **Re-prove spike.** Every 60-90 s each session asks the database once (spread by jitter). At 2,000 sessions that is about 33 calls per second on average.
5. **Routine deferral under a nearly exhausted budget** is reported as a failure of the pass; it should never occur with a pass of a few seconds.
6. **`Shaped` can count a session whose lease write later fails** (the failure is reported and the session is failed closed in the same pass).
7. SHARED-allocation sessions are not batched (per-session path, group re-rate once per group per pass).
8. Not exercised: a real appliance (this was the harness, a simulation and a privileged container), real Kea, real Postgres latency, the production nft ruleset (the sets were created with the production key types), MAC binding switched on (covered by unit tests only).

## 7. Lab re-run

1. Build netd from this branch and deploy it to the lab appliance only (the standing test-appliance authorization applies; nothing here deploys it). Record the deployed commit from `go version -m` of the installed binary.
2. Put the simulated clients on at least two guest networks (bridges), at most about 1,000 per network, because of limit 1 above.
3. Run the same load: ramp to 2,000 sessions, then 300 sign-ins per minute for at least 10 minutes.
4. Watch, per minute: sessions in `PENDING_ENFORCEMENT` (should drain within a few seconds of each batch of sign-ins), `journalctl -u stayconnect-netd | grep 'phase3 shaping applied with problems'` (should be absent), and `strace -f -e trace=execve -p $(pidof netd) 2>&1 | grep -c execve` over 12 s (a few dozen with sign-ins arriving, single digits when idle; the base commit showed 195 with no new sessions).
5. Rollback switch: `NETD_PASS_BATCHING=false` in the netd environment and restart gives the per-session pass of the base commit.
6. Harness against the lab image, no appliance needed: `NETD_SCALE_REAL=1 NETD_SCALE_BRIDGES=2 NETD_SCALE_N=100,500,1000 go test ./cmd/netd -run TestScaleTable -v` in a privileged container with `tc`, `nft`, `ip` (the test creates and removes its own bridges and the `inet stayconnect` table; never run it on a host that has a real ruleset).

## 8. Verification performed

* Linux (`golang:1.26.10`, privileged): `go test -v ./cmd/netd ./internal/shape ./internal/nft ./internal/shapeplan ./cmd/acctd ./internal/conntrack` -> all packages ok, 517 top-level tests pass, 0 fail, 2 skipped (the real-kernel tests when `NETD_SCALE_REAL` is not set). `go test -race` over `cmd/netd`, `internal/shape`, `internal/nft` -> ok, no race.
* Real kernel (privileged `oglab-base:1`): `TestScaleTable` (N = 100, 500, 1,000 on one bridge; N = 2,000 on two) and `TestRealKernelBatchFallsBackOnRealTCErrors` pass.
* On the Windows development host two pre-existing, unrelated tests fail on directory `fsync` (`TestSysnetNetplanFileIsReplacedAtomically`, `TestModeRequiresAVerifiedAssignment`, plus several in `cmd/acctd`); they pass on Linux.
* Not run: the project-wide suites, governance validators, the four gates (Direct Execution Mode; this is a targeted change). Nothing was pushed or deployed.

## 9. Files

* `data-plane/cmd/netd/phase3_batch.go` (new): ordering, batching, gates, flushes.
* `data-plane/cmd/netd/phase3_provision.go`, `phase3_shaping.go`, `phase3_enforcement.go`, `phase3_origin.go`, `phase3_fastpath.go`, `main.go`: hooks (pre-check/tail split, deferral of denials and lease writes, unchanged-class and proof skips, switch).
* `data-plane/internal/nft/batch.go` (new), `data-plane/internal/shape/batch.go` (new), `data-plane/internal/shape/shape.go` (`ClassBytes` carries parent/rate/ceil; `NewWithExec`), `data-plane/internal/nft/nft.go` (`NewWithExec`).
* Tests: `cmd/netd/phase3_batch_test.go`, `phase3_scale_test.go`, `phase3_fakekernel_test.go`, `internal/nft/batch_test.go`, `internal/shape/batch_test.go`.

## 10. Second lab run: sustained churn plus traffic (continuation)

Release 885b6ac986c6 (this work, commit 56b35312) ramped 1,700 sessions well (98 % sign-ins, p95 7.6 s, 45-61 % of 2 vCPU, 2.4 execs/s at rest) and then collapsed under the steady phase: 10 % of the 1,700 moving 256 KB every 5 s while 300 devices cycled 60-second vouchers at 300 sign-ins a minute. 2,214 of 6,474 traffic requests and 23 of 3,000 churn sign-ins succeeded; the stress phase (all 1,700 moving) 4,778 of 33,320. This section is what the logs and an instrumented re-run showed, what was changed, and what is still open.

### 10.1 Evidence

Method: the pass now logs one line per full pass (`phase3: pass`) with the time it waited for the lock, the budget left when it got it, where its time went (stage in ms), counts of what it did, and the database calls it made (`db_<kind>=calls/total ms/slowest ms`). The base build was re-run with that line (lab, 6 min steady), the original `netd2.log` was re-read, and the appliance was sampled once a second (authorization set, 40 long sessions, MAC set), plus blocked-query and active-query samples of Postgres.

| Finding | Evidence |
|---|---|
| **The "pass budget" was not 2 minutes.** The two-minute context was created in the HTTP handler before the pass waited for `p.mu`. acctd submits again after its 10 s client timeout while the previous pass is still running, so detached handlers queued, each arriving at the front with its budget spent. | `netd2.log`: 3,647 of the 3,652 `session enforcement ended but durable state not converged` lines have `err = context deadline exceeded` and 3,096 fall in one 30 s window starting 19:18:32; `phase-3 authorization scan failed` (its list ran with a dead context); the 72 `budget nearly exhausted` warnings came from the same passes (the reserve check saw an expired deadline). New log shows `lock_wait_ms` up to 13.7 s and `budget_at_lock_ms` ≈ 119,990 once the budget starts at the lock. |
| **Lease renewal waited behind everything else**, so one slow pass let every lease run out together. | Base build, instrumented: one pass of 109 s, whose `admit` stage was 107 s because ONE `allocate_class_generation` call took 106 s (`db_alloc=20/106232/106163`). `monitor2.csv`: the authorization set reads 0 at 738, 923, 1,045 and 1,229 s although 1,700 sessions are active. |
| **Every re-prove of every active session was a locking write** (`activate_session_enforcement` does `SELECT ... FOR UPDATE` and commits, even for an already-active session): 1,700 per 60-90 s, 1,700 `ALREADY_ACTIVE` log lines in a burst. | Function definition; `db_activate=1700/5166/10` in one pass. |
| **Ended sessions were recorded as ended again on every pass.** A churned client keeps its address, so each ended session is "handed over" to its successor and the handover branch ran `end_session_enforcement` (also `FOR UPDATE`) each pass, for as long as the plan lists it (one hour). | `db_ended=4176` in a single pass; 388-711 per pass thereafter. Simulator: 28,355 end writes for 2,043 ended sessions (14 each). |
| **A session that ended while its plan was in flight was treated as a failed activation:** grace, strike, quarantine, `Failed++`, degraded pass. | `ENFORCE_SESSION_ENDED`, `ACCT_SESSION_OUT_OF_SCOPE ... has ended`, `less than one representable second of access remains` in the problem lists (64 degraded passes, `failed` 2..302). |
| **The plan grows without bound under churn.** Ended sessions stay in it for an hour (`internal/enforce.PlanForSite`): 18,000 entries at 300/min, past netd's 4 MB request limit after about 45 minutes, and thousands of entries walked per pass. | Code; plan sizes 3,392 → 5,876 in the runs. |
| **Per-session database calls ran one after another with no bound.** | Pass stages of 43-64 s for 37-300 admissions; calls with a 5-8 s tail. |
| **The remaining slowness is the database, not netd, and it is caused by the sign-in path.** Active-query sampling (150 samples): `svc_scd` sign-in transactions waiting on `pg_advisory_xact_lock(hashtextextended(appliance, 7))` in 136 samples and "idle in transaction" in most others. That lock is `reserveLicensedSlot` (`cmd/scd/iamv2_session_ops.go`), held for the whole sign-in transaction, so all activations on the appliance are serial; `svc_scd` is limited to 20 connections and hit it (`too many connections for role "svc_scd"`); `lock_timeout` 5 s and `idle_in_transaction_session_timeout` 30 s kill transactions (`canceling statement due to lock timeout`, `terminating connection due to idle-in-transaction timeout`). netd's own calls were not blocked by locks (a sampler of blocked `svc_netd` calls stayed empty), they were slow: `db_alloc` 87 ms/call in a quiet pass, 460 ms/call under churn. | `pg_stat_activity` samples, Postgres log, role limits. |
| **The traffic success rate in this lab is not a measure of forwarding.** A burst of 850 clients fetching from the appliance's OWN portal (no forwarding, no NAT, no authorization set involved) failed 43 % of connections, and the bridge counters show that the failed SYNs never arrived (`rx_packets` rose only by the successful connections, no drops). 100 clients at once: 100 % ok. Spreading the requests over 4 s did not change it (58 % failed). | `ogload traffic -sink http://10.60.0.1:8380`; `sink` saw exactly the successful number of passive opens. The limit is in the generator's side of the lab, above roughly 100 simultaneous namespace dials. |

### 10.2 What changed

* **Budget at the lock, newest plan wins** (`phase3_shaping.go`): the handler no longer starts the clock; `submit` does, after it has the lock and a fresh `now`. `noteArrival` records the newest generation on arrival and a plan that was overtaken while waiting is answered `superseded_by_newer_plan` without walking.
* **Order of a pass** (`phase3_batch.go`, `provisionBridgeBatched`): precheck (memory only), denials, **lease renewal of every unchanged session (one transaction, no database)**, new sessions, the per-session path, a second renewal when the pass has already run 8 s (judged by the time left NOW: `gateView.aged`, `renewAgain`), re-proof, and last the durable end records. Nothing that can wait on the database stands between a long session and its lease.
* **A pass bounds its work** (`phase3SoftBudget`, 15 s): admissions and the per-session path stop after it; what was not reached is not a failure and is first in line next pass (`indivCursor` resumes where the previous pass stopped, so a few sessions that always wait cannot take every pass). A single database call is bounded (`phase3DBCallTimeout`, 3 s; applied in `timedEnforcement` / `timedOrigins` / `timedGenerations`).
* **Database calls a few at a time** (`phase3_churn.go`, `phase3DBConcurrency` = 6, pool raised to 8 but within the role's limit of 10): class generations, origins and promotions of a chunk, promotions of the per-session path, re-proofs, end records. Results are consumed in order by the unchanged state machines (`takeActivation`, `activationResult`), so quarantine, grace and the write-ahead record are untouched.
* **Reads instead of writes**: a re-prove is a plain `Confirm` read (the promotion call is only used when the read does not say "active"); an end record is skipped when `IsEnded` (plain read) says durable state already ended the session. Both fall back to the previous calls on any other answer.
* **An ended session is recorded once**: the handover branch is de-duplicated through `endedTorn`; end records are written at the end of the pass, a few at a time, with a limit of three failures (a database that does not answer is not asked once per ended session).
* **The ordinary end of a session is an event, not a failure** (`activationEnded`, `endClosed`, `ended_mid_pass` in the response): `ENFORCE_SESSION_ENDED`, an origin refused because the session ended, and a window with under a second left withdraw access (denial proven first, tc torn down) with no grace, strike, quarantine or `Failed++`. A withdrawal that cannot be proven is still a failure.
* **The plan lists ended sessions for 10 minutes, not an hour** (`enforce.EndedListedFor`): netd's stray sweeps and the durable record already cover them. Requires the acctd of this branch.
* Instrumentation kept: the `phase3: pass` line and `db_*` timings; `ogload traffic -spread`.

Invariants: unchanged paths for fail-closed, element timeouts (a renewal restores the pass's lease and a second renewal restores that lease less the time already spent, never longer), the write-ahead journal, quarantine and backoff, the durable class store, MAC binding and error reporting. A session is renewed at full length only while its durable proof is fresh (5 minutes, `phase3ProofMaxAge`); an unproven or stale one gets the short lease until a read confirms it. Off switch: `NETD_PASS_BATCHING=false` still gives the per-session pass (the new bookkeeping reads and the superseding of plans apply in both).

### 10.3 Numbers

**Simulator** (`TestChurn*`, 300 long sessions, 100 clients cycling 60-second sessions, 60 passes of 5 s, one 100 s database stall armed in the run, database latencies 40 ms writes / 5 ms reads added to the simulated clock; same harness run against commit 56b35312 for "before"):

| | before (56b35312) | after |
|---|---|---|
| long sessions whose authorization lapsed | 268 of 300 | 0 |
| longest pass (simulated) | 127.8 s | 4.3 s |
| p95 pass | 45.6 s | 1.8 s |
| longest wait of a new session for promotion | 127.8 s | 4.3 s |
| database calls | 40,209 | 3,114 |
| locking end writes for 2,043 / 483 ended sessions | 28,355 | 0 |
| failed passes' failures | 53 | 0 (11 sessions counted as ended mid-pass) |

Full size (1,000 long, 300 clients, 120 passes; `NETD_CHURN_FULL=1`, informational: the simulated database adds the latency of concurrent calls up instead of overlapping them): no lapse, 3,638 arrivals all promoted in their pass, p95 pass 11.2 s, longest 24.9 s, 24,422 database calls, 0 failures.

**Lab** (2 vCPU / 4 GB appliance, 1,700 long sessions, steady phase; the generator limit of 10.1 applies to the traffic rows):

| | base build (coordinator run) | base build + pass log (re-run) | this branch, final build, 10 min |
|---|---|---|---|
| passes | 64 degraded, 72 `budget nearly exhausted` | pass of 109 s (one 106 s call), 70 s, 18 s | 35 passes, p50 15.8 s, p95 23.2 s, max 35.4 s; lock wait p50 4.1 s, max 10.5 s |
| end records per pass | 3,096 failing in one window | 4,176 | 0 writes (reads only) |
| authorization of long sessions (1 s sampler) | set at 0 four times | not sampled | 446 of 494 samples complete; 48 s with the whole set empty, see below. The previous build of this branch (6 min, soft budget, no second renewal): 314 of 314 complete. |
| new sessions promoted within 10 s of creation | n/a | 4.3 % | 4.6 % (p50 29 s; was 44.5 s in the re-run of the base build) |
| churn sign-ins ok | 23 / 3,000 | 40 / 1,800 | 22 / 3,000 |
| traffic ok | 2,214 / 6,474 | 1,237 / 4,316 | 2,753 / 6,806 |

### 10.4 What is NOT solved, and why

1. **New sign-ins do not meet 95 % within 10 s in the lab, and this is no longer netd's pass.** Promotion takes p50 29 s because every database call the pass makes costs 0.1-0.5 s while the sign-in path saturates the database (section 10.1). Needed, outside netd: (a) scd: `reserveLicensedSlot` takes an appliance-wide advisory lock for the whole activation transaction and counts sessions by a join; hold it for the shortest part of the transaction (or count from an indexed counter), and keep the transaction free of non-database work (it sits `idle in transaction` for 5-9 s); (b) the `svc_scd` connection limit (20) is below the concurrency a 9 s sign-in at 5 per second needs; (c) acctd's accounting (one commit per live session per tick) and its expiry transaction (idle in transaction 5-14 s) are the other large writers. A migration or role change is a controlled decision and was not made.
2. **Authorization of long sessions still lapses when acctd delivers no plan for about 90 s.** In the final run the whole set was empty from 21:38:44 to 21:39:35: plan generations 14303 and 14304 are consecutive and 91 s apart, acctd logged `expiry enforcement failed: terminating connection due to idle-in-transaction timeout` at 21:39:37, and Postgres was in a lock-timeout storm from 21:38:05. By design a lease lives only as long as the plan that renews it (lease TTL = plan validity = 90 s), so a producer that cannot reach its database for 90 s takes every guest offline. Whether that bound should be longer is a product and security decision (PO); it is not changed here.
3. **Traffic success is gated by the generator** above about 100 simultaneous dials; use fewer movers per round or the new `-spread` flag and judge forwarding from the authorization sampler.
4. The lab host's C: drive was at 100 % while this was being measured (the Go build cache; 6 GB freed). If the lab VM shares it, its fsync latency during the earlier runs is suspect.

### 10.5 Verification and files

* Linux (`golang:1.26.10`): `go test -v` over `cmd/netd`, `internal/shape`, `internal/nft`, `internal/shapeplan`, `cmd/acctd`, `internal/conntrack`, `internal/enforce` -> all ok, 541 top-level tests pass, 0 fail, 2 skipped (real-kernel tests); `go test -race` over `cmd/netd`, `internal/nft`, `internal/shape` -> ok. The `internal/enforce` integration tests need a database and were not run, so the shorter ended listing (`EndedListedFor`) is untested against Postgres.
* New tests: `phase3_churn_test.go` (`TestChurn*`, superseded plans, a database call that never answers, ended-in-durable-state sessions, ended once, deferral, second renewal), `TestLeaseRenewalComesBeforeAnyDatabaseCallAndNewSessionsFollow`, `TestBetweenWalkRenewalIsOneTransaction`.
* Files: `cmd/netd/phase3_churn.go`, `phase3_dbtime.go`, `phase3_trace.go`, `phase3_origin_register.go` (new); `phase3_batch.go`, `phase3_provision.go`, `phase3_shaping.go`, `phase3_enforcement.go`, `phase3_gate.go`, `phase3_origin.go`, `main.go`; `internal/enforce/enforce.go`; `tools/ogload/main.go` (`-spread`).

### 10.6 Lab hygiene

netd and acctd were replaced (originals kept as `*.orig`, restored byte for byte and removed at the end: sizes equal the installed release), samplers stopped, every file added to `oglab-appl`, `oglab-gen` and `oglab-sink` removed. several batches of 3,600 short vouchers (`LAB-S60`) were issued on the appliance and remain in its database; no other lab state was changed.
