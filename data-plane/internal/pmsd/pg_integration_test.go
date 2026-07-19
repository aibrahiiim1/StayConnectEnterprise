//go:build integration

package pmsd

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests require a disposable PostgreSQL 16 with the accepted iam_v2 schema + migration 0010 applied,
// reachable via PHASE3_TEST_DSN. scripts/pmsd-pg-integration.sh builds it. They FAIL (not skip) if the DSN
// is set but the database is unreachable/misbuilt; they skip ONLY when no DSN is configured (local dev).

func integPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping pmsd PG16 integration (CI sets it)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect PHASE3_TEST_DSN: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PHASE3_TEST_DSN: %v", err)
	}
	if n := scalarInt(t, pool, "SELECT count(*) FROM information_schema.schemata WHERE schema_name='iam_v2'"); n != 1 {
		t.Fatalf("iam_v2 schema not present (schema not built)")
	}
	return pool
}

func scalarInt(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

type seeded struct {
	tenant, site, iface, rev, sg string
	otherTenant                  string
}

// seedScope inserts a tenant/site/interface/revision(+full config)/secret-generation and returns the ids.
func seedScope(t *testing.T, pool *pgxpool.Pool) seeded {
	t.Helper()
	ctx := context.Background()
	var s seeded
	err := pool.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  ot AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state,current_revision_id)
	         SELECT gen_random_uuid(), si.tenant_id, si.id, 'protel-fias', 'ACTIVE', NULL FROM si RETURNING id,tenant_id,site_id),
	  pr AS (INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 1, 'UTC',
	           '{"endpoint":"127.0.0.1:15010","auth":{"read_only":true},"dial_timeout_ms":1000,"read_timeout_ms":1000,"write_timeout_ms":1000,"heartbeat_interval_ms":1000,"heartbeat_timeout_ms":2000,"feed_freshness_ms":60000,"complete_sync_ms":300000,"resync_supported":true}'::jsonb
	         FROM pi RETURNING id, pms_interface_id),
	  sg AS (INSERT INTO iam_v2.pms_interface_secret_generations(id,tenant_id,site_id,pms_interface_id,generation_no,ciphertext,nonce,encryption_key_id,cipher_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 1, '\x00'::bytea, '\x00'::bytea, gen_random_uuid(), 1 FROM pi RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM pr)::text, (SELECT id FROM sg)::text, (SELECT id FROM ot)::text`).
		Scan(&s.tenant, &s.site, &s.iface, &s.rev, &s.sg, &s.otherTenant)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// set current_revision_id in a SEPARATE statement (a same-statement CTE cannot update the row another
	// CTE just inserted).
	if _, err := pool.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$1 WHERE id=$2`, s.rev, s.iface); err != nil {
		t.Fatalf("seed set current_revision_id: %v", err)
	}
	return s
}

func TestIntegration_AssignmentScopeAndCrossScope(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)

	got, err := repo.ListActiveInterfaces(context.Background(), s.tenant, s.site)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range got {
		if i.ID == s.iface {
			found = true
		}
	}
	if !found {
		t.Fatal("assignment-scoped discovery did not return the seeded interface")
	}
	// cross-scope: the other tenant sees nothing for this site
	other, err := repo.ListActiveInterfaces(context.Background(), s.otherTenant, s.site)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range other {
		if i.ID == s.iface {
			t.Fatal("cross-scope query returned another tenant's interface")
		}
	}
}

func TestIntegration_LoadInterfaceTypedRevisionAndSecret(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)

	_, rev, sg, err := repo.LoadInterface(context.Background(), s.tenant, s.site, s.iface)
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Validate(); err != nil {
		t.Fatalf("seeded revision must validate: %v -- rev=%+v", err, rev)
	}
	if sg.ID != s.sg || rev.ActiveSecretGenerationID != s.sg {
		t.Fatalf("active secret generation mismatch: sg=%s rev.sg=%s want %s", sg.ID, rev.ActiveSecretGenerationID, s.sg)
	}
	if !rev.ReadOnly {
		t.Fatal("read-only capability must come through as true")
	}
}

func TestIntegration_AtomicGenerationAndStaleCAS(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	req := GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg}

	genA, err := repo.AllocateRuntimeGeneration(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	genB, err := repo.AllocateRuntimeGeneration(context.Background(), req) // restart
	if err != nil {
		t.Fatal(err)
	}
	if genB != genA+1 {
		t.Fatalf("restart must obtain N+1: genA=%d genB=%d", genA, genB)
	}
	base := axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface}
	// stale owner A rejected
	ua := TransportUpdate{axisBase: base, Status: TransportConnecting}
	ua.ExpectedGeneration = genA
	if err := repo.UpdateTransport(context.Background(), ua); err != ErrStaleGeneration {
		t.Fatalf("stale owner A must be rejected with ErrStaleGeneration, got %v", err)
	}
	// current owner B accepted
	ub := TransportUpdate{axisBase: base, Status: TransportConnecting}
	ub.ExpectedGeneration = genB
	if err := repo.UpdateTransport(context.Background(), ub); err != nil {
		t.Fatalf("current owner B must update: %v", err)
	}
}

func TestIntegration_IndependentAxisPreservation(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	req := GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg}
	gen, err := repo.AllocateRuntimeGeneration(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	base := axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, ExpectedGeneration: gen}
	now := time.Now()
	// establish CONNECTED (with last_connected_at, required by the pir_connected_pins CHECK)
	if err := repo.UpdateTransport(context.Background(), TransportUpdate{axisBase: base, Status: TransportConnected, LastConnectedAt: &now}); err != nil {
		t.Fatal(err)
	}
	// establish continuity + sync
	if err := repo.UpdateContinuity(context.Background(), ContinuityUpdate{axisBase: base, Status: ContinuityContinuous, LastValidEventAt: &now, LastEventCursor: "cur-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSync(context.Background(), SyncUpdate{axisBase: base, Status: SyncInSync, LastCompleteSyncAt: &now, SyncCursor: "sync-1"}); err != nil {
		t.Fatal(err)
	}
	// a transport heartbeat must NOT erase continuity/sync
	if err := repo.UpdateTransport(context.Background(), TransportUpdate{axisBase: base, Status: TransportConnected, LastHeartbeatAt: &now}); err != nil {
		t.Fatal(err)
	}
	var cont, syn, cur, scur string
	if err := pool.QueryRow(context.Background(), `SELECT continuity_status, sync_status, COALESCE(last_event_cursor,''), COALESCE(sync_cursor,'')
		FROM iam_v2.pms_interface_runtime WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3`,
		s.tenant, s.site, s.iface).Scan(&cont, &syn, &cur, &scur); err != nil {
		t.Fatal(err)
	}
	if cont != "CONTINUOUS" || syn != "IN_SYNC" || cur != "cur-1" || scur != "sync-1" {
		t.Fatalf("heartbeat erased another axis: continuity=%s sync=%s cursor=%s syncCursor=%s", cont, syn, cur, scur)
	}
}

func TestIntegration_RealAdvisoryLockCompetition(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	key, err := LockKey(s.tenant, s.site, s.iface)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewPgLocker(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewPgLocker(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	gotA, err := a.TryLock(context.Background(), key)
	if err != nil || !gotA {
		t.Fatalf("owner A must acquire the lock: got=%v err=%v", gotA, err)
	}
	gotB, err := b.TryLock(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if gotB {
		t.Fatal("competing owner B must NOT acquire the same advisory lock")
	}
	// after A releases, B can acquire
	_ = a.Close()
	gotB2, err := b.TryLock(context.Background(), key)
	if err != nil || !gotB2 {
		t.Fatalf("B must acquire after A releases: got=%v err=%v", gotB2, err)
	}
}

func TestIntegration_ZeroRuntimeGrantsWhileDark(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	// no runtime service role (svc_*) may hold any privilege on the runtime/stay tables while DARK
	n := scalarInt(t, pool, `SELECT count(*) FROM information_schema.role_table_grants
		WHERE table_schema='iam_v2' AND grantee LIKE 'svc_%'`)
	if n != 0 {
		t.Fatalf("expected zero svc_* grants on iam_v2 while dark, got %d", n)
	}
}
