//go:build integration

package main

// ROOM SIGN-IN WHILE A REFRESHED ROSTER IS WRITTEN IN (0205). Every complete PMS sync is published and then drained into
// the stays one event at a time (~3.5 s for ~420 stays on the test appliance). Before 0205 every guest was refused for the
// whole drain. Now a guest who is found signs in at once, and a guest who is not found is held back until the drain ends:
// never "your details are wrong" while their row may still be on its way.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// startDrain publishes a refresh and leaves one of its rows PENDING, exactly as pmsd leaves the roster between
// WORKER_RESYNC_PUBLISHED and the applier's last claim. It returns a function that ends the drain.
func (f *authFixture) startDrain(t *testing.T) (end func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatalf("open controlled operation: %v", err)
	}
	var gen int64
	if err := tx.QueryRow(ctx, `UPDATE iam_v2.pms_interface_runtime
		   SET resync_generation_seq = GREATEST(resync_generation_seq, published_resync_generation) + 1,
		       published_resync_generation = GREATEST(resync_generation_seq, published_resync_generation) + 1
		 WHERE pms_interface_id=$1 RETURNING published_resync_generation`, f.iface).Scan(&gen); err != nil {
		t.Fatalf("publish a generation: %v", err)
	}
	var event string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events
		(id, tenant_id, site_id, pms_interface_id, external_event_identity, event_type, received_at,
		 sequence_version, normalization_version, clock_suspect, payload, processing_status,
		 admission_kind, admission_runtime_generation, resync_generation, fingerprint_key_version)
		VALUES (gen_random_uuid(), $1,$2,$3, 'drain-' || gen_random_uuid()::text, 'GI', now(),
		        0, 1, false, '{}'::jsonb, 'PENDING', 'RESYNC', 1, $4, 1)
		RETURNING id::text`, f.tenant, f.site, f.iface, gen).Scan(&event); err != nil {
		t.Fatalf("leave a published row pending: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return func() {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
			t.Error(err)
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events SET processing_status='MANUAL_REVIEW', processed_at=now(),
			review_code='GO_UNKNOWN_STAY' WHERE id=$1`, event); err != nil {
			t.Error(err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			t.Error(err)
		}
	}
}

func (f *authFixture) lastAttemptResult(t *testing.T) string {
	t.Helper()
	var r string
	if err := f.pool.QueryRow(context.Background(), `SELECT result FROM iam_v2.sign_in_attempts
		 WHERE tenant_id=$1 AND site_id=$2 ORDER BY occurred_at DESC LIMIT 1`, f.tenant, f.site).Scan(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

// The defect measured on 2026-11-02: a guest with correct details, refused one second after a refresh was published.
func TestIntegration_RosterDrain_AFoundGuestSignsInAtOnce(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startDrain(t)()

	start := time.Now()
	res := f.resolveAs(t, "412", "Okonkwo", "")
	if res.Outcome != outcomeVerified || res.AuthContextID == "" {
		t.Fatalf("a guest whose stay is in the roster was refused while a refresh was written in: %+v (attempt %s)",
			res, f.lastAttemptResult(t))
	}
	if d := time.Since(start); d > drainWaitMax {
		t.Fatalf("a found guest waited %v for the drain; only a non-match may wait", d)
	}
}

// Many guests at once during the drain: every one is answered from the roster, none is held or refused.
func TestIntegration_RosterDrain_ConcurrentSignInsAllVerify(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startDrain(t)()

	const n = 12
	var wg sync.WaitGroup
	results := make([]phase3Response, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = f.resolveAs(t, "412", "Okonkwo", "")
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if r.Outcome != outcomeVerified {
			t.Fatalf("concurrent sign-in %d during a drain was refused: %+v", i, r)
		}
	}
}

// A guest not (yet) in the roster is never told their details are wrong while rows are still to come: the answer is
// technical and is not counted against the device.
func TestIntegration_RosterDrain_ANonMatchIsHeldBackNotRefusedAsWrongDetails(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startDrain(t)()

	start := time.Now()
	res := f.resolveAs(t, "412", "Newcomer", "")
	if res.Outcome == outcomeVerified {
		t.Fatalf("a guest who is not in the roster was let in: %+v", res)
	}
	if res.FailureClass == "CREDENTIAL" {
		t.Fatalf("during a drain a non-match was answered as wrong details (%+v); their row may still be on its way", res)
	}
	if got := f.lastAttemptResult(t); got == "CREDENTIAL_MISMATCH" || got == "ROOM_NOT_IN_MIRROR" || got == "STAY_NOT_ELIGIBLE" {
		t.Fatalf("the attempt was recorded as %s, which counts against the guest", got)
	}
	if d := time.Since(start); d < drainWaitMax-2*drainWaitStep {
		t.Fatalf("the non-match was answered after %v without waiting for the drain", d)
	}
}

// When the drain ends inside the wait, the guest gets the real answer from the complete roster.
func TestIntegration_RosterDrain_ANonMatchIsAnsweredOnceTheDrainEnds(t *testing.T) {
	f := newAuthFixture(t)
	end := f.startDrain(t)
	go func() { time.Sleep(300 * time.Millisecond); end() }()

	res := f.resolveAs(t, "412", "Newcomer", "")
	if res.Outcome == outcomeVerified {
		t.Fatalf("a guest who is not in the roster was let in: %+v", res)
	}
	if res.FailureClass != "CREDENTIAL" {
		t.Fatalf("after the drain ended the complete roster answers; want the wrong-details answer, got %+v", res)
	}
}

// A LIVE event waiting during a drain is newer than the roster and may be a checkout: it still closes Room sign-in.
func TestIntegration_RosterDrain_ALiveEventStillBlocks(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startDrain(t)()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_events
		(id, tenant_id, site_id, pms_interface_id, external_event_identity, event_type, received_at,
		 sequence_version, normalization_version, clock_suspect, payload, processing_status,
		 admission_kind, admission_runtime_generation, resync_generation, fingerprint_key_version)
		VALUES (gen_random_uuid(), $1,$2,$3, 'live-' || gen_random_uuid()::text, 'GO', now(),
		        0, 1, false, '{}'::jsonb, 'PENDING', 'LIVE', 1, 0, 1)`, f.tenant, f.site, f.iface); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if res := f.resolveAs(t, "412", "Okonkwo", ""); res.Outcome == outcomeVerified {
		t.Fatalf("a guest was let in with a checkout waiting to be applied: %+v", res)
	}
}
