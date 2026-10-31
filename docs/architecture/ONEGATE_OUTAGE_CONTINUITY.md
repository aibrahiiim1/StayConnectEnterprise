# OneGate outage continuity: keeping guests online when OneGate itself fails (2026-10-30)

Status (2026-10-31): merged to master in PR #277 and deployed to the test appliance (the lab results of that mission are in
[`AUDIT_DECISION_DISPOSITIONS.md`](../AUDIT_DECISION_DISPOSITIONS.md)). The Product Owner's final decisions are in section 1a; the corrections made
after the lab (F2 coverage of recently admitted sessions, F5 evidence) are on branch `delivery/package-controls-backup-remediation`,
**implemented, not yet tested or deployed**. Supersedes the "not built" paragraph of
[`reviews/AVAILABILITY_DURING_ACCTD_INTERRUPTION.md`](../reviews/AVAILABILITY_DURING_ACCTD_INTERRUPTION.md) section 4, whose analysis (section 1) it takes as its starting point.

## 1a. Final Product Owner decisions (2026-10-31)

| # | Decision | Where it holds |
|---|---|---|
| 1 | A rotated MAC without authenticated pre-checkout lineage stays ineligible for After Check-out Access. | `after_checkout_device_guard` (0193, 0202): only devices the checkout carried over (or a staff reset carried from that cohort) |
| 2 | Staff may end the whole After Check-out entitlement. | End Package (0202); the kernel withdrawal is immediate (scd `/v1/admin/sessions/withdraw`) |
| 3 | An appliance reboot while PostgreSQL is down **fails closed**: no unmetered or unverified access is restored. | The governor never creates an element; a record from another boot is refused (D-1 accepted as recommended) |
| 4 | Database-down exposure is accepted within the configured window (default 60, 0 to 120 minutes). | D-2 accepted; `outage_continuity_minutes` |
| 5 | Inside that window, authorized sessions continue only within their hard expiry, quota and continuity bounds. | Section 3 |
| 6 | No false success for an operator revocation or a PMS checkout that cannot be processed. | Section 6: what needs the database is refused or reported, never shown done |
| 7 | The temporary idle-timeout limitation during an outage is accepted within those bounds. | D-3 accepted (R3) |

The 90-second kernel lease and the fail-closed boundaries are unchanged.

**Corrections on the branch (not yet tested):**

* **F2.** A session admitted after the last 30-second counter sample used to get no continuity. It is now covered: each sample records which classes existed, and a class created after the sample is metered from zero. That can only overcount, never grant extra.
* What remains of F2 is inherent: a session admitted inside the last plan interval (5 s) before plans stop is not in the record.
* **F5.** The governor now reports, per reason, which held sessions it did not renew. The reasons are:
  - element absent;
  - DHCP ownership unproven;
  - counters unreadable;
  - gate unreadable;
  - deadline reached.

  They appear in `GET /v1/phase3/continuity` (`not_renewed`) and in the log. The next run that shows fewer elements than the record therefore says which step skipped them. **The cause of F5 is still not established.**

## 1. Decision summary

* Product Owner priority: guest continuity when OneGate itself fails. Default **60 minutes**, administrator-configurable **0 to 120 minutes**, **0 = strict** (the previous 90-second lease, exactly).
* The 90-second kernel lease (`phase3LeaseTTL`, `planValidity`, nft element timeouts) is **not** lengthened. A *different* mechanism, a bounded **continuity governor in netd**, keeps renewing
  only the authorizations that are still present, for only the sessions the last healthy plan bounded, each renewal still a 90-second (or shorter) kernel lease.
* What bounds a session during an outage, whichever comes first: the window (monotonic clock), the session's hard end (entitlement window, plan maximum session length, online-time allowance),
  the entitlement's **byte budget metered by netd from the tc class counters**, a **revocation** (pushed directly to netd and journalled, or found by a database probe), loss of DHCP ownership
  evidence, loss of the traffic class, an unreadable clock or counter.
* **What cannot be supported without a material tradeoff is listed in section 8 with the exact decisions required.** In short: reboot is not covered (nothing survives it to renew); while the database is
  down nothing can be revoked (and nothing can change, which is the only reason that is tolerable); idle timeout is not enforced during an outage.
  None of those is hidden behind the upper bound; the upper bound of 120 minutes is supportable *given* those three stated residuals.

## 2. Why the existing design could not simply be lengthened

[`reviews/AVAILABILITY_DURING_ACCTD_INTERRUPTION.md`](../reviews/AVAILABILITY_DURING_ACCTD_INTERRUPTION.md) section 1: the lease is the only thing that ends access for a revocation or an exhausted quota when
acctd is away, because *only acctd knows* (a) revocations in the database, (b) bytes used, (c) time-limited and licence-related ends. A longer lease would hand out unmetered, unrevocable access for its length.
The new mechanism therefore gives each of the three things a path that does **not** need acctd:

| acctd knew | now known to the governor by |
|---|---|
| a session / entitlement was revoked, ended, suspended, checked out | (1) staff disconnect: `POST /v1/flows/end` (existing scd to netd call) now also writes a durable journal entry, synchronously; `POST /v1/phase3/continuity/revoke` for any other pusher; (2) a read-only database probe by netd, every tick (5 s), `iam_v2.continuity_not_entitled` (migration 0199) |
| bytes used against the allowance | netd meters the class counters (down + up) itself, against a budget snapshot acctd supplies |
| time allowance, plan maximum session length, entitlement window | hard end per session, computed by acctd, carried in the plan |
| licence validity | see section 7 (licence is an admission control; continuity admits nobody) |

## 3. Mechanism

```
 acctd (healthy)                         netd                                           kernel
 ----------------                       ------                                         --------
 every plan (5 s): DesiredState   ----> normal pass; renews phase3_auth_ipv4 (<= 90 s lease)
 + Continuity block (not hashed          contNotePlan: per session
   with the plan, own hash):               deadline = now_boot + min(window, hard end - now)
   window, AsOf, per-session HardEnd,      baseline = counter sample at/before AsOf
   per-entitlement RemainingBytes          save record (atomic, 0600)  /var/lib/stayconnect/netd-phase3-continuity.json
 samples class counters every 30 s <---- contTick (every 5 s)
 ...acctd stops / DB down...
                                         plans silent for 60 s (monotonic) => governor takes over, per tick:
                                           1 drop: deadline passed | journal match | probe says not entitled
                                           2 meter: used = sum(counter - baseline) per entitlement >= budget => drop all its sessions
                                           3 deny dropped sessions at once (nft delete + conntrack flush)
                                           4 renew only: element PRESENT, DHCP ownership proven, metered if budgeted
                                             ttl = min(90 s, deadline - now, DHCP lease end)  ------------------------>  element refreshed
 acctd back: plans resume => governor stands down; acctd reads the ABSOLUTE class counters, ingests the delta
```

Key properties, each covered by a test (section 9):

1. **Not a longer lease.** Every renewal is at most 90 s and never past the session deadline; a wedged netd costs at most `min(90 s, remaining)` of stale access, exactly as today.
2. **Never creates an authorization.** An absent element (removed by an administrator, flushed set, reboot) is not re-created. Continuity can only *keep*, never *grant*.
3. **Deadlines are monotonic.** Computed on the boot-relative security clock (`/proc/uptime`, the clock the activation journal uses) when the block is accepted. Wall-clock jumps in either direction change nothing
   (`TestContinuity_WallClockJumpsDoNotStretchTheWindow`); an unreadable clock extends nothing.
4. **The block is bounded input, not authority.** Own SHA-256 hash (truncation/alteration refused), window must be 1..7200 s, `AsOf` within [-2 min, +30 s] of netd's clock, scope fields come from netd's own assignment (as for plans).
   A refused block clears continuity and the plan still applies strictly.
5. **Absence of a block says nothing new**: what is held is kept, never extended; an explicit "off" block (setting 0) clears it. This is why an acctd that flaps (restarts, or briefly stops vouching because accounting is stale)
   does not cost guests their continuity, and why setting 0 takes effect on the next plan (5 s).
6. **Fail closed on any doubt**: no baseline sample at/before AsOf for a budgeted session (no continuity for it), counter read failing (budgeted sessions are not renewed and run down), class gone or restarted (dropped),
   DHCP evidence missing (not renewed), journal unreadable or altered (continuity stays off), record from another boot / tenant / site / appliance / older assignment / future (refused and deleted), persisted checksum mismatch (refused).

### 3.1 The byte budget

acctd supplies, per entitlement with a data quota: `RemainingBytes = quota - consumed_data_bytes (as read at AsOf) - margin`, where `margin = sum over its live sessions of (down + up kbps) * 125 * 60 s`
(60 s = accounting lag + spacing of netd's counter samples, at the session's *rate ceiling*; a session with no ceiling is assumed 1 Gbit/s each way, which usually makes its budget 0, i.e. no continuity).
netd's baseline for a session is the counter of the **latest sample at or before AsOf** (counters sampled every 30 s while plans are healthy, 12 kept), so bytes between AsOf and the takeover are counted, and bytes
between the sample and AsOf are counted twice (conservative). When `sum(counter - baseline) >= RemainingBytes` the entitlement's sessions are denied. A counter that went backwards counts in full.

Overshoot after the budget is spent is bounded by one governor tick plus the counter read: `sum over sessions of (down + up kbps) * 125 * ~7 s` (a 10/3 Mbit/s session: about 11 MB). Shaping bounds the rate; the margin is
already taken off the budget.

### 3.2 Accounting reconciliation (no bytes fabricated or lost)

acctd ingests **absolute** per-class counters with netd's class generation (`ingest_absolute_counters`, checkpoint delta). The tc classes and their counters live in the kernel and survive an acctd outage and a netd
restart (generation carried forward when the kernel inventory verifies), so when acctd returns the next pass ingests the whole outage as one delta, attributed to the entitlement the session is bound to. netd's own metering only
*decides*; it never writes accounting. Two cases lose bytes, both in the safe direction (under-counting, never over-granting): a host reboot (counters reset; nothing is continued across a reboot anyway), and a class
re-created by netd (new generation, delta restarts). **To be demonstrated in the lab: accounted bytes equal generated bytes after a 60-minute outage** (lab plan scenario 1).

## 4. Settings

`public.enforcement_policy.outage_continuity_minutes` (migration 0198), the existing audited singleton behind Hotel Admin > Network > Client access protection (permission `network`, password step-up, reason
required, append-only `enforcement_policy_changes`, operator audit entry), served by edged with its bounds.

| | |
|---|---|
| Default | 60 minutes |
| Bounds | 0 to 120, whole minutes (table CHECK, `enforcement_policy_set` validation, edged validation, UI validation: four places, one number) |
| 0 | strict: guests lose access about 90 s after acctd stops, as before |
| Scope | the appliance (one site) |
| Takes effect | acctd reads it every 30 s; the plan after that carries the new window (or the explicit off); no restart |
| Explanation | served with the setting; the UI also states the effect of the typed number in words |
| Upgrade note | an existing appliance gets 60 on upgrade (the PO's stated priority). Set 0 to restore the previous behaviour exactly. |

The upper bound is justified by the threat table and the residuals in section 8, not by technical capacity: nothing in the mechanism changes between 60 and 120; the exposure to the residuals scales linearly with the window.

## 5. Threat table

| # | Threat | Control | Residual |
|---|---|---|---|
| T1 | Staff disconnect during an outage is ignored | scd's existing `/v1/flows/end` now journals (fsync + rename) then denies and flushes conntrack, before answering; governor drops the session; restart cannot revive it (journal wins over any older record) | none; if the journal cannot be written the call returns an error and the in-memory removal still happens |
| T2 | Session ended / entitlement suspended or terminated / checkout in the database while acctd is away | probe `continuity_not_entitled` every tick, mirrors the plan's entitled predicate; unknown session = not entitled | up to one tick (5 s) + probe time (<= 3 s) = **8 s**; **unbounded while the database is down** (section 8, R2) |
| T3 | Quota exhausted while acctd is away | netd meters class counters against the budget | overshoot <= about 7 s at rate ceiling per session; unrated sessions get no continuity if the quota is small |
| T4 | Quota already nearly gone when acctd died | margin at rate ceiling for 60 s subtracted; budget 0 => session excluded | none beyond the stated margin |
| T5 | Entitlement window / max session / time allowance end during the outage | per-session hard end; allowance divided by live sessions of the entitlement | none |
| T6 | Idle timeout | not enforced during an outage | R3: a session can idle past its idle timeout until the window/hard end; no extra bytes |
| T7 | Address reassigned to another device (DHCP) during the outage | Kea ownership proven each renewal (`addressOwned`, MAC match); foreign => removed; unknown => not renewed | Kea itself unavailable: sessions run down at the lease (<= 90 s) |
| T8 | Persisted record tampered with (longer deadline, other session) | SHA-256 over the record, 0600 root-only, scope + boot id + assignment generation checks, monotonic sanity, deleted when refused | root can rewrite file and checksum, but root can also write the nft set directly; no new capability |
| T9 | Stale record resurrects old authorizations | never creates elements; deadlines are boot-relative so a reboot invalidates the record (boot id) | none |
| T10 | Wall-clock manipulation | all deadlines monotonic | none |
| T11 | Hostile/garbled plan block | own hash; window range; AsOf range; only the single uid-authenticated producer can submit | the producer is trusted for the *content* (as it is for the plan) |
| T12 | acctd vouches with stale accounting | block withheld when the last accounting pass is older than 30 s or degraded; netd keeps what it holds, extends nothing | sessions already in the record keep their (older) deadlines/budget baselines: conservative |
| T13 | Operator sets 0 or lowers the window | explicit-off block at the next plan clears the record; a shorter window applies to blocks accepted after it | a record accepted before the change keeps its older (longer) deadlines for at most the old window if acctd is also gone at that moment |
| T14 | netd itself fails | crash: systemd restarts it in seconds, record restored (same boot), set elements survive their remaining lease; hang: worst case `min(90 s, remaining)` after the last renewal | netd down for longer than the remaining lease: guests lost at the lease (nothing can renew without netd) |
| T15 | Licence suspended/expired/deleted | licence is enforced at admission and by `hello`; continuity admits nobody and keeps only existing sessions | R4: no licence-driven session teardown exists to preserve |

## 6. Failure matrix

| Failure | Guests online | New sign-ins | Revocation | Quota | Recovery |
|---|---|---|---|---|---|
| acctd crash (2.5 s restart) | unaffected (inside lease) | refused while down | n/a | n/a | next plan |
| acctd hang (watchdog about 25 s) | unaffected | refused | n/a | n/a | watchdog restart |
| acctd down for minutes to the window | **kept to the window** | refused | staff disconnect immediate; DB revocations <= 8 s | metered by netd | acctd returns: plans resume, delta ingested, governor stands down |
| acctd down past the window / hard end | removed at the deadline, exactly | refused | n/a | n/a | acctd returns: sessions re-admitted by the normal path (re-sign-in or existing session re-authorized if still entitled) |
| database down (acctd, scd, edged degraded) | kept to the window | refused (cannot be recorded) | **nothing can be changed in the database**; a disconnect that reaches scd still performs the kernel half and is journalled (console reachability with the DB down untested); probe fails and is reported | metered by netd | database returns: plan path resumes; anything that changed meanwhile is reflected immediately |
| netd crash/restart (same boot) | elements live out their lease (<= 90 s); netd restarts in seconds, restores the record, renews | refused | journal honoured | baselines persisted in the record | automatic |
| netd hung | stale access <= `min(90 s, remaining)` | refused | **not applied until netd answers** | not metered | systemd restart |
| appliance reboot (DB up) | offline until netd, acctd and the plan path are back (boot time + next plan), then re-admitted by the normal path | after services start | n/a | counters reset | normal |
| appliance reboot, database still down | **offline** (the kernel set is empty and nothing creates elements); see R1 | refused | n/a | n/a | database + acctd back |
| clock jump / NTP step | unaffected (monotonic) | | | | |
| Kea down | renewals stop (ownership unprovable): lease <= 90 s | | | | Kea back |

## 7. Licensing

Licence capacity (`max_concurrent_online_guests`) and licence validity are enforced by scd at **admission** and through the `hello` licence enforcement (a deleted appliance stops admitting). No code path tears
existing sessions down on licence expiry, so continuity (which admits nothing, creates nothing and renews only existing elements) neither adds nor removes a licence protection. A licence that expires inside the window
keeps already-online guests until the window ends, as it already keeps them until they leave today. Exactness of capacity is untouched: sessions in the record were already counted when admitted.

## 8. Measured limitations and the exact Product Owner decisions required

R1. **Appliance reboot is not covered.** After a reboot the kernel set is empty and the tc classes are gone. Re-creating them from a persisted record would mean authorizing guests with no accounting origin and no class
generation (acctd/DB unavailable to register them), i.e. unmetered traffic, which the Product Owner's rule rules out. Measured behaviour: guests are offline from the reboot until netd, acctd and the database are up and the
next plan is accepted (normal boot time, the lab measures it). *Decision D-1:* accept this (recommended), or authorise a separate design that persists the nft set and class parameters and meters from the persisted ledger
only, for reboots where the database stays down. I do not recommend it: the cases it would add (reboot AND database down AND within the window) are the cases where nothing can be reconciled afterwards.

R2. **While the database is down almost no revocation can happen, and none can be seen.** scd's disconnect endpoint still performs the kernel half when the database step fails
(`cmd/scd/main.go`, "the kernel step runs either way"), and that half now journals in netd, so a disconnect that reaches scd is honoured. But the Admin Console needs the database to sign an operator in and to
audit, so whether an operator can *reach* that endpoint with the database down was not tested and must be assumed not; PMS checkouts cannot be recorded, entitlements cannot be changed. The probe fails and is reported (`database_probe_ok:false`). Everything that was true when the database went away stays true until the window or the sessions' hard ends. This is inherent, not a defect of the
mechanism: access that "should" end because the *outside world* changed (a checkout arriving at the PMS while the database is down) ends at the session's hard end, normally the entitlement window. *Decision D-2:* accept that
exposure for up to the configured window (the reason the default is 60 and the maximum 120, not unbounded), **or** set the window to 0 for hotels that prefer to lose guests rather than risk it.

R3. **Idle timeout and plan "disconnect oldest device" style controls are not applied during an outage.** They end a session, not bytes; the session still stops at the window/hard end. *Decision D-3:* accept (recommended).

R4. **Licence teardown does not exist today** (section 7); nothing to preserve or block.

R5. **A budgeted session is excluded from continuity if there is no counter sample at or before the block's AsOf** (the first minute after netd starts, or a
class that existed but could not be read) and **an entitlement with a small quota and unrated sessions gets none** (the 60 s margin at 1 Gbit/s exceeds the quota).
Conservative; guests in those cases fall back to the 90 s lease. A session whose class was created after the sample is no longer excluded: it is metered from zero
(section 1a, F2).

R6. **Overshoot of a spent budget is about 7 s of traffic at the rate ceiling per session.** Stated in 3.1. The budget has the uncertainty margin already subtracted.

R7. **Unmetered lag at the very start of an outage**: the budget is the database's figure at AsOf (up to 30 s old at the last plan) minus 60 s at the rate ceiling. Bytes that flowed in that gap beyond the ceiling are
impossible (shaping); bytes that flowed within the ceiling are in the margin. If shaping were not in force (a class missing), the session is dropped ("class is gone").

*The mission asked whether 60-120 minutes needs accepting a material tradeoff.* With R1-R3 stated and decided, no further tradeoff is needed for 120. If the Product Owner declines D-2, the supportable ceiling is
whatever interval the hotel is willing to accept a database-down checkout exposure for; the setting already expresses exactly that.

## 9. Where it is and how it is proven

Code: `internal/shapeplan/continuity.go` (block, hash, validation), `internal/enforce/continuity.go` (budgets and hard ends from durable state), `cmd/acctd/continuity.go` (setting watch, withholding while accounting is stale,
explicit off), `cmd/netd/phase3_continuity.go` (+ `_http.go`; governor, persistence, journal, probe), `cmd/netd/flows.go` (journal on staff disconnect), migrations 0198 (setting) and 0199 (probe), grants in
`deploy/gatep/svc-netd-iamv2-enforcement-grants.sql`, edged `resources_enforcement_policy.go`, hotel-admin `network/enforcement/page.tsx`.

Tests (package level, real code paths, fake kernel unless stated): `cmd/netd/phase3_continuity_test.go` (acctd gone to window and no longer; strict == 90 s; hard end; quota removes only that entitlement; no baseline; unreadable
counters; staff disconnect immediate and durable across a restart with an older record; probe revocation; database-down keeps to the bound and reports; never re-creates; reassigned address; broken clock; wall-clock jump;
record tamper / other boot / other scope / future / corrupt journal; netd restart keeps the original deadline; plans returning; bad blocks; block-less plan extends nothing), `phase3_continuity_http_test.go`,
`phase3_continuity_realkernel_test.go` (**real nft in a privileged container**: kernel timeouts never reach past the deadline, element gone at the deadline, disconnect removes from the real set, a removed element is not
re-created), `internal/enforce/continuity_test.go` and `continuity_integration_test.go` (**real PostgreSQL 16/TimescaleDB, full schema**: default 60, bounds 0..120, audit, probe exactness including svc_netd privilege and
no write, producer rows and budget; migrations 0198/0199 down and up again), `cmd/acctd/continuity_test.go`, `cmd/edged/enforcement_continuity_test.go`, `hotel-admin/test/enforcement-policy.test.tsx`.

Not yet demonstrated (needs the lab): 60 and 120 minute real-time runs, accounted bytes equal generated bytes after an outage, behaviour at 1,700 sessions (governor cost), netd restart under load, database-down with the
real services. See the lab plan.
