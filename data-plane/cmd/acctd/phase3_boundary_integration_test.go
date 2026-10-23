//go:build integration

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// asProbe creates a non-owner role with full table DML on the accounting chain: even that role must not be able
// to write accounting state. A probe without privileges would fail with "permission denied" and prove nothing.
func asProbe(t *testing.T, f *ingestFixture) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		DO $$ BEGIN CREATE ROLE p3_raw_probe NOLOGIN NOSUPERUSER; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
		GRANT USAGE ON SCHEMA iam_v2 TO p3_raw_probe;
		GRANT SELECT, INSERT, UPDATE, DELETE ON
		  iam_v2.accounting_records, iam_v2.accounting_checkpoints,
		  iam_v2.delayed_accounting_records, iam_v2.sessions TO p3_raw_probe;`); err != nil {
		t.Fatalf("prepare the raw-write probe: %v", err)
	}
}

// rawDenied asserts the controlled-writer boundary, not some other error, refused the statement.
func rawDenied(t *testing.T, f *ingestFixture, what, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE p3_raw_probe`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err == nil {
		t.Fatalf("%s was accepted as a raw write; the controlled-writer boundary did not hold", what)
	} else if !strings.Contains(err.Error(), "controlled") && !strings.Contains(err.Error(), "immutable") {
		// Only the controlled-writer or immutability guard counts; any other error means it was blocked by accident.
		t.Fatalf("%s failed for the wrong reason: %v", what, err)
	}
}

// rawAllowed asserts the boundary is precise, not a blanket lock on ordinary session writes.
func rawAllowed(t *testing.T, f *ingestFixture, what, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE p3_raw_probe`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("the boundary blocked %s: %v", what, err)
	}
}

func TestIntegration_Acct_RawWritesAreRefused(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	asProbe(t, f)
	_, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 1000, 2000)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())
	c.absolutes(t, "br-guest", "10.9.0.1", 1400, 2600)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	// invented usage
	rawDenied(t, f, "a raw accounting_records INSERT", `
		INSERT INTO iam_v2.accounting_records (tenant_id, site_id, session_id, sample_seq, bytes_up, bytes_down, sampled_at)
		VALUES ($1,$2,$3,99,500,500,now())`, f.tenant, f.site, sess)

	// rewritten history
	rawDenied(t, f, "a raw accounting_records UPDATE",
		`UPDATE iam_v2.accounting_records SET bytes_up = 0 WHERE session_id=$1`, sess)

	// erased history
	rawDenied(t, f, "a raw accounting_records DELETE",
		`DELETE FROM iam_v2.accounting_records WHERE session_id=$1`, sess)

	// A checkpoint reset to zero would bill the guest for the session's entire history.
	rawDenied(t, f, "a raw checkpoint UPDATE",
		`UPDATE iam_v2.accounting_checkpoints SET prev_bytes_up = 0, prev_bytes_down = 0 WHERE session_id=$1`, sess)
	rawDenied(t, f, "a raw checkpoint DELETE",
		`DELETE FROM iam_v2.accounting_checkpoints WHERE session_id=$1`, sess)
	rawDenied(t, f, "a raw checkpoint INSERT", `
		INSERT INTO iam_v2.accounting_checkpoints
		  (tenant_id, site_id, session_id, source_device_id, bridge, class_minor, source_epoch,
		   prev_bytes_up, prev_bytes_down, prev_sampled_at, last_classification)
		VALUES ($1,$2,$3,$4,'br-evil',1,1,0,0,now(),'BASELINED')`, f.tenant, f.site, sess, f.device)

	// a total moved without a record behind it
	rawDenied(t, f, "a raw session usage UPDATE",
		`UPDATE iam_v2.sessions SET bytes_up = bytes_up + 10000 WHERE id=$1`, sess)

	rawAllowed(t, f, "an ordinary session write",
		`UPDATE iam_v2.sessions SET expires_at = now() + interval '1 hour' WHERE id=$1`, sess)

	var up, down int64
	if err := f.pool.QueryRow(ctx,
		`SELECT bytes_up, bytes_down FROM iam_v2.accounting_records WHERE session_id=$1`, sess).Scan(&up, &down); err != nil {
		t.Fatal(err)
	}
	if up != 400 || down != 600 {
		t.Fatalf("the stored measurement was altered: %d/%d", up, down)
	}
}

func TestIntegration_Acct_ControlledFunctionShape(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()

	var secdef bool
	var cfg []string
	var owner string
	err := f.pool.QueryRow(ctx, `
		SELECT p.prosecdef, COALESCE(p.proconfig, ARRAY[]::text[]), pg_get_userbyid(p.proowner)
		  FROM pg_proc p
		 WHERE p.oid = to_regprocedure('iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)')
	`).Scan(&secdef, &cfg, &owner)
	if err != nil {
		t.Fatalf("the approved accounting operation is not resolvable: %v", err)
	}
	if !secdef {
		t.Fatal("the accounting operation is not SECURITY DEFINER; the table guards could never be satisfied")
	}
	// An unpinned search_path on a SECURITY DEFINER function lets the caller choose which schema names resolve to.
	pinned := false
	for _, c := range cfg {
		if strings.HasPrefix(c, "search_path=") {
			pinned = true
			if !strings.Contains(c, "iam_v2") {
				t.Fatalf("search_path is pinned to %q, which does not include iam_v2", c)
			}
		}
	}
	if !pinned {
		t.Fatal("the accounting operation does not pin its search_path")
	}

	// PUBLIC EXECUTE is revoked; access is granted per role.
	var publicExec bool
	if err := f.pool.QueryRow(ctx, `
		SELECT has_function_privilege('public',
		  to_regprocedure('iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)'), 'EXECUTE')
	`).Scan(&publicExec); err != nil {
		t.Fatal(err)
	}
	if publicExec {
		t.Fatal("PUBLIC can execute the accounting operation")
	}

	// The table guards must resolve the same owner, or the operation's own writes would be refused.
	var guardOwner string
	if err := f.pool.QueryRow(ctx, `SELECT iam_v2.p3_controlled_writer_owner('accounting')`).Scan(&guardOwner); err != nil {
		t.Fatalf("the accounting writer family is not resolvable: %v", err)
	}
	if guardOwner != owner {
		t.Fatalf("the guard expects writer %q but the operation runs as %q", guardOwner, owner)
	}

	// The superseded function must be gone: a second, weaker writer is a bypass even if unused.
	var stillThere bool
	if err := f.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		                WHERE n.nspname='iam_v2' AND p.proname='ingest_accounting_sample')`).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere {
		t.Fatal("the superseded accounting-ingestion function still exists as an alternative writer")
	}
}

// A sample belongs to the Entitlement bound when it was measured, even if ingested after a grace rebinding.
func TestIntegration_Acct_AttributionFollowsTheBindingAtSampleTime(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()

	started := time.Now().Add(-3 * time.Hour)
	first := f.grantEntitlement(t, started, nil)
	sess := f.openSessionOn(t, first, f.device, started, "10.9.0.1", "br-guest")
	ep := newEpochs()
	ep.set("br-guest", sess, 1)

	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 1000, 1000)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now().Add(-30*time.Minute))

	beforeBoundary := time.Now().Add(-20 * time.Minute)
	c.absolutes(t, "br-guest", "10.9.0.1", 1300, 1500)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", beforeBoundary); n != 1 {
		t.Fatal("the pre-boundary observation was not accepted")
	}

	boundary := time.Now().Add(-15 * time.Minute)
	if _, err := f.pool.Exec(ctx, `
		SELECT iam_v2.terminate_entitlement_at_boundary($1, $2, 'CHECKOUT')`, first, boundary); err != nil {
		t.Fatalf("terminate at the boundary: %v", err)
	}
	second := f.grantEntitlement(t, boundary, nil)
	// Admit the device before rebinding: a live session may not sit on an entitlement it has no authorization binding for.
	if _, err := f.pool.Exec(ctx,
		`SELECT iam_v2.authorize_entitlement_device($1,$2,$3)`, second, f.device, boundary); err != nil {
		t.Fatalf("admit the device to the successor entitlement: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`SELECT iam_v2.rebind_session_entitlement($1,$2,$3)`, sess, second, boundary); err != nil {
		t.Fatalf("rebind: %v", err)
	}

	upBefore, downBefore := f.usage(t, first)
	if upBefore != 300 || downBefore != 500 {
		t.Fatalf("pre-boundary usage = %d/%d, want 300/500", upBefore, downBefore)
	}

	c.absolutes(t, "br-guest", "10.9.0.1", 1500, 1900)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now().Add(-5*time.Minute)); n != 1 {
		t.Fatal("the post-boundary observation was not accepted")
	}
	if u, d := f.usage(t, first); u != upBefore || d != downBefore {
		t.Fatalf("post-boundary traffic was charged to the pre-boundary entitlement: %d/%d", u, d)
	}
	if u, d := f.usage(t, second); u != 200 || d != 400 {
		t.Fatalf("grace usage = %d/%d, want 200/400", u, d)
	}

	// Attribution comes from the interval history, not a pointer that has since moved.
	if n := f.countRows(t, `
		SELECT count(*) FROM iam_v2.accounting_records ar
		 WHERE ar.session_id=$1 AND EXISTS (
		   SELECT 1 FROM iam_v2.session_entitlement_bindings b
		    WHERE b.session_id=ar.session_id AND b.entitlement_id=$2
		      AND b.bound_from <= ar.sampled_at AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at))`,
		sess, first); n != 1 {
		t.Fatalf("pre-boundary records attributed to the first entitlement = %d, want 1", n)
	}
}

// A sample in a gap with no binding is refused, never attributed to the session's current entitlement.
func TestIntegration_Acct_NoBindingIsRefusedNotAttributedToTheCurrentOne(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 100, 100)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	// Close the open interval, leaving the session bound to nothing.
	gap := time.Now()
	if err := opExec(t, f.pool, "session_binding", `
		UPDATE iam_v2.session_entitlement_bindings SET bound_until=$2
		 WHERE session_id=$1 AND bound_until IS NULL`, sess, gap.Add(-time.Minute)); err != nil {
		t.Fatalf("close the binding: %v", err)
	}

	c.absolutes(t, "br-guest", "10.9.0.1", 900, 900)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", gap); n != 0 {
		t.Fatalf("an unattributable sample was billed to %d records", n)
	}
	if rows := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); rows != 0 {
		t.Fatalf("an unattributable sample stored %d records", rows)
	}
	// The checkpoint is untouched, so the traffic is not lost.
	if up, down, _, _ := f.checkpoint(t, sess); up != 100 || down != 100 {
		t.Fatalf("a refused sample moved the checkpoint to %d/%d", up, down)
	}
	if p := f.p3.AccountingDegraded(); p == "" {
		t.Fatal("a refused observation was not reported as degraded")
	}
}

// A guest on a different bridge is a new Session with its own counter series; measuring it against the old
// checkpoint would compute a delta between unrelated series.
func TestIntegration_Acct_ADifferentBridgeIsADifferentSeries(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sessA, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 5000, 9000)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	rawDenied(t, f, "a raw session interface rewrite",
		`UPDATE iam_v2.sessions SET ingress_interface='br-g301' WHERE id=$1`, sessA)
	rawDenied(t, f, "a raw session address rewrite",
		`UPDATE iam_v2.sessions SET ip='10.9.0.77'::inet WHERE id=$1`, sessA)

	started := time.Now().Add(-time.Hour)
	entB := f.grantEntitlementFor(t, f.device2, started)
	sessB := f.openSessionOn(t, entB, f.device2, started, "10.9.0.2", "br-g301")
	ep.set("br-g301", sessB, 1)
	c.absolutes(t, "br-g301", "10.9.0.2", 10, 20)

	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("the first observation on a new series billed %d samples; it must baseline", n)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_checkpoints WHERE session_id IN ($1,$2)`, sessA, sessB); n != 2 {
		t.Fatalf("checkpoints = %d, want one per counter series", n)
	}
	if rows := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id IN ($1,$2)`, sessA, sessB); rows != 0 {
		t.Fatalf("two separate series produced %d accounting rows", rows)
	}
}

// Devices sharing one entitlement are measured independently; a checkpoint keyed only by session or class
// minor would use one device's counter as the other's baseline.
func TestIntegration_Acct_TwoDevicesOnOneEntitlement(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-2 * time.Hour)
	ent := f.grantEntitlement(t, started, nil)

	sessA := f.openSessionOn(t, ent, f.device, started, "10.9.0.1", "br-guest")
	sessB := f.openSessionOn(t, ent, f.device2, started, "10.9.0.2", "br-guest")
	ep := newEpochs()
	ep.set("br-guest", sessA, 1)
	ep.set("br-guest", sessB, 1)

	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 1000, 1000)
	c.absolutes(t, "br-guest", "10.9.0.2", 7000, 7000)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	c.absolutes(t, "br-guest", "10.9.0.1", 1100, 1200)
	c.absolutes(t, "br-guest", "10.9.0.2", 7300, 7400)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 2 {
		t.Fatalf("accepted %d observations, want one per device", n)
	}
	if u, d := f.usage(t, ent); u != 400 || d != 600 {
		t.Fatalf("shared entitlement usage = %d/%d, want the sum 400/600", u, d)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_checkpoints WHERE session_id IN ($1,$2)`, sessA, sessB); n != 2 {
		t.Fatalf("checkpoints = %d, want one per device series", n)
	}
}

// While dark, the accounting pass issues no query, writes nothing and leaves the legacy writer in charge.
func TestIntegration_Acct_DarkWritesNothingAnywhere(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 1000, 2000)

	var dark *phase3
	if n := dark.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("a dark arm accounted %d observations", n)
	}
	if c.reads != 0 {
		t.Fatalf("a dark arm read tc counters %d times", c.reads)
	}
	if dark.ownsAccounting() {
		t.Fatal("a dark arm claimed accounting ownership; the legacy writer would have stood down for nothing")
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_checkpoints WHERE session_id=$1`, sess); n != 0 {
		t.Fatalf("a dark arm created %d checkpoints", n)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); n != 0 {
		t.Fatalf("a dark arm stored %d accounting rows", n)
	}
}

// A forged row with no binding at its sample time must not fall back to sessions.entitlement_id (who owns the
// session now, not then): nothing is created and nothing moves.
func TestIntegration_Acct_ForgedRowCannotBeAttributedThroughTheCurrentPointer(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sess, ep := f.live(t, "10.9.0.1", "br-guest")

	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 100, 100)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())
	c.absolutes(t, "br-guest", "10.9.0.1", 300, 400)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	beforeUp, beforeDown := f.sessionTotals(t, sess)
	ckUp, ckDown, _, _ := f.checkpoint(t, sess)
	records := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess)

	// Leave sessions.entitlement_id pointing at the entitlement but unbound at the forged sample's time.
	gap := time.Now()
	if err := opExec(t, f.pool, "session_binding", `
		UPDATE iam_v2.session_entitlement_bindings SET bound_until=$2
		 WHERE session_id=$1 AND bound_until IS NULL`, sess, gap.Add(-time.Minute)); err != nil {
		t.Fatalf("close the binding: %v", err)
	}
	var pointer string
	if err := f.pool.QueryRow(ctx, `SELECT entitlement_id::text FROM iam_v2.sessions WHERE id=$1`, sess).Scan(&pointer); err != nil {
		t.Fatal(err)
	}
	if pointer == "" {
		t.Fatal("setup: the session lost its current pointer, so this test would prove nothing")
	}

	_, err := f.pool.Exec(ctx, `
		INSERT INTO iam_v2.accounting_records (tenant_id, site_id, session_id, sample_seq, bytes_up, bytes_down, sampled_at)
		VALUES ($1,$2,$3,9999,777,888,$4)`, f.tenant, f.site, sess, gap)
	if err == nil {
		t.Fatal("a forged accounting row with no binding at its sample time was accepted")
	}
	if !strings.Contains(err.Error(), "ACCT_NO_BINDING") && !strings.Contains(err.Error(), "controlled") {
		t.Fatalf("the forged row was refused for the wrong reason: %v", err)
	}

	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); n != records {
		t.Fatalf("accounting rows changed from %d to %d", records, n)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.delayed_accounting_records WHERE session_id=$1`, sess); n != 0 {
		t.Fatalf("a delayed-accounting row was created for a refused sample (%d)", n)
	}
	if u, d := f.sessionTotals(t, sess); u != beforeUp || d != beforeDown {
		t.Fatalf("session totals moved from %d/%d to %d/%d", beforeUp, beforeDown, u, d)
	}
	if u, d, _, _ := f.checkpoint(t, sess); u != ckUp || d != ckDown {
		t.Fatalf("the checkpoint advanced from %d/%d to %d/%d", ckUp, ckDown, u, d)
	}
}

// The operation and the triggers beneath it must give the same binding answer, or the boundary has a seam.
func TestIntegration_Acct_OperationAndTriggersShareOneBindingAnswer(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 10, 10)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	var covered, uncovered *string
	if err := f.pool.QueryRow(ctx,
		`SELECT iam_v2.p3_entitlement_at($1, now())::text`, sess).Scan(&covered); err != nil {
		t.Fatal(err)
	}
	if covered == nil {
		t.Fatal("the resolver found no entitlement for a live, bound session")
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT iam_v2.p3_entitlement_at($1, now() - interval '10 hours')::text`, sess).Scan(&uncovered); err != nil {
		t.Fatal(err)
	}
	if uncovered != nil {
		t.Fatalf("the resolver attributed a pre-session instant to %s", *uncovered)
	}

	if _, err := f.pool.Exec(ctx, `
		SELECT iam_v2.ingest_absolute_counters($1,$2,$3::uuid,$4::uuid,'br-guest',4097,1,50,50, now() - interval '10 hours')`,
		f.tenant, f.site, sess, f.device); err == nil {
		t.Fatal("the operation accepted a sample the resolver could not attribute")
	}
}

// The counter-source binding is validated inside PostgreSQL: values acctd would never produce must be refused,
// or the database is a recorder rather than a boundary.
func TestIntegration_Acct_SourceBindingIsProvenInTheDatabase(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 1000, 1000)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	var trueMinor int
	if err := f.pool.QueryRow(ctx,
		`SELECT iam_v2.p3_expected_class_minor(ip) FROM iam_v2.sessions WHERE id=$1`, sess).Scan(&trueMinor); err != nil {
		t.Fatal(err)
	}

	call := func(device, bridge string, minor int) error {
		_, err := f.pool.Exec(ctx, `
			SELECT iam_v2.ingest_absolute_counters($1,$2,$3::uuid,$4::uuid,$5,$6,1,9999,9999,now())`,
			f.tenant, f.site, sess, device, bridge, minor)
		return err
	}

	cases := []struct {
		name           string
		device, bridge string
		minor          int
	}{
		{"a bridge the session is not on", f.device, "br-elsewhere", trueMinor},
		{"a class minor the address does not map to", f.device, "br-guest", trueMinor + 1},
		{"another device's counters", f.device2, "br-guest", trueMinor},
		{"the right device on the wrong bridge with the wrong minor", f.device, "br-g301", trueMinor + 7},
	}
	for _, tc := range cases {
		err := call(tc.device, tc.bridge, tc.minor)
		if err == nil {
			t.Fatalf("%s was accepted", tc.name)
		}
		if !strings.Contains(err.Error(), "ACCT_SOURCE_MISMATCH") {
			t.Fatalf("%s failed for the wrong reason: %v", tc.name, err)
		}
	}

	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_checkpoints WHERE session_id=$1`, sess); n != 1 {
		t.Fatalf("checkpoints for the session = %d, want exactly the one authoritative series", n)
	}
	if u, d, _, _ := f.checkpoint(t, sess); u != 1000 || d != 1000 {
		t.Fatalf("a refused source advanced the authoritative checkpoint to %d/%d", u, d)
	}
}

// A Session with no recorded interface cannot be measured; a default would let the caller decide the counter source.
func TestIntegration_Acct_SessionWithoutAnInterfaceIsNotMeasurable(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	// A live session with no authorization binding is refused, so admit the device first.
	if _, err := f.pool.Exec(ctx,
		`SELECT iam_v2.authorize_entitlement_device($1,$2,$3)`, ent, f.device, started); err != nil {
		t.Fatalf("authorize the device: %v", err)
	}
	var sess string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(tenant_id,site_id,entitlement_id,device_id,state,started,ip)
		VALUES ($1,$2,$3,$4,'active',$5,'10.9.0.5'::inet) RETURNING id::text`,
		f.tenant, f.site, ent, f.device, started).Scan(&sess); err != nil {
		t.Fatal(err)
	}

	if _, err := f.pool.Exec(ctx, `
		SELECT iam_v2.ingest_absolute_counters($1,$2,$3::uuid,$4::uuid,'br-guest',4101,1,10,10,now())`,
		f.tenant, f.site, sess, f.device); err == nil {
		t.Fatal("a session with no ingress interface was measured against a caller-supplied bridge")
	}

	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.5", 10, 10)
	ep := newEpochs()
	ep.set("br-guest", sess, 1)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("an unmeasurable session was accounted %d times", n)
	}
	if f.p3.AccountingDegraded() == "" {
		t.Fatal("a live session that cannot be measured was not reported as degraded")
	}
}

// Time only moves forward within a series: without comparing against prev_sampled_at, a late OLDER reading
// would count as new usage dated into a possibly frozen window.
func TestIntegration_Acct_SampleTimeCannotMoveBackwards(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sess, _ := f.live(t, "10.9.0.1", "br-guest")

	ing := func(up, down int64, at time.Time) (string, error) {
		var class string
		err := f.pool.QueryRow(ctx, `
			SELECT iam_v2.ingest_absolute_counters($1,$2,$3::uuid,$4::uuid,'br-guest',4097,1,$5,$6,$7)`,
			f.tenant, f.site, sess, f.device, up, down, at).Scan(&class)
		return class, err
	}

	t0 := time.Now().Add(-30 * time.Minute)
	if c, err := ing(1000, 1000, t0); err != nil || c != "BASELINED" {
		t.Fatalf("baseline: %s %v", c, err)
	}
	t1 := t0.Add(5 * time.Minute)
	if c, err := ing(1400, 1600, t1); err != nil || c != "ACCEPTED" {
		t.Fatalf("second observation: %s %v", c, err)
	}

	if _, err := ing(1500, 1700, t0.Add(time.Minute)); err == nil {
		t.Fatal("an observation dated before the last accepted one was accepted as new usage")
	} else if !strings.Contains(err.Error(), "ACCT_STALE_SAMPLE") {
		t.Fatalf("wrong refusal: %v", err)
	}

	// An exact replay (uncertain commit) reports what was persisted, marked as a replay, so a retry is not new traffic.
	if c, err := ing(1400, 1600, t1); err != nil {
		t.Fatalf("exact replay: %v", err)
	} else if c != "REPLAY:ACCEPTED" {
		t.Fatalf("an exact replay reported %q; want the persisted classification, marked as a replay", c)
	}

	// The same instant with advanced counters is real usage on a coarse clock; refusing it would discard bytes.
	if c, err := ing(1450, 1650, t1); err != nil {
		t.Fatalf("a same-instant advance was refused: %v", err)
	} else if c != "ACCEPTED" {
		t.Fatalf("a same-instant advance was classified %q", c)
	}

	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); n != 2 {
		t.Fatalf("stored records = %d, want the two real deltas", n)
	}
	if u, d, _, _ := f.checkpoint(t, sess); u != 1450 || d != 1650 {
		t.Fatalf("the checkpoint is at %d/%d", u, d)
	}

	if c, err := ing(1500, 1700, t1.Add(time.Minute)); err != nil || c != "ACCEPTED" {
		t.Fatalf("a later observation was refused: %s %v", c, err)
	}
}

// A sample dated into a frozen watermark window is recorded as DELAYED evidence and must not rewrite the frozen totals.
func TestIntegration_Acct_DelayedDeliveryCannotRewriteAFrozenWindow(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ent, sess, _ := f.live(t, "10.9.0.1", "br-guest")

	ing := func(up, down int64, at time.Time) (string, error) {
		var class string
		err := f.pool.QueryRow(ctx, `
			SELECT iam_v2.ingest_absolute_counters($1,$2,$3::uuid,$4::uuid,'br-guest',4097,1,$5,$6,$7)`,
			f.tenant, f.site, sess, f.device, up, down, at).Scan(&class)
		return class, err
	}

	t0 := time.Now().Add(-40 * time.Minute)
	if _, err := ing(0, 0, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := ing(500, 500, t0.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}

	boundary := t0.Add(10 * time.Minute)
	var wm string
	wtx := opTx(t, f.pool, "checkout_conversion")
	defer func() { _ = wtx.Rollback(ctx) }()
	if err := wtx.QueryRow(ctx, `
		INSERT INTO iam_v2.entitlement_boundary_watermarks
		  (tenant_id,site_id,entitlement_id,boundary_at,bytes_up,bytes_down,records_counted)
		SELECT $1,$2,$3,$4,u.bytes_up,u.bytes_down,u.records
		  FROM iam_v2.entitlement_usage_bytes($3,$4) u RETURNING id::text`,
		f.tenant, f.site, ent, boundary).Scan(&wm); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if err := wtx.Commit(ctx); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	var frozenUp, frozenDown int64
	if err := f.pool.QueryRow(ctx,
		`SELECT bytes_up, bytes_down FROM iam_v2.entitlement_boundary_watermarks WHERE id=$1`, wm).
		Scan(&frozenUp, &frozenDown); err != nil {
		t.Fatal(err)
	}

	if c, err := ing(700, 800, t0.Add(8*time.Minute)); err != nil {
		t.Fatalf("the delayed observation was refused outright: %v", err)
	} else if c != "DELAYED" {
		t.Fatalf("a sample inside a frozen window was classified %q, want DELAYED", c)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.delayed_accounting_records WHERE watermark_id=$1`, wm); n != 1 {
		t.Fatalf("delayed-evidence rows = %d, want 1", n)
	}
	var nowUp, nowDown int64
	if err := f.pool.QueryRow(ctx,
		`SELECT bytes_up, bytes_down FROM iam_v2.entitlement_boundary_watermarks WHERE id=$1`, wm).
		Scan(&nowUp, &nowDown); err != nil {
		t.Fatal(err)
	}
	if nowUp != frozenUp || nowDown != frozenDown {
		t.Fatalf("the frozen decision was rewritten from %d/%d to %d/%d", frozenUp, frozenDown, nowUp, nowDown)
	}
}

// Without a registered origin, the first observation only baselines and traffic before the first pass is lost
// silently. The TC owner registers what it read at creation, then the guest uses the network, then the first pass runs.
func TestIntegration_Acct_TrafficBeforeTheFirstPassIsBilledExactlyOnce(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ent, sess, ep := f.live(t, "10.9.0.1", "br-guest")

	created := time.Now().Add(-2 * time.Minute)
	var outcome string
	if err := f.pool.QueryRow(ctx, `
		SELECT iam_v2.register_class_origin($1,$2,$3::uuid,$4::uuid,'br-guest',4097,1,0,0,$5)`,
		f.tenant, f.site, sess, f.device, created).Scan(&outcome); err != nil {
		t.Fatalf("register origin: %v", err)
	}
	if outcome != "ORIGIN_REGISTERED" {
		t.Fatalf("origin outcome = %q", outcome)
	}

	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 400, 900)

	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 1 {
		t.Fatalf("the first pass accounted %d observations; the pre-tick traffic must be billed", n)
	}
	if u, d := f.usage(t, ent); u != 400 || d != 900 {
		t.Fatalf("usage = %d/%d, want the whole 400/900 used before the first pass", u, d)
	}

	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("a second pass re-billed %d observations", n)
	}
	if u, d := f.usage(t, ent); u != 400 || d != 900 {
		t.Fatalf("usage drifted to %d/%d", u, d)
	}
}

// Re-registering the same generation must not move the origin, or every TC-owner restart forgives usage.
func TestIntegration_Acct_ReRegisteringOneGenerationDoesNotForgiveUsage(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ent, sess, ep := f.live(t, "10.9.0.1", "br-guest")

	reg := func(epoch, up, down int64, at time.Time) string {
		var out string
		if err := f.pool.QueryRow(ctx, `
			SELECT iam_v2.register_class_origin($1,$2,$3::uuid,$4::uuid,'br-guest',4097,$5,$6,$7,$8)`,
			f.tenant, f.site, sess, f.device, epoch, up, down, at).Scan(&out); err != nil {
			t.Fatalf("register(%d): %v", epoch, err)
		}
		return out
	}

	t0 := time.Now().Add(-5 * time.Minute)
	if out := reg(1, 0, 0, t0); out != "ORIGIN_REGISTERED" {
		t.Fatalf("first registration = %q", out)
	}
	if out := reg(1, 300, 300, t0.Add(time.Minute)); out != "ORIGIN_UNCHANGED" {
		t.Fatalf("re-registering one generation returned %q; it must not move the origin", out)
	}
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 300, 300)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 1 {
		t.Fatalf("accounted %d observations", n)
	}
	if u, d := f.usage(t, ent); u != 300 || d != 300 {
		t.Fatalf("usage = %d/%d; re-registration forgave real traffic", u, d)
	}

	if out := reg(2, 0, 0, time.Now()); out != "ORIGIN_RESET" {
		t.Fatalf("a new generation returned %q", out)
	}
	if u, d, epoch, _ := f.checkpoint(t, sess); u != 0 || d != 0 || epoch != 2 {
		t.Fatalf("after a reset the checkpoint is %d/%d at epoch %d", u, d, epoch)
	}
}

// An origin that does not describe the Session is refused like an observation: it is a checkpoint write.
func TestIntegration_Acct_ClassOriginIsSourceBound(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sess, _ := f.live(t, "10.9.0.1", "br-guest")

	for _, tc := range []struct {
		name           string
		device, bridge string
		minor          int
	}{
		{"another device", f.device2, "br-guest", 4097},
		{"a bridge the session is not on", f.device, "br-elsewhere", 4097},
		{"a class the address does not map to", f.device, "br-guest", 4098},
	} {
		_, err := f.pool.Exec(ctx, `
			SELECT iam_v2.register_class_origin($1,$2,$3::uuid,$4::uuid,$5,$6,1,0,0,now())`,
			f.tenant, f.site, sess, tc.device, tc.bridge, tc.minor)
		if err == nil {
			t.Fatalf("an origin for %s was accepted", tc.name)
		}
		if !strings.Contains(err.Error(), "ACCT_SOURCE_MISMATCH") {
			t.Fatalf("%s failed for the wrong reason: %v", tc.name, err)
		}
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_checkpoints WHERE session_id=$1`, sess); n != 0 {
		t.Fatalf("a refused origin created %d checkpoints", n)
	}
}
