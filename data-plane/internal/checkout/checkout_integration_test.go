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
	gracePkgRev, svcRev       string
}

type seedOpts struct {
	configureTypedPolicy bool
	pinGracePackage      bool
	systemGracePackage   bool // grace package is is_system + current revision (valid). false => invalid config.
}

// seedBase builds tenant/site/interface + a system service-plan/CHECKOUT_GRACE package (current revision) + an
// IN_HOUSE Stay (episode 1) + a site_checkout_grace_config per opts.
func seedBase(t *testing.T, p *pgxpool.Pool, o seedOpts) fixture {
	t.Helper()
	ctx := context.Background()
	var f fixture
	sys := "true"
	if !o.systemGracePackage {
		sys = "false" // a non-system grace package fails item-6 validation -> Emergency path
	}
	err := p.QueryRow(ctx, `WITH
	  t  AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id, 'protel-fias','ACTIVE' FROM si RETURNING id,tenant_id,site_id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, 'grace-plan', true FROM pi RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id, 1, 5000, 2000, 'VALIDITY_WINDOW', 1073741824 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, 'grace-pkg', `+sys+` FROM pi RETURNING id,tenant_id,site_id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,settlement_methods)
	          SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.id, 1, spr.id, 'CHECKOUT_GRACE', 0, ARRAY['NOT_REQUIRED']::text[] FROM ip, spr RETURNING id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 'R1','R1','IN_HOUSE',1 FROM pi RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM st)::text, (SELECT id FROM ipr)::text, (SELECT id FROM spr)::text`).
		Scan(&f.tenant, &f.site, &f.iface, &f.stay, &f.gracePkgRev, &f.svcRev)
	if err != nil {
		t.Fatalf("seed base: %v", err)
	}
	// point the grace package at its revision (separate statement — a CTE cannot update a sibling CTE's row)
	if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages
		SET current_revision_id=$1 WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, f.gracePkgRev); err != nil {
		t.Fatalf("seed set current revision: %v", err)
	}
	pkg := "NULL"
	args := []any{f.tenant, f.site}
	if o.pinGracePackage {
		pkg = "$3"
		args = append(args, f.gracePkgRev)
	}
	if o.configureTypedPolicy {
		if _, err := p.Exec(ctx, `INSERT INTO iam_v2.site_checkout_grace_config
			(tenant_id,site_id,grace_package_revision_id,grace_duration_seconds,grace_down_kbps,grace_up_kbps,grace_data_quota_bytes,grace_device_limit,grace_device_limit_policy)
			VALUES ($1,$2,`+pkg+`,3600,4000,1500,524288000,2,'REJECT_NEW_DEVICE')`, args...); err != nil {
			t.Fatalf("seed grace config: %v", err)
		}
	} else {
		if _, err := p.Exec(ctx, `INSERT INTO iam_v2.site_checkout_grace_config (tenant_id,site_id,grace_package_revision_id) VALUES ($1,$2,`+pkg+`)`, args...); err != nil {
			t.Fatalf("seed grace config: %v", err)
		}
	}
	return f
}

// seedEntitlement inserts a purchase + entitlement with explicit activation/window/status timestamps.
func seedEntitlement(t *testing.T, p *pgxpool.Pool, f fixture, activatedAt, windowEndsAt *time.Time, status string, terminalReason *string, terminatedAt *time.Time) string {
	t.Helper()
	ctx := context.Background()
	var purchaseID string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,$5,'ADMIN_GRANT',0,'GRANTED') RETURNING id`,
		f.tenant, f.site, f.gracePkgRev, f.iface, f.stay).Scan(&purchaseID); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	var ent string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,
		 time_accounting_mode,end_mode,status,activated_at,window_ends_at,terminal_reason,terminated_at)
		VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7,'VALIDITY_WINDOW','VALIDITY_WINDOW',$8,$9,$10,$11,$12) RETURNING id`,
		f.tenant, f.site, f.stay, f.iface, purchaseID, f.svcRev, f.gracePkgRev,
		status, activatedAt, windowEndsAt, terminalReason, terminatedAt).Scan(&ent); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	return ent
}

// seedDeviceSession attaches a device (first_authorized=authAt) + a live session (started=startAt) to ent.
func seedDeviceSession(t *testing.T, p *pgxpool.Pool, f fixture, ent string, idx int, authAt, startAt time.Time) (string, string) {
	t.Helper()
	ctx := context.Background()
	var dev string
	mac := fmt.Sprintf("02:00:00:00:0%d:%02d", idx/100, idx%100)
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
		VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),$3::macaddr) RETURNING id`, f.tenant, f.site, mac).Scan(&dev); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
		(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
		VALUES ($1,$2,$3,$4,'AUTHORIZED',$5,$5)`, f.tenant, f.site, ent, dev, authAt); err != nil {
		t.Fatalf("seed entitlement_device: %v", err)
	}
	var sess string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(id,tenant_id,site_id,entitlement_id,device_id,state,started)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,'active',$5) RETURNING id`, f.tenant, f.site, ent, dev, startAt).Scan(&sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return dev, sess
}

func trusted(at time.Time) Boundary { return Boundary{TrustedAt: at, Trusted: true} }

func count(t *testing.T, p *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func stayState(t *testing.T, p *pgxpool.Pool, stay string) (status string, effcoSet, posting bool) {
	t.Helper()
	if err := p.QueryRow(context.Background(), `SELECT status, effective_checkout_at IS NOT NULL, posting_allowed FROM iam_v2.stays WHERE id=$1`, stay).Scan(&status, &effcoSet, &posting); err != nil {
		t.Fatal(err)
	}
	return
}

func liveOriginals(t *testing.T, p *pgxpool.Pool, stay string) int {
	// pre-checkout (non-grace) entitlements still live for the Stay
	return count(t, p, `SELECT count(*) FROM iam_v2.entitlements
		WHERE stay_id=$1 AND end_mode<>'GRACE_AFTER_CHECKOUT' AND status IN ('ACTIVE','PENDING','SUSPENDED')`, stay)
}

// TestIntegration_ConvertCreatesGrace: normal conversion + boundary device/session grandfathering (a device and
// session created AFTER the boundary are NOT grandfathered).
func TestIntegration_ConvertCreatesGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true})

	boundary := time.Now().Add(-2 * time.Hour).Truncate(time.Microsecond)
	before := boundary.Add(-30 * time.Minute)
	after := boundary.Add(30 * time.Minute)
	win := boundary.Add(48 * time.Hour)
	ent := seedEntitlement(t, p, f, &before, &win, "ACTIVE", nil, nil)
	seedDeviceSession(t, p, f, ent, 1, before, before)     // grandfathered
	seedDeviceSession(t, p, f, ent, 2, boundary, boundary) // authorized exactly at boundary -> grandfathered
	seedDeviceSession(t, p, f, ent, 3, after, after)       // post-boundary device -> NOT grandfathered

	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(boundary))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !res.CheckedOut || !res.GraceCreated || res.Trigger != grace.TriggerCheckoutGrace || res.IsEmergency {
		t.Fatalf("unexpected: %+v", res)
	}
	if res.DevicesGrandfathered != 2 || res.SessionsRebound != 2 {
		t.Fatalf("boundary snapshot = %d devices / %d sessions, want 2/2 (post-boundary excluded)", res.DevicesGrandfathered, res.SessionsRebound)
	}
	if st, effco, posting := stayState(t, p, f.stay); st != "CHECKED_OUT" || !effco || posting {
		t.Fatalf("stay=%s effco=%v posting_allowed=%v, want CHECKED_OUT/true/false", st, effco, posting)
	}
	if liveOriginals(t, p, f.stay) != 0 {
		t.Fatal("CHECKED_OUT must leave no live pre-checkout entitlement")
	}
	// window from the trusted boundary + 3600s; supersedes the terminated original
	var windowOK bool
	var supersedes string
	if err := p.QueryRow(ctx, `SELECT window_ends_at = $2::timestamptz + interval '3600 seconds', supersedes_entitlement_id::text
		FROM iam_v2.entitlements WHERE id=$1`, res.NewEntitlementID, boundary).Scan(&windowOK, &supersedes); err != nil {
		t.Fatal(err)
	}
	if !windowOK || supersedes != ent {
		t.Fatalf("grace window/supersedes wrong: windowOK=%v supersedes=%s", windowOK, supersedes)
	}
	// audit row: CHECKOUT_GRACE, not emergency, no alert, checkout policy version, one row
	var trig, polv string
	var isEm bool
	var alert *string
	if err := p.QueryRow(ctx, `SELECT trigger, is_emergency, policy_version, alert_code FROM iam_v2.checkout_grace_audit
		WHERE stay_id=$1 AND lifecycle_version=1`, f.stay).Scan(&trig, &isEm, &polv, &alert); err != nil {
		t.Fatal(err)
	}
	if trig != "CHECKOUT_GRACE" || isEm || polv != "CHECKOUT_GRACE_V1" || alert != nil {
		t.Fatalf("audit wrong: trig=%s emergency=%v ver=%s alert=%v", trig, isEm, polv, alert)
	}
}

// TestIntegration_Idempotent_BoundaryPreserved: a duplicate/delayed checkout with a DIFFERENT trusted timestamp
// preserves the established boundary and creates no second grace/audit.
func TestIntegration_Idempotent_BoundaryPreserved(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true})
	boundary := time.Now().Add(-3 * time.Hour).Truncate(time.Microsecond)
	before := boundary.Add(-time.Hour)
	win := boundary.Add(48 * time.Hour)
	seedEntitlement(t, p, f, &before, &win, "ACTIVE", nil, nil)

	if _, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(boundary)); err != nil {
		t.Fatalf("first: %v", err)
	}
	var effco1 time.Time
	if err := p.QueryRow(ctx, `SELECT effective_checkout_at FROM iam_v2.stays WHERE id=$1`, f.stay).Scan(&effco1); err != nil {
		t.Fatal(err)
	}
	// second call, DIFFERENT trusted timestamp — boundary must not move, no second grace/audit
	res2, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(boundary.Add(90*time.Minute)))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !res2.AlreadyProcessed || res2.GraceCreated {
		t.Fatalf("second must be idempotent: %+v", res2)
	}
	var effco2 time.Time
	if err := p.QueryRow(ctx, `SELECT effective_checkout_at FROM iam_v2.stays WHERE id=$1`, f.stay).Scan(&effco2); err != nil {
		t.Fatal(err)
	}
	if !effco1.Equal(effco2) {
		t.Fatalf("boundary moved: %v -> %v", effco1, effco2)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, f.stay); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); n != 1 {
		t.Fatalf("grace entitlements = %d, want 1", n)
	}
}

// TestIntegration_ConcurrentSingleWinner: >=24 concurrent handlers -> exactly one grace + one audit + no live
// original, on every path.
func TestIntegration_ConcurrentSingleWinner(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true})
	boundary := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	before := boundary.Add(-30 * time.Minute)
	win := boundary.Add(48 * time.Hour)
	seedEntitlement(t, p, f, &before, &win, "ACTIVE", nil, nil)

	const n = 24
	var wins int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.ConvertAtCheckout(context.Background(), f.tenant, f.site, f.iface, f.stay, trusted(boundary))
			if err == nil && r.GraceCreated {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("grace winners = %d, want 1", wins)
	}
	if m := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); m != 1 {
		t.Fatalf("grace entitlements = %d, want 1", m)
	}
	if a := count(t, p, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, f.stay); a != 1 {
		t.Fatalf("audit rows = %d, want 1", a)
	}
	if liveOriginals(t, p, f.stay) != 0 {
		t.Fatal("no live pre-checkout entitlement must remain")
	}
}

// TestIntegration_EligibilityAtBoundary covers item-3 delayed-event semantics.
func TestIntegration_EligibilityAtBoundary(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	reason := "HARD_EXPIRY"

	// (a) active at boundary, EXPIRED before processing (terminated after boundary) -> eligible
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true})
	b := time.Now().Add(-4 * time.Hour).Truncate(time.Microsecond)
	act := b.Add(-time.Hour)
	win := b.Add(30 * time.Minute) // window ended after the boundary but before now
	term := b.Add(30 * time.Minute)
	seedEntitlement(t, p, f, &act, &win, "TERMINATED", &reason, &term)
	r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(b))
	if err != nil || !r.GraceCreated {
		t.Fatalf("(a) active-at-boundary-expired-after must be eligible: %+v err=%v", r, err)
	}

	// (b) created AFTER the boundary -> not eligible
	f = seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true})
	b = time.Now().Add(-4 * time.Hour).Truncate(time.Microsecond)
	actAfter := b.Add(time.Hour)
	winAfter := b.Add(50 * time.Hour)
	seedEntitlement(t, p, f, &actAfter, &winAfter, "ACTIVE", nil, nil)
	r, err = c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(b))
	if err != nil || r.GraceCreated {
		t.Fatalf("(b) created-after-boundary must be ineligible: %+v err=%v", r, err)
	}

	// (c) expired BEFORE the boundary -> not eligible
	f = seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true})
	b = time.Now().Add(-4 * time.Hour).Truncate(time.Microsecond)
	act = b.Add(-2 * time.Hour)
	winBefore := b.Add(-time.Hour)
	term = b.Add(-time.Hour)
	seedEntitlement(t, p, f, &act, &winBefore, "TERMINATED", &reason, &term)
	r, err = c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(b))
	if err != nil || r.GraceCreated {
		t.Fatalf("(c) expired-before-boundary must be ineligible: %+v err=%v", r, err)
	}

	// (d) PENDING (never activated) -> not eligible
	f = seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true})
	b = time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	winP := b.Add(50 * time.Hour)
	seedEntitlement(t, p, f, nil, &winP, "PENDING", nil, nil)
	r, err = c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(b))
	if err != nil || r.GraceCreated {
		t.Fatalf("(d) PENDING must be ineligible: %+v err=%v", r, err)
	}

	// (e) SUSPENDED -> not silently reauthorized
	f = seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true})
	b = time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	act = b.Add(-30 * time.Minute)
	winS := b.Add(50 * time.Hour)
	seedEntitlement(t, p, f, &act, &winS, "SUSPENDED", nil, nil)
	r, err = c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(b))
	if err != nil || r.GraceCreated {
		t.Fatalf("(e) SUSPENDED must be ineligible: %+v err=%v", r, err)
	}
}

// TestIntegration_ClockSuspectFallback: untrusted PMS time -> conservative server-clock boundary, recorded
// clock-suspect in the audit; server time is not silently presented as PMS time.
func TestIntegration_ClockSuspectFallback(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true})
	act := time.Now().Add(-time.Hour)
	win := time.Now().Add(50 * time.Hour)
	seedEntitlement(t, p, f, &act, &win, "ACTIVE", nil, nil)

	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, Boundary{Trusted: false, UntrustedReason: "PMS_TIME_STALE"})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !res.BoundaryClockSuspect || !res.GraceCreated {
		t.Fatalf("expected clock-suspect grace: %+v", res)
	}
	var suspect, near bool
	if err := p.QueryRow(ctx, `SELECT boundary_clock_suspect, boundary_at > now() - interval '1 minute' FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, f.stay).Scan(&suspect, &near); err != nil {
		t.Fatal(err)
	}
	if !suspect || !near {
		t.Fatalf("audit must record clock-suspect server boundary: suspect=%v near-now=%v", suspect, near)
	}
}

// TestIntegration_EmergencyFallback: invalid/unconfigured Hotel-Admin policy -> versioned Emergency Grace via
// the canonical system package, independent of config, with a durable CHECKOUT_GRACE_CONFIG_INVALID alert.
func TestIntegration_EmergencyFallback(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	// unconfigured typed policy AND no grace package pinned — Emergency must STILL convert the eligible Guest.
	f := seedBase(t, p, seedOpts{configureTypedPolicy: false, pinGracePackage: false, systemGracePackage: true})
	b := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	act := b.Add(-30 * time.Minute)
	win := b.Add(50 * time.Hour)
	seedEntitlement(t, p, f, &act, &win, "ACTIVE", nil, nil)

	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(b))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !res.GraceCreated || res.Trigger != grace.TriggerEmergency || !res.IsEmergency || !res.ConfigInvalidAlert {
		t.Fatalf("expected emergency grace: %+v", res)
	}
	if liveOriginals(t, p, f.stay) != 0 {
		t.Fatal("emergency path must still leave no live pre-checkout entitlement")
	}
	// durable alert + emergency policy version + built-in emergency window (3600s), is_emergency entitlement
	var alert, polv string
	if err := p.QueryRow(ctx, `SELECT alert_code, policy_version FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, f.stay).Scan(&alert, &polv); err != nil {
		t.Fatal(err)
	}
	if alert != "CHECKOUT_GRACE_CONFIG_INVALID" || polv != "EMERGENCY_GRACE_V1" {
		t.Fatalf("audit alert/version wrong: %s/%s", alert, polv)
	}
	var isEm, windowOK bool
	if err := p.QueryRow(ctx, `SELECT is_emergency_grace, window_ends_at = $2::timestamptz + interval '3600 seconds' FROM iam_v2.entitlements WHERE id=$1`, res.NewEntitlementID, b).Scan(&isEm, &windowOK); err != nil {
		t.Fatal(err)
	}
	if !isEm || !windowOK {
		t.Fatalf("emergency entitlement wrong: emergency=%v windowOK=%v", isEm, windowOK)
	}
	// the canonical system emergency package now exists and is used (traceable, not a per-request fake)
	if n := count(t, p, `SELECT count(*) FROM iam_v2.internet_packages WHERE tenant_id=$1 AND site_id=$2 AND code='__sys_emergency_grace_pkg__' AND is_system`, f.tenant, f.site); n != 1 {
		t.Fatalf("canonical emergency package count = %d, want 1", n)
	}
}

// TestIntegration_InvalidConfiguredPackageRoutesEmergency: a configured grace package that fails item-6
// validation (non-system) routes the eligible Guest to Emergency.
func TestIntegration_InvalidConfiguredPackageRoutesEmergency(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: false})
	b := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	act := b.Add(-30 * time.Minute)
	win := b.Add(50 * time.Hour)
	seedEntitlement(t, p, f, &act, &win, "ACTIVE", nil, nil)

	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(b))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !res.GraceCreated || !res.IsEmergency {
		t.Fatalf("invalid configured package must route to Emergency: %+v", res)
	}
}

// TestIntegration_NoActiveEntitlement: checkout still commits, no grace, a NO_GRACE audit row, no live original.
func TestIntegration_NoActiveEntitlement(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true})
	b := time.Now().Add(-time.Hour).Truncate(time.Microsecond)

	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(b))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !res.CheckedOut || res.GraceCreated {
		t.Fatalf("no-entitlement stay: checkout only, got %+v", res)
	}
	if st, _, posting := stayState(t, p, f.stay); st != "CHECKED_OUT" || posting {
		t.Fatalf("stay must be CHECKED_OUT + posting off, got %s posting=%v", st, posting)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1 AND trigger='NO_GRACE'`, f.stay); n != 1 {
		t.Fatalf("NO_GRACE audit rows = %d, want 1", n)
	}
}

// TestIntegration_AuditAppendOnly: the audit row is immutable (no UPDATE/DELETE).
func TestIntegration_AuditAppendOnly(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true})
	b := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	act := b.Add(-30 * time.Minute)
	win := b.Add(50 * time.Hour)
	seedEntitlement(t, p, f, &act, &win, "ACTIVE", nil, nil)
	if _, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, trusted(b)); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.checkout_grace_audit SET reason_code='TAMPERED' WHERE stay_id=$1`, f.stay); err == nil {
		t.Fatal("audit UPDATE must be rejected (append-only)")
	}
	if _, err := p.Exec(ctx, `DELETE FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, f.stay); err == nil {
		t.Fatal("audit DELETE must be rejected (append-only)")
	}
}
