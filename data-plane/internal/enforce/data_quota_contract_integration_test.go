//go:build integration

package enforce

// THE QUOTA CONTRACT, PROVEN THROUGH THE OPERATION THE APPLIANCE ACTUALLY RUNS.
//
// Every other quota test in this package inserts into iam_v2.accounting_records directly. That proves the
// DERIVED crossing and nothing else — and it is exactly why a real defect survived a green suite for the whole
// life of the product: iam_v2.ingest_absolute_counters, the operation acctd calls on every tick, advanced the
// accounting record, the session totals and the checkpoint, but never the ENTITLEMENT's own consumed_data_bytes.
// On the appliance all four entitlements read 0 bytes and usage_version 0 while a real guest moved 35 MB.
//
// The tested path and the live path were different paths. So these cases go through the live one: every byte
// below enters as an ABSOLUTE counter reading submitted to the real ingestion operation, with the real
// checkpoint, replay, epoch and attribution rules in the way — because those rules are the contract.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------------------------------------

// quotaSeed builds a site whose plan revision carries an explicit quota, speed allocation and time mode. It is
// separate from seed() because two of those three are the axes this file varies, and the contract says none of
// them may change data-quota semantics.
func quotaSeed(t *testing.T, p *pgxpool.Pool, quota any, alloc, timeMode string) fixture {
	t.Helper()
	var f fixture
	if err := p.QueryRow(context.Background(), `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
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
	return f
}

// measured is a session the ingestion operation will accept: it carries the ingress interface and address the
// operation re-derives its counter source from. A session without them cannot be metered at all, by design.
type measured struct{ id, device, ip, iface string }

// meteredSession attaches a device to an entitlement and opens a live session on a named bridge and address.
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

// firstMeteredSession is the same for the session grant() already created: it needs an interface before the
// ingestion operation will measure it.
func onBridge(t *testing.T, p *pgxpool.Pool, sess, iface string) measured {
	t.Helper()
	var m measured
	m.id, m.iface = sess, iface
	if err := p.QueryRow(context.Background(), `UPDATE iam_v2.sessions SET ingress_interface=$2
		WHERE id=$1 RETURNING device_id::text, host(ip)`, sess, iface).Scan(&m.device, &m.ip); err != nil {
		t.Fatalf("place session on bridge: %v", err)
	}
	return m
}

// submit hands the operation an ABSOLUTE counter reading, exactly as acctd does. The class minor is derived by
// the database from the session's own address, so the test does not get to choose it.
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

// submitErr is submit for the cases that MUST be refused.
func submitErr(t *testing.T, p *pgxpool.Pool, f fixture, m measured, epoch, absUp, absDown int64, at time.Time) error {
	t.Helper()
	var got string
	return p.QueryRow(context.Background(),
		`SELECT iam_v2.ingest_absolute_counters($1,$2,$3::uuid,$4::uuid,$5,
		        iam_v2.p3_expected_class_minor(($6::text)::inet),$7,$8,$9,$10)`,
		f.tenant, f.site, m.id, m.device, m.iface, m.ip, epoch, absUp, absDown, at).Scan(&got)
}

// recorded is what the ENTITLEMENT says it has spent; attributed is what the accounting series says. The whole
// defect was that the first never moved, so every case below asserts on both.
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

// agree is the invariant the migration asserts and every case re-checks: the maintained counter and the
// derived series are two views of one fact and may never diverge.
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

// ---------------------------------------------------------------------------------------------------------
// 1. THE DEFECT ITSELF
// ---------------------------------------------------------------------------------------------------------

// THE REGRESSION. One session, real ingestion, and the Entitlement must record what it spent. Before the fix
// this failed on the first assertion with 0 — which is precisely what the appliance showed.
func TestIntegration_Quota_TheEntitlementRecordsWhatItSpent(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-test")

	// The first reading is the BASELINE: nothing is billed, because there is nothing to subtract from.
	if got := submit(t, p, f, m, 1, 0, 0, at.Add(time.Minute)); got != "BASELINED" {
		t.Fatalf("first observation = %q, want BASELINED", got)
	}
	agree(t, p, ent, 0)

	// 12 MB of real movement.
	if got := submit(t, p, f, m, 1, 2_000_000, 10_000_000, at.Add(2*time.Minute)); got != "ACCEPTED" {
		t.Fatalf("observation = %q, want ACCEPTED", got)
	}
	agree(t, p, ent, 12_000_000)

	// ...and it accumulates rather than being replaced.
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

// MONOTONIC. The contract's before-crossing requirement: usage only ever increases, sample after sample.
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

// ---------------------------------------------------------------------------------------------------------
// 2. AGGREGATION — the property the whole contract rests on
// ---------------------------------------------------------------------------------------------------------

// TWO DEVICES, ONE ALLOWANCE, THROUGH THE REAL OPERATION. Neither device crosses it alone.
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
	// EVERY session under it is torn down, not just the one that happened to cross.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE entitlement_id=$1 AND ended IS NULL`, ent); n != 0 {
		t.Fatalf("%d session(s) still live under an exhausted entitlement", n)
	}
}

// A NEW ADDRESS IS NOT A NEW ALLOWANCE. The guest's phone moves to a different address on a different bridge,
// which opens a NEW session — the exact shape that would reset a per-session counter.
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

	// The old session ends (lease lost, roaming, reconnect — the reason does not matter) and a new one opens
	// on a new address under the SAME entitlement.
	if _, err := p.Exec(ctx, `SELECT iam_v2.close_session($1,'ADDRESS_NO_LONGER_OWNED')`, first.id); err != nil {
		t.Fatal(err)
	}
	moved := meteredSession(t, p, f, ent, "10.20.31.77", "02:00:00:00:40:11", "br-g-two", at.Add(2*time.Minute))
	submit(t, p, f, moved, 1, 0, 0, at.Add(2*time.Minute))

	// The allowance did not start again: 70 MB is still spent.
	agree(t, p, ent, 70_000_000)
	if due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil || len(due) != 0 {
		t.Fatalf("unexpected expiry before the quota: %+v (err %v)", due, err)
	}

	// 40 MB on the new address crosses the SHARED total, not a fresh one.
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

// ---------------------------------------------------------------------------------------------------------
// 3. IDEMPOTENCE — a retry is not traffic
// ---------------------------------------------------------------------------------------------------------

// THE SAME READING TWICE IS NOT TWICE THE BYTES. An uncertain commit and a retry is ordinary; counting it
// would let a flaky link exhaust a guest's package without them using anything.
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

	// Replay the identical reading four times.
	for i := 0; i < 4; i++ {
		got := submit(t, p, f, m, 1, 5_000_000, 20_000_000, at.Add(time.Minute))
		if got != "REPLAY:ACCEPTED" && got != "DUPLICATE" {
			t.Fatalf("replay %d classified %q", i, got)
		}
	}
	agree(t, p, ent, 25_000_000)

	// An OUT-OF-ORDER delivery is refused outright and must not move the counter either.
	if err := submitErr(t, p, f, m, 1, 6_000_000, 21_000_000, at.Add(30*time.Second)); err == nil {
		t.Fatal("a sample dated before the last accepted one was accepted")
	}
	agree(t, p, ent, 25_000_000)
}

// A COUNTER RESET IS A NEW BASELINE, NOT A REFUND AND NOT A WINDFALL. netd replacing a class bumps the epoch;
// the bytes already spent stay spent.
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

	// New epoch: the kernel counters legitimately restart from zero.
	if got := submit(t, p, f, m, 2, 0, 0, at.Add(2*time.Minute)); got != "RESET_BASELINED" {
		t.Fatalf("epoch bump = %q, want RESET_BASELINED", got)
	}
	agree(t, p, ent, 30_000_000) // unchanged: a reset is not usage

	submit(t, p, f, m, 2, 1_000_000, 4_000_000, at.Add(3*time.Minute))
	agree(t, p, ent, 35_000_000) // and counting continues on top

	// An OLDER epoch is a stale reading and is refused.
	if err := submitErr(t, p, f, m, 1, 9_000_000, 30_000_000, at.Add(4*time.Minute)); err == nil {
		t.Fatal("a reading from a superseded epoch was accepted")
	}
	agree(t, p, ent, 35_000_000)
}

// ---------------------------------------------------------------------------------------------------------
// 4. THE CROSSING
// ---------------------------------------------------------------------------------------------------------

// EXACTLY ONCE, AT THE SAMPLE THAT CROSSED IT. The instant is the sample's, not the sweep's, and a second
// sweep changes nothing.
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

	// ONE transition. A second sweep must not re-terminate, re-date, or report anything.
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

// LATE ACCOUNTING MAY DEEPEN THE AUDIT TRAIL. IT MAY NOT REOPEN ACCESS.
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

	// The session is over, so the operation refuses further readings for it outright — an ended session owns
	// no further traffic. Access cannot be revived by submitting more.
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
	// AND NO REPLACEMENT WAS CONJURED. Exhaustion ends access; it does not silently sell another package.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay); n != 1 {
		t.Fatalf("%d entitlements exist for the stay; exhaustion must not create another", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1`, f.stay); n != 1 {
		t.Fatalf("%d purchases exist for the stay; exhaustion must not create another", n)
	}
}

// A SWEEP THAT RESTARTS MID-STAY FINDS THE SAME NUMBER. Usage is durable state, not something a process
// remembers: the enforcer is thrown away and rebuilt between every sample here.
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
		// a brand-new Enforcer every pass: nothing is carried in memory
		if _, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
			t.Fatal(err)
		}
	}
	agree(t, p, ent, 70_000_000)
	if s := status(t, p, ent); s != "ACTIVE" {
		t.Fatalf("access is %s at 70MB of 100MB across five restarts", s)
	}
}

// ---------------------------------------------------------------------------------------------------------
// 5. THE AXES THAT MUST NOT CHANGE QUOTA SEMANTICS
// ---------------------------------------------------------------------------------------------------------

// SHARED SPEED ALLOCATION IS A SHAPING DECISION, NOT A QUOTA ONE. The same aggregate rule applies.
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

// A PLAN WITH NO QUOTA IS UNLIMITED, and stays unlimited however much is spent. The counter still records it —
// unlimited means no ceiling, not no measurement.
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

// THE OUTER WINDOW AND THE QUOTA ARE INDEPENDENT. A VALIDITY_WINDOW entitlement whose window has NOT elapsed
// still runs out of data, and the reason recorded is DATA rather than TIME.
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

// DATA QUOTA IS NOT A PHASE-6 FEATURE. This is the coupling the Product Owner asked about explicitly: the
// aggregate ONLINE-TIME mode is dark, and data enforcement must be completely independent of it. The enforcer
// here is the plain one — no WithAggregateOnlineTime, charge bound zero — which is the configuration a
// rollback or a disabled flag produces.
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

	e := New(p) // aggregateTimeMaxCharge == 0: the Phase-6 tick never runs
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
	// And with the feature ON the answer is identical: the flag changes nothing about data.
	f2 := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	ent2, sess2 := grant(t, p, f2, nil, at)
	m2 := onBridge(t, p, sess2, "br-g-test")
	submit(t, p, f2, m2, 1, 0, 0, at)
	submit(t, p, f2, m2, 1, 30_000_000, 80_000_000, at.Add(time.Minute))
	due2, err := New(p).WithAggregateOnlineTime(60).EnforceExpiries(ctx, f2.tenant, f2.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due2) != 1 || due2[0].Reason != "DATA" || due2[0].EntitlementID != ent2 {
		t.Fatalf("the same crossing produced a different answer with the flag on: %+v", due2)
	}
}

// ---------------------------------------------------------------------------------------------------------
// 6. NOTHING FINANCIAL IS INVOLVED
// ---------------------------------------------------------------------------------------------------------

// RUNNING OUT OF DATA IS NOT A FINANCIAL EVENT. No posting, no attempt, no outbox row, no payment — a guest
// hitting their allowance must never touch the PMS or a payment provider.
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
