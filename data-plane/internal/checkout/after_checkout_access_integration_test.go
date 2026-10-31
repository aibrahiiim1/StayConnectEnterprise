//go:build integration

package checkout

// AFTER CHECK-OUT ACCESS (Product Owner, 2026-10-30): the unified contract, end to end on a real PostgreSQL with the real
// checkout converter, the real sign-in (internal/poststay) and the real database rules (migration 0193). Nothing here talks to
// a PMS, a payment provider or any host.
//
// The worked example from the decision is TestAC_WorkedExample: verified checkout 12:00, 8 h, 5 Mbps down / 2 Mbps up, shared
// 1 GB; an iPhone online at checkout and a Samsung offline but previously authorised are eligible; the Samsung reconnects at
// 15:00 with room + reservation; a new iPad is refused with correct credentials; the iPhone uses 600 MB and the Samsung 400 MB,
// the shared 1 GB is used up and access ends for both exactly once; at 20:00 the entitlement expires whatever is left.
// (The test's clock is relative: the checkout is 3 hours ago, so "now" is the 15:00 of the example.)

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/poststay"
	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
)

const acQuota = int64(1_000_000_000) // 1 GB

func acOpts() seedOpts {
	return seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true,
		durSec: 8 * 3600, down: 5000, up: 2000, quota: acQuota, devLim: 5, boundaryAgo: 3 * time.Hour}
}

func acStore(t *testing.T, p *pgxpool.Pool) (*poststay.Store, *authctx.Store) {
	t.Helper()
	thr, err := throttle.New(p, []byte("after-checkout-test-key-0123456789"), time.Minute)
	if err != nil {
		t.Fatalf("throttle: %v", err)
	}
	return poststay.New(p, thr), authctx.NewStore(p)
}

func acGuestNetwork(t *testing.T, p *pgxpool.Pool, f fixture) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `INSERT INTO public.guest_networks(id,tenant_id,site_id,name,parent_interface,bridge_name,
		gateway_cidr,gateway_ip,subnet_cidr)
		SELECT g,$1,$2,'net','p'||substr(md5(g::text),1,12),'b'||substr(md5(g::text),1,12),'10.99.0.1/24','10.99.0.1','10.99.0.0/24'
		  FROM gen_random_uuid() g RETURNING id::text`, f.tenant, f.site).Scan(&id); err != nil {
		t.Fatalf("guest network: %v", err)
	}
	return id
}

func acRoom(t *testing.T, p *pgxpool.Pool, stay, room string) {
	t.Helper()
	if err := guardedExec(p, stayFamily, `UPDATE iam_v2.stays SET normalized_room_number=$2 WHERE id=$1`, stay, room); err != nil {
		t.Fatalf("room: %v", err)
	}
}

func acIP(device string) string {
	h := 0
	for _, c := range device {
		h = (h*31 + int(c)) & 0xffffff
	}
	return fmt.Sprintf("10.%d.%d.%d", 100+(h>>16)%100, (h>>8)&0xff, h&0xff)
}

// acSignIn is the guest's whole path after check-out: room + reservation, one-time context, rejoin.
func acSignIn(t *testing.T, p *pgxpool.Pool, st *poststay.Store, ac *authctx.Store, f fixture, gn, dev, room, res string) (poststay.Converted, error) {
	t.Helper()
	ctx := context.Background()
	profile, err := st.Verify(ctx, poststay.VerifyRequest{Tenant: f.tenant, Site: f.site, Device: dev, IP: acIP(dev), Room: room, Reservation: res})
	if err != nil {
		return poststay.Converted{}, err
	}
	id, err := ac.IssuePostStay(ctx, authctx.PostStayGrant{Tenant: f.tenant, Site: f.site, Profile: profile, Device: dev, GuestNetwork: gn, TTLSeconds: 600})
	if err != nil {
		return poststay.Converted{}, err
	}
	return st.Convert(ctx, poststay.ConvertRequest{Tenant: f.tenant, Site: f.site, Context: id,
		Presenter: authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: dev, GuestNetwork: gn}}, ac)
}

func acGraceEnt(t *testing.T, p *pgxpool.Pool, stay string) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `SELECT id::text FROM iam_v2.entitlements
		WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, stay).Scan(&id); err != nil {
		t.Fatalf("grace entitlement of the stay: %v", err)
	}
	return id
}

func acNewDevice(t *testing.T, p *pgxpool.Pool, f fixture, mac string) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
		VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),$3::macaddr) RETURNING id::text`, f.tenant, f.site, mac).Scan(&id); err != nil {
		t.Fatalf("device: %v", err)
	}
	return id
}

// acSession opens a session of a device on an entitlement the way scd does after a successful rejoin.
func acSession(t *testing.T, p *pgxpool.Pool, f fixture, ent, dev string, started time.Time) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `INSERT INTO iam_v2.sessions(id,tenant_id,site_id,entitlement_id,device_id,state,started)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,'active',$5) RETURNING id::text`, f.tenant, f.site, ent, dev, started).Scan(&id); err != nil {
		t.Fatalf("session: %v", err)
	}
	return id
}

// acSecondStay adds another stay on the SAME site, interface and room number (a room re-let, or a shared room) and its own
// After Check-out entitlement carrying the given devices, exactly as the converter would have written it.
func acSecondStay(t *testing.T, p *pgxpool.Pool, f fixture, iface, reservation, room, status string, devices ...string) (stay, ent string) {
	t.Helper()
	ctx := context.Background()
	co := "NULL"
	if status == "CHECKED_OUT" {
		co = "now() - interval '3 hours'"
	}
	if err := guarded(p, stayFamily, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
			status,lifecycle_version,last_applied_event_version,normalized_room_number,effective_checkout_at)
			VALUES (gen_random_uuid(),$1,$2,$3,$4,$4,$5,1,5,$6,`+co+`) RETURNING id::text`,
			f.tenant, f.site, iface, reservation, status, room).Scan(&stay)
	}); err != nil {
		t.Fatalf("second stay: %v", err)
	}
	if status != "CHECKED_OUT" {
		return stay, ""
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, fam := range []string{"commerce_intent", "device_auth"} {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, fam); err != nil {
			t.Fatal(err)
		}
	}
	var purchase string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state,checkout_episode)
		VALUES ($1,$2,$3,$4,$5,'CHECKOUT_GRACE',0,'GRANTED',1) RETURNING id::text`, f.tenant, f.site, f.gracePkgRev, iface, stay).Scan(&purchase); err != nil {
		t.Fatalf("second stay purchase: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,
		service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,window_ends_at,status,activated_at)
		VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7,'VALIDITY_WINDOW','GRACE_AFTER_CHECKOUT', now() + interval '5 hours','ACTIVE', now() - interval '3 hours')
		RETURNING id::text`, f.tenant, f.site, stay, iface, purchase, f.svcRev, f.gracePkgRev).Scan(&ent); err != nil {
		t.Fatalf("second stay entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now() - interval '3 hours','GRACE_CONVERSION')`, ent); err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices(tenant_id,site_id,entitlement_id,device_id,status,grandfathered,first_authorized,last_authorized)
			VALUES ($1,$2,$3,$4,'AUTHORIZED',true,now() - interval '3 hours',now() - interval '3 hours')`, f.tenant, f.site, ent, d); err != nil {
			t.Fatalf("second stay device: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
			VALUES ($1,$2,$3,$4,1,now() - interval '3 hours')`, f.tenant, f.site, ent, d); err != nil {
			t.Fatalf("second stay authorization: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("second stay commit: %v", err)
	}
	return stay, ent
}

func acOperator(t *testing.T, p *pgxpool.Pool, tenant string) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `INSERT INTO public.operators(tenant_id,email,display_name,password_hash,status)
		VALUES ($1,$2,'AC Test','$argon2id$fixture','active') RETURNING id::text`, tenant, "ac-"+tenant[:8]+"@example.invalid").Scan(&id); err != nil {
		t.Fatalf("operator: %v", err)
	}
	return id
}

func acSwitch(t *testing.T, p *pgxpool.Pool, f fixture, enabled bool, expected int, actor string) (int, error) {
	t.Helper()
	var v int
	err := p.QueryRow(context.Background(), `SELECT iam_v2.set_after_checkout_enabled($1,$2,$3,$4,$5::uuid,'TEST_REASON')`,
		f.tenant, f.site, enabled, expected, actor).Scan(&v)
	return v, err
}

// ---------------------------------------------------------------------------------------------------------------------------
// THE WORKED EXAMPLE
// ---------------------------------------------------------------------------------------------------------------------------

func TestAC_WorkedExample(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, acOpts())
	b := f.boundary // "12:00"; now is "15:00"
	acRoom(t, p, f.stay, "101")
	gn := acGuestNetwork(t, p, f)
	st, ac := acStore(t, p)

	orig := activeEnt(t, p, f)
	// iPhone: authorised on the stay, online at checkout. Samsung: authorised on the stay, offline since long before checkout.
	_, iphoneSess := seedDeviceAuth(t, p, f, orig, 1, b.Add(-48*time.Hour), nil, b.Add(-2*time.Hour), nil)
	samsung, _ := seedDeviceAuth(t, p, f, orig, 2, b.Add(-47*time.Hour), nil, b.Add(-30*time.Hour), ptr(b.Add(-20*time.Hour)))

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated || res.IsEmergency {
		t.Fatalf("checkout: %+v err=%v", res, err)
	}
	if res.DevicesGrandfathered != 2 || res.SessionsRebound != 1 {
		t.Fatalf("the iPhone (online) and the Samsung (offline, previously authorised) must both be carried over, the iPhone's session without a logout: %+v", res)
	}
	ent := res.NewEntitlementID

	// ONE entitlement, the package snapshot, the window running from the verified checkout, not from any reconnection.
	var windowEnds time.Time
	var status, mode string
	var snap []byte
	if err := p.QueryRow(ctx, `SELECT window_ends_at, status, end_mode, policy_snapshot FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&windowEnds, &status, &mode, &snap); err != nil {
		t.Fatal(err)
	}
	if !windowEnds.Equal(b.Add(8*time.Hour)) || status != "ACTIVE" || mode != "GRACE_AFTER_CHECKOUT" {
		t.Fatalf("entitlement window=%s (want checkout+8h=%s) status=%s mode=%s", windowEnds, b.Add(8*time.Hour), status, mode)
	}
	for _, want := range []string{`"DurationSeconds": 28800`, `"DownKbps": 5000`, `"UpKbps": 2000`, `"DataQuotaBytes": 1000000000`} {
		if !strings.Contains(string(snap), want) {
			t.Fatalf("policy snapshot %s lacks %s", snap, want)
		}
	}
	var sessEnt string
	if err := p.QueryRow(ctx, `SELECT entitlement_id::text FROM iam_v2.sessions WHERE id=$1 AND state='active'`, iphoneSess).Scan(&sessEnt); err != nil || sessEnt != ent {
		t.Fatalf("the iPhone's live session did not move to the After Check-out entitlement without a logout: %v %v", sessEnt, err)
	}

	// The Samsung reconnects at 15:00 with room + reservation only.
	got, err := acSignIn(t, p, st, ac, f, gn, samsung, "101", "R1")
	if err != nil || got.Entitlement != ent || !got.WindowEnds.Equal(windowEnds) {
		t.Fatalf("the Samsung (previously authorised, offline at checkout) was not let back onto the same entitlement: %+v err=%v", got, err)
	}
	samsungSess := acSession(t, p, f, ent, samsung, time.Now().Add(-30*time.Minute))

	// A new iPad with the CORRECT room and reservation is refused, however often it tries; so is a MAC the stay never saw.
	ipad := acNewDevice(t, p, f, "02:00:00:00:aa:99")
	for i := 0; i < 3; i++ {
		if _, err := acSignIn(t, p, st, ac, f, gn, ipad, "101", "R1"); !errors.Is(err, poststay.ErrNotAuthenticable) {
			t.Fatalf("attempt %d: a new iPad with correct credentials got %v, want the generic refusal", i, err)
		}
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$1`, ent); n != 2 {
		t.Fatalf("the entitlement has %d devices, want 2 (iPhone, Samsung)", n)
	}
	// Not even by the database: no route can add the iPad to the entitlement.
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, ipad); err == nil || !strings.Contains(err.Error(), "AFTER_CHECKOUT_NEW_DEVICE_REJECTED") {
		t.Fatalf("authorize_entitlement_device admitted the iPad: %v", err)
	}

	// Reconnecting, a new session, signing in again and again change nothing: one entitlement, one window, one allowance.
	for i := 0; i < 3; i++ {
		again, err := acSignIn(t, p, st, ac, f, gn, samsung, "101", "R1")
		if err != nil || again.Entitlement != ent || !again.WindowEnds.Equal(windowEnds) {
			t.Fatalf("sign-in %d: %+v err=%v", i, again, err)
		}
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, f.stay); n != 1 {
		t.Fatalf("%d live entitlements for the stay, want exactly 1", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1 AND trigger IN ('CHECKOUT_GRACE','EMERGENCY_GRACE','POST_STAY_CONVERSION') AND trigger<>'ADMIN_GRANT'`, f.stay); n != 1 {
		t.Fatalf("%d After Check-out purchases for the stay, want exactly 1", n)
	}

	// iPhone 600 MB + Samsung 400 MB = the shared 1 GB.
	sample(t, p, f, iphoneSess, 1, 100_000_000, 500_000_000, time.Now().Add(-90*time.Minute))
	if _, err := p.Exec(ctx, `SELECT iam_v2.p6_advance_data_crossing($1,$2)`, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	var crossed *time.Time
	if err := p.QueryRow(ctx, `SELECT iam_v2.p6_data_crossing($1)`, ent).Scan(&crossed); err != nil || crossed != nil {
		t.Fatalf("600 MB of 1 GB crossed the quota: %v %v", crossed, err)
	}
	sample(t, p, f, samsungSess, 1, 50_000_000, 350_000_000, time.Now().Add(-time.Minute))
	if _, err := p.Exec(ctx, `SELECT iam_v2.p6_advance_data_crossing($1,$2)`, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `SELECT iam_v2.p6_data_crossing($1)`, ent).Scan(&crossed); err != nil || crossed == nil {
		t.Fatalf("600 MB + 400 MB across two devices did not use up the SHARED 1 GB: %v %v", crossed, err)
	}
	// Access ends for both, exactly once.
	var reason string
	var devices, sessions int
	var at time.Time
	if err := p.QueryRow(ctx, `SELECT reason, at, devices, sessions FROM iam_v2.p6_expire_entitlement($1)`, ent).Scan(&reason, &at, &devices, &sessions); err != nil {
		t.Fatalf("termination: %v", err)
	}
	if devices != 2 || sessions != 2 {
		t.Fatalf("termination ended %d devices and %d sessions, want 2 and 2 (%s)", devices, sessions, reason)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.p6_expire_entitlement($1)`, ent); n != 0 {
		t.Fatalf("a second termination reported %d rows, want none (exactly once)", n)
	}
	// ...and nobody can come back for a second helping.
	if _, err := acSignIn(t, p, st, ac, f, gn, samsung, "101", "R1"); !errors.Is(err, poststay.ErrNotAuthenticable) {
		t.Fatalf("after the shared quota ended access, the Samsung got %v", err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, f.stay); n != 0 {
		t.Fatalf("%d live entitlements after exhaustion, want 0: nothing may replace it", n)
	}
}

// At the end of the window the entitlement expires whatever is left, and nothing follows it.
func TestAC_ExpiresAtTheWindowEndRegardlessOfRemainingBytes(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	// A 1-hour window that ends a few seconds from now (the window cannot be moved once granted, so the test waits for it).
	o := acOpts()
	o.durSec, o.boundaryAgo = 3600, time.Hour-10*time.Second
	f := seedBase(t, p, o)
	b := f.boundary
	acRoom(t, p, f.stay, "101")
	gn := acGuestNetwork(t, p, f)
	st, ac := acStore(t, p)
	orig := activeEnt(t, p, f)
	dev, _ := seedDeviceAuth(t, p, f, orig, 1, b.Add(-5*time.Hour), nil, b.Add(-2*time.Hour), nil)
	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("checkout: %+v %v", res, err)
	}
	ent := res.NewEntitlementID
	// A context issued while the window was open; the window then ends ("20:00").
	profile, err := st.Verify(ctx, poststay.VerifyRequest{Tenant: f.tenant, Site: f.site, Device: dev, IP: acIP(dev), Room: "101", Reservation: "R1"})
	if err != nil {
		t.Fatalf("verify inside the window: %v", err)
	}
	cid, err := ac.IssuePostStay(ctx, authctx.PostStayGrant{Tenant: f.tenant, Site: f.site, Profile: profile, Device: dev, GuestNetwork: gn, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	end := b.Add(time.Hour)
	if time.Until(end) <= 0 {
		t.Skip("the machine was too slow to issue the context inside a 10-second window")
	}
	time.Sleep(time.Until(end) + 300*time.Millisecond)
	_, err = st.Convert(ctx, poststay.ConvertRequest{Tenant: f.tenant, Site: f.site, Context: cid,
		Presenter: authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: dev, GuestNetwork: gn}}, ac)
	if !errors.Is(err, poststay.ErrEntitlementNotJoinable) && !errors.Is(err, authctx.ErrContextInvalid) { // the profile ends with the window too, so the one-time context is already invalid
		t.Fatalf("a rejoin after the window ended got %v, want a refusal (not joinable / context invalid)", err)
	}
	if _, err := acSignIn(t, p, st, ac, f, gn, dev, "101", "R1"); !errors.Is(err, poststay.ErrNotAuthenticable) {
		t.Fatalf("a sign-in after the window ended got %v, want the generic refusal", err)
	}
	var reason string
	if err := p.QueryRow(ctx, `SELECT reason FROM iam_v2.p6_expire_entitlement($1)`, ent).Scan(&reason); err != nil {
		t.Fatalf("the expired entitlement was not terminated: %v", err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.p6_expire_entitlement($1)`, ent); n != 0 {
		t.Fatalf("expiry ran twice: %d", n)
	}
}

// ---------------------------------------------------------------------------------------------------------------------------
// ELIGIBILITY: previously authorised devices ONLY
// ---------------------------------------------------------------------------------------------------------------------------

func TestAC_OnlyPreviouslyAuthorisedDevicesAreEligible(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, acOpts())
	b := f.boundary
	acRoom(t, p, f.stay, "101")
	gn := acGuestNetwork(t, p, f)
	st, ac := acStore(t, p)
	orig := activeEnt(t, p, f)

	online, onlineSess := seedDeviceAuth(t, p, f, orig, 1, b.Add(-10*time.Hour), nil, b.Add(-time.Hour), nil)
	offline, _ := seedDeviceAuth(t, p, f, orig, 2, b.Add(-10*time.Hour), nil, b.Add(-9*time.Hour), ptr(b.Add(-8*time.Hour)))
	revokedAt := b.Add(-time.Hour)
	revoked, _ := seedDeviceAuth(t, p, f, orig, 3, b.Add(-10*time.Hour), &revokedAt, b.Add(-10*time.Hour), &revokedAt)
	lateJoiner, _ := seedDeviceAuth(t, p, f, orig, 4, b.Add(time.Minute), nil, b.Add(time.Minute), nil) // authorised only AFTER the boundary
	rotated := acNewDevice(t, p, f, "02:00:00:00:c0:de")                                                // a rotated MAC: no lineage at all
	lanOnly := acNewDevice(t, p, f, "02:00:00:00:c0:df")                                                // on the Wi-Fi, no stay authorisation

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("checkout: %+v %v", res, err)
	}
	ent := res.NewEntitlementID
	if res.DevicesGrandfathered != 2 {
		t.Fatalf("carried %d devices, want exactly the online and the previously-authorised offline one", res.DevicesGrandfathered)
	}
	var sessEnt string
	if err := p.QueryRow(ctx, `SELECT entitlement_id::text FROM iam_v2.sessions WHERE id=$1`, onlineSess).Scan(&sessEnt); err != nil || sessEnt != ent {
		t.Fatalf("the online device's session was not transferred: %v %v", sessEnt, err)
	}
	for name, dev := range map[string]string{"online": online, "offline previously authorised": offline} {
		if _, err := acSignIn(t, p, st, ac, f, gn, dev, "101", "R1"); err != nil {
			t.Errorf("%s: eligible device refused: %v", name, err)
		}
	}
	for name, dev := range map[string]string{
		"revoked before checkout":        revoked,
		"authorised only after checkout": lateJoiner,
		"rotated MAC":                    rotated,
		"on the LAN without a stay":      lanOnly,
	} {
		if _, err := acSignIn(t, p, st, ac, f, gn, dev, "101", "R1"); !errors.Is(err, poststay.ErrNotAuthenticable) {
			t.Errorf("%s: got %v, want the generic refusal even with the correct room and reservation", name, err)
		}
	}
	// Removal during the window ends eligibility for good: a device staff released cannot rejoin.
	if err := guarded(p, []string{"device_auth"}, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `SELECT iam_v2.deauthorize_entitlement_device($1,$2,now(),'STAFF_RELEASED')`, ent, offline)
		return e
	}); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := acSignIn(t, p, st, ac, f, gn, offline, "101", "R1"); !errors.Is(err, poststay.ErrNotAuthenticable) {
		t.Fatalf("a device staff removed got %v, want the generic refusal", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, offline); err == nil || !strings.Contains(err.Error(), "AFTER_CHECKOUT_NEW_DEVICE_REJECTED") {
		t.Fatalf("a removed device re-entered the entitlement through the database: %v", err)
	}
}

// ---------------------------------------------------------------------------------------------------------------------------
// ROOM REUSE, OVERLAPPING ROOM NUMBERS, SHARED OCCUPANCY
// ---------------------------------------------------------------------------------------------------------------------------

func TestAC_RoomReuseAndSharedRoomNumbers(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, acOpts())
	b := f.boundary
	acRoom(t, p, f.stay, "101")
	gn := acGuestNetwork(t, p, f)
	st, ac := acStore(t, p)
	orig := activeEnt(t, p, f)
	prevDev, _ := seedDeviceAuth(t, p, f, orig, 1, b.Add(-5*time.Hour), nil, b.Add(-time.Hour), nil)
	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("checkout: %+v %v", res, err)
	}

	// The room is re-let: the next occupant (reservation R2) is in house in the same room number on the same interface, with
	// a device of their own that was authorised on THEIR stay only.
	_, _ = acSecondStay(t, p, f, f.iface, "R2", "101", "IN_HOUSE")
	nextDev := acNewDevice(t, p, f, "02:00:00:00:d0:01")
	// the previous guest's device types the new occupant's reservation: nothing
	if _, err := acSignIn(t, p, st, ac, f, gn, prevDev, "101", "R2"); !errors.Is(err, poststay.ErrNotAuthenticable) {
		t.Fatalf("the previous stay's device reached the next occupant's stay: %v", err)
	}
	// the next occupant's device types the previous guest's room + reservation: nothing (no lineage on that stay)
	if _, err := acSignIn(t, p, st, ac, f, gn, nextDev, "101", "R1"); !errors.Is(err, poststay.ErrNotAuthenticable) {
		t.Fatalf("the next occupant's device reached the previous stay's After Check-out access: %v", err)
	}
	// the previous guest's own device still works for ITS stay
	if got, err := acSignIn(t, p, st, ac, f, gn, prevDev, "101", "R1"); err != nil || got.Entitlement != res.NewEntitlementID {
		t.Fatalf("the previous guest's device lost its own access: %+v %v", got, err)
	}

	// The same room number and the same reservation text on ANOTHER PMS interface (room numbers are interface-scoped): a
	// second stay there, with its own device and its own entitlement. Each device resolves only to its own stay.
	var iface2 string
	if err := guarded(p, stayFamily, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
			VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id::text`, f.tenant, f.site).Scan(&iface2); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime(tenant_id,site_id,pms_interface_id,published_resync_generation,resync_generation_seq)
			VALUES ($1,$2,$3,0,0)`, f.tenant, f.site, iface2)
		return err
	}); err != nil {
		t.Fatalf("second interface: %v", err)
	}
	otherDev := acNewDevice(t, p, f, "02:00:00:00:d0:02")
	otherStay, otherEnt := acSecondStay(t, p, f, iface2, "R1", "101", "CHECKED_OUT", otherDev)
	got, err := acSignIn(t, p, st, ac, f, gn, otherDev, "101", "R1")
	if err != nil || got.Stay != otherStay || got.Entitlement != otherEnt {
		t.Fatalf("the other interface's device resolved to %+v (%v), want its own stay %s", got, err, otherStay)
	}
	got, err = acSignIn(t, p, st, ac, f, gn, prevDev, "101", "R1")
	if err != nil || got.Stay != f.stay || got.Entitlement != res.NewEntitlementID {
		t.Fatalf("the first interface's device resolved to %+v (%v), want ITS stay %s", got, err, f.stay)
	}

	// Shared occupancy: two guests of ONE stay, each with their own device, rejoin the SAME entitlement.
	shareStay, shareEnt := acSecondStay(t, p, f, f.iface, "R3", "202", "CHECKED_OUT")
	_ = shareStay
	d1 := acNewDevice(t, p, f, "02:00:00:00:d0:03")
	d2 := acNewDevice(t, p, f, "02:00:00:00:d0:04")
	for _, d := range []string{d1, d2} {
		if err := guarded(p, []string{"commerce_intent", "device_auth"}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices(tenant_id,site_id,entitlement_id,device_id,status,grandfathered,first_authorized,last_authorized)
				VALUES ($1,$2,$3,$4,'AUTHORIZED',true,now(),now())`, f.tenant, f.site, shareEnt, d); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
				VALUES ($1,$2,$3,$4,1,now())`, f.tenant, f.site, shareEnt, d)
			return e
		}); err != nil {
			t.Fatalf("share device: %v", err)
		}
		if got, err := acSignIn(t, p, st, ac, f, gn, d, "202", "R3"); err != nil || got.Entitlement != shareEnt {
			t.Fatalf("a guest of a shared stay did not rejoin the shared entitlement: %+v %v", got, err)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------------------
// CONCURRENCY, IDEMPOTENCE, LATE PROCESSING
// ---------------------------------------------------------------------------------------------------------------------------

func TestAC_ConcurrentReconnectionsGrantNothingTwice(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, acOpts())
	b := f.boundary
	acRoom(t, p, f.stay, "101")
	gn := acGuestNetwork(t, p, f)
	st, ac := acStore(t, p)
	orig := activeEnt(t, p, f)
	d1, _ := seedDeviceAuth(t, p, f, orig, 1, b.Add(-5*time.Hour), nil, b.Add(-time.Hour), nil)
	d2, _ := seedDeviceAuth(t, p, f, orig, 2, b.Add(-5*time.Hour), nil, b.Add(-4*time.Hour), ptr(b.Add(-3*time.Hour)))
	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("checkout: %+v %v", res, err)
	}
	ent := res.NewEntitlementID
	var before time.Time
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&before); err != nil {
		t.Fatal(err)
	}
	purchases := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1`, f.stay)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	// 4 attempts per device: the per-stay-and-device throttle (5 a minute) is part of what this feature keeps.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dev := d1
			if i%2 == 1 {
				dev = d2
			}
			got, err := acSignIn(t, p, st, ac, f, gn, dev, "101", "R1")
			if err != nil {
				errs <- fmt.Errorf("worker %d: %w", i, err)
				return
			}
			if got.Entitlement != ent {
				errs <- fmt.Errorf("worker %d joined %s, want %s", i, got.Entitlement, ent)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	var after time.Time
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&after); err != nil || !after.Equal(before) {
		t.Fatalf("concurrent reconnections moved the window: %s -> %s (%v)", before, after, err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1`, f.stay); n != purchases {
		t.Fatalf("purchases %d -> %d: a reconnection created a benefit", purchases, n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); n != 1 {
		t.Fatalf("%d After Check-out entitlements, want 1", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$1`, ent); n != 2 {
		t.Fatalf("%d devices on the entitlement, want 2", n)
	}
}

// A checkout processed twice, and concurrently, grants once.
func TestAC_RepeatedAndConcurrentCheckoutsGrantOnce(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, acOpts())
	b := f.boundary
	orig := activeEnt(t, p, f)
	seedDeviceAuth(t, p, f, orig, 1, b.Add(-5*time.Hour), nil, b.Add(-time.Hour), nil)
	src := checkoutEvent(t, p, f)
	var wg sync.WaitGroup
	created := make(chan bool, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, src)
			if err != nil {
				t.Errorf("convert: %v", err)
				return
			}
			created <- r.GraceCreated
		}()
	}
	wg.Wait()
	close(created)
	n := 0
	for c := range created {
		if c {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d of 6 concurrent identical checkouts created a grant, want exactly 1", n)
	}
	if c := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); c != 1 {
		t.Fatalf("%d After Check-out entitlements, want 1", c)
	}
}

// PMS outage: the checkout is processed hours late. The verified checkout time stays the boundary and the window runs from it.
func TestAC_LateCheckoutKeepsTheEffectiveTimestamp(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()

	// Late, window still open: checkout 6 h ago, 8 h window => 2 h left (NOT 8 h from now).
	o := acOpts()
	o.boundaryAgo = 6 * time.Hour
	f := seedBase(t, p, o)
	orig := activeEnt(t, p, f)
	dev, _ := seedDeviceAuth(t, p, f, orig, 1, f.boundary.Add(-5*time.Hour), nil, f.boundary.Add(-time.Hour), nil)
	acRoom(t, p, f.stay, "101")
	gn := acGuestNetwork(t, p, f)
	st, ac := acStore(t, p)
	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("late checkout: %+v %v", res, err)
	}
	var eff, ends time.Time
	if err := p.QueryRow(ctx, `SELECT s.effective_checkout_at, e.window_ends_at FROM iam_v2.stays s, iam_v2.entitlements e WHERE s.id=$1 AND e.id=$2`,
		f.stay, res.NewEntitlementID).Scan(&eff, &ends); err != nil {
		t.Fatal(err)
	}
	if !eff.Equal(f.boundary) || !ends.Equal(f.boundary.Add(8*time.Hour)) {
		t.Fatalf("effective checkout %s / window end %s, want %s / %s: the processing time must not move either", eff, ends, f.boundary, f.boundary.Add(8*time.Hour))
	}
	if _, err := acSignIn(t, p, st, ac, f, gn, dev, "101", "R1"); err != nil {
		t.Fatalf("the device could not rejoin in the remaining 2 hours: %v", err)
	}

	// Late beyond the window: checkout 10 h ago, 8 h window => already over when processed. The boundary is still the real
	// checkout time; the entitlement cannot be joined, and it ends exactly once.
	o2 := acOpts()
	o2.boundaryAgo = 10 * time.Hour
	g := seedBase(t, p, o2)
	origG := activeEnt(t, p, g)
	devG, _ := seedDeviceAuth(t, p, g, origG, 1, g.boundary.Add(-5*time.Hour), nil, g.boundary.Add(-time.Hour), nil)
	acRoom(t, p, g.stay, "101")
	gnG := acGuestNetwork(t, p, g)
	resG, err := NewConverter(p).ConvertAtCheckout(ctx, g.tenant, g.site, g.iface, g.stay, checkoutEvent(t, p, g))
	if err != nil || !resG.GraceCreated {
		t.Fatalf("very late checkout: %+v %v", resG, err)
	}
	if _, err := acSignIn(t, p, st, ac, g, gnG, devG, "101", "R1"); !errors.Is(err, poststay.ErrNotAuthenticable) {
		t.Fatalf("a rejoin to an entitlement whose window ended before the checkout was processed got %v", err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.p6_expire_entitlement($1)`, resG.NewEntitlementID); n != 1 {
		t.Fatalf("the over entitlement was ended %d times, want once", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.p6_expire_entitlement($1)`, resG.NewEntitlementID); n != 0 {
		t.Fatal("the over entitlement was ended twice")
	}
}

// ---------------------------------------------------------------------------------------------------------------------------
// ONE POLICY, ONE SWITCH, ONE BENEFIT
// ---------------------------------------------------------------------------------------------------------------------------

func TestAC_SwitchAppliesToLaterCheckoutsOnlyAndIsAudited(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, acOpts())
	b := f.boundary
	op := acOperator(t, p, f.tenant)

	// stay 1 checks out while the feature is on.
	orig := activeEnt(t, p, f)
	seedDeviceAuth(t, p, f, orig, 1, b.Add(-5*time.Hour), nil, b.Add(-time.Hour), nil)
	res1, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res1.GraceCreated {
		t.Fatalf("checkout 1: %+v %v", res1, err)
	}
	var winBefore time.Time
	var snapBefore string
	if err := p.QueryRow(ctx, `SELECT window_ends_at, policy_snapshot::text FROM iam_v2.entitlements WHERE id=$1`, res1.NewEntitlementID).Scan(&winBefore, &snapBefore); err != nil {
		t.Fatal(err)
	}

	// Authorisation rules of the switch itself: a stale version, a stranger and a free-text reason are refused.
	if _, err := acSwitch(t, p, f, false, 99, op); err == nil || !strings.Contains(err.Error(), "GRACE_VERSION_CONFLICT") {
		t.Fatalf("a stale expected version was accepted: %v", err)
	}
	if _, err := acSwitch(t, p, f, false, 1, "00000000-0000-0000-0000-000000000001"); err == nil || !strings.Contains(err.Error(), "GRACE_ACTOR_INVALID") {
		t.Fatalf("a non-operator switched the feature: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.site_checkout_grace_config SET after_checkout_enabled=false WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site); err == nil {
		t.Fatal("the switch was changed by a plain UPDATE")
	}
	v, err := acSwitch(t, p, f, false, 1, op)
	if err != nil || v != 2 {
		t.Fatalf("switch off: version %d err %v, want version 2", v, err)
	}
	if v2, err := acSwitch(t, p, f, false, 2, op); err != nil || v2 != 2 {
		t.Fatalf("switching off again must be a no-op without a new version: %d %v", v2, err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.checkout_grace_policy_publications WHERE tenant_id=$1 AND site_id=$2 AND policy_snapshot->>'change'='AFTER_CHECKOUT_ENABLED'`, f.tenant, f.site); n != 1 {
		t.Fatalf("%d history rows for the switch, want exactly 1", n)
	}

	// The first stay's grant is untouched by the change: same window, same snapshot, still ACTIVE.
	var winAfter time.Time
	var snapAfter, st1 string
	if err := p.QueryRow(ctx, `SELECT window_ends_at, policy_snapshot::text, status FROM iam_v2.entitlements WHERE id=$1`, res1.NewEntitlementID).Scan(&winAfter, &snapAfter, &st1); err != nil {
		t.Fatal(err)
	}
	if !winAfter.Equal(winBefore) || snapAfter != snapBefore || st1 != "ACTIVE" {
		t.Fatalf("switching the feature off rewrote a grant already issued: %s/%s %s", winAfter, st1, snapAfter)
	}

	// A LATER checkout while it is off grants nothing and ends what the stay held; the fallback does not apply to "off".
	f2 := f
	stay2, _ := acSecondStay(t, p, f, f.iface, "R7", "303", "IN_HOUSE")
	f2.stay = stay2
	ent2 := seedEntFor(t, p, f2)
	dev2, sess2 := seedDeviceAuth(t, p, f2, ent2, 5, b.Add(-5*time.Hour), nil, b.Add(-time.Hour), nil)
	eid := seedEvent(t, p, f2, &f.boundary, false, 9, "LIVE", 0, stay2)
	applyEvent(t, p, eid, stay2)
	res2, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, stay2, BoundarySource{StayEventID: eid})
	if err != nil {
		t.Fatalf("checkout 2: %v", err)
	}
	if res2.GraceCreated || res2.IsEmergency || res2.Reason != "AFTER_CHECKOUT_DISABLED" {
		t.Fatalf("a checkout while the feature is off: %+v", res2)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, stay2); n != 0 {
		t.Fatalf("%d entitlements still live on a stay that checked out with the feature off", n)
	}
	var sst string
	if err := p.QueryRow(ctx, `SELECT state FROM iam_v2.sessions WHERE id=$1`, sess2).Scan(&sst); err != nil || sst != "ended" {
		t.Fatalf("the session of a stay with no After Check-out access did not end at the boundary: %v %v", sst, err)
	}
	_ = dev2

	// Switching it back on applies to the next checkout again.
	if v3, err := acSwitch(t, p, f, true, 2, op); err != nil || v3 != 3 {
		t.Fatalf("switch on: %d %v", v3, err)
	}
	f3 := f
	stay3, _ := acSecondStay(t, p, f, f.iface, "R8", "304", "IN_HOUSE")
	f3.stay = stay3
	ent3 := seedEntFor(t, p, f3)
	seedDeviceAuth(t, p, f3, ent3, 6, b.Add(-5*time.Hour), nil, b.Add(-time.Hour), nil)
	eid3 := seedEvent(t, p, f3, &f.boundary, false, 10, "LIVE", 0, stay3)
	applyEvent(t, p, eid3, stay3)
	res3, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, stay3, BoundarySource{StayEventID: eid3})
	if err != nil || !res3.GraceCreated || res3.IsEmergency {
		t.Fatalf("checkout 3 after switching back on: %+v %v", res3, err)
	}
}

// seedEntFor is activeEnt for a stay other than the fixture's own.
func seedEntFor(t *testing.T, p *pgxpool.Pool, f fixture) string {
	t.Helper()
	return activeEnt(t, p, f)
}

// The separate Post-Stay benefit is gone: no purchase can carry that trigger any more, whoever writes it.
func TestAC_ThePostStayBenefitCannotBeCreatedAnyMore(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seedBase(t, p, acOpts())
	err := guardedExec(p, commerceFamily, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,$5,'POST_STAY_CONVERSION',0,'GRANTED')`, f.tenant, f.site, f.gracePkgRev, f.iface, f.stay)
	if err == nil || !strings.Contains(err.Error(), "POST_STAY_CONVERSION_RETIRED") {
		t.Fatalf("a POST_STAY_CONVERSION purchase was accepted: %v", err)
	}
}

// A stay with no eligible active entitlement at the checkout boundary gets nothing, and a later reconnection cannot create it.
func TestAC_NoEligibleEntitlementMeansNoAccessAndNoRoute(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, acOpts())
	b := f.boundary
	acRoom(t, p, f.stay, "101")
	gn := acGuestNetwork(t, p, f)
	st, ac := acStore(t, p)
	// The stay's only entitlement ended BEFORE the boundary (the guest had used it up): nothing eligible at checkout.
	win := b.Add(-time.Hour)
	ent := seedEnt(t, p, f, &win, []txn{{"ACTIVE", b.Add(-30 * time.Hour)}, {"TERMINATED", b.Add(-time.Hour)}})
	dev, _ := seedDeviceAuth(t, p, f, ent, 1, b.Add(-30*time.Hour), ptr(b.Add(-time.Hour)), b.Add(-30*time.Hour), ptr(b.Add(-time.Hour)))
	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil {
		t.Fatal(err)
	}
	if res.GraceCreated {
		t.Fatalf("a stay with nothing active at the boundary received After Check-out access: %+v", res)
	}
	if _, err := acSignIn(t, p, st, ac, f, gn, dev, "101", "R1"); !errors.Is(err, poststay.ErrNotAuthenticable) {
		t.Fatalf("a device of a stay with no After Check-out access signed in: %v", err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, f.stay); n != 0 {
		t.Fatalf("%d live entitlements, want none", n)
	}
}
