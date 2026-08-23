//go:build integration

package pmsd

// A RESYNC INTERRUPTED BETWEEN DS AND DE MUST NOT WEDGE THE INTERFACE FOREVER.
//
// pir_resync_coherent requires resync_started_at >= resync_requested_at. RequireInitialResync writes a fresh
// requested_at on every connect, so if an abandoned DS…DE window left an older started_at on the row, the
// CHECK rejects the write, Serve returns, and the transport closes — about four milliseconds after
// connecting. The interface then reconnects and fails again, permanently, with no operator-visible cause
// beyond UNCLASSIFIED.
//
// That is not hypothetical: it is what took a PRE-LIVE Protel interface offline for a day and a half. The link
// was healthy the whole time. LIVE admission stays barred until a complete resync generation publishes, so the
// event backlog grew, no Stay could gain occupancy evidence, and no guest could sign in.

import (
	"context"
	"testing"
	"time"
)

// The exact wedge: an abandoned resync window, then a reconnect. Before the fix this returned a CHECK
// violation; the interface could never connect again without manual database surgery.
func TestIntegration_ReconnectAfterAbandonedResyncIsNotWedged(t *testing.T) {
	p := integPool(t)
	defer p.Close()
	ctx := context.Background()
	s := seedScope(t, p)
	repo := NewPgRepoFromPool(p)
	gen, err := repo.AllocateRuntimeGeneration(ctx, GenerationRequest{TenantID: s.tenant, SiteID: s.site,
		PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	base := axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, ExpectedGeneration: gen}

	// An operator-visible resync began and never finished — the DE never arrived because the link dropped.
	requested := time.Now().UTC().Add(-26 * time.Hour)
	started := requested.Add(200 * time.Millisecond)
	if _, err := p.Exec(ctx, `UPDATE iam_v2.pms_interface_runtime
		SET sync_status='RESYNC_IN_PROGRESS', resync_requested_at=$2, resync_started_at=$3
		WHERE pms_interface_id=$1`, s.iface, requested, started); err != nil {
		t.Fatalf("stage abandoned resync: %v", err)
	}

	// Reconnect. This is precisely what workerSink.RequireInitialResync does.
	now := time.Now().UTC()
	base.At = now
	if err := repo.UpdateSync(ctx, SyncUpdate{
		axisBase:          base,
		Status:            SyncResyncRequired,
		ResyncRequestedAt: &now,
	}); err != nil {
		t.Fatalf("a reconnect after an abandoned resync was rejected — the interface is wedged and cannot "+
			"recover without manual intervention: %v", err)
	}

	var startedAt *time.Time
	var status string
	if err := p.QueryRow(ctx, `SELECT sync_status, resync_started_at FROM iam_v2.pms_interface_runtime
		WHERE pms_interface_id=$1`, s.iface).Scan(&status, &startedAt); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "RESYNC_REQUIRED" {
		t.Fatalf("sync_status = %q, want RESYNC_REQUIRED", status)
	}
	if startedAt != nil {
		t.Fatal("declaring RESYNC_REQUIRED must abandon the previous window by clearing resync_started_at; " +
			"leaving it set is what violates pir_resync_coherent on the next connect")
	}
}

// The reset is keyed to the STATUS, so it must not fire on the other sync transitions — clearing started_at
// while a resync is genuinely in progress would lose the window's own start time.
func TestIntegration_InProgressResyncKeepsItsStartedAt(t *testing.T) {
	p := integPool(t)
	defer p.Close()
	ctx := context.Background()
	s := seedScope(t, p)
	repo := NewPgRepoFromPool(p)
	gen, err := repo.AllocateRuntimeGeneration(ctx, GenerationRequest{TenantID: s.tenant, SiteID: s.site,
		PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	base := axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, ExpectedGeneration: gen}

	now := time.Now().UTC()
	base.At = now
	if err := repo.UpdateSync(ctx, SyncUpdate{
		axisBase:          base,
		Status:            SyncResyncRequired,
		ResyncRequestedAt: &now,
	}); err != nil {
		t.Fatalf("require resync: %v", err)
	}
	// Truncated to microseconds because timestamptz stores no finer, so an untruncated Go time would differ
	// from what comes back purely in digits PostgreSQL never kept.
	started := now.Add(time.Second).Truncate(time.Microsecond)
	base.At = started
	if err := repo.UpdateSync(ctx, SyncUpdate{
		axisBase:        base,
		Status:          SyncResyncInProgress,
		ResyncStartedAt: &started,
	}); err != nil {
		t.Fatalf("start resync: %v", err)
	}

	var got *time.Time
	if err := p.QueryRow(ctx, `SELECT resync_started_at FROM iam_v2.pms_interface_runtime
		WHERE pms_interface_id=$1`, s.iface).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got == nil || !got.UTC().Equal(started) {
		t.Fatalf("an in-progress resync must keep its own start time, got %v want %s", got, started)
	}
}
