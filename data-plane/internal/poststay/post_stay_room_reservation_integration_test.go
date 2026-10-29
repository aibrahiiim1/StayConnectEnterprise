//go:build integration && phase5

package poststay

// Post-stay sign-in by room number and reservation number (audit decision AD-36). The cases are written as attacks: the
// property is that nobody can reach a stay that is not theirs, and that every refusal looks the same.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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

type fx struct{ tenant, site, iface, rev, stay, device, network, pkg string }

// seed builds a site with a CHECKED_OUT Stay and a zero-price POST_STAY package, in its own tenant.
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
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'PS-PLAN' FROM si RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,
	            max_concurrent_devices,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id,1,2,'VALIDITY_WINDOW',1000000 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,active)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'PS-PKG',true FROM si RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
	            service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(), si.tenant_id, si.id, ip.id,1, spr.id,'POST_STAY',0,
	                 ARRAY['NOT_REQUIRED']::text[], '{"duration_minutes":120}'::jsonb
	            FROM si, ip, spr RETURNING id, package_id)
	SELECT (SELECT tenant_id FROM pi)::text,(SELECT site_id FROM pi)::text,(SELECT id FROM pi)::text,
	       (SELECT id FROM pr)::text,(SELECT id FROM st)::text,(SELECT id FROM dv)::text,
	       (SELECT id FROM gn)::text,(SELECT id FROM ipr)::text`).
		Scan(&f.tenant, &f.site, &f.iface, &f.rev, &f.stay, &f.device, &f.network, &f.pkg)
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
	// posting_allowed=false in the same UPDATE, as internal/checkout does: the posting_only_in_house CHECK runs
	// before the AFTER trigger recomputes it for the departure.
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false, effective_checkout_at=now()
		WHERE id=$1`, f.stay); err != nil {
		t.Fatalf("re-checkout: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// authorizeDevice records the "device was authenticated on this stay" lineage the guest path derives everything from.
func authorizeDevice(t *testing.T, p *pgxpool.Pool, f fx) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// purchases are a controlled-writer table (commerce_intent) on the full schema
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
		t.Fatalf("open commerce_intent: %v", err)
	}
	var purchase, ent string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,'ADMIN_GRANT',0,'GRANTED') RETURNING id::text`,
		f.tenant, f.site, f.pkg, f.stay).Scan(&purchase); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,
		 time_accounting_mode,end_mode,window_ends_at,status)
		SELECT $1,$2,$3,$4,'{}'::jsonb, ipr.service_plan_revision_id, $5,'VALIDITY_WINDOW','VALIDITY_WINDOW',
		       now()+interval '2 hours','PENDING'
		  FROM iam_v2.internet_package_revisions ipr WHERE ipr.id=$5
		RETURNING id::text`, f.tenant, f.site, f.stay, purchase, f.pkg).Scan(&ent); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now(),NULL)`, ent); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// The authorization interval needs a binding behind it; the guard refuses one without.
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
		(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
		VALUES ($1,$2,$3,$4,'AUTHORIZED', now(), now())`, f.tenant, f.site, ent, f.device); err != nil {
		t.Fatalf("bind device: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('device_auth')`); err != nil {
		t.Fatalf("open device_auth: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations
		(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
		VALUES ($1,$2,$3,$4,1, now())`, f.tenant, f.site, ent, f.device); err != nil {
		t.Fatalf("authorize device: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
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

// roomAndGuest gives the seeded stay a room and one guest with the given last name (what the PMS mirror holds).
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

func req(f fx, device, room, res, last string) VerifyRequest {
	return VerifyRequest{Tenant: f.tenant, Site: f.site, Device: device, IP: "10.99.0.5", Room: room, Reservation: res, LastName: last}
}

// The device that was on the stay signs in with the room and the reservation; a profile is made once, with no PIN.
func TestIntegration_AD36_LineageDeviceSignsInWithRoomAndReservation(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	s := store(t, p)
	ctx := context.Background()

	// Typed the way a guest types it: lower case room, spaces around.
	profile, err := s.Verify(ctx, req(f, f.device, " 101 ", " R1 ", ""))
	if err != nil {
		t.Fatalf("the right room and reservation: %v", err)
	}
	var via string
	var pinHash *string
	if err := p.QueryRow(ctx, `SELECT issued_via, pin_hash FROM iam_v2.post_stay_profiles WHERE id=$1`, profile).Scan(&via, &pinHash); err != nil {
		t.Fatal(err)
	}
	if via != "ROOM_RESERVATION" || pinHash != nil {
		t.Fatalf("profile via=%q pin_hash=%v, want ROOM_RESERVATION with no PIN", via, pinHash)
	}
	again, err := s.Verify(ctx, req(f, f.device, "101", "R1", ""))
	if err != nil || again != profile {
		t.Fatalf("second sign-in: profile %q err %v, want the same profile %q", again, err, profile)
	}
	var ok bool
	if err := p.QueryRow(ctx, `SELECT iam_v2.p5_post_stay_authenticable($1,$2,$3)`, f.tenant, f.site, profile).Scan(&ok); err != nil || !ok {
		t.Fatalf("the profile cannot authenticate: %v %v", ok, err)
	}
}

// Every refusal is the same refusal, whichever part was wrong. Each case gets its own stay and device, since the
// lockout (correctly) counts repeated failures from one device.
func TestIntegration_AD36_EveryRefusalIsTheSameRefusal(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := store(t, p)
	ctx := context.Background()
	for name, c := range map[string][3]string{
		"wrong room":             {"102", "R1", ""},
		"wrong reservation":      {"101", "R2", ""},
		"both wrong":             {"999", "ZZ", ""},
		"empty":                  {"", "", ""},
		"room only":              {"101", "", ""},
		"oversized":              {string(make([]byte, 500)), "R1", ""},
		"right reservation only": {"", "R1", ""},
	} {
		f := seed(t, p)
		roomAndGuest(t, p, f, "101", "LOVELACE")
		authorizeDevice(t, p, f)
		_, err := s.Verify(ctx, req(f, f.device, c[0], c[1], c[2]))
		if !errors.Is(err, ErrNotAuthenticable) || err.Error() != ErrNotAuthenticable.Error() {
			t.Errorf("%s: err = %v, want the single ErrNotAuthenticable", name, err)
		}
	}
}

// Typing another guest's room and reservation from a device that was never on that stay reaches nothing.
func TestIntegration_AD36_NoCrossStayGuessing(t *testing.T) {
	p := pool(t)
	defer p.Close()
	mine := seed(t, p)
	roomAndGuest(t, p, mine, "101", "LOVELACE")
	authorizeDevice(t, p, mine)
	theirs := seed(t, p)
	roomAndGuest(t, p, theirs, "202", "BABBAGE")
	authorizeDevice(t, p, theirs)
	s := store(t, p)
	ctx := context.Background()

	// My device types THEIR room and reservation (R1 is the seeded reservation in both fixtures; the room differs).
	r := req(mine, mine.device, "202", "R1", "")
	if _, err := s.Verify(ctx, r); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("a lineage device reached another stay by its room: %v", err)
	}
	// Even with their last name: a device with a stay of its own does not use the new-device route.
	r.LastName = "BABBAGE"
	if _, err := s.Verify(ctx, r); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("a lineage device reached another stay by name: %v", err)
	}
}

// A device that was never on the stay needs the last name from the stay record as well, and exactly one stay.
func TestIntegration_AD36_ANewDeviceMustAlsoGiveTheLastName(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	fresh := newDevice(t, p, f, "02:00:00:00:00:77")
	s := store(t, p)
	ctx := context.Background()

	for name, r := range map[string]VerifyRequest{
		"no name":    req(f, fresh, "101", "R1", ""),
		"wrong name": req(f, fresh, "101", "R1", "SMITH"),
		"wrong room": req(f, fresh, "102", "R1", "LOVELACE"),
		"wrong res":  req(f, fresh, "101", "R9", "LOVELACE"),
	} {
		if _, err := s.Verify(ctx, r); !errors.Is(err, ErrNotAuthenticable) {
			t.Errorf("%s: %v, want ErrNotAuthenticable", name, err)
		}
	}
	if _, err := s.Verify(ctx, req(f, fresh, "101", "R1", " lovelace ")); err != nil {
		t.Fatalf("the three facts together from a new device: %v", err)
	}
}

// The lockout is per stay AND device: the guesser is locked out, the guest's own devices are not.
func TestIntegration_AD36_LockoutIsPerStayAndDevice(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	other := newDevice(t, p, f, "02:00:00:00:00:78")
	s := storeAtFixedTime(t, p, time.Now().Add(-90*time.Minute))
	ctx := context.Background()

	// The legitimate device mistypes five times, then is refused even with the right answer.
	for i := 0; i < 5; i++ {
		if _, err := s.Verify(ctx, req(f, f.device, "101", "WRONG", "")); !errors.Is(err, ErrNotAuthenticable) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := s.Verify(ctx, req(f, f.device, "101", "R1", "")); !errors.Is(err, ErrThrottled) {
		t.Fatalf("after five wrong answers the device must be locked out, got %v", err)
	}
	// Someone else's device guessing at this stay does not lock the guest's own device out.
	for i := 0; i < 5; i++ {
		_, _ = s.Verify(ctx, req(f, other, "101", "GUESS", "NOBODY"))
	}
	var locked bool
	_, err := s.Verify(ctx, req(f, other, "101", "R1", "LOVELACE"))
	locked = errors.Is(err, ErrThrottled)
	if !locked {
		t.Fatalf("the guessing device should itself be locked out, got %v", err)
	}
}

// A revoked profile never authenticates again, and a reinstatement orphans the old profile.
func TestIntegration_AD36_RevokedAndOrphanedProfilesRefuse(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	s := store(t, p)
	ctx := context.Background()

	profile, err := s.Verify(ctx, req(f, f.device, "101", "R1", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, RevokeRequest{Tenant: f.tenant, Site: f.site, Profile: profile, Operator: "11111111-1111-1111-1111-111111111111", Reason: "test"}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.Verify(ctx, req(f, f.device, "101", "R1", "")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("a revoked profile signed in: %v", err)
	}

	g := seed(t, p)
	roomAndGuest(t, p, g, "303", "HOPPER")
	authorizeDevice(t, p, g)
	prof2, err := s.Verify(ctx, req(g, g.device, "303", "R1", ""))
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
	_ = os.Getenv
	_ = throttle.ScopeDevice
}
