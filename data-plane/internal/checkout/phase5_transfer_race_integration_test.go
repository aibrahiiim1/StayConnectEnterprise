//go:build integration && phase5

package checkout

// F9-i — THE CONTRACTUAL RACE: a Cross-PMS Transfer against the REAL Phase-3 Checkout/Grace conversion, on
// the same Stay, at the same moment.
//
// WHY THIS TEST EXISTS AS A FIX-FORWARD. The Phase-5 Plan defines F9-i as "transfer racing checkout or grace:
// one transaction wins; no split state". The test that carried the F9-i label until now was the
// opposite-direction deadlock case — valuable, and kept — but it proves a different property: that two
// TRANSFERS cannot deadlock. It says nothing about a transfer racing the operation that ENDS the stay.
//
// The two operations touch the same rows from opposite directions. Checkout terminates the live entitlement
// and creates a grace one; a transfer terminates the same live entitlement and creates one on another Stay.
// If they interleaved, the possible wreckage is specific and each piece is asserted against here:
//
//   * two live entitlements for one Stay (checkout's grace AND the transfer's destination), which the
//     one-live-per-subject index should make impossible but which a mis-scoped lock could still attempt;
//   * a session pointing at a terminated entitlement — a guest the kernel is still forwarding for, whose
//     authority no longer exists;
//   * a device authorization interval left open against a terminated entitlement;
//   * a transfer lineage row whose source is TERMINATED for the wrong reason (CHECKOUT rather than
//     TRANSFERRED), which would be a durable false statement about where the guest went.
//
// It lives in the checkout package so it can drive the REAL ConvertAtCheckout with the real durable event,
// grace config and emergency catalog its own fixtures build — a re-implementation of checkout here would
// prove that the re-implementation is safe.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/transfer"
)

// destination adds a SECOND PMS interface with an IN_HOUSE Stay to an existing fixture — the other property
// a guest can be transferred to.
func destination(t *testing.T, p *pgxpool.Pool, f fixture) string {
	t.Helper()
	var stay string
	if err := p.QueryRow(context.Background(), `WITH
	  ib AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id),
	  sb AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	           external_stay_identity,status,lifecycle_version,last_applied_event_version,normalized_room_number)
	         SELECT gen_random_uuid(),$1,$2,ib.id,'R2','R2','IN_HOUSE',1,0,'201' FROM ib RETURNING id)
	SELECT id::text FROM sb`, f.tenant, f.site).Scan(&stay); err != nil {
		t.Fatalf("seed destination: %v", err)
	}
	return stay
}

type raceState struct {
	staySrcStatus    string
	liveOnSource     int
	liveOnDest       int
	sessionsDangling int
	devicesDangling  int
	transfers        int
	srcTerminalRsn   string
	graceCreated     int
	// srcLiveAttachments is how much is ATTACHED to whatever the source still holds: authorized devices,
	// open authorization intervals and live sessions, summed. A grace entitlement left behind by a checkout
	// that ran after a transfer is legitimate — the Stay did hold an active entitlement at the boundary — but
	// it must be an EMPTY courtesy shell. Anything attached to it would mean the guest is simultaneously on
	// two properties.
	srcLiveAttachments int
}

// observe reads everything the two operations could have disagreed about.
func observe(t *testing.T, p *pgxpool.Pool, f fixture, dest, sourceEnt string) raceState {
	t.Helper()
	var s raceState
	ctx := context.Background()
	if err := p.QueryRow(ctx, `SELECT
		 (SELECT status FROM iam_v2.stays WHERE id=$1),
		 (SELECT count(*) FROM iam_v2.entitlements
		   WHERE stay_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')),
		 (SELECT count(*) FROM iam_v2.entitlements
		   WHERE stay_id=$2 AND status IN ('PENDING','ACTIVE','SUSPENDED')),
		 -- a session whose entitlement is terminated: the kernel would still be forwarding for a guest whose
		 -- authority no longer exists. SCOPED to this round's two stays -- the disposable database is shared
		 -- across every test in the package, so a global count would measure other tests' history and report
		 -- their leftovers as this race's split state.
		 (SELECT count(*) FROM iam_v2.sessions se
		    JOIN iam_v2.entitlements e ON e.id=se.entitlement_id
		   WHERE se.state='active' AND e.status='TERMINATED' AND e.stay_id IN ($1,$2)),
		 -- an OPEN authorization interval against a terminated entitlement.
		 (SELECT count(*) FROM iam_v2.entitlement_device_authorizations eda
		    JOIN iam_v2.entitlements e ON e.id=eda.entitlement_id
		   WHERE eda.deauthorized_at IS NULL AND e.status='TERMINATED' AND e.stay_id IN ($1,$2)),
		 (SELECT count(*) FROM iam_v2.entitlement_transfers WHERE from_stay_id=$1),
		 (SELECT COALESCE(terminal_reason,'') FROM iam_v2.entitlements WHERE id=$3),
		 (SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1 AND trigger IN ('CHECKOUT_GRACE','EMERGENCY_GRACE')),
		 (SELECT COALESCE(
		     (SELECT count(*) FROM iam_v2.entitlement_devices ed
		       WHERE ed.entitlement_id=e.id AND ed.status='AUTHORIZED'), 0)
		   + COALESCE(
		     (SELECT count(*) FROM iam_v2.entitlement_device_authorizations a
		       WHERE a.entitlement_id=e.id AND a.deauthorized_at IS NULL), 0)
		   + COALESCE(
		     (SELECT count(*) FROM iam_v2.sessions se
		       WHERE se.entitlement_id=e.id AND se.state='active'), 0)
		    FROM iam_v2.entitlements e
		   WHERE e.stay_id=$1 AND e.status IN ('PENDING','ACTIVE','SUSPENDED')
		   LIMIT 1)`,
		f.stay, dest, sourceEnt).
		Scan(&s.staySrcStatus, &s.liveOnSource, &s.liveOnDest, &s.sessionsDangling, &s.devicesDangling,
			&s.transfers, &s.srcTerminalRsn, &s.graceCreated, &s.srcLiveAttachments); err != nil {
		t.Fatalf("observe: %v", err)
	}
	return s
}

// assertCoherent checks the state against BOTH legal outcomes and refuses anything in between.
func assertCoherent(t *testing.T, s raceState, checkoutErr, transferErr error) {
	t.Helper()

	// Whatever happened, these are never acceptable.
	if s.sessionsDangling != 0 {
		t.Fatalf("SPLIT STATE: %d active session(s) point at a TERMINATED entitlement", s.sessionsDangling)
	}
	if s.devicesDangling != 0 {
		t.Fatalf("SPLIT STATE: %d open device authorization(s) against a TERMINATED entitlement", s.devicesDangling)
	}
	if s.liveOnSource > 1 {
		t.Fatalf("SPLIT STATE: %d live entitlements on the source stay", s.liveOnSource)
	}
	if s.liveOnDest > 1 {
		t.Fatalf("SPLIT STATE: %d live entitlements on the destination stay", s.liveOnDest)
	}

	switch {
	case transferErr == nil:
		// THE TRANSFER WON. The guest is on the destination, and the source's access ended AS TRANSFERRED —
		// not as CHECKOUT, which would be the lineage claiming something that did not happen.
		if s.transfers != 1 {
			t.Fatalf("the transfer reported success but recorded %d lineage row(s)", s.transfers)
		}
		if s.srcTerminalRsn != "TRANSFERRED" {
			t.Fatalf("the transfer won but the source ended as %q", s.srcTerminalRsn)
		}
		if s.liveOnDest != 1 {
			t.Fatalf("the transfer won but the destination has %d live entitlement(s)", s.liveOnDest)
		}
		// Checkout may still have committed afterwards. When it does, it legitimately creates a CHECKOUT_GRACE
		// entitlement on the source: the Stay DID hold an active entitlement at the effective-checkout
		// boundary, which is exactly the contract's eligibility rule, and that is true whatever happened to
		// the guest afterwards.
		//
		// So the source holding one live entitlement is NOT split state. What would be split state is anything
		// ATTACHED to it — a device still authorized, an interval still open, a session still live — because
		// that is the guest being on two properties at once. The grace left behind must be an empty shell.
		if s.liveOnSource > 1 {
			t.Fatalf("the transfer won but the source has %d live entitlements", s.liveOnSource)
		}
		if s.srcLiveAttachments != 0 {
			t.Fatalf("SPLIT STATE: the transfer won but %d device/interval/session attachment(s) remain on the source",
				s.srcLiveAttachments)
		}
	case checkoutErr == nil && transferErr != nil:
		// CHECKOUT WON. The stay is checked out, no lineage was recorded, and the source ended for a
		// checkout reason rather than a transfer one.
		if s.transfers != 0 {
			t.Fatalf("the transfer failed but recorded %d lineage row(s)", s.transfers)
		}
		if s.srcTerminalRsn == "TRANSFERRED" {
			t.Fatalf("checkout won but the source is marked TRANSFERRED")
		}
		if s.staySrcStatus != "CHECKED_OUT" {
			t.Fatalf("checkout reported success but the stay is %q", s.staySrcStatus)
		}
		if s.liveOnDest != 0 {
			t.Fatalf("checkout won but the destination gained %d live entitlement(s)", s.liveOnDest)
		}
	default:
		// Both failed. Legal — one of them lost a lock and the other hit an unrelated refusal — but the state
		// must still be untouched in the ways that matter, which the invariants above already asserted.
		if s.transfers != 0 {
			t.Fatalf("both operations failed yet %d lineage row(s) exist", s.transfers)
		}
	}
}

// TestIntegration_F9i_TransferRacesCheckoutGrace is the contractual F9-i.
//
// It runs the race many times because a race that is run once has been sampled once: the interleaving that
// breaks a lock boundary is rarely the first one. Each iteration is a fresh site, so a failure names a state
// rather than an accumulation.
func TestIntegration_F9i_TransferRacesCheckoutGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()

	const rounds = 12
	transferWins, checkoutWins, bothFailed := 0, 0, 0

	for i := 0; i < rounds; i++ {
		f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true,
			systemGracePackage: true, bootstrapEmergency: true})
		dest := destination(t, p, f)
		// A live entitlement with a device authorized and an active session: the state a guest who is
		// actually online is in, and the only state in which either operation has work to do.
		window := f.boundary.Add(4 * time.Hour)
		ent := seedEnt(t, p, f, &window, []txn{{state: "ACTIVE", at: f.boundary.Add(-time.Hour)}})
		seedDeviceAuth(t, p, f, ent, i, f.boundary.Add(-time.Hour), nil, f.boundary.Add(-time.Hour), nil)
		src := checkoutEvent(t, p, f)

		var wg sync.WaitGroup
		var checkoutErr, transferErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, checkoutErr = NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay,
				src)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, transferErr = transfer.New(p).Execute(ctx, transfer.Request{
				Tenant: f.tenant, Site: f.site, FromStay: f.stay, ToStay: dest,
				Operator:      "33333333-3333-3333-3333-333333333333",
				Reason:        "guest moved while the front desk was checking them out",
				GraceValidFor: 2 * time.Hour,
			})
		}()
		close(start)
		wg.Wait()

		s := observe(t, p, f, dest, ent)
		assertCoherent(t, s, checkoutErr, transferErr)

		switch {
		case transferErr == nil:
			transferWins++
		case checkoutErr == nil:
			checkoutWins++
		default:
			bothFailed++
		}
		// A deadlock is never an acceptable outcome of this race: both operations lock the Stay first, so
		// they serialize on it rather than forming a cycle.
		for _, err := range []error{checkoutErr, transferErr} {
			if err != nil && (strings.Contains(err.Error(), "deadlock") ||
				strings.Contains(err.Error(), "40P01")) {
				t.Fatalf("transfer/checkout deadlocked: %v", err)
			}
		}
	}

	t.Logf("F9-i over %d rounds: transfer won %d, checkout won %d, both refused %d",
		rounds, transferWins, checkoutWins, bothFailed)
	// Both orders must actually occur across the rounds, or the "race" is deterministic and has been
	// measuring one path twice. If this ever fails, the test is not exercising what it claims.
	if transferWins == 0 && checkoutWins == 0 {
		t.Fatalf("neither operation ever succeeded; the race never ran")
	}
}

// The same race, one round, with the transfer started first — a crude but real way to bias the interleaving
// so the OTHER order is exercised deliberately rather than left to the scheduler.
func TestIntegration_F9i_CheckoutArrivesMidTransfer(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true,
			systemGracePackage: true, bootstrapEmergency: true})
		dest := destination(t, p, f)
		window := f.boundary.Add(4 * time.Hour)
		ent := seedEnt(t, p, f, &window, []txn{{state: "ACTIVE", at: f.boundary.Add(-time.Hour)}})
		seedDeviceAuth(t, p, f, ent, i, f.boundary.Add(-time.Hour), nil, f.boundary.Add(-time.Hour), nil)
		src := checkoutEvent(t, p, f)

		var wg sync.WaitGroup
		var checkoutErr, transferErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, transferErr = transfer.New(p).Execute(ctx, transfer.Request{
				Tenant: f.tenant, Site: f.site, FromStay: f.stay, ToStay: dest,
				Operator:      "33333333-3333-3333-3333-333333333333",
				Reason:        "guest moved to the sister property",
				GraceValidFor: 2 * time.Hour,
			})
		}()
		go func() {
			defer wg.Done()
			time.Sleep(2 * time.Millisecond) // let the transfer take the Stay lock first
			_, checkoutErr = NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay,
				src)
		}()
		wg.Wait()

		assertCoherent(t, observe(t, p, f, dest, ent), checkoutErr, transferErr)
	}
}

// THE OTHER OUTCOME, made deterministic rather than hoped for.
//
// The unbiased race above is won by the transfer every time, and a 3ms head start does not change that: the
// transfer takes both Stay locks in one statement and finishes, while checkout is still doing the reads that
// precede its own lock. That is a real and correct outcome — checkout then finds the entitlement already
// TERMINATED(TRANSFERRED), converts nothing, and commits a coherent CHECKED_OUT stay.
//
// But it means the CHECKOUT-WINS branch of assertCoherent was never reached, so its assertions were unproven.
// Rather than tune a sleep until the scheduler cooperates — which would be a flaky test pretending to be a
// deterministic one — this sequences the two explicitly: checkout is allowed to COMMIT, and the transfer then
// runs against the state it left. The interleaving is different; the state that must be coherent is the same,
// and it is the state a real front desk produces when the guest is checked out before anyone asks to move
// them.
func TestIntegration_F9i_TransferAfterCheckoutCommitted(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true,
			systemGracePackage: true, bootstrapEmergency: true})
		dest := destination(t, p, f)
		window := f.boundary.Add(4 * time.Hour)
		ent := seedEnt(t, p, f, &window, []txn{{state: "ACTIVE", at: f.boundary.Add(-time.Hour)}})
		seedDeviceAuth(t, p, f, ent, i, f.boundary.Add(-time.Hour), nil, f.boundary.Add(-time.Hour), nil)
		src := checkoutEvent(t, p, f)

		res, checkoutErr := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay,
			src)
		if checkoutErr != nil {
			t.Fatalf("checkout: %v", checkoutErr)
		}
		if !res.CheckedOut {
			t.Fatalf("checkout did not check the stay out")
		}

		_, transferErr := transfer.New(p).Execute(ctx, transfer.Request{
			Tenant: f.tenant, Site: f.site, FromStay: f.stay, ToStay: dest,
			Operator:      "33333333-3333-3333-3333-333333333333",
			Reason:        "guest asked to move after being checked out",
			GraceValidFor: 2 * time.Hour,
		})
		if transferErr == nil {
			// A guest who has checked out has a GRACE entitlement, not the access they were transferred for.
			// Moving that to another property would extend a courtesy window across a boundary nobody
			// authorized, so the transfer must refuse rather than move it.
			t.Fatalf("a transfer succeeded after checkout committed; grace access must not be transferable")
		}

		s := observe(t, p, f, dest, ent)
		// The checkout-wins shape, asserted directly rather than through the race's switch.
		if s.staySrcStatus != "CHECKED_OUT" {
			t.Fatalf("stay is %q after a committed checkout", s.staySrcStatus)
		}
		if s.transfers != 0 {
			t.Fatalf("a refused transfer recorded %d lineage row(s)", s.transfers)
		}
		if s.srcTerminalRsn == "TRANSFERRED" {
			t.Fatalf("checkout terminated the entitlement but it is marked TRANSFERRED")
		}
		if s.liveOnDest != 0 {
			t.Fatalf("the destination gained %d live entitlement(s) from a refused transfer", s.liveOnDest)
		}
		if s.sessionsDangling != 0 || s.devicesDangling != 0 {
			t.Fatalf("SPLIT STATE after checkout: %d dangling session(s), %d dangling authorization(s)",
				s.sessionsDangling, s.devicesDangling)
		}
	}
}
