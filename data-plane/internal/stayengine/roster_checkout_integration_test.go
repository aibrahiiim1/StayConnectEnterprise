//go:build integration

package stayengine

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/checkout"
)

// AUDIT H-2: A ROSTER DEPARTURE IS A CHECKOUT.
//
// pms_roster_reconcile used to set a stay CHECKED_OUT directly, so the stay's entitlement stayed ACTIVE, its
// sessions and device authorizations survived, and no grace or audit was produced -- the checkout conversion
// Phase-0 invariant 10 requires never ran. Since 0123 an APPLY queues one checkout (GO) event per absent stay
// and the Stay Engine applies it through the Checkout Converter, exactly as a PMS checkout.

// rosterGeneration publishes resync generation gen whose roster lists exactly the given reservations, with
// sweep coverage for their rooms and a healthy link, and lowers the roster-size floor so a small fixture is
// trusted.
func rosterGeneration(t *testing.T, p *pgxpool.Pool, s scope, gen int64, rooms []string, reservations ...string) {
	t.Helper()
	ctx := context.Background()
	for i, res := range reservations {
		if err := guardedExec(p, stayFamily, `INSERT INTO iam_v2.stay_events
			(tenant_id, site_id, pms_interface_id, external_event_identity, event_type, payload,
			 admission_kind, admission_runtime_generation, resync_generation, received_at)
			VALUES ($1,$2,$3,$4,'GI',$5::jsonb,'RESYNC',1,$6,now())`,
			s.tenant, s.site, s.iface, "SWEEP-"+res, pay(res, rooms[i], "Roster", "Ann", "", "260101", "261231"), gen); err != nil {
			t.Fatalf("roster event %s: %v", res, err)
		}
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.pms_record_resync_coverage($1,$2,$3,$4,$5,$6,0,0)`,
		s.tenant, s.site, s.iface, gen, rooms, len(reservations)); err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.pms_interface_runtime
		SET resync_generation_seq=GREATEST(resync_generation_seq,$4), published_resync_generation=$4, sync_status='IN_SYNC', continuity_status='CONTINUOUS'
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3`, s.tenant, s.site, s.iface, gen); err != nil {
		t.Fatalf("publish generation: %v", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.pms_reconciliation_settings_set($1,$2,1,500,'test','small fixture',2,10)`,
		s.tenant, s.site); err != nil {
		t.Fatalf("settings: %v", err)
	}
}

func reconcile(t *testing.T, p *pgxpool.Pool, s scope, gen int64, apply bool) (outcome string, closed int) {
	t.Helper()
	if err := p.QueryRow(context.Background(), `SELECT outcome, stays_closed
		FROM iam_v2.pms_roster_reconcile($1,$2,$3,$4,'test operator',$5,'test')`,
		s.tenant, s.site, s.iface, gen, apply).Scan(&outcome, &closed); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return outcome, closed
}

func drain(t *testing.T, pr *Processor, s scope) {
	t.Helper()
	for i := 0; i < 50; i++ {
		ok, err := pr.ProcessNext(context.Background(), s.tenant, s.site, s.iface)
		if err != nil {
			t.Fatalf("ProcessNext: %v", err)
		}
		if !ok {
			return
		}
	}
	t.Fatal("inbox never drained")
}

func TestIntegration_RosterDepartureRunsTheCheckoutConversion(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pkgRev := commerce(t, p, s, true)
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))

	// Two guests arrive live; the one in 202 holds internet.
	insertLive(t, p, s, "L-GI-1", "GI", pay("RSTAY", "101", "Stay", "Sam", "", "260101", "261231"))
	insertLive(t, p, s, "L-GI-2", "GI", pay("RGONE", "202", "Gone", "Gil", "", "260101", "261231"))
	drain(t, pr, s)
	var gone string
	if err := p.QueryRow(ctx, `SELECT id::text FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='RGONE'`, s.iface).Scan(&gone); err != nil {
		t.Fatal(err)
	}
	ent := grantEntitlement(t, p, s, gone, pkgRev, time.Now().Add(-3*time.Hour))

	// A complete roster that lists only RSTAY.
	rosterGeneration(t, p, s, 1, []string{"101", "202"}, "RSTAY")

	// A DRY RUN reports the departure and queues nothing (and establishes no inventory -- H-13).
	if out, n := reconcile(t, p, s, 1, false); out != "COMPLETED" || n != 0 {
		t.Fatalf("dry run = %s/%d, want COMPLETED/0", out, n)
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.pms_room_inventory WHERE pms_interface_id=$1`, s.iface); n != 0 {
		t.Fatalf("a dry run established the room inventory (%d rows)", n)
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.stay_events WHERE pms_interface_id=$1 AND external_event_identity LIKE 'ROSTER_RECONCILIATION:%'`, s.iface); n != 0 {
		t.Fatalf("a dry run queued %d checkouts", n)
	}

	// The APPLY queues exactly one checkout and does NOT itself flip the stay.
	if out, n := reconcile(t, p, s, 1, true); out != "COMPLETED" || n != 1 {
		t.Fatalf("apply = %s/%d, want COMPLETED/1", out, n)
	}
	var st string
	_ = p.QueryRow(ctx, `SELECT status FROM iam_v2.stays WHERE id=$1`, gone).Scan(&st)
	if st != "IN_HOUSE" {
		t.Fatalf("the reconcile function closed the stay itself (%s); the checkout path must do it", st)
	}
	// A repeated apply on the same generation queues nothing more.
	if _, n := reconcile(t, p, s, 1, true); n != 0 {
		t.Fatalf("a repeated apply queued %d more checkouts", n)
	}

	// The engine applies it through the converter.
	drain(t, pr, s)
	var effco *time.Time
	var posting bool
	if err := p.QueryRow(ctx, `SELECT status, effective_checkout_at, posting_allowed FROM iam_v2.stays WHERE id=$1`, gone).
		Scan(&st, &effco, &posting); err != nil {
		t.Fatal(err)
	}
	if st != "CHECKED_OUT" || effco == nil || posting {
		t.Fatalf("stay %s effco=%v posting=%v, want CHECKED_OUT with a boundary and no posting", st, effco, posting)
	}
	var entStatus, reason string
	_ = p.QueryRow(ctx, `SELECT status, COALESCE(terminal_reason,'') FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&entStatus, &reason)
	if entStatus != "TERMINATED" {
		t.Fatalf("the roster-closed stay's entitlement is %s, want TERMINATED (H-2)", entStatus)
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode <> 'GRACE_AFTER_CHECKOUT'
		AND status IN ('PENDING','ACTIVE','SUSPENDED')`, gone); n != 0 {
		t.Fatalf("%d pre-checkout entitlements still live after a roster checkout", n)
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.checkout_grace_audit a
		JOIN iam_v2.stay_events e ON e.id = a.boundary_event_id
		WHERE a.stay_id=$1 AND e.external_event_identity LIKE 'ROSTER_RECONCILIATION:%'`, gone); n != 1 {
		t.Fatalf("checkout audit citing the roster checkout = %d, want 1", n)
	}
	// The guest still listed is untouched.
	_ = p.QueryRow(ctx, `SELECT status FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='RSTAY'`, s.iface).Scan(&st)
	if st != "IN_HOUSE" {
		t.Fatalf("the listed guest is %s", st)
	}
}
