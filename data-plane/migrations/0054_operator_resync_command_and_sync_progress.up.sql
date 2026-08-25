-- AN OPERATOR CAN ASK FOR A FRESH GUEST LIST, AND CAN WATCH IT ARRIVE.
--
-- Two gaps, and they are different in kind.
--
-- THE COMMAND. pmsd already knows how to run a complete resync: it allocates a generation, stages every
-- record under it, and publishes the whole thing atomically at DE. What it has never had is a way for anyone
-- OUTSIDE the process to ask for one. The worker decided on its own — at connect, on a continuity fault, on
-- queue overflow — and an operator watching a stale roster had no lever at all short of restarting a service.
--
-- The command cannot be a function call, because the thing that must execute it is the one goroutine that
-- owns the socket, and edged is a different process that must never write a frame. So it is a durable row,
-- and the worker claims it. That indirection is the point rather than a limitation: a row can be written by
-- an operator who then disconnects, claimed exactly once, and guarded by the same runtime_generation CAS that
-- already protects every other write on this table — so a command issued against a worker that has since
-- been replaced is refused by construction rather than by a check someone remembered to write.
--
-- THE PROGRESS. FIAS gives no total before DE. None. There is no field to read, no count to derive, and any
-- percentage a UI displays during a resync is invented. What IS deterministic is how many records we have
-- actually staged, which stage we are in, and when each stage began — so those are stored, and nothing else
-- is. An operator watching "1,847 records received" learns something true; an operator watching "63%" is
-- being told a number the PMS never sent.
--
-- WHY ON THE RUNTIME ROW rather than a new table. Every column here is single-valued per interface and is
-- read in the same breath as transport_status and sync_status — the health endpoint already selects this row,
-- and a join would buy nothing. More importantly the runtime row is where runtime_generation lives, and both
-- the command claim and every progress write need that CAS. Putting them anywhere else would mean either
-- duplicating the guard or writing across two rows in one logical operation.

BEGIN;

ALTER TABLE iam_v2.pms_interface_runtime
  -- THE COMMAND. All four are set together and cleared together: a request is a single fact.
  ADD COLUMN IF NOT EXISTS resync_command_id            uuid,
  ADD COLUMN IF NOT EXISTS resync_command_requested_at  timestamptz,
  ADD COLUMN IF NOT EXISTS resync_command_reason        text,
  -- The runtime_generation the command was issued against. A worker whose generation has moved on refuses it:
  -- the operator asked THAT worker, on THAT socket, and a replacement has a different roster to fetch.
  ADD COLUMN IF NOT EXISTS resync_command_generation    bigint,
  -- Set when the worker claims it, so a claimed-but-unfinished command is distinguishable from a fresh one.
  ADD COLUMN IF NOT EXISTS resync_command_claimed_at    timestamptz,

  -- THE PROGRESS.
  ADD COLUMN IF NOT EXISTS sync_stage                   text,
  ADD COLUMN IF NOT EXISTS sync_stage_at                timestamptz,
  -- Records actually STAGED under the open resync generation. Reset to 0 at DS, never estimated.
  ADD COLUMN IF NOT EXISTS sync_records_received        bigint NOT NULL DEFAULT 0,
  -- Well-formed records that describe no keyable Stay. Real durable evidence, not a guess: the adapter
  -- already counts these, it simply had nowhere to put the number.
  ADD COLUMN IF NOT EXISTS sync_records_skipped         bigint NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS sync_failure_code            text,
  -- The IN_HOUSE count the last COMPLETED sync produced. Stored at publish rather than counted on read, so
  -- the operator sees what that sync actually delivered instead of what the table happens to hold now.
  ADD COLUMN IF NOT EXISTS last_sync_in_house_count     bigint;

-- The stage vocabulary is CLOSED. A UI renders these as words to hotel staff, and an unbounded text column is
-- how an internal token ends up on a screen at a hotel. Anything not in this list is a bug, and the database
-- says so rather than storing it.
ALTER TABLE iam_v2.pms_interface_runtime
  DROP CONSTRAINT IF EXISTS pir_sync_stage_bounded;
ALTER TABLE iam_v2.pms_interface_runtime
  ADD CONSTRAINT pir_sync_stage_bounded CHECK (
    sync_stage IS NULL OR sync_stage IN (
      'REQUESTING_FULL_SYNC',  -- the command row is written; the worker has not claimed it yet
      'WAITING_FOR_PMS',       -- DR submitted through the serialized writer; awaiting DS
      'RECEIVING',             -- inside the DS→DE window, staging under the open generation
      'PUBLISHING',            -- DE seen; the atomic publish barrier is running
      'COMPLETE',              -- a generation was published; the mirror is fresh
      'FAILED',                -- the sync ended with a bounded cause and published nothing
      'INTERRUPTED'            -- ownership or transport was lost mid-sync; published nothing
    ));

-- A claimed command must have been requested first, and a claim cannot precede its request. The same shape as
-- pir_resync_coherent, for the same reason: a half-set command is a state nothing should be able to write.
ALTER TABLE iam_v2.pms_interface_runtime
  DROP CONSTRAINT IF EXISTS pir_resync_command_coherent;
ALTER TABLE iam_v2.pms_interface_runtime
  ADD CONSTRAINT pir_resync_command_coherent CHECK (
    (resync_command_id IS NULL) = (resync_command_requested_at IS NULL)
    AND (resync_command_id IS NOT NULL OR resync_command_claimed_at IS NULL)
    AND (resync_command_claimed_at IS NULL OR resync_command_requested_at IS NULL
         OR resync_command_claimed_at >= resync_command_requested_at));

COMMENT ON COLUMN iam_v2.pms_interface_runtime.resync_command_id IS
  'Operator-requested full resync awaiting execution by the pmsd worker that owns this socket. Written by '
  'edged, claimed exactly once by the worker under a runtime_generation CAS, cleared on claim. edged never '
  'writes a PMS frame; this row is the whole of the command channel.';
COMMENT ON COLUMN iam_v2.pms_interface_runtime.resync_command_generation IS
  'The runtime_generation the command was issued against. A worker whose generation has advanced refuses it: '
  'the operator asked a specific worker on a specific socket, and a replacement is not that worker.';
COMMENT ON COLUMN iam_v2.pms_interface_runtime.sync_records_received IS
  'Records actually staged under the open resync generation. FIAS provides no total before DE, so this is a '
  'real running count and there is deliberately no total, percentage or remaining-count column to pair it '
  'with — every such number would be invented.';
COMMENT ON COLUMN iam_v2.pms_interface_runtime.last_sync_in_house_count IS
  'IN_HOUSE Stays the last COMPLETED sync produced, stamped at publish rather than counted on read, so it '
  'reports what that sync delivered instead of what the table holds now.';

COMMIT;
