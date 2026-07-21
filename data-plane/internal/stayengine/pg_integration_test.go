//go:build integration

package stayengine

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/checkout"
)

// These tests require a disposable PostgreSQL 16 with the accepted iam_v2 schema + migration 0010 applied,
// reachable via PHASE3_TEST_DSN (scripts/pmsd-pg-integration.sh builds it). They skip ONLY when no DSN is set.

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping stayengine PG16 integration")
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

type scope struct{ tenant, site, iface string }

func seed(t *testing.T, p *pgxpool.Pool) scope {
	t.Helper()
	ctx := context.Background()
	var s scope
	if err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state,current_revision_id)
	         SELECT gen_random_uuid(), si.tenant_id, si.id, 'protel-fias','ACTIVE',NULL FROM si RETURNING id,tenant_id,site_id)
	SELECT tenant_id::text, site_id::text, id::text FROM pi`).Scan(&s.tenant, &s.site, &s.iface); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id, site_id, pms_interface_id, runtime_generation, credential_mode, published_resync_generation)
		VALUES ($1,$2,$3,1,'NONE',0)`, s.tenant, s.site, s.iface); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	return s
}

// insertLive appends a LIVE PENDING inbox row and returns nothing (the processor consumes it next).
func insertLive(t *testing.T, p *pgxpool.Pool, s scope, identity, eventType, payloadJSON string) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `INSERT INTO iam_v2.stay_events
		(tenant_id, site_id, pms_interface_id, external_event_identity, event_type, payload,
		 admission_kind, admission_runtime_generation, resync_generation, received_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,'LIVE',1,0,now())`,
		s.tenant, s.site, s.iface, identity, eventType, payloadJSON); err != nil {
		t.Fatalf("insert event %s: %v", identity, err)
	}
}

func pay(res, room, last, first, folio, ga, gd string) string {
	return fmt.Sprintf(`{"reservation":%q,"room":%q,"last_name":%q,"first_name":%q,"folio":%q,"arrival_raw":%q,"departure_raw":%q}`,
		res, room, last, first, folio, ga, gd)
}

// process consumes exactly one pending event and asserts it was processed.
func process(t *testing.T, pr *Processor, s scope) {
	t.Helper()
	ok, err := pr.ProcessNext(context.Background(), s.tenant, s.site, s.iface)
	if err != nil {
		t.Fatalf("ProcessNext: %v", err)
	}
	if !ok {
		t.Fatal("expected a pending event to process")
	}
}

func stayState(t *testing.T, p *pgxpool.Pool, s scope, res string) (status string, lv int, room string) {
	t.Helper()
	_ = p.QueryRow(context.Background(), `SELECT status, lifecycle_version, COALESCE(normalized_room_number,'')
		FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id=$2`, s.iface, res).Scan(&status, &lv, &room)
	return
}

func eventOutcome(t *testing.T, p *pgxpool.Pool, s scope, identity string) (status, review string) {
	t.Helper()
	_ = p.QueryRow(context.Background(), `SELECT processing_status, COALESCE(review_code,'')
		FROM iam_v2.stay_events WHERE pms_interface_id=$1 AND external_event_identity=$2`, s.iface, identity).Scan(&status, &review)
	return
}

// TestIntegration_StayLifecycle drives GI→GC→room-move→GO→reinstate through the real transactional processor
// and asserts the authoritative Stay state, lifecycle_version episodes, primary guest, folio identity, and the
// terminal event outcomes — end to end on PostgreSQL through the real append-only + lifecycle triggers.
func TestIntegration_StayLifecycle(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seed(t, p)
	// a GO event has no legacy Stay-domain-only path any more, so the lifecycle test drives the REAL wired
	// Checkout slice (commerce seeded so the conversion can run).
	commerce(t, p, s, true)
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))
	res := "RES-1"

	// GI → create IN_HOUSE stay + primary guest + folio identity
	insertLive(t, p, s, "e-gi", "GI", pay(res, "1408", "Smith", "John", "F900", "260101", "260105"))
	process(t, pr, s)
	if st, lv, room := stayState(t, p, s, res); st != "IN_HOUSE" || lv != 1 || room != "1408" {
		t.Fatalf("after GI: status=%s lv=%d room=%s, want IN_HOUSE/1/1408", st, lv, room)
	}
	if n := scalar(t, p, `SELECT count(*) FROM iam_v2.stay_guests g JOIN iam_v2.stays s ON s.id=g.stay_id WHERE s.external_reservation_id=$1 AND g.is_primary`, res); n != 1 {
		t.Fatalf("primary guest count=%d, want 1", n)
	}
	if n := scalar(t, p, `SELECT count(*) FROM iam_v2.folios WHERE pms_interface_id=$1 AND external_folio_id='F900'`, s.iface); n != 1 {
		t.Fatalf("folio identity count=%d, want 1", n)
	}
	if st, _ := eventOutcome(t, p, s, "e-gi"); st != "APPLIED" {
		t.Fatalf("GI outcome=%s, want APPLIED", st)
	}

	// GC same room → update (name correction), still IN_HOUSE/1
	insertLive(t, p, s, "e-gc", "GC", pay(res, "1408", "Smithe", "John", "", "260101", "260105"))
	process(t, pr, s)
	if st, lv, room := stayState(t, p, s, res); st != "IN_HOUSE" || lv != 1 || room != "1408" {
		t.Fatalf("after GC: %s/%d/%s", st, lv, room)
	}

	// GC different room → room move (episode preserved, lv unchanged)
	insertLive(t, p, s, "e-move", "GC", pay(res, "1500", "Smithe", "John", "", "260101", "260105"))
	process(t, pr, s)
	if st, lv, room := stayState(t, p, s, res); st != "IN_HOUSE" || lv != 1 || room != "1500" {
		t.Fatalf("after room move: %s/%d/%s, want IN_HOUSE/1/1500", st, lv, room)
	}

	// GO → checkout
	insertLive(t, p, s, "e-go", "GO", pay(res, "1500", "", "", "", "", ""))
	process(t, pr, s)
	if st, lv, _ := stayState(t, p, s, res); st != "CHECKED_OUT" || lv != 1 {
		t.Fatalf("after GO: %s/%d, want CHECKED_OUT/1", st, lv)
	}

	// duplicate GO → idempotent skip (still CHECKED_OUT)
	insertLive(t, p, s, "e-go2", "GO", pay(res, "1500", "", "", "", "", ""))
	process(t, pr, s)
	if st, r := eventOutcome(t, p, s, "e-go2"); st != "SKIPPED_DUPLICATE" {
		t.Fatalf("duplicate GO outcome=%s review=%s, want SKIPPED_DUPLICATE", st, r)
	}

	// GI on a checked-out reservation → REINSTATE (exactly one lifecycle bump)
	insertLive(t, p, s, "e-reinstate", "GI", pay(res, "1500", "Smithe", "John", "", "260601", "260605"))
	process(t, pr, s)
	if st, lv, _ := stayState(t, p, s, res); st != "IN_HOUSE" || lv != 2 {
		t.Fatalf("after reinstate: %s/%d, want IN_HOUSE/2", st, lv)
	}
}

// TestIntegration_ManualReviewAndEmptyQueue proves an ambiguous event fails closed to MANUAL_REVIEW with a
// bounded code and admits no Stay, and that ProcessNext reports no work when the inbox is empty.
func TestIntegration_ManualReviewAndEmptyQueue(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seed(t, p)
	pr := NewProcessor(p)

	// empty inbox
	if ok, err := pr.ProcessNext(context.Background(), s.tenant, s.site, s.iface); err != nil || ok {
		t.Fatalf("empty inbox: ok=%v err=%v, want false/nil", ok, err)
	}
	// GO for an unknown reservation → MANUAL_REVIEW, no stay created
	insertLive(t, p, s, "e-orphan", "GO", pay("UNKNOWN", "9", "", "", "", "", ""))
	process(t, pr, s)
	if st, review := eventOutcome(t, p, s, "e-orphan"); st != "MANUAL_REVIEW" || review == "" {
		t.Fatalf("orphan GO outcome=%s review=%q, want MANUAL_REVIEW + bounded code", st, review)
	}
	if n := scalar(t, p, `SELECT count(*) FROM iam_v2.stays WHERE pms_interface_id=$1`, s.iface); n != 0 {
		t.Fatalf("MANUAL_REVIEW must create no Stay, got %d", n)
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
