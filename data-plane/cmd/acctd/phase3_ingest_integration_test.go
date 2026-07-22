//go:build integration

package main

// Composition-root tests for Phase-3 accounting ingestion, with non-zero synthetic counter deltas driven
// through the SAME entry point acctd's tick uses, against a real PostgreSQL 16. These cover the cases where
// usage is easy to lose, double-count or attribute to the wrong Entitlement.

import (
	"context"
	"os"
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

// openSession creates a session on a bridge. The interface and address are set AT CREATION because they are
// accounting identity: the controlled operation re-derives the counter source from them, and the writer
// boundary refuses a later UPDATE of either.
func (f *ingestFixture) openSession(t *testing.T, ent, device string, startedAt time.Time, ip string) string {
	return f.openSessionOn(t, ent, device, startedAt, ip, "br-guest")
}

func (f *ingestFixture) openSessionOn(t *testing.T, ent, device string, startedAt time.Time, ip, bridge string) string {
	t.Helper()
	var sess string
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO iam_v2.sessions
		(tenant_id,site_id,entitlement_id,device_id,state,started,ip,ingress_interface)
		VALUES ($1,$2,$3,$4,'active',$5,$6::inet,$7)
		RETURNING id::text`, f.tenant, f.site, ent, device, startedAt, ip, bridge).Scan(&sess); err != nil {
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

// sessionTotals reads the Session's own running usage — the number a refused sample must never move.
func (f *ingestFixture) sessionTotals(t *testing.T, sess string) (up, down int64) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT bytes_up, bytes_down FROM iam_v2.sessions WHERE id=$1`, sess).Scan(&up, &down); err != nil {
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

// grantEntitlementFor creates an ACTIVE entitlement for a specific device's stay context.
func (f *ingestFixture) grantEntitlementFor(t *testing.T, device string, activatedAt time.Time) string {
	t.Helper()
	// the previous entitlement must be terminated first: ent_live_stay allows one live entitlement per Stay
	if _, err := f.pool.Exec(context.Background(),
		`SELECT iam_v2.terminate_entitlement_at_boundary(e.id, now() - interval '1 minute', 'ADMIN')
		   FROM iam_v2.entitlements e WHERE e.stay_id=$1 AND e.status IN ('ACTIVE','PENDING','SUSPENDED')`,
		f.stay); err != nil {
		t.Fatalf("terminate previous entitlement: %v", err)
	}
	return f.grantEntitlement(t, activatedAt, nil)
}
