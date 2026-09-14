# Standing exception: one departure for a stay this appliance never saw

**Status:** OPEN and not closable locally. Recorded 2026-09-13.
**Scope:** one `stay_events` row, interface `protel-fias` on the PRE-LIVE appliance.

## What it is

On 2026-08-23 the PMS announced a Guest-Out **carrying a valid reservation number** — a real departure,
properly reported. The appliance could not place it, because it holds no stay under that reservation. The
event is recorded `MANUAL_REVIEW` with review code `GO_UNKNOWN_STAY`.

It is the **only** unanswered case. The other 13,716 were roster snapshots — the connector observing empty
rooms during a resync — and were dispositioned as `NOT_A_DEPARTURE_ROSTER_SNAPSHOT`. This one is different
in kind: the PMS made a genuine statement about a genuine guest, and this system cannot match it.

## The evidence, and what is missing

Established read-only:

| question | answer |
|---|---|
| Stays under that reservation, any status | **0** |
| Arrival (`GI`) or change (`GC`) records ever carrying it | **0** |
| Events mentioning it at all | **1** — its own departure |
| Stays ever recorded in that room | **0** |
| Times that room was reported occupied under *other* reservations | 63 |
| First data this appliance ever received | 2026-08-22 00:14:43, at resync generation 11 |
| Rooms named by the first two sweeps | **501 of 587** |

So the room is real and ordinary, and the reservation was never announced as arriving. The appliance joined
an already-running property mid-stream, and its first two sweeps were demonstrably partial.

**The missing evidence is an arrival record for that reservation** — a `GI` or `GC` the appliance never
received — or confirmation from the PMS that the stay never existed on their side.

## Why it is not being resolved locally

Closing it would require inventing the arrival it never saw. That is fabrication: a stay would be created
from a departure, and the system would then hold a guest record no PMS ever sent. The room cannot stand in
for the identity either — that is precisely the room-keyed inference that produced a 14,125-record backlog
and wrongly closed stays.

Roster reconciliation cannot reach it: reconciliation closes stays the mirror holds and the roster does not,
and there is no stay here to close.

## What would resolve it

1. **Ask the PMS** for the stay history of that reservation. If it existed, the arrival record is what is
   missing and Protel can re-send or confirm it.
2. **Accept it as a known gap** from the appliance's mid-stream start, and record that decision.

Either is a Product-Owner decision. Until one is taken the case stays visible under **PMS connection → Advanced diagnostics → Roster reconciliation**,
unhidden and uncounted against any "resolved" total — a case with no answer stays on the list.

## What must not happen

* Do not synthesise a stay, an arrival, or a checkout boundary for it.
* Do not dispose it as a roster snapshot. It is not one: it arrived LIVE and carried a reservation.
* Do not suppress it from the operator view to make a counter read zero.
