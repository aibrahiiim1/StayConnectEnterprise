//go:build integration && phase5

package transfer

import (
	"context"
	"testing"
)

// Usage continuity across a Cross-PMS Transfer. Accounting attributes a sample to the Entitlement the session was BOUND to at the
// sample's instant (iam_v2.session_entitlement_bindings), not to sessions.entitlement_id. A transfer that only re-pointed the
// session row left its open binding on the terminated source, so the destination's own data limit was never charged for what the
// guest then used. The transfer now moves the session through rebind_session_entitlement.
func TestIntegration_F9_TransferMovesTheSessionBindingSoUsageIsChargedToTheDestination(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	fromEnt, session := grant(t, p, f, f.stayA)
	ctx := context.Background()

	var openBefore string
	if err := p.QueryRow(ctx, `SELECT entitlement_id::text FROM iam_v2.session_entitlement_bindings
		WHERE session_id=$1 AND bound_until IS NULL`, session).Scan(&openBefore); err != nil || openBefore != fromEnt {
		t.Fatalf("before the transfer the open binding must be the source (got %q, err %v)", openBefore, err)
	}

	out, err := New(p).Execute(ctx, req(f, f.stayA, f.stayB))
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}

	// Exactly one open binding, on the destination; the source's interval is closed, and at the instant the destination's opens.
	var openCount int
	var openEnt string
	if err := p.QueryRow(ctx, `SELECT count(*), COALESCE(max(entitlement_id::text),'') FROM iam_v2.session_entitlement_bindings
		WHERE session_id=$1 AND bound_until IS NULL`, session).Scan(&openCount, &openEnt); err != nil {
		t.Fatalf("open binding read: %v", err)
	}
	if openCount != 1 || openEnt != out.ToEntitlement {
		t.Fatalf("open bindings = %d on %q, want exactly one on the destination %q", openCount, openEnt, out.ToEntitlement)
	}
	var gap int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.session_entitlement_bindings a
		JOIN iam_v2.session_entitlement_bindings b ON b.session_id=a.session_id AND b.seq=a.seq+1
		WHERE a.session_id=$1 AND a.entitlement_id=$2 AND b.entitlement_id=$3 AND a.bound_until = b.bound_from`,
		session, fromEnt, out.ToEntitlement).Scan(&gap); err != nil || gap != 1 {
		t.Fatalf("the source interval must close at the instant the destination's opens (rows=%d, err=%v)", gap, err)
	}

	// The attribution function itself: a sample taken after the transfer belongs to the destination, one taken before to the source.
	var after, before string
	if err := p.QueryRow(ctx, `SELECT
		(SELECT entitlement_id::text FROM iam_v2.session_entitlement_bindings
		  WHERE session_id=$1 AND bound_from <= now() AND (bound_until IS NULL OR bound_until > now())),
		(SELECT entitlement_id::text FROM iam_v2.session_entitlement_bindings
		  WHERE session_id=$1 AND seq=1)`, session).Scan(&after, &before); err != nil {
		t.Fatalf("attribution read: %v", err)
	}
	if after != out.ToEntitlement || before != fromEnt {
		t.Fatalf("a sample now is charged to %s (want the destination %s), the history keeps %s (want %s)", after, out.ToEntitlement, before, fromEnt)
	}
}

// A session still waiting for enforcement moves with the access; otherwise it would become active on the terminated source.
func TestIntegration_F9_TransferAlsoMovesAPendingSession(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	fromEnt, session := grant(t, p, f, f.stayA)
	ctx := context.Background()
	// grant() opens an active session; a pending one is made by a second session of the same device being impossible, so use the
	// same fixture: end the active one and open a PENDING_ENFORCEMENT one (a controlled-writer state, so through the owner path).
	if _, err := p.Exec(ctx, `SELECT iam_v2.end_session_enforcement($1::uuid,$2::uuid,$3::uuid,'TEST')`, f.tenant, f.site, session); err != nil {
		t.Skipf("cannot end the fixture session in this schema: %v", err)
	}
	var pending string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(tenant_id,site_id,entitlement_id,device_id,state) VALUES ($1,$2,$3,$4,'PENDING_ENFORCEMENT')
		RETURNING id::text`, f.tenant, f.site, fromEnt, f.device).Scan(&pending); err != nil {
		t.Skipf("a pending session cannot be fabricated here (controlled writer): %v", err)
	}
	out, err := New(p).Execute(ctx, req(f, f.stayA, f.stayB))
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if out.SessionsRebound != 1 {
		t.Fatalf("rebound %d session(s), want the pending one", out.SessionsRebound)
	}
	var ent string
	if err := p.QueryRow(ctx, `SELECT entitlement_id::text FROM iam_v2.session_entitlement_bindings
		WHERE session_id=$1 AND bound_until IS NULL`, pending).Scan(&ent); err != nil || ent != out.ToEntitlement {
		t.Fatalf("the pending session's open binding is %q (err %v), want the destination %q", ent, err, out.ToEntitlement)
	}
}
