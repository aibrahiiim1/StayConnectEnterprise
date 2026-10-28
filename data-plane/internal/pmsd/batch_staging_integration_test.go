//go:build integration

package pmsd

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Performance audit D6-M2: a batch of RESYNC rows is written in one transaction under one ownership proof; a repeat
// inside the batch is inserted once and counted; a stale owner writes nothing; a failure leaves nothing behind.
func TestIntegration_StageResyncEventsBatch(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	ctx := context.Background()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	gen, err := repo.AllocateRuntimeGeneration(ctx, GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg})
	if err != nil {
		t.Fatal(err)
	}
	rg, err := repo.AllocateResyncGeneration(ctx, ResyncScope{axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, ExpectedGeneration: gen}})
	if err != nil {
		t.Fatal(err)
	}
	row := func(id string) InboxRow {
		r := inbox(s, gen, id, "GI")
		r.ResyncGeneration = rg
		return r
	}
	resyncRows := func() int {
		return scalarInt(t, pool, `SELECT count(*) FROM iam_v2.stay_events WHERE pms_interface_id=$1 AND admission_kind='RESYNC'`, s.iface)
	}
	var rows []InboxRow
	for i := 0; i < stageBatchSize; i++ {
		rows = append(rows, row(fmt.Sprintf("batch-%02d", i)))
	}
	rows = append(rows, row("batch-07")) // a repeat inside the batch
	staged, dups, err := repo.StageResyncEvents(ctx, rows)
	if err != nil || staged != stageBatchSize || dups != 1 {
		t.Fatalf("staged=%d dups=%d err=%v, want %d/1/nil", staged, dups, err, stageBatchSize)
	}
	if n := resyncRows(); n != stageBatchSize {
		t.Fatalf("resync rows = %d, want %d", n, stageBatchSize)
	}

	// A newer owner: nothing of the batch is written.
	if _, err := repo.AllocateRuntimeGeneration(ctx, GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.StageResyncEvents(ctx, []InboxRow{row("stale-1"), row("stale-2")}); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale owner batch = %v, want ErrStaleGeneration", err)
	}
	if n := resyncRows(); n != stageBatchSize {
		t.Fatalf("a stale batch wrote rows: %d", n)
	}
}

// Performance audit D6-M2 step 3: LIVE admission and continuity commit together; a repeat advances nothing.
func TestIntegration_AdmitLiveEventAndContinuity(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	ctx := context.Background()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	gen, err := repo.AllocateRuntimeGeneration(ctx, GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg})
	if err != nil {
		t.Fatal(err)
	}
	ax := axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, ExpectedGeneration: gen, At: time.Now()}
	at := time.Now().UTC().Truncate(time.Second)
	cont := ContinuityUpdate{axisBase: ax, Status: ContinuityContinuous, LastValidEventAt: &at, LastEventCursor: "R1:101"}
	if _, err := repo.AdmitLiveEventAndContinuity(ctx, inbox(s, gen, "live-1", "GI"), cont); err != nil {
		t.Fatal(err)
	}
	var status, cursor string
	if err := pool.QueryRow(ctx, `SELECT continuity_status, COALESCE(last_event_cursor,'') FROM iam_v2.pms_interface_runtime
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3`, s.tenant, s.site, s.iface).Scan(&status, &cursor); err != nil {
		t.Fatal(err)
	}
	if status != "CONTINUOUS" || cursor != "R1:101" {
		t.Fatalf("continuity=%q cursor=%q after admission", status, cursor)
	}
	cont2 := cont
	cont2.LastEventCursor = "R2:202"
	var dup *DuplicateLiveEventError
	if _, err := repo.AdmitLiveEventAndContinuity(ctx, inbox(s, gen, "live-1", "GI"), cont2); !errors.As(err, &dup) {
		t.Fatalf("repeat = %v, want DuplicateLiveEventError", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(last_event_cursor,'') FROM iam_v2.pms_interface_runtime
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3`, s.tenant, s.site, s.iface).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != "R1:101" {
		t.Fatalf("a repeat advanced continuity: cursor=%q", cursor)
	}
	// A stale owner admits nothing.
	if _, err := repo.AllocateRuntimeGeneration(ctx, GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AdmitLiveEventAndContinuity(ctx, inbox(s, gen, "live-2", "GI"), cont); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale owner = %v, want ErrStaleGeneration", err)
	}
	if n := liveRows(t, pool, s); n != 1 {
		t.Fatalf("live rows = %d, want 1", n)
	}
}
