# Usage after the raw purge: migration 0180 (2026-10-29)

Follow-up to PR #272 (migration 0175, AD-48 (a), unchanged). CONTROLLED work: appliance migration on the entitlement and
accounting core. Not deployed anywhere, not pushed. The production baseline is not regenerated here.

## 1. The defect, confirmed

0175 keeps a raw sample in `iam_v2.accounting_records` only until it is rolled up and older than `raw_retention_days`
(default 2, 1-30). Three functions still derived an entitlement's usage by re-summing the raw samples:

| function | used by |
|---|---|
| `iam_v2.entitlement_usage_bytes(p_ent, p_at)` (0065) | `checkout.go` post-stay quota test (`validAtBoundary`) and the frozen `entitlement_boundary_watermarks` |
| `iam_v2.p3_entitlement_data_usage(p_ent)` (0062) | the derivation `consumed_data_bytes` must equal (0061) |
| `iam_v2.p6_data_crossing_full(p_ent)` (0122) | the exact recount inside `p6_advance_data_crossing` and `p6_data_crossing` |

After the purge each of them reads only the surviving raw samples. Demonstrated on the unmodified schema (tests in section 6):
a 6-day session with a rebind in the middle read `{up 0, down 0, records 0}` for the first entitlement after the purge
against `{564200, 2712000, 434}` before; a 60-day active entitlement went from 48,000 to 8,000 bytes.

### The p6 recount (what it decides, and the failure mode)

`p6_advance_data_crossing` keeps a checkpoint per binding (`data_crossing_progress.counted_bytes`, advanced by `sample_seq`
from the raw samples that arrived since). That incremental total is correct and does not read old raw. When the sum of an
entitlement's checkpoints reaches the quota it calls `p6_data_crossing_full`, a window sum over every attributed RAW sample in
`(sampled_at, id)` order that returns the first sample whose running sum reaches the quota. Its result is the only thing
that decides the crossing: `data_crossing_state.crossed_at` is what the expiry sweep terminates on.

With the early days purged, the running sum starts at the oldest surviving sample, so it either never reaches the quota
(NULL) or reaches it too late. NULL is read by the caller as "the checkpoint overstated": it then rewrites every binding's
`counted_bytes` from the surviving raw samples only (so the purged days leave the running total for good) and stores
`crossed_at = NULL`. From then on `p6_data_crossing`'s upper bound (`counted_bytes` + tail) is below the quota and no
crossing is ever established until the guest spends the missing days again. Effect: **a multi-day guest with a data quota
can pass it; the quota is in effect raised by whatever the purge removed.** Demonstrated on the unmodified schema by
`TestIntegration_UsageAfterRawPurge_AMultiDayGuestCannotPassTheirQuotaByWaiting` (500 MB quota, 500 MB used over five days,
`crossed_at` never set).

## 2. The design

`iam_v2.accounting_usage_entitlement`, one row per `(entitlement_id, hour bucket of sampled_at, arm)`: `bytes_up`,
`bytes_down`, `records`, `latest_sampled_at`, `exact`. **It holds exactly the samples the purge has deleted**; the raw table
holds the rest; the readers return the sum of the two.

**Deviation from the suggested design (roll-up fills it).** Attribution is by `session_entitlement_bindings`, and a binding
can be written retroactively: `rebind_session_entitlement(session, entitlement, p_at)` closes the open binding at `p_at` and
opens the next at `p_at`, and checkout passes the PMS boundary (in the past); ending a session closes the open binding at its
`ended` instant. A sample rolled up one minute after it was taken is attributed to the binding in force then; a rebind at an
earlier instant moves it to another entitlement, and once the sample is gone it can no longer be repaired in a roll-up-filled
accumulator. So the accumulator is filled by the purge instead, in tier 1, in the same statement as the delete:
`WITH doomed AS (...), del AS (DELETE ... RETURNING ...), att AS (attribution), fold AS (INSERT ... ON CONFLICT DO UPDATE)`.
A sample is therefore in exactly one place, however a run is interrupted or repeated (the whole purge is one transaction and
`DELETE ... RETURNING` returns only the rows this statement removed). The roll-up is not touched, so its throughput and its
atomicity are exactly 0175's. The cut-off is unchanged: `least(now() - raw_retention_days, watermark)`, so a sample the
roll-up has not read is never deleted (and so never folded).

Attribution arms (kept separate so each function stays exactly what it was):

* arm 0: `bound_from <= sampled_at < bound_until` (all three functions)
* arm 1: `sampled_at = bound_until` and no other binding covers it (0062, `p3_entitlement_data_usage` only)
* arm 2: a session with no binding row at all, to `sessions.entitlement_id` (0065, `entitlement_usage_bytes` only)

Readers (signatures, return columns, SECURITY DEFINER, `search_path`, owner and ACLs unchanged; single SQL statements, so
the folded and raw halves come from one snapshot):

* `entitlement_usage_bytes(p_ent, p_at)`: accumulator arms 0 and 2 for buckets whose every sample is at or before `p_at`
  (`bucket + 1 hour <= p_at` or `latest_sampled_at <= p_at`) + raw per binding. The raw part now drives from the entitlement's
  bindings (`seb_attribution`, then `accounting_records (session_id, ...)`) instead of scanning the table.
* `p3_entitlement_data_usage(p_ent)`: accumulator arms 0 and 1 + raw with the 0062 predicate.
* `p6_data_crossing_full(p_ent)`: the raw running sum starts from the folded arm-0 total; if the quota is reached inside the
  folded part, the answer is `latest_sampled_at` of the first bucket that reaches it.
* `p6_advance_data_crossing`: identical to 0122 except the "overstated checkpoint" recount, which now adds the folded arm-0
  total to the entitlement's first checkpoint row (the decision sums an entitlement's rows), so the purged bytes stay in the
  running total.
* `purge_accounting_detail`: 0175 plus the fold in tier 1 and, inside tier 3 under the same cut-off, the removal of
  accumulator rows of an entitlement that is TERMINATED and all of whose sessions (by binding or by current pointer) ended
  before the cut-off. An entitlement that can still be charged, quota-tested or checked out never loses its total. The
  rows share the hour tier's `accounting_purge_runs` record (the run count per tier is unchanged).

## 3. Exact and best-effort

Exact:

* every total (`p3_entitlement_data_usage`, `entitlement_usage_bytes(p_ent, now())`), `records` and `latest_sampled_at`, whatever
  has been purged, for samples purged by this version;
* a usage-at-instant read whenever every folded sample of the bucket holding `p_at` is at or before `p_at` (the usual checkout
  case: the pre-checkout entitlement's samples all precede the boundary) or `p_at` is not in a purged hour;
* with all raw present, every function returns what it returned before (property test, 8 random multi-session scenarios with
  rebinds, gaps, zero-length bindings, closing-instant samples, no-binding sessions, 3 entitlements, ~40 probe instants each);
* the crossing instant while the crossing sample is still raw.

Best effort, stated:

* a usage-at-instant read where `p_at` falls inside a purged hour that holds folded samples after `p_at`: the bucket cannot be
  split, so the result is a lower bound short by at most that one hour of the entitlement's usage (never an overcount; asserted);
* a crossing whose sample was already folded (acctd was down for longer than the raw retention): the latest sample time of the
  crossing hour, not the sample (ordering among samples within a few seconds of the purge cut-off is by ingestion, not sampled_at);
* the backfill below.

Retroactive bindings: a binding written for a time older than `raw_retention_days` re-attributes only the samples whose raw
still exists; samples already folded stay with the entitlement they were attributed to at fold time. (Rolling the accumulator at
roll-up time would be worse: it is wrong after any rebind to a past boundary, which checkout does routinely.)

## 4. Existing data at migration time

The migration backfills from the per-session hour tier: for each `(session, hour)` the hour tier's total minus the rolled-up
raw samples still present is what was purged; it is attributed to the first binding overlapping the hour (the one in force at its
start whenever one is) or, with no binding row, to the session's current entitlement (arm 2). `exact` is true only when the
attribution cannot depend on instants inside the hour (the binding covers the whole hour, or no binding row); otherwise
`exact = false`. `latest_sampled_at` of a backfilled row is the end of its hour. This affects only appliances that ran 0175 (the
test appliance). Samples purged by the old 12-month purge before 0175 were never in the hour tier and cannot be recovered
(`consumed_data_bytes` still holds those totals). The migration takes `SHARE ROW EXCLUSIVE` on `accounting_records` for the
duration of the backfill (acctd inserts wait; its 5 s lock timeout fails that tick and retries) so no purge that does not fold
can run between the backfill and the commit. Verified: on a 1,000,000-sample site rolled up and purged on 0175, the backfilled
accumulator + remaining raw equalled the hour tier exactly (3,001,000,000 bytes, 1,000,000 samples); 55 of 7,440 rows were inexact.

## 5. Performance (1 test container, 2 vCPU, 2 GB; 1,000,000 raw samples over 6 days, 2,000 sessions, 60 entitlements, two bindings each)

| | master (0175) | with 0180 |
|---|---|---|
| roll-up, 2-hour windows | 14,993 rows/s (66.7 s) | 15,116 rows/s (66.2 s): untouched |
| purge, 676,5xx raw samples, 100,000 batch | 8,687 rows/s (77.9 s) | 8,287 rows/s (81.6 s): 1.05x the time, the fold included |
| `entitlement_usage_bytes` at now() | 1,282 ms (scans the raw table) | 22 ms |
| `p3_entitlement_data_usage` | 22 ms | 12.6 ms |

The absolute rates are far below the 85,000 rows/s quoted for the appliance because this load is one row per
(session, minute) over a shared container; only the ratio is meaningful. The roll-up is unchanged by design, so the 1.5x
budget is not at risk there; the purge is the changed path and costs about 5 percent.

## 6. Tests

`data-plane/internal/enforce/usage_after_raw_purge_integration_test.go` (tag `integration`, `PHASE3_TEST_DSN`, run with `-p 1`;
keeps a copy of the 0065/0062/0122 definitions as `zz_old_*` for the comparison):

* `..._MultiDayRebindReadsTheSameAfterThePurge`: 6-day session, rebind mid-way, one sample on the boundary, raw purged at
  `raw_retention_days = 1`; all readers equal their pre-purge values (up, down, records, latest) EXACTLY. Fails on master.
* `..._EqualsTheRawFunctionsWhenRawIsComplete`: the property/equivalence test above.
* `..._PurgeChangesNoTotal`: random scenarios, purged with random batch sizes; totals, instants inside the retained raw, and
  the crossing (exact when raw, the hour's latest sample when folded) equal the pre-purge raw-based results. Fails on master.
* `..._AnInstantInsideAPurgedHourIsALowerBound`: never an overcount, short by at most the instant's hour.
* `..._InterruptedRollupAndPurgeLoseNothing`: statement-timeout cancels (16) and `pg_terminate_backend` of roll-up and purge
  windows, then a completed and a repeated run; totals equal the pre-purge raw sums at every step; folded + raw never exceeds
  the loaded samples.
* `..._AMultiDayGuestCannotPassTheirQuotaByWaiting`: the p6 bypass. Fails on master.
* `..._AccumulatorOutlivesAnActiveEntitlement`: an active entitlement keeps its total past the usage-detail retention; a
  TERMINATED one of ended sessions loses only rows past it. Fails on master.
* `..._MigrationBackfillsAndRollsBack`: as `iam_v2_owner`, in a rolled-back transaction: the down, a 0175-style purge, the up
  (backfill restores the exact sum), the down again.

## 7. Rolling back

`0180_...down.sql` restores the 0065/0062/0122/0175 definitions byte for byte and drops the accumulator. The cost: everything the
purge folded since 0180 is gone from the readers, which then read only the raw that remains (the defect again).
`consumed_data_bytes` and session totals are untouched and stay true. Grants are kept (`CREATE OR REPLACE` keeps the ACL).
Verified down then up on a live database holding folded rows.

## 8. Not done / for the lead

* The production baseline is not regenerated.
* `data-plane/internal/iamv2/testdata/iamv2_tenant_tables.txt` is already stale on master (it lacks the 0175 tables and the
  tables of later migrations, and `stay_event_payload_reductions` fails the catalog test for lack of `tenant_id`): the test
  `TestIAMv2CatalogEveryTableCarriesItsTenantAndSite` fails on master for reasons that predate this change. The new table has
  `tenant_id` and `site_id`, so it only needs the regenerated list line.
* Cross-tenant transition (`tenantOwnedTables`) and `sitemigrate` do not carry the roll-up tiers (0175) and do not carry this
  accumulator either; accounting for them is a separate decision.
