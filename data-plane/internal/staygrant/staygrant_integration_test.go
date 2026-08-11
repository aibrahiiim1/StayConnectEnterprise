//go:build integration

package staygrant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping staygrant PG16 integration")
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

type fixture struct {
	tenant, site, iface, rev, stay, device, network string
	pkgRev                                          string
}

// seed builds a tenant/site/interface/revision + IN_HOUSE Stay with fresh occupancy evidence + a guest
// network + a device, plus an INCLUDED (zero-price, NOT_REQUIRED) guest package whose current revision is
// pinned — the exact shape a real grant runs against.
func seed(t *testing.T, p *pgxpool.Pool, priceMinor int64, settlement, pkgType string, devices int) fixture {
	t.Helper()
	ctx := context.Background()
	cfg := `{"endpoint":"x","max_auth_cache_age_seconds":3600}`
	var f fixture
	if err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  gn AS (INSERT INTO public.guest_networks(id,tenant_id,site_id) SELECT gen_random_uuid(), si.tenant_id, si.id FROM si RETURNING id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'protel-fias','ACTIVE' FROM si RETURNING id,tenant_id,site_id),
	  pr AS (INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 1, 'UTC', $1::jsonb FROM pi RETURNING id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,
	           occupancy_evidence_at,occupancy_ingested_at,occupancy_revision_id,occupancy_normalization_version,occupancy_clock_suspect,occupancy_evidence_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,'R1','R1','IN_HOUSE',1, now(), now(), pr.id, 1, false, 1 FROM pi, pr RETURNING id),
	  dv AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, gen_random_uuid(),'02:00:00:00:10:01'::macaddr FROM pi RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'guest-plan',true FROM pi RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id,1,8000,3000,$5,'REJECT_NEW_DEVICE','VALIDITY_WINDOW',1073741824 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'guest-pkg',false FROM pi RETURNING id,tenant_id,site_id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.id,1,spr.id,$4::text,$3::bigint,ARRAY[$2::text],
	                 '{"end_mode":"VALIDITY_WINDOW","duration_seconds":86400}'::jsonb FROM ip, spr RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM pr)::text, (SELECT id FROM st)::text, (SELECT id FROM dv)::text,
	       (SELECT id FROM gn)::text, (SELECT id FROM ipr)::text`,
		cfg, settlement, priceMinor, pkgType, devices).
		Scan(&f.tenant, &f.site, &f.iface, &f.rev, &f.stay, &f.device, &f.network, &f.pkgRev); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, f.pkgRev); err != nil {
		t.Fatalf("pin revision: %v", err)
	}
	return f
}

func seedIncluded(t *testing.T, p *pgxpool.Pool) fixture {
	return seed(t, p, 0, "NOT_REQUIRED", "FREE_STAY", 2)
}

func issue(t *testing.T, p *pgxpool.Pool, f fixture) string {
	t.Helper()
	id, err := authctx.NewStore(p).IssuePMS(context.Background(), authctx.PMSGrant{
		Tenant: f.tenant, Site: f.site, Interface: f.iface, Revision: f.rev,
		Stay: f.stay, Device: f.device, GuestNetwork: f.network, TTLSeconds: 300,
	})
	if err != nil {
		t.Fatalf("issue auth context: %v", err)
	}
	return id
}

func presenter(f fixture) authctx.Presenter {
	return authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: f.device, GuestNetwork: f.network}
}

func count(t *testing.T, p *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestIntegration_GrantIsOneTransaction proves the whole chain commits together and produces exactly the
// history a later Checkout reads: context consumed once, quote + purchase pinned to it, entitlement ACTIVE
// with its INITIAL transition, and the device's authorization interval open.
func TestIntegration_GrantIsOneTransaction(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedIncluded(t, p)
	acID := issue(t, p, f)

	r, err := New(p).Grant(ctx, f.tenant, f.site, Request{AuthContextID: acID, Presenter: presenter(f), PackageRevID: f.pkgRev})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if r.Stay != f.stay || r.Interface != f.iface {
		t.Fatalf("grant resolved stay=%s iface=%s, want %s/%s", r.Stay, r.Interface, f.stay, f.iface)
	}
	// auth context consumed exactly once
	if n := count(t, p, `SELECT count(*) FROM iam_v2.auth_contexts WHERE id=$1 AND consumed_at IS NOT NULL`, acID); n != 1 {
		t.Fatalf("auth context not consumed (%d)", n)
	}
	// quote + purchase are pinned to that context
	if n := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE id=$1 AND offer_quote_id=$2 AND auth_context_id=$3
		AND trigger='GUEST_SELECTION' AND state='GRANTED' AND amount_minor=0`, r.PurchaseID, r.QuoteID, acID); n != 1 {
		t.Fatal("purchase not pinned to the consumed context + quote")
	}
	// entitlement ACTIVE, backed by its INITIAL transition (seq=1, from_state NULL)
	var status, endMode string
	var window *time.Time
	if err := p.QueryRow(ctx, `SELECT status, end_mode, window_ends_at FROM iam_v2.entitlements WHERE id=$1`,
		r.EntitlementID).Scan(&status, &endMode, &window); err != nil {
		t.Fatal(err)
	}
	if status != "ACTIVE" || endMode != "VALIDITY_WINDOW" || window == nil {
		t.Fatalf("entitlement status=%s end_mode=%s window=%v", status, endMode, window)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_state_transitions
		WHERE entitlement_id=$1 AND seq=1 AND from_state IS NULL AND to_state='ACTIVE' AND superseded_by IS NULL`, r.EntitlementID); n != 1 {
		t.Fatal("entitlement has no initial ACTIVE history")
	}
	// device authorized: current view + an OPEN append-only interval
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices
		WHERE entitlement_id=$1 AND device_id=$2 AND status='AUTHORIZED'`, r.EntitlementID, f.device); n != 1 {
		t.Fatal("device not authorized in the current view")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		WHERE entitlement_id=$1 AND device_id=$2 AND seq=1 AND deauthorized_at IS NULL`, r.EntitlementID, f.device); n != 1 {
		t.Fatal("device authorization interval not open")
	}
}

// TestIntegration_GrantRollsBackTogether proves there is no partially-granted state: when a late step fails,
// the Auth Context is NOT consumed and no quote/purchase/entitlement survives.
func TestIntegration_GrantRollsBackTogether(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedIncluded(t, p)
	acID := issue(t, p, f)
	// the grant composes into a CALLER transaction: when anything the caller does afterwards fails, the WHOLE
	// chain — including the one-time Auth Context consumption — must roll back with it.
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(p).GrantTx(ctx, tx, f.tenant, f.site, Request{AuthContextID: acID, Presenter: presenter(f), PackageRevID: f.pkgRev}); err != nil {
		t.Fatalf("grant inside the caller tx: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1/0`); err == nil {
		t.Fatal("expected the caller's later statement to fail")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.auth_contexts WHERE id=$1 AND consumed_at IS NOT NULL`, acID); n != 0 {
		t.Fatal("auth context was consumed by a failed grant")
	}
	for _, q := range []string{
		`SELECT count(*) FROM iam_v2.offer_quotes WHERE auth_context_id=$1`,
		`SELECT count(*) FROM iam_v2.purchases WHERE auth_context_id=$1`,
	} {
		if n := count(t, p, q, acID); n != 0 {
			t.Fatalf("failed grant left rows: %s", q)
		}
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay); n != 0 {
		t.Fatal("failed grant left an entitlement")
	}
}

// TestIntegration_PaidPackageFailsClosed proves paid access is refused rather than silently granted for free.
func TestIntegration_PaidPackageFailsClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		price      int64
		settlement string
	}{
		{"priced", 1500, "NOT_REQUIRED"},
		{"settlement-required", 0, "ROOM_CHARGE"},
	} {
		f := seed(t, p, tc.price, tc.settlement, "FREE_STAY", 2)
		acID := issue(t, p, f)
		_, err := New(p).Grant(ctx, f.tenant, f.site, Request{AuthContextID: acID, Presenter: presenter(f), PackageRevID: f.pkgRev})
		if !errors.Is(err, ErrSettlementRequired) {
			t.Fatalf("%s: err=%v, want ErrSettlementRequired", tc.name, err)
		}
		if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay); n != 0 {
			t.Fatalf("%s: a refused paid grant created an entitlement", tc.name)
		}
		if n := count(t, p, `SELECT count(*) FROM iam_v2.auth_contexts WHERE id=$1 AND consumed_at IS NOT NULL`, acID); n != 0 {
			t.Fatalf("%s: a refused paid grant consumed the context", tc.name)
		}
	}
}

// TestIntegration_GraceCatalogNotGuestPurchasable proves the system grace catalogs can never be granted
// through the guest path.
func TestIntegration_GraceCatalogNotGuestPurchasable(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 0, "NOT_REQUIRED", "CHECKOUT_GRACE", 2)
	acID := issue(t, p, f)
	if _, err := New(p).Grant(ctx, f.tenant, f.site, Request{AuthContextID: acID, Presenter: presenter(f), PackageRevID: f.pkgRev}); !errors.Is(err, ErrPackageNotGrantable) {
		t.Fatalf("err=%v, want ErrPackageNotGrantable", err)
	}
}

// TestIntegration_ConcurrentGrantsOneWinner proves >=24 concurrent grants against the SAME Stay produce
// exactly ONE entitlement, one purchase and one consumed context — the one-time context and the one-live-
// entitlement rule both hold under contention, with no deadlock and no partial state.
func TestIntegration_ConcurrentGrantsOneWinner(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seedIncluded(t, p)
	const n = 24
	// every racer presents the SAME one-time context
	acID := issue(t, p, f)
	s := New(p)
	var wg sync.WaitGroup
	okc := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Grant(context.Background(), f.tenant, f.site,
				Request{AuthContextID: acID, Presenter: presenter(f), PackageRevID: f.pkgRev}); err == nil {
				okc <- struct{}{}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent grants did not settle — possible deadlock")
	}
	close(okc)
	if got := len(okc); got != 1 {
		t.Fatalf("%d grants succeeded, want exactly 1", got)
	}
	if got := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay); got != 1 {
		t.Fatalf("entitlements = %d, want 1", got)
	}
	if got := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1`, f.stay); got != 1 {
		t.Fatalf("purchases = %d, want 1", got)
	}
	if got := count(t, p, `SELECT count(*) FROM iam_v2.offer_quotes WHERE auth_context_id=$1`, acID); got != 1 {
		t.Fatalf("quotes = %d, want 1", got)
	}
}

// TestIntegration_DeviceAuthorizationLifecycle proves the controlled device operations keep the current view
// and the append-only interval history in step, are idempotent, enforce the plan's device limit atomically
// under concurrency, and allow a clean re-authorization after a deauthorization.
func TestIntegration_DeviceAuthorizationLifecycle(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 0, "NOT_REQUIRED", "FREE_STAY", 2) // plan allows 2 concurrent devices
	acID := issue(t, p, f)
	r, err := New(p).Grant(ctx, f.tenant, f.site, Request{AuthContextID: acID, Presenter: presenter(f), PackageRevID: f.pkgRev})
	if err != nil {
		t.Fatal(err)
	}
	ent := r.EntitlementID

	// idempotent: re-authorizing the same device does not open a second interval
	var again string
	if err := p.QueryRow(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())::text`, ent, f.device).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again != r.DeviceAuthID {
		t.Fatalf("re-authorization opened a new interval %s (want the existing %s)", again, r.DeviceAuthID)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id=$1 AND device_id=$2`, ent, f.device); n != 1 {
		t.Fatalf("intervals for the device = %d, want 1", n)
	}

	// a second device fits the limit of 2; a third is refused
	devs := make([]string, 0, 2)
	for i := 2; i <= 3; i++ {
		var d string
		if err := p.QueryRow(ctx, `INSERT INTO iam_v2.devices(tenant_id,site_id,appliance_id,mac)
			VALUES ($1,$2,gen_random_uuid(),$3::macaddr) RETURNING id::text`,
			f.tenant, f.site, fmt.Sprintf("02:00:00:00:10:%02d", i)).Scan(&d); err != nil {
			t.Fatal(err)
		}
		devs = append(devs, d)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, devs[0]); err != nil {
		t.Fatalf("second device should fit the limit: %v", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, devs[1]); err == nil {
		t.Fatal("third device must be refused by the plan's device limit")
	}

	// deauthorize device 2, then the third fits — and the closed interval stays readable
	var closed bool
	if err := p.QueryRow(ctx, `SELECT iam_v2.deauthorize_entitlement_device($1,$2,now(),'GUEST_LOGOUT')`, ent, devs[0]).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("deauthorization reported nothing to close")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		WHERE entitlement_id=$1 AND device_id=$2 AND deauthorized_at IS NOT NULL`, ent, devs[0]); n != 1 {
		t.Fatal("closed interval not recorded")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices
		WHERE entitlement_id=$1 AND device_id=$2 AND status='DISCONNECTED' AND disconnected_reason='GUEST_LOGOUT'`, ent, devs[0]); n != 1 {
		t.Fatal("current view not updated by the deauthorization")
	}
	// deauthorizing again is a no-op, not an error
	if err := p.QueryRow(ctx, `SELECT iam_v2.deauthorize_entitlement_device($1,$2,now(),'GUEST_LOGOUT')`, ent, devs[0]).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if closed {
		t.Fatal("second deauthorization must be a no-op")
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, devs[1]); err != nil {
		t.Fatalf("third device should fit after a deauthorization: %v", err)
	}
	// re-authorizing device 2 opens a NEW interval (seq 2) rather than reopening the closed one
	if _, err := p.Exec(ctx, `SELECT iam_v2.deauthorize_entitlement_device($1,$2,now(),'ADMIN')`, ent, devs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, devs[0]); err != nil {
		t.Fatal(err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		WHERE entitlement_id=$1 AND device_id=$2 AND seq=2 AND deauthorized_at IS NULL`, ent, devs[0]); n != 1 {
		t.Fatal("re-authorization did not open a fresh interval")
	}
}

// TestIntegration_ConcurrentDeviceAuthorizationsRespectLimit proves the device limit is enforced atomically:
// 24 racers authorizing 24 distinct devices against a 2-device plan leave exactly 2 open intervals.
func TestIntegration_ConcurrentDeviceAuthorizationsRespectLimit(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 0, "NOT_REQUIRED", "FREE_STAY", 2)
	acID := issue(t, p, f)
	r, err := New(p).Grant(ctx, f.tenant, f.site, Request{AuthContextID: acID, Presenter: presenter(f), PackageRevID: f.pkgRev})
	if err != nil {
		t.Fatal(err)
	}
	// the granting device already holds 1 of the 2 slots
	const n = 24
	devs := make([]string, n)
	for i := range devs {
		if err := p.QueryRow(ctx, `INSERT INTO iam_v2.devices(tenant_id,site_id,appliance_id,mac)
			VALUES ($1,$2,gen_random_uuid(),$3::macaddr) RETURNING id::text`,
			f.tenant, f.site, fmt.Sprintf("02:00:00:00:20:%02x", i)).Scan(&devs[i]); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for _, d := range devs {
		wg.Add(1)
		go func(dev string) {
			defer wg.Done()
			_, _ = p.Exec(context.Background(), `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, r.EntitlementID, dev)
		}(d)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent device authorizations did not settle — possible deadlock")
	}
	if open := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		WHERE entitlement_id=$1 AND deauthorized_at IS NULL`, r.EntitlementID); open != 2 {
		t.Fatalf("open authorization intervals = %d, want exactly 2 (the plan limit)", open)
	}
}

// TestIntegration_DeviceMustBeInScope proves the controlled authorization refuses a device from another
// tenant/site — an entitlement can never authorize someone else's hardware.
func TestIntegration_DeviceMustBeInScope(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedIncluded(t, p)
	other := seedIncluded(t, p)
	r, err := New(p).Grant(ctx, f.tenant, f.site, Request{AuthContextID: issue(t, p, f), Presenter: presenter(f), PackageRevID: f.pkgRev})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, r.EntitlementID, other.device); err == nil {
		t.Fatal("a device from another tenant/site must never be authorizable")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id=$1`, r.EntitlementID); n != 1 {
		t.Fatalf("out-of-scope authorization left %d intervals, want the original 1", n)
	}
}
