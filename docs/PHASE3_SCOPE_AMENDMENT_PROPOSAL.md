# Phase-3 scope-amendment proposal — Hotel-Admin operator surfaces

**Status:** PROPOSED — awaiting Product-Owner decision. Nothing here has been implemented or omitted
silently; this document exists because the Phase-3 Plan lists operator surfaces that the current software
candidate does not deliver, and the standing instruction is to *record* such a gap rather than quietly drop it.

**Scope of this document:** Hotel-Admin (operator) UI only. It proposes nothing about the guest path, the
enforcement path, the accounting path or the database contract, all of which are delivered.

---

## 1. What is delivered

These operator surfaces exist, are backed by real `edged` routes against `iam_v2`, and are covered by both
API/PostgreSQL tests and real-browser tests:

| Surface | Page | Covered by |
|---|---|---|
| Stays list, occupants, folios | `/stays` | API/PG + browser |
| Stay-Event review queue, refusal reasons | `/stay-events` | API/PG + browser |
| Checkout-Grace policy: selector, publish, version conflict | `/checkout-grace` | API/PG + browser |
| Operational alerts: triage, acknowledge, resolve, concurrent change | `/operational-alerts` | API/PG + browser |

Accessibility (named controls, one heading per page, labelled filters) is asserted for all four.

---

## 2. What the Plan lists and this candidate does not deliver

Each row states what the operator cannot currently do, and — more importantly — **what they would have to do
instead**. That second column is the actual decision: an absent UI is only acceptable when the fallback is
safe and auditable.

| Plan surface | Operator impact today | Fallback while absent |
|---|---|---|
| **PMS Interfaces** (create, lifecycle state) | Cannot see or change which interfaces exist | Provisioned by deployment; visible only in the database |
| **Interface Revisions + publish state** | Cannot see which configuration is live, or publish a new one | Applied by deployment |
| **Write-only Secret rotation** | Cannot rotate a PMS credential from the UI | Rotation is a deployment action |
| **Routing (network → interface) + intersection validation** | Cannot see or change which guest network resolves against which interface, and cannot be warned about overlapping routing | Configured in the database; the resolver still fails closed on an ambiguous or unmapped network, so a mistake degrades to "no access", never to "wrong guest's access" |
| **Transport / continuity / sync / occupancy health** | Cannot see per-interface connection health in the UI | `pmsd` logs and `/v1/health`; a degraded interface is INDETERMINATE to the resolver, so guests are refused rather than mis-resolved |
| **Ingestion backlog** | Cannot see how far behind Stay-Event application is | `pmsd` logs and the durable inbox table |
| **Resolution evidence** (why a given attempt resolved as it did) | Cannot answer a guest's "why can't I get online?" from the UI | `iam_v2.auth_resolutions` holds the recorded outcome and candidate vector; answering requires database access |
| **Source conflicts** (two interfaces claiming one stay) | Cannot review conflicts in the UI | The resolver refuses (AMBIGUOUS) and records it; no guest is granted on a conflict |

**The common property:** every absent surface is an *observability or configuration* surface. None of them is
a safety control. In every case the underlying system fails closed without the UI: an unmapped network, a
degraded interface, an ambiguous match and a stale interface all produce the same uniform "not verified"
answer for the guest, and all are recorded durably. The cost of the gap is **operator blindness during
Increment-9 live validation**, not a route to incorrect access.

---

## 3. Why this is being proposed rather than built

Building these eight surfaces to the standard the delivered four meet — real `edged` routes, API/PG tests,
browser tests, accessibility assertions, and the write-path safety a Secret-rotation UI in particular demands
— is a substantial body of work that would not be finished to that standard inside this candidate. Shipping
them at a lower standard is the worse option: a half-tested operator surface that *writes* PMS configuration
or rotates a credential is a larger risk than not having it, because operators would trust it.

Specifically, **write-only Secret rotation** should not be delivered without its own review: it is the one
item on the list that mutates a credential, and it needs an explicit decision about who may rotate, what is
recorded, and what happens to in-flight connections using the previous generation.

---

## 4. The three options

**Option A — Accept the reduced Hotel-Admin scope for the DARK candidate.**
Phase 3 is accepted with the four delivered surfaces. The eight above move to a follow-on increment, tracked
explicitly. Increment-9 live validation proceeds with database-level observability for the absent surfaces.
*Risk:* during live validation, diagnosing a resolution or interface problem requires database access rather
than the UI, which is slower and puts an engineer in the loop.

**Option B — Deliver the read-only subset first, defer the write surfaces.**
Build Interfaces (read), Revisions + publish state (read), routing (read) + intersection validation, health,
backlog, Resolution evidence and source conflicts. Defer Secret rotation and all configuration *writes* to a
separately reviewed increment. This removes the operator blindness that matters during live validation while
avoiding the item that needs its own safety review.
*Cost:* an additional increment before Phase-3 acceptance. *Recommended if Increment-9 is imminent.*

**Option C — Deliver the full Plan list before acceptance.**
No scope reduction. Phase-3 software acceptance waits for all eight surfaces including Secret rotation, with
the same test standard as the delivered four, and with a separate design review for the rotation write path.

---

## 5. What is NOT being proposed

* No reduction of the guest path, enforcement, accounting, or database contract.
* No weakening of any test standard for what *is* delivered.
* No change to the DARK default, the flag set, or the Gate-P boundary.
* No claim that the absent surfaces are unnecessary — only that their absence is an observability cost, and
  that the Product Owner should decide whether to pay it now or later.

**This proposal requires an explicit Product-Owner decision (A, B or C) before Phase 3 can be marked accepted
on the Hotel-Admin dimension.** Until then, the Acceptance Matrix records these surfaces as awaiting that
decision — not as passed, and not as silently out of scope.
