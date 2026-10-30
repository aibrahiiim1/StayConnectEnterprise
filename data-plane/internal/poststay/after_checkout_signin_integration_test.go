//go:build integration && phase5

package poststay

// After Check-out sign-in (AD-36 as amended by the Product-Owner unification of Checkout Grace and Post-Stay, 2026-10-30).
// Real PostgreSQL, full appliance schema. This file replaces the earlier AD-36 test files of this package; what each of them
// pinned that the unified contract keeps is carried over here with the same attacks, and what they pinned that the contract
// retired (a "new device" route with a surname; a separate Post-Stay benefit; joins limited by the plan's device limit; a
// settings-driven sign-in window) is gone: those behaviours no longer exist, and their opposites are asserted instead
// (TestAC_*, in internal/checkout, runs the whole path through the real checkout converter).
//
// The property under test: nobody reaches a stay that is not theirs, a device without the pre-checkout authorisation lineage is
// never let in whatever it types, every refusal looks the same, and nothing here can create a window, an allowance or a device
// place.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping post-stay integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := p.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return p
}

type fx struct{ tenant, site, iface, rev, stay, device, network, pkg, plan string }

// seed builds a site with a CHECKED_OUT Stay (room "R1" reservation, set by roomAndGuest), one device and a guest network, in its
// own tenant. The device has NO lineage yet: afterCheckoutLineage gives it the place the checkout conversion would have.
func seed(t *testing.T, p *pgxpool.Pool) fx {
	t.Helper()
	ctx := context.Background()
	var f fx
	stx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("seed begin: %v", err)
	}
	defer func() { _ = stx.Rollback(ctx) }()
	if _, err := stx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatalf("seed open: %v", err)
	}
	err = stx.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM gen_random_uuid() g RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, gen_random_uuid() g RETURNING id, tenant_id),
	  gn AS (INSERT INTO public.guest_networks(id,tenant_id,site_id,name,parent_interface,bridge_name,
	           gateway_cidr,gateway_ip,subnet_cidr)
	         SELECT g, si.tenant_id, si.id,'net','p'||substr(md5(g::text),1,12),'b'||substr(md5(g::text),1,12),
	                '10.99.0.1/24','10.99.0.1','10.99.0.0/24' FROM si, gen_random_uuid() g RETURNING id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'protel-fias','ACTIVE' FROM si RETURNING id,tenant_id,site_id),
	  pr AS (INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 1,'UTC','{}'::jsonb FROM pi RETURNING id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	           external_stay_identity,status,lifecycle_version,effective_checkout_at)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,'R1','R1','CHECKED_OUT',1, now() FROM pi RETURNING id),
	  dv AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, gen_random_uuid(),'02:00:00:00:00:01'::macaddr FROM pi RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'AC-PLAN' FROM si RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,
	            max_concurrent_devices,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id,1,5,'VALIDITY_WINDOW',1000000 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,active)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'AC-PKG',true FROM si RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
	            service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(), si.tenant_id, si.id, ip.id,1, spr.id,'CHECKOUT_GRACE',0,
	                 ARRAY['NOT_REQUIRED']::text[], '{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":7200,"policy_version":"CHECKOUT_GRACE_V1"}'::jsonb
	            FROM si, ip, spr RETURNING id, package_id)
	SELECT (SELECT tenant_id FROM pi)::text,(SELECT site_id FROM pi)::text,(SELECT id FROM pi)::text,
	       (SELECT id FROM pr)::text,(SELECT id FROM st)::text,(SELECT id FROM dv)::text,
	       (SELECT id FROM gn)::text,(SELECT id FROM ipr)::text,(SELECT id FROM spr)::text`).
		Scan(&f.tenant, &f.site, &f.iface, &f.rev, &f.stay, &f.device, &f.network, &f.pkg, &f.plan)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := stx.Commit(ctx); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, f.pkg); err != nil {
		t.Fatalf("seed current revision: %v", err)
	}
	return f
}

func store(t *testing.T, p *pgxpool.Pool) *Store {
	t.Helper()
	thr, err := throttle.New(p, []byte("post-stay-test-key-0123456789abcd"), time.Minute)
	if err != nil {
		t.Fatalf("throttle: %v", err)
	}
	return New(p, thr)
}

// storeAtFixedTime pins the throttle clock so a lockout test cannot be split by a window rolling over mid-loop. Each
// caller passes its own instant so no other test reaches its buckets.
func storeAtFixedTime(t *testing.T, p *pgxpool.Pool, at time.Time) *Store {
	t.Helper()
	thr, err := throttle.New(p, []byte("post-stay-test-key-0123456789abcd"), time.Minute)
	if err != nil {
		t.Fatalf("throttle: %v", err)
	}
	thr.SetClock(func() time.Time { return at })
	return New(p, thr)
}

// reLet reinstates the Stay (a new episode) and checks it out again, as the room's next occupant.
func reLet(t *testing.T, p *pgxpool.Pool, f fx) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatalf("open stay: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays
		SET status='IN_HOUSE', effective_checkout_at=NULL, lifecycle_version=lifecycle_version+1
		WHERE id=$1`, f.stay); err != nil {
		t.Fatalf("reinstate: %v", err)
	}
	// posting_allowed=false in the same UPDATE, as internal/checkout does.
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false, effective_checkout_at=now()
		WHERE id=$1`, f.stay); err != nil {
		t.Fatalf("re-checkout: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// afterCheckoutLineage gives the stay's device the place the checkout conversion gives a device that was authorised on the stay
// at the boundary: the stay's live After Check-out entitlement (granted at checkout, ending in two hours) with the device
// carried over. This is the fixture of the lineage, not of a benefit: nothing in this package can create it.
func afterCheckoutLineage(t *testing.T, p *pgxpool.Pool, f fx) string {
	t.Helper()
	return afterCheckoutLineageFor(t, p, f, f.stay, f.device)
}

func afterCheckoutLineageFor(t *testing.T, p *pgxpool.Pool, f fx, stay, device string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, fam := range []string{"commerce_intent", "device_auth"} {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, fam); err != nil {
			t.Fatalf("open %s: %v", fam, err)
		}
	}
	var purchase, ent string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state,checkout_episode)
		VALUES ($1,$2,$3,(SELECT pms_interface_id FROM iam_v2.stays WHERE id=$4),$4,'CHECKOUT_GRACE',0,'GRANTED',1) RETURNING id::text`,
		f.tenant, f.site, f.pkg, stay).Scan(&purchase); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,
		 time_accounting_mode,end_mode,window_ends_at,status,activated_at)
		VALUES ($1,$2,$3,(SELECT pms_interface_id FROM iam_v2.stays WHERE id=$3),$4,'{}'::jsonb,$5,$6,'VALIDITY_WINDOW','GRACE_AFTER_CHECKOUT',
		        now()+interval '2 hours','ACTIVE', now()) RETURNING id::text`, f.tenant, f.site, stay, purchase, f.plan, f.pkg).Scan(&ent); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now(),'GRACE_CONVERSION')`, ent); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
		(tenant_id,site_id,entitlement_id,device_id,status,grandfathered,first_authorized,last_authorized)
		VALUES ($1,$2,$3,$4,'AUTHORIZED',true,now(),now())`, f.tenant, f.site, ent, device); err != nil {
		t.Fatalf("carry the device over: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations
		(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at) VALUES ($1,$2,$3,$4,1,now())`, f.tenant, f.site, ent, device); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return ent
}

// roomAndGuest gives the seeded stay a room and one guest with the given last name (what the PMS mirror holds). The last name is
// never used to sign in; it is on the record so the tests can prove that.
func roomAndGuest(t *testing.T, p *pgxpool.Pool, f fx, room, last string) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET normalized_room_number=$2 WHERE id=$1`, f.stay, room); err != nil {
		t.Fatalf("room: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,first_name_norm,last_name_norm,is_primary)
		VALUES ($1,$2,$3,$4,'ADA',$5,true)`, f.tenant, f.site, f.iface, f.stay, last); err != nil {
		t.Fatalf("guest: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// newDevice adds a device that was never on the stay.
func newDevice(t *testing.T, p *pgxpool.Pool, f fx, mac string) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
		VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),$3::macaddr) RETURNING id::text`, f.tenant, f.site, mac).Scan(&id); err != nil {
		t.Fatalf("device: %v", err)
	}
	return id
}

// req derives the source address from the device: the per-address bucket is real state in the database, so tests sharing one
// address blocked each other across runs.
func req(f fx, device, room, res string) VerifyRequest {
	h := 0
	for _, c := range device {
		h = (h*31 + int(c)) & 0xffffff
	}
	ip := fmt.Sprintf("10.%d.%d.%d", 100+(h>>16)%100, (h>>8)&0xff, h&0xff)
	return VerifyRequest{Tenant: f.tenant, Site: f.site, Device: device, IP: ip, Room: room, Reservation: res}
}

func count(t *testing.T, p *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// signIn is the guest path: verify, mint the one-time context, rejoin.
func signIn(t *testing.T, s *Store, p *pgxpool.Pool, f fx, device string, r VerifyRequest) (Converted, error) {
	t.Helper()
	ctx := context.Background()
	ac := authctx.NewStore(p)
	profile, err := s.Verify(ctx, r)
	if err != nil {
		return Converted{}, err
	}
	id, err := ac.IssuePostStay(ctx, authctx.PostStayGrant{Tenant: f.tenant, Site: f.site, Profile: profile,
		Device: device, GuestNetwork: f.network, TTLSeconds: 600})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return s.Convert(ctx, ConvertRequest{Tenant: f.tenant, Site: f.site, Context: id,
		Presenter: authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: device, GuestNetwork: f.network}}, ac)
}

// The device that was on the stay signs in with the room and the reservation; a profile is made once, with no PIN; and the
// reconnection joins the stay's one entitlement.
func TestIntegration_AC_LineageDeviceSignsInWithRoomAndReservationOnly(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	ent := afterCheckoutLineage(t, p, f)
	s := store(t, p)
	ctx := context.Background()

	// Typed the way a guest types it: lower case room, spaces around.
	profile, err := s.Verify(ctx, req(f, f.device, " 101 ", " R1 "))
	if err != nil {
		t.Fatalf("the right room and reservation: %v", err)
	}
	var via string
	var pinHash *string
	var validUntil, windowEnds time.Time
	if err := p.QueryRow(ctx, `SELECT issued_via, pin_hash, valid_until FROM iam_v2.post_stay_profiles WHERE id=$1`, profile).Scan(&via, &pinHash, &validUntil); err != nil {
		t.Fatal(err)
	}
	if via != "ROOM_RESERVATION" || pinHash != nil {
		t.Fatalf("profile via=%q pin_hash=%v, want ROOM_RESERVATION with no PIN", via, pinHash)
	}
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&windowEnds); err != nil {
		t.Fatal(err)
	}
	if d := validUntil.Sub(windowEnds); d < -time.Second || d > time.Second {
		t.Fatalf("the profile ends at %s, the entitlement at %s: they must end together", validUntil, windowEnds)
	}
	again, err := s.Verify(ctx, req(f, f.device, "101", "R1"))
	if err != nil || again != profile {
		t.Fatalf("second sign-in: profile %q err %v, want the same profile %q", again, err, profile)
	}
	got, err := signIn(t, s, p, f, f.device, req(f, f.device, "101", "R1"))
	if err != nil || got.Entitlement != ent || !got.Joined {
		t.Fatalf("the rejoin: %+v err=%v, want the stay's one entitlement %s", got, err, ent)
	}
	if n := count(t, p, `SELECT count(*) FROM public.audit_log WHERE tenant_id=$1 AND action=$2`, f.tenant, AuditActionEntitlementJoined); n != 1 {
		t.Fatalf("%d join audit rows, want 1", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay); n != 1 {
		t.Fatalf("%d entitlements for the stay, want 1: signing in never creates one", n)
	}
}

// Every refusal is the same refusal, whichever part was wrong. Each case gets its own stay and device, since the lockout
// (correctly) counts repeated failures from one device.
func TestIntegration_AC_EveryRefusalIsTheSameRefusal(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := store(t, p)
	ctx := context.Background()
	for name, c := range map[string][2]string{
		"wrong room":             {"102", "R1"},
		"wrong reservation":      {"101", "R2"},
		"both wrong":             {"999", "ZZ"},
		"empty":                  {"", ""},
		"room only":              {"101", ""},
		"oversized":              {string(make([]byte, 500)), "R1"},
		"right reservation only": {"", "R1"},
	} {
		f := seed(t, p)
		roomAndGuest(t, p, f, "101", "LOVELACE")
		afterCheckoutLineage(t, p, f)
		_, err := s.Verify(ctx, req(f, f.device, c[0], c[1]))
		if !errors.Is(err, ErrNotAuthenticable) || err.Error() != ErrNotAuthenticable.Error() {
			t.Errorf("%s: err = %v, want the single ErrNotAuthenticable", name, err)
		}
	}
}

// Typing another guest's room and reservation from a device that is on a different stay reaches nothing.
func TestIntegration_AC_NoCrossStayGuessing(t *testing.T) {
	p := pool(t)
	defer p.Close()
	mine := seed(t, p)
	roomAndGuest(t, p, mine, "101", "LOVELACE")
	afterCheckoutLineage(t, p, mine)
	theirs := seed(t, p)
	roomAndGuest(t, p, theirs, "202", "BABBAGE")
	afterCheckoutLineage(t, p, theirs)
	s := store(t, p)
	ctx := context.Background()
	// My device types THEIR room and reservation (R1 is the seeded reservation in both fixtures; the room differs).
	if _, err := s.Verify(ctx, req(mine, mine.device, "202", "R1")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("a lineage device reached another stay by its room: %v", err)
	}
}

// A device with no lineage on the stay is refused with the correct room and reservation, and no profile or context comes out
// of it. The surname is not a field of the request any more, so it cannot be offered as a second factor either.
func TestIntegration_AC_ADeviceWithoutLineageIsNeverLetIn(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	afterCheckoutLineage(t, p, f)
	fresh := newDevice(t, p, f, "02:00:00:00:00:77")
	s := store(t, p)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Verify(ctx, req(f, fresh, "101", "R1")); !errors.Is(err, ErrNotAuthenticable) {
			t.Fatalf("attempt %d: a device with no lineage and the right room + reservation got %v", i, err)
		}
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.post_stay_profiles WHERE origin_stay_id=$1`, f.stay); n != 0 {
		t.Fatalf("a refused device caused %d profiles to be created", n)
	}
	// The same device is refused at the join too, even if a context somehow existed for the stay: it holds no place on the
	// entitlement (the join checks the lineage itself and the database refuses to add the device).
	ac := authctx.NewStore(p)
	own, err := s.Verify(ctx, req(f, f.device, "101", "R1"))
	if err != nil {
		t.Fatal(err)
	}
	cid, err := ac.IssuePostStay(ctx, authctx.PostStayGrant{Tenant: f.tenant, Site: f.site, Profile: own, Device: fresh, GuestNetwork: f.network, TTLSeconds: 600})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := s.Convert(ctx, ConvertRequest{Tenant: f.tenant, Site: f.site, Context: cid,
		Presenter: authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: fresh, GuestNetwork: f.network}}, ac); !errors.Is(err, ErrNoLineage) {
		t.Fatalf("the join admitted a device without lineage: %v", err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE device_id=$1`, fresh); n != 0 {
		t.Fatalf("the refused device holds %d places", n)
	}
}

// A device removed between the check and the rejoin is refused at the rejoin: eligibility is decided where access is given.
func TestIntegration_AC_ARemovedDeviceIsRefusedAtTheJoin(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	ent := afterCheckoutLineage(t, p, f)
	s := store(t, p)
	ctx := context.Background()
	ac := authctx.NewStore(p)
	profile, err := s.Verify(ctx, req(f, f.device, "101", "R1"))
	if err != nil {
		t.Fatal(err)
	}
	cid, err := ac.IssuePostStay(ctx, authctx.PostStayGrant{Tenant: f.tenant, Site: f.site, Profile: profile, Device: f.device, GuestNetwork: f.network, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('device_auth')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.deauthorize_entitlement_device($1,$2,now(),'STAFF_RELEASED')`, ent, f.device); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Convert(ctx, ConvertRequest{Tenant: f.tenant, Site: f.site, Context: cid,
		Presenter: authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: f.device, GuestNetwork: f.network}}, ac); !errors.Is(err, ErrNoLineage) {
		t.Fatalf("a removed device rejoined: %v", err)
	}
	if _, err := s.Verify(ctx, req(f, f.device, "101", "R1")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("a removed device passed the check again: %v", err)
	}
}

// A stay checked out before the unification whose earlier post-stay entitlement is still running is honoured until it ends:
// the device that holds a place on it rejoins; nothing extends it and nothing new is granted.
func TestIntegration_AC_AnEntitlementIssuedByTheRetiredPostStayConversionIsHonouredNotExtended(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	ent := afterCheckoutLineage(t, p, f)
	ctx := context.Background()
	// Re-label it as the retired conversion issued it (an UPDATE: new purchases of that kind are refused by the database).
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT iam_v2.begin_controlled_operation('commerce_intent')`,
		`UPDATE iam_v2.purchases SET trigger='POST_STAY_CONVERSION' WHERE id=(SELECT purchase_id FROM iam_v2.entitlements WHERE id='` + ent + `')`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET end_mode='VALIDITY_WINDOW' WHERE id=$1`, ent); err != nil {
		t.Fatalf("relabel: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	s := store(t, p)
	var before time.Time
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&before); err != nil {
		t.Fatal(err)
	}
	got, err := signIn(t, s, p, f, f.device, req(f, f.device, "101", "R1"))
	if err != nil || got.Entitlement != ent || !got.WindowEnds.Equal(before) {
		t.Fatalf("an entitlement already issued did not keep working: %+v err=%v", got, err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1`, f.stay); n != 1 {
		t.Fatalf("%d purchases for the stay, want the one already there", n)
	}
	// ...and a new one cannot be written.
	tx2, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback(ctx) }()
	if _, err := tx2.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx2.Exec(ctx, `INSERT INTO iam_v2.purchases(tenant_id,site_id,package_revision_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,'POST_STAY_CONVERSION',0,'GRANTED')`, f.tenant, f.site, f.pkg, f.stay); err == nil || !strings.Contains(err.Error(), "POST_STAY_CONVERSION_RETIRED") {
		t.Fatalf("a new post-stay conversion was accepted: %v", err)
	}
}

// The lockout is per stay AND device: the guesser is locked out, the guest's own devices are not.
func TestIntegration_AC_LockoutIsPerStayAndDevice(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	afterCheckoutLineage(t, p, f)
	other := newDevice(t, p, f, "02:00:00:00:00:78")
	s := storeAtFixedTime(t, p, time.Now().Add(-90*time.Minute))
	ctx := context.Background()
	// The legitimate device mistypes five times, then is refused even with the right answer.
	for i := 0; i < 5; i++ {
		if _, err := s.Verify(ctx, req(f, f.device, "101", "WRONG")); !errors.Is(err, ErrNotAuthenticable) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := s.Verify(ctx, req(f, f.device, "101", "R1")); !errors.Is(err, ErrThrottled) {
		t.Fatalf("after five wrong answers the device must be locked out, got %v", err)
	}
	// Someone else's device guessing at this stay does not lock the guest's own device out, and is itself locked out.
	for i := 0; i < 5; i++ {
		_, _ = s.Verify(ctx, req(f, other, "101", "GUESS"))
	}
	if _, err := s.Verify(ctx, req(f, other, "101", "R1")); !errors.Is(err, ErrThrottled) {
		t.Fatalf("the guessing device should itself be locked out, got %v", err)
	}
}

// A revoked profile never authenticates again, and a reinstatement orphans the old profile.
func TestIntegration_AC_RevokedAndOrphanedProfilesRefuse(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	afterCheckoutLineage(t, p, f)
	s := store(t, p)
	ctx := context.Background()
	profile, err := s.Verify(ctx, req(f, f.device, "101", "R1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, RevokeRequest{Tenant: f.tenant, Site: f.site, Profile: profile, Operator: "11111111-1111-1111-1111-111111111111", Reason: "test"}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.Verify(ctx, req(f, f.device, "101", "R1")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("a revoked profile signed in: %v", err)
	}
	g := seed(t, p)
	roomAndGuest(t, p, g, "303", "HOPPER")
	afterCheckoutLineage(t, p, g)
	prof2, err := s.Verify(ctx, req(g, g.device, "303", "R1"))
	if err != nil {
		t.Fatal(err)
	}
	reLet(t, p, g)
	var ok bool
	if err := p.QueryRow(ctx, `SELECT iam_v2.p5_post_stay_authenticable($1,$2,$3)`, g.tenant, g.site, prof2).Scan(&ok); err != nil || ok {
		t.Fatalf("the old episode's profile still authenticates after a reinstatement: %v %v", ok, err)
	}
}

func TestSameTextIsConstantTimeAndNeverMatchesNothing(t *testing.T) {
	if !sameText("101", "101") || sameText("101", "102") || sameText("", "") || sameText("101", "") || sameText("", "101") {
		t.Fatal("sameText semantics changed")
	}
	_ = throttle.ScopeDevice
}

// Every refusal is written to the audit log with a closed reason code and keyed pseudonyms, and never with the surname,
// the room or the reservation number; and the same shape whether or not the stay exists (no enumeration through the log).
func TestIntegration_AC_RefusalsArePersistedWithoutGuestData(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "B7K", "ZEPPELIN")
	fresh := newDevice(t, p, f, "02:00:00:00:e5:01")
	other := newDevice(t, p, f, "02:00:00:00:e5:02")
	s := storeAtFixedTime(t, p, time.Now().Add(-650*time.Minute))
	ctx := context.Background()

	// An existing room with a wrong reservation, and a room that does not exist: both from devices with no lineage.
	if _, err := s.Verify(ctx, req(f, fresh, "B7K", "WRONG-RES-ZQ9")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Verify(ctx, req(f, other, "NOSUCHROOM", "WRONG-RES-ZQ9")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("got %v", err)
	}
	rows, err := p.Query(ctx, `SELECT payload::text, actor_type, actor_id, ip::text, target_type FROM public.audit_log
		WHERE tenant_id=$1 AND action=$2 ORDER BY ts`, f.tenant, AuditActionRefused)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var shapes []string
	for rows.Next() {
		var payload, actorType, targetType string
		var actorID, ip *string
		if err := rows.Scan(&payload, &actorType, &actorID, &ip, &targetType); err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"ZEPPELIN", "WRONG-RES-ZQ9", "B7K", "NOSUCHROOM", "R1"} {
			if strings.Contains(payload, banned) || (actorID != nil && strings.Contains(*actorID, banned)) {
				t.Fatalf("audit row holds guest data %q: %s", banned, payload)
			}
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatal(err)
		}
		if m["code"] != RefusalNotAuthenticable || m["route"] != RouteNewDevice || m["candidate_stays"] != float64(0) {
			t.Fatalf("payload %v", m)
		}
		if _, has := m["stay_id"]; has {
			t.Fatalf("a refusal row must not name a stay: %v", m)
		}
		if actorType != "guest" || targetType != "post_stay_signin" || ip == nil || actorID == nil || len(*actorID) != 16 {
			t.Fatalf("row shape actor=%s target=%s ip=%v actor_id=%v", actorType, targetType, ip, actorID)
		}
		shapes = append(shapes, fmt.Sprint(len(m)))
	}
	if len(shapes) != 2 || shapes[0] != shapes[1] {
		t.Fatalf("want two refusal rows of the same shape, got %v", shapes)
	}
}

// A throttled attempt is audited too (with the scope that stopped it), and a flood cannot fill the table: rows are capped
// per device and minute and the rest are counted in one suppression row.
func TestIntegration_AC_RefusalAuditIsBounded(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	fresh := newDevice(t, p, f, "02:00:00:00:e6:01")
	at := time.Now().Add(-660 * time.Minute).Truncate(time.Minute)
	s := storeAtFixedTime(t, p, at)
	s.SetClock(func() time.Time { return at })
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		r := req(f, fresh, "101", fmt.Sprintf("X%d", i))
		r.IP = "10.99.9.9" // its own address: a block on the shared test address would outlive this test's clock
		_, _ = s.Verify(ctx, r)
	}
	if n := count(t, p, `SELECT count(*) FROM public.audit_log WHERE tenant_id=$1 AND action=$2`, f.tenant, AuditActionRefused); n > auditPerKeyPerMinute*2 {
		t.Fatalf("%d refusal rows from one device in one minute, want at most %d per reason", n, auditPerKeyPerMinute)
	}
	if n := count(t, p, `SELECT count(*) FROM public.audit_log WHERE tenant_id=$1 AND action=$2 AND payload->>'code'=$3`,
		f.tenant, AuditActionRefused, RefusalThrottled); n == 0 {
		t.Fatal("throttled attempts were not audited")
	}
	// The next minute: the dropped ones are summarised once.
	s.SetClock(func() time.Time { return at.Add(2 * time.Minute) })
	next := req(f, fresh, "101", "X-next")
	next.IP = "10.99.9.9"
	_, _ = s.Verify(ctx, next)
	if n := count(t, p, `SELECT count(*) FROM public.audit_log WHERE tenant_id=$1 AND action=$2 AND (payload->>'suppressed')::int > 0`,
		f.tenant, AuditActionRefusalsDropped); n != 1 {
		t.Fatalf("%d suppression rows, want 1", n)
	}
}

// A client that changes its device identity (MAC) on every attempt is bounded by the room and the reservation it must
// guess, not by the identity: the guesses against one room stop at the cap, whatever identities they come from; the guest's
// own device is never affected; and the lockout ends by itself.
func TestIntegration_AC_RotatingDeviceIdentityDoesNotMultiplyGuesses(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	afterCheckoutLineage(t, p, f) // the guest's own, known device (lineage)
	at := time.Now().Add(-700 * time.Minute)
	s := storeAtFixedTime(t, p, at)
	ctx := context.Background()

	evaluated, throttled := 0, 0
	for d := 0; d < 25; d++ {
		dev := newDevice(t, p, f, fmt.Sprintf("02:00:00:00:f0:%02x", d))
		r := VerifyRequest{Tenant: f.tenant, Site: f.site, Device: dev, IP: fmt.Sprintf("10.77.%d.1", d), // a fresh address too
			Room: "101", Reservation: fmt.Sprintf("GUESS-%d", d)}
		_, err := s.Verify(ctx, r)
		switch {
		case errors.Is(err, ErrThrottled):
			throttled++
		case errors.Is(err, ErrNotAuthenticable):
			evaluated++
		default:
			t.Fatalf("unexpected %v", err)
		}
	}
	if evaluated != BoundNewDeviceRoom.def {
		t.Fatalf("evaluated=%d throttled=%d: 25 device identities got %d guesses at one room, want the cap of %d",
			evaluated, throttled, evaluated, BoundNewDeviceRoom.def)
	}
	// Even a device with NO lineage gets nothing from the right answer during the lockout (a block cannot be an oracle)...
	late := newDevice(t, p, f, "02:00:00:00:f1:01")
	if _, err := s.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Device: late, IP: "10.78.0.1", Room: "101", Reservation: "R1"}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("a new identity during the lockout: %v, want ErrThrottled", err)
	}
	// ...while the guest's own device, already on the stay, is unaffected.
	if _, err := s.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Device: f.device, IP: "10.78.0.2", Room: "101", Reservation: "R1"}); err != nil {
		t.Fatalf("the guest's known device during the flood: %v", err)
	}
	// The lockout is finite: after its length (default 15 minutes) the throttle lets the identity be judged again -- and a
	// device with no lineage is still refused, because it has none (not because it is throttled).
	later := storeAtFixedTime(t, p, at.Add(time.Duration(BoundLockoutMinutes.def+2)*time.Minute))
	if _, err := later.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Device: late, IP: "10.78.0.1", Room: "101", Reservation: "R1"}); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("after the lockout ended a device with no lineage got %v, want the plain refusal", err)
	}
}

// The same across rooms: one reservation number tried against many rooms from many identities is capped by the
// reservation, so spraying a guessed number over the hotel does not work either.
func TestIntegration_AC_SprayingOneReservationAcrossRoomsIsCapped(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	s := storeAtFixedTime(t, p, time.Now().Add(-710*time.Minute))
	ctx := context.Background()
	evaluated := 0
	for d := 0; d < 25; d++ {
		dev := newDevice(t, p, f, fmt.Sprintf("02:00:00:00:f2:%02x", d))
		_, err := s.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Device: dev, IP: fmt.Sprintf("10.79.%d.1", d),
			Room: fmt.Sprintf("%d", 200+d), Reservation: "SPRAYED"})
		if errors.Is(err, ErrNotAuthenticable) {
			evaluated++
		}
	}
	if evaluated != BoundNewDeviceRoom.def {
		t.Fatalf("evaluated=%d, want the cap of %d for one reservation number", evaluated, BoundNewDeviceRoom.def)
	}
}

// The limits are the site's settings: a lower wrong-answer count and a shorter lockout apply at once, and the lockout
// ends after the configured time.
func TestIntegration_AC_LimitsComeFromTheSettings(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	afterCheckoutLineage(t, p, f)
	at := time.Now().Add(-720 * time.Minute)
	s := storeAtFixedTime(t, p, at)
	ctx := context.Background()
	lim := Limits{WrongAnswersBeforeLockout: 3, LockoutMinutes: 2}
	r := req(f, f.device, "101", "WRONG")
	r.IP = "10.99.9.10"
	r.Limits = lim
	for i := 0; i < 3; i++ {
		if _, err := s.Verify(ctx, r); !errors.Is(err, ErrNotAuthenticable) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	r.Reservation = "R1"
	if _, err := s.Verify(ctx, r); !errors.Is(err, ErrThrottled) {
		t.Fatalf("after 3 wrong answers (setting 3): %v, want ErrThrottled", err)
	}
	// Two minutes later (setting) the device may try again; with the defaults it would still be held.
	later := storeAtFixedTime(t, p, at.Add(3*time.Minute))
	if _, err := later.Verify(ctx, r); err != nil {
		t.Fatalf("after the configured 2-minute lockout: %v", err)
	}
}

// The settings are bounded, defaulted and audited in the database: the stored bounds are the code's bounds, an
// out-of-range value is refused, and a change leaves an append-only row with operator and reason.
func TestIntegration_AC_ThrottleSettingsAreBoundedAndAudited(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	ctx := context.Background()

	var got Limits
	read := func() {
		if err := p.QueryRow(ctx, `SELECT post_stay_device_attempts_per_minute, post_stay_ip_attempts_per_minute,
			post_stay_wrong_answers_before_lockout, post_stay_lockout_minutes,
			post_stay_new_device_room_attempts_per_minute, post_stay_appliance_attempts_per_minute
			FROM iam_v2.signin_capacity_settings_get($1::uuid,$2::uuid)`, f.tenant, f.site).
			Scan(&got.DeviceAttemptsPerMinute, &got.IPAttemptsPerMinute, &got.WrongAnswersBeforeLockout,
				&got.LockoutMinutes, &got.NewDeviceRoomAttemptsPerMinute, &got.ApplianceAttemptsPerMinute); err != nil {
			t.Fatalf("read settings: %v", err)
		}
	}
	read()
	if got != DefaultLimits() {
		t.Fatalf("a site with no row reads %+v, want the defaults %+v", got, DefaultLimits())
	}
	set := func(values, reason string) error {
		_, err := p.Exec(ctx, `SELECT iam_v2.signin_capacity_settings_set($1::uuid,$2::uuid,$3::jsonb,'tester',$4)`, f.tenant, f.site, values, reason)
		return err
	}
	if err := set(`{"post_stay_lockout_minutes":30,"post_stay_wrong_answers_before_lockout":4}`, "tighten after a complaint"); err != nil {
		t.Fatalf("in-range change: %v", err)
	}
	read()
	if got.LockoutMinutes != 30 || got.WrongAnswersBeforeLockout != 4 || got.DeviceAttemptsPerMinute != 10 {
		t.Fatalf("after the change %+v", got)
	}
	for name, v := range map[string]string{
		"lockout over four hours": `{"post_stay_lockout_minutes":241}`,
		"lockout zero":            `{"post_stay_lockout_minutes":0}`,
		"wrong answers below 3":   `{"post_stay_wrong_answers_before_lockout":2}`,
		"room cap above 60":       `{"post_stay_new_device_room_attempts_per_minute":61}`,
		"appliance ceiling below": `{"post_stay_appliance_attempts_per_minute":19}`,
		"address cap above 200":   `{"post_stay_ip_attempts_per_minute":201}`,
		"device cap below 3":      `{"post_stay_device_attempts_per_minute":2}`,
		"unknown setting":         `{"post_stay_nothing":5}`,
	} {
		if err := set(v, "out of range"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := set(`{"post_stay_lockout_minutes":20}`, "x"); err == nil {
		t.Error("a change without a real reason was accepted")
	}
	read()
	if got.LockoutMinutes != 30 {
		t.Fatalf("a refused change moved the value: %+v", got)
	}
	var by, reason, oldV, newV string
	if err := p.QueryRow(ctx, `SELECT changed_by, change_reason, old_values::text, new_values::text
		FROM iam_v2.signin_capacity_settings_changes WHERE tenant_id=$1 AND site_id=$2 ORDER BY changed_at DESC LIMIT 1`,
		f.tenant, f.site).Scan(&by, &reason, &oldV, &newV); err != nil {
		t.Fatalf("change log: %v", err)
	}
	if by != "tester" || reason != "tighten after a complaint" ||
		!strings.Contains(oldV, `"post_stay_lockout_minutes": 15`) || !strings.Contains(newV, `"post_stay_lockout_minutes": 30`) {
		t.Fatalf("change log row: by=%q reason=%q old=%s new=%s", by, reason, oldV, newV)
	}
}
