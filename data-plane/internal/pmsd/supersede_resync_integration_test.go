//go:build integration

package pmsd

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Audit AD-38: the reducer, run as pmsd's own login (svc_pmsd), empties only middle copies of a record that exists earlier and
// later in an older, fully applied generation, and nothing else.
func TestIntegration_AD38_SupersededRosterCopiesKeepTheirHashNotTheirText(t *testing.T) {
	dsn, pmsdDSN := os.Getenv("PHASE3_TEST_DSN"), os.Getenv("ROOMCHARGE_PMSD_DSN")
	if dsn == "" || pmsdDSN == "" {
		t.Skip("PHASE3_TEST_DSN / ROOMCHARGE_PMSD_DSN not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	svc, err := pgxpool.New(ctx, pmsdDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	var tenant, site, iface string
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `WITH
	  t  AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM gen_random_uuid() g RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, gen_random_uuid() g RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id, 'protel-fias','ACTIVE' FROM si RETURNING id, tenant_id, site_id),
	  rt AS (INSERT INTO iam_v2.pms_interface_runtime(tenant_id,site_id,pms_interface_id,runtime_generation,credential_mode,published_resync_generation,resync_generation_seq)
	         SELECT tenant_id, site_id, id, 1, 'NONE', 5, 5 FROM pi RETURNING 1)
	SELECT tenant_id::text, site_id::text, id::text FROM pi`).Scan(&tenant, &site, &iface); err != nil {
		t.Fatal(err)
	}
	// One guest present unchanged in generations 1-5, one that changed at generation 3.
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_events
	  (tenant_id, site_id, pms_interface_id, external_event_identity, event_type, payload, received_at, sequence_version,
	   admission_kind, admission_runtime_generation, resync_generation, processing_status, processed_at)
	SELECT $1::uuid, $2::uuid, $3::uuid, 'g' || g || '-' || gen, 'GI',
	       jsonb_build_object('reservation','R'||g,'room', CASE WHEN g = 2 AND gen >= 3 THEN '9' ELSE '1' END),
	       now(), gen, 'RESYNC', 1, gen, 'APPLIED', now()
	  FROM generate_series(1,2) g, generate_series(1,5) gen`, tenant, site, iface); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	n, err := NewPgRepoFromPool(svc).SupersedeResyncPayloads(ctx, tenant, site)
	if err != nil {
		t.Fatalf("as svc_pmsd: %v", err)
	}
	// guest 1: generations 2, 3, 4 are middle copies (3); guest 2: room 1 in gens 1-2 (no middle), room 9 in gens 3-5 (gen 4).
	if n != 4 {
		t.Fatalf("reduced %d, want 4", n)
	}
	var whole int
	if err := admin.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.stay_events WHERE pms_interface_id=$1 AND payload <> '{}'::jsonb`, iface).Scan(&whole); err != nil || whole != 6 {
		t.Fatalf("whole copies left: %d %v (first and newest copies and every changed record stay)", whole, err)
	}
	if n2, err := NewPgRepoFromPool(svc).SupersedeResyncPayloads(ctx, tenant, site); err != nil || n2 != 0 {
		t.Fatalf("a second pass reduces nothing: %d %v", n2, err)
	}
}
