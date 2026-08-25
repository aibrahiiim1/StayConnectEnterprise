-- Remove the operator command channel and the durable sync progress.
--
-- Rolling this back returns pmsd to deciding on its own when a resync happens — at connect, on a continuity
-- fault, on queue overflow — with no way for an operator to ask for a fresh guest list and no visibility into
-- a sync while it runs. The automatic initial sync is unaffected either way; it never used any of this.
--
-- Any command in flight is discarded rather than executed. That is the safe direction: a command row with no
-- worker left to claim it is inert, and dropping the column cannot interrupt a resync already in progress
-- because the resync itself lives in the generation columns, which this migration never touched.

BEGIN;

ALTER TABLE iam_v2.pms_interface_runtime
  DROP CONSTRAINT IF EXISTS pir_resync_command_coherent;
ALTER TABLE iam_v2.pms_interface_runtime
  DROP CONSTRAINT IF EXISTS pir_sync_stage_bounded;

ALTER TABLE iam_v2.pms_interface_runtime
  DROP COLUMN IF EXISTS resync_command_id,
  DROP COLUMN IF EXISTS resync_command_requested_at,
  DROP COLUMN IF EXISTS resync_command_reason,
  DROP COLUMN IF EXISTS resync_command_generation,
  DROP COLUMN IF EXISTS resync_command_claimed_at,
  DROP COLUMN IF EXISTS sync_stage,
  DROP COLUMN IF EXISTS sync_stage_at,
  DROP COLUMN IF EXISTS sync_records_received,
  DROP COLUMN IF EXISTS sync_records_skipped,
  DROP COLUMN IF EXISTS sync_failure_code,
  DROP COLUMN IF EXISTS last_sync_in_house_count;

COMMIT;
