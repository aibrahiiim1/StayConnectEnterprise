//go:build integration

package checkout

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

// guarded runs fn in one transaction that first opens the controlled-operation families the controlled-writer
// triggers require, as the product's writers do. The error is returned so a test of a genuine refusal sees it
// for the right reason, not because the writer guard fired first.
func guarded(p *pgxpool.Pool, families []string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, f := range families {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, f); err != nil {
			return err
		}
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func guardedExec(p *pgxpool.Pool, families []string, q string, args ...any) error {
	return guarded(p, families, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, args...)
		return err
	})
}

// asOwnerExec runs one seed write as iam_v2_owner, only for owner-guarded tables
// (site_checkout_grace_config, accounting_records) where the seed needs a shape no operation produces. Never
// use it in a path under test or where a refusal is asserted.
func asOwnerExec(p *pgxpool.Pool, q string, args ...any) error {
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE iam_v2_owner`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, q, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var (
	stayFamily     = []string{"stay"}
	commerceFamily = []string{"commerce_intent"}
	alertFamily    = []string{"alert"}
	convFamily     = []string{"checkout_conversion"}
)

type fixture struct {
	tenant, site, iface, stay string
	gracePkgRev, svcRev       string
	boundary                  time.Time
}

type seedOpts struct {
	configureTypedPolicy bool
	pinGracePackage      bool
	systemGracePackage   bool   // grace package is is_system + current revision + plan matching typed policy
	mismatchField        string // when set, the plan revision differs from the typed policy in this one field
	bootstrapEmergency   bool   // pre-provision the canonical Emergency-Grace catalog
}

func mv(o seedOpts, field string, def int) int {
	if o.mismatchField == field {
		return def + 7
	}
	return def
}

// seedBase builds a site with a system CHECKOUT_GRACE package matching the typed policy (unless mismatchField
// differs), an IN_HOUSE Stay (episode 1, last_applied_event_version 5) and a grace config.
func seedBase(t *testing.T, p *pgxpool.Pool, o seedOpts) fixture {
	t.Helper()
	ctx := context.Background()
	var f fixture
	f.boundary = time.Now().Add(-2 * time.Hour).Truncate(time.Microsecond)
	sys := "true"
	if !o.systemGracePackage {
		sys = "false"
	}
	down := mv(o, "down", 4000)
	up := mv(o, "up", 1500)
	quota := int64(524288000)
	if o.mismatchField == "quota" {
		quota += 1024
	}
	devLim := mv(o, "devlim", 2)
	devPol := "REJECT_NEW_DEVICE"
	tam := "VALIDITY_WINDOW"
	if o.mismatchField == "tam" {
		tam = "AGGREGATE_ONLINE_TIME"
	}
	dur := 3600
	if o.mismatchField == "duration" {
		dur = 7200
	}
	durPolicy := fmt.Sprintf(`{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":%d,"policy_version":"CHECKOUT_GRACE_V1"}`, dur)

	err := guarded(p, stayFamily, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `WITH
	  t  AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM gen_random_uuid() g RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, gen_random_uuid() g RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id, 'protel-fias','ACTIVE' FROM si RETURNING id,tenant_id,site_id),
	  rt AS (INSERT INTO iam_v2.pms_interface_runtime(tenant_id,site_id,pms_interface_id,published_resync_generation,resync_generation_seq)
	         SELECT pi.tenant_id, pi.site_id, pi.id, 0, 0 FROM pi RETURNING pms_interface_id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, 'grace-plan', true FROM pi RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id, 1, $1, $2, $3, $4, $5, $6 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, 'grace-pkg', `+sys+` FROM pi RETURNING id,tenant_id,site_id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.id, 1, spr.id, 'CHECKOUT_GRACE', 0, ARRAY['NOT_REQUIRED']::text[], $7::jsonb FROM ip, spr RETURNING id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 'R1','R1','IN_HOUSE',1,5 FROM pi RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM st)::text, (SELECT id FROM ipr)::text, (SELECT id FROM spr)::text`,
			down, up, devLim, devPol, tam, quota, durPolicy).
			Scan(&f.tenant, &f.site, &f.iface, &f.stay, &f.gracePkgRev, &f.svcRev)
	})
	if err != nil {
		t.Fatalf("seed base: %v", err)
	}
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
		if err := asOwnerExec(p, `INSERT INTO iam_v2.site_checkout_grace_config
			(tenant_id,site_id,grace_package_revision_id,grace_duration_seconds,grace_down_kbps,grace_up_kbps,grace_data_quota_bytes,grace_device_limit,grace_device_limit_policy)
			VALUES ($1,$2,`+pkg+`,3600,4000,1500,524288000,2,'REJECT_NEW_DEVICE')`, args...); err != nil {
			t.Fatalf("seed grace config: %v", err)
		}
	} else {
		if err := asOwnerExec(p, `INSERT INTO iam_v2.site_checkout_grace_config (tenant_id,site_id,grace_package_revision_id) VALUES ($1,$2,`+pkg+`)`, args...); err != nil {
			t.Fatalf("seed grace config: %v", err)
		}
	}
	if o.bootstrapEmergency {
		if _, err := p.Exec(ctx, `SELECT iam_v2.bootstrap_emergency_grace($1,$2)`, f.tenant, f.site); err != nil {
			t.Fatalf("bootstrap emergency: %v", err)
		}
	}
	return f
}

// seedEvent inserts the checkout Stay Event (PENDING then APPLIED) and returns its id.
func seedEvent(t *testing.T, p *pgxpool.Pool, f fixture, ts *time.Time, suspect bool, seq int, admission string, resyncGen int, stayForApply string) string {
	t.Helper()
	if stayForApply == "" {
		stayForApply = f.stay
	}
	var eid string
	if err := guarded(p, stayFamily, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events
		(id,tenant_id,site_id,pms_interface_id,stay_id,external_event_identity,event_type,pms_timestamp_raw,pms_timestamp_utc,
		 source_timezone,sequence_version,normalization_version,clock_suspect,payload,processing_status,admission_kind,resync_generation)
		VALUES (gen_random_uuid(),$1,$2,$3,NULL,$4,'GO','x',$5,'UTC',$6,1,$7,'{}','PENDING',$8,$9) RETURNING id`,
			f.tenant, f.site, f.iface, fmt.Sprintf("EV-%d-%s", seq, admission), ts, seq, suspect, admission, resyncGen).Scan(&eid)
	}); err != nil {
		t.Fatalf("seed event insert: %v", err)
	}
	return eid
}

// applyEvent mirrors the Stay engine's finishEvent, including pinning stays.last_applied_event_id; an unpinned
// event is refused as an unverifiable boundary.
func applyEvent(t *testing.T, p *pgxpool.Pool, eid, stay string) {
	t.Helper()
	if err := guardedExec(p, stayFamily, `UPDATE iam_v2.stay_events SET stay_id=$2, processing_status='APPLIED', processed_at=now() WHERE id=$1`, eid, stay); err != nil {
		t.Fatalf("apply event: %v", err)
	}
	if err := guardedExec(p, stayFamily, `UPDATE iam_v2.stays SET last_applied_event_id=$1::uuid, last_applied_event_version=last_applied_event_version+1 WHERE id=$2`, eid, stay); err != nil {
		t.Fatalf("pin lineage: %v", err)
	}
}

func checkoutEvent(t *testing.T, p *pgxpool.Pool, f fixture) BoundarySource {
	eid := seedEvent(t, p, f, &f.boundary, false, 5, "LIVE", 0, "")
	applyEvent(t, p, eid, f.stay)
	return BoundarySource{StayEventID: eid}
}

type txn struct {
	state string
	at    time.Time
}

// seedEnt builds history through apply_entitlement_transition; the final status is txns[len-1].state.
func seedEnt(t *testing.T, p *pgxpool.Pool, f fixture, window *time.Time, txns []txn) string {
	t.Helper()
	ctx := context.Background()
	// Insert and initial transition must share one transaction: the deferred status/history coherence
	// constraint forbids committing an entitlement without its history.
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
		t.Fatal(err)
	}
	var purchaseID string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,$5,'ADMIN_GRANT',0,'GRANTED') RETURNING id`,
		f.tenant, f.site, f.gracePkgRev, f.iface, f.stay).Scan(&purchaseID); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	var ent string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,
		 time_accounting_mode,end_mode,status,window_ends_at)
		VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7,'VALIDITY_WINDOW','VALIDITY_WINDOW',$8,$9) RETURNING id`,
		f.tenant, f.site, f.stay, f.iface, purchaseID, f.svcRev, f.gracePkgRev, txns[0].state, window).Scan(&ent); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	for _, tr := range txns {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,$2,$3,'SEED')`, ent, tr.state, tr.at); err != nil {
			t.Fatalf("seed transition %s: %v", tr.state, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("seed entitlement commit: %v", err)
	}
	return ent
}

// seedDeviceAuth attaches a device with an authorization interval [authAt, deauthAt) + a session [startAt, endAt).
func seedDeviceAuth(t *testing.T, p *pgxpool.Pool, f fixture, ent string, idx int, authAt time.Time, deauthAt *time.Time, startAt time.Time, endAt *time.Time) (string, string) {
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
	if err := guardedExec(p, []string{"device_auth"}, `INSERT INTO iam_v2.entitlement_device_authorizations
		(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at,deauthorized_at)
		VALUES ($1,$2,$3,$4,1,$5,$6)`, f.tenant, f.site, ent, dev, authAt, deauthAt); err != nil {
		t.Fatalf("seed device auth: %v", err)
	}
	var sess string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(id,tenant_id,site_id,entitlement_id,device_id,state,started,ended)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,'active',$5,$6) RETURNING id`, f.tenant, f.site, ent, dev, startAt, endAt).Scan(&sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return dev, sess
}

func count(t *testing.T, p *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func liveOriginals(t *testing.T, p *pgxpool.Pool, stay string) int {
	return count(t, p, `SELECT count(*) FROM iam_v2.entitlements
		WHERE stay_id=$1 AND end_mode<>'GRACE_AFTER_CHECKOUT' AND status IN ('ACTIVE','PENDING','SUSPENDED')`, stay)
}

func ptr(tm time.Time) *time.Time { return &tm }

// sample inserts one accounting record for a session at an explicit sample time.
func sample(t *testing.T, p *pgxpool.Pool, f fixture, sess string, seq int, up, down int64, at time.Time) {
	t.Helper()
	if err := asOwnerExec(p, `INSERT INTO iam_v2.accounting_records
		(tenant_id,site_id,session_id,sample_seq,bytes_up,bytes_down,sampled_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		f.tenant, f.site, sess, seq, up, down, at); err != nil {
		t.Fatalf("sample: %v", err)
	}
}

func activeEnt(t *testing.T, p *pgxpool.Pool, f fixture) string {
	act := f.boundary.Add(-30 * time.Minute)
	win := f.boundary.Add(48 * time.Hour)
	return seedEnt(t, p, f, &win, []txn{{"ACTIVE", act}})
}

func TestIntegration_ConvertCreatesGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	ent := activeEnt(t, p, f)
	b := f.boundary
	seedDeviceAuth(t, p, f, ent, 1, b.Add(-time.Hour), nil, b.Add(-time.Hour), nil)                        // in-interval -> grandfather
	seedDeviceAuth(t, p, f, ent, 2, b.Add(-time.Hour), ptr(b.Add(time.Hour)), b.Add(-time.Hour), nil)      // deauth AFTER boundary -> grandfather
	seedDeviceAuth(t, p, f, ent, 3, b.Add(-2*time.Hour), ptr(b.Add(-time.Hour)), b.Add(-2*time.Hour), nil) // deauth BEFORE boundary -> excluded
	seedDeviceAuth(t, p, f, ent, 4, b.Add(time.Hour), nil, b.Add(time.Hour), nil)                          // auth AFTER boundary -> excluded

	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !res.GraceCreated || res.Trigger != grace.TriggerCheckoutGrace || res.IsEmergency {
		t.Fatalf("unexpected: %+v", res)
	}
	if res.DevicesGrandfathered != 2 || res.SessionsRebound != 2 {
		t.Fatalf("boundary snapshot = %d dev / %d sess, want 2/2", res.DevicesGrandfathered, res.SessionsRebound)
	}
	if res.BoundaryReason != "TRUSTED_PMS_CHECKOUT_TS" || res.BoundaryClockSuspect {
		t.Fatalf("boundary provenance wrong: %s suspect=%v", res.BoundaryReason, res.BoundaryClockSuspect)
	}
	if liveOriginals(t, p, f.stay) != 0 {
		t.Fatal("no live pre-checkout entitlement may remain")
	}
	// audit: config_version + boundary event recorded, trigger coherent
	var cfgVer int64
	var evID string
	if err := p.QueryRow(ctx, `SELECT config_version, boundary_event_id::text FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, f.stay).Scan(&cfgVer, &evID); err != nil {
		t.Fatal(err)
	}
	if cfgVer != 1 || evID == "" {
		t.Fatalf("audit provenance wrong: cfgVer=%d ev=%s", cfgVer, evID)
	}
}

// An ineligible non-terminal Entitlement (PENDING/SUSPENDED at the boundary) is still terminated by a committed
// checkout so it cannot activate later. ent_live_stay allows only one live row per Stay.
func TestIntegration_TerminatesNonTerminal(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()

	for _, st := range []string{"PENDING", "SUSPENDED"} {
		f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
		b := f.boundary
		win := b.Add(48 * time.Hour)
		var txns []txn
		if st == "PENDING" {
			txns = []txn{{"PENDING", b.Add(-time.Hour)}}
		} else {
			txns = []txn{{"ACTIVE", b.Add(-2 * time.Hour)}, {"SUSPENDED", b.Add(-90 * time.Minute)}}
		}
		seedEnt(t, p, f, &win, txns)
		res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
		if err != nil {
			t.Fatalf("%s convert: %v", st, err)
		}
		if res.GraceCreated || res.EntitlementsEnded != 1 {
			t.Fatalf("%s: want no grace + 1 terminated, got %+v", st, res)
		}
		if liveOriginals(t, p, f.stay) != 0 {
			t.Fatalf("%s: non-terminal state remained live after checkout", st)
		}
	}
}

func TestIntegration_EligibilityAtBoundary(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()

	run := func(name string, build func(f fixture), wantGrace, wantManual bool) {
		f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
		build(f)
		r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.GraceCreated != wantGrace || r.ManualReview != wantManual {
			t.Fatalf("%s: grace=%v manual=%v, want grace=%v manual=%v", name, r.GraceCreated, r.ManualReview, wantGrace, wantManual)
		}
		if liveOriginals(t, p, f.stay) != 0 {
			t.Fatalf("%s: live original remained", name)
		}
	}
	b0 := time.Now().Add(-2 * time.Hour).Truncate(time.Microsecond)
	// ACTIVE at boundary, SUSPENDED after -> eligible
	run("active-then-suspended", func(f fixture) {
		win := b0.Add(48 * time.Hour)
		seedEnt(t, p, f, &win, []txn{{"ACTIVE", b0.Add(-time.Hour)}, {"SUSPENDED", b0.Add(time.Hour)}})
	}, true, false)
	// SUSPENDED at boundary, reactivated after -> NOT eligible
	run("suspended-then-reactivated", func(f fixture) {
		win := b0.Add(48 * time.Hour)
		seedEnt(t, p, f, &win, []txn{{"ACTIVE", b0.Add(-2 * time.Hour)}, {"SUSPENDED", b0.Add(-time.Hour)}, {"ACTIVE", b0.Add(time.Hour)}})
	}, false, false)
	// PENDING at boundary, activated after -> NOT eligible
	run("pending-then-activated", func(f fixture) {
		win := b0.Add(48 * time.Hour)
		seedEnt(t, p, f, &win, []txn{{"PENDING", b0.Add(-time.Hour)}, {"ACTIVE", b0.Add(time.Hour)}})
	}, false, false)
	// ACTIVE only after boundary -> NOT eligible
	run("active-only-after", func(f fixture) {
		win := b0.Add(48 * time.Hour)
		seedEnt(t, p, f, &win, []txn{{"ACTIVE", b0.Add(time.Hour)}})
	}, false, false)
}

// Two entitlements ACTIVE at the boundary go to manual review: both terminated, no grace.
func TestIntegration_OverlappingActiveManualReview(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	b := f.boundary
	win := b.Add(48 * time.Hour)
	// ent_live_stay forbids two currently-live rows, so seed the one terminated after the boundary first.
	seedEnt(t, p, f, &win, []txn{{"ACTIVE", b.Add(-time.Hour)}, {"TERMINATED", b.Add(time.Hour)}})
	seedEnt(t, p, f, &win, []txn{{"ACTIVE", b.Add(-time.Hour)}})

	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if res.GraceCreated || !res.ManualReview {
		t.Fatalf("want manual review, no grace: %+v", res)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1 AND trigger='NO_GRACE'`, f.stay); n != 1 {
		t.Fatalf("NO_GRACE audit rows = %d, want 1", n)
	}
	if liveOriginals(t, p, f.stay) != 0 {
		t.Fatal("both originals must be terminated")
	}
}

func TestIntegration_QuotaWindowAtBoundary(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()

	// window elapsed before boundary -> ACTIVE per history but validity gone -> not eligible
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	b := f.boundary
	winPast := b.Add(-time.Minute)
	seedEnt(t, p, f, &winPast, []txn{{"ACTIVE", b.Add(-time.Hour)}})
	r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || r.GraceCreated {
		t.Fatalf("window-elapsed must be ineligible: %+v err=%v", r, err)
	}

	// data quota exhausted at boundary (ledger sum >= plan quota) -> not eligible
	f = seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	b = f.boundary
	win := b.Add(48 * time.Hour)
	ent := seedEnt(t, p, f, &win, []txn{{"ACTIVE", b.Add(-time.Hour)}})
	_, sess := seedDeviceAuth(t, p, f, ent, 1, b.Add(-time.Hour), nil, b.Add(-time.Hour), nil)
	// one accounting sample before the boundary that meets/exceeds the 500MB plan quota
	if err := asOwnerExec(p, `INSERT INTO iam_v2.accounting_records(tenant_id,site_id,session_id,sample_seq,bytes_up,bytes_down,sampled_at)
		VALUES ($1,$2,$3,1,524288000,1,$4)`, f.tenant, f.site, sess, b.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	r, err = c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || r.GraceCreated {
		t.Fatalf("quota-exhausted-at-boundary must be ineligible: %+v err=%v", r, err)
	}
}

func TestIntegration_BoundaryEventVerification(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()

	// unprocessed (PENDING) event -> reject
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)
	pend := seedEvent(t, p, f, &f.boundary, false, 5, "LIVE", 0, "")
	if _, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, BoundarySource{StayEventID: pend}); err != ErrInvalidBoundaryEvent {
		t.Fatalf("unprocessed event = %v, want ErrInvalidBoundaryEvent", err)
	}

	// Exact lineage: a different applied GO event is not a valid source once the Stay pins its applied event.
	f = seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)
	pinned := seedEvent(t, p, f, &f.boundary, false, 5, "LIVE", 0, "")
	applyEvent(t, p, pinned, f.stay)
	other := seedEvent(t, p, f, &f.boundary, false, 6, "LIVE", 0, "")
	applyEvent(t, p, other, f.stay)
	if err := guardedExec(p, stayFamily, `UPDATE iam_v2.stays SET last_applied_event_id=$2::uuid WHERE id=$1`, f.stay, pinned); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, BoundarySource{StayEventID: other}); err != ErrInvalidBoundaryEvent {
		t.Fatalf("event that is NOT the pinned last_applied_event_id = %v, want ErrInvalidBoundaryEvent", err)
	}
	// the pinned event IS accepted
	if r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, BoundarySource{StayEventID: pinned}); err != nil || !r.GraceCreated {
		t.Fatalf("pinned lineage event must be accepted: %+v err=%v", r, err)
	}

	// unpublished RESYNC (gen 5 > published 0) -> reject
	f = seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)
	rs := seedEvent(t, p, f, &f.boundary, false, 5, "RESYNC", 5, "")
	applyEvent(t, p, rs, f.stay)
	if _, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, BoundarySource{StayEventID: rs}); err != ErrInvalidBoundaryEvent {
		t.Fatalf("unpublished-resync event = %v, want ErrInvalidBoundaryEvent", err)
	}

	// cross-stay: event applied to a different stay on the same interface -> reject for our stay
	f = seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)
	var otherStay string
	if err := guarded(p, stayFamily, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version)
		VALUES (gen_random_uuid(),$1,$2,$3,'R2','R2','IN_HOUSE',1,5) RETURNING id`, f.tenant, f.site, f.iface).Scan(&otherStay)
	}); err != nil {
		t.Fatal(err)
	}
	xev := seedEvent(t, p, f, &f.boundary, false, 5, "LIVE", 0, "")
	applyEvent(t, p, xev, otherStay)
	if _, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, BoundarySource{StayEventID: xev}); err != ErrInvalidBoundaryEvent {
		t.Fatalf("cross-stay event = %v, want ErrInvalidBoundaryEvent", err)
	}

	// future timestamp (beyond skew) -> conservative clock-suspect fallback (still converts)
	f = seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)
	future := time.Now().Add(48 * time.Hour)
	fev := seedEvent(t, p, f, &future, false, 5, "LIVE", 0, "")
	applyEvent(t, p, fev, f.stay)
	r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, BoundarySource{StayEventID: fev})
	if err != nil {
		t.Fatalf("future-ts convert: %v", err)
	}
	if !r.BoundaryClockSuspect || r.BoundaryReason != "PMS_TIME_IMPLAUSIBLE_FUTURE" {
		t.Fatalf("future timestamp must fall back clock-suspect, got suspect=%v reason=%s", r.BoundaryClockSuspect, r.BoundaryReason)
	}
}

func TestIntegration_EmergencyReadsCatalog(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()

	// invalid config + bootstrapped catalog -> emergency grace via canonical package
	f := seedBase(t, p, seedOpts{configureTypedPolicy: false, pinGracePackage: false, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)
	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil {
		t.Fatalf("emergency convert: %v", err)
	}
	if !res.IsEmergency || !res.GraceCreated || !res.ConfigInvalidAlert {
		t.Fatalf("want emergency grace: %+v", res)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.active_operational_alerts WHERE stay_id=$1 AND alert_code='CHECKOUT_GRACE_CONFIG_INVALID'`, f.stay); n != 1 {
		t.Fatalf("operational alert rows = %d, want 1", n)
	}

	// invalid config + NO catalog -> fail closed
	f = seedBase(t, p, seedOpts{configureTypedPolicy: false, pinGracePackage: false, systemGracePackage: true, bootstrapEmergency: false})
	activeEnt(t, p, f)
	if _, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f)); err != ErrEmergencyCatalogUnavailable {
		t.Fatalf("absent catalog = %v, want ErrEmergencyCatalogUnavailable", err)
	}

	// poisoned reserved code: a non-system package with the reserved code is rejected by the namespace trigger
	f = seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code,is_system) VALUES ($1,$2,'__sys_emergency_grace_pkg__',false)`, f.tenant, f.site); err == nil {
		t.Fatal("non-system reserved-code package must be rejected")
	}
	if _, err := p.Exec(ctx, `DELETE FROM iam_v2.internet_packages WHERE tenant_id=$1 AND site_id=$2 AND code='__sys_emergency_grace_pkg__'`, f.tenant, f.site); err == nil {
		t.Fatal("reserved-code package delete must be rejected")
	}
}

// An Emergency conversion opens an alert (OPEN/ACKNOWLEDGED active, RESOLVED not; evidence immutable), and a
// mismatched boundary event is rejected by the provenance guard.
func TestIntegration_AlertLifecycleAndProvenance(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: false, pinGracePackage: false, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)
	res, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.IsEmergency {
		t.Fatalf("emergency convert: %+v err=%v", res, err)
	}
	var auditID string
	if err := p.QueryRow(ctx, `SELECT id::text FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, f.stay).Scan(&auditID); err != nil {
		t.Fatal(err)
	}
	// The alert opens with the audit row in the same transaction, or it would be invisible to the operator queue.
	if count(t, p, `SELECT count(*) FROM iam_v2.checkout_grace_alert_actions
		WHERE audit_id=$1 AND seq=1 AND action='OPEN'`, auditID) != 1 {
		t.Fatal("an alert-bearing audit must open its own lifecycle")
	}
	if count(t, p, `SELECT count(*) FROM iam_v2.active_operational_alerts WHERE audit_id=$1 AND alert_state='OPEN'`, auditID) != 1 {
		t.Fatal("emergency alert must be active and OPEN before resolution")
	}
	// RESOLVED still requires an actor, and a second OPEN is not a legal edge
	actor := "11111111-1111-1111-1111-111111111111"
	if err := guardedExec(p, alertFamily, `INSERT INTO iam_v2.checkout_grace_alert_actions(tenant_id,site_id,audit_id,seq,action) VALUES ($1,$2,$3,2,'RESOLVED')`, f.tenant, f.site, auditID); err == nil {
		t.Fatal("RESOLVED needs an actor")
	}
	if err := guardedExec(p, alertFamily, `INSERT INTO iam_v2.checkout_grace_alert_actions(tenant_id,site_id,audit_id,seq,action) VALUES ($1,$2,$3,2,'OPEN')`, f.tenant, f.site, auditID); err == nil {
		t.Fatal("OPEN -> OPEN is not a legal edge")
	}
	if err := guardedExec(p, alertFamily, `INSERT INTO iam_v2.checkout_grace_alert_actions(tenant_id,site_id,audit_id,seq,action,actor) VALUES ($1,$2,$3,2,'ACKNOWLEDGED',$4)`, f.tenant, f.site, auditID, actor); err != nil {
		t.Fatal(err)
	}
	if count(t, p, `SELECT count(*) FROM iam_v2.active_operational_alerts WHERE audit_id=$1`, auditID) != 1 {
		t.Fatal("ACKNOWLEDGED alert must stay active")
	}
	if err := guardedExec(p, alertFamily, `INSERT INTO iam_v2.checkout_grace_alert_actions(tenant_id,site_id,audit_id,seq,action,actor,reason_code) VALUES ($1,$2,$3,3,'RESOLVED',$4,'CATALOG_FIXED')`, f.tenant, f.site, auditID, actor); err != nil {
		t.Fatal(err)
	}
	if count(t, p, `SELECT count(*) FROM iam_v2.active_operational_alerts WHERE audit_id=$1`, auditID) != 0 {
		t.Fatal("RESOLVED alert must leave the active view")
	}
	// evidence immutable + a resolved alert is terminal
	if err := guardedExec(p, alertFamily, `UPDATE iam_v2.checkout_grace_alert_actions SET action='OPEN' WHERE audit_id=$1`, auditID); err == nil {
		t.Fatal("alert actions are append-only")
	}
	if err := guardedExec(p, alertFamily, `INSERT INTO iam_v2.checkout_grace_alert_actions(tenant_id,site_id,audit_id,seq,action,actor) VALUES ($1,$2,$3,4,'ACKNOWLEDGED',$4)`, f.tenant, f.site, auditID, actor); err == nil {
		t.Fatal("a RESOLVED alert is terminal")
	}

	// provenance: an audit row whose boundary_event_id belongs to a DIFFERENT stay is rejected.
	f2 := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	wrongEv := seedEvent(t, p, f2, &f2.boundary, false, 5, "LIVE", 0, "")
	applyEvent(t, p, wrongEv, f2.stay)
	// craft an audit for f2.stay but citing wrongEv with a MISMATCHED seq → provenance guard rejects
	// The family is opened so the refusal is the provenance guard, not the writer guard.
	if err := guardedExec(p, convFamily, `INSERT INTO iam_v2.checkout_grace_audit
		(tenant_id,site_id,pms_interface_id,stay_id,lifecycle_version,trigger,is_emergency,policy_version,reason_code,
		 boundary_event_id,boundary_event_seq,boundary_normalization_version,boundary_reason_code,config_version,boundary_at)
		VALUES ($1,$2,$3,$4,1,'NO_GRACE',false,'NONE','X',$5,999,1,'TRUSTED_PMS_CHECKOUT_TS',1,now())`,
		f2.tenant, f2.site, f2.iface, f2.stay, wrongEv); err == nil {
		t.Fatal("audit with a mismatched boundary event seq must be rejected by the provenance guard")
	}
}

// Any single field mismatch between the site policy and the plan revision routes to Emergency.
func TestIntegration_ExactPolicyEqualityRoutesEmergency(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()
	for _, field := range []string{"down", "up", "quota", "devlim", "duration"} {
		f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true, mismatchField: field})
		activeEnt(t, p, f)
		r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
		if err != nil {
			t.Fatalf("%s convert: %v", field, err)
		}
		if !r.IsEmergency {
			t.Fatalf("mismatch field %q must route to Emergency, got %+v", field, r)
		}
	}
}

func TestIntegration_IdempotentAndConcurrent(t *testing.T) {
	p := pool(t)
	defer p.Close()
	c := NewConverter(p)
	ctx := context.Background()

	// idempotent
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)
	src := checkoutEvent(t, p, f)
	if _, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, src); err != nil {
		t.Fatalf("first: %v", err)
	}
	var effco1 time.Time
	_ = p.QueryRow(ctx, `SELECT effective_checkout_at FROM iam_v2.stays WHERE id=$1`, f.stay).Scan(&effco1)
	r2, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, src)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !r2.AlreadyProcessed || r2.GraceCreated {
		t.Fatalf("second must be idempotent: %+v", r2)
	}
	var effco2 time.Time
	_ = p.QueryRow(ctx, `SELECT effective_checkout_at FROM iam_v2.stays WHERE id=$1`, f.stay).Scan(&effco2)
	if !effco1.Equal(effco2) {
		t.Fatal("boundary moved on re-entry")
	}

	// concurrent
	f = seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)
	src = checkoutEvent(t, p, f)
	const n = 24
	var wins int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.ConvertAtCheckout(context.Background(), f.tenant, f.site, f.iface, f.stay, src)
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
	if liveOriginals(t, p, f.stay) != 0 {
		t.Fatal("no live original may remain after concurrent checkout")
	}
}

// Termination keeps the true boundary as effective_at (never clamped forward) even when a later fact exists;
// that fact is invalidated, not deleted, and recorded_at stays distinct from effective_at.
func TestIntegration_BoundaryTerminationIsNotClamped(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	win := f.boundary.Add(48 * time.Hour)
	// ACTIVE before the boundary, then SUSPENDED an hour AFTER it (a fact recorded for a period the guest had
	// already checked out of).
	ent := seedEnt(t, p, f, &win, []txn{{"ACTIVE", f.boundary.Add(-time.Hour)}, {"SUSPENDED", f.boundary.Add(time.Hour)}})

	if _, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f)); err != nil {
		t.Fatal(err)
	}

	var termAt, recAt time.Time
	var termID string
	if err := p.QueryRow(ctx, `SELECT id::text, effective_at, recorded_at FROM iam_v2.entitlement_state_transitions
		WHERE entitlement_id=$1 AND to_state='TERMINATED' AND superseded_by IS NULL`, ent).Scan(&termID, &termAt, &recAt); err != nil {
		t.Fatal(err)
	}
	if !termAt.Equal(f.boundary) {
		t.Fatalf("termination effective_at %v != boundary %v (it must NOT be clamped forward)", termAt, f.boundary)
	}
	if !recAt.After(termAt) {
		t.Fatalf("recorded_at %v must be the system time, distinct from the business time %v", recAt, termAt)
	}
	// the post-boundary SUSPENDED fact is invalidated by the termination, and still readable
	var supBy *string
	if err := p.QueryRow(ctx, `SELECT superseded_by::text FROM iam_v2.entitlement_state_transitions
		WHERE entitlement_id=$1 AND to_state='SUSPENDED'`, ent).Scan(&supBy); err != nil {
		t.Fatal(err)
	}
	if supBy == nil || *supBy != termID {
		t.Fatalf("post-boundary SUSPENDED fact was not invalidated by the boundary termination (superseded_by=%v)", supBy)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_state_transitions WHERE entitlement_id=$1`, ent); n != 3 {
		t.Fatalf("history rows = %d, want 3 (nothing deleted)", n)
	}
	// the entitlement's derived terminal time follows the LIVE chain
	var terminated time.Time
	if err := p.QueryRow(ctx, `SELECT terminated_at FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&terminated); err != nil {
		t.Fatal(err)
	}
	if !terminated.Equal(f.boundary) {
		t.Fatalf("terminated_at %v != boundary %v", terminated, f.boundary)
	}
}

// No access survives the boundary outside the grace cohort: a cohort device is rebound without a logout, a
// device authorized only after the boundary is cut at it, and the original is left with nothing live.
func TestIntegration_PostBoundaryRevocation(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	b := f.boundary
	ent := activeEnt(t, p, f)
	// cohort device: authorized before the boundary, still open, with a live session started before it
	inDev, inSess := seedDeviceAuth(t, p, f, ent, 1, b.Add(-time.Hour), nil, b.Add(-time.Hour), nil)
	// outsider: authorized only AFTER the boundary, with a session that also started after it
	outDev, outSess := seedDeviceAuth(t, p, f, ent, 2, b.Add(time.Hour), nil, b.Add(time.Hour), nil)

	r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil {
		t.Fatal(err)
	}
	if !r.GraceCreated || r.DevicesGrandfathered != 1 || r.SessionsRebound != 1 {
		t.Fatalf("grace=%v grandfathered=%d rebound=%d, want true/1/1", r.GraceCreated, r.DevicesGrandfathered, r.SessionsRebound)
	}
	// the cohort device keeps access on the GRACE entitlement, its session never ended
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND entitlement_id=$2 AND state='active' AND ended IS NULL`,
		inSess, r.NewEntitlementID); n != 1 {
		t.Fatal("the grandfathered device's session was not rebound without a logout")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		WHERE entitlement_id=$1 AND device_id=$2 AND deauthorized_at IS NULL`, r.NewEntitlementID, inDev); n != 1 {
		t.Fatal("the grandfathered device has no open grace authorization interval")
	}
	// the outsider is revoked AT the boundary — no open interval, session ended with a bounded reason
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		WHERE entitlement_id=$1 AND device_id=$2 AND deauthorized_at IS NOT NULL`, ent, outDev); n != 1 {
		t.Fatal("the post-boundary device's interval was not closed")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state='ended' AND end_reason='CHECKOUT_BOUNDARY'`, outSess); n != 1 {
		t.Fatal("the post-boundary device's session was not revoked")
	}
	// the ORIGINAL entitlement keeps nothing live
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		WHERE entitlement_id=$1 AND deauthorized_at IS NULL`, ent); n != 0 {
		t.Fatalf("the terminated entitlement still has %d open authorization intervals", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE entitlement_id=$1 AND state='active'`, ent); n != 0 {
		t.Fatal("the terminated entitlement still has a live session")
	}
	if r.DevicesRevoked < 1 || r.SessionsRevoked < 1 {
		t.Fatalf("revocation counts devices=%d sessions=%d, want >=1 each", r.DevicesRevoked, r.SessionsRevoked)
	}
}

// Revocation also runs when no grace is created, so an ineligible Stay cannot keep browsing after checkout.
func TestIntegration_RevocationWithoutGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	b := f.boundary
	win := b.Add(48 * time.Hour)
	// SUSPENDED at the boundary -> not eligible -> no grace
	ent := seedEnt(t, p, f, &win, []txn{{"ACTIVE", b.Add(-2 * time.Hour)}, {"SUSPENDED", b.Add(-time.Hour)}})
	dev, sess := seedDeviceAuth(t, p, f, ent, 1, b.Add(-2*time.Hour), nil, b.Add(-2*time.Hour), nil)

	r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil {
		t.Fatal(err)
	}
	if r.GraceCreated {
		t.Fatal("no grace should have been created for a boundary-ineligible entitlement")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		WHERE entitlement_id=$1 AND device_id=$2 AND deauthorized_at IS NOT NULL`, ent, dev); n != 1 {
		t.Fatal("device interval not closed on the no-grace path")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state='ended' AND end_reason='CHECKOUT_BOUNDARY'`, sess); n != 1 {
		t.Fatal("session not revoked on the no-grace path")
	}
}

// Usage is attributed by binding interval, the decision's evidence is frozen in a watermark, and a late sample
// for the frozen period is recorded as DELAYED without rewriting it.
func TestIntegration_BoundaryWatermarkAndDelayedAccounting(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	b := f.boundary
	ent := activeEnt(t, p, f)
	_, sess := seedDeviceAuth(t, p, f, ent, 1, b.Add(-2*time.Hour), nil, b.Add(-2*time.Hour), nil)
	sample(t, p, f, sess, 1, 1_000, 2_000, b.Add(-time.Hour)) // real pre-boundary usage

	r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil {
		t.Fatal(err)
	}
	if !r.GraceCreated || r.SessionsRebound != 1 {
		t.Fatalf("grace=%v rebound=%d, want true/1", r.GraceCreated, r.SessionsRebound)
	}
	// the session now points at the grace entitlement, but its PRE-boundary binding is preserved
	if n := count(t, p, `SELECT count(*) FROM iam_v2.session_entitlement_bindings
		WHERE session_id=$1 AND entitlement_id=$2 AND bound_until IS NOT NULL`, sess, ent); n != 1 {
		t.Fatal("the original binding interval was not closed and kept")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.session_entitlement_bindings
		WHERE session_id=$1 AND entitlement_id=$2 AND bound_until IS NULL`, sess, r.NewEntitlementID); n != 1 {
		t.Fatal("no open binding interval on the grace entitlement")
	}
	// attribution: the pre-boundary sample belongs to the ORIGINAL, never to the grace entitlement
	var up, down int64
	if err := p.QueryRow(ctx, `SELECT bytes_up, bytes_down FROM iam_v2.entitlement_usage_bytes($1,now())`, ent).Scan(&up, &down); err != nil {
		t.Fatal(err)
	}
	if up != 1_000 || down != 2_000 {
		t.Fatalf("original usage = %d/%d, want 1000/2000", up, down)
	}
	if err := p.QueryRow(ctx, `SELECT bytes_up, bytes_down FROM iam_v2.entitlement_usage_bytes($1,now())`, r.NewEntitlementID).Scan(&up, &down); err != nil {
		t.Fatal(err)
	}
	if up != 0 || down != 0 {
		t.Fatalf("grace usage = %d/%d, want 0/0 (pre-boundary samples must not follow the session)", up, down)
	}
	// the decision's evidence is frozen
	var wmUp, wmDown, wmRecs int64
	var wmID string
	if err := p.QueryRow(ctx, `SELECT id::text, bytes_up, bytes_down, records_counted
		FROM iam_v2.entitlement_boundary_watermarks WHERE entitlement_id=$1 AND boundary_at=$2`, ent, b).Scan(&wmID, &wmUp, &wmDown, &wmRecs); err != nil {
		t.Fatalf("no boundary watermark: %v", err)
	}
	if wmUp != 1_000 || wmDown != 2_000 || wmRecs != 1 {
		t.Fatalf("watermark %d/%d over %d records, want 1000/2000 over 1", wmUp, wmDown, wmRecs)
	}
	// a LATE sample for the frozen period: recorded as delayed, watermark untouched
	sample(t, p, f, sess, 2, 500, 700, b.Add(-30*time.Minute))
	if n := count(t, p, `SELECT count(*) FROM iam_v2.delayed_accounting_records
		WHERE watermark_id=$1 AND entitlement_id=$2`, wmID, ent); n != 1 {
		t.Fatal("the late sample was not recorded as delayed")
	}
	var stillUp, stillDown int64
	if err := p.QueryRow(ctx, `SELECT bytes_up, bytes_down FROM iam_v2.entitlement_boundary_watermarks WHERE id=$1`, wmID).Scan(&stillUp, &stillDown); err != nil {
		t.Fatal(err)
	}
	if stillUp != wmUp || stillDown != wmDown {
		t.Fatal("a late sample rewrote the frozen watermark")
	}
	// the watermark itself is immutable
	// the 'checkout_conversion' family is opened so the refusal is the append-only guard, not the writer guard
	if err := guardedExec(p, convFamily, `UPDATE iam_v2.entitlement_boundary_watermarks SET bytes_up=999 WHERE id=$1`, wmID); err == nil {
		t.Fatal("watermark must be append-only")
	}
	// a sample taken AFTER the boundary on the grace entitlement is ordinary usage, not delayed
	sample(t, p, f, sess, 3, 10, 20, b.Add(time.Hour))
	if n := count(t, p, `SELECT count(*) FROM iam_v2.delayed_accounting_records WHERE session_id=$1`, sess); n != 1 {
		t.Fatal("a post-boundary sample must not be flagged delayed")
	}
	if err := p.QueryRow(ctx, `SELECT bytes_up, bytes_down FROM iam_v2.entitlement_usage_bytes($1,now())`, r.NewEntitlementID).Scan(&up, &down); err != nil {
		t.Fatal(err)
	}
	if up != 10 || down != 20 {
		t.Fatalf("grace usage after the boundary = %d/%d, want 10/20", up, down)
	}
}
