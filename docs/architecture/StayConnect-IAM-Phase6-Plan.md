# StayConnect IAM — Phase 6 Plan
## Guest Device Self-Service + the AGGREGATE_ONLINE_TIME package time mode

**Status:** AUTHORIZED and IN_PROGRESS under Product-Owner decision **D25**, start transition **T0057**
(2026-08-15). Baseline master `09e67156fb6cb286fe47fe632a368a3c4e4c6d23`. Branch
`phase/6-device-selfservice-and-time-modes`, one pull request → `master`. Delivered **DARK**: the phase-level
capability gate defaults OFF. Target acceptance maturity: **VERIFIED LIVE-DARK** on the **development**
appliance.

**Explicitly NOT in scope (fail-closed, governance-enforced):** any Production environment mutation or
contact; real guest traffic; real PMS, provider or financial traffic; Phase-4 financial enablement; IAM-v2
authentication cutover; paid-access activation; Phase 7; unrelated network changes; merging the Phase-6 PR;
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
| §6.1 terminal precedence | **Partially built.** `enforce.EnforceExpiries` terminates on `TIME` (window elapsed) and `DATA` (quota crossed), at the **true** time, through `terminate_entitlement_at_boundary`. | Aggregate exhaustion is a **new terminal cause** that must enter the same single atomic path — not a parallel one. |
| §6.4 idempotent accounting | **Present for bytes.** `accounting_records UNIQUE(session_id, sample_seq)` makes replay a no-op; `session_counter_watermarks(source_epoch, last_up, last_down, sample_seq)` detects counter resets and re-baselines. | Time has **no** watermark. Phase 6 adds the same shape for online seconds, or replay/reboot double-counts. |
| §6.3 device slots | **Present.** Per-entitlement distinct-device slots in `entitlement_devices(status AUTHORIZED\|DISCONNECTED)`; license capacity is the outermost gate; `entitlement_device_authorizations` carries the interval history (Phase-5 D5-1). | A **guest-driven** release path does not exist. Phase 6 adds one that frees the slot by the same `DISCONNECTED` mechanism the system already uses, so no new slot-accounting concept appears. |
| §6.3 "a device-management surface" | **Named by the contract, never built.** | This is Phase 6's first half. |
| Per-appliance product settings | **No appliance-scoped product-setting store exists.** `public.tenants.auth_methods` is a tenant-scoped local-first jsonb bundle; `public.appliances.metadata` is untyped. | Phase 6 adds a typed, appliance-scoped, additive settings table rather than overloading either. |

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

### 2.4 Offline-only removal, re-checked atomically

A device is **offline** when it holds no session in `active` or `PENDING_ENFORCEMENT`. Removal:

1. locks the entitlement row, then the device's session rows;
2. **re-reads** the offline state inside that lock — an offline→online race therefore loses safely, because
   the winner is decided by the database, not by the check that preceded it;
3. sets `entitlement_devices.status='DISCONNECTED'` with a distinct `disconnected_reason` naming guest
   self-service, closes the open `entitlement_device_authorizations` interval, and writes an audit row;
4. **preserves** the `devices` row, every `accounting_records` row, every authorization interval and every
   audit row. The slot is freed; the evidence is not touched.

`PENDING_ENFORCEMENT` counts as online deliberately: a grant still converging at the edge is not yet safe to
release, and treating it as offline would let a guest free a slot whose kernel authorization is still landing.

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

### 3.3 Exhaustion at the true time

When the budget is crossed inside a tick, the terminal instant is computed **within** the tick rather than
stamped at sweep time: with *n* eligible sessions consuming, the remaining budget is consumed *n* times
faster, so exhaustion lands at `accounted_through + remaining/n`. This matches the existing rule that a data
crossing is recorded at the sample that crossed it, not when the sweep noticed.

Termination goes through the **existing** `terminate_entitlement_at_boundary` path with a new reason, so the
precedence rule in §6.1 — the first reached of {window end, data cap, hard expiry, checkout, admin,
aggregate} triggers **one** atomic terminal transition — stays a single code path.

### 3.4 Immutability

The mode and its parameters are pinned in the **immutable package revision snapshot** at quote time, exactly
as `end_mode` and `window_ends_at` already are. Existing revisions carry `VALIDITY_WINDOW` in their snapshots
and are therefore untouched by construction, not by a compatibility branch.

---

## 4. Milestones

| # | Content |
|---|---|
| **M1** | the pre-live clarification; this reconciliation; additive migration and configuration boundaries; the foundation for the appliance setting and the time-mode state |
| **M2** | the complete Guest Device Self-Service vertical slice — setting, Hotel Admin, guest surface, offline-only removal, authorization, race safety, auditing, throttling, adversarial tests |
| **M3** | the complete AGGREGATE_ONLINE_TIME vertical slice — immutable configuration, consumption semantics, outer window, entitlement/session integration, guest/admin presentation, concurrency/replay/reboot/accounting regression |
| **M4** | hardening: full Phase-3/4/5/6 regression, adversarial matrix, least-privilege and local-first verification, backup, real scratch restore, rollback rehearsal, reboot verification, zero-stale governance, authoritative CI and evidence artifacts, controlled DEVELOPMENT-appliance LIVE-DARK validation |

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
