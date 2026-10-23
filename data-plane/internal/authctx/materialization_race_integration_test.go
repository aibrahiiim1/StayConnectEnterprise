//go:build integration

package authctx

// A predicate alone cannot close the race: p3_feed_authorizes is STABLE under READ COMMITTED, so it proves the
// roster was materialized only as of one snapshot, and a concurrent pmsd admission can commit a conflicting
// event right after. SERIALIZABLE does not help (SSI sees no dangerous cycle). The fix asserted here is that
// callers take pms_interface_runtime FOR UPDATE, the row admission serializes on.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// admitPending mirrors pmsd's admission: lock the runtime row, insert, commit.
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
	if err := openOps(ctx, tx, "stay"); err != nil { // stay_events is a controlled-writer family
		t.Fatalf("admission open stay operation: %v", err)
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

// A claimable pending event closes room auth.
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

// An abandoned generation must not block: an interrupted resync leaves PENDING rows the applier can never
// claim, and blocking on them would disable Room sign-in forever, including offline.
func TestIntegration_Materialization_AbandonedGenerationDoesNotBlock(t *testing.T) {
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
	abandoned := published + 1 // never published, so the applier can never claim it
	admitPending(t, s, "RESYNC", abandoned)

	if !authorizable(t, p, s) {
		t.Fatal("an abandoned resync generation blocked Room auth. The applier can never consume those rows, " +
			"so this would refuse every guest permanently — including offline, where the mirror is fine")
	}
}

// A pending row at the published generation does block (the post-publish drain).
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
	// se_admission_coherent requires a RESYNC row to carry a generation > 0, so advance first.
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

// Terminal rows (APPLIED, MANUAL_REVIEW, SKIPPED_DUPLICATE) never block.
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
			// APPLIED demands a resolved same-interface stay_id and MANUAL_REVIEW a bounded review_code.
			if _, err := gexec(ctx, p, "stay",
				`UPDATE iam_v2.stay_events
				    SET processing_status=$2, processed_at=now(),
				        stay_id = CASE WHEN $2='APPLIED' THEN $3::uuid ELSE stay_id END,
				        review_code = CASE WHEN $2='MANUAL_REVIEW' THEN 'GO_UNKNOWN_STAY' ELSE review_code END
				  WHERE pms_interface_id=$1 AND processing_status='PENDING'`,
				s.iface, status, s.stay); err != nil {
				t.Fatal(err)
			}
			if !authorizable(t, p, s) {
				t.Fatalf("terminal %s rows blocked Room auth; only unfinished work may", status)
			}
		})
	}
}

// The offline branch is gated too: materialization is a property of the mirror, not the transport.
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

// Session A holds the Stay then runtime lock; B's admission must not commit until A releases. That makes the
// STABLE predicate trustworthy at commit time.
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

	// NOWAIT on a second connection proves the lock is actually held.
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

// No deadlock cycle: auth takes stays then runtime; admission takes runtime only.
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

// Least privilege: svc_scd can EXECUTE the definer functions p3_feed_authorizes and
// lock_pms_interface_runtime and holds no direct privilege on stay_events or pms_interface_runtime
// (deploy/gatep/svc-scd-iamv2-guest-auth-grants.sql).
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
	for _, fn := range []string{
		"iam_v2.p3_feed_authorizes(uuid,uuid,uuid,uuid,timestamptz)",
		"iam_v2.lock_pms_interface_runtime(uuid,uuid,uuid)",
	} {
		var exec, secdef bool
		var owner string
		if err := p.QueryRow(ctx, `SELECT has_function_privilege('svc_scd', $1::regprocedure, 'EXECUTE'),
		        p.prosecdef, pg_get_userbyid(p.proowner)
		   FROM pg_proc p WHERE p.oid = $1::regprocedure`, fn).Scan(&exec, &secdef, &owner); err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		if !exec {
			t.Fatalf("svc_scd lacks EXECUTE on %s, so the readiness term / linearization lock would require widening "+
				"privilege. It must not: they were chosen to fit inside existing grants", fn)
		}
		if !secdef || owner != "iam_v2_owner" {
			t.Fatalf("%s must be SECURITY DEFINER owned by iam_v2_owner (secdef=%v owner=%s): otherwise svc_scd "+
				"would need direct table privilege", fn, secdef, owner)
		}
	}
	for _, obj := range []string{"iam_v2.stay_events", "iam_v2.pms_interface_runtime"} {
		for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			var ok bool
			if err := p.QueryRow(ctx, `SELECT has_table_privilege('svc_scd', $1, $2)`, obj, priv).Scan(&ok); err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Fatalf("svc_scd holds %s on %s: the guest-auth path was widened beyond Gate-P's definer-function "+
					"grants (deploy/gatep/svc-scd-iamv2-guest-auth-grants.sql)", priv, obj)
			}
		}
	}
}
