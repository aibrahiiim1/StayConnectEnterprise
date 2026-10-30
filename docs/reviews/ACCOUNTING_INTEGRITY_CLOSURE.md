# Accounting integrity closure: migration 0181 (2026-10-30)

Follow-up to migration 0175 (roll-up and raw purge) and 0180 (usage survives the raw purge). CONTROLLED work: an appliance
migration on the accounting and entitlement core. Not deployed, not pushed, the production baseline is not regenerated (0181 is applied
after it, like 0180). Rule applied throughout: missing history is never invented, and an uncertain figure is read in the direction that
cannot grant quota, with the uncertainty recorded where an operator can see it.

## 1. Findings per scenario

| Scenario | Finding | Result |
|---|---|---|
| Interrupted writes | The roll-up is one statement per window and the purge folds in the same statement it deletes (0180), so a sample is in exactly one place. Ingestion is one statement per sample under the checkpoint row lock. 0180's tests (statement-timeout cancels, `pg_terminate_backend`, repeated runs) still pass on the 0181 schema. | sound, no change |
| Replays / retries of one sample | Replay protection is the checkpoint's absolute counters, not the raw table, so purging a sample cannot let a re-sent reading count twice. Proven after the purge: exact replay answers `REPLAY:ACCEPTED`, an older reading and a counter regression are refused, the total does not move. | sound, test added |
| Reboots / epoch changes | A new epoch re-baselines (`RESET_BASELINED`); only bytes after it count; a reading from the old epoch is refused. Unaffected by the purge. Proven after the purge. | sound, test added |
| **Long sessions spanning purges** | **DEFECT (quota bypass).** `sample_seq` was `max(raw sample_seq) + 1`. When the purge removes all of a session's samples (idle for longer than `raw_retention_days`, default 2), the next sample restarts at 1, below the crossing checkpoint `data_crossing_progress.examined_seq`. `p6_advance_data_crossing` and `p6_data_crossing` read only `sample_seq > examined_seq`, so every new sample up to the old high-water mark was never counted toward the crossing. `consumed_data_bytes` stayed right, but the quota never terminated. Reproduced on the 0180 schema: the first sample after the idle gap got `sample_seq` 1 against a checkpoint of 2. | fixed (section 2.1) |
| Retroactive bindings, arm 0 / 1 / 2 | Arms are attributed at fold time, from the bindings then in force. A binding written later for an instant older than the raw retention (checkout passes the PMS boundary; `rebind_session_entitlement(session, ent, p_at)`) moves samples already folded into the previous entitlement; the fold cannot be redone. 0180 left this silent: the new entitlement was under-charged (extra quota). | bounded conservatively + audited (2.4) |
| Transfers | Samples before a rebind stay with the first entitlement, after it with the second, folded or not (property test from 0180 plus the new five-round test below). Closing-instant (arm 1) is the terminal sample of a terminated entitlement and is counted by `p3_entitlement_data_usage` but, by 0065's definition, not by `entitlement_usage_bytes`; unchanged and now documented in the test. | sound |
| Termination at the exact quota | With the crossing sample still raw the termination is dated by that exact sample, and one byte under the quota never terminates (tested). When the quota is reached inside the folded part the instant is the latest sample time of the crossing hour (the sample is gone), always in the right hour, never earlier than the crossing. | exact when raw; approximate when folded, now recorded (2.5) |
| `entitlement_usage_bytes(ent, p_at)` inside a folded hour | 0180 omitted a folded hour that held samples after `p_at`: a **lower bound**, so the checkout quota test (`validAtBoundary`) could read less than the guest used. | now an **upper bound** (2.2) |
| Checkpoint understated for any reason (defect above, a binding with no checkpoint row, retroactive charge) | The cheap trigger could be blind to folded usage. | lifted, never lowered (2.3) |
| 0180 backfill, inexact rows | Rows made from the per-session hour tier are attributed by hour (`exact = false`); invisible to operators. | recorded per entitlement + live count (2.6) |

## 2. What changed (migration 0181, `0181_accounting_integrity_closure.{up,down}.sql`)

1. **Sample sequence survives the purge.** `accounting_checkpoints.last_sample_seq` (new column, default 0), advanced by
   `ingest_absolute_counters`, which now allocates `greatest(max raw sample_seq, max last_sample_seq of the session) + 1`. The
   unique `(session_id, sample_seq)` index is unaffected. Existing rows are backfilled from `max(raw seq, examined_seq)` under
   `SHARE ROW EXCLUSIVE` on `accounting_checkpoints` (ingestion waits; its lock timeout retries the tick). Ingestion is otherwise
   byte-for-byte the 0061 definition.
2. **`entitlement_usage_bytes` is exact or an upper bound.** A folded hour that straddles `p_at` is counted whole; `latest_sampled_at`
   is clamped to `p_at`. `entitlement_usage_uncertainty(ent, p_at)` returns `straddling_bytes` (how far the figure may be over) and
   `inexact_bytes` (estimated rows); both zero means sample-exact. The excess is bounded by one hour of that entitlement's usage.
3. **`p6_reconcile_crossing_checkpoint(ent)`**: lifts the crossing checkpoint to folded arm-0 + the raw samples already examined when it
   is lower; never lowers; records `CHECKPOINT_BELOW_EXACT`. Run once in the migration for every live quota-bearing entitlement with
   folded or checkpoint rows (repairs sessions the defect had already hit) and by (4). An overcount is self-correcting: the exact
   derivation (`p6_data_crossing_full`) decides the crossing, and its existing "overstated" path rewrites the checkpoint.
4. **`rebind_session_entitlement`** (0010 body plus): when `p_at` is older than the site's `raw_retention_days`, an event
   `RETROACTIVE_BINDING_PAST_RAW_RETENTION` is always written (the purge may be running concurrently); if the session's hour tier
   shows folded bytes from the hour of `p_at` onwards, the new entitlement is charged exactly those bytes (accumulator row, arm 0,
   `exact = false`) and its checkpoint is reconciled. The previous entitlement keeps what it was charged, so the total over both is an
   overcount, never an undercount. With no folded history nothing is charged and the event says 0 bytes (tested: nothing fabricated).
   A rebind inside the retention changes nothing: the raw samples re-attribute exactly.
5. **`p6_advance_data_crossing`** (0180 body plus): `CROSSING_INSTANT_APPROXIMATE` is recorded when the folded total alone reaches the
   quota.
6. **Visibility.** `iam_v2.accounting_integrity_events` (kinds `CHECKPOINT_BELOW_EXACT`, `RETROACTIVE_BINDING_PAST_RAW_RETENTION`,
   `CROSSING_INSTANT_APPROXIMATE`, `BACKFILL_INEXACT_ATTRIBUTION`; ids, byte counts and instants only, no guest data) and
   `iam_v2.accounting_integrity_summary(tenant, site)` (per kind: events, entitlements, bytes, last time, plus the live count of
   inexact accumulator rows). An empty summary means every figure is sample-exact. No Go or UI change was made: surfacing it in the
   admin console is a UI task (the function is granted to `svc_edged`).

Grants: `svc_edged` EXECUTE on the summary, `svc_pmsd` EXECUTE on the uncertainty function, both also in
`deploy/gatep/svc-usage-rollup-grants.sql` (guarded on the migration) so a Gate-P reconcile reproduces them. The events table has no
runtime grantee. Owner `iam_v2_owner`. The reconcile function has no grantee. `entitlement_usage_bytes`, `ingest_absolute_counters`,
`rebind_session_entitlement`, `p6_advance_data_crossing` keep their signatures, owners and ACLs (`CREATE OR REPLACE`).

## 3. Tests (real PostgreSQL 16 / TimescaleDB 2.16.1, the full appliance schema built by `scripts/lib-fullschema-testdb.sh`)

New: `data-plane/internal/enforce/accounting_integrity_integration_test.go` (tag `integration`, `PHASE3_TEST_DSN`, `-p 1`):

* `AnIdleSessionPastTheRetentionStillCrossesItsQuota`: real ingestion, 6 MB, purge of the whole history, 3 MB, 3 MB; the quota (10 MB)
  terminates at the crossing sample; counter and derivation agree at 12,000,000. **Fails on the 0180 schema** (`sample_seq 1` against a
  checkpoint of 2); passes on 0181.
* `AnUnderstatedCheckpointIsLiftedNeverLowered`: the state the defect left is lifted to the exact figure once, idempotently, and an
  overstated checkpoint is never lowered.
* `ReplaysAndRebootsAfterThePurgeCountEachByteOnce`: replay, stale reading, regression, epoch change, old-epoch reading, repeated and
  1-row-batch purges; total stays exact; the sequence does not restart.
* `ALongTransferredSessionReadsTheSameAfterEveryPurgeRound`: five rounds of load + roll-up + purge over a transferred session, both
  entitlements and `p3_entitlement_data_usage` equal the loaded bytes after every round.
* `TerminationAtTheExactQuotaWhenTheCrossingSampleIsStillRaw`: dated by the exact sample; one byte under does not terminate.
* `TerminationWhenTheQuotaWasReachedInsideTheFoldedPartIsRecorded`: terminates in the right hour, the approximate instant is an event
  and shows in the summary.
* `ARetroactiveRebindIntoFoldedHistoryNeverGrantsExtraQuota`: neither entitlement reads below its true usage; e2 over by at most one hour;
  event, inexact bytes and checkpoint visible.
* `ARecentRebindChargesNothingAndMissingHistoryIsNeverInvented`.
* `AnInstantInsideAFoldedHourIsCountedWholeAndSaysSo`: upper bound, clamped latest, uncertainty function; exact read has zero uncertainty.

Changed: 0180's `..._AnInstantInsideAPurgedHourIsALowerBound` became `..._IsAnUpperBound` (the documented behaviour is deliberately
reversed; it asserts never-undercount and that the uncertainty function admits the excess).

Results are in section 5.

## 4. Compatibility, rollback, backup

* Additive: one table, one column, three new functions, four `CREATE OR REPLACE`. No data is rewritten except
  `accounting_checkpoints.last_sample_seq` (backfill) and, in `data_crossing_progress`, lifted `counted_bytes` for understated rows.
  Raw samples, the accumulator, `consumed_data_bytes` and session totals are untouched by the migration itself.
* Fresh installs: the migration applies after the baseline (which contains through 0180), like 0180.
* Back up before applying on an appliance with the usual database backup; nothing here is irreversible without it.
* `0181...down.sql` restores the 0061 / 0010 / 0180 definitions, takes the retroactive charges back out of the accumulator, drops the new
  functions, the events table and the column. Cost: the idle-session sequence hole returns; the events are lost (copy them first); checkpoint
  lifts are not reverted (they only raise a trigger estimate and are corrected by the exact derivation). Verified down then up twice on a
  database holding data.
* Behaviour change to know about: `entitlement_usage_bytes(ent, p_at)` can now be larger than before by at most one folded hour of that
  entitlement. This is deliberate and only occurs when `p_at` is inside an hour the purge has folded.

## 5. Not verified / limits

* The behaviour on the 1 vCPU test appliance and with production-size data (the 1,000,000-sample figures of the 0180 review): the 0181
  additions are not on the roll-up or purge paths; `ingest_absolute_counters` gains one indexed `max()` over the session's checkpoints. Not
  measured.
* `p6_data_crossing` (the read-only sweep candidate query) still uses `sample_seq > examined_seq`; it is correct because sequences are
  now monotonic. A *future* writer that bypasses `ingest_absolute_counters` would reintroduce the problem; none exists.
* A retroactive binding is charged an upper bound; the previous entitlement is not refunded. Refunding needs per-sample history that
  does not exist; the safe direction is kept by design.
* A purge that races a retroactive rebind of the same session is covered only by the event (always written when `p_at` is older than the
  retention), not by a charge.
* The crossing instant for a quota reached inside the folded part is only known to the hour (event recorded).
* ~~The 0180 note stands: `testdata/iamv2_tenant_tables.txt` is stale on master; cross-tenant transition and `sitemigrate` do not carry the
  roll-up tiers or the accumulator.~~ **Reviewed in the final engineering round (see section 9 below):** the catalog list was regenerated
  and the missing exemption added; `sitemigrate` and the cross-tenant transition carrying nothing of the usage tiers is correct by
  construction, not a gap.
* Nothing was run against a real appliance, Central, or the network.

## 9. Final engineering round (2026-10-30): what was reviewed, fixed and left

| Limit named above | Outcome |
|---|---|
| "Old entitlement not refunded after a retroactive rebind" | **Kept, by design, and now stated as such.** The previous entitlement keeps what it was charged and the new one is charged an upper bound. A refund would have to subtract bytes whose per-sample history no longer exists (they are folded into hour buckets), so it could only be a guess, and a guess in the guest's favour is extra quota. The direction can only be conservative; every case is recorded in `accounting_integrity_events`. |
| "The crossing instant is only known to the hour inside folded data" | **Kept.** The folded hour tier has no finer time. The case is recorded as an event and the entitlement is treated as already over its limit from the hour, never later. |
| "`sitemigrate` / cross-tenant transition do not carry the roll-up tiers or the accumulator" | **Not a defect.** `sitemigrate` is the delivered one-time copy of the legacy `public.*` guest tables from the former cloud database; the usage tiers (`iam_v2.accounting_usage_*`, `accounting_usage_entitlement`) and the entitlements and sessions they describe never existed there, and a tier without its entitlements would be unattributable. The cross-tenant transition purges the previous customer's tenant-owned `public` tables and holds the appliance fail-closed while another customer's `iam_v2` guest records exist (`cmd/scd/tenant_transition.go`); usage rows are keyed by tenant and site and are never read under another tenant, so nothing is carried and nothing can be fabricated or reset into extra allowance. Neither path creates an entitlement. |
| "No admin visibility of `accounting_integrity_summary`" | **Added**, read-only: `GET /edge/v1/accounting-integrity` in edged (permission `reports`, counts and bytes per kind, `exact: true` when nothing is recorded; no guest, device, session or entitlement identifier). Real-PostgreSQL test `TestIntegration_AccountingIntegrity_TheOperatorViewIsReadOnlyAndExact`. The Hotel Admin screen is not built. |
| `testdata/iamv2_tenant_tables.txt` stale; `stay_event_payload_reductions` failed the catalog test | **Fixed**: the table is named in `unscopedTables` (an ids-only undo record of 0171, child of `stay_events`) and the list regenerated. |

**A defect found while reviewing usage continuity of the Cross-PMS Transfer (`internal/transfer`), fixed:** the transfer re-pointed
live sessions with `UPDATE sessions SET entitlement_id`, which leaves the session's OPEN binding on the terminated source entitlement.
Accounting charges a sample to the entitlement the session was *bound* to at the sample's instant, so after a transfer the destination's
own data limit was never charged for what the guest then used (and the terminated source kept accruing). The transfer now moves each
live session (active and pending enforcement) through `rebind_session_entitlement`, which closes the source interval and opens the
destination's at the instant the devices move. Tests: `TestIntegration_F9_TransferMovesTheSessionBindingSoUsageIsChargedToTheDestination`
and `..._TransferAlsoMovesAPendingSession`. **Not fixed, and reported:** with the production grants (Gate-P) the role edged connects as
(`svc_edged`) holds neither `EXECUTE` on `apply_entitlement_transition` nor `INSERT` on `purchases`, `entitlements`
or `entitlement_transfers`, so the transfer cannot complete on a real appliance (it fails closed: one transaction, nothing is
applied). The integration tests run as the database owner and cannot see it. Running it needs a decision on *which service performs the
transfer* (the entitlement state writers belong to scd and pmsd), which is an authority-boundary change and not made here.
