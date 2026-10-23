//go:build integration

package enforce

// The quota contract, proven through the operation acctd runs. Other quota tests insert into
// iam_v2.accounting_records directly and prove only the derived crossing; here every byte enters as an absolute
// counter reading submitted to iam_v2.ingest_absolute_counters, with the real checkpoint, replay, epoch and
// attribution rules, which also advance the entitlement's own consumed_data_bytes.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// quotaSeed builds a site whose plan revision carries an explicit quota, speed allocation and time mode; none
// of them may change data-quota semantics.
func quotaSeed(t *testing.T, p *pgxpool.Pool, quota any, alloc, timeMode string) fixture {
	t.Helper()
	var f fixture
	ctx := context.Background()
	tx := opTx(t, p, "stay")
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM gen_random_uuid() g RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, gen_random_uuid() g RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'protel-fias','ACTIVE' FROM si RETURNING id,tenant_id,site_id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,'RQ','SQ','IN_HOUSE',1,0 FROM pi RETURNING id),
	  dv AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, gen_random_uuid(),'02:00:00:00:40:01'::macaddr FROM pi RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'qplan',true FROM pi RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes,speed_allocation)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id,1,8000,3000,4,'REJECT_NEW_DEVICE',$2,$1,$3 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'qpkg',false FROM pi RETURNING id,tenant_id,site_id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.id,1,spr.id,'FREE_STAY',0,ARRAY['NOT_REQUIRED']::text[],'{}'::jsonb FROM ip, spr RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM st)::text, (SELECT id FROM dv)::text, (SELECT id FROM ipr)::text, (SELECT id FROM spr)::text`,
		quota, timeMode, alloc).Scan(&f.tenant, &f.site, &f.iface, &f.stay, &f.device, &f.pkgRev, &f.svcRev); err != nil {
		t.Fatalf("quota seed: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("quota seed: %v", err)
	}
	return f
}

// measured is a session the ingestion operation accepts: it needs the ingress interface and address, or it cannot be metered.
type measured struct{ id, device, ip, iface string }

func meteredSession(t *testing.T, p *pgxpool.Pool, f fixture, ent, ip, mac, iface string, at time.Time) measured {
	t.Helper()
	ctx := context.Background()
	var dev string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
		VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),($3::text)::macaddr) RETURNING id::text`,
		f.tenant, f.site, mac).Scan(&dev); err != nil {
		t.Fatalf("device: %v", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,$3)`, ent, dev, at); err != nil {
		t.Fatalf("authorize device: %v", err)
	}
	var sess string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(tenant_id,site_id,entitlement_id,device_id,state,started,ip,mac,ingress_interface)
		VALUES ($1,$2,$3,$4,'active',$5,($6::text)::inet,($7::text)::macaddr,$8) RETURNING id::text`,
		f.tenant, f.site, ent, dev, at, ip, mac, iface).Scan(&sess); err != nil {
		t.Fatalf("session: %v", err)
	}
	return measured{id: sess, device: dev, ip: ip, iface: iface}
}

// firstMeteredSession is the same for the session grant() created, which needs an interface before it is measured.
func onBridge(t *testing.T, p *pgxpool.Pool, sess, iface string) measured {
	t.Helper()
	var m measured
	m.id, m.iface = sess, iface
	// A session's ingress interface is accounting identity, which only the ingestion owner may change; this
	// seed runs as iam_v2_owner.
	ctx := context.Background()
	tx := ownerTx(t, p)
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `UPDATE iam_v2.sessions SET ingress_interface=$2
		WHERE id=$1 RETURNING device_id::text, host(ip)`, sess, iface).Scan(&m.device, &m.ip); err != nil {
		t.Fatalf("place session on bridge: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("place session on bridge: %v", err)
	}
	return m
}

// submit hands the operation an absolute counter reading, as acctd does; the database derives the class minor.
func submit(t *testing.T, p *pgxpool.Pool, f fixture, m measured, epoch, absUp, absDown int64, at time.Time) string {
	t.Helper()
	var got string
	err := p.QueryRow(context.Background(),
		`SELECT iam_v2.ingest_absolute_counters($1,$2,$3::uuid,$4::uuid,$5,
		        iam_v2.p3_expected_class_minor(($6::text)::inet),$7,$8,$9,$10)`,
		f.tenant, f.site, m.id, m.device, m.iface, m.ip, epoch, absUp, absDown, at).Scan(&got)
	if err != nil {
		t.Fatalf("ingest (up=%d down=%d at=%s): %v", absUp, absDown, at.Format(time.RFC3339Nano), err)
	}
	return got
}

func submitErr(t *testing.T, p *pgxpool.Pool, f fixture, m measured, epoch, absUp, absDown int64, at time.Time) error {
	t.Helper()
	var got string
	return p.QueryRow(context.Background(),
		`SELECT iam_v2.ingest_absolute_counters($1,$2,$3::uuid,$4::uuid,$5,
		        iam_v2.p3_expected_class_minor(($6::text)::inet),$7,$8,$9,$10)`,
		f.tenant, f.site, m.id, m.device, m.iface, m.ip, epoch, absUp, absDown, at).Scan(&got)
}

// recorded is what the entitlement says it spent; attributed is what the accounting series says. Cases assert on both.
func recorded(t *testing.T, p *pgxpool.Pool, ent string) int64 {
	t.Helper()
	var n int64
	if err := p.QueryRow(context.Background(),
		`SELECT consumed_data_bytes FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func attributed(t *testing.T, p *pgxpool.Pool, ent string) int64 {
	t.Helper()
	var n int64
	if err := p.QueryRow(context.Background(),
		`SELECT iam_v2.p3_entitlement_data_usage($1)`, ent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// agree asserts the maintained counter and the derived series never diverge.
func agree(t *testing.T, p *pgxpool.Pool, ent string, want int64) {
	t.Helper()
	r, a := recorded(t, p, ent), attributed(t, p, ent)
	if r != a {
		t.Fatalf("entitlement records %d bytes but %d are attributable: the counter and the accounting "+
			"series disagree", r, a)
	}
	if want >= 0 && r != want {
		t.Fatalf("usage = %d, want %d", r, want)
	}
}

func status(t *testing.T, p *pgxpool.Pool, ent string) string {
	t.Helper()
	var s string
	if err := p.QueryRow(context.Background(),
		`SELECT status FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// One session, real ingestion: the entitlement must record what it spent.
func TestIntegration_Quota_TheEntitlementRecordsWhatItSpent(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")

	// The first reading is the baseline: nothing is billed.
	if got := submit(t, p, f, m, 1, 0, 0, at.Add(time.Minute)); got != "BASELINED" {
		t.Fatalf("first observation = %q, want BASELINED", got)
	}
	agree(t, p, ent, 0)

	if got := submit(t, p, f, m, 1, 2_000_000, 10_000_000, at.Add(2*time.Minute)); got != "ACCEPTED" {
		t.Fatalf("observation = %q, want ACCEPTED", got)
	}
	agree(t, p, ent, 12_000_000)

	submit(t, p, f, m, 1, 3_000_000, 15_000_000, at.Add(3*time.Minute))
	agree(t, p, ent, 18_000_000)

	var uv int
	if err := p.QueryRow(context.Background(),
		`SELECT usage_version FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&uv); err != nil {
		t.Fatal(err)
	}
	if uv == 0 {
		t.Fatal("usage_version never moved: nothing recorded that the entitlement's usage changed")
	}
}

// Usage only ever increases, sample after sample.
func TestIntegration_Quota_UsageIncreasesMonotonically(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)

	var prev int64
	for i := 1; i <= 8; i++ {
		submit(t, p, f, m, 1, int64(i)*1_000_000, int64(i)*3_000_000, at.Add(time.Duration(i)*time.Minute))
		now := recorded(t, p, ent)
		if now <= prev {
			t.Fatalf("sample %d: usage went from %d to %d", i, prev, now)
		}
		agree(t, p, ent, -1)
		prev = now
		if s := status(t, p, ent); s != "ACTIVE" {
			t.Fatalf("sample %d: access is %s at %d of 100000000 bytes", i, s, now)
		}
	}
}

// Two devices, one allowance: neither device crosses it alone.
func TestIntegration_Quota_TwoDevicesSpendOneAllowance(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	const quota = 100_000_000
	f := quotaSeed(t, p, int64(quota), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	phone := onBridge(t, p, sess, "br-g-test")
	laptop := meteredSession(t, p, f, ent, "10.20.30.55", "02:00:00:00:40:09", "br-g-test", at)

	submit(t, p, f, phone, 1, 0, 0, at)
	submit(t, p, f, laptop, 1, 0, 0, at)
	submit(t, p, f, phone, 1, 10_000_000, 50_000_000, at.Add(time.Minute)) // 60 MB
	agree(t, p, ent, 60_000_000)
	if due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil || len(due) != 0 {
		t.Fatalf("access ended at 60MB of %d: %+v (err %v)", quota, due, err)
	}

	submit(t, p, f, laptop, 1, 10_000_000, 40_000_000, at.Add(2*time.Minute)) // +50 MB = 110 MB
	agree(t, p, ent, 110_000_000)
	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].EntitlementID != ent || due[0].Reason != "DATA" {
		t.Fatalf("the aggregate crossing did not end access: %+v — a second device must not buy a second "+
			"allowance", due)
	}
	// Every session under it is torn down, not just the one that crossed.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE entitlement_id=$1 AND ended IS NULL`, ent); n != 0 {
		t.Fatalf("%d session(s) still live under an exhausted entitlement", n)
	}
}

// A new address (a new session on another bridge) is not a new allowance; a per-session counter would reset.
func TestIntegration_Quota_MovingToANewAddressDoesNotResetUsage(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	first := onBridge(t, p, sess, "br-g-one")
	submit(t, p, f, first, 1, 0, 0, at)
	submit(t, p, f, first, 1, 20_000_000, 50_000_000, at.Add(time.Minute)) // 70 MB
	agree(t, p, ent, 70_000_000)

	// The old session ends and a new one opens on a new address under the same entitlement.
	if _, err := p.Exec(ctx, `SELECT iam_v2.close_session($1,'ADDRESS_NO_LONGER_OWNED')`, first.id); err != nil {
		t.Fatal(err)
	}
	moved := meteredSession(t, p, f, ent, "10.20.31.77", "02:00:00:00:40:11", "br-g-two", at.Add(2*time.Minute))
	submit(t, p, f, moved, 1, 0, 0, at.Add(2*time.Minute))

	agree(t, p, ent, 70_000_000)
	if due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil || len(due) != 0 {
		t.Fatalf("unexpected expiry before the quota: %+v (err %v)", due, err)
	}

	submit(t, p, f, moved, 1, 10_000_000, 30_000_000, at.Add(3*time.Minute))
	agree(t, p, ent, 110_000_000)
	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" {
		t.Fatalf("a new address bought a fresh allowance: %+v", due)
	}
}

// The same reading twice is not twice the bytes, or a flaky link could exhaust a guest's package.
func TestIntegration_Quota_DuplicateReadingsDoNotDoubleCount(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 5_000_000, 20_000_000, at.Add(time.Minute))
	agree(t, p, ent, 25_000_000)

	for i := 0; i < 4; i++ {
		got := submit(t, p, f, m, 1, 5_000_000, 20_000_000, at.Add(time.Minute))
		if got != "REPLAY:ACCEPTED" && got != "DUPLICATE" {
			t.Fatalf("replay %d classified %q", i, got)
		}
	}
	agree(t, p, ent, 25_000_000)

	// An out-of-order delivery is refused and must not move the counter.
	if err := submitErr(t, p, f, m, 1, 6_000_000, 21_000_000, at.Add(30*time.Second)); err == nil {
		t.Fatal("a sample dated before the last accepted one was accepted")
	}
	agree(t, p, ent, 25_000_000)
}

// A counter reset (netd replacing a class bumps the epoch) is a new baseline: spent bytes stay spent.
func TestIntegration_Quota_AClassReplacementKeepsTheSpentBytes(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 8_000_000, 22_000_000, at.Add(time.Minute))
	agree(t, p, ent, 30_000_000)

	if got := submit(t, p, f, m, 2, 0, 0, at.Add(2*time.Minute)); got != "RESET_BASELINED" {
		t.Fatalf("epoch bump = %q, want RESET_BASELINED", got)
	}
	agree(t, p, ent, 30_000_000) // unchanged: a reset is not usage

	submit(t, p, f, m, 2, 1_000_000, 4_000_000, at.Add(3*time.Minute))
	agree(t, p, ent, 35_000_000) // and counting continues on top

	// An older epoch is stale and refused.
	if err := submitErr(t, p, f, m, 1, 9_000_000, 30_000_000, at.Add(4*time.Minute)); err == nil {
		t.Fatal("a reading from a superseded epoch was accepted")
	}
	agree(t, p, ent, 35_000_000)
}

// The crossing happens once, at the crossing sample's instant (not the sweep's); a second sweep changes nothing.
func TestIntegration_Quota_CrossingTerminatesExactlyOnceAtTheCrossingSample(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 20_000_000, 70_000_000, at.Add(time.Minute)) // 90 MB — under
	crossing := at.Add(2 * time.Minute)
	submit(t, p, f, m, 1, 25_000_000, 80_000_000, crossing) // 105 MB — over

	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" {
		t.Fatalf("expiry = %+v, want one DATA termination", due)
	}
	if !due[0].At.Equal(crossing.UTC()) && due[0].At.Sub(crossing).Abs() > time.Millisecond {
		t.Fatalf("ended at %s, want the crossing sample %s — the ending is dated by the sample, not the sweep",
			due[0].At, crossing)
	}
	if s := status(t, p, ent); s != "TERMINATED" {
		t.Fatalf("entitlement is %s after crossing its quota", s)
	}

	n1 := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_state_transitions WHERE entitlement_id=$1`, ent)
	again, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("a second sweep terminated it again: %+v", again)
	}
	if n2 := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_state_transitions WHERE entitlement_id=$1`, ent); n2 != n1 {
		t.Fatalf("transitions went %d -> %d: the termination was recorded more than once", n1, n2)
	}
}

// Late accounting may deepen the audit trail but may not reopen access.
func TestIntegration_Quota_LateAccountingCannotReopenAccess(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 40_000_000, 70_000_000, at.Add(time.Minute))
	if _, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	if s := status(t, p, ent); s != "TERMINATED" {
		t.Fatalf("entitlement is %s", s)
	}
	spent := recorded(t, p, ent)

	// An ended session owns no further traffic, so more readings are refused.
	if err := submitErr(t, p, f, m, 1, 45_000_000, 75_000_000, at.Add(2*time.Minute)); err == nil {
		t.Fatal("the operation accepted a reading for a session whose entitlement had ended")
	}
	if s := status(t, p, ent); s != "TERMINATED" {
		t.Fatalf("entitlement left TERMINATED and is now %s", s)
	}
	if now := recorded(t, p, ent); now != spent {
		t.Fatalf("usage moved from %d to %d after termination", spent, now)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE entitlement_id=$1 AND ended IS NULL`, ent); n != 0 {
		t.Fatalf("%d session(s) live again after termination", n)
	}
	// Exhaustion ends access; it does not silently sell another package.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay); n != 1 {
		t.Fatalf("%d entitlements exist for the stay; exhaustion must not create another", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1`, f.stay); n != 1 {
		t.Fatalf("%d purchases exist for the stay; exhaustion must not create another", n)
	}
}

// Usage is durable state: the enforcer is rebuilt between every sample here.
func TestIntegration_Quota_UsageSurvivesRestartAndReconcile(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)

	for i := 1; i <= 5; i++ {
		submit(t, p, f, m, 1, int64(i)*4_000_000, int64(i)*10_000_000, at.Add(time.Duration(i)*time.Minute))
		if _, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
			t.Fatal(err)
		}
	}
	agree(t, p, ent, 70_000_000)
	if s := status(t, p, ent); s != "ACTIVE" {
		t.Fatalf("access is %s at 70MB of 100MB across five restarts", s)
	}
}

// Shared speed allocation is a shaping decision, not a quota one; the same aggregate rule applies.
func TestIntegration_Quota_SharedAllocationObeysTheSameAggregateQuota(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "SHARED", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	a := onBridge(t, p, sess, "br-g-test")
	b := meteredSession(t, p, f, ent, "10.20.30.66", "02:00:00:00:40:21", "br-g-test", at)
	submit(t, p, f, a, 1, 0, 0, at)
	submit(t, p, f, b, 1, 0, 0, at)
	submit(t, p, f, a, 1, 10_000_000, 45_000_000, at.Add(time.Minute))
	submit(t, p, f, b, 1, 10_000_000, 40_000_000, at.Add(2*time.Minute))
	agree(t, p, ent, 105_000_000)

	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" {
		t.Fatalf("a SHARED plan did not obey the aggregate quota: %+v", due)
	}
}

// A plan with no quota is unlimited however much is spent, but the counter still records it.
func TestIntegration_Quota_NoQuotaMeansUnlimited(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, nil, "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 900_000_000, 900_000_000, at.Add(time.Minute))
	agree(t, p, ent, 1_800_000_000)

	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("an unlimited plan ended access after 1.8GB: %+v", due)
	}
	if s := status(t, p, ent); s != "ACTIVE" {
		t.Fatalf("unlimited access is %s", s)
	}
}

// The window and the quota are independent: a VALIDITY_WINDOW entitlement within its window still runs out of data (reason DATA).
func TestIntegration_Quota_DataQuotaAppliesUnderValidityWindow(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	future := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, p, f, &future, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 50_000_000, 60_000_000, at.Add(time.Minute))

	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" || due[0].EntitlementID != ent {
		t.Fatalf("expiry = %+v, want DATA — a day of validity left is not more data", due)
	}
}

// Data enforcement must be independent of the aggregate online-time mode; the plain Enforcer here (no
// WithAggregateOnlineTime) is what a rollback or disabled flag produces.
func TestIntegration_Quota_EnforcedWithTheAggregateTimeFeatureOff(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 30_000_000, 80_000_000, at.Add(time.Minute))
	agree(t, p, ent, 110_000_000)

	e := New(p)
	if e.aggregateTimeMaxCharge != 0 {
		t.Fatal("this case is meaningless unless the aggregate-time feature is off")
	}
	due, err := e.EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" || due[0].EntitlementID != ent {
		t.Fatalf("data quota was not enforced with the aggregate-time feature off: %+v — the two semantics "+
			"are independent and must not be coupled", due)
	}
	// The flag-on direction needs p6_tick_online_time and is covered by the Phase-6 suite.
}

// Running out of data is not a financial event: no posting, outbox row or payment may result.
func TestIntegration_Quota_ExhaustionTouchesNothingFinancial(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	_, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 60_000_000, 60_000_000, at.Add(time.Minute))
	if _, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct{ what, sql string }{
		{"pms_postings", `SELECT count(*) FROM iam_v2.pms_postings WHERE site_id=$1`},
		{"posting_attempts", `SELECT count(*) FROM iam_v2.posting_attempts WHERE site_id=$1`},
		{"posting_outbox", `SELECT count(*) FROM iam_v2.posting_outbox WHERE site_id=$1`},
		{"payment_transactions", `SELECT count(*) FROM iam_v2.payment_transactions WHERE site_id=$1`},
	} {
		if n := count(t, p, q.sql, f.site); n != 0 {
			t.Fatalf("%d %s row(s) after a data-quota exhaustion", n, q.what)
		}
	}
}

// The crossing sample still belongs to the entitlement that spent it: the binding closes at the termination
// instant, and a strictly half-open interval would drop a sample landing on its close. The half-open bound stays
// (it makes a rebinding unambiguous); only the terminal case, with no successor binding, keeps its last sample.
// After a data termination the counter and derived usage agree and both include the crossing sample.
func TestIntegration_Quota_CrossingSampleStaysAttributedAfterTermination(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 20_000_000, 70_000_000, at.Add(time.Minute)) // 90 MB, under
	crossing := at.Add(2 * time.Minute)
	submit(t, p, f, m, 1, 25_000_000, 80_000_000, crossing) // 105 MB, the crossing sample

	before := recorded(t, p, ent)
	if before != 105_000_000 {
		t.Fatalf("counter = %d before termination, want 105000000", before)
	}
	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" {
		t.Fatalf("expiry = %+v, want one DATA termination", due)
	}

	agree(t, p, ent, 105_000_000)
	if s := status(t, p, ent); s != "TERMINATED" {
		t.Fatalf("entitlement is %s", s)
	}
}

// A rebinding at an exact boundary is counted once, by the successor, which covers the instant.
func TestIntegration_Quota_RebindingBoundarySampleIsCountedOnce(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	first, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 1_000_000, 4_000_000, at.Add(time.Minute)) // 5 MB under the first entitlement
	agree(t, p, first, 5_000_000)

	// A second entitlement on another Stay (ent_live_stay allows one live per Stay), as a transfer does.
	second := secondEntitlement(t, p, f, at)
	boundary := at.Add(2 * time.Minute)
	// p6_session_requires_authorized_binding: authorize the device on the successor before rebinding.
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,$3)`, second, m.device, boundary); err != nil {
		t.Fatalf("authorize device on the successor: %v", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.rebind_session_entitlement($1,$2,$3)`, m.id, second, boundary); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	// A sample lands exactly on the boundary.
	submit(t, p, f, m, 1, 1_500_000, 5_500_000, boundary) // +2 MB

	if got := attributed(t, p, first); got != 5_000_000 {
		t.Fatalf("the closed binding claimed the boundary sample too: %d bytes attributed, want 5000000", got)
	}
	if got := attributed(t, p, second); got != 2_000_000 {
		t.Fatalf("the successor was attributed %d bytes at the boundary, want 2000000", got)
	}
	if total := attributed(t, p, first) + attributed(t, p, second); total != 7_000_000 {
		t.Fatalf("total attributed = %d, want 7000000 - a boundary sample was double counted", total)
	}
}

// The attribution correction must not change enforcement decisions, accounting records or anything financial.
func TestIntegration_Quota_TheCorrectionChangesNoOtherSemantics(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 10_000_000, 30_000_000, at.Add(time.Minute)) // 40 MB, well under

	if due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil || len(due) != 0 {
		t.Fatalf("an entitlement at 40MB of 100MB was ended: %+v (err %v)", due, err)
	}
	agree(t, p, ent, 40_000_000)

	records := count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, m.id)
	if records != 1 {
		t.Fatalf("%d accounting records for one billed sample, want 1", records)
	}
	for _, q := range []struct{ what, sql string }{
		{"pms_postings", `SELECT count(*) FROM iam_v2.pms_postings WHERE site_id=$1`},
		{"posting_attempts", `SELECT count(*) FROM iam_v2.posting_attempts WHERE site_id=$1`},
		{"posting_outbox", `SELECT count(*) FROM iam_v2.posting_outbox WHERE site_id=$1`},
		{"payment_transactions", `SELECT count(*) FROM iam_v2.payment_transactions WHERE site_id=$1`},
	} {
		if n := count(t, p, q.sql, f.site); n != 0 {
			t.Fatalf("%d %s row(s)", n, q.what)
		}
	}
}

// secondEntitlement creates another Stay in the site with an ACTIVE Entitlement on the fixture's interface and plan.
func secondEntitlement(t *testing.T, p *pgxpool.Pool, f fixture, at time.Time) string {
	t.Helper()
	ctx := context.Background()
	var stay string
	stx := opTx(t, p, "stay")
	defer func() { _ = stx.Rollback(ctx) }()
	if err := stx.QueryRow(ctx, `INSERT INTO iam_v2.stays
		(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,
		 lifecycle_version,last_applied_event_version)
		VALUES (gen_random_uuid(),$1,$2,$3,'RQ2','SQ2','IN_HOUSE',1,0) RETURNING id::text`,
		f.tenant, f.site, f.iface).Scan(&stay); err != nil {
		t.Fatalf("second stay: %v", err)
	}
	if err := stx.Commit(ctx); err != nil {
		t.Fatalf("second stay: %v", err)
	}
	tx := opTx(t, p, "commerce_intent")
	defer func() { _ = tx.Rollback(ctx) }()
	var pur, ent string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,$5,'ADMIN_GRANT',0,'GRANTED') RETURNING id::text`,
		f.tenant, f.site, f.pkgRev, f.iface, stay).Scan(&pur); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,
		 package_revision_id,time_accounting_mode,end_mode,status)
		VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7,'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE') RETURNING id::text`,
		f.tenant, f.site, stay, f.iface, pur, f.svcRev, f.pkgRev).Scan(&ent); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',$2,'GRANT')`, ent, at); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return ent
}
