//go:build integration

package pmsd

// THE OPERATOR COMMAND CHANNEL, AGAINST A REAL POSTGRESQL.
//
// The claim is one UPDATE whose WHERE clause carries the entire safety model — exactly-once, no stale
// execution, no parallel DR. A fake repository can be made to agree with any of those by construction, so the
// claim is tested here against the real statement and the real CHECK constraints instead.
//
// No PMS traffic is involved and none is possible: this exercises the row, not the socket.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// requestCommand writes an operator command the way edged does, including binding it to the runtime's own
// generation. Kept as SQL rather than calling edged so this suite tests the row contract both processes share.
func requestCommand(t *testing.T, pool *pgxpool.Pool, ax axisBase, reason string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		UPDATE iam_v2.pms_interface_runtime rt
		   SET resync_command_id=gen_random_uuid(), resync_command_requested_at=now(),
		       resync_command_reason=$4, resync_command_generation=rt.runtime_generation,
		       resync_command_claimed_at=NULL, sync_stage='REQUESTING_FULL_SYNC', sync_stage_at=now(),
		       updated_at=now()
		 WHERE rt.tenant_id=$1 AND rt.site_id=$2 AND rt.pms_interface_id=$3
		   AND rt.resync_command_id IS NULL
		RETURNING rt.resync_command_id::text`,
		ax.TenantID, ax.SiteID, ax.PMSInterfaceID, reason).Scan(&id)
	if err != nil {
		t.Fatalf("write operator command: %v", err)
	}
	return id
}

// A REQUEST IS CLAIMED EXACTLY ONCE, and the second claim finds nothing.
//
// This is the duplicate-click case. Two claims in a row model two workers racing, and one operator clicking
// twice reaches the same place: the command row can only be consumed once, so only one DR can result.
func TestIntegration_ResyncCommand_ClaimedExactlyOnce(t *testing.T) {
	p, repo, ax, _, _ := newResyncFixture(t)

	id := requestCommand(t, p, ax, "OPERATOR_VERIFICATION")

	first, err := repo.ClaimResyncCommand(context.Background(), ResyncScope{ax})
	if err != nil || first == nil {
		t.Fatalf("the first claim did not take the command: %v %+v", err, first)
	}
	if first.ID != id || first.Reason != "OPERATOR_VERIFICATION" {
		t.Fatalf("the claim returned %+v, want the requested command %s. Returning the post-update row here "+
			"would hand the worker an empty id and a DR attributable to nothing", first, id)
	}

	second, err := repo.ClaimResyncCommand(context.Background(), ResyncScope{ax})
	if err != nil {
		t.Fatalf("second claim errored: %v", err)
	}
	if second != nil {
		t.Fatalf("the command was claimed twice (%+v). Two claims means two DRs on one socket", second)
	}
}

// A COMMAND ISSUED AGAINST A REPLACED WORKER IS NEVER EXECUTED.
//
// The operator asked a specific worker on a specific socket. By the time a replacement exists, that
// instruction is about a connection the operator never saw, and the replacement has its own initial sync to
// perform anyway.
func TestIntegration_ResyncCommand_StaleGenerationIsRefused(t *testing.T) {
	p, repo, ax, rev, sg := newResyncFixture(t)
	requestCommand(t, p, ax, "SUPPORT_REQUEST")

	// A new owner allocates a fresh runtime generation — exactly what worker replacement does. The transport
	// is dropped first because pir_connected_pins requires a CONNECTED runtime to carry its pins, and taking
	// over an interface necessarily means the previous owner's socket is gone.
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET transport_status='DISCONNECTED', disconnected_since=now(), updated_at=now()
		WHERE pms_interface_id=$1`, ax.PMSInterfaceID); err != nil {
		t.Fatalf("drop the previous owner's transport: %v", err)
	}
	newGen, err := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{TenantID: ax.TenantID,
		SiteID: ax.SiteID, PMSInterfaceID: ax.PMSInterfaceID, PinnedRevisionID: rev, PinnedSecretGenerationID: sg})
	if err != nil {
		t.Fatalf("allocate a replacement generation: %v", err)
	}
	replaced := ax
	replaced.ExpectedGeneration = newGen

	got, err := repo.ClaimResyncCommand(context.Background(), ResyncScope{replaced})
	if err != nil {
		t.Fatalf("claim errored: %v", err)
	}
	if got != nil {
		t.Fatalf("a replacement worker executed a command issued to the worker it replaced: %+v", got)
	}
}

// A RESYNC ALREADY IN PROGRESS BLOCKS THE CLAIM, so a command cannot open a second staging window over an
// open one. The command is left on the row rather than discarded — the operator's request is not lost, it is
// simply not run yet.
func TestIntegration_ResyncCommand_RefusedWhileResyncing(t *testing.T) {
	p, repo, ax, _, _ := newResyncFixture(t)
	ctx := context.Background()

	if _, err := p.Exec(ctx, `UPDATE iam_v2.pms_interface_runtime
		SET sync_status='RESYNC_IN_PROGRESS', resync_requested_at=now()-interval '1 minute',
		    resync_started_at=now(), updated_at=now() WHERE pms_interface_id=$1`,
		ax.PMSInterfaceID); err != nil {
		t.Fatalf("open a resync window: %v", err)
	}
	requestCommand(t, p, ax, "AFTER_PMS_MAINTENANCE")

	got, err := repo.ClaimResyncCommand(ctx, ResyncScope{ax})
	if err != nil {
		t.Fatalf("claim errored: %v", err)
	}
	if got != nil {
		t.Fatalf("a command was claimed while a resync was already running: %+v", got)
	}
	var stillPending bool
	if err := p.QueryRow(ctx,
		`SELECT resync_command_id IS NOT NULL FROM iam_v2.pms_interface_runtime WHERE pms_interface_id=$1`,
		ax.PMSInterfaceID).Scan(&stillPending); err != nil {
		t.Fatal(err)
	}
	if !stillPending {
		t.Fatal("the refused command was discarded. The operator's request should wait, not vanish")
	}
}

// THE STAGE VOCABULARY IS CLOSED, enforced by the database rather than by convention.
//
// These words are rendered to hotel staff. An unbounded column is how an internal token reaches a screen, so
// the CHECK is the guard and this proves it is really there.
func TestIntegration_ResyncCommand_StageVocabularyIsEnforced(t *testing.T) {
	p, repo, ax, _, _ := newResyncFixture(t)
	ctx := context.Background()

	for _, st := range []SyncStage{StageRequesting, StageWaiting, StageReceiving, StagePublishing,
		StageComplete, StageFailed, StageInterrupted} {
		if err := repo.UpdateSyncStage(ctx, StageUpdate{axisBase: ax, Stage: st}); err != nil {
			t.Fatalf("stage %s was rejected: %v", st, err)
		}
	}
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.pms_interface_runtime SET sync_stage='SEVENTY_THREE_PERCENT' WHERE pms_interface_id=$1`,
		ax.PMSInterfaceID); err == nil {
		t.Fatal("the database accepted an invented stage. A percentage is exactly the kind of number FIAS " +
			"never provides, and the CHECK exists to keep it off the screen")
	}
}

// PROGRESS WRITES ARE GUARDED BY OWNERSHIP. A worker that has lost the interface cannot keep writing progress
// for a socket someone else now owns.
func TestIntegration_ResyncCommand_StaleWorkerCannotWriteProgress(t *testing.T) {
	_, repo, ax, _, _ := newResyncFixture(t)
	stale := ax
	stale.ExpectedGeneration = ax.ExpectedGeneration + 99

	if err := repo.UpdateSyncStage(context.Background(), StageUpdate{axisBase: stale, Stage: StageReceiving}); err == nil {
		t.Fatal("a stale worker wrote sync progress. Progress is guarded by the same CAS as every other " +
			"write on this row, and must be for the same reason")
	}
}

// COUNTERS ARE REPLACED, NEVER INCREMENTED, so a retried write cannot inflate the number an operator watches.
func TestIntegration_ResyncCommand_CountersAreIdempotent(t *testing.T) {
	p, repo, ax, _, _ := newResyncFixture(t)
	ctx := context.Background()
	n := int64(1847)

	for i := 0; i < 3; i++ { // the same write three times, as a retry would
		if err := repo.UpdateSyncStage(ctx, StageUpdate{axisBase: ax, Stage: StageReceiving, RecordsReceived: &n}); err != nil {
			t.Fatalf("progress write: %v", err)
		}
	}
	var got int64
	if err := p.QueryRow(ctx,
		`SELECT sync_records_received FROM iam_v2.pms_interface_runtime WHERE pms_interface_id=$1`,
		ax.PMSInterfaceID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != n {
		t.Fatalf("received count is %d after three identical writes, want %d. An incrementing counter makes "+
			"every retry visible to the operator as records that never arrived", got, n)
	}
}

// A CLAIM RESETS THE COUNTERS, so a new sync starts from zero rather than continuing the previous roster's
// total. Without this the second sync of the day would appear to begin at whatever the first one ended on.
func TestIntegration_ResyncCommand_ClaimResetsCounters(t *testing.T) {
	p, repo, ax, _, _ := newResyncFixture(t)
	ctx := context.Background()
	prev := int64(500)
	if err := repo.UpdateSyncStage(ctx, StageUpdate{axisBase: ax, Stage: StageComplete, RecordsReceived: &prev}); err != nil {
		t.Fatal(err)
	}
	requestCommand(t, p, ax, "SUSPECTED_STALE_GUEST_LIST")
	if _, err := repo.ClaimResyncCommand(ctx, ResyncScope{ax}); err != nil {
		t.Fatal(err)
	}

	var got int64
	var stage string
	if err := p.QueryRow(ctx,
		`SELECT sync_records_received, sync_stage FROM iam_v2.pms_interface_runtime WHERE pms_interface_id=$1`,
		ax.PMSInterfaceID).Scan(&got, &stage); err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("the previous sync's count (%d) survived the claim", got)
	}
	if stage != string(StageWaiting) {
		t.Fatalf("stage after claim is %q, want WAITING_FOR_PMS — the DR has been submitted and the PMS has "+
			"not started sending yet", stage)
	}
}

// newResyncFixture builds one ACTIVE, CONNECTED interface with a runtime row, and returns the axis a worker
// would hold while serving it.
func newResyncFixture(t *testing.T) (*pgxpool.Pool, Repo, axisBase, string, string) {
	t.Helper()
	p := integPool(t)
	t.Cleanup(p.Close)
	ctx := context.Background()
	s := seedScope(t, p)
	repo := NewPgRepoFromPool(p)
	gen, err := repo.AllocateRuntimeGeneration(ctx, GenerationRequest{TenantID: s.tenant, SiteID: s.site,
		PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.pms_interface_runtime
		SET transport_status='CONNECTED', sync_status='IN_SYNC', continuity_status='CONTINUOUS',
		    last_connected_at=now(), last_heartbeat_at=now(), updated_at=now()
		WHERE pms_interface_id=$1`, s.iface); err != nil {
		t.Fatalf("mark the interface connected: %v", err)
	}
	return p, repo, axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface,
		ExpectedGeneration: gen, At: time.Now().UTC()}, s.rev, s.sg
}
