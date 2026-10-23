//go:build integration && phase5

package checkout

// F9-i: a Cross-PMS Transfer racing the real Checkout/Grace conversion on the same Stay; one transaction
// wins, with no split state. Asserted against: two live entitlements on one Stay, a session on a terminated
// entitlement, an open device interval on a terminated entitlement, and a lineage row whose source ended
// as CHECKOUT instead of TRANSFERRED. Lives in this package to drive the real ConvertAtCheckout.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/transfer"
)

// destination adds a second PMS interface with an IN_HOUSE Stay: the property a guest is transferred to.
func destination(t *testing.T, p *pgxpool.Pool, f fixture) string {
	t.Helper()
	var stay string
	if err := guarded(p, stayFamily, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `WITH
	  ib AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id),
	  sb AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	           external_stay_identity,status,lifecycle_version,last_applied_event_version,normalized_room_number)
	         SELECT gen_random_uuid(),$1,$2,ib.id,'R2','R2','IN_HOUSE',1,0,'201' FROM ib RETURNING id)
	SELECT id::text FROM sb`, f.tenant, f.site).Scan(&stay)
	}); err != nil {
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
	// srcLiveAttachments sums authorized devices, open intervals and live sessions on the source's live
	// entitlement. A grace left by a checkout after a transfer is legitimate but must be an empty shell, or the
	// guest is on two properties.
	srcLiveAttachments int
}

// observe reads everything the two operations could disagree about.
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

// assertCoherent accepts exactly the two legal outcomes and nothing in between.
func assertCoherent(t *testing.T, s raceState, checkoutErr, transferErr error) {
	t.Helper()

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
		// The source must end as TRANSFERRED, not CHECKOUT, or the lineage states something false.
		if s.transfers != 1 {
			t.Fatalf("the transfer reported success but recorded %d lineage row(s)", s.transfers)
		}
		if s.srcTerminalRsn != "TRANSFERRED" {
			t.Fatalf("the transfer won but the source ended as %q", s.srcTerminalRsn)
		}
		if s.liveOnDest != 1 {
			t.Fatalf("the transfer won but the destination has %d live entitlement(s)", s.liveOnDest)
		}
		// A later checkout may legitimately leave a CHECKOUT_GRACE entitlement on the source; only anything
		// attached to it (device, open interval, live session) is split state.
		if s.liveOnSource > 1 {
			t.Fatalf("the transfer won but the source has %d live entitlements", s.liveOnSource)
		}
		if s.srcLiveAttachments != 0 {
			t.Fatalf("SPLIT STATE: the transfer won but %d device/interval/session attachment(s) remain on the source",
				s.srcLiveAttachments)
		}
	case checkoutErr == nil && transferErr != nil:
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
		// Both failing is legal, but no lineage may exist.
		if s.transfers != 0 {
			t.Fatalf("both operations failed yet %d lineage row(s) exist", s.transfers)
		}
	}
}

// TestIntegration_F9i_TransferRacesCheckoutGrace runs the race repeatedly (a breaking interleaving is rarely
// the first), each round on a fresh site.
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
		// A live entitlement with an authorized device and active session: the only state with work to do.
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
		// Both operations lock the Stay first, so they serialize; a deadlock is never acceptable.
		for _, err := range []error{checkoutErr, transferErr} {
			if err != nil && (strings.Contains(err.Error(), "deadlock") ||
				strings.Contains(err.Error(), "40P01")) {
				t.Fatalf("transfer/checkout deadlocked: %v", err)
			}
		}
	}

	t.Logf("F9-i over %d rounds: transfer won %d, checkout won %d, both refused %d",
		rounds, transferWins, checkoutWins, bothFailed)
	if transferWins == 0 && checkoutWins == 0 {
		t.Fatalf("neither operation ever succeeded; the race never ran")
	}
}

// The same race with the transfer started first, to bias the interleaving.
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
			time.Sleep(2 * time.Millisecond)
			_, checkoutErr = NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay,
				src)
		}()
		wg.Wait()

		assertCoherent(t, observe(t, p, f, dest, ent), checkoutErr, transferErr)
	}
}

// The races above are won by the transfer (it takes both Stay locks in one statement before checkout's own
// lock), leaving the checkout-wins branch unreached. This sequences it deterministically: checkout commits,
// then the transfer runs against that state.
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
			// Grace access must not move to another property, or a courtesy window would cross an unauthorized boundary.
			t.Fatalf("a transfer succeeded after checkout committed; grace access must not be transferable")
		}

		s := observe(t, p, f, dest, ent)
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
