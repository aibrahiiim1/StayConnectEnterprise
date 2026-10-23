package pmsd

// The operator command channel: edged writes a durable row and only the socket-owning worker claims it, so
// edged never emits a frame.
//
// Claiming is one UPDATE guarded by the exact runtime_generation that clears the command as it reads it:
//   - exactly once: racing workers produce one winner and one zero-row update (no duplicate DR);
//   - no stale execution: a replaced worker's generation matches nothing;
//   - no parallel DR: the claim refuses while sync_status is RESYNC_IN_PROGRESS.
//
// There is deliberately no retry or persistence across ownership changes: an unclaimed command whose
// generation moved on is dead, since re-requesting is one click and running a stale instruction is worse.

import (
	"time"
)

// Stage is a closed vocabulary shown to hotel staff; the database enforces the same list (pir_sync_stage_bounded).
type SyncStage string

const (
	StageRequesting  = SyncStage("REQUESTING_FULL_SYNC")
	StageWaiting     = SyncStage("WAITING_FOR_PMS")
	StageReceiving   = SyncStage("RECEIVING")
	StagePublishing  = SyncStage("PUBLISHING")
	StageComplete    = SyncStage("COMPLETE")
	StageFailed      = SyncStage("FAILED")
	StageInterrupted = SyncStage("INTERRUPTED")
)

type ResyncCommand struct {
	ID          string
	RequestedAt time.Time
	Reason      string
}

// StageUpdate fields other than the stage are optional so a caller needn't restate (and accidentally reset) a counter.
type StageUpdate struct {
	axisBase
	Stage SyncStage
	// RecordsReceived, when non-nil, replaces the counter (never incremented here), so a retry can't double-count.
	RecordsReceived *int64
	RecordsSkipped  *int64
	FailureCode     string
	// InHouseCount is unused: it was stamped before the applier wrote the new roster, so it reported the old
	// one. Kept only so a rollback to the prior binary finds the column; UpdateSyncStage COALESCEs it away.
	InHouseCount *int64
}
