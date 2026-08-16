# StayConnect IAM — Phase 6 Plan
## Guest Device Self-Service + the AGGREGATE_ONLINE_TIME package time mode

**Status:** **ACCEPTED AND CLOSED at verified LIVE-DARK maturity** — Product-Owner decision **D26**, closure
transition **T0061** (2026-08-16), at delivery head `1bdf9bfbd96b7f0264d634183d5cc8e69904cbb9`. Authorized
under **D25** / **T0057** (2026-08-15) from baseline master `09e67156fb6cb286fe47fe632a368a3c4e4c6d23`, branch
`phase/6-device-selfservice-and-time-modes`, one pull request → `master`. Delivered **DARK**: the phase-level
capability gate defaults OFF, and acceptance authorizes no enablement on any environment. This plan is kept as
the record of what was planned and what was delivered against it.

**Explicitly NOT in scope (fail-closed, governance-enforced):** any Production environment mutation or
contact; real guest traffic; real PMS, provider or financial traffic; Phase-4 financial enablement; IAM-v2
authentication cutover; paid-access activation; unrelated network changes;
marking Phase 6 accepted or closed.

---

## 0. The pre-live clarification this phase starts from

Recorded separately and first, as **D24 / T0056**: StayConnect **has not yet entered real hotel guest/staff
production operation**. No guest and no hotel user currently depends on either the prior public-schema IAM or
`iam_v2` for live service; the system is under active development and controlled testing.

The **architectural** facts are unchanged and are restated rather than softened: `iam_v2` is **not cut over**,
holds **0 rows**, and has **no service routed to it**; the public-schema path remains the **currently
configured** legacy authentication/routing baseline. What was corrected forward is the operational
implication — "the SOLE PRODUCTION authority", "the live authority", "removing it would break all guest
authentication" — which described the configuration correctly while reading as though real hotel guests are
being served today.

Genuine historical evidence is **not** rewritten: the supervised Protel FIAS Gate-3A acceptance test, every
transition receipt and every dated decision entry stand exactly as written.

---

## 1. As-built reconciliation against the FINAL contract

This section is the M1 deliverable that decides how much of Phase 6 is *new* and how much is *activation of
something already built*. Each row was read from the schema and the runtime, not assumed.

| Contract element | As-built today | Phase-6 gap |
|---|---|---|
| §6.1 `time_accounting_mode` on the entitlement | **Present.** `iam_v2.entitlements.time_accounting_mode text NOT NULL`, stamped from the package-revision snapshot by the grant kernel (`coalesce(snap->>'time_accounting_mode','VALIDITY_WINDOW')`, migrations 0021/0024). | The column exists and is populated; **nothing reads it**. Phase 6 gives it meaning. |
| §6.1 aggregate balance | **Column present.** `entitlements.consumed_online_seconds bigint NOT NULL DEFAULT 0 CHECK (>= 0)` with `usage_version`. | Never incremented. Phase 6 accrues into it. |
| §6.1 plan-level quota | **Present.** `service_plan_revisions.time_quota_seconds`, immutable and append-only. | Not consulted for time. Phase 6 reads it as the aggregate budget. |
| §6.1 outer hard-validity window | **Present.** `entitlements.window_ends_at`, stamped once and never moved. | Phase 6 reuses it *unchanged* as the outer window of an AGGREGATE_ONLINE_TIME entitlement — no second window concept is invented. |
| §6.1 terminal precedence | **Partially built.** `enforce.EnforceExpiries` terminates on `TIME` (window elapsed) and `DATA` (quota crossed), at the **true** time, through `terminate_entitlement_at_boundary`. | Aggregate exhaustion enters that **same** atomic path with the contract's existing `TIME` reason. The contract's `terminal_reason` set is **not widened** — that is contract vocabulary and belongs to the Product Owner; the distinction is carried by append-only `entitlement_termination_evidence`, which unlike an enum value also carries the budget, the consumption and the crossing instant. |
| §6.4 idempotent accounting | **Present for bytes.** `accounting_records UNIQUE(session_id, sample_seq)` makes replay a no-op; `session_counter_watermarks(source_epoch, last_up, last_down, sample_seq)` detects counter resets and re-baselines. | Time has **no** watermark. Phase 6 adds the same shape for online seconds, or replay/reboot double-counts. |
| §6.3 device slots | **Present.** Per-entitlement distinct-device slots in `entitlement_devices(status AUTHORIZED\|DISCONNECTED)`; license capacity is the outermost gate; `entitlement_device_authorizations` carries the interval history (Phase-5 D5-1). | A **guest-driven** release path does not exist. Phase 6 adds one that frees the slot by the same `DISCONNECTED` mechanism the system already uses, so no new slot-accounting concept appears. |
| §6.3 "a device-management surface" | **Named by the contract, never built.** | This is Phase 6's first half. |
| Per-appliance product settings | **No appliance-scoped product-setting store exists.** `public.tenants.auth_methods` is a tenant-scoped local-first jsonb bundle; `public.appliances.metadata` is untyped. | Phase 6 adds a typed, appliance-scoped, additive settings table **foreign-keyed to the enrolled appliance**, so managed state for an appliance that does not exist cannot be created, and an audit row **foreign-keyed to the authenticated operator**, so an actor a caller can choose is not accepted as an actor. Scope and identity are derived server-side from trusted assignment/authentication context, never from a request body. |

**Conclusion.** The schema was built for both features and the runtime implements neither. Phase 6 is
therefore *additive and activating*: three of its four core state elements already exist and are already
immutable-by-construction, which is why existing revisions cannot be reinterpreted retroactively — their
snapshots already say `VALIDITY_WINDOW`.

---

## 2. Guest Device Self-Service

### 2.1 Two controls, deliberately not merged

| Control | What it is | Default |
|---|---|---|
| **Per-appliance product setting** `guest_device_self_service` | the long-term control a hotel operates | **OFF** |
| **Phase-level capability gate** `STAYCONNECT_PHASE6_*` | the safe deployment boundary until activation is separately authorized | **OFF**, and absent from every env file and unit |

Collapsing them would mean that shipping the product control also shipped the activation. The guest surface
requires **both**: gate ON *and* setting ON.

### 2.2 Local-first

The setting lives in the **site database on the appliance** and is read there. No Central Control Plane call
is on the path, so an appliance with no uplink answers exactly as one with an uplink. This is tested by
running the whole slice with Central unreachable.

### 2.3 The authorization rule that makes the feature safe

**The caller's subject is derived, never supplied.** The guest is already authenticated; the server resolves
the entitlement from that authenticated context and lists only its devices. There is no `mac`, `entitlement`,
`stay`, `room`, `pms_interface` or `profile` parameter *at all* — absent, not validated — which is the same
rule Phase 5 arrived at for post-stay, for the same reason: a parameter that does not exist cannot be
validated wrongly.

### 2.3a What is audited, exactly

**`RELEASE` is audited in every outcome, including refusals. `LIST` is not audited at all.**

The schema briefly named a `LIST` action that no code path ever wrote — a standing claim that a guest's list
requests were investigable when nothing recorded one. Migration 0035 narrowed the action set to the truth
rather than stretching the implementation to meet it: listing reads and changes nothing, and auditing reads
would have required granting the guest surface a write on its own audit table, which is precisely the
privilege the Phase-6 audit removed on purpose.

Refusals are audited deliberately. A guest repeatedly attempting to release somebody else's device, or
hammering one that keeps coming back online, is exactly the pattern an operator needs to see, and a log
containing only what worked cannot show it.

### 2.4 Offline-only removal, re-checked atomically

A device is **offline** when it holds no session in `active` or `PENDING_ENFORCEMENT`.

**The as-built design (migration 0032).** An earlier draft of this section described the release as "lock the
entitlement row, then the device's session rows". That was wrong in a way worth recording: *`FOR UPDATE` locks
rows, and it cannot lock the absence of a row* — so locking the sessions that already exist says nothing about
the one a concurrent admission is about to insert. The real design has three parts:

1. **A shared L3 serialization boundary.** The release takes the entitlement row lock first, in the global
   lock order, and then performs the release **through `iam_v2.deauthorize_entitlement_device`** — the
   primitive migration 0010 declares to be one of only two approved ways to close an authorization interval,
   and the counterpart of the `authorize_entitlement_device` the production grant path calls. Release and
   admission therefore serialize on the *same* lock, taken by the *same* primitives. No release-only lock is
   invented: a lock the session writer does not also acquire synchronises nothing.
2. **No hand-written interval mutation.** The release does not write `entitlement_devices` or
   `entitlement_device_authorizations` itself. A second implementation of an invariant is a second place for
   it to drift, and the first one already had — it never declared the `device_auth` scope and skipped the
   "an interval may not close before it opened" rule.
3. **A structural guard.** A session may not be `active` or `PENDING_ENFORCEMENT` while its binding is not
   `AUTHORIZED`. This makes `DISCONNECTED + live session` **unrepresentable** rather than merely unlikely, so
   the invariant does not depend on any writer getting its lock ordering right.

**Reconnect after release goes through normal authorization.** A released device cannot simply resume: the
guard refuses a live session on its binding, so it must pass through `authorize_entitlement_device` again,
which re-checks the device limit under the entitlement lock and opens a **new** interval. The closed interval
stays closed, and the history shows two intervals rather than one revived one.

Throughout, the release **preserves** the `devices` row, every `accounting_records` row, every authorization
interval and every audit row. The slot is freed; the evidence is not touched.

`PENDING_ENFORCEMENT` counts as **non-removable** deliberately: a grant still converging at the edge is not
yet safe to release, and treating it as offline would let a guest free a slot whose kernel authorization is
still landing. This is the *removal-safety* predicate and it is deliberately **not** the accounting one —
`PENDING_ENFORCEMENT` never consumes online-time budget. See §3.2z.

### 2.5 The three surfaces, as built (M2)

**The guest sees only what is using their allowance.** The listing returns the caller's own entitlement's
**AUTHORIZED** bindings — a released device is not in it. The first implementation returned released bindings
too, reasoning that a device vanishing might leave a guest unsure the removal worked; walking the assembled
flow showed that backwards. The guest is already told in words that the device was removed and its place is
free, and a released device reappearing under a heading that says *the devices using your internet access*,
annotated as un-removable, contradicts what just happened. The durable history lives in the binding row, the
authorization intervals and the audit — none of which is a guest-facing screen.

Each device is presented by **when it was last used and whether it is connected**. No MAC: on a shared
network that is a stable identifier for somebody's phone. The opaque device id travels in the release request
and is never rendered. Every refusal — online, not yours, already released, throttled, switched off, not
deployed — is one identical sentence, because the API collapses them for the same reason.

**The portal panel is hidden until the appliance answers with a list.** On an appliance where the capability
is not deployed or the hotel has it off, the guest sees an ordinary success page and learns nothing about
whether device management exists here.

**The operator surface is authorization, not just authentication.** The setting routes are mounted through
`mountResource`, so `resourcePermission` and the role matrix decide. `guest-device-self-service` follows
`auth-methods` exactly — write for `site_admin` and `hotel_it_manager`, read for the two desk roles and the
two read-only roles, nothing for `voucher_operator` — because which capabilities the property offers its
guests is a configuration decision rather than a desk action. No new role was invented, and
`docs/ROLE_AND_SCOPE_MATRIX.md` now carries the row that `auth.go` says it defines, **checked cell for cell
by test in both directions**.

**The Hotel Admin screen shows two facts, never one.** The product setting ("this property offers it") and
the deployment state ("available in this release") are separate panels plus a sentence stating what the
current combination means. No wording may suggest that switching the setting on deploys anything: an operator
who turns something on, sees it confirmed, and tells guests it exists has been misled by the product.

**The public source identity comes from the connection and only from the connection.** `middleware.RealIP`
was installed on the portal router and had to be removed. On this architecture guests reach the portal
directly through nftables DNAT, so `X-Forwarded-For` and its relatives are entirely guest-controlled — and
that address derives the device, which derives the entitlement. The Phase-6 surface takes no subject
parameter precisely so identity comes from the connection; a rewritten `RemoteAddr` handed the parameter
back. If a real reverse proxy is ever introduced the fix is **not** to re-add it, but to trust exactly that
proxy's address and strip client-supplied forwarding headers at the edge.

### 2.6 What the four combinations must do

| deployment gate | product setting | guest-visible result |
|---|---|---|
| OFF | OFF | routes **absent** (404) |
| OFF | ON | routes **absent** (404) — the hotel's decision is recorded and deploys nothing |
| ON | OFF | uniform `UNAVAILABLE`, indistinguishable from any other refusal; no durable change |
| ON | ON | the capability, scoped to the caller's own entitlement |

Proven end to end against a real PostgreSQL over real HTTP, on a subject produced by the **real Phase-3 grant
path**. An appliance with no setting row at all behaves as OFF: opt-in is a decision the hotel makes, not a
default in the guest's favour.

---

## 3. AGGREGATE_ONLINE_TIME

### 3.1 What it is

An **aggregate online-time budget** *plus* an **outer calendar/hard-validity window** — "120 online minutes,
valid for 7 days". Only eligible online sessions consume; with no eligible session online the timer does not
move; idle-reaped and terminated sessions stop consuming. **Shared device-minute semantics** from the
contract: two eligible devices online for ten minutes consume **twenty** aggregate minutes.

### 3.2 Idempotency, which is the whole problem

Bytes are safe today because they are **cumulative counters with a durable watermark**. Wall-clock time has
no counter, so Phase 6 gives it the same shape: a per-session durable `accounted_through` instant. Each tick,
in one transaction per entitlement:

* every eligible session accrues `now - accounted_through`, clamped at zero;
* the watermark advances to the same `now` that was charged;
* the entitlement's `consumed_online_seconds` increases by the **sum** across sessions, which is what produces
  device-minutes without a second mechanism.

A replayed tick charges zero because the watermark already moved. A reboot charges only the real elapsed
interval. A late sample cannot reopen a terminal entitlement — the same rule §6.4 already states for bytes.

### 3.2z Two different predicates, and they are not interchangeable

**`PENDING_ENFORCEMENT` is not proof of network access.** It means a grant whose kernel authorization is
still converging at the edge — the guest may have no forwarding at all yet. Charging it would bill a guest
for minutes during which the network did not carry their traffic.

So Phase 6 uses **two separate predicates**, and never one as a proxy for the other:

| Predicate | Includes | Used for | Why |
|---|---|---|---|
| **Accounting eligibility** | `active` only | consuming the aggregate budget | only `active` states that enforcement is applied and the network is carrying the session. Time is charged for access delivered, not access intended. |
| **Removal safety** | `active` **and** `PENDING_ENFORCEMENT` | refusing guest self-service removal | a grant still converging must not have its slot released underneath it; the kernel authorization is still landing, and releasing it would create a device whose enforcement outlives its binding. |

The asymmetry is deliberate. For *charging*, an unproven state must not count. For *removing*, an unproven
state must count — because in each case the unproven state is resolved in the direction that cannot harm the
guest. A single "is it live?" predicate could not do both, and using one would have been the bug.

### 3.2a A watermark proves idempotency — it does NOT prove the time was online

**The gap, stated plainly.** `accounted_through` makes a replayed tick charge zero. It says nothing about
whether the interval it charges was time the guest was actually *online*. Charge `now - accounted_through`
naively and an appliance that was powered off for six hours bills six hours the moment it comes back — the
session row still says `active`, because nothing was running to end it.

**So elapsed wall-clock is not evidence of online time. Observation is.** Accrual is bounded by how recently
the accounting service last *observed* the session:

* each tick charges `min(now - accounted_through, MAX_CHARGE_PER_TICK)`, where the bound is a small multiple
  of the tick interval;
* a gap larger than the bound means the service was not running, so the unobserved remainder is **not
  charged** — it is re-baselined and recorded as a skipped interval, which is evidence rather than silence;
* the session must be **accounting-eligible now**: state `active` (never `PENDING_ENFORCEMENT` — see §3.2z)
  with its entitlement live. A session that was idle-reaped or ended while the service was down stops at its
  `ended` instant, not at `now`; a session still converging has not started consuming at all.

The failure this rules out is asymmetric on purpose. Charging unobserved time takes minutes from a guest who
was not using them and cannot be undone without an audited adjustment; declining to charge it gives away at
most one bound's worth per outage, to a guest who may genuinely have been online. **Given the choice, the
system under-charges.**

**Adversarial proof required before M3 is implemented:** nothing is charged while a session is
`PENDING_ENFORCEMENT` · nothing is charged for the interval *before* enforcement became active · nothing is
charged if enforcement never succeeds and the session ends from `PENDING_ENFORCEMENT` · nothing is charged
after enforcement ends · clean restart mid-session · crash/reboot with the session row left `active` · a gap
far longer than the bound · idle reap during the gap · restoration from backup to an older watermark (the
monotonic trigger refuses the backwards move) · and the ordinary case, where a continuously observed active
session accrues exactly the elapsed time.

### 3.3 Exhaustion at the true time

**The billable interval, per session.** A session contributes `[accounted_through, ceiling]`, where the
ceiling is the **earliest** of: now · the session's own `ended` · the entitlement's immutable
`window_ends_at` · any terminal instant the sweep already knows about, chiefly the **DATA crossing**. The
per-tick observation bound then trims the charged end; whatever it trims is recorded in
`online_time_skipped_intervals` rather than charged. A session with no watermark has never been observed by
this path, so its first tick charges nothing and baselines instead.

**The crossing is piecewise, over the union of those intervals.** An earlier draft of this section described
it as `accounted_through + remaining/n` for *n* eligible sessions. That is exact only when every contributor
starts together and runs the whole interval — and staggered watermarks, a device joining late and a device
disconnecting mid-tick are all ordinary. The burn rate is not a count of sessions; it is **how many
intervals cover that instant**, and it changes at every interval boundary.

So the boundaries are walked in order: for each segment, `rate` is the number of intervals covering it and
the segment contributes `duration × rate`. The first segment whose contribution reaches the remaining budget
contains the crossing, at `p0 + (remaining − accumulated)/rate`. Segments no interval covers contribute
nothing — nobody is online, so nothing burns. This matches the existing rule that a data crossing is recorded
at the sample that crossed it, not when the sweep noticed.

Why the instant matters as much as the total: the sweep compares it against the other terminal candidates to
decide **which condition ended the entitlement**. A total that is right to the second, carrying an instant on
the wrong side of the window or the data crossing, produces a true number attached to a false account of what
happened to the guest.

**DATA truth stays in one place.** The tick never computes a data crossing. The expiry sweep already derives
it from the running sum over attributed samples; it runs that query **first** and passes the instants in as
per-entitlement caps, which the tick treats exactly like the outer window. Duplicating that algorithm would
create two answers that drift.

**Termination is unchanged.** It goes through the **existing** `terminate_entitlement_at_boundary` path with
the contract's **existing `TIME` reason** — no new terminal vocabulary, because AGGREGATE_ONLINE_TIME is a
*time mode* and exhausting its budget is a time termination. §6.1's precedence rule — the first reached of
{window end, data cap, hard expiry, checkout, admin} triggers **one** atomic terminal transition — is
unchanged and stays a single code path; the merge keeps the earliest instant, so a losing condition
contributes nothing. Which time rule ran out is recorded in append-only
`entitlement_termination_evidence`, bound by trigger to the entitlement's actual terminal transition, so it
can neither describe a termination that did not happen nor disagree with the one that did.

### 3.3a The mode is owned by the immutable revision, and gated twice

The accounting mode is a property of the **immutable plan revision** and of nothing else. An omitted mode is
`VALIDITY_WINDOW` — a default, not a branch, which is why every revision published before Phase 6 keeps its
meaning with no compatibility path. A grant tier **may not override it**: an offer-time override would let one
revision be granted under two different accounting rules, which is exactly the retroactive reinterpretation
immutability exists to prevent.

Two separate gates sit on top, and they answer different questions:

| Gate | Question | Off means |
|---|---|---|
| **Publication** (`CommerceAdmin`) | may this build publish a revision in the new mode? | the mode is refused at publication, and a positive `time_quota_seconds` is required when it is allowed |
| **Acquisition** (`TimeModeAcquirable`) | may a NEW quote/purchase/entitlement be created in it? | every acquisition path refuses, with one shared reason |

Acquisition is gated on **every** free path — the PMS/stay grant and the Phase-2 free-commerce
quote/confirm — through one shared rule, because two entry points each carrying their own copy is how they
come to disagree. Confirm re-checks, above the consume step, so a quote minted while the capability was on
and presented after it went off is refused **without** consuming the quote or the guest's auth context.

The reason is arithmetic rather than policy: acctd creates no aggregate accrual **for entitlements that do
not exist**, so an entitlement created in that mode on a build that never accounts for it would never consume
its budget and never exhaust — an unlimited package by accident. Nothing already durable is touched: an
immutable aggregate revision keeps existing and keeps its meaning, and entitlements granted earlier under it
are not reinterpreted, re-moded or deleted.

### 3.3b Accounting for what already exists is a safety invariant, not feature activation

**Accrual is DATA-DRIVEN, and deliberately not gated on the deployment flag.** The obvious wiring — accrue
only while the aggregate flag is on — has a failure mode worse than the feature being off. An appliance that
had already granted aggregate entitlements, whose flag then went away through a rollback, a config change or
a half-applied deployment, would stop consuming their budgets. Nothing would exhaust. **A guest holding a
finite two-hour package would silently hold unlimited access**, with consumption frozen and no evidence that
anything had stopped.

So the three concerns are separated, and only the first two are feature activation:

| Concern | Gated by the flag? | Why |
|---|---|---|
| Publishing a revision in the mode | **Yes** | offering something new is activation |
| Acquiring a NEW quote/purchase/entitlement in it | **Yes** | the same, from the guest's side |
| Guest and operator surfaces | **Yes** | screens and routes are the product |
| **Accounting an entitlement that already exists** | **No** | not accounting it is what creates unlimited access |

**An empty aggregate set produces no writes.** The tick runs every sweep and iterates over live entitlements
in that mode; where there are none — every appliance today, because the acquisition gate makes creating one
impossible while the flag is off — it writes no consumption, no watermark, no skipped interval and no
evidence. That is what dark means here: no Phase-6 *behaviour*, rather than "the accounting for existing
entitlements is switched off".

Both halves are proven at the sweep level: a live aggregate entitlement still consumes, exhausts and is
terminated with **no Phase-6 flag set anywhere**, and a validity-window appliance is untouched by the same
sweep.

### 3.4 Immutability

The mode and its parameters are pinned in the **immutable package revision snapshot** at quote time, exactly
as `end_mode` and `window_ends_at` already are. Existing revisions carry `VALIDITY_WINDOW` in their snapshots
and are therefore untouched by construction, not by a compatibility branch.

---

## 4. Milestones

| # | Content |
|---|---|
| **M1** | the pre-live clarification; this reconciliation; additive migration and configuration boundaries; the foundation for the appliance setting and the time-mode state |
| **M2** | **COMPLETE** — the Guest Device Self-Service vertical slice: setting, Hotel Admin, guest surface, offline-only removal, authorization, race safety, auditing, throttling, adversarial tests, all four gate/setting combinations end to end, and the local-first proof with Central unreachable |
| **M3** | **COMPLETE** (T0059) — the AGGREGATE_ONLINE_TIME vertical slice — immutable configuration, consumption semantics, outer window, entitlement/session integration, guest/admin presentation, concurrency/replay/reboot/accounting regression |
| **M4** | hardening: full Phase-3/4/5/6 regression, adversarial matrix, least-privilege and local-first verification, backup, real scratch restore, rollback rehearsal, reboot verification, zero-stale governance, authoritative CI and evidence artifacts, controlled DEVELOPMENT-appliance LIVE-DARK validation |

### M4 evidence, and what it does and does not cover

Repository- and database-side M4 gates, all green and all repeatable against a database that has already
seen them:

| Gate | What it proves |
|---|---|
| `phase6_rollback_rehearsal.sh` (65) | **the whole slice**, 0047 → 0030 down and back up, crossing the 0032 boundary the plan records as load-bearing; the quiescence preconditions are checked rather than assumed — no live aggregate entitlement, no appliance still offering the device capability, no released binding carrying a live session — and they have demonstrably refused. The runner decides each migration on its **exit status**: the earlier version matched output text, so a missing file and a database it could not reach both produced no `ERROR` line and were counted as passes. It is mutation-proven to fail hard — `pass=2 fail=5`, aborting at the first down migration — when pointed at a container that does not exist |
| `phase6_backup_restore.sh` (18) | pg_dump → **DROP** → pg_restore with pg_restore's own exit status and error lines checked, then functions, privileges, guards and a marker row verified on the restored copy, and the gate removes the row it wrote |
| `phase6-flag-coherence.sh` (6) | scd/acctd/edged agree on every Phase-6 flag, no child without its master, and the accounting prerequisite: an appliance may not offer online-time budgets with its accounting daemon inactive |
| foundation / device / aggregate / least-privilege (50 / 22 / 49 / 65) | the durable invariants, measured as the real roles |
| the integration matrix | green **twice consecutively on one database**, which is what makes it a regression suite rather than a one-shot |

### What the DEVELOPMENT appliance has proven, and what it has not

**Proven** (see [the appliance evidence](../acceptance/StayConnect-IAM-Phase6-Development-Appliance-Evidence.md)):

* **pre-runtime flag coherence, 6/6**, read from the appliance's own systemd units — every Phase-6 flag OFF
  and agreeing across scd, acctd and edged;
* a **pre-Phase-6 backup**, taken and read back (1187 TOC entries);
* the **Phase-6 schema applied DARK**: 75 → 81 tables, 0 → 16 `p6_` functions, and `iam_v2` still holding
  **zero rows**, with the 0032 guard present and no service errors;
* **a real reboot**, after which all five services are active, coherence is 6/6 again, the schema is unchanged
  at zero rows and no appliance setting row exists.

**The Phase-6 Go runtime is now deployed on that appliance as well** — scd, acctd, edged and portald, built
from this branch, checksum-verified on the appliance, with the previous binaries retained at `*.bak-prep6`.
All services are healthy and each reports Phase 6 OFF in its own words; runtime flag coherence is 6/6 and
`iam_v2` still holds zero rows. The runtime is deployed and **inert**.

**Three environments, and they are not the same thing:**

| | Schema | Runtime | Phase-6 capability |
|---|---|---|---|
| **Production** | untouched, prior accepted baseline | untouched | not present, not contacted |
| **DEVELOPMENT appliance** | Phase-6 schema applied (0030→0047) | Phase-6 binaries **and** the dark Hotel Admin bundle deployed | validated, then **every capability OFF**, coherent, verified across a second reboot |
| scratch database | full schema, disposable | n/a | gates only |

**Now proven on that appliance as well**, in one supervised run under D25 that enabled the capabilities and
restored them (42 proofs, no failures — see the evidence document):

* the **Hotel Admin standalone bundle is deployed**, dark, through the existing deploy script's atomic release
  symlink. `NEXT_PUBLIC_PHASE6_ADMIN` is build-time state and this bundle was built without it, so the
  operator screens are compiled out of the artifact rather than hidden by a runtime check;
* the **gate/setting matrix** across both controls, against the real `scd` socket: master alone mounts
  nothing; the guest child without the Phase-3 auth arm refuses to start at all; the arm without the Phase-6
  child still leaves the route absent; both together mount it; and the per-appliance setting is consulted on
  every request, taking effect with no restart;
* the **release rules and their audit**, including the refusal of an id that is not the caller's own;
* **local-first** with the real Central address blackholed;
* **accrual, exhaustion and termination by the running `acctd`** — and the safe-disable invariant proven on a
  live entitlement: with the aggregate capability **disabled**, the already-durable budget is still accounted,
  so a rollback cannot turn finite access into unlimited access;
* **restoration verified against the runtime** — the coherence gate, a real socket request, service health,
  the accounting owner, and the Hotel Admin release by path and content hash — then **a second reboot**, after
  which all six services are active, coherence is 6/6, the guest route is absent, and no appliance is left
  with the capability enabled.

The controlled run created only a zero-price `ADMIN_GRANT` on a reserved stay: **no paid access, and no real
guest, PMS, provider or financial traffic**.

**Deliberately not claimed:** a full in-place restore of the appliance's `stayconnect_site`. The backup was
taken with the sanctioned script and **proven restorable** into a scratch database on the appliance, but an
in-place restore requires a manifest signed off-appliance with the registry root key and verified against the
appliance's pinned anchor. That is key custody, and working around it to make a test pass is the kind of
shortcut this phase has refused elsewhere.

**What the appliance taught that no test could.** Three findings came out of running the surface as the real
service role, rather than as a role that owns the schema the way every test connects:

1. `svc_scd` held `SELECT` on `iam_v2.devices` and nothing more, so the guest surface failed on its first
   line. Device resolution is an **upsert** — a device row is created the first time the appliance sees it —
   and 0033's grant list, written for "listing a guest's devices is a read", was true of the listing and false
   of the request. **Migration 0047** grants `INSERT` plus a column-level `UPDATE` on `mac`, `last_seen` and
   `last_ip` only, and `SELECT` on `entitlements` and `service_plan_revisions`; it asserts the refusals too —
   no `DELETE` on devices, no write of any kind to entitlements, no rewriting an immutable plan revision.
2. Neither `scd` nor `acctd` could open a controlled operation, so both refuse to serve the Phase-3 auth arm
   the Phase-6 guest surface depends on. That is **Phase-3 provisioning**, not Phase-6: the validation harness
   grants it for the duration of the run and revokes it afterwards.
3. `scd` **refuses to start** with the guest surface enabled while the Phase-3 arm is off. That is fail-closed
   and correct — a half-wired guest path should not run at all — and it is now proven rather than assumed.

### Over budget now, but the crossing cannot be dated (migrations 0045-0046)

`p6_exhaustion_instant` will not invent an instant. It uses a stamped exhaustion time if one exists; failing
that it walks the audited adjustments for a **lower bound** on consumption, which can prove exhaustion but
never dates it precisely; and failing that it returns NULL. That is the right answer to *when did this end*,
and it left the wrong answer to a different question — because "the historical instant is unknown" was
allowing an entitlement that is **over its budget right now** to keep carrying traffic.

The two questions are separated. History stays unknown; access stops. An entitlement over budget with an
undatable crossing is moved to **SUSPENDED** — an existing, approved status with existing, approved semantics
— through `apply_entitlement_transition(..., 'AGGREGATE_OVER_BUDGET')`. `authorize_entitlement_device` refuses
anything that is not ACTIVE and `PlanForSite` selects only ACTIVE, so there is no new session and nothing
forwards. Nothing terminal is written: no `terminated_at`, no `terminal_reason`, no TIME evidence. If durable
evidence later establishes the true crossing, the entitlement converges to the single TIME terminal path at
the instant the evidence proves.

**0046 is the same principle one level down.** 0045 was careful at the parent and then closed that
entitlement's devices and sessions with `ENTITLEMENT_ENDED` — writing the false claim it had just refused to
write, in exactly the rows an operator opens first when a guest asks why their access stopped. The writer now
closes them as `ENTITLEMENT_SUSPENDED`. Both columns are free text carrying machine codes, so no constrained
vocabulary is widened; `entitlements.terminal_reason`, which *is* constrained, still receives nothing.

### M4 rollback prerequisite — recorded now, because it is easy to discover too late

**Before migration 0032 is rolled back, the Phase-6 capability must be OFF and its routes fail-closed.**

0032's down migration is *faithful*: it restores the pre-fix schema, which means it removes the structural
session-binding guard and returns the release to its own hand-written deauthorization. In that state the
release/admission defect is **representable again** — a `DISCONNECTED` binding can carry a live session. A
rollback performed while the guest surface is still serving would therefore reintroduce the exact defect the
migration exists to prevent, on live traffic.

This is not an argument for a dishonest down migration. A rollback that quietly kept the fix would make the
pair untrustworthy in both directions. It is an argument for ordering: **disable, then roll back.**

---

## 5. Test obligations

Real PostgreSQL wherever the invariant is database- or concurrency-dependent. Synthetic fixtures are confined
to controlled test environments and are never presented as live topology, live PMS state or live acceptance
evidence.

**Device self-service:** OFF means no guest exposure · ON shows only the caller's own devices ·
client-supplied MAC/room/stay/entitlement/PMS/profile cannot select another subject · online removal refused ·
offline→online concurrent removal race · two simultaneous removals · slot freed exactly once · durable
history preserved · same-device reconnect · replacement at the max-device limit · license-capacity
interaction · checkout/grace interaction · Post-Stay interaction · Cross-PMS transfer interaction ·
reboot/offline operation with Central unavailable.

**Time modes:** VALIDITY_WINDOW regression unchanged · the aggregate timer pauses with zero eligible
sessions · one-device and multi-device consumption · overlapping sessions use device-minute semantics ·
reconnect and duplicate samples do not double-count · source counter reset/re-baseline · simultaneous
exhaustion from multiple sessions · hard expiry racing aggregate exhaustion · checkout racing aggregate
consumption · supersession/transfer cannot split or resurrect balances · terminal entitlements never reopen
from late samples · existing revisions behave exactly as before.
