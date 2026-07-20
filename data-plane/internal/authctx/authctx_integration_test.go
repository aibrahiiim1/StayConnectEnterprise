//go:build integration

package authctx

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
// Auth Context needs. The revision's max_auth_cache_age_seconds is 3600; occupancy evidence is fresh (now()).
func seed(t *testing.T, p *pgxpool.Pool) fixture { return seedCacheAge(t, p, "3600") }

// seedCacheAge is seed with an explicit max_auth_cache_age_seconds JSON value (e.g. "3600", "\"abc\"",
// "null"), so the immutable revision carries the (possibly malformed) config from creation.
func seedCacheAge(t *testing.T, p *pgxpool.Pool, cacheAgeJSON string) fixture {
	return seedAged(t, p, cacheAgeJSON, "now()", false)
}

// seedAged builds the fixture with an explicit occupancy-evidence timestamp EXPRESSION (e.g. "now()" or
// "now() - interval '2 hours'") and clock-suspect flag, both fixed at seed time. Because the migration's
// evidence-version guard now rejects mutating occupancy fields without the required version transition, tests
// that need a stale / clock-suspect Stay must seed it that way (and prove the ISSUANCE-time guard rejects it),
// rather than mutating a fresh Stay after issue.
func seedAged(t *testing.T, p *pgxpool.Pool, cacheAgeJSON, evidenceAtExpr string, clockSuspect bool) fixture {
	t.Helper()
	ctx := context.Background()
	cfg := `{"endpoint":"x","max_auth_cache_age_seconds":` + cacheAgeJSON + `}`
	// evidenceAtExpr and clockSuspect are test-controlled literals spliced into the seed DDL (never guest input).
	suspect := "false"
	if clockSuspect {
		suspect = "true"
	}
	sql := `WITH
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
	           ` + evidenceAtExpr + `, now(), pr.id, 1, ` + suspect + `, 1 FROM pi, pr RETURNING id),
	  dv AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, gen_random_uuid(), '02:00:00:00:00:01'::macaddr FROM pi RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM pr)::text, (SELECT id FROM st)::text, (SELECT id FROM dv)::text, (SELECT id FROM gn)::text`
	var f fixture
	if err := p.QueryRow(ctx, sql, cfg).
		Scan(&f.tenant, &f.site, &f.iface, &f.rev, &f.stay, &f.device, &f.network); err != nil {
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
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now() WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(context.Background(), id2, pres(f)); err != ErrContextInvalid {
		t.Fatalf("checked-out stay consume = %v, want ErrContextInvalid", err)
	}
}

// TestIntegration_EvidenceAndInterfacePins proves the pinned Interface / occupancy-evidence guards:
//   - a context whose pinned Interface is disabled AFTER issue is rejected at consume;
//   - a Stay whose occupancy evidence is already STALE or CLOCK-SUSPECT at issue never yields a context
//     (issuance fails closed — an unusable context is never persisted);
//   - an authoritative occupancy-evidence replacement (material change + monotonic version bump) invalidates
//     an already-issued context at consume.
func TestIntegration_EvidenceAndInterfacePins(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := NewStore(p)
	ctx := context.Background()

	// disabled interface AFTER issue → consume rejected
	f := seed(t, p)
	id, _ := s.IssuePMS(ctx, grant(f, 600))
	if _, err := p.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET lifecycle_state='AUTH_DISABLED' WHERE id=$1`, f.iface); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != ErrContextInvalid {
		t.Fatalf("disabled interface = %v, want ErrContextInvalid", err)
	}

	// stale occupancy evidence at issue (2h old, older than max_auth_cache_age=3600s) → issuance fails closed
	fs := seedAged(t, p, "3600", "now() - interval '2 hours'", false)
	before := authCtxCount(t, p, fs)
	if _, err := s.IssuePMS(ctx, grant(fs, 600)); err != ErrGrantIncomplete {
		t.Fatalf("stale-at-issue = %v, want ErrGrantIncomplete", err)
	}
	if after := authCtxCount(t, p, fs); after != before {
		t.Fatalf("stale-at-issue must not persist a context: before=%d after=%d", before, after)
	}

	// clock-suspect occupancy evidence at issue → issuance fails closed
	fc := seedAged(t, p, "3600", "now()", true)
	if _, err := s.IssuePMS(ctx, grant(fc, 600)); err != ErrGrantIncomplete {
		t.Fatalf("clock-suspect-at-issue = %v, want ErrGrantIncomplete", err)
	}

	// authoritative evidence replacement after issue (material change + exactly-+1 version) → consume rejected
	f = seed(t, p)
	id, _ = s.IssuePMS(ctx, grant(f, 600))
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays
		SET occupancy_evidence_at = now() + interval '1 second', occupancy_evidence_version = occupancy_evidence_version + 1
		WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != ErrContextInvalid {
		t.Fatalf("evidence replacement (version bump) = %v, want ErrContextInvalid", err)
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

	// same evidence version but DIFFERENT occupancy Revision → rejected by the consume-time PROVENANCE clause.
	// The evidence-version trigger normally forces ANY material change (incl. a Revision change) to bump the
	// version, so a same-version/different-Revision state is unreachable via legal mutation. We bypass the
	// trigger inside a single tx (SET LOCAL session_replication_role = replica) precisely to construct that
	// illegal state and prove the provenance clause rejects it even though the pinned version still matches.
	f = seed(t, p)
	rev2 := secondRevision(t, p, f)
	id, _ = s.IssuePMS(ctx, grant(f, 600))
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET occupancy_revision_id=$2 WHERE id=$1`, f.stay, rev2); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != ErrContextInvalid {
		t.Fatalf("different occupancy revision (same version) must be rejected by provenance, got %v", err)
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
		// empty
		func(g *PMSGrant) { g.Tenant = "" }, func(g *PMSGrant) { g.Site = "" },
		func(g *PMSGrant) { g.Interface = "" }, func(g *PMSGrant) { g.Revision = "" },
		func(g *PMSGrant) { g.Stay = "" }, func(g *PMSGrant) { g.Device = "" },
		func(g *PMSGrant) { g.GuestNetwork = "" },
		// malformed UUID → typed error BEFORE SQL, never a raw cast error
		func(g *PMSGrant) { g.Stay = "not-a-uuid" },
		// whitespace-only
		func(g *PMSGrant) { g.Site = "   " },
		// overlong
		func(g *PMSGrant) { g.Interface = g.Interface + "x" },
		// nil UUID is not a real identity pin
		func(g *PMSGrant) { g.Device = "00000000-0000-0000-0000-000000000000" },
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
		// no supplied identifier may appear in the error text
		if err != nil {
			for _, id := range []string{f.stay, f.iface, f.rev, f.device, f.network, f.tenant, f.site} {
				if strings.Contains(err.Error(), id) {
					t.Fatalf("error text leaked an identifier: %v", err)
				}
			}
		}
	}
	if after := authCtxCount(t, p, f); after != before {
		t.Fatalf("incomplete issuance inserted rows: before=%d after=%d", before, after)
	}

	// malformed / nil consume id + malformed presenter → uniform ErrContextInvalid before SQL, no row consumed
	valid, err := s.IssuePMS(context.Background(), g0)
	if err != nil {
		t.Fatalf("valid issue must succeed: %v", err)
	}
	for _, badID := range []string{"", "   ", "not-a-uuid", "00000000-0000-0000-0000-000000000000", valid + "x"} {
		if _, err := s.Consume(context.Background(), badID, pres(f)); err != ErrContextInvalid {
			t.Fatalf("consume(badID=%q) = %v, want ErrContextInvalid", badID, err)
		}
	}
	badPres := pres(f)
	badPres.Device = "nope"
	if _, err := s.Consume(context.Background(), valid, badPres); err != ErrContextInvalid {
		t.Fatalf("consume(malformed presenter) = %v, want ErrContextInvalid", err)
	}
	// the real context survived every malformed attempt (none consumed it) → still consumable exactly once
	if _, err := s.Consume(context.Background(), valid, pres(f)); err != nil {
		t.Fatalf("valid context must still consume after malformed attempts: %v", err)
	}
}

// TestIntegration_FreshnessConfigFailClosed proves a malformed / out-of-range max_auth_cache_age config never
// causes a cast error or an unbounded window: it falls back to the strict 300s default.
func TestIntegration_FreshnessConfigFailClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := NewStore(p)
	ctx := context.Background()

	// Freshness is now revalidated at ISSUANCE (an already-stale Stay must never yield a persisted context), so
	// the fail-closed parse is proven there. INVALID / out-of-range / overflow-sized values → strict 300s
	// default (never a cast error, never widened to a large window).
	for _, cfg := range []string{`"abc"`, `"-5"`, `"0"`, `null`, `604801`, `2147483648`, `99999999999999999999`} {
		// fresh evidence (within the 300s default) → issues AND consumes: proves no SQL cast error, default applied
		f := seedCacheAge(t, p, cfg)
		id, err := s.IssuePMS(ctx, grant(f, 600))
		if err != nil {
			t.Fatalf("cfg=%s fresh issue: %v", cfg, err)
		}
		if _, err := s.Consume(ctx, id, pres(f)); err != nil {
			t.Fatalf("cfg=%s fresh consume: %v", cfg, err)
		}
		// evidence 10min old (> 300s default) → issuance fails closed: proves the invalid value was NOT widened
		f2 := seedAged(t, p, cfg, "now() - interval '10 minutes'", false)
		before := authCtxCount(t, p, f2)
		if _, err := s.IssuePMS(ctx, grant(f2, 600)); err != ErrGrantIncomplete {
			t.Fatalf("cfg=%s stale-beyond-default = %v, want ErrGrantIncomplete (invalid value must not widen)", cfg, err)
		}
		if after := authCtxCount(t, p, f2); after != before {
			t.Fatalf("cfg=%s stale-at-issue must not persist a context: before=%d after=%d", cfg, before, after)
		}
	}

	// VALID values are honored exactly (not defaulted, not truncated).
	// 300: evidence 5min old is stale (>300s) → issuance rejected.
	f300 := seedAged(t, p, "300", "now() - interval '5 minutes'", false)
	if _, err := s.IssuePMS(ctx, grant(f300, 600)); err != ErrGrantIncomplete {
		t.Fatalf("cfg=300 evidence 5min old must be stale at issue, got %v", err)
	}
	// 604800: evidence 2h old is fresh (within 7 days) → issues AND consumes.
	f7d := seedAged(t, p, "604800", "now() - interval '2 hours'", false)
	id7, err := s.IssuePMS(ctx, grant(f7d, 600))
	if err != nil {
		t.Fatalf("cfg=604800 evidence 2h old must issue: %v", err)
	}
	if _, err := s.Consume(ctx, id7, pres(f7d)); err != nil {
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
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now() WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET status='IN_HOUSE', lifecycle_version=lifecycle_version+1, effective_checkout_at=NULL WHERE id=$1`, f.stay); err != nil {
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
	// material evidence change (evidence_at) + exactly-+1 version bump — the trigger-legal replacement path.
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays
		SET occupancy_evidence_at = now() + interval '1 second', occupancy_evidence_version = occupancy_evidence_version + 1
		WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, stale, pres(f)); err != ErrContextInvalid {
		t.Fatalf("evidence-replaced old context must be rejected, got %v", err)
	}
}

// lockStayFirst mimics a Checkout/evidence writer that correctly obeys L1: it locks the Stay row FIRST
// (SELECT ... FOR UPDATE) inside the returned open transaction. The caller then mutates + commits/rolls back.
func lockStayFirst(t *testing.T, p *pgxpool.Pool, stay string) pgx.Tx {
	t.Helper()
	tx, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `SELECT 1 FROM iam_v2.stays WHERE id=$1 FOR UPDATE`, stay); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	return tx
}

// TestIntegration_ConsumeStayFirstLockOrder proves ConsumeTx obeys the global L1 Stay-first lock order, so it
// serializes deterministically against Checkout / evidence replacement and never races them.
func TestIntegration_ConsumeStayFirstLockOrder(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := NewStore(p)
	ctx := context.Background()

	// (a) Checkout takes the Stay lock FIRST → a concurrent consume WAITS on it, then REJECTS after commit.
	f := seed(t, p)
	id, _ := s.IssuePMS(ctx, grant(f, 600))
	txCk := lockStayFirst(t, p, f.stay)
	if _, err := txCk.Exec(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now() WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	consumeErr := make(chan error, 1)
	go func() { _, e := s.Consume(context.Background(), id, pres(f)); consumeErr <- e }()
	select {
	case e := <-consumeErr:
		t.Fatalf("consume returned %v before checkout committed (did not wait on the Stay lock)", e)
	case <-time.After(500 * time.Millisecond):
	}
	if err := txCk.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if e := <-consumeErr; e != ErrContextInvalid {
		t.Fatalf("post-checkout consume = %v, want ErrContextInvalid", e)
	}

	// (b) Consume takes the Stay lock FIRST (inside a caller tx that then runs commerce) → a concurrent
	//     Checkout WAITS until that whole transaction commits/rolls back.
	f = seed(t, p)
	id, _ = s.IssuePMS(ctx, grant(f, 600))
	txC, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeTx(ctx, txC, id, pres(f)); err != nil {
		t.Fatalf("ConsumeTx (stay-first): %v", err)
	}
	ckDone := make(chan error, 1)
	go func() {
		tx2, e := p.Begin(context.Background())
		if e != nil {
			ckDone <- e
			return
		}
		if _, e = tx2.Exec(context.Background(), `SELECT 1 FROM iam_v2.stays WHERE id=$1 FOR UPDATE`, f.stay); e != nil {
			_ = tx2.Rollback(context.Background())
			ckDone <- e
			return
		}
		ckDone <- tx2.Commit(context.Background())
	}()
	select {
	case e := <-ckDone:
		t.Fatalf("checkout acquired the Stay lock (%v) while consume held it — lock order violated", e)
	case <-time.After(500 * time.Millisecond):
	}
	if err := txC.Commit(ctx); err != nil { // commerce succeeds
		t.Fatal(err)
	}
	if e := <-ckDone; e != nil {
		t.Fatalf("checkout after consume commit: %v", e)
	}

	// (c) Evidence replacement takes the Stay lock FIRST and commits → the old context REJECTS.
	f = seed(t, p)
	id, _ = s.IssuePMS(ctx, grant(f, 600))
	txEv := lockStayFirst(t, p, f.stay)
	if _, err := txEv.Exec(ctx, `UPDATE iam_v2.stays
		SET occupancy_evidence_at = now() + interval '1 second', occupancy_evidence_version = occupancy_evidence_version + 1
		WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	if err := txEv.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, id, pres(f)); err != ErrContextInvalid {
		t.Fatalf("post-evidence-replacement consume = %v, want ErrContextInvalid", err)
	}

	// (d) Failed commerce rolls the consumption back atomically — the context stays consumable.
	f = seed(t, p)
	id, _ = s.IssuePMS(ctx, grant(f, 600))
	txF, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeTx(ctx, txF, id, pres(f)); err != nil {
		t.Fatalf("ConsumeTx: %v", err)
	}
	_ = txF.Rollback(ctx) // commerce failed
	if _, err := s.Consume(ctx, id, pres(f)); err != nil {
		t.Fatalf("after rolled-back commerce the context must remain consumable: %v", err)
	}
}

// TestIntegration_MixedCheckoutConsumeNoDeadlock runs ≥24 mixed Checkout-cycle / Consume operations against the
// SAME Stay concurrently. Because every operation acquires the single Stay row lock FIRST (L1), there is a total
// lock order and NO deadlock is possible; every operation completes and none returns a deadlock (SQLSTATE
// 40P01). Consumers may legitimately reject (ErrContextInvalid) when a concurrent reinstatement bumped the
// episode first — that is correct, not a failure.
func TestIntegration_MixedCheckoutConsumeNoDeadlock(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := NewStore(p)
	ctx := context.Background()

	f := seed(t, p)
	const consumers = 12
	ids := make([]string, consumers)
	for i := range ids {
		id, err := s.IssuePMS(ctx, grant(f, 600))
		if err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
		ids[i] = id
	}

	// a Stay-first checkout→reinstate cycle: keeps the Stay IN_HOUSE but bumps lifecycle_version (invalidating
	// contexts). Holding the Stay lock across both statements serializes cleanly with everyone else.
	mutate := func() error {
		tx, e := p.Begin(context.Background())
		if e != nil {
			return e
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		var st string
		if e := tx.QueryRow(context.Background(), `SELECT status FROM iam_v2.stays WHERE id=$1 FOR UPDATE`, f.stay).Scan(&st); e != nil {
			return e
		}
		if st == "IN_HOUSE" {
			if _, e := tx.Exec(context.Background(), `UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now() WHERE id=$1`, f.stay); e != nil {
				return e
			}
			if _, e := tx.Exec(context.Background(), `UPDATE iam_v2.stays SET status='IN_HOUSE', lifecycle_version=lifecycle_version+1, effective_checkout_at=NULL WHERE id=$1`, f.stay); e != nil {
				return e
			}
		}
		return tx.Commit(context.Background())
	}

	const mutators = 12
	errs := make(chan error, consumers+mutators)
	var wg sync.WaitGroup
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, e := s.Consume(context.Background(), id, pres(f))
			if e != nil && e != ErrContextInvalid { // ErrContextInvalid is a legitimate outcome
				errs <- e
			}
		}(ids[i])
	}
	for i := 0; i < mutators; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := mutate(); e != nil {
				errs <- e
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("mixed checkout/consume did not complete in time — possible deadlock/livelock")
	}
	close(errs)
	for e := range errs {
		if strings.Contains(strings.ToLower(e.Error()), "deadlock") {
			t.Fatalf("deadlock detected under mixed operations: %v", e)
		}
		t.Fatalf("unexpected error under mixed operations: %v", e)
	}
}
