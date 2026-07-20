//go:build integration

package authctx

import (
	"context"
	"os"
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
// Auth Context needs.
func seed(t *testing.T, p *pgxpool.Pool) fixture {
	t.Helper()
	ctx := context.Background()
	var f fixture
	err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  gn AS (INSERT INTO public.guest_networks(id,tenant_id,site_id)
	         SELECT gen_random_uuid(), si.tenant_id, si.id FROM si RETURNING id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state,current_revision_id)
	         SELECT gen_random_uuid(), si.tenant_id, si.id, 'protel-fias','ACTIVE',NULL FROM si RETURNING id,tenant_id,site_id),
	  pr AS (INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 1, 'UTC', '{"endpoint":"x"}'::jsonb FROM pi RETURNING id, pms_interface_id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 'R1','R1','IN_HOUSE',1 FROM pi RETURNING id),
	  dv AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, gen_random_uuid(), '02:00:00:00:00:01'::macaddr FROM pi RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM pr)::text, (SELECT id FROM st)::text, (SELECT id FROM dv)::text, (SELECT id FROM gn)::text`).
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
	got, err := s.Consume(context.Background(), f.tenant, f.site, id)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if got.Method != "PMS" || got.Stay != f.stay || got.Interface != f.iface {
		t.Fatalf("consume pins = %+v, want PMS/%s/%s", got, f.stay, f.iface)
	}
	// replay → rejected uniformly
	if _, err := s.Consume(context.Background(), f.tenant, f.site, id); err != ErrContextInvalid {
		t.Fatalf("replay = %v, want ErrContextInvalid", err)
	}

	// expired context (TTL 0 → expires_at = now, so expires_at > now() is false) → rejected
	expID, err := s.IssuePMS(context.Background(), grant(f, 0))
	if err != nil {
		t.Fatalf("issue expired: %v", err)
	}
	if _, err := s.Consume(context.Background(), f.tenant, f.site, expID); err != ErrContextInvalid {
		t.Fatalf("expired consume = %v, want ErrContextInvalid", err)
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
			if _, err := s.Consume(context.Background(), f.tenant, f.site, id); err == nil {
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
