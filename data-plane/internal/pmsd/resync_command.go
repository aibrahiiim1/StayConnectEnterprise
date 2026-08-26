package pmsd

// THE OPERATOR COMMAND CHANNEL, AND THE ONLY WAY INTO IT.
//
// edged cannot ask pmsd for a resync by calling it: pmsd is a different process, and the thing that has to
// perform the request is the single goroutine that owns the socket. Everything else in this file follows from
// that. The command is a durable row; the worker claims it; edged never emits a frame.
//
// CLAIMING IS THE WHOLE SAFETY ARGUMENT. It is one UPDATE guarded by the exact runtime_generation, and it
// clears the command in the same statement that reads it. Three properties fall out of that single write,
// none of which needs a lock, a queue or a second round trip:
//
//   * EXACTLY ONCE. Two workers racing to claim the same command produce one winner and one zero-row update.
//     The loser sees no command rather than a duplicate DR.
//   * NO STALE EXECUTION. A command carries the runtime_generation it was issued against. A worker that has
//     been replaced has a different generation, so its claim matches nothing — the operator asked THAT
//     worker on THAT socket, and a replacement has a different roster to fetch anyway.
//   * NO PARALLEL DR. The claim refuses while sync_status is RESYNC_IN_PROGRESS, so a command written during
//     a running sync waits rather than opening a second staging window over the first.
//
// What this file deliberately does NOT contain is any notion of retrying, queueing or persisting a command
// across ownership changes. An unclaimed command whose generation has moved on is dead, and that is correct:
// re-requesting is one click, and silently executing a minutes-old instruction against a socket the operator
// never saw is not something a hotel should have to reason about.

import (
	"time"
)

// Stage is the closed operator-facing vocabulary for where a sync currently is. The database enforces the
// same list (pir_sync_stage_bounded) because these words reach a screen in a hotel.
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

// ResyncCommand is one operator request, as claimed.
type ResyncCommand struct {
	ID          string
	RequestedAt time.Time
	Reason      string
}

// StageUpdate carries one progress write. Every field is optional except the stage itself: a sync moving from
// RECEIVING to PUBLISHING has nothing new to say about the received count, and forcing a caller to restate it
// is how a counter gets accidentally reset.
type StageUpdate struct {
	axisBase
	Stage SyncStage
	// RecordsReceived, when non-nil, REPLACES the counter. The adapter sets it to 0 at DS and then reports the
	// running total; it is never incremented by this layer, so a retried write cannot double-count.
	RecordsReceived *int64
	RecordsSkipped  *int64
	FailureCode     string
	// InHouseCount is NO LONGER SET BY ANYTHING. It was stamped at the publish barrier, which is before the
	// applier writes the new roster, so it reported the previous one. The field and its column survive only
	// so a rollback to the prior binary finds the shape it expects; the write path leaves it nil and
	// UpdateSyncStage COALESCEs it away.
	InHouseCount *int64
}
