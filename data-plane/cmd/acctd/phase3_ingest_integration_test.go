//go:build integration

package main

// Composition-root tests for Phase-3 accounting ingestion, with non-zero synthetic counter deltas driven
// through the SAME entry point acctd's tick uses, against a real PostgreSQL 16. These cover the cases where
// usage is easy to lose, double-count or attribute to the wrong Entitlement.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

type ingestFixture struct {
	pool                          *pgxpool.Pool
	p3                            *phase3
	tenant, site, iface, stay     string
	device, device2, ent, session string
	svcRev, pkgRev                string
}

func newIngest(t *testing.T, quota int64) *ingestFixture {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping acctd ingestion integration")
	}
	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)

	f := &ingestFixture{pool: p}
	if err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'protel-fias','ACTIVE' FROM si RETURNING id,tenant_id,site_id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,'RACC','SACC','IN_HOUSE',1,0 FROM pi RETURNING id),
	  d1 AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, gen_random_uuid(),'02:00:00:00:40:01'::macaddr FROM pi RETURNING id),
	  d2 AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, gen_random_uuid(),'02:00:00:00:40:02'::macaddr FROM pi RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'acct-plan',true FROM pi RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id,1,8000,3000,4,'REJECT_NEW_DEVICE','VALIDITY_WINDOW',$1 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'acct-pkg',false,true FROM pi RETURNING id,tenant_id,site_id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.id,1,spr.id,'FREE_STAY',0,ARRAY['NOT_REQUIRED']::text[],'{}'::jsonb FROM ip, spr RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM st)::text, (SELECT id FROM d1)::text, (SELECT id FROM d2)::text,
	       (SELECT id FROM spr)::text, (SELECT id FROM ipr)::text`, quota).
		Scan(&f.tenant, &f.site, &f.iface, &f.stay, &f.device, &f.device2, &f.svcRev, &f.pkgRev); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f.p3 = &phase3{cfg: iamv2.PMSConfig{MasterEnabled: true, CheckoutGraceEnabled: true},
		enf: enforce.New(p), tenant: f.tenant, site: f.site}
	return f
}

// grantEntitlement creates an ACTIVE entitlement with its initial history.
func (f *ingestFixture) grantEntitlement(t *testing.T, activatedAt time.Time, window *time.Time) string {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var pur, ent string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,$5,'ADMIN_GRANT',0,'GRANTED') RETURNING id::text`,
		f.tenant, f.site, f.pkgRev, f.iface, f.stay).Scan(&pur); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,
		 time_accounting_mode,end_mode,status,window_ends_at)
		VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7,'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE',$8) RETURNING id::text`,
		f.tenant, f.site, f.stay, f.iface, pur, f.svcRev, f.pkgRev, window).Scan(&ent); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',$2,'GRANT')`, ent, activatedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return ent
}

func (f *ingestFixture) openSession(t *testing.T, ent, device string, startedAt time.Time, ip string) string {
	t.Helper()
	var sess string
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO iam_v2.sessions
		(tenant_id,site_id,entitlement_id,device_id,state,started,ip) VALUES ($1,$2,$3,$4,'active',$5,$6::inet)
		RETURNING id::text`, f.tenant, f.site, ent, device, startedAt, ip).Scan(&sess); err != nil {
		t.Fatalf("open session: %v", err)
	}
	return sess
}

func (f *ingestFixture) usage(t *testing.T, ent string) (up, down int64) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT bytes_up, bytes_down FROM iam_v2.entitlement_usage_bytes($1,now())`, ent).Scan(&up, &down); err != nil {
		t.Fatal(err)
	}
	return
}

func (f *ingestFixture) countRows(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// An ordinary active Entitlement: a non-zero delta is attributed to it, sampled_at is preserved separately
// from ingested_at, and the session counters advance.
func TestIntegration_Acct_OrdinaryActiveEntitlement(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-2 * time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, ent, f.device, started, "10.9.0.1")

	sampledAt := time.Now().Add(-30 * time.Minute).Truncate(time.Microsecond)
	class, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 1}, 1500, 2500, sampledAt)
	if err != nil || class != "ACCEPTED" {
		t.Fatalf("ingest: %v / %s", err, class)
	}
	up, down := f.usage(t, ent)
	if up != 1500 || down != 2500 {
		t.Fatalf("attributed usage = %d/%d, want 1500/2500", up, down)
	}
	var storedSampled, storedIngested time.Time
	if err := f.pool.QueryRow(ctx, `SELECT sampled_at, ingested_at FROM iam_v2.accounting_records
		WHERE session_id=$1 AND sample_seq=1`, sess).Scan(&storedSampled, &storedIngested); err != nil {
		t.Fatal(err)
	}
	if !storedSampled.Equal(sampledAt) {
		t.Fatalf("sampled_at = %v, want the measurement time %v", storedSampled, sampledAt)
	}
	if !storedIngested.After(storedSampled) {
		t.Fatal("ingested_at must be the arrival time, distinct from the measurement time")
	}
	var sUp, sDown int64
	if err := f.pool.QueryRow(ctx, `SELECT bytes_up, bytes_down FROM iam_v2.sessions WHERE id=$1`, sess).Scan(&sUp, &sDown); err != nil {
		t.Fatal(err)
	}
	if sUp != 1500 || sDown != 2500 {
		t.Fatalf("session counters = %d/%d, want the accepted delta", sUp, sDown)
	}
}

// Two devices sharing one Entitlement: both sessions' usage lands on the same Entitlement and is summed, not
// multiplied or split.
func TestIntegration_Acct_MultipleDevicesShareAnEntitlement(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-2 * time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	s1 := f.openSession(t, ent, f.device, started, "10.9.0.1")
	s2 := f.openSession(t, ent, f.device2, started, "10.9.0.2")

	at := time.Now().Add(-10 * time.Minute)
	if _, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: s1, Seq: 1}, 100, 200, at); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: s2, Seq: 1}, 300, 400, at); err != nil {
		t.Fatal(err)
	}
	up, down := f.usage(t, ent)
	if up != 400 || down != 600 {
		t.Fatalf("shared usage = %d/%d, want 400/600 (summed across devices)", up, down)
	}
}

// The case the whole binding model exists for: a sample TAKEN before the Checkout boundary but INGESTED after
// the session was rebound to Grace must be attributed to the ORIGINAL entitlement and flagged delayed — never
// folded into the frozen decision or charged to the grace entitlement.
func TestIntegration_Acct_LateSampleFromBeforeTheBoundary(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-3 * time.Hour)
	original := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, original, f.device, started, "10.9.0.1")
	boundary := time.Now().Add(-time.Hour).Truncate(time.Microsecond)

	// usage before the boundary, ingested on time
	if _, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 1}, 1000, 1000, boundary.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// freeze the decision at the boundary
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.entitlement_boundary_watermarks
		(tenant_id,site_id,entitlement_id,boundary_at,bytes_up,bytes_down,records_counted)
		SELECT $1,$2,$3,$4,u.bytes_up,u.bytes_down,u.records FROM iam_v2.entitlement_usage_bytes($3,$4) u`,
		f.tenant, f.site, original, boundary); err != nil {
		t.Fatal(err)
	}
	// terminate the original and rebind the session to a grace entitlement, as the conversion does
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.terminate_entitlement_at_boundary($1,$2,'CHECKOUT')`, original, boundary); err != nil {
		t.Fatal(err)
	}
	grace := f.grantEntitlement(t, boundary, nil)
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.rebind_session_entitlement($1,$2,$3)`, sess, grace, boundary); err != nil {
		t.Fatal(err)
	}

	// NOW a sample arrives that was measured BEFORE the boundary
	class, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 2}, 500, 700, boundary.Add(-10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if class != "DELAYED" {
		t.Fatalf("classification = %s, want DELAYED", class)
	}
	// it belongs to the ORIGINAL entitlement, not to grace
	origUp, origDown := f.usage(t, original)
	if origUp != 1500 || origDown != 1700 {
		t.Fatalf("original usage = %d/%d, want 1500/1700 (the late sample belongs here)", origUp, origDown)
	}
	graceUp, graceDown := f.usage(t, grace)
	if graceUp != 0 || graceDown != 0 {
		t.Fatalf("grace usage = %d/%d, want 0/0 — pre-boundary usage must not follow the session", graceUp, graceDown)
	}
	// and the frozen decision is untouched
	var wmUp, wmDown int64
	if err := f.pool.QueryRow(ctx, `SELECT bytes_up, bytes_down FROM iam_v2.entitlement_boundary_watermarks
		WHERE entitlement_id=$1`, original).Scan(&wmUp, &wmDown); err != nil {
		t.Fatal(err)
	}
	if wmUp != 1000 || wmDown != 1000 {
		t.Fatalf("the watermark moved to %d/%d — a late sample rewrote a frozen decision", wmUp, wmDown)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.delayed_accounting_records WHERE entitlement_id=$1`, original); n != 1 {
		t.Fatalf("delayed records = %d, want 1", n)
	}
}

// A sample measured AFTER the rebinding is ordinary grace usage.
func TestIntegration_Acct_SampleAfterRebindingIsGraceUsage(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-3 * time.Hour)
	original := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, original, f.device, started, "10.9.0.1")
	boundary := time.Now().Add(-2 * time.Hour).Truncate(time.Microsecond)
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.terminate_entitlement_at_boundary($1,$2,'CHECKOUT')`, original, boundary); err != nil {
		t.Fatal(err)
	}
	grace := f.grantEntitlement(t, boundary, nil)
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.rebind_session_entitlement($1,$2,$3)`, sess, grace, boundary); err != nil {
		t.Fatal(err)
	}
	class, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 1}, 42, 84, time.Now().Add(-time.Minute))
	if err != nil || class != "ACCEPTED" {
		t.Fatalf("ingest: %v / %s", err, class)
	}
	up, down := f.usage(t, grace)
	if up != 42 || down != 84 {
		t.Fatalf("grace usage = %d/%d, want 42/84", up, down)
	}
	if o1, o2 := f.usage(t, original); o1 != 0 || o2 != 0 {
		t.Fatalf("original usage = %d/%d, want 0/0", o1, o2)
	}
}

// Replaying the same sample identity (a retry, a restart mid-tick, a duplicated delivery) stores it once.
func TestIntegration_Acct_DuplicateSampleReplay(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, ent, f.device, started, "10.9.0.1")
	at := time.Now().Add(-10 * time.Minute)

	first, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 7}, 900, 100, at)
	if err != nil || first != "ACCEPTED" {
		t.Fatalf("first: %v / %s", err, first)
	}
	second, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 7}, 900, 100, at)
	if err != nil {
		t.Fatal(err)
	}
	if second != "DUPLICATE" {
		t.Fatalf("replay classification = %s, want DUPLICATE", second)
	}
	up, down := f.usage(t, ent)
	if up != 900 || down != 100 {
		t.Fatalf("usage after replay = %d/%d — a replayed sample was counted twice", up, down)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); n != 1 {
		t.Fatalf("stored samples = %d, want 1", n)
	}
}

// Every fail-closed case: no binding at sample time, a session from another scope, a negative delta (a
// counter regression whose baseline is wrong), and a sample that predates its own session.
func TestIntegration_Acct_FailsClosed(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, ent, f.device, started, "10.9.0.1")

	// a sample from BEFORE any binding existed has no owner
	if _, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 1}, 10, 10, started.Add(-time.Hour)); err == nil ||
		!strings.Contains(err.Error(), "ACCT_INVALID") {
		t.Fatalf("a sample predating the session was accepted: %v", err)
	}
	// counter regression
	if _, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 2}, -5, 10, time.Now()); err == nil ||
		!strings.Contains(err.Error(), "ACCT_COUNTER_REGRESSION") {
		t.Fatalf("a negative delta was accepted: %v", err)
	}
	// another tenant's session
	other := newIngest(t, 0)
	otherEnt := other.grantEntitlement(t, started, nil)
	otherSess := other.openSession(t, otherEnt, other.device, started, "10.9.0.5")
	if _, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: otherSess, Seq: 1}, 10, 10, time.Now()); err == nil ||
		!strings.Contains(err.Error(), "ACCT_SESSION_OUT_OF_SCOPE") {
		t.Fatalf("a cross-scope session was accepted: %v", err)
	}
	// a sample measured AFTER the session ended has no binding covering it — ending a session closes its
	// attribution interval, so there is no Entitlement that owns that period and guessing one would charge
	// usage to whoever happened to be bound most recently.
	endedAt := time.Now().Add(-5 * time.Minute)
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended', ended=$2, end_reason='TEST' WHERE id=$1`,
		sess, endedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 3}, 10, 10, endedAt.Add(time.Minute)); err == nil ||
		!strings.Contains(err.Error(), "ACCT_NO_BINDING") {
		t.Fatalf("a sample with no binding was accepted: %v", err)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); n != 0 {
		t.Fatalf("a refused sample was stored (%d rows)", n)
	}
}

// A quota crossing is enforced at the SAMPLED time of the record that crossed it, driven end to end through
// ingestion and the expiry sweep.
func TestIntegration_Acct_QuotaCrossesAtTheSampledTime(t *testing.T) {
	f := newIngest(t, 1000) // 1000-byte allowance
	ctx := context.Background()
	started := time.Now().Add(-3 * time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, ent, f.device, started, "10.9.0.1")

	crossing := time.Now().Add(-90 * time.Minute).Truncate(time.Microsecond)
	if _, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 1}, 300, 0, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p3.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 2}, 400, 400, crossing); err != nil {
		t.Fatal(err)
	}
	due, err := enforce.New(f.pool).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" {
		t.Fatalf("expiries = %+v, want one DATA ending", due)
	}
	if !due[0].At.Equal(crossing) {
		t.Fatalf("ended at %v, want the crossing sample time %v", due[0].At, crossing)
	}
}

// While the flags are OFF the arm does not exist, so ingestion writes nothing at all.
func TestIntegration_Acct_FlagsOffWritesNothing(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, ent, f.device, started, "10.9.0.1")

	dark := newPhase3(iamv2.PMSConfig{}, &acctd{db: f.pool}, f.tenant, f.site)
	if dark != nil {
		t.Fatal("the Phase-3 arm was constructed while dark")
	}
	if dark.ownsAccounting() {
		t.Fatal("a dark arm claimed ownership of accounting — the legacy path must keep running")
	}
	class, err := dark.ingestSample(ctx, sampleIdentity{SessionID: sess, Seq: 1}, 999, 999, time.Now())
	if err != nil || class != "" {
		t.Fatalf("a dark ingest did something: %v / %s", err, class)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); n != 0 {
		t.Fatalf("a dark arm wrote %d accounting rows", n)
	}
	if up, down := f.usage(t, ent); up != 0 || down != 0 {
		t.Fatalf("a dark arm attributed usage: %d/%d", up, down)
	}
	_ = fmt.Sprint()
}
