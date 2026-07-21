//go:build integration

package enforce

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping enforce PG16 integration")
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

type fixture struct{ tenant, site, iface, stay, device, pkgRev, svcRev string }

// seed builds a site with one IN_HOUSE Stay, a device, and a plan revision carrying explicit rates + quota.
func seed(t *testing.T, p *pgxpool.Pool, down, up int, quota int64) fixture {
	t.Helper()
	ctx := context.Background()
	var f fixture
	if err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'protel-fias','ACTIVE' FROM si RETURNING id,tenant_id,site_id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,'R1','S1','IN_HOUSE',1,0 FROM pi RETURNING id),
	  dv AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, gen_random_uuid(),'02:00:00:00:30:01'::macaddr FROM pi RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'plan',true FROM pi RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id,1,$1,$2,4,'REJECT_NEW_DEVICE','VALIDITY_WINDOW',$3 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'pkg',false FROM pi RETURNING id,tenant_id,site_id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.id,1,spr.id,'FREE_STAY',0,ARRAY['NOT_REQUIRED']::text[],'{}'::jsonb FROM ip, spr RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM st)::text, (SELECT id FROM dv)::text, (SELECT id FROM ipr)::text, (SELECT id FROM spr)::text`,
		down, up, quota).Scan(&f.tenant, &f.site, &f.iface, &f.stay, &f.device, &f.pkgRev, &f.svcRev); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return f
}

// grant creates an ACTIVE entitlement with its initial history and an authorized device + live session.
func grant(t *testing.T, p *pgxpool.Pool, f fixture, window *time.Time, startedAt time.Time) (string, string) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
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
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',$2,'GRANT')`, ent, startedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,$3)`, ent, f.device, startedAt); err != nil {
		t.Fatalf("authorize device: %v", err)
	}
	var sess string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.sessions(tenant_id,site_id,entitlement_id,device_id,state,started,ip,mac)
		VALUES ($1,$2,$3,$4,'active',$5,'10.20.30.40'::inet,'02:00:00:00:30:01'::macaddr) RETURNING id::text`,
		f.tenant, f.site, ent, f.device, startedAt).Scan(&sess); err != nil {
		t.Fatal(err)
	}
	return ent, sess
}

func count(t *testing.T, p *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestIntegration_PlanIsDerivedFromDurableState proves the shaping plan comes from the Entitlement's pinned
// plan revision — no separate bookkeeping — and that an ended/unentitled session is torn down with no rates.
func TestIntegration_PlanIsDerivedFromDurableState(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 8000, 3000, 0)
	win := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, p, f, &win, time.Now().Add(-time.Hour))

	plan, err := New(p).PlanForSite(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Shape) != 1 || len(plan.Tear) != 0 {
		t.Fatalf("plan shape=%d tear=%d, want 1/0", len(plan.Shape), len(plan.Tear))
	}
	s := plan.Shape[0]
	if s.SessionID != sess || s.EntitlementID != ent || s.DownKbps != 8000 || s.UpKbps != 3000 {
		t.Fatalf("shape = %+v, want the entitlement's pinned plan rates 8000/3000", s)
	}
	if s.IP != "10.20.30.40" || s.MAC == "" {
		t.Fatalf("shape carries no addressing: %+v", s)
	}

	// terminate the entitlement: the SAME session must now be torn down, with no rates at all
	if _, err := p.Exec(ctx, `SELECT iam_v2.terminate_entitlement_at_boundary($1,now(),'ADMIN')`, ent); err != nil {
		t.Fatal(err)
	}
	plan, err = New(p).PlanForSite(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Shape) != 0 || len(plan.Tear) != 1 {
		t.Fatalf("after termination shape=%d tear=%d, want 0/1", len(plan.Shape), len(plan.Tear))
	}
	if plan.Tear[0].DownKbps != 0 || plan.Tear[0].UpKbps != 0 {
		t.Fatal("a torn-down session must carry no rates (removed, not throttled)")
	}
}

// TestIntegration_WindowExpiryEndsAtTheTrueTime proves acctd-style enforcement ends access at the instant the
// window elapsed — not when the sweep noticed — and revokes the device and session with it.
func TestIntegration_WindowExpiryEndsAtTheTrueTime(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 5000, 2000, 0)
	elapsed := time.Now().Add(-20 * time.Minute).Truncate(time.Microsecond)
	ent, sess := grant(t, p, f, &elapsed, time.Now().Add(-2*time.Hour))

	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].EntitlementID != ent || due[0].Reason != "TIME" {
		t.Fatalf("due = %+v, want one TIME expiry for %s", due, ent)
	}
	if !due[0].At.Equal(elapsed) {
		t.Fatalf("expiry recorded at %v, want the TRUE window end %v", due[0].At, elapsed)
	}
	var status string
	var terminated time.Time
	if err := p.QueryRow(ctx, `SELECT status, terminated_at FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&status, &terminated); err != nil {
		t.Fatal(err)
	}
	if status != "TERMINATED" || !terminated.Equal(elapsed) {
		t.Fatalf("status=%s terminated_at=%v, want TERMINATED at %v", status, terminated, elapsed)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state='ended' AND end_reason='ENTITLEMENT_ENDED' AND ended=$2`,
		sess, elapsed); n != 1 {
		t.Fatal("the session was not ended at the true expiry instant")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id=$1 AND deauthorized_at IS NULL`, ent); n != 0 {
		t.Fatal("a device authorization survived the expiry")
	}
	// idempotent: a second sweep finds nothing and changes nothing
	again, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("a repeated sweep re-enforced %d expiries", len(again))
	}
}

// TestIntegration_QuotaExpiryEndsWhenTheQuotaWasCrossed proves a data-quota ending is recorded at the SAMPLE
// that crossed the allowance — the moment the guest actually used it up — not when the sweep ran.
func TestIntegration_QuotaExpiryEndsWhenTheQuotaWasCrossed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 5000, 2000, 1000) // 1000-byte quota
	ent, sess := grant(t, p, f, nil, time.Now().Add(-3*time.Hour))
	crossing := time.Now().Add(-90 * time.Minute).Truncate(time.Microsecond)
	for i, s := range []struct {
		at       time.Time
		up, down int64
		seq      int
	}{
		{time.Now().Add(-2 * time.Hour), 300, 0, 1},
		{crossing, 400, 400, 2}, // running total reaches 1100 >= 1000 here
		{time.Now().Add(-time.Hour), 10, 10, 3},
	} {
		if _, err := p.Exec(ctx, `INSERT INTO iam_v2.accounting_records
			(tenant_id,site_id,session_id,sample_seq,bytes_up,bytes_down,sampled_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			f.tenant, f.site, sess, s.seq, s.up, s.down, s.at); err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
	}
	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" {
		t.Fatalf("due = %+v, want one DATA expiry", due)
	}
	if !due[0].At.Equal(crossing) {
		t.Fatalf("quota ending recorded at %v, want the crossing sample time %v", due[0].At, crossing)
	}
	var terminated time.Time
	var reason string
	if err := p.QueryRow(ctx, `SELECT terminated_at, terminal_reason FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&terminated, &reason); err != nil {
		t.Fatal(err)
	}
	if !terminated.Equal(crossing) || reason != "DATA" {
		t.Fatalf("terminated_at=%v reason=%s, want %v/DATA", terminated, reason, crossing)
	}
	// the plan no longer shapes that session. (It also carries no tear instruction: the session ended more
	// than an hour ago in business time, so the edge has long since removed it and the plan stays small.)
	plan, err := New(p).PlanForSite(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Shape) != 0 {
		t.Fatalf("after a quota ending the session is still shaped: %+v", plan.Shape)
	}
}

// TestIntegration_UnexpiredAccessIsUntouched proves the sweep never ends access that is still valid.
func TestIntegration_UnexpiredAccessIsUntouched(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 5000, 2000, 1_000_000)
	win := time.Now().Add(2 * time.Hour)
	ent, sess := grant(t, p, f, &win, time.Now().Add(-time.Hour))
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.accounting_records
		(tenant_id,site_id,session_id,sample_seq,bytes_up,bytes_down,sampled_at) VALUES ($1,$2,$3,1,10,10,now())`,
		f.tenant, f.site, sess); err != nil {
		t.Fatal(err)
	}
	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("the sweep ended %d still-valid entitlements", len(due))
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND status='ACTIVE'`, ent); n != 1 {
		t.Fatal("a valid entitlement was terminated by the sweep")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state='active'`, sess); n != 1 {
		t.Fatal("a valid session was ended by the sweep")
	}
}
