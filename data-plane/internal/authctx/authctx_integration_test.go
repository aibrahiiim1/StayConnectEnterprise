//go:build integration

package authctx

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping authctx PG16 integration")
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

type fixture struct{ tenant, site, iface, rev, stay, device, network string }

// seed builds tenant/site/interface/revision + an IN_HOUSE stay + a guest network, returning the pins a PMS
// Auth Context needs. The revision's max_auth_cache_age_seconds is 3600.
func seed(t *testing.T, p *pgxpool.Pool) fixture { return seedCacheAge(t, p, "3600") }

// seedCacheAge is seed with an explicit max_auth_cache_age_seconds JSON value (e.g. "3600", "\"abc\"",
// "null"), so the immutable revision carries the (possibly malformed) config from creation.
func seedCacheAge(t *testing.T, p *pgxpool.Pool, cacheAgeJSON string) fixture {
	t.Helper()
	ctx := context.Background()
	cfg := `{"endpoint":"x","max_auth_cache_age_seconds":` + cacheAgeJSON + `}`
	var f fixture
	err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  gn AS (INSERT INTO public.guest_networks(id,tenant_id,site_id)
	         SELECT gen_random_uuid(), si.tenant_id, si.id FROM si RETURNING id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state,current_revision_id)
	         SELECT gen_random_uuid(), si.tenant_id, si.id, 'protel-fias','ACTIVE',NULL FROM si RETURNING id,tenant_id,site_id),
	  pr AS (INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 1, 'UTC', $1::jsonb FROM pi RETURNING id, pms_interface_id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,
	           occupancy_evidence_at,occupancy_ingested_at,occupancy_revision_id,occupancy_normalization_version,occupancy_clock_suspect,occupancy_evidence_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 'R1','R1','IN_HOUSE',1,
	           now(), now(), pr.id, 1, false, 1 FROM pi, pr RETURNING id),
	  dv AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, gen_random_uuid(), '02:00:00:00:00:01'::macaddr FROM pi RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM pr)::text, (SELECT id FROM st)::text, (SELECT id FROM dv)::text, (SELECT id FROM gn)::text`, cfg).
		Scan(&f.tenant, &f.site, &f.iface, &f.rev, &f.stay, &f.device, &f.network)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// point the interface at its revision (separate statement — a CTE cannot update a row another CTE inserted)
	if _, err := p.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$1 WHERE id=$2`, f.rev, f.iface); err != nil {
		t.Fatalf("seed set current revision: %v", err)
	}
	return f
}

func grant(f fixture, ttl int) PMSGrant {
	return PMSGrant{Tenant: f.tenant, Site: f.site, Interface: f.iface, Revision: f.rev, Stay: f.stay,
		Device: f.device, GuestNetwork: f.network, TTLSeconds: ttl}
}

func pres(f fixture) Presenter {
	return Presenter{Tenant: f.tenant, Site: f.site, Device: f.device, GuestNetwork: f.network}
}

// TestIntegration_OneTimeConsumeAndReplay proves the core one-time semantics: a fresh context consumes once
// (returning server pins), a replay is rejected, and an expired context is rejected — uniformly.
func TestIntegration_OneTimeConsumeAndReplay(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := NewStore(p)

	id, err := s.IssuePMS(context.Background(), grant(f, 600))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := s.Consume(context.Background(), id, pres(f))
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if got.Method != "PMS" || got.Stay != f.stay || got.Interface != f.iface || got.Revision != f.rev {
		t.Fatalf("consume pins = %+v, want PMS/%s/%s/%s", got, f.stay, f.iface, f.rev)
	}
	// replay → rejected uniformly
	if _, err := s.Consume(context.Background(), id, pres(f)); err != ErrContextInvalid {
		t.Fatalf("replay = %v, want ErrContextInvalid", err)
	}

	// expired context: issue with a valid TTL, then move expires_at into the past → consume rejected.
	expID, err := s.IssuePMS(context.Background(), grant(f, 600))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.auth_contexts SET expires_at = now() - interval '1 minute' WHERE id=$1`, expID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(context.Background(), expID, pres(f)); err != ErrContextInvalid {
		t.Fatalf("expired consume = %v, want ErrContextInvalid", err)
	}
}

// TestIntegration_PinnedPresenterAndOccupancy proves a context is UNUSABLE from a different device or guest
// network, and that a PMS context whose pinned Stay is no longer IN_HOUSE cannot be consumed.
func TestIntegration_PinnedPresenterAndOccupancy(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := NewStore(p)

	// wrong device → invalid
	id, _ := s.IssuePMS(context.Background(), grant(f, 600))
	wrongDev := pres(f)
	wrongDev.Device = "00000000-0000-0000-0000-000000000000"
	if _, err := s.Consume(context.Background(), id, wrongDev); err != ErrContextInvalid {
		t.Fatalf("wrong device = %v, want ErrContextInvalid", err)
	}
	// wrong network → invalid (and the earlier failed attempt did not consume it: a correct presenter still works)
	wrongNet := pres(f)
	wrongNet.GuestNetwork = "00000000-0000-0000-0000-000000000000"
	if _, err := s.Consume(context.Background(), id, wrongNet); err != ErrContextInvalid {
		t.Fatalf("wrong network = %v, want ErrContextInvalid", err)
	}
	if _, err := s.Consume(context.Background(), id, pres(f)); err != nil {
		t.Fatalf("correct presenter after wrong attempts must still consume: %v", err)
	}

	// pinned Stay no longer IN_HOUSE → invalid
	id2, _ := s.IssuePMS(context.Background(), grant(f, 600))
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.stays SET status='CHECKED_OUT' WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(context.Background(), id2, pres(f)); err != ErrContextInvalid {
		t.Fatalf("checked-out stay consume = %v, want ErrContextInvalid", err)
	}
}

// TestIntegration_EvidenceAndInterfacePins proves ConsumeTx rejects a PMS context when the pinned Interface
// is disabled, the occupancy evidence is stale / clock-suspect / a different version, or occupancy evidence is
// absent — each a uniform ErrContextInvalid.
func TestIntegration_EvidenceAndInterfacePins(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := NewStore(p)
	ctx := context.Background()

	// disabled interface
	f := seed(t, p)
	id, _ := s.IssuePMS(ctx, grant(f, 600))
	if _, err := p.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET lifecycle_state='AUTH_DISABLED' WHERE id=$1`, f.iface); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != ErrContextInvalid {
		t.Fatalf("disabled interface = %v, want ErrContextInvalid", err)
	}

	// stale occupancy evidence (older than max_auth_cache_age=3600s)
	f = seed(t, p)
	id, _ = s.IssuePMS(ctx, grant(f, 600))
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET occupancy_evidence_at = now() - interval '2 hours' WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != ErrContextInvalid {
		t.Fatalf("stale evidence = %v, want ErrContextInvalid", err)
	}

	// clock-suspect occupancy evidence
	f = seed(t, p)
	id, _ = s.IssuePMS(ctx, grant(f, 600))
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET occupancy_clock_suspect=true WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != ErrContextInvalid {
		t.Fatalf("clock-suspect = %v, want ErrContextInvalid", err)
	}

	// monotonic occupancy-evidence version changed (no longer matches the pinned snapshot)
	f = seed(t, p)
	id, _ = s.IssuePMS(ctx, grant(f, 600))
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET occupancy_evidence_version=99 WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != ErrContextInvalid {
		t.Fatalf("evidence version mismatch = %v, want ErrContextInvalid", err)
	}
}

// TestIntegration_ConsumeTxRollback proves the consumption is ATOMIC with the caller's transaction: if the
// intended commerce transaction fails (rollback), the context is NOT left permanently consumed.
func TestIntegration_ConsumeTxRollback(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := NewStore(p)
	id, _ := s.IssuePMS(context.Background(), grant(f, 600))

	tx, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeTx(context.Background(), tx, id, pres(f)); err != nil {
		t.Fatalf("ConsumeTx: %v", err)
	}
	// the "purchase" fails → roll back; the consumption must roll back with it
	_ = tx.Rollback(context.Background())

	// the context is still usable (not permanently consumed)
	if _, err := s.Consume(context.Background(), id, pres(f)); err != nil {
		t.Fatalf("after rollback the context must remain consumable, got %v", err)
	}
}

// TestIntegration_ConcurrentConsumeSingleWinner proves that under concurrent consumption of the SAME context,
// exactly ONE caller wins (the single-row atomic UPDATE) — no double-spend.
func TestIntegration_ConcurrentConsumeSingleWinner(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := NewStore(p)
	id, err := s.IssuePMS(context.Background(), grant(f, 600))
	if err != nil {
		t.Fatal(err)
	}
	const n = 16
	var wins int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Consume(context.Background(), id, pres(f)); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("concurrent consume winners = %d, want exactly 1", wins)
	}
}

func scalar(t *testing.T, p *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("scalar %q: %v", sql, err)
	}
	return n
}

// secondRevision inserts another immutable Revision for the same interface and returns its id.
func secondRevision(t *testing.T, p *pgxpool.Pool, f fixture) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `INSERT INTO iam_v2.pms_interface_revisions
		(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
		VALUES (gen_random_uuid(),$1,$2,$3,2,'UTC','{"endpoint":"x","max_auth_cache_age_seconds":3600}'::jsonb)
		RETURNING id::text`, f.tenant, f.site, f.iface).Scan(&id); err != nil {
		t.Fatalf("second revision: %v", err)
	}
	return id
}

func authCtxCount(t *testing.T, p *pgxpool.Pool, f fixture) int {
	return scalar(t, p, `SELECT count(*) FROM iam_v2.auth_contexts WHERE pms_interface_id=$1`, f.iface)
}

// TestIntegration_OccupancyRevisionProvenance proves the context binds to the EXACT Revision that produced the
// Stay's occupancy evidence: a matching evidence-version integer under a DIFFERENT occupancy Revision is
// rejected; a newer published Revision does not by itself invalidate the immutable pinned Revision.
func TestIntegration_OccupancyRevisionProvenance(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := NewStore(p)
	ctx := context.Background()

	// exact Revision + snapshot, and PUBLISHING a newer Revision (current_revision_id → rev2) does NOT
	// invalidate the immutable pinned Revision-1 context while the Stay evidence snapshot is unchanged.
	f := seed(t, p)
	id, err := s.IssuePMS(ctx, grant(f, 600))
	if err != nil {
		t.Fatal(err)
	}
	rev2pub := secondRevision(t, p, f)
	if _, err := p.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2 WHERE id=$1`, f.iface, rev2pub); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != nil {
		t.Fatalf("published newer revision must not invalidate the pinned rev-1 context: %v", err)
	}

	// same evidence version but DIFFERENT occupancy Revision → rejected
	f = seed(t, p)
	rev2 := secondRevision(t, p, f)
	id, _ = s.IssuePMS(ctx, grant(f, 600))
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET occupancy_revision_id=$2 WHERE id=$1`, f.stay, rev2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != ErrContextInvalid {
		t.Fatalf("different occupancy revision (same version) must be rejected, got %v", err)
	}
}

// TestIntegration_IssueRejectsIncomplete proves PMS issuance fails BEFORE any INSERT when a required pin is
// missing/invalid — no unusable/already-expired context reaches the table — with a sanitized error.
func TestIntegration_IssueRejectsIncomplete(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := NewStore(p)
	f := seed(t, p)
	before := authCtxCount(t, p, f)

	g0 := grant(f, 600)
	bad := []PMSGrant{}
	zt := g0
	zt.TTLSeconds = 0
	bad = append(bad, zt)
	nt := g0
	nt.TTLSeconds = -5
	bad = append(bad, nt)
	for _, mut := range []func(*PMSGrant){
		func(g *PMSGrant) { g.Tenant = "" }, func(g *PMSGrant) { g.Site = "" },
		func(g *PMSGrant) { g.Interface = "" }, func(g *PMSGrant) { g.Revision = "" },
		func(g *PMSGrant) { g.Stay = "" }, func(g *PMSGrant) { g.Device = "" },
		func(g *PMSGrant) { g.GuestNetwork = "" },
	} {
		m := g0
		mut(&m)
		bad = append(bad, m)
	}
	for i, g := range bad {
		_, err := s.IssuePMS(context.Background(), g)
		if err != ErrGrantIncomplete {
			t.Fatalf("bad grant %d = %v, want ErrGrantIncomplete", i, err)
		}
		if err != nil && strings.Contains(err.Error(), f.stay) {
			t.Fatalf("error text leaked an identifier: %v", err)
		}
	}
	if after := authCtxCount(t, p, f); after != before {
		t.Fatalf("incomplete issuance inserted rows: before=%d after=%d", before, after)
	}
	if _, err := s.IssuePMS(context.Background(), g0); err != nil {
		t.Fatalf("valid issue must succeed: %v", err)
	}
}

// TestIntegration_FreshnessConfigFailClosed proves a malformed / out-of-range max_auth_cache_age config never
// causes a cast error or an unbounded window: it falls back to the strict 300s default.
func TestIntegration_FreshnessConfigFailClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := NewStore(p)
	ctx := context.Background()

	// INVALID / out-of-range / overflow-sized values → strict 300s default (never a cast error, never widened).
	for _, cfg := range []string{`"abc"`, `"-5"`, `"0"`, `null`, `604801`, `2147483648`, `99999999999999999999`} {
		f := seedCacheAge(t, p, cfg)
		id, err := s.IssuePMS(ctx, grant(f, 600))
		if err != nil {
			t.Fatalf("cfg=%s issue: %v", cfg, err)
		}
		// fresh evidence (within the 300s default) still consumes — proves no SQL cast error
		if _, err := s.Consume(ctx, id, pres(f)); err != nil {
			t.Fatalf("cfg=%s fresh consume: %v", cfg, err)
		}
		// evidence older than the 300s default → rejected — proves the invalid value was NOT widened
		f2 := seedCacheAge(t, p, cfg)
		id2, _ := s.IssuePMS(ctx, grant(f2, 600))
		if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET occupancy_evidence_at = now() - interval '10 minutes' WHERE id=$1`, f2.stay); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Consume(ctx, id2, pres(f2)); err != ErrContextInvalid {
			t.Fatalf("cfg=%s stale-beyond-default = %v, want ErrContextInvalid (invalid value must not widen)", cfg, err)
		}
	}

	// VALID values are honored exactly (not defaulted, not truncated).
	// 300: evidence at now()-5min is stale (>300s) → rejected.
	f := seedCacheAge(t, p, "300")
	id, _ := s.IssuePMS(ctx, grant(f, 600))
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET occupancy_evidence_at = now() - interval '5 minutes' WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != ErrContextInvalid {
		t.Fatalf("cfg=300 evidence 5min old must be stale, got %v", err)
	}
	// 604800: evidence at now()-2h is fresh (within 7 days) → consumes.
	f = seedCacheAge(t, p, "604800")
	id, _ = s.IssuePMS(ctx, grant(f, 600))
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET occupancy_evidence_at = now() - interval '2 hours' WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != nil {
		t.Fatalf("cfg=604800 evidence 2h old must be fresh: %v", err)
	}
}

// TestIntegration_EpisodeAndEvidenceSnapshot proves the context pins the exact Stay EPISODE + evidence
// snapshot: a Checkout→Reinstatement (new lifecycle_version) invalidates the old context even within TTL, a
// new context for the reinstated episode succeeds, and an authoritative evidence-version bump invalidates an
// old context under the same Revision + normalization version + IN_HOUSE.
func TestIntegration_EpisodeAndEvidenceSnapshot(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := NewStore(p)
	ctx := context.Background()

	// Checkout → Reinstatement within the original TTL → old context rejected (lifecycle_version changed).
	f := seed(t, p)
	oldID, err := s.IssuePMS(ctx, grant(f, 600))
	if err != nil {
		t.Fatal(err)
	}
	// checkout then reinstate on the same Stay row (the migration trigger enforces the +1 on CHECKED_OUT→IN_HOUSE)
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT' WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET status='IN_HOUSE', lifecycle_version=lifecycle_version+1 WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, oldID, pres(f)); err != ErrContextInvalid {
		t.Fatalf("post-reinstatement old context must be rejected, got %v", err)
	}
	// a NEW context for the reinstated episode succeeds (issuance reads the new lifecycle_version)
	newID, err := s.IssuePMS(ctx, grant(f, 600))
	if err != nil {
		t.Fatalf("issue for reinstated episode: %v", err)
	}
	if _, err := s.Consume(ctx, newID, pres(f)); err != nil {
		t.Fatalf("reinstated-episode context must consume: %v", err)
	}

	// authoritative evidence replacement (evidence_version bump) invalidates an old context, unchanged succeeds.
	f = seed(t, p)
	unchanged, _ := s.IssuePMS(ctx, grant(f, 600))
	if _, err := s.Consume(ctx, unchanged, pres(f)); err != nil {
		t.Fatalf("unchanged snapshot must consume: %v", err)
	}
	stale, _ := s.IssuePMS(ctx, grant(f, 600))
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET occupancy_evidence_version=occupancy_evidence_version+1, occupancy_ingested_at=now() WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, stale, pres(f)); err != ErrContextInvalid {
		t.Fatalf("evidence-replaced old context must be rejected, got %v", err)
	}
}
