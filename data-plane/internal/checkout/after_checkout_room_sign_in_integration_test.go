//go:build integration

package checkout

// D93 through room sign-in after a REAL check-out (0209): the converter checks the stay out and creates its After Check-out
// entitlement; a room sign-in context is then issued to the departed stay, and the device joins that same entitlement.
// Before 0209 the context function refused every CHECKED_OUT stay, so no device could reconnect this way at all.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onegate/onegate/data-plane/internal/authctx"
	"github.com/onegate/onegate/data-plane/internal/staygrant"
)

// acRevision publishes a PMS interface revision for the fixture, as a room sign-in context names one.
func acRevision(t *testing.T, p *pgxpool.Pool, f fixture) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
		VALUES (gen_random_uuid(),$1,$2,$3,1,'UTC','{}'::jsonb) RETURNING id::text`, f.tenant, f.site, f.iface).Scan(&id); err != nil {
		t.Fatalf("interface revision: %v", err)
	}
	return id
}

func acRoomContext(p *pgxpool.Pool, f fixture, rev, device, gn string) (string, error) {
	var id string
	err := p.QueryRow(context.Background(), `SELECT context_id::text FROM iam_v2.issue_or_return_pms_context($1,$2,$3,$4,$5,$6,$7,gen_random_uuid(),600)`,
		f.tenant, f.site, f.iface, rev, f.stay, device, gn).Scan(&id)
	return id, err
}

// acRoomJoin is the room grant's join: consume the context and authorize the device on the stay's live entitlement.
func acRoomJoin(p *pgxpool.Pool, f fixture, ctxID, device, gn string) (staygrant.Result, error) {
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		return staygrant.Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := staygrant.New(p).JoinLiveTx(ctx, tx, f.tenant, f.site, staygrant.Request{AuthContextID: ctxID,
		Presenter: authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: device, GuestNetwork: gn}})
	if err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

type acTerms struct {
	window   time.Time
	consumed int64
	quota    *int64
	devices  int
}

func acTermsOf(t *testing.T, p *pgxpool.Pool, ent string) acTerms {
	t.Helper()
	var x acTerms
	if err := p.QueryRow(context.Background(), `SELECT e.window_ends_at, e.consumed_data_bytes, COALESCE(e.data_quota_bytes, spr.data_quota_bytes),
		(SELECT count(*) FROM iam_v2.entitlement_device_authorizations a WHERE a.entitlement_id = e.id AND a.deauthorized_at IS NULL)
		  FROM iam_v2.entitlements e LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id WHERE e.id = $1`,
		ent).Scan(&x.window, &x.consumed, &x.quota, &x.devices); err != nil {
		t.Fatal(err)
	}
	return x
}

func TestAC_RoomSignInAfterARealCheckoutRejoinsTheStaysOwnDevicesOnly(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, acOpts())
	b := f.boundary
	acRoom(t, p, f.stay, "101")
	gn := acGuestNetwork(t, p, f)
	rev := acRevision(t, p, f)

	orig := activeEnt(t, p, f)
	// The phone was online at check-out; the laptop was authorised on the stay but offline since the night before.
	phone, _ := seedDeviceAuth(t, p, f, orig, 1, b.Add(-48*time.Hour), nil, b.Add(-2*time.Hour), nil)
	laptop, _ := seedDeviceAuth(t, p, f, orig, 2, b.Add(-47*time.Hour), nil, b.Add(-30*time.Hour), ptr(b.Add(-20*time.Hour)))

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.CheckedOut || !res.GraceCreated {
		t.Fatalf("checkout: %+v err=%v", res, err)
	}
	ent := res.NewEntitlementID
	var status string
	if err := p.QueryRow(ctx, `SELECT status FROM iam_v2.stays WHERE id=$1`, f.stay).Scan(&status); err != nil || status != "CHECKED_OUT" {
		t.Fatalf("the stay is %q after check-out (%v)", status, err)
	}
	before := acTermsOf(t, p, ent)
	purchases := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1`, f.stay)
	entitlements := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay)

	// THE STAY'S OWN DEVICES reconnect by room sign-in, onto the SAME entitlement, again and again.
	for i, dev := range []string{laptop, phone, laptop} {
		id, err := acRoomContext(p, f, rev, dev, gn)
		if err != nil {
			t.Fatalf("sign-in %d: no room sign-in context for a departed stay with valid After Check-out Access: %v", i, err)
		}
		got, err := acRoomJoin(p, f, id, dev, gn)
		if err != nil || got.EntitlementID != ent || !got.Joined {
			t.Fatalf("sign-in %d: the stay's own device did not rejoin its After Check-out entitlement: %+v err=%v", i, got, err)
		}
	}
	// Nothing was reset or added: one window, one allowance, its use as it was, the same two devices.
	after := acTermsOf(t, p, ent)
	if !after.window.Equal(before.window) || after.consumed != before.consumed || after.devices != before.devices ||
		(after.quota == nil) != (before.quota == nil) || (after.quota != nil && *after.quota != *before.quota) {
		t.Fatalf("the rejoin changed the entitlement's terms: before %+v after %+v", before, after)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1`, f.stay); n != purchases {
		t.Fatalf("purchases %d -> %d: a rejoin must buy nothing", purchases, n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay); n != entitlements {
		t.Fatalf("entitlements %d -> %d: a rejoin must create none", entitlements, n)
	}

	// A NEW DEVICE with the right room details gets a context but is refused at the join, by the database's own rule.
	tablet := acNewDevice(t, p, f, "02:00:00:00:ab:01")
	id, err := acRoomContext(p, f, rev, tablet, gn)
	if err != nil {
		t.Fatalf("context for a new device: %v", err)
	}
	if _, err := acRoomJoin(p, f, id, tablet, gn); err == nil || !strings.Contains(err.Error(), "AFTER_CHECKOUT_NEW_DEVICE_REJECTED") {
		t.Fatalf("a device the stay never used joined After Check-out Access: %v", err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$1 AND device_id=$2`, ent, tablet); n != 0 {
		t.Fatal("the refused device holds a place on the entitlement")
	}

	// A context issued while the access was valid cannot be used once it has ended, and no new one is issued.
	pending, err := acRoomContext(p, f, rev, laptop, gn)
	if err != nil {
		t.Fatal(err)
	}
	if err := guarded(p, []string{"commerce_intent", "device_auth"}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT iam_v2.terminate_entitlement_at_boundary($1, now(), 'ADMIN')`, ent)
		return err
	}); err != nil {
		t.Fatalf("end the After Check-out entitlement: %v", err)
	}
	if _, err := acRoomJoin(p, f, pending, laptop, gn); err == nil {
		t.Fatal("a context issued before the After Check-out Access ended still let the device in")
	}
	if _, err := acRoomContext(p, f, rev, laptop, gn); err == nil || !strings.Contains(err.Error(), "CONTEXT_INVALID") {
		t.Fatalf("a departed stay whose After Check-out Access ended got a context: %v", err)
	}
}
