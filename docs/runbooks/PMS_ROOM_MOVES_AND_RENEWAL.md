# PMS room moves and package renewal

Migration 0213 (Product Owner, 2026-11-06). Contract references: Phase 0 §7 and check F1 (a room move preserves the
entitlement, its devices and its quota), §7.2 (reinstatement), FIAS 2.20 p. 46 (a room move may be sent as a check-out
and a check-in, in either order).

## How a room move is recognised

Only from the reservation's own records, never from timing:

| What arrives | Stay before | What OneGate does |
|---|---|---|
| Check-in (GI) of the reservation into another room | In house | Room move: the stay's room changes; nothing else does |
| Check-out (GO) of a room the reservation has already left | In house (in its new room) | **Vacates the old room only.** No checkout, no After Check-out Access |
| Check-out (GO) of the stay's own room | In house | A real checkout, unchanged: After Check-out Access as configured |
| Check-in (GI) into **another** room after that checkout | Checked out | Reinstated, and the converted package is **continued** (below) |
| Check-in (GI) into the **same** room after a checkout | Checked out | Reinstatement as before (§7.2): the guest signs in again |
| Check-out with no room, or a stay with no recorded room | In house | A real checkout, unchanged |

## What a continuation keeps and never does

`iam_v2.restore_room_move_access` re-checks the evidence in the database: the checkout record of the episode cites a
check-out event of this stay naming room A, and a check-in event of the same stay names room B. Then it:

- ends the episode's After Check-out Access (superseded), and creates the continuation of the converted package: the same
  package and service plan, **the same end time** (a package whose time had already run out is not continued: `EXPIRED`),
  and an allowance equal to **what the original and the After Check-out Access left** (none left: `DATA_USED_UP`);
- re-authorizes the devices the package held at the checkout and any the After Check-out Access held, and moves their
  live sessions across without a disconnect;
- writes a **zero-price** `ROOM_MOVE_RESTORE` purchase: no settlement, no posting, no room charge.

It never replaces other live access of the stay (`LIVE_ACCESS_PRESENT`), never runs twice for one episode, and records
every decision in `iam_v2.room_move_restorations` (append-only, one row per checkout episode). A failure is logged by pmsd
(`stayengine: room move not restored`) and leaves the reinstatement applied; the PMS feed is never held on it.

## Package renewal

Each package version has **Room stays can buy it again** (`renewable`) and **Maximum per stay**
(`max_purchases_per_stay`, empty = unlimited, counting the first purchase), in Admin Console > Internet packages. One
database rule, `iam_v2.stay_package_purchase_refusal`, decides for the guest offer list and for the purchase itself:

- never bought by the stay: may buy;
- bought before: only when renewable, nothing of it is live (`STILL_ACTIVE`) or being settled (`PURCHASE_IN_PROGRESS`),
  and the stay is under its maximum (`LIMIT_REACHED`); otherwise `NOT_RENEWABLE`.

Each purchase carries the next `purchase_seq`; the unique index includes it, so two simultaneous purchases of one renewal
leave exactly one purchase and at most one room charge. A **Once per client** limit on the package still applies.

## What the guest is told

| Situation | Sentence |
|---|---|
| Stay verified, nothing on offer | Your stay is confirmed, but no Internet package is available to your room right now. Please contact the site team. |
| Stay verified, room charge withheld while the PMS link refreshes (a resync, a dropped link) | Your stay is confirmed. Charging to your room is paused for a moment while the property system updates. Please try again in a minute. |

Room charge is still never taken while the link is refreshing (contract: a stale mirror or a resync in progress blocks
money); only the sentence changed. The page asks once more by itself after two seconds.

## One-time recovery of stays affected before 0213

Run only for a stay whose package a room move converted, after a verified backup:

```
SELECT * FROM iam_v2.restore_room_move_access('<tenant>','<site>','<stay>', <episode>, NULL, 'RECOVERY', <supersede>);
```

`<episode>` is the `lifecycle_version` of the stay's checkout record for the move. `RECOVERY` finds the stay's first
applied check-in (live or roster resync) into another room after that check-out itself. An older episode is continued only
when every later checkout of the stay was itself a proven room move (`ROOM_MOVE_SUPERSEDED` otherwise). `<supersede>` true lets an unlimited, stay-long original replace a
live package of the stay that it fully covers (as fast, as many devices); the replaced package's purchase and any room
charge stay exactly as they are. Refunds are a separate decision and are never made here.
