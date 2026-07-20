//go:build integration

package checkout

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/grace"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping checkout PG16 integration")
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
	tenant, site, iface, stay string
	gracePkgRev               string
	oldEnt                    string
	devices                   []string
	sessions                  []string
}

// seedOpts controls the site grace config the fixture is built with.
type seedOpts struct {
	configureTypedPolicy bool // populate the all-or-none typed grace scalars (valid configured policy)
	pinGracePackage      bool // pin grace_package_revision_id
	withEntitlement      bool // give the IN_HOUSE Stay an ACTIVE entitlement + 2 devices + 2 sessions
}

// seed builds a full commerce fixture: tenant/site/interface + service-plan/package revisions + an IN_HOUSE
// Stay (lifecycle_version 1), optionally an active Entitlement with 2 authorized devices and 2 live sessions,
// and a site_checkout_grace_config per opts.
func seed(t *testing.T, p *pgxpool.Pool, o seedOpts) fixture {
	t.Helper()
	ctx := context.Background()
	var f fixture
	f.devices = make([]string, 0, 2)
	f.sessions = make([]string, 0, 2)

	// tenant/site/interface/revision + service-plan + grace package
	var svcRev string
	err := p.QueryRow(ctx, `WITH
	  t  AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id, 'protel-fias','ACTIVE' FROM si RETURNING id,tenant_id,site_id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, 'grace-plan' FROM pi RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id, 1, 5000, 2000, 'VALIDITY_WINDOW', 1073741824 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, 'grace-pkg', true FROM pi RETURNING id,tenant_id,site_id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type)
	          SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.id, 1, spr.id, 'CHECKOUT_GRACE' FROM ip, spr RETURNING id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 'R1','R1','IN_HOUSE',1 FROM pi RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM st)::text, (SELECT id FROM ipr)::text, (SELECT id FROM spr)::text`).
		Scan(&f.tenant, &f.site, &f.iface, &f.stay, &f.gracePkgRev, &svcRev)
	if err != nil {
		t.Fatalf("seed core: %v", err)
	}

	if o.withEntitlement {
		// original purchase (ADMIN_GRANT — no quote required) + ACTIVE entitlement on the Stay
		var purchaseID string
		if err := p.QueryRow(ctx, `INSERT INTO iam_v2.purchases
			(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
			VALUES ($1,$2,$3,$4,$5,'ADMIN_GRANT',0,'GRANTED') RETURNING id`,
			f.tenant, f.site, f.gracePkgRev, f.iface, f.stay).Scan(&purchaseID); err != nil {
			t.Fatalf("seed purchase: %v", err)
		}
		if err := p.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
			(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,
			 time_accounting_mode,end_mode,status,activated_at)
			VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7,'VALIDITY_WINDOW','AT_CHECKOUT','ACTIVE',now()) RETURNING id`,
			f.tenant, f.site, f.stay, f.iface, purchaseID, svcRev, f.gracePkgRev).Scan(&f.oldEnt); err != nil {
			t.Fatalf("seed entitlement: %v", err)
		}
		for i := 0; i < 2; i++ {
			var dev string
			mac := fmt.Sprintf("02:00:00:00:00:0%d", i+1)
			if err := p.QueryRow(ctx, `INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
				VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),$3::macaddr) RETURNING id`,
				f.tenant, f.site, mac).Scan(&dev); err != nil {
				t.Fatalf("seed device: %v", err)
			}
			f.devices = append(f.devices, dev)
			if _, err := p.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
				(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
				VALUES ($1,$2,$3,$4,'AUTHORIZED',now(),now())`, f.tenant, f.site, f.oldEnt, dev); err != nil {
				t.Fatalf("seed entitlement_device: %v", err)
			}
			var sess string
			if err := p.QueryRow(ctx, `INSERT INTO iam_v2.sessions
				(id,tenant_id,site_id,entitlement_id,device_id,state,started)
				VALUES (gen_random_uuid(),$1,$2,$3,$4,'active',now()) RETURNING id`,
				f.tenant, f.site, f.oldEnt, dev).Scan(&sess); err != nil {
				t.Fatalf("seed session: %v", err)
			}
			f.sessions = append(f.sessions, sess)
		}
	}

	// grace config
	pkg := "NULL"
	args := []any{f.tenant, f.site}
	if o.pinGracePackage {
		pkg = "$3"
		args = append(args, f.gracePkgRev)
	}
	if o.configureTypedPolicy {
		if _, err := p.Exec(ctx, `INSERT INTO iam_v2.site_checkout_grace_config
			(tenant_id,site_id,grace_package_revision_id,grace_duration_seconds,grace_down_kbps,grace_up_kbps,grace_data_quota_bytes,grace_device_limit,grace_device_limit_policy)
			VALUES ($1,$2,`+pkg+`,3600,5000,2000,524288000,2,'REJECT_NEW_DEVICE')`, args...); err != nil {
			t.Fatalf("seed grace config (typed): %v", err)
		}
	} else {
		// unconfigured typed policy (all NULL) — the Emergency-fallback path
		if _, err := p.Exec(ctx, `INSERT INTO iam_v2.site_checkout_grace_config
			(tenant_id,site_id,grace_package_revision_id) VALUES ($1,$2,`+pkg+`)`, args...); err != nil {
			t.Fatalf("seed grace config (unconfigured): %v", err)
		}
	}
	return f
}

func stayStatus(t *testing.T, p *pgxpool.Pool, stay string) (string, bool) {
	t.Helper()
	var st string
	var effco bool
	if err := p.QueryRow(context.Background(), `SELECT status, effective_checkout_at IS NOT NULL FROM iam_v2.stays WHERE id=$1`, stay).Scan(&st, &effco); err != nil {
		t.Fatal(err)
	}
	return st, effco
}

// TestIntegration_ConvertCreatesGrace proves the full atomic conversion: checkout + one CHECKOUT_GRACE
// entitlement superseding the terminated original, devices grandfathered, live sessions rebound (no logout).
func TestIntegration_ConvertCreatesGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seed(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, withEntitlement: true})

	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !res.CheckedOut || !res.GraceCreated || res.Trigger != grace.TriggerCheckoutGrace || res.IsEmergency {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.DevicesGrandfathered != 2 || res.SessionsRebound != 2 {
		t.Fatalf("rebind counts = %d devices / %d sessions, want 2/2", res.DevicesGrandfathered, res.SessionsRebound)
	}
	if st, effco := stayStatus(t, p, f.stay); st != "CHECKED_OUT" || !effco {
		t.Fatalf("stay status=%s effco=%v, want CHECKED_OUT/true", st, effco)
	}
	// original entitlement terminated as CONVERTED
	var oldStatus, oldReason string
	if err := p.QueryRow(ctx, `SELECT status, terminal_reason FROM iam_v2.entitlements WHERE id=$1`, f.oldEnt).Scan(&oldStatus, &oldReason); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "TERMINATED" || oldReason != "CONVERTED" {
		t.Fatalf("old entitlement = %s/%s, want TERMINATED/CONVERTED", oldStatus, oldReason)
	}
	// new grace entitlement: ACTIVE, GRACE_AFTER_CHECKOUT, supersedes old, window = effco+3600, not emergency
	var status, endMode, supersedes string
	var isEmergency, windowOK bool
	if err := p.QueryRow(ctx, `SELECT e.status, e.end_mode, e.supersedes_entitlement_id::text, e.is_emergency_grace,
		e.window_ends_at = s.effective_checkout_at + interval '3600 seconds'
		FROM iam_v2.entitlements e JOIN iam_v2.stays s ON s.id=e.stay_id
		WHERE e.id=$1`, res.NewEntitlementID).Scan(&status, &endMode, &supersedes, &isEmergency, &windowOK); err != nil {
		t.Fatal(err)
	}
	if status != "ACTIVE" || endMode != "GRACE_AFTER_CHECKOUT" || supersedes != f.oldEnt || isEmergency || !windowOK {
		t.Fatalf("new entitlement bad: status=%s end=%s supersedes=%s emergency=%v windowOK=%v", status, endMode, supersedes, isEmergency, windowOK)
	}
	// exactly one live entitlement for the Stay (ent_live_stay), and it is the new one
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status IN ('ACTIVE','PENDING','SUSPENDED')`, f.stay); n != 1 {
		t.Fatalf("live entitlements = %d, want 1", n)
	}
	// sessions rebound to the new entitlement, still active (no logout)
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE entitlement_id=$1 AND state='active'`, res.NewEntitlementID); n != 2 {
		t.Fatalf("rebound active sessions = %d, want 2", n)
	}
	// grandfathered device rows on the new entitlement
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$1 AND grandfathered`, res.NewEntitlementID); n != 2 {
		t.Fatalf("grandfathered devices = %d, want 2", n)
	}
}

// TestIntegration_Idempotent proves a duplicate checkout creates no second grace conversion.
func TestIntegration_Idempotent(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seed(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, withEntitlement: true})

	if _, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay); err != nil {
		t.Fatalf("first convert: %v", err)
	}
	res2, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay)
	if err != nil {
		t.Fatalf("second convert: %v", err)
	}
	if !res2.AlreadyCheckedOut || res2.GraceCreated {
		t.Fatalf("second convert should be idempotent no-op: %+v", res2)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1 AND trigger IN ('CHECKOUT_GRACE','EMERGENCY_GRACE')`, f.stay); n != 1 {
		t.Fatalf("grace purchases = %d, want exactly 1", n)
	}
}

// TestIntegration_ConcurrentSingleWinner proves ≥24 concurrent checkout handlers on the same Stay produce
// EXACTLY ONE grace conversion (the Stay lock serializes; the one_conversion_per_episode index backstops).
func TestIntegration_ConcurrentSingleWinner(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	f := seed(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, withEntitlement: true})

	const n = 24
	var wins int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.ConvertAtCheckout(context.Background(), f.tenant, f.site, f.iface, f.stay)
			if err == nil && r.GraceCreated {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("grace-creating winners = %d, want exactly 1", wins)
	}
	if m := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); m != 1 {
		t.Fatalf("grace entitlements = %d, want exactly 1", m)
	}
}

// TestIntegration_NoActiveEntitlement proves checkout still happens but no grace is minted for a Stay that
// held no active Entitlement at the boundary.
func TestIntegration_NoActiveEntitlement(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seed(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, withEntitlement: false})

	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !res.CheckedOut || res.GraceCreated || res.Reason != "NO_ACTIVE_ENTITLEMENT_AT_CHECKOUT" {
		t.Fatalf("unexpected: %+v", res)
	}
	if st, _ := stayStatus(t, p, f.stay); st != "CHECKED_OUT" {
		t.Fatalf("stay must still be checked out, got %s", st)
	}
}

// TestIntegration_EmergencyFallback proves an unconfigured/invalid typed policy (with a pinned grace package)
// still converts the eligible Guest onto the VERSIONED built-in Emergency policy, flagged + alerting.
func TestIntegration_EmergencyFallback(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seed(t, p, seedOpts{configureTypedPolicy: false, pinGracePackage: true, withEntitlement: true})

	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !res.GraceCreated || res.Trigger != grace.TriggerEmergency || !res.IsEmergency || !res.ConfigInvalidAlert {
		t.Fatalf("unexpected emergency result: %+v", res)
	}
	// the new entitlement carries is_emergency_grace and the built-in emergency window (3600s)
	var isEmergency, windowOK bool
	if err := p.QueryRow(ctx, `SELECT e.is_emergency_grace,
		e.window_ends_at = s.effective_checkout_at + interval '3600 seconds'
		FROM iam_v2.entitlements e JOIN iam_v2.stays s ON s.id=e.stay_id WHERE e.id=$1`,
		res.NewEntitlementID).Scan(&isEmergency, &windowOK); err != nil {
		t.Fatal(err)
	}
	if !isEmergency || !windowOK {
		t.Fatalf("emergency entitlement flags wrong: emergency=%v windowOK=%v", isEmergency, windowOK)
	}
}

// TestIntegration_NoGracePackageFailClosed proves that with NO pinned grace package the conversion fails closed:
// the checkout stands, no phantom entitlement is minted, and the config-invalid alert is raised.
func TestIntegration_NoGracePackageFailClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seed(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: false, withEntitlement: true})

	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if res.GraceCreated || !res.ConfigInvalidAlert || res.Reason != "NO_GRACE_PACKAGE_FAIL_CLOSED" {
		t.Fatalf("expected fail-closed no-grace, got %+v", res)
	}
	if st, _ := stayStatus(t, p, f.stay); st != "CHECKED_OUT" {
		t.Fatalf("checkout must still stand, got %s", st)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); n != 0 {
		t.Fatalf("no grace entitlement must exist, got %d", n)
	}
}

func count(t *testing.T, p *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}
