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
