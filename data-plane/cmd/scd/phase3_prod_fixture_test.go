//go:build integration && prodprivilege

package main

// A FIXTURE THE REAL BASELINE ACCEPTS.
//
// The shared newAuthFixture cannot seed this database, and that is not a bug in it — it targets the DARK
// schema, which is deliberately looser. Three production rules break it:
//
//   * public.tenants declares slug and name NOT NULL; the DARK table carries only id.
//   * guest_networks_bridge_uniq and guest_networks_untagged_parent_uniq mean every fixture needs its own
//     bridge and parent interface. The shared one hardcodes 'br-p3' on 'ens192', so the second fixture in a
//     run collides with the first.
//   * The writerguard refuses a direct INSERT into the stay family unless the transaction has opened a
//     controlled operation. The shared fixture seeds stays on a pooled query.
//
// Earlier attempts relaxed the schema instead — defaults on the platform tables, disabled triggers, a dropped
// unique constraint. Every one of those made this database less like production, which defeats the only
// reason it exists. Satisfying the rules is the point, so this fixture satisfies them: it supplies the
// required columns, derives unique names from the per-fixture subnet the harness already allocates, and opens
// a controlled operation exactly as real code does before touching a Stay.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newProdAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Fatal("PHASE3_TEST_DSN not set; the production privilege harness must supply it")
	}
	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)

	claimFreeFixtureOctet(t, p)
	f := &authFixture{pool: p, net: nextFixtureNet()}
	// The subnet is already unique per fixture, so it is the natural source for the two names production
	// requires to be unique. Dots and slashes are not valid in an interface name.
	uniq := strings.NewReplacer("/", "-", ".", "-").Replace(f.net.subnet)

	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Satisfy the writerguard rather than disable it: this is the call real code makes before touching the
	// stay family, so the fixture obeys the rule the product enforces instead of switching it off.
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatalf("open controlled operation: %v", err)
	}

	const seed = `WITH
	  t AS (INSERT INTO public.tenants(id, slug, name)
	        VALUES (gen_random_uuid(), 'prodpriv-' || $4::text, 'prodpriv fixture') RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  gn AS (INSERT INTO public.guest_networks
	           (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,enabled)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'p3-guests','ens-' || $4::text, 'br-' || $4::text,
	                ($1::text)::inet, ($2::text)::inet, ($3::text)::cidr, true FROM si RETURNING id,tenant_id,site_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), gn.tenant_id, gn.site_id,'protel-fias','ACTIVE' FROM gn RETURNING id,tenant_id,site_id),
	  pir AS (INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
	          SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,1,'UTC','{}'::jsonb FROM pi RETURNING id),
	  m AS (INSERT INTO iam_v2.guest_network_pms_map(tenant_id,site_id,guest_network_id,pms_interface_id,is_default)
	        SELECT gn.tenant_id, gn.site_id, gn.id, pi.id, true FROM gn, pi RETURNING guest_network_id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
	                                  normalized_room_number,status,lifecycle_version,last_applied_event_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,'RES-' || $4::text,'STAY-' || $4::text,
	                '412','IN_HOUSE',1,0 FROM pi RETURNING id,tenant_id,site_id,pms_interface_id),
	  sg AS (INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,last_name_norm,is_primary)
	         SELECT st.tenant_id, st.site_id, st.pms_interface_id, st.id,'OKONKWO',true FROM st RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'p3-plan',true FROM pi RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,
	                                                    max_concurrent_devices,device_limit_policy,time_accounting_mode)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id,1,9000,4000,3,'REJECT_NEW_DEVICE','VALIDITY_WINDOW' FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'STAY_INCLUDED',false,true FROM pi RETURNING id,tenant_id,site_id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,
	                                                        package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.id,1,spr.id,'FREE_STAY',0,ARRAY['NOT_REQUIRED']::text[],
	                 '{"mode":"VALIDITY_WINDOW","seconds":86400}'::jsonb FROM ip, spr RETURNING id),
	  pip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	          SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'PREMIUM_PAID',false,true FROM pi RETURNING id,tenant_id,site_id),
	  pipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,
	                                                         package_type,price_minor,settlement_methods,duration_policy)
	           SELECT gen_random_uuid(), pip.tenant_id, pip.site_id, pip.id,1,spr.id,'GENERAL',1500,ARRAY['PMS_CHARGE']::text[],
	                  '{"mode":"VALIDITY_WINDOW","seconds":86400}'::jsonb FROM pip, spr RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM pir)::text, (SELECT guest_network_id FROM m)::text,
	       (SELECT id FROM st)::text, (SELECT id FROM ipr)::text, (SELECT id FROM pipr)::text`

	if err := tx.QueryRow(ctx, seed, f.net.gateway+"/24", f.net.gateway, f.net.subnet, uniq).
		Scan(&f.tenant, &f.site, &f.iface, &f.revision, &f.network, &f.stay, &f.pkgRev, &f.priced); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Fresh occupancy evidence from the pinned Revision, and a healthy feed. Without both, nothing authorises
	// and every test below would fail for a reason that has nothing to do with privilege.
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays
		SET occupancy_evidence_at=now(), occupancy_ingested_at=now(), occupancy_revision_id=$2::uuid,
		    occupancy_normalization_version=1, occupancy_clock_suspect=false, occupancy_evidence_version=1
		WHERE id=$1::uuid`, f.stay, f.revision); err != nil {
		t.Fatalf("stamp occupancy evidence: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id, site_id, pms_interface_id, runtime_generation, credential_mode, published_resync_generation,
		 pinned_revision_id, transport_status, sync_status, continuity_status, last_connected_at,
		 last_heartbeat_at, last_complete_sync_at)
		VALUES ($1,$2,$3,1,'NONE',0,$4,'CONNECTED','IN_SYNC','CONTINUOUS',now(),now(),now())`,
		f.tenant, f.site, f.iface, f.revision); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return f
}
