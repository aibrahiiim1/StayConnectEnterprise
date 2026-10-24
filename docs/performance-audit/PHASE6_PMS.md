# Phase 6: PMS (pmsd)

**Audited:** master `653363d7`. Code and repository only: nothing was run against PRE-LIVE, Protel or any PMS, and no PMS traffic was sent.

**Covers:**
- how a PMS record reaches the appliance:
  - the FIAS adapter (`internal/pmsd/adapter_fias.go`);
  - admission to the inbox, `iam_v2.stay_events` (`internal/pmsd/pg.go`);
  - the stay applier (`internal/pmsd/applier.go`, `internal/stayengine/pg.go`);
  - checkout conversion (`internal/checkout`).
- Full resyncs: staging, publication, coverage and roster reconciliation (`iam_v2.pms_roster_reconcile`).
- The room sign-in feed check (`iam_v2.p3_feed_authorizes`, migration 0150).
- The posting relay to the PMS (`internal/pmsd/financial_relay.go`, `internal/postinghandoff`).
- The items earlier phases handed on:
  - Phase 2 D2-M4 (the interface lock during a resync);
  - Phase 0 H5 (roster batching);
  - Phase 5 (checkout conversion, the posting hand-off, and posting throughput).

**How:**
- **Database:** a throwaway full-schema database (`scripts/lib-fullschema-testdb.sh`), limited to 2 CPUs and 4 GB with the reference settings.
- **Code driven:** pmsd's own repository and the real stay applier, run from a temporary integration test that was deleted afterwards. The test admitted records as the FIAS adapter builds them.
- **Data:** generated only:
  - 370,000 already-processed events on the interface, as many as PRE-LIVE had (migration 0149);
  - a 500-room roster, synced twice;
  - a wave of 100 check-outs.
- **Probes:** while the test ran, two probes ran beside it:
  - the room sign-in feed check, every 100 ms;
  - the sign-in's lock on the interface runtime row, every 20 ms.
- **Not measured:** the PMS's own sending pace, which only PRE-LIVE can show (Phase 11).
- **Compliance:** checked against the [README](README.md#compliance) rules.

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 1 | 2 | 1 | 0 |

**Main result.** Room sign-in is refused across the whole hotel whenever the stay mirror has any applied-but-unprocessed PMS event, and the applier works through events one transaction at a time (D6-H1).

- **After a resync:**
  - Every resync is followed by about **10 s of refused room sign-ins per 500 rooms**.
  - It makes no difference whether anything changed: the applier re-applies the whole roster.
  - Every reconnect starts a resync. On PRE-LIVE on 2026-10-24, the link dropped eight times in ten minutes.
- **After any live PMS event:** room sign-in is refused for up to about 2 s, because the applier waits 2 s when idle.

**Two smaller costs** stretch the resync itself:
- **Roster reconciliation scans every event the interface has ever received.** It is 336 ms per read today and grows without limit. One missing condition fixes it (D6-M1).
- **Each roster record is its own transaction:** 8–10 ms per record (D6-M2).

**D2-M4 answered.** pmsd does not hold the interface lock through a resync, only for each record: the sign-in's lock wait was 5–7 ms at p95.

## Measured costs

| What | Result |
|---|---|
| Staging a 500-record roster (`StageResyncEvent`, one transaction per record) | 4.1–5.2 s; 7.7–10.1 ms p50, 11.4–13.1 ms p95 per record |
| Sign-in's runtime-row lock wait while staging | p50 1.7–3.0 ms, p95 5.4–7.2 ms, max 14–27 ms |
| Publishing a generation | 3–10 ms |
| Recording coverage | 4–11 ms |
| Roster reconciliation (`pms_roster_reconcile`), 370,000 events on the interface | 0.21–1.15 s, outcome `COMPLETED` |
| `pms_roster_of_generation`, one call | 336 ms (scans 370,600 rows); **2.1 ms** with `admission_kind = 'RESYNC'` added |
| Applying a 500-record roster, empty mirror | 10.0–10.2 s; 18.9–19.7 ms p50 per event |
| Applying the same 500 records again, nothing changed | 9.1–9.5 s; 17.1–18.1 ms p50 per event |
| Room sign-in feed check while either roster was applied | refused in **every** probe: 87–98 of 87–98 |
| 100 live check-outs: admission and continuity | 1.3 s; 12.1 ms p50; runtime-lock wait p95 6.6 ms, max 66 ms |
| Applying the 100 check-outs, with checkout conversion | 3.7–4.1 s; 35.7–38.0 ms p50; feed check refused in every probe |
| Where database time went (`track_functions`) | About 6 s of function time across the roughly 27 s run. The largest were `begin_controlled_operation` (0.48 ms a call) and the controlled-writer triggers. The rest is round trips and one commit per event. |

## Findings

### D6-H1: Room sign-in is closed across the hotel while any PMS event waits to be applied, and the applier is slow to empty the queue (High, Measured)

**Where:**
- **The rule.** `iam_v2.p3_feed_authorizes` (migration 0150) authorises a room sign-in only when **no** claimable `PENDING` stay event exists for the interface. Claimable means a LIVE event, or a RESYNC event of a published generation. The rule is site-wide: one waiting event for any room refuses every room.
- **The applier** (`internal/stayengine/pg.go`, `ProcessNext`) applies one event per transaction. For each event it takes:
  - the interface advisory lock;
  - the claim (`FOR UPDATE SKIP LOCKED`);
  - the stay lookup;
  - the decision;
  - the event's completion;
  - the checkout conversion on a departure.
- **The loop** (`internal/pmsd/applier.go:48-75`, the sleep at `:73`) sleeps 2 s whenever it finds nothing to do.
- **Every connection** starts with a full resync (`adapter_fias.go:169`, `RequireInitialResync`, then DR). Every record of the published roster then becomes a claimable event, whether or not it changes anything.

**Measured:**
- **Apply rate:** 17–20 ms per roster event, and 36–38 ms per departure.
- **A 500-room roster:**
  - takes 9.1–10.2 s to apply;
  - the same cost applies a second time with nothing changed;
  - the feed check refused every probe for the whole time.
- **100 departures:** apply in 3.7–4.1 s, again with every probe refused.

**Impact.**
- **Every reconnect closes room sign-in for the whole hotel.** It is closed for about 10 s per 500 rooms after the roster is published, and about 20 s at 1,000 rooms. Migration 0150 kept room sign-in open while the roster is received. It is still refused while the roster is applied.
- **In a reconnect storm the closures add up.** On PRE-LIVE on 2026-10-24, the link dropped eight times in ten minutes (migration 0150's own record), which is eight closures.
- **Any live PMS event closes it briefly.** A check-in, room move or check-out refuses all room sign-ins until it is applied: up to the 2 s idle pause plus the apply time.
- **The morning check-out wave** keeps the queue non-empty, at about 37 ms per departure plus the pauses.
- **Guests are refused, not kept waiting.** The refusal reaches the guest as a failed room sign-in. Phase 2 measured its retry behaviour.

**Recommended fix.** In this order:
1. **Wake the applier at once.** The worker and the applier are in the same process. Signal the applier when an event is admitted or a generation is published, instead of relying on the 2 s sleep, and keep the sleep only as a fallback.
2. **Skip what is unchanged.** When a roster record matches the stay as already mirrored (same reservation, room, names, dates, sharers), mark it `SKIPPED_DUPLICATE` without the full apply. Better still, apply a published generation set-based in one statement per kind of change, so only changed stays are touched.
3. **Batch the apply.** Apply up to 50 events per transaction, in the same `received_at, id` order. A failure rolls back the batch, and the batch is then retried one event at a time, so one bad event still goes to review alone.
4. **DECISION: scope the rule.** Refuse a room sign-in only while an event for **that** reservation or room is waiting, instead of any event. This changes guest-authentication semantics, so it is the Product Owner's decision. It is not needed if steps 1–3 bring the closure under a second.

**Proof.**
- On this phase's data, after an unchanged 500-room resync, the feed check refuses for under 1 s.
- After a live check-in, it refuses for under 100 ms.
- The 0150 integration tests still pass: the partial generation stays unclaimable, and a waiting check-out still blocks.

**Batch:** B2. CONTROLLED (PMS; guest authentication).

### D6-M1: Roster reconciliation scans every event the interface has ever received (Medium, Measured)

**Where:**
- `iam_v2.pms_roster_of_generation` and the boundary query at the top of `iam_v2.pms_roster_reconcile` filter `stay_events` by `resync_generation` without `admission_kind = 'RESYNC'`.
- The partial index made for exactly this, `se_resync_identity (tenant_id, site_id, pms_interface_id, resync_generation, external_event_identity) WHERE admission_kind = 'RESYNC'`, therefore cannot be used.
- Postgres reads the interface's whole event history through `stay_events_scoped_identity` instead.
- `pms_roster_reconcile` reads the roster three times (count, absent, protected) plus the boundary.

**Measured** with 370,000 events on the interface:
- one roster read: **336 ms**, 370,600 rows removed by the filter;
- the same read with `admission_kind = 'RESYNC'` added: **2.1 ms**, through `se_resync_identity`;
- a whole reconciliation: 0.21–1.15 s.

**Impact.**
- **The cost grows with all history.** `stay_events` is never deleted (retention anonymises; Phase 1 D1-L5), and every resync adds a roster's worth of rows (D6-L1).
- **It delays its own corrections.** Reconciliation runs after every publish, and the check-outs it queues wait for it.
- **It reaches pmsd's statement limit.** svc_pmsd's `statement_timeout` is 60 s. At this rate that is reached at about 20 million events. From then on, reconciliation fails on every resync, and stays the PMS no longer lists are never closed.

**Recommended fix.** Add `AND e.admission_kind = 'RESYNC'` to `pms_roster_of_generation` and to the boundary query. No index and no data change are needed.

**Proof.** `EXPLAIN` shows `se_resync_identity` for both. Reconciliation stays under 100 ms with 370,000 and with 3 million events. The reconcile integration tests give the same outcomes.

**Batch:** B1. CONTROLLED (PMS; a database function). **Do first:** it is two conditions.

### D6-M2: Each PMS record is its own transaction, and the socket waits for it (Medium, Measured)

**Where:**
- **Roster records:** each is staged by `insertInboxRow` (`internal/pmsd/pg.go:256`) in its own transaction, which:
  - opens the Stay writer scope;
  - proves ownership with `SELECT ... pms_interface_runtime ... FOR UPDATE`;
  - inserts;
  - commits.
- **Live records:** each takes two transactions, the admission and then `UpdateContinuity` (`worker.go:386`).
- **The socket:** the adapter reads the next record only after the current one is committed (`adapter_fias.go`, the read loop).

**Measured:** 7.7–10.1 ms per staged record, so 4.1–5.2 s for 500. 12.1 ms per live record.

**Impact.**
- **Resyncs last longer.** A resync lasts as long as the slower of the PMS's sending and the appliance's writing. While it runs:
  - postings to the PMS are refused with `LINK_RESYNCING` (`financial_relay.go`);
  - room sign-in answers from the previous roster.
- **On PRE-LIVE**, about 480 records took about 40 s, about 83 ms each (migration 0150's record). That is eight times this measurement. Whether the gap is Protel's pace or the appliance's disk cannot be told from code; Phase 11 measures it.
- **Lock contention:** each record takes the runtime row lock that room sign-in also takes, and many short locks add contention. The measured wait stays small: p95 5–7 ms.

**Recommended fix.**
1. Stage roster records in transactions of up to 50, with one ownership proof per transaction. The generation is invisible until it is published, so a partial batch is as safe as a partial sweep is today.
2. Read the socket on its own goroutine into the existing bounded queue, so the PMS is never held back by a commit.
3. Write a live record's admission and continuity in one transaction.

**Proof.** Staging 500 records takes under 1 s, and the stale-owner and duplicate tests still pass.

**Batch:** B3. CONTROLLED (PMS).

### D6-L1: Every resync keeps another full copy of every in-house guest's record (Low, Confirmed)

**Where.**
- Each published generation stores its roster as RESYNC rows in `stay_events`, with the payload: reservation, room, guest names, dates.
- The rows stay until the stay's retention anonymises them (`run_data_retention`, `pms_stay_days`).
- Eight reconnects mean eight copies of every in-house guest's name.

**Impact.**
- **Performance:** table growth of one roster per reconnect, which feeds D6-M1 and Phase 1 D1-L5.
- **Compliance:** the copies duplicate personal data beyond what the mirror needs once a newer generation is applied (GDPR Art. 5(1)(c)).

**Recommended fix.** DECISION for the Product Owner, because these rows are evidence. Once a newer generation is published and fully applied, reduce the payloads of superseded RESYNC rows to the evidence hash. The hash is already computed, as `ComputeEvidenceHMAC`. The latest generation and every row that changed a stay stay whole.

**Batch:** B4, after the decision. CONTROLLED (PMS; historical data).

## Checked, no finding

| What | Result |
|---|---|
| Posting relay | One posting in flight per interface, which FIAS requires. 5 s authorisation and up to 60 s for the PMS's answer. Refused while a resync runs and retried later, never lost. The 2 s posting tick (Phase 5) caps a lane at 1,800 an hour, which is enough at the target load. |
| Operator-resync claim on every inbound record (`ClaimResyncCommand`) | One indexed statement that locks nothing unless a command is pending. |
| Financial-resync check | Rate-limited to once a minute (`finResyncCheckEvery`), with backoff. |
| Advisory-lock keepalive (2 s ping) and applier supervisor (15 s) | Fixed small cost. |
| Heartbeats | One runtime-row update per heartbeat interval. |
| The applier's claim and the feed check against history | Both use the partial index `se_pending_claimable` (`WHERE processing_status = 'PENDING'`), so 370,000 processed events do not slow them. The feed check took 2.1 ms. |

## Items from earlier phases

| Item | Outcome |
|---|---|
| **Phase 2 D2-M4:** does a resync hold the interface lock? | **No.** pmsd takes the runtime row lock once per record, briefly. While a 500-record roster was staged, the sign-in's lock waited p50 1.7–3.0 ms, p95 5.4–7.2 ms, max 27 ms. During live admission the maximum was 66 ms. The resync's real effect on room sign-in is D6-H1's site-wide refusal. The serialisation of sign-ins against each other stays with Phase 2 and the Phase 11 storm. |
| **Phase 0 H5:** roster batching | D6-M2 for admission; D6-H1 step 3 for the apply. |
| **Phase 5:** checkout conversion | 35.7–38.0 ms per departure, conversion included, with no entitlements present. Departures that end entitlements also pay the session scan of Phase 5 D5-H1 (about 80 ms each without its index), so that fix comes first. |
| **Phase 5:** posting hand-off and throughput | See Checked: one at a time per interface by design, enough at the target load, and paused during resyncs, which D6-M2 shortens. |

## Handed on

| To | What |
|---|---|
| Phase 7 | edged's PMS pages: sync progress, case review and the event lists, which read `stay_events`. |
| Phase 10 | `stay_events` growth: one roster per reconnect plus live events. |
| Phase 11 | PRE-LIVE resync duration from pmsd's own progress log only (record counts and timestamps, no guest data), to tell Protel's pace from the appliance's. A room-sign-in storm during a resync, with D6-H1's closure measured from the guest side. |

## Batches

| Batch | Findings | Notes |
|---|---|---|
| **B1** | D6-M1 | Two conditions in two functions. **Do first.** CONTROLLED. |
| **B2** | D6-H1 steps 1–3 | Immediate wake, skip unchanged records, batched apply. CONTROLLED. Step 4 only if the Product Owner decides. |
| **B3** | D6-M2 | Batched staging, a separate socket reader, one transaction per live record. CONTROLLED. |
| **B4** | D6-L1 | After the Product Owner's decision. CONTROLLED. |

All four touch PMS processing, so each needs the Product Owner's authorisation (CLAUDE.md §0). Each is tested against generated rosters only: no PMS traffic.

## Compliance

| Finding | Rules | What the fix must respect |
|---|---|---|
| D6-H1 | GDPR Art. 32; the product's local-first and feed-trust contract (D39, D69) | The partial generation stays unclaimable. A waiting check-out still blocks a sign-in for that stay. Skipped records are recorded as such, so the inbox remains the full account. Scoping the rule (step 4) is a guest-authentication decision. |
| D6-M1 | — | The same rows are read, through an index, with the same results. |
| D6-M2 | GDPR Art. 32 | Every record is still admitted only under a proven, current ownership. Batching changes how many records share one proof, not whether they are proven. |
| D6-L1 | GDPR Art. 5(1)(c), (e), Art. 17; evidence retention | Fewer copies of guest names. The evidence hash and the rows that changed a stay are kept, and the guest export and erasure still find them. |
| All | GDPR Art. 25; README rule 7; CLAUDE.md (PMS traffic) | Generated rosters only. No PMS was contacted. PRE-LIVE facts are quoted from migration 0150's own record, not read from the appliance. |

## Tests run

| Test | Result |
|---|---|
| Full-schema database, 2 CPUs and 4 GB, reference settings; 370,000 processed events on one interface | Built |
| Temporary integration test in `internal/pmsd`, using pmsd's repository and `stayengine.NewProcessorWithCheckout`. Run twice, the second time with `track_functions`. | Measured costs table |
| Two 500-record resyncs: stage, publish, coverage, reconcile, apply; feed and lock probes | D6-H1, D6-M1, D6-M2 |
| 100 live departures: admission, continuity, apply with checkout conversion | D6-H1 |
| `EXPLAIN (ANALYZE, BUFFERS)` of the roster read, with and without `admission_kind = 'RESYNC'` | 336 ms and 2.1 ms |
| Code trace of the FIAS adapter, worker, applier, reconcile, feed check and posting relay | D6-L1 and the Checked list |

**Cleanup.**
- **Test file:** the temporary test (`zz_perfaudit_integration_test.go`) was deleted, and `git status` is clean.
- **Docker:** the database container was removed with its volume (`docker rm -f -v`). Docker is back to its state before the phase: no audit containers and 14 volumes.
- **Image:** the TimescaleDB image was already present and was kept.
