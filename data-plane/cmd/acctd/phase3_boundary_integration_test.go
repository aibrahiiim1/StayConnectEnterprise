//go:build integration

package main

// THE ACCOUNTING WRITER BOUNDARY, and the attribution cases that only appear once a Session is REBOUND.
//
// Usage is the one kind of Phase-3 state that cannot be reconciled against a second copy: a plausible row is
// indistinguishable from a real one, and a wrong checkpoint silently changes every future measurement. These
// tests prove that the only way to write it is the controlled operation, and that a sample is attributed to
// whichever Entitlement was in force WHEN IT WAS MEASURED — never to whichever one happens to be current.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// asProbe creates a NON-OWNER role and gives it FULL table DML on the accounting chain. That is deliberately
// the worst realistic case: the point of the boundary is that even a role which has every table privilege
// still cannot write accounting state, because writing it requires BEING the controlled operation. A probe
// without privileges would fail with "permission denied" and prove nothing about the guard.
//
// The role lives only in the disposable test database and is never a service role.
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

// rawDenied runs a statement as the privileged-but-not-owner probe and asserts the controlled-writer boundary
// refused it. The message must name the boundary: an operator debugging a failed write has to know it was
// policy, not a transient error.
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
		// The refusal must come from the boundary itself — either the controlled-writer guard or the
		// append-only/immutability guard in front of it. Anything else (a constraint, a type error, a
		// permission error) would mean this statement was blocked by accident and a differently-shaped one
		// might not be.
		t.Fatalf("%s failed for the wrong reason: %v", what, err)
	}
}

// rawAllowed runs a statement as the same probe and asserts it SUCCEEDS — the boundary has to be precise, not
// a blanket lock that would break the ordinary session writes every other subsystem depends on.
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

// Every table in the measurement chain refuses raw runtime DML. The runtime role can call the operation; it
// cannot write the rows the operation produces.
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

	// INVENTED USAGE: a row with no measurement behind it.
	rawDenied(t, f, "a raw accounting_records INSERT", `
		INSERT INTO iam_v2.accounting_records (tenant_id, site_id, session_id, sample_seq, bytes_up, bytes_down, sampled_at)
		VALUES ($1,$2,$3,99,500,500,now())`, f.tenant, f.site, sess)

	// REWRITTEN HISTORY: changing what a stored measurement says.
	rawDenied(t, f, "a raw accounting_records UPDATE",
		`UPDATE iam_v2.accounting_records SET bytes_up = 0 WHERE session_id=$1`, sess)

	// ERASED HISTORY.
	rawDenied(t, f, "a raw accounting_records DELETE",
		`DELETE FROM iam_v2.accounting_records WHERE session_id=$1`, sess)

	// THE WORST ONE: moving the value every FUTURE delta is computed from. A checkpoint set back to zero would
	// make the next ordinary observation bill the guest for the session's entire history.
	rawDenied(t, f, "a raw checkpoint UPDATE",
		`UPDATE iam_v2.accounting_checkpoints SET prev_bytes_up = 0, prev_bytes_down = 0 WHERE session_id=$1`, sess)
	rawDenied(t, f, "a raw checkpoint DELETE",
		`DELETE FROM iam_v2.accounting_checkpoints WHERE session_id=$1`, sess)
	rawDenied(t, f, "a raw checkpoint INSERT", `
		INSERT INTO iam_v2.accounting_checkpoints
		  (tenant_id, site_id, session_id, source_device_id, bridge, class_minor, source_epoch,
		   prev_bytes_up, prev_bytes_down, prev_sampled_at, last_classification)
		VALUES ($1,$2,$3,$4,'br-evil',1,1,0,0,now(),'BASELINED')`, f.tenant, f.site, sess, f.device)

	// SESSION TOTALS: a total moved without a record behind it.
	rawDenied(t, f, "a raw session usage UPDATE",
		`UPDATE iam_v2.sessions SET bytes_up = bytes_up + 10000 WHERE id=$1`, sess)

	// ...while the ordinary, non-accounting parts of a Session stay writable.
	rawAllowed(t, f, "an ordinary session write",
		`UPDATE iam_v2.sessions SET expires_at = now() + interval '1 hour' WHERE id=$1`, sess)

	// and everything that WAS stored is still exactly what the operation stored
	var up, down int64
	if err := f.pool.QueryRow(ctx,
		`SELECT bytes_up, bytes_down FROM iam_v2.accounting_records WHERE session_id=$1`, sess).Scan(&up, &down); err != nil {
		t.Fatal(err)
	}
	if up != 400 || down != 600 {
		t.Fatalf("the stored measurement was altered: %d/%d", up, down)
	}
}

// The controlled function is the ONLY approved writer, and it is not executable by PUBLIC. Together with the
// table guards this is what makes "zero persistent runtime privileges while DARK" a checkable property rather
// than a deployment convention.
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
	// A SECURITY DEFINER function without a pinned search_path is a privilege-escalation primitive: the caller
	// chooses which schema the unqualified names inside it resolve to.
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

	// PUBLIC EXECUTE is revoked: reachability is granted deliberately, per role, at Gate-P — never inherited.
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

	// The table guards resolve the SAME owner, by unambiguous signature. If they resolved a different role, the
	// operation's own writes would be refused by its own boundary.
	var guardOwner string
	if err := f.pool.QueryRow(ctx, `SELECT iam_v2.p3_controlled_writer_owner('accounting')`).Scan(&guardOwner); err != nil {
		t.Fatalf("the accounting writer family is not resolvable: %v", err)
	}
	if guardOwner != owner {
		t.Fatalf("the guard expects writer %q but the operation runs as %q", guardOwner, owner)
	}

	// The superseded sample-sequence function is GONE, not merely unused: a second, weaker way to write
	// accounting rows is a bypass whether or not anything calls it today.
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

// ATTRIBUTION AT SAMPLE TIME, across a Grace rebinding. A sample measured BEFORE the boundary belongs to the
// pre-boundary Entitlement even if it is ingested after the rebinding — and a sample measured after belongs to
// the new one. Attributing by "the session's current entitlement" would charge a departing guest's browsing to
// the grace allowance, and would make a Folio impossible to reconstruct.
func TestIntegration_Acct_AttributionFollowsTheBindingAtSampleTime(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()

	started := time.Now().Add(-3 * time.Hour)
	first := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, first, f.device, started, "10.9.0.1")
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET ingress_interface='br-guest' WHERE id=$1`, sess); err != nil {
		t.Fatal(err)
	}
	ep := newEpochs()
	ep.set("br-guest", sess, 1)

	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 1000, 1000)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now().Add(-30*time.Minute)) // baseline

	// the guest browses, and that traffic is measured while the FIRST entitlement is bound
	beforeBoundary := time.Now().Add(-20 * time.Minute)
	c.absolutes(t, "br-guest", "10.9.0.1", 1300, 1500)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", beforeBoundary); n != 1 {
		t.Fatal("the pre-boundary observation was not accepted")
	}

	// the stay ends and the session is rebound to a second (grace) entitlement, through the controlled
	// operations — the binding history is append-only and gapless by construction
	boundary := time.Now().Add(-15 * time.Minute)
	if _, err := f.pool.Exec(ctx, `
		SELECT iam_v2.terminate_entitlement_at_boundary($1, $2, 'CHECKOUT')`, first, boundary); err != nil {
		t.Fatalf("terminate at the boundary: %v", err)
	}
	second := f.grantEntitlement(t, boundary, nil)
	if _, err := f.pool.Exec(ctx,
		`SELECT iam_v2.rebind_session_entitlement($1,$2,$3)`, sess, second, boundary); err != nil {
		t.Fatalf("rebind: %v", err)
	}

	upBefore, downBefore := f.usage(t, first)
	if upBefore != 300 || downBefore != 500 {
		t.Fatalf("pre-boundary usage = %d/%d, want 300/500", upBefore, downBefore)
	}

	// traffic measured AFTER the boundary must land on the grace entitlement
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

	// and the pre-boundary record is STILL attributed to the pre-boundary entitlement after the rebinding —
	// the attribution is a property of the interval history, not of a pointer that has since moved
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

// A sample whose measurement time falls in a gap with NO binding is refused outright. The tempting fallback —
// "attribute it to the session's current entitlement" — is exactly how usage from an unbound window silently
// lands on whichever allowance happens to be open.
func TestIntegration_Acct_NoBindingIsRefusedNotAttributedToTheCurrentOne(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 100, 100)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	// close the open interval, leaving the session bound to nothing at all. Closing the latest open interval
	// once is the only mutation the append-only history permits, and it is exactly what a termination does.
	gap := time.Now()
	if _, err := f.pool.Exec(ctx, `
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
	// the checkpoint is untouched, so the traffic is not lost: once a binding exists again, the delta is still
	// measurable from the last known-good value
	if up, down, _, _ := f.checkpoint(t, sess); up != 100 || down != 100 {
		t.Fatalf("a refused sample moved the checkpoint to %d/%d", up, down)
	}
	if p := f.p3.AccountingDegraded(); p == "" {
		t.Fatal("a refused observation was not reported as degraded")
	}
}

// A session that moved to a different BRIDGE gets a different checkpoint. Reusing the old one would compute a
// delta between two unrelated counter series — typically a huge phantom burst, or a regression that stalls the
// session's accounting entirely.
func TestIntegration_Acct_BridgeChangeStartsANewSeries(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 5000, 9000)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	// the guest is moved to another guest bridge; its class there starts from zero
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET ingress_interface='br-g301' WHERE id=$1`, sess); err != nil {
		t.Fatal(err)
	}
	ep.set("br-g301", sess, 1)
	c.absolutes(t, "br-g301", "10.9.0.1", 10, 20)

	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("the first observation on a new bridge billed %d samples; it must baseline", n)
	}
	n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_checkpoints WHERE session_id=$1`, sess)
	if n != 2 {
		t.Fatalf("checkpoints for the session = %d, want one per counter series", n)
	}
	// and nothing was billed for the apparent "drop" from 5000 to 10
	if rows := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); rows != 0 {
		t.Fatalf("a bridge change produced %d accounting rows", rows)
	}
}

// TWO DEVICES SHARING ONE ENTITLEMENT are measured independently and their usage adds up. A checkpoint keyed
// only by session (or only by class minor) would let one device's counter be used as the other's baseline.
func TestIntegration_Acct_TwoDevicesOnOneEntitlement(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-2 * time.Hour)
	ent := f.grantEntitlement(t, started, nil)

	sessA := f.openSession(t, ent, f.device, started, "10.9.0.1")
	sessB := f.openSession(t, ent, f.device2, started, "10.9.0.2")
	for _, s := range []string{sessA, sessB} {
		if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET ingress_interface='br-guest' WHERE id=$1`, s); err != nil {
			t.Fatal(err)
		}
	}
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

// While DARK, the accounting pass issues NO Phase-3 query and writes nothing — and the legacy writer keeps
// ownership. A dark appliance that had started writing iam_v2 rows would be the single worst outcome of this
// whole subsystem: unreviewed state, on a site that never enabled the feature.
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
