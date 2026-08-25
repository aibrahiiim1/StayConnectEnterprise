# Room-auth materialization readiness — design spike

**Status: DESIGN ONLY. Nothing here is implemented.** Produced under the Product Owner's records-and-design
authorization of 2026-08-26, following the live PMS acceptance receipted by T0097.

---

## 1. The defect, stated precisely

`p3_feed_authorizes` treats `sync_status = 'IN_SYNC'` as evidence that the guest roster is usable. It is not.
`IN_SYNC` means *a generation was published*; it says nothing about whether that generation's events have been
applied to `iam_v2.stays`, which is the table Room resolution actually reads.

Measured live on 2026-08-25:

| Moment | Time | Fact |
|---|---|---|
| Publish barrier | `23:17:57.801404` | `published_resync_generation` 19→20, `IN_SYNC`, `COMPLETE` |
| First generation-20 event applied | `23:17:58.012291` | 211 ms **after** publish |
| Last generation-20 event terminal | `23:18:01.289397` | **3.487993 s** after publish |

During that window `room_auth_ready` was `true` and roughly 129 in-house guests did not yet exist in
`iam_v2.stays`. No guest attempted authentication (`auth_resolutions = 0`), so the race was not run — but it is
real, and it is not confined to full sync.

## 2. THE ORDINARY LIVE BACKLOG IS THE SAME RACE

This is the finding that decides the shape of the fix, and it was not visible before reading the applier.

`stayengine.ProcessNext` claims work with:

```sql
WHERE se.processing_status='PENDING'
  AND (se.admission_kind='LIVE' OR se.resync_generation <= r.published_resync_generation)
ORDER BY se.received_at, se.id
FOR UPDATE OF se SKIP LOCKED
```

under a per-interface `pg_advisory_xact_lock`. Two consequences follow.

**Staged resync events are unclaimable until publish.** `resync_generation <= published_resync_generation` is
false for the open generation, which is exactly why the drain begins at the barrier and not before. The
staging design is doing its job.

**LIVE events are always claimable, and always lag.** In steady state a `GO` is admitted `PENDING` and the
guest stays `IN_HOUSE` in `iam_v2.stays` until the applier reaches it. Nothing on the auth path consults
`stay_events`. So a checked-out guest can authenticate for the duration of ordinary applier lag, every day,
with no full sync involved.

**Therefore a resync-generation watermark is NOT sufficient.** A `materialized_resync_generation` column would
close the 3.5-second full-sync window and leave the continuous one wide open. Readiness must be defined over
*all* occupancy-affecting pending events, not over generations.

## 3. What can serve as the watermark

**FIAS provides no sequence number.** `SequenceVersion` is written as a literal `0` in
`worker.go`; there is no PMS-supplied ordering field to read. Any "PMS sequence" would be invented, and the
design does not invent one.

What *is* authoritative is the applier's own ordering. Because application is serialized per interface by the
advisory lock and ordered by `(received_at, id)`, the stream is totally ordered and gap-free: if the applier
has finished event *E*, every event ordered before *E* is already terminal.

That gives two candidate invariants.

### Option A — direct predicate: "no occupancy-affecting event is pending"

```sql
NOT EXISTS (SELECT 1 FROM iam_v2.stay_events se
             WHERE se.tenant_id=$1 AND se.site_id=$2 AND se.pms_interface_id=$3
               AND se.processing_status='PENDING')
```

*Correct by construction* — it is the literal statement of what we need, needs no new column, no new writer
and no new privilege, and cannot drift from reality because it reads reality.

*Cost*: it puts an `EXISTS` on `stay_events` into the authentication hot path. **There is currently no index
supporting it** — `stay_events` carries only `stay_events_pkey`, `stay_events_scoped_identity`,
`se_live_identity` and `se_resync_identity`, none of which lead with `processing_status`. A partial index
would be required:

```sql
CREATE INDEX CONCURRENTLY se_pending_by_interface
    ON iam_v2.stay_events (tenant_id, site_id, pms_interface_id)
 WHERE processing_status = 'PENDING';
```

A partial index on a status that is nearly always empty is small and cheap to maintain, and the lookup becomes
an index probe returning nothing.

### Option B — durable watermark advanced by the applier

Add to `pms_interface_runtime`:

```
materialized_through_at    timestamptz   -- received_at of the last terminal event
materialized_through_id    uuid          -- its id, to break ties within a timestamp
```

advanced by `stayengine` inside the same transaction that finishes an event. Readiness becomes "no admitted
event is ordered after the watermark".

*Cost*: a second source of truth that can drift; a new writer on the runtime row; and a Gate-P question,
because `stayengine` runs inside `pmsd` and `svc_pmsd` would need write access to columns it does not
currently touch. It also does not remove the need to know whether anything is pending — it only caches it.

### Recommendation: **Option A**, with the partial index

One authoritative predicate, no new state, no new writer, no drift, no privilege change. Option B is a
performance optimisation of Option A and should only be considered if the index probe measurably hurts, which
on a table where `PENDING` is normally zero rows it should not.

This is deliberately *one* invariant rather than several UI approximations, per the instruction.

## 4. Answers to the specific questions

**Is a `materialized_resync_generation` watermark sufficient by itself?** **No.** It addresses only the
full-sync window and leaves the continuous LIVE-backlog race untouched (§2).

**What if LIVE events arrive after DE while generation-20 events are still draining?** Ordering holds. A LIVE
event admitted after DE has a later `received_at`, and the single ordered claim under the advisory lock applies
the staged backlog first. No reordering occurs — this part is already correct.

**Steady-state LIVE ingestion where a GO is durable but not applied?** The guest remains `IN_HOUSE` in
`iam_v2.stays` and would authenticate. This is the everyday form of the same defect.

**Can Room auth accept stale Stay state during ordinary applier backlog today?** **Yes.** Proven by
construction from the applier claim query and the auth path, which never intersect.

**Must readiness account for all occupancy-affecting PENDING events?** **Yes** — that is the whole conclusion
of §2.

**What durable watermark proves materialization consumed everything auth-relevant?** The absence of `PENDING`
rows for the interface (Option A), or `(received_at, id)` of the last terminal event (Option B). Both derive
from the applier's own total order, not from the PMS.

**Which process may advance it?** Under Option A, none — there is nothing to advance. Under Option B, only
`stayengine`, in the same transaction as `finishEvent`, so the watermark cannot outrun the work it describes.

**How is it guarded against stale worker/generation ownership?** Option A needs no guard: it is a property of
the durable rows, not of a worker's belief. Option B would need the existing `runtime_generation` CAS, and a
stale worker's advance must fail exactly as its other writes do.

**What happens to offline/local-first auth if the PMS disconnects while materialization is behind?** This is
the case that must not regress D39. The correct behaviour: **readiness is independent of transport.** If the
socket drops with events still pending, the mirror is *incomplete*, not merely stale, and must not authorise —
the applier keeps draining without the socket, so this resolves on its own within seconds rather than
requiring the PMS back. Once drained, the local-first branch authorises exactly as it does today. Local-first
is preserved and **no cloud availability is introduced anywhere in this design.**

## 5. Proposed patch plan (NOT IMPLEMENTED)

| Area | Change |
|---|---|
| **Schema** | One migration: the partial index `se_pending_by_interface`. No new columns under Option A. |
| **Applier** | **No change.** Ordering and terminality are already correct; the defect is that nothing reads them. |
| **`p3_feed_authorizes`** | Add the `NOT EXISTS` pending term, applying to **both** the live and local-mirror branches — it is a mirror-trust property, not a transport property. |
| **`room_auth_ready`** | Mirror the same term in the edged readiness CASE, with a new bounded code `MATERIALIZATION_BEHIND`. Distinct from `NOT_IN_SYNC`: the guest list is arriving, not absent. |
| **Hotel Admin** | `COMPLETE` must mean *published and materialized*. Either hold `PUBLISHING` until the drain finishes, or add a terminal `FINALIZING` stage. Operator wording: "Applying the guest list". |
| **`last_sync_in_house_count`** | **Remove.** It measures IN_HOUSE at the publish barrier — i.e. the roster the sync replaced — and contradicts the live figure on the same screen. Once `COMPLETE` genuinely means materialized, the ordinary live IN_HOUSE value is correct and sufficient. Drop the column, its write, its API field and its UI row. |
| **During full sync** | Auth closed from DS (already, via `IN_SYNC`) through to drain completion (new). |
| **During LIVE backlog** | Auth closed while any event is pending — the continuous case, closed for the first time. |
| **While disconnected** | Local-first unchanged, plus the same pending term. §4. |
| **Migration/rollback** | `CREATE INDEX CONCURRENTLY` up, `DROP INDEX` down; the predicate change is a `CREATE OR REPLACE FUNCTION` with the prior body as the down. Rollback restores today's behaviour exactly, including the race. |
| **Gate-P** | **No privilege change under Option A.** `svc_scd` already holds EXECUTE on `p3_feed_authorizes` and SELECT on `stay_events`; confirm the latter before implementing rather than assuming it. |

### Tests required

*Predicate*: pending LIVE event ⇒ refused, on both branches · pending staged event of an unpublished
generation ⇒ **not** counted (it is not claimable and must not block) · drained ⇒ authorised · disconnected
with a pending event ⇒ refused · disconnected and drained ⇒ authorised (D39 preserved).

*Guest path*: sign-in refused while a GO for that stay is pending, and succeeds once applied — the everyday
race, asserted end to end.

*Readiness*: `MATERIALIZATION_BEHIND` surfaces while draining and clears after; never reported as an outage.

*Full sync*: `COMPLETE` is not reported until the drain is terminal; the counts shown at COMPLETE match the
materialized roster.

### Controlled PRE-LIVE acceptance

Trigger one operator Full Resync with the page open; observe `COMPLETE` appearing only after the last event is
terminal; assert `room_auth_ready` is false for the whole window and true immediately after; confirm the
displayed in-house count equals `SELECT count(*) … IN_HOUSE`; confirm counters remain 0/0/0. No guest sign-in
required to accept this — the predicate can be evaluated read-only.

## 6. Minimal scope for approval

1. One migration: the partial index.
2. `p3_feed_authorizes`: one `NOT EXISTS` term.
3. `room_auth_ready`: the same term, one new bounded code.
4. Hotel Admin: `COMPLETE` means materialized; remove `last_sync_in_house_count`.
5. The tests above.

No new table, no new writer, no new privilege, no cloud dependency, no change to the applier, and no change to
the staging or publish design — which the live acceptance proved correct.
