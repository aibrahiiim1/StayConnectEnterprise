//go:build integration

package pmsd

// A resync interrupted between DS and DE must not wedge the interface. pir_resync_coherent requires
// started_at >= requested_at and RequireInitialResync writes a fresh requested_at on every connect, so a stale
// started_at would make the CHECK reject every reconnect permanently.

import (
	"context"
	"testing"
	"time"
)

// An abandoned resync window, then a reconnect, must not violate the CHECK.
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

	// A resync began and never finished: the link dropped before DE.
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

// The reset is keyed to the status and must not fire on other transitions: clearing started_at mid-resync loses the window's start.
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
	// Truncated to microseconds: timestamptz stores no finer.
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
