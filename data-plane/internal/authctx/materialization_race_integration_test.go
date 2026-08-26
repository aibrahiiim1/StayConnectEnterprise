//go:build integration

package authctx

// THE RACE THIS CLOSES, RUN AS A RACE.
//
// A predicate alone cannot close it. p3_feed_authorizes is STABLE and these transactions are READ COMMITTED,
// so evaluating "the roster is materialized" proves only that it was true as of one statement's snapshot — a
// concurrent pmsd admission can commit a conflicting occupancy event immediately afterwards and the
// authorisation can still commit on top of it.
//
// That was measured against PostgreSQL 16 before this fix existed:
//
//	A1 predicate = true, Stay locked
//	B  admission COMMITTED 3s later, never blocked (it locks the RUNTIME row, auth locks the STAY row)
//	A3 auth COMMITTED 8s later, having never observed the pending event
//
// SERIALIZABLE does not help either — also measured. The auth transaction reads stay_events and writes
// auth_contexts while admission writes stay_events and reads nothing auth wrote, so SSI finds no dangerous
// cycle and aborts nobody.
//
// The fix is the lock these tests assert: callers take pms_interface_runtime FOR UPDATE, the row admission
// already serializes on, so from that point until commit no new occupancy event can be admitted.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// admitPending inserts a durable PENDING occupancy event exactly as pmsd's admission path does: runtime row
// locked first, then the insert, then commit.
func admitPending(t *testing.T, s fixture, kind string, gen int64) {
	t.Helper()
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin admission: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM iam_v2.pms_interface_runtime
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 FOR UPDATE`,
		s.tenant, s.site, s.iface); err != nil {
		t.Fatalf("admission runtime lock: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_events
		(id, tenant_id, site_id, pms_interface_id, external_event_identity, event_type, received_at,
		 sequence_version, normalization_version, clock_suspect, payload, processing_status,
		 admission_kind, admission_runtime_generation, resync_generation, fingerprint_key_version)
		VALUES (gen_random_uuid(), $1,$2,$3, 'race-' || gen_random_uuid()::text, 'GO', now(),
		        0, 1, false, '{}'::jsonb, 'PENDING', $4, 1, $5, 1)`,
		s.tenant, s.site, s.iface, kind, gen); err != nil {
		t.Fatalf("admit pending event: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit admission: %v", err)
	}
}

// A CLAIMABLE PENDING EVENT CLOSES ROOM AUTH — the everyday case, no full sync involved.
func TestIntegration_Materialization_LivePendingBlocks(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()
	setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

	if !authorizable(t, p, s) {
		t.Fatal("setup: a healthy caught-up feed should authorise")
	}
	admitPending(t, s, "LIVE", 0) // se_admission_coherent: LIVE rows carry generation 0
	if authorizable(t, p, s) {
		t.Fatal("a guest was authorised while a LIVE occupancy event was durable and unapplied. If that " +
			"event is a GO, this is a checked-out guest getting internet")
	}
}

// AN ABANDONED GENERATION MUST NOT BLOCK — the case that would otherwise kill D39 offline auth permanently.
//
// An interrupted resync leaves rows PENDING at a generation that will never publish. The applier can never
// claim them, so blocking on them would disable Room sign-in forever, including offline where the mirror is
// otherwise perfectly serviceable.
func TestIntegration_Materialization_AbandonedGenerationDoesNotBlock(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()
	setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

	// published_resync_generation stays where it is; the row belongs to a HIGHER, never-published generation.
	var published int64
	if err := p.QueryRow(context.Background(),
		`SELECT published_resync_generation FROM iam_v2.pms_interface_runtime WHERE pms_interface_id=$1`,
		s.iface).Scan(&published); err != nil {
		t.Fatal(err)
	}
	abandoned := published + 1 // never published, so the applier can never claim it
	admitPending(t, s, "RESYNC", abandoned)

	if !authorizable(t, p, s) {
		t.Fatal("an abandoned resync generation blocked Room auth. The applier can never consume those rows, " +
			"so this would refuse every guest permanently — including offline, where the mirror is fine")
	}
}

// A PUBLISHED-GENERATION PENDING ROW DOES BLOCK: the post-publish drain, which is the window measured live.
func TestIntegration_Materialization_PublishedGenerationPendingBlocks(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()
	setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

	var published int64
	if err := p.QueryRow(context.Background(),
		`SELECT published_resync_generation FROM iam_v2.pms_interface_runtime WHERE pms_interface_id=$1`,
		s.iface).Scan(&published); err != nil {
		t.Fatal(err)
	}
	// se_admission_coherent requires a RESYNC row to carry a generation > 0, so a fixture published at 0 has
	// to advance first. This is what a completed sync leaves behind anyway.
	if published == 0 {
		if _, err := p.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
			SET resync_generation_seq=1, published_resync_generation=1 WHERE pms_interface_id=$1`,
			s.iface); err != nil {
			t.Fatal(err)
		}
		published = 1
	}
	admitPending(t, s, "RESYNC", published) // at the published generation: claimable

	if authorizable(t, p, s) {
		t.Fatal("a guest was authorised during the post-publish drain — the exact 3.5-second window measured " +
			"on the appliance, where ~129 in-house guests did not yet exist in iam_v2.stays")
	}
}

// TERMINAL ROWS NEVER BLOCK. APPLIED, MANUAL_REVIEW and SKIPPED_DUPLICATE are finished work; the 608
// GO_UNKNOWN_STAY rows on the appliance are MANUAL_REVIEW and must not refuse a single guest.
func TestIntegration_Materialization_TerminalRowsDoNotBlock(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	for _, status := range []string{"APPLIED", "MANUAL_REVIEW", "SKIPPED_DUPLICATE"} {
		t.Run(status, func(t *testing.T) {
			s := seedCacheAge(t, p, "null")
			now := time.Now().UTC()
			setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
			setEvidenceAge(t, p, s, now.Add(-2*time.Hour))
			admitPending(t, s, "LIVE", 0) // se_admission_coherent: LIVE rows carry generation 0
			if _, err := p.Exec(ctx,
				`UPDATE iam_v2.stay_events SET processing_status=$2, processed_at=now()
				  WHERE pms_interface_id=$1 AND processing_status='PENDING'`, s.iface, status); err != nil {
				t.Fatal(err)
			}
			if !authorizable(t, p, s) {
				t.Fatalf("terminal %s rows blocked Room auth; only unfinished work may", status)
			}
		})
	}
}

// THE OFFLINE BRANCH IS GATED TOO. Materialization is a property of the mirror, not of the transport: a
// mirror the applier has not caught up with is incomplete, whether or not the socket is up.
func TestIntegration_Materialization_OfflineBranchIsAlsoGated(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()
	setFeed(t, p, s, "DISCONNECTED", "RESYNC_REQUIRED", "CONTINUOUS", now.Add(-time.Hour))
	establishMirror(t, p, s, now.Add(-2*time.Hour))
	setEvidenceAge(t, p, s, now.Add(-3*time.Hour))

	if !authorizable(t, p, s) {
		t.Fatal("setup: D39 offline auth should work on a trusted drained mirror")
	}
	admitPending(t, s, "LIVE", 0) // se_admission_coherent: LIVE rows carry generation 0
	if authorizable(t, p, s) {
		t.Fatal("the offline branch authorised with a claimable pending event outstanding. The applier keeps " +
			"draining without the socket, so this resolves in seconds — but not before")
	}
}

// THE LINEARIZATION LOCK, ASSERTED AS AN ORDERING not as a hope.
//
// Session A takes the Stay lock then the runtime lock and holds them. Session B attempts an admission. B must
// not commit until A releases — that is the entire safety property, and it is what makes the STABLE predicate
// trustworthy at commit time rather than only at snapshot time.
func TestIntegration_Materialization_RuntimeLockSerializesAdmission(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	ctx := context.Background()

	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM iam_v2.stays WHERE id=$1 FOR UPDATE`, s.stay); err != nil {
		t.Fatalf("stay lock: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM iam_v2.pms_interface_runtime
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 FOR UPDATE`,
		s.tenant, s.site, s.iface); err != nil {
		t.Fatalf("runtime lock: %v", err)
	}

	// A second connection tries to take the same runtime row with NOWAIT: if the lock were not held, this
	// would succeed and the test would be asserting nothing.
	p2 := pool(t)
	defer p2.Close()
	tx2, err := p2.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback(ctx) }()
	_, err = tx2.Exec(ctx, `SELECT 1 FROM iam_v2.pms_interface_runtime
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 FOR UPDATE NOWAIT`,
		s.tenant, s.site, s.iface)
	if err == nil {
		t.Fatal("a concurrent admission acquired the runtime row while an auth transaction held it. Without " +
			"that exclusion the auth predicate is only ever true as of its own snapshot")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("expected lock_not_available (55P03), got %v", err)
	}
}

// NO NEW DEADLOCK CYCLE. Auth takes stays then runtime; admission takes runtime only and never takes a Stay
// lock. Nothing acquires stays after runtime, so the two orders cannot form a cycle — run concurrently here
// rather than argued, because "no cycle exists" is exactly the claim that is embarrassing to get wrong.
func TestIntegration_Materialization_NoDeadlockUnderConcurrency(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")

	done := make(chan error, 8)
	for i := 0; i < 4; i++ {
		go func() { // auth order: stays -> runtime
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			tx, err := p.Begin(ctx)
			if err != nil {
				done <- err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SELECT 1 FROM iam_v2.stays WHERE id=$1 FOR UPDATE`, s.stay); err != nil {
				done <- err
				return
			}
			if _, err := tx.Exec(ctx, `SELECT 1 FROM iam_v2.pms_interface_runtime
				WHERE pms_interface_id=$1 FOR UPDATE`, s.iface); err != nil {
				done <- err
				return
			}
			done <- tx.Commit(ctx)
		}()
		go func() { // admission order: runtime only
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			tx, err := p.Begin(ctx)
			if err != nil {
				done <- err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SELECT 1 FROM iam_v2.pms_interface_runtime
				WHERE pms_interface_id=$1 FOR UPDATE`, s.iface); err != nil {
				done <- err
				return
			}
			done <- tx.Commit(ctx)
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
				t.Fatalf("DEADLOCK detected: %v. The lock order is supposed to be acyclic — auth takes "+
					"stays then runtime, admission takes runtime only", err)
			}
			t.Fatalf("concurrent transaction failed: %v", err)
		}
	}
}

// LEAST PRIVILEGE. The readiness term reads iam_v2.stay_events from inside p3_feed_authorizes, and
// internal/authctx takes a FOR UPDATE on iam_v2.pms_interface_runtime. Both must already be within svc_scd's
// existing grants: this change must widen nothing.
func TestIntegration_Materialization_NoPrivilegeWidening(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()

	var exists bool
	if err := p.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='svc_scd')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Skip("svc_scd is not provisioned in this disposable database; Gate-P asserts this in production")
	}
	for _, c := range []struct{ obj, priv string }{
		{"iam_v2.stay_events", "SELECT"},
		{"iam_v2.pms_interface_runtime", "SELECT"},
	} {
		var ok bool
		if err := p.QueryRow(ctx, `SELECT has_table_privilege('svc_scd', $1, $2)`, c.obj, c.priv).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("svc_scd lacks %s on %s, so this change would require widening privilege. It must not: "+
				"the readiness term and the linearization lock were chosen to fit inside existing grants",
				c.priv, c.obj)
		}
	}
}
