//go:build integration

package pmsd

// The claim's WHERE clause is the whole safety model (exactly-once, no stale execution, no parallel DR), which
// a fake repository can't prove, so it runs against real PostgreSQL. Exercises the row only; no PMS traffic.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// requestCommand writes the command as edged does, as SQL so the suite tests the row contract both processes share.
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

// A request is claimed exactly once (models racing workers and a double click).
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

// A command issued against a replaced worker is never executed.
func TestIntegration_ResyncCommand_StaleGenerationIsRefused(t *testing.T) {
	p, repo, ax, rev, sg := newResyncFixture(t)
	requestCommand(t, p, ax, "SUPPORT_REQUEST")

	// A new owner allocates a fresh generation. The transport is dropped first because pir_connected_pins
	// requires a CONNECTED runtime to carry its pins.
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

// A resync in progress blocks the claim; the command stays on the row, not lost, just not run yet.
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

// The stage vocabulary is closed by a database CHECK, since these words are rendered to hotel staff.
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

// A worker that lost the interface cannot keep writing progress.
func TestIntegration_ResyncCommand_StaleWorkerCannotWriteProgress(t *testing.T) {
	_, repo, ax, _, _ := newResyncFixture(t)
	stale := ax
	stale.ExpectedGeneration = ax.ExpectedGeneration + 99

	if err := repo.UpdateSyncStage(context.Background(), StageUpdate{axisBase: stale, Stage: StageReceiving}); err == nil {
		t.Fatal("a stale worker wrote sync progress. Progress is guarded by the same CAS as every other " +
			"write on this row, and must be for the same reason")
	}
}

// Counters are replaced, never incremented, so a retried write can't inflate them.
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

// A claim resets the counters so a new sync starts from zero.
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
