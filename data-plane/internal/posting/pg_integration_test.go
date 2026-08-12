//go:build integration

package posting

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The Phase-4 financial-core integration matrix. It needs a DISPOSABLE PostgreSQL 16 carrying the
// authoritative chain plus migration 0011, reachable via PHASE4_TEST_DSN — scripts/phase4-pg-integration.sh
// builds and tears one down. It contacts no appliance, no Production database and no PMS.
//
// No test here transmits anything. The only transports in this file are in-process stubs, and the DARK
// tests assert that even a transport which WOULD accept a send is never reached.

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE4_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE4_TEST_DSN not set; skipping the phase-4 financial PG integration matrix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := p.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// scope is one completely independent property: its own tenant, site, PMS interface, plans, packages,
// mappings, stay, folio, purchase and settlement. Every test builds its own, so no test can be affected by
// another's append-only rows.
type scope struct {
	tenant, site      string
	iface, rev        string // financially onboarded revision
	revNoCurrency     string // folio strategy set, NOT financially onboarded
	revUnsetFolio     string
	stay, folio       string
	purchase, settle  string
	pkgRev, mapping   string
	room, folioNumber string
	currency          string
	exponent          int16
}

func mustExec(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := p.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec failed: %v\nSQL: %s", err, strings.TrimSpace(sql))
	}
}

func scan1[T any](t *testing.T, p *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("query failed: %v\nSQL: %s", err, strings.TrimSpace(sql))
	}
	return v
}

// seedProperty builds one financially onboarded property. currency/exponent are the property's own, so a
// test can build a EUR property and a USD property side by side and prove they never mix.
func seedProperty(t *testing.T, p *pgxpool.Pool, currency string, exponent int16) scope {
	t.Helper()
	ctx := context.Background()
	s := scope{currency: currency, exponent: exponent, room: "1421", folioNumber: "5"}

	if err := p.QueryRow(ctx, `WITH
	  t  AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id)
	SELECT tenant_id::text, id::text FROM si`).Scan(&s.tenant, &s.site); err != nil {
		t.Fatalf("seed tenant/site: %v", err)
	}
	s.iface = scan1[string](t, p, `INSERT INTO iam_v2.pms_interfaces(tenant_id,site_id,connector_kind)
		VALUES ($1,$2,'protel-fias') RETURNING id::text`, s.tenant, s.site)

	// Three revisions, so a test can pick exactly the onboarding state it wants to prove.
	s.revUnsetFolio = scan1[string](t, p, `INSERT INTO iam_v2.pms_interface_revisions
		(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config)
		VALUES ($1,$2,$3,1,'UTC','UNSET','{}') RETURNING id::text`, s.tenant, s.site, s.iface)
	s.revNoCurrency = scan1[string](t, p, `INSERT INTO iam_v2.pms_interface_revisions
		(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config)
		VALUES ($1,$2,$3,2,'UTC','GLOBALLY_UNIQUE','{}') RETURNING id::text`, s.tenant, s.site, s.iface)
	s.rev = scan1[string](t, p, `INSERT INTO iam_v2.pms_interface_revisions
		(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config,
		 financial_base_currency,financial_base_currency_exponent)
		VALUES ($1,$2,$3,3,'UTC','GLOBALLY_UNIQUE',
		 '{"heartbeat_timeout_ms":60000,"feed_freshness_ms":300000,"complete_sync_ms":3600000}',$4,$5)
		RETURNING id::text`, s.tenant, s.site, s.iface, currency, exponent)
	mustExec(t, p, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2 WHERE id=$1`, s.iface, s.rev)

	// Migration 0012 makes the financial boundary consult the SAME Phase-3 runtime axes the resolver uses.
	// A property with no runtime row fails closed with RUNTIME_UNKNOWN, which is correct -- so a fixture
	// that wants to test anything else has to record a healthy interface explicitly.
	mustExec(t, p, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id,site_id,pms_interface_id,pinned_revision_id,credential_mode,runtime_generation,
		 transport_status,last_connected_at,last_heartbeat_at,continuity_status,last_valid_event_at,
		 sync_status,last_complete_sync_at,resync_generation_seq,published_resync_generation)
		VALUES ($1,$2,$3,$4,'NONE',1,'CONNECTED',now(),now(),'CONTINUOUS',now(),'IN_SYNC',now(),0,0)`,
		s.tenant, s.site, s.iface, s.rev)

	plan := scan1[string](t, p, `INSERT INTO iam_v2.service_plans(tenant_id,site_id,code)
		VALUES ($1,$2,'P-'||substr(gen_random_uuid()::text,1,8)) RETURNING id::text`, s.tenant, s.site)
	planRev := scan1[string](t, p, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id,site_id,service_plan_id,revision_no,name,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'plan',2,'VALIDITY_WINDOW',1000000) RETURNING id::text`, s.tenant, s.site, plan)

	pkg := scan1[string](t, p, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code)
		VALUES ($1,$2,'K-'||substr(gen_random_uuid()::text,1,8)) RETURNING id::text`, s.tenant, s.site)
	s.pkgRev = scan1[string](t, p, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent)
		VALUES ($1,$2,$3,1,$4,'GENERAL',1000,$5,$6) RETURNING id::text`,
		s.tenant, s.site, pkg, planRev, currency, exponent)
	s.mapping = scan1[string](t, p, `INSERT INTO iam_v2.package_settlement_mappings
		(tenant_id,site_id,package_revision_id,pms_interface_id,mapping_revision,posting_code)
		VALUES ($1,$2,$3,$4,1,'WIFI') RETURNING id::text`, s.tenant, s.site, s.pkgRev, s.iface)

	s.stay = scan1[string](t, p, `INSERT INTO iam_v2.stays
		(tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
		 normalized_room_number,status,posting_allowed)
		VALUES ($1,$2,$3,'R-'||substr(gen_random_uuid()::text,1,8),'S1',$4,'IN_HOUSE',true)
		RETURNING id::text`, s.tenant, s.site, s.iface, s.room)
	s.folio = scan1[string](t, p, `INSERT INTO iam_v2.folios
		(tenant_id,site_id,pms_interface_id,external_folio_id)
		VALUES ($1,$2,$3,$4) RETURNING id::text`, s.tenant, s.site, s.iface, s.folioNumber)

	s.purchase = scan1[string](t, p, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,settlement_mapping_id,trigger,
		 amount_minor,currency,currency_exponent,state)
		VALUES ($1,$2,$3,$4,$5,$6,'VOUCHER_REDEMPTION',1000,$7,$8,'GRANTED') RETURNING id::text`,
		s.tenant, s.site, s.pkgRev, s.iface, s.stay, s.mapping, currency, exponent)
	s.settle = scan1[string](t, p, `INSERT INTO iam_v2.settlements
		(tenant_id,site_id,purchase_id,method,status) VALUES ($1,$2,$3,'PMS_POSTING','REQUIRED')
		RETURNING id::text`, s.tenant, s.site, s.purchase)
	return s
}

func (s scope) pinned(idem string) Pinned {
	return Pinned{
		TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface,
		PostingInterfaceRevisionID: s.rev, StayID: s.stay, FolioID: s.folio,
		PackageRevisionID: s.pkgRev, SettlementMappingID: s.mapping,
		PurchaseID: s.purchase, SettlementID: s.settle,
		AmountMinor: 1000, Currency: s.currency, CurrencyExponent: s.exponent,
		RN: s.room, GNumber: s.folioNumber, PostingCode: "WIFI", IdempotencyKey: idem,
	}
}

func idem(t *testing.T) string { return "idem-" + strings.ReplaceAll(t.Name(), "/", "-") }

// ---- flag presets --------------------------------------------------------------------------------------

var (
	// darkCfg is the DELIVERED state plus the two flags that let the domain and worker RUN at all. Transmit
	// stays OFF, which is what makes the no-egress tests meaningful: the worker really executes.
	darkCfg = Config{MasterEnabled: true, PostingEnabled: true, OutboxEnabled: true, ReviewEnabled: true}
	// liveCfg is used ONLY against an in-process stub transport, never a socket.
	liveCfg = Config{MasterEnabled: true, PostingEnabled: true, OutboxEnabled: true, TransmitEnabled: true, ReviewEnabled: true}
)

// ---- stub transports -----------------------------------------------------------------------------------

type stubTransport struct {
	mu     sync.Mutex
	bodies []string
	answer func(pn int64) (*PA, error)
}

func (s *stubTransport) SendPS(_ context.Context, _ string, pn int64, body string) (*PA, error) {
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	s.mu.Unlock()
	if s.answer != nil {
		return s.answer(pn)
	}
	return &PA{PNumber: pn, AS: "OK"}, nil
}

func (s *stubTransport) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.bodies) }

// ---- helpers -------------------------------------------------------------------------------------------

func pCounter(t *testing.T, p *pgxpool.Pool, iface string) int64 {
	t.Helper()
	var n *int64
	if err := p.QueryRow(context.Background(),
		`SELECT next_p_number FROM iam_v2.pms_interface_pnumber_seq WHERE pms_interface_id=$1`, iface).Scan(&n); err != nil {
		return 1 // no row yet: nothing has ever been allocated
	}
	if n == nil {
		return 1
	}
	return *n
}

func attemptCount(t *testing.T, p *pgxpool.Pool, postingID string) int {
	t.Helper()
	return scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_attempts WHERE internal_posting_id=$1`, postingID)
}

// ---- 1. the happy path ---------------------------------------------------------------------------------

func TestIntegrationPosting_HappyPathPostsOnce(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	tr := &stubTransport{}
	e := NewEngine(liveCfg, NewRepo(p), tr)
	ctx := context.Background()

	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.Result != "POSTED" || out.ASStatus != "OK" {
		t.Fatalf("expected POSTED/OK, got %+v", out)
	}
	if tr.count() != 1 {
		t.Fatalf("expected exactly one PS, got %d", tr.count())
	}
	if !strings.HasPrefix(tr.bodies[0], "PS|RN"+s.room+"|G#"+s.folioNumber+"|TA1000|PTD|SOWIFI|CTWIFI|P#") {
		t.Fatalf("PS wire is not the contract shape: %q", tr.bodies[0])
	}
	st, err := NewRepo(p).ReadState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.ExecutionState != "POSTED" || st.AttemptCount != 1 || st.HasUnknownHistory {
		t.Fatalf("read model disagrees with the ledger: %+v", st)
	}
	// a second lane pass has nothing to do — the posting is DONE, not re-queued
	out2, err := e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if err != nil || out2.Claimed {
		t.Fatalf("a completed posting must not be claimable again: %+v %v", out2, err)
	}
	if tr.count() != 1 {
		t.Fatalf("a completed posting must not be re-sent (%d sends)", tr.count())
	}
}

// ---- 2. the fail-closed matrix, against the real database ----------------------------------------------

// Every refusal must cost NOTHING: no posting, no outbox row, no P#, no bytes.
func TestIntegrationPosting_FailClosedConsumesNothing(t *testing.T) {
	p := pool(t)
	repo := NewRepo(p)
	ctx := context.Background()

	cases := []struct {
		name string
		mut  func(t *testing.T, s *scope, pin *Pinned)
		code Code
	}{
		// The UNSET revision is also superseded, so two rules would refuse it. The ONBOARDING refusal must
		// win: "this property has no folio identity strategy" is what an operator can act on, and "that
		// revision is not current" would send them to the wrong place.
		{"folio strategy UNSET", func(_ *testing.T, s *scope, pin *Pinned) {
			pin.PostingInterfaceRevisionID = s.revUnsetFolio
		}, ErrFolioStrategyUnset},
		{"interface not financially onboarded", func(t *testing.T, s *scope, pin *Pinned) {
			mustExec(t, p, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2 WHERE id=$1`, s.iface, s.revNoCurrency)
			pin.PostingInterfaceRevisionID = s.revNoCurrency
		}, ErrInterfaceNoCurrency},
		{"missing RN", func(t *testing.T, s *scope, pin *Pinned) {
			mustExec(t, p, `UPDATE iam_v2.stays SET normalized_room_number=NULL WHERE id=$1`, s.stay)
			pin.RN = ""
		}, ErrRNMissing},
		{"missing G#", func(_ *testing.T, _ *scope, pin *Pinned) { pin.GNumber = "" }, ErrGNumberMissing},
		{"RN carries the field delimiter", func(_ *testing.T, _ *scope, pin *Pinned) { pin.RN = "14|21" }, ErrRNNotWireSafe},
		{"stay checked out", func(t *testing.T, s *scope, _ *Pinned) {
			mustExec(t, p, `UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now(),
				posting_allowed=false WHERE id=$1`, s.stay)
		}, ErrStayNotInHouse},
		{"posting not allowed", func(t *testing.T, s *scope, _ *Pinned) {
			mustExec(t, p, `UPDATE iam_v2.stays SET posting_allowed=false WHERE id=$1`, s.stay)
		}, ErrPostingNotAllowed},
		{"currency mismatch", func(_ *testing.T, _ *scope, pin *Pinned) { pin.Currency = "EUR" }, ErrCurrencyMismatch},
		{"exponent mismatch", func(_ *testing.T, _ *scope, pin *Pinned) { pin.CurrencyExponent = 3 }, ErrExponentMismatch},
		{"interface DRAINING refuses new work", func(t *testing.T, s *scope, _ *Pinned) {
			mustExec(t, p, `UPDATE iam_v2.pms_interfaces SET lifecycle_state='DRAINING' WHERE id=$1`, s.iface)
		}, ErrInterfaceInactive},
		{"interface DECOMMISSIONED", func(t *testing.T, s *scope, _ *Pinned) {
			mustExec(t, p, `UPDATE iam_v2.pms_interfaces SET lifecycle_state='DECOMMISSIONED' WHERE id=$1`, s.iface)
		}, ErrInterfaceDecomm},
		{"transport axis disconnected", func(t *testing.T, s *scope, _ *Pinned) {
			mustExec(t, p, `UPDATE iam_v2.pms_interface_runtime SET transport_status='DISCONNECTED',
				updated_at=now() WHERE pms_interface_id=$1`, s.iface)
		}, ErrInterfaceNotFresh},
		{"heartbeat axis stale", func(t *testing.T, s *scope, _ *Pinned) {
			mustExec(t, p, `UPDATE iam_v2.pms_interface_runtime
				SET last_heartbeat_at=now()-interval '10 minutes', updated_at=now()
				WHERE pms_interface_id=$1`, s.iface)
		}, ErrInterfaceNotFresh},
		{"continuity axis gapped", func(t *testing.T, s *scope, _ *Pinned) {
			mustExec(t, p, `UPDATE iam_v2.pms_interface_runtime SET continuity_status='GAP_DETECTED',
				updated_at=now() WHERE pms_interface_id=$1`, s.iface)
		}, ErrInterfaceNotFresh},
		{"sync axis requires resync", func(t *testing.T, s *scope, _ *Pinned) {
			mustExec(t, p, `UPDATE iam_v2.pms_interface_runtime SET sync_status='RESYNC_REQUIRED',
				updated_at=now() WHERE pms_interface_id=$1`, s.iface)
		}, ErrInterfaceNotFresh},
		{"pin coherence: a resync generation is part-published", func(t *testing.T, s *scope, _ *Pinned) {
			mustExec(t, p, `UPDATE iam_v2.pms_interface_runtime SET resync_generation_seq=2,
				published_resync_generation=1, updated_at=now() WHERE pms_interface_id=$1`, s.iface)
		}, ErrInterfaceNotFresh},
		{"no PMS runtime state at all", func(t *testing.T, s *scope, _ *Pinned) {
			mustExec(t, p, `DELETE FROM iam_v2.pms_interface_runtime WHERE pms_interface_id=$1`, s.iface)
		}, ErrInterfaceNotFresh},
		{"stale stay evidence", func(_ *testing.T, _ *scope, pin *Pinned) {
			v := 99
			pin.ExpectStayLifecycleVersion = &v
		}, ErrEvidenceStale},
		{"out-of-scope folio", func(t *testing.T, s *scope, pin *Pinned) {
			other := seedProperty(t, p, "USD", 2)
			pin.FolioID = other.folio
		}, ErrEvidenceOutOfScope},
		{"out-of-scope stay", func(t *testing.T, s *scope, pin *Pinned) {
			other := seedProperty(t, p, "USD", 2)
			pin.StayID = other.stay
		}, ErrEvidenceOutOfScope},
		{"zero amount", func(_ *testing.T, _ *scope, pin *Pinned) { pin.AmountMinor = 0 }, ErrAmountInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := seedProperty(t, p, "USD", 2)
			tr := &stubTransport{}
			e := NewEngine(liveCfg, repo, tr) // deliberately NOT dark: the refusal must be the gate's, not DARK's
			pin := s.pinned("idem-" + strings.ReplaceAll(t.Name(), "/", "-"))
			tc.mut(t, &s, &pin)

			pBefore := pCounter(t, p, s.iface)
			_, err := e.CreatePosting(ctx, pin)
			if err == nil {
				t.Fatal("the gate must refuse")
			}
			if CodeOf(err) != tc.code {
				t.Fatalf("expected %s, got %s (%v)", tc.code, CodeOf(err), err)
			}
			// ---- and it must have cost nothing ----
			if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.pms_postings WHERE idempotency_key=$1`,
				pin.IdempotencyKey); n != 0 {
				t.Fatalf("a refused charge left %d posting rows", n)
			}
			if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_outbox WHERE pms_interface_id=$1`,
				s.iface); n != 0 {
				t.Fatalf("a refused charge left %d outbox rows", n)
			}
			if got := pCounter(t, p, s.iface); got != pBefore {
				t.Fatalf("a refused charge consumed a P# (%d -> %d)", pBefore, got)
			}
			if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_attempts WHERE pms_interface_id=$1`,
				s.iface); n != 0 {
				t.Fatalf("a refused charge left %d attempts", n)
			}
			if tr.count() != 0 {
				t.Fatalf("a refused charge produced %d financial wire bodies", tr.count())
			}
		})
	}
}

// ---- 3. DARK: positive no-egress -----------------------------------------------------------------------

// This is the claim that matters most, so it is proved positively. The worker is RUNNING, the flags are the
// delivered ones, a real posting is queued, and the inner transport is one that WOULD accept a send.
func TestIntegrationPosting_DarkWorkerProducesNoEgress(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	inner := &stubTransport{}
	e := NewEngine(darkCfg, NewRepo(p), inner)
	ctx := context.Background()

	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before := pCounter(t, p, s.iface)

	// run the lane repeatedly, exactly as a live worker loop would
	for i := 0; i < 5; i++ {
		out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface)
		if err != nil {
			t.Fatalf("dark lane pass %d returned an error: %v", i, err)
		}
		if !out.Claimed {
			t.Fatalf("dark lane pass %d claimed nothing — the work must stay queued, not vanish", i)
		}
		if out.Result != "DECLINED" || out.RefusedFor != ErrDarkNoEgress {
			t.Fatalf("dark lane pass %d: expected a DARK decline, got %+v", i, out)
		}
	}

	// POSITIVE evidence: the worker really tried, five times, and was refused AT THE WIRE.
	if e.TransportRefusals() != 5 {
		t.Fatalf("expected 5 recorded wire refusals, got %d", e.TransportRefusals())
	}
	if !strings.HasPrefix(e.LastRefusedBody(), "PS|RN") {
		t.Fatalf("the guard should have refused a fully-built PS, got %q", e.LastRefusedBody())
	}
	// NEGATIVE evidence: nothing was produced, allocated or sent.
	if inner.count() != 0 {
		t.Fatalf("the inner transport was reached %d times while DARK", inner.count())
	}
	if got := pCounter(t, p, s.iface); got != before {
		t.Fatalf("a DARK worker consumed a P# (%d -> %d)", before, got)
	}
	if n := attemptCount(t, p, id); n != 0 {
		t.Fatalf("a DARK worker wrote %d attempts", n)
	}
	st, err := NewRepo(p).ReadState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.ExecutionState != "NOT_ATTEMPTED" || st.OutboxState == nil || *st.OutboxState != "QUEUED" {
		t.Fatalf("a DARK decline must leave the work QUEUED and unattempted: %+v", st)
	}
}

// A worker whose outbox flag is OFF must not even claim.
func TestIntegrationPosting_OutboxFlagOffClaimsNothing(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	full := NewEngine(darkCfg, NewRepo(p), nil)
	if _, err := full.CreatePosting(context.Background(), s.pinned(idem(t))); err != nil {
		t.Fatal(err)
	}
	off := NewEngine(Config{MasterEnabled: true, PostingEnabled: true}, NewRepo(p), &stubTransport{})
	out, err := off.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	if err == nil || out.Claimed {
		t.Fatalf("a disabled outbox worker must claim nothing: %+v %v", out, err)
	}
}

// ---- 4. P# concurrency ---------------------------------------------------------------------------------

func TestIntegrationPosting_PNumberConcurrencyIsAtomicAndPerInterface(t *testing.T) {
	p := pool(t)
	a := seedProperty(t, p, "USD", 2)
	b := seedProperty(t, p, "EUR", 2)
	repo := NewRepo(p)
	ctx := context.Background()

	const workers, each = 8, 25
	seen := make(chan int64, workers*each)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				tx, err := p.Begin(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				n, err := repo.AllocatePNumber(ctx, tx, a.tenant, a.site, a.iface)
				if err != nil {
					_ = tx.Rollback(ctx)
					t.Error(err)
					return
				}
				if err := tx.Commit(ctx); err != nil {
					t.Error(err)
					return
				}
				seen <- n
			}
		}()
	}
	wg.Wait()
	close(seen)

	uniq := map[int64]bool{}
	var min, max int64 = 1 << 62, 0
	total := 0
	for n := range seen {
		if uniq[n] {
			t.Fatalf("P# %d was allocated twice", n)
		}
		uniq[n] = true
		total++
		if n < min {
			min = n
		}
		if n > max {
			max = n
		}
	}
	if total != workers*each {
		t.Fatalf("expected %d allocations, got %d", workers*each, total)
	}
	if max-min+1 != int64(total) {
		t.Fatalf("allocations are not a gapless range: %d..%d for %d values", min, max, total)
	}
	// the other interface's namespace is completely untouched by the contention
	tx, _ := p.Begin(ctx)
	nb, err := repo.AllocatePNumber(ctx, tx, b.tenant, b.site, b.iface)
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit(ctx)
	if nb != 1 {
		t.Fatalf("a second interface must start its own sequence at 1, got %d", nb)
	}
}

// A rolled-back transaction gives the number back: the allocator is transactional, not a clock.
func TestIntegrationPosting_PNumberRollbackConsumesNothing(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	repo := NewRepo(p)
	ctx := context.Background()
	tx, _ := p.Begin(ctx)
	if _, err := repo.AllocatePNumber(ctx, tx, s.tenant, s.site, s.iface); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	if got := pCounter(t, p, s.iface); got != 1 {
		t.Fatalf("a rolled-back allocation consumed a P# (counter is %d)", got)
	}
}

// ---- 5. outbox concurrency + lane isolation ------------------------------------------------------------

// Two workers racing on ONE queued posting must produce exactly one attempt, never two.
func TestIntegrationPosting_ConcurrentWorkersProduceOneAttempt(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	tr := &stubTransport{}
	e := NewEngine(liveCfg, NewRepo(p), tr)
	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	const racers = 6
	var wg sync.WaitGroup
	claimed := make(chan bool, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := NewEngine(liveCfg, NewRepo(p), tr)
			out, _ := w.RunOnce(ctx, s.tenant, s.site, s.iface)
			claimed <- out.Claimed
		}()
	}
	wg.Wait()
	close(claimed)
	n := 0
	for c := range claimed {
		if c {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("exactly one worker may claim a queued posting, %d did", n)
	}
	if got := attemptCount(t, p, id); got != 1 {
		t.Fatalf("a raced posting produced %d attempts", got)
	}
	if tr.count() != 1 {
		t.Fatalf("a raced posting produced %d financial transmissions", tr.count())
	}
}

// A failing lane must not affect another interface's lane.
func TestIntegrationPosting_LanesAreIndependent(t *testing.T) {
	p := pool(t)
	usd := seedProperty(t, p, "USD", 2)
	eur := seedProperty(t, p, "EUR", 2)
	ctx := context.Background()

	// the USD lane's transport always fails to transmit; the EUR lane's works
	failing := &stubTransport{answer: func(int64) (*PA, error) {
		return nil, fmt.Errorf("connection refused: %w", ErrNotTransmitted)
	}}
	working := &stubTransport{}

	eu := NewEngine(liveCfg, NewRepo(p), failing)
	ee := NewEngine(liveCfg, NewRepo(p), working)
	if _, err := eu.CreatePosting(ctx, usd.pinned(idem(t)+"-usd")); err != nil {
		t.Fatal(err)
	}
	eurID, err := ee.CreatePosting(ctx, eur.pinned(idem(t)+"-eur"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := eu.RunOnce(ctx, usd.tenant, usd.site, usd.iface); err != nil || out.Result != "NOT_SENT" {
		t.Fatalf("the failing lane should report NOT_SENT: %+v %v", out, err)
	}
	out, err := ee.RunOnce(ctx, eur.tenant, eur.site, eur.iface)
	if err != nil || out.Result != "POSTED" {
		t.Fatalf("the healthy lane must be unaffected by the failing one: %+v %v", out, err)
	}
	st, _ := NewRepo(p).ReadState(ctx, eurID)
	if st.ExecutionState != "POSTED" {
		t.Fatalf("healthy lane state: %+v", st)
	}
	// a NOT_TRANSMITTED failure is safe to retry automatically, so the USD work is queued again
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_outbox
		WHERE pms_interface_id=$1 AND state='QUEUED'`, usd.iface); n != 1 {
		t.Fatalf("a provably-not-transmitted attempt must be re-queued, found %d queued rows", n)
	}
}

// ---- 6. UNKNOWN ----------------------------------------------------------------------------------------

func TestIntegrationPosting_TransmittedWithoutAnswerBecomesUnknownAndStaysThere(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	repo := NewRepo(p)
	// a transport that writes the bytes and then never conclusively answers
	silent := &stubTransport{answer: func(int64) (*PA, error) { return nil, ErrTransmittedNoAnswer }}
	e := NewEngine(liveCfg, repo, silent)

	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if out.Result != "UNKNOWN" {
		t.Fatalf("a transmitted PS with no matched PA must be UNKNOWN, got %+v (%v)", out, err)
	}
	if CodeOf(err) != ErrUnknownTerminal {
		t.Fatalf("expected the UNKNOWN terminal signal, got %s", CodeOf(err))
	}
	afterFirst := pCounter(t, p, s.iface)

	// Now run the lane many more times, exactly as a restarted, healthy, running worker would.
	for i := 0; i < 5; i++ {
		out, _ := e.RunOnce(ctx, s.tenant, s.site, s.iface)
		if out.Claimed {
			t.Fatalf("pass %d claimed an UNKNOWN posting: UNKNOWN is never auto-retried", i)
		}
	}
	if silent.count() != 1 {
		t.Fatalf("UNKNOWN was retransmitted (%d sends)", silent.count())
	}
	if got := pCounter(t, p, s.iface); got != afterFirst {
		t.Fatalf("UNKNOWN consumed a second P# (%d -> %d)", afterFirst, got)
	}
	if n := attemptCount(t, p, id); n != 1 {
		t.Fatalf("UNKNOWN produced %d attempts", n)
	}
	st, _ := repo.ReadState(ctx, id)
	if st.ExecutionState != "UNKNOWN" || !st.AwaitingManualReview || !st.HasUnknownHistory {
		t.Fatalf("read model must surface UNKNOWN for review: %+v", st)
	}
	if st.OutboxState == nil || *st.OutboxState != "HELD_RECOVERY" {
		t.Fatalf("UNKNOWN must park the outbox row in HELD_RECOVERY: %+v", st)
	}
	// and it cannot be requeued without an audited decision
	if err := e.Requeue(ctx, id); CodeOf(err) != ErrRetryNotAuthed {
		t.Fatalf("requeue without a review decision must be refused, got %v", err)
	}
	// even a direct attempt insert is structurally refused by the database
	_, dbErr := p.Exec(ctx, `INSERT INTO iam_v2.posting_attempts
		(tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,sent_at)
		VALUES ($1,$2,$3,$4,2,'999',$5,$6,now())`, s.tenant, s.site, id, s.iface, s.room, s.folioNumber)
	if dbErr == nil || !strings.Contains(dbErr.Error(), "RETRY_REQUIRES_REVIEW") {
		t.Fatalf("the database must refuse an unauthorized second attempt, got %v", dbErr)
	}
}

// The ONLY way out of UNKNOWN: an audited CONFIRM_NOT_POSTED_RETRY, then exactly one further attempt,
// reusing the SAME business idempotency key with a NEW protocol reference.
func TestIntegrationPosting_UnknownLeavesOnlyThroughAuditedReview(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	repo := NewRepo(p)
	answers := 0
	tr := &stubTransport{answer: func(pn int64) (*PA, error) {
		answers++
		if answers == 1 {
			return nil, ErrTransmittedNoAnswer
		}
		return &PA{PNumber: pn, AS: "OK"}, nil
	}}
	e := NewEngine(liveCfg, repo, tr)
	key := idem(t)
	id, err := e.CreatePosting(ctx, s.pinned(key))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = e.RunOnce(ctx, s.tenant, s.site, s.iface) // -> UNKNOWN

	actor := scan1[string](t, p, `SELECT gen_random_uuid()::text`)
	if _, err := repo.RecordReview(ctx, id, "CONFIRM_NOT_POSTED_RETRY", actor,
		"operator checked the folio: nothing was posted",
		`{"folio_checked_at":"2026-08-12T00:00:00Z","operator_finding":"no charge present"}`, nil, nil); err != nil {
		t.Fatalf("review: %v", err)
	}
	if err := e.Requeue(ctx, id); err != nil {
		t.Fatalf("requeue after an audited decision must succeed: %v", err)
	}
	out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if err != nil || out.Result != "POSTED" || out.AttemptNo != 2 {
		t.Fatalf("the ONE authorized retry should post: %+v %v", out, err)
	}
	// same money, same business identity, a different protocol reference
	if n := scan1[int](t, p, `SELECT count(DISTINCT p_number)::int FROM iam_v2.posting_attempts
		WHERE internal_posting_id=$1`, id); n != 2 {
		t.Fatalf("each attempt must carry its own P#, found %d distinct", n)
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.pms_postings WHERE idempotency_key=$1`, key); n != 1 {
		t.Fatalf("a retry must not create a second posting (%d found)", n)
	}
	st, _ := repo.ReadState(ctx, id)
	if st.ExecutionState != "POSTED" || !st.HasUnknownHistory || st.AwaitingManualReview {
		t.Fatalf("read model after the authorized retry: %+v", st)
	}
	// the authorization was for ONE attempt and is now spent
	_, dbErr := p.Exec(ctx, `INSERT INTO iam_v2.posting_attempts
		(tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,sent_at)
		VALUES ($1,$2,$3,$4,3,'998',$5,$6,now())`, s.tenant, s.site, id, s.iface, s.room, s.folioNumber)
	if dbErr == nil {
		t.Fatal("a spent retry authorization must not permit a third attempt")
	}
}

// ---- 7. reviewer concurrency ---------------------------------------------------------------------------

func TestIntegrationPosting_ConcurrentReviewersCannotBothWin(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	repo := NewRepo(p)
	silent := &stubTransport{answer: func(int64) (*PA, error) { return nil, ErrTransmittedNoAnswer }}
	e := NewEngine(liveCfg, repo, silent)
	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = e.RunOnce(ctx, s.tenant, s.site, s.iface) // -> UNKNOWN

	actor := scan1[string](t, p, `SELECT gen_random_uuid()::text`)
	incompatible := []string{"CONFIRM_POSTED", "CREATE_REVERSAL", "CONFIRM_NOT_POSTED_ABANDON", "CONFIRM_NOT_POSTED_RETRY"}
	results := make(chan error, len(incompatible))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, action := range incompatible {
		wg.Add(1)
		go func(a string) {
			defer wg.Done()
			<-start
			_, err := repo.RecordReview(ctx, id, a, actor, "racing reviewer "+a,
				`{"operator_finding":"racing decision"}`, nil, nil)
			results <- err
		}(action)
	}
	close(start)
	wg.Wait()
	close(results)

	won := 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case CodeOf(err) == ErrReviewConflict:
			// correct: refused because someone else had already decided
		default:
			t.Fatalf("a losing reviewer must be refused as a conflict, got %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("exactly one incompatible decision may commit, %d did", won)
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_review_actions WHERE posting_id=$1`, id); n != 1 {
		t.Fatalf("the append-only review ledger holds %d actions for one decision", n)
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_review_state
		WHERE posting_id=$1 AND terminal_action IS NOT NULL`, id); n != 1 {
		t.Fatal("exactly one terminal decision must be recorded")
	}
}

func TestIntegrationPosting_StaleReviewVersionIsRefused(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	repo := NewRepo(p)
	silent := &stubTransport{answer: func(int64) (*PA, error) { return nil, ErrTransmittedNoAnswer }}
	e := NewEngine(liveCfg, repo, silent)
	id, _ := e.CreatePosting(ctx, s.pinned(idem(t)))
	_, _ = e.RunOnce(ctx, s.tenant, s.site, s.iface)

	actor := scan1[string](t, p, `SELECT gen_random_uuid()::text`)
	stale := 7
	if _, err := repo.RecordReview(ctx, id, "ESCALATE", actor, "needs finance", "", &stale, nil); CodeOf(err) != ErrReviewStale {
		t.Fatalf("a decision against a stale version must be refused, got %v", err)
	}
	// ESCALATE decides nothing, so the posting is still awaiting a decision afterwards
	if _, err := repo.RecordReview(ctx, id, "ESCALATE", actor, "needs finance", "", nil, nil); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	st, _ := repo.ReadState(ctx, id)
	if st.TerminalReviewAction != nil || !st.AwaitingManualReview {
		t.Fatalf("ESCALATE must leave the posting undecided: %+v", st)
	}
}

// The append-only review ledger has exactly one writer, even for a privileged direct INSERT.
func TestIntegrationPosting_ReviewLedgerHasOneWriter(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	e := NewEngine(liveCfg, NewRepo(p), &stubTransport{})
	id, _ := e.CreatePosting(ctx, s.pinned(idem(t)))
	_, err := p.Exec(ctx, `INSERT INTO iam_v2.posting_review_actions
		(tenant_id,site_id,posting_id,action,actor,reason) VALUES ($1,$2,$3,'CONFIRM_POSTED',$1,'bypass')`,
		s.tenant, s.site, id)
	if err == nil || !strings.Contains(err.Error(), "REVIEW_WRITER_ONLY") {
		t.Fatalf("a direct review INSERT must be refused, got %v", err)
	}
}

// ---- 8. restart persistence ----------------------------------------------------------------------------

// Nothing the worker knows lives in memory. A "restart" is modelled as a brand-new Engine over the same
// database — which is exactly what the process gets after a reboot.
func TestIntegrationPosting_StateSurvivesProcessRestart(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	silent := &stubTransport{answer: func(int64) (*PA, error) { return nil, ErrTransmittedNoAnswer }}
	first := NewEngine(liveCfg, NewRepo(p), silent)
	id, err := first.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = first.RunOnce(ctx, s.tenant, s.site, s.iface) // -> UNKNOWN, HELD_RECOVERY
	pAfter := pCounter(t, p, s.iface)

	// ---- restart ----
	restarted := NewEngine(liveCfg, NewRepo(p), silent)
	st, err := NewRepo(p).ReadState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.ExecutionState != "UNKNOWN" || st.OutboxState == nil || *st.OutboxState != "HELD_RECOVERY" {
		t.Fatalf("the restarted process must see the durable UNKNOWN state: %+v", st)
	}
	for i := 0; i < 3; i++ {
		if out, _ := restarted.RunOnce(ctx, s.tenant, s.site, s.iface); out.Claimed {
			t.Fatal("a restarted worker must not treat a restart as authorization to retry UNKNOWN")
		}
	}
	if silent.count() != 1 || pCounter(t, p, s.iface) != pAfter {
		t.Fatalf("a restart caused a retransmission or a new P# (sends=%d)", silent.count())
	}
	// P# continues from the DURABLE counter, never from a fresh in-memory seed
	tx, _ := p.Begin(ctx)
	next, err := NewRepo(p).AllocatePNumber(ctx, tx, s.tenant, s.site, s.iface)
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit(ctx)
	if next != pAfter {
		t.Fatalf("P# after restart must continue the durable sequence (expected %d, got %d)", pAfter, next)
	}
}

// ---- 9. business idempotency vs protocol identity ------------------------------------------------------

func TestIntegrationPosting_DuplicateIdempotencyKeyIsRefused(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	e := NewEngine(liveCfg, NewRepo(p), &stubTransport{})
	key := idem(t)
	if _, err := e.CreatePosting(ctx, s.pinned(key)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreatePosting(ctx, s.pinned(key)); err == nil {
		t.Fatal("a duplicate business idempotency key must be refused")
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.pms_postings WHERE idempotency_key=$1`, key); n != 1 {
		t.Fatalf("expected exactly one posting for one key, found %d", n)
	}
}

// ---- 10. controlled Stay lifecycle -> posting eligibility ----------------------------------------------

// The DB-level charge_gate proof exists in iam_v2_scratch/phase4_0011_financial.sh. This is the OTHER half
// the audit called for: the same boundary observed end-to-end through the engine, where the stay moves
// after the posting was already authorized and queued.
func TestIntegrationPosting_CheckoutAfterQueueingStopsTheCharge(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	tr := &stubTransport{}
	e := NewEngine(liveCfg, NewRepo(p), tr)
	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	before := pCounter(t, p, s.iface)

	// the guest checks out through the approved lifecycle move: status, boundary and posting permission
	// change together, exactly as the Phase-3 contract requires
	mustExec(t, p, `UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now(),
		posting_allowed=false WHERE id=$1`, s.stay)

	out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if out.Result != "DECLINED" {
		t.Fatalf("a checked-out stay must stop the queued charge: %+v %v", out, err)
	}
	if CodeOf(err) != ErrStayNotInHouse {
		t.Fatalf("expected stay_not_in_house, got %s", CodeOf(err))
	}
	if tr.count() != 0 {
		t.Fatalf("a stopped charge produced %d transmissions", tr.count())
	}
	if got := pCounter(t, p, s.iface); got != before {
		t.Fatalf("a stopped charge consumed a P# (%d -> %d)", before, got)
	}
	if n := attemptCount(t, p, id); n != 0 {
		t.Fatalf("a stopped charge wrote %d attempts", n)
	}
}

// ---- 11. PA handling on the real ledger ----------------------------------------------------------------

func TestIntegrationPosting_RejectedAnswerIsRecordedNotRetried(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	tr := &stubTransport{answer: func(pn int64) (*PA, error) { return &PA{PNumber: pn, AS: "NG"}, nil }}
	e := NewEngine(liveCfg, NewRepo(p), tr)
	id, _ := e.CreatePosting(ctx, s.pinned(idem(t)))
	out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if err != nil || out.Result != "REJECTED" || out.ASStatus != "NG" {
		t.Fatalf("an NG answer must be recorded as REJECTED: %+v %v", out, err)
	}
	st, _ := NewRepo(p).ReadState(ctx, id)
	if st.ExecutionState != "REJECTED" {
		t.Fatalf("read model: %+v", st)
	}
	// and it is not retried on its own
	for i := 0; i < 3; i++ {
		if o, _ := e.RunOnce(ctx, s.tenant, s.site, s.iface); o.Claimed {
			t.Fatal("a rejected posting must not be auto-retried")
		}
	}
	if tr.count() != 1 {
		t.Fatalf("a rejected posting was re-sent (%d sends)", tr.count())
	}
}

func TestIntegrationPosting_AmbiguousAnswerBecomesUnknown(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	// the PMS answers, but with a P# that belongs to nothing this attempt sent
	tr := &stubTransport{answer: func(int64) (*PA, error) {
		return nil, fail(ErrPACorrelation, "answer does not correlate to any live attempt")
	}}
	e := NewEngine(liveCfg, NewRepo(p), tr)
	id, _ := e.CreatePosting(ctx, s.pinned(idem(t)))
	out, _ := e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if out.Result != "UNKNOWN" {
		t.Fatalf("an uncorrelatable answer must NOT be resolved; expected UNKNOWN, got %+v", out)
	}
	st, _ := NewRepo(p).ReadState(ctx, id)
	if !st.AwaitingManualReview {
		t.Fatalf("an uncorrelatable answer must be surfaced for review: %+v", st)
	}
}

// ---- 12. no secrets or PII in the evidence ledger ------------------------------------------------------

func TestIntegrationPosting_AttemptEvidenceCarriesNoSecretsOrPII(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	ctx := context.Background()
	e := NewEngine(liveCfg, NewRepo(p), &stubTransport{})
	id, _ := e.CreatePosting(ctx, s.pinned(idem(t)))
	if _, err := e.RunOnce(ctx, s.tenant, s.site, s.iface); err != nil {
		t.Fatal(err)
	}
	rows := scan1[string](t, p, `SELECT coalesce(string_agg(detail::text, ' '), '')
		FROM iam_v2.posting_attempt_events e
		JOIN iam_v2.posting_attempts a ON a.id = e.posting_attempt_id
	   WHERE a.internal_posting_id = $1`, id)
	for _, banned := range []string{"password", "secret", "ciphertext", "token", "pan", "cvv", "first_name", "last_name"} {
		if strings.Contains(strings.ToLower(rows), banned) {
			t.Fatalf("attempt evidence contains %q: %s", banned, rows)
		}
	}
}

// ---- hardening: per-interface lane serialization, with DIFFERENT postings -----------------------------

// The 0011 claim was overstated. outbox_one_active stops two ACTIVE rows for the SAME posting; it says
// nothing about two DIFFERENT postings on one interface. This is the test that actually proves Contract
// section 10: many queued postings, many workers, ONE interface -- and at most one in flight at a time,
// while a second interface is completely unaffected.
func TestIntegrationPosting_InterfaceLaneIsSerializedAcrossDifferentPostings(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	a := seedProperty(t, p, "USD", 2)
	b := seedProperty(t, p, "EUR", 2)

	// a transport that holds the lane open long enough for a second worker to try to enter it
	var peak, cur int32
	hold := func(int64) (*PA, error) {
		n := atomic.AddInt32(&cur, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if n <= old || atomic.CompareAndSwapInt32(&peak, old, n) {
				break
			}
		}
		time.Sleep(120 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		return &PA{PNumber: 1, AS: "OK"}, nil
	}
	trA := &stubTransport{answer: func(pn int64) (*PA, error) {
		if _, err := hold(pn); err != nil {
			return nil, err
		}
		return &PA{PNumber: pn, AS: "OK"}, nil
	}}
	trB := &stubTransport{}
	ea := NewEngine(liveCfg, NewRepo(p), trA)
	eb := NewEngine(liveCfg, NewRepo(p), trB)

	const postings = 6
	for i := 0; i < postings; i++ {
		if _, err := ea.CreatePosting(ctx, a.pinned(fmt.Sprintf("%s-a-%d", idem(t), i))); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	bID, err := eb.CreatePosting(ctx, b.pinned(idem(t)+"-b"))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for w := 0; w < 5; w++ { // five workers, one lane
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < postings; i++ {
				_, _ = NewEngine(liveCfg, NewRepo(p), trA).RunOnce(ctx, a.tenant, a.site, a.iface)
			}
		}()
	}
	wg.Add(1) // and one worker on the OTHER interface, running concurrently throughout
	go func() {
		defer wg.Done()
		_, _ = eb.RunOnce(ctx, b.tenant, b.site, b.iface)
	}()
	wg.Wait()

	if got := atomic.LoadInt32(&peak); got > 1 {
		t.Fatalf("Contract 10: at most ONE posting may be in flight per interface, observed %d", got)
	}
	// every posting is accounted for exactly once
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_attempts WHERE pms_interface_id=$1`, a.iface); n != postings {
		t.Fatalf("expected exactly %d attempts on the serialized lane, got %d", postings, n)
	}
	if n := scan1[int](t, p, `SELECT count(DISTINCT p_number)::int FROM iam_v2.posting_attempts
		WHERE pms_interface_id=$1`, a.iface); n != postings {
		t.Fatalf("every attempt must own a distinct P#, got %d distinct", n)
	}
	// the other interface was never blocked by the busy one
	st, _ := NewRepo(p).ReadState(ctx, bID)
	if st.ExecutionState != "POSTED" {
		t.Fatalf("a busy lane must not affect another interface: %+v", st)
	}
}

// ---- hardening: DRAINING drains -----------------------------------------------------------------------

func TestIntegrationPosting_DrainingInterfaceStillDrains(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedProperty(t, p, "USD", 2)
	tr := &stubTransport{}
	e := NewEngine(liveCfg, NewRepo(p), tr)
	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, p, `UPDATE iam_v2.pms_interfaces SET lifecycle_state='DRAINING' WHERE id=$1`, s.iface)

	// new work is refused...
	if _, err := e.CreatePosting(ctx, s.pinned(idem(t)+"-new")); CodeOf(err) != ErrInterfaceInactive {
		t.Fatalf("DRAINING must refuse NEW financial work, got %v", err)
	}
	// ...but the outbox drains, which is the whole point of the state
	out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if err != nil || out.Result != "POSTED" {
		t.Fatalf("DRAINING must let the existing outbox drain: %+v %v", out, err)
	}
	st, _ := NewRepo(p).ReadState(ctx, id)
	if st.ExecutionState != "POSTED" {
		t.Fatalf("read model: %+v", st)
	}
}

// A DECOMMISSIONED interface may not transmit, and it may not be decommissioned while work is outstanding.
func TestIntegrationPosting_DecommissionRequiresAQuietInterface(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedProperty(t, p, "USD", 2)
	e := NewEngine(liveCfg, NewRepo(p), &stubTransport{})
	if _, err := e.CreatePosting(ctx, s.pinned(idem(t))); err != nil {
		t.Fatal(err)
	}
	_, err := p.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET lifecycle_state='DECOMMISSIONED' WHERE id=$1`, s.iface)
	if err == nil || !strings.Contains(err.Error(), "DECOMMISSION_BLOCKED") {
		t.Fatalf("decommissioning with a queued posting must be refused, got %v", err)
	}
	if _, err := e.RunOnce(ctx, s.tenant, s.site, s.iface); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET lifecycle_state='DECOMMISSIONED' WHERE id=$1`, s.iface); err != nil {
		t.Fatalf("a quiet interface must be decommissionable: %v", err)
	}
}

// ---- hardening: freshness is re-checked at TRANSMISSION, not only at authorization --------------------

func TestIntegrationPosting_InterfaceGoingStaleStopsTheBytes(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedProperty(t, p, "USD", 2)
	tr := &stubTransport{}
	e := NewEngine(liveCfg, NewRepo(p), tr)
	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	before := pCounter(t, p, s.iface)
	// the feed goes discontinuous AFTER the charge was authorized and queued
	mustExec(t, p, `UPDATE iam_v2.pms_interface_runtime SET continuity_status='GAP_DETECTED', updated_at=now()
		WHERE pms_interface_id=$1`, s.iface)

	out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if out.Result != "DECLINED" || CodeOf(err) != ErrInterfaceNotFresh {
		t.Fatalf("a stale interface must stop the bytes: %+v %v", out, err)
	}
	if tr.count() != 0 {
		t.Fatalf("a stale interface produced %d transmissions", tr.count())
	}
	if got := pCounter(t, p, s.iface); got != before {
		t.Fatalf("a stale interface consumed a P# (%d -> %d)", before, got)
	}
	if n := attemptCount(t, p, id); n != 0 {
		t.Fatalf("a stale interface wrote %d attempts", n)
	}
}

// ---- hardening: a PA for another P# never ACKs this attempt -------------------------------------------

func TestIntegrationPosting_PAForAnotherPNumberNeverAcks(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedProperty(t, p, "USD", 2)
	// a valid-looking, catalog-correct, OK answer -- for somebody else's attempt
	tr := &stubTransport{answer: func(pn int64) (*PA, error) { return &PA{PNumber: pn + 1000, AS: "OK"}, nil }}
	e := NewEngine(liveCfg, NewRepo(p), tr)
	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if out.Result == "POSTED" {
		t.Fatal("a PA carrying another P# must NEVER be accepted as this attempt's acknowledgement")
	}
	if out.Result != "UNKNOWN" {
		t.Fatalf("the PS was transmitted and never conclusively answered; expected UNKNOWN, got %+v", out)
	}
	st, _ := NewRepo(p).ReadState(ctx, id)
	if st.ExecutionState != "UNKNOWN" || !st.AwaitingManualReview {
		t.Fatalf("a mis-correlated answer must be surfaced for review: %+v", st)
	}
	if st.LatestPAStatus() != "" {
		t.Fatalf("no AS may be recorded from an answer that was not this attempt's: %q", st.LatestPAStatus())
	}
}

// ---- hardening: the retry authorization is single-use -------------------------------------------------

func TestIntegrationPosting_RetryAuthorizationIsConsumedExactlyOnce(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedProperty(t, p, "USD", 2)
	repo := NewRepo(p)
	silent := &stubTransport{answer: func(int64) (*PA, error) { return nil, ErrTransmittedNoAnswer }}
	e := NewEngine(liveCfg, repo, silent)
	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = e.RunOnce(ctx, s.tenant, s.site, s.iface) // -> UNKNOWN

	actor := scan1[string](t, p, `SELECT gen_random_uuid()::text`)
	ev := `{"folio_checked_at":"2026-08-12T00:00:00Z","operator_finding":"no charge present"}`
	if _, err := repo.RecordReview(ctx, id, "CONFIRM_NOT_POSTED_RETRY", actor, "folio verified clean", ev, nil, nil); err != nil {
		t.Fatalf("review: %v", err)
	}
	if err := e.Requeue(ctx, id); err != nil {
		t.Fatalf("first requeue: %v", err)
	}
	// the authorized retry runs and goes UNKNOWN again -- the worst case for a hot loop
	_, _ = e.RunOnce(ctx, s.tenant, s.site, s.iface)

	// The authorization was for ONE attempt and is now spent. A second Requeue must find nothing, and the
	// item must NOT be sitting QUEUED waiting to be picked up again.
	if err := e.Requeue(ctx, id); CodeOf(err) != ErrRetryNotAuthed {
		t.Fatalf("a spent retry authorization must not permit a second requeue, got %v", err)
	}
	st, _ := repo.ReadState(ctx, id)
	if st.OutboxState == nil || *st.OutboxState != "HELD_RECOVERY" {
		t.Fatalf("a second UNKNOWN must park in HELD_RECOVERY, not hot-loop as QUEUED: %+v", st)
	}
	if !st.RetryAuthorizationConsumed {
		t.Fatal("the retry authorization must be recorded as consumed")
	}
	if silent.count() != 2 {
		t.Fatalf("exactly two transmissions were authorized in total, got %d", silent.count())
	}
	for i := 0; i < 4; i++ {
		if o, _ := e.RunOnce(ctx, s.tenant, s.site, s.iface); o.Claimed {
			t.Fatal("nothing may be claimed after the authorization is spent")
		}
	}
}

// ---- hardening: a posted charge can never be retried --------------------------------------------------

func TestIntegrationPosting_AckedOKChargeCanNeverBeRetried(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedProperty(t, p, "USD", 2)
	repo := NewRepo(p)
	e := NewEngine(liveCfg, repo, &stubTransport{})
	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface); err != nil || out.Result != "POSTED" {
		t.Fatalf("setup: %+v %v", out, err)
	}
	actor := scan1[string](t, p, `SELECT gen_random_uuid()::text`)
	ev := `{"operator_finding":"I think it did not post"}`
	_, err = repo.RecordReview(ctx, id, "CONFIRM_NOT_POSTED_RETRY", actor, "operator believes it failed", ev, nil, nil)
	if err == nil {
		t.Fatal("retrying a charge the PMS ACKed OK would post it twice and must be refused")
	}
	if !strings.Contains(err.Error(), "REVIEW_RETRY_REFUSED") && CodeOf(err) != ErrRetryNotAuthed {
		t.Fatalf("expected an explicit retry refusal, got %v", err)
	}
	if n := attemptCount(t, p, id); n != 1 {
		t.Fatalf("the posted charge must still have exactly one attempt, got %d", n)
	}
}

// ---- hardening: review evidence is mandatory ----------------------------------------------------------

func TestIntegrationPosting_TerminalReviewRequiresEvidence(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedProperty(t, p, "USD", 2)
	repo := NewRepo(p)
	silent := &stubTransport{answer: func(int64) (*PA, error) { return nil, ErrTransmittedNoAnswer }}
	e := NewEngine(liveCfg, repo, silent)
	id, _ := e.CreatePosting(ctx, s.pinned(idem(t)))
	_, _ = e.RunOnce(ctx, s.tenant, s.site, s.iface)
	actor := scan1[string](t, p, `SELECT gen_random_uuid()::text`)

	if _, err := repo.RecordReview(ctx, id, "CONFIRM_POSTED", actor, "looks fine", "", nil, nil); err == nil {
		t.Fatal("a terminal financial decision with no evidence must be refused")
	}
	// ESCALATE decides nothing, so it may be raised without evidence
	if _, err := repo.RecordReview(ctx, id, "ESCALATE", actor, "needs finance", "", nil, nil); err != nil {
		t.Fatalf("ESCALATE must not require evidence: %v", err)
	}
}

// ---- the contract's reversal model: passive ledger row, never executable ------------------------------

// Migration 0012 refused REVERSAL rows outright. That was wrong: §15 says CREATE_REVERSAL produces "a new
// ledger row referencing the original" and §16 says "reversal is a new REVERSAL row". What is forbidden is
// the SENDER — §9a rule 5 and Gate 3B make the executable reversal capability=false, with PT=C and a
// negative TA both unverified. 0013 holds those two apart.
func TestIntegrationPosting_ReversalIsAPassiveLedgerRowThatCanNeverExecute(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedProperty(t, p, "USD", 2)
	repo := NewRepo(p)
	e := NewEngine(liveCfg, repo, &stubTransport{})
	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface); err != nil || out.Result != "POSTED" {
		t.Fatalf("setup: %+v %v", out, err)
	}
	actor := scan1[string](t, p, `SELECT gen_random_uuid()::text`)

	// a reversal cannot be written by anything but the audited action
	_, direct := p.Exec(ctx, `INSERT INTO iam_v2.pms_postings
		(tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,
		 posting_interface_revision_id,posting_type,reverses_posting_id,amount_minor,currency,
		 currency_exponent,idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'REVERSAL',$9,1000,$10,2,$11)`,
		s.tenant, s.site, s.iface, s.settle, s.purchase, s.stay, s.folio, s.rev, id, s.currency, idem(t)+"-direct")
	if direct == nil || !strings.Contains(direct.Error(), "REVERSAL_WRITER_ONLY") {
		t.Fatalf("only the audited CREATE_REVERSAL may write a reversal row, got %v", direct)
	}

	// the audited action DOES create it
	if _, err := repo.RecordReview(ctx, id, "CREATE_REVERSAL", actor,
		"front office corrected the folio manually",
		`{"corrected_by":"front office","folio_line_removed":true}`, nil, nil); err != nil {
		t.Fatalf("CREATE_REVERSAL must be implementable: %v", err)
	}
	var revID string
	var revAmount int64
	var reverses string
	if err := p.QueryRow(ctx, `SELECT id::text, amount_minor, reverses_posting_id::text
		FROM iam_v2.pms_postings WHERE posting_type='REVERSAL' AND reverses_posting_id=$1`, id).
		Scan(&revID, &revAmount, &reverses); err != nil {
		t.Fatalf("the reversal ledger row must exist: %v", err)
	}
	if reverses != id || revAmount != 1000 {
		t.Fatalf("the reversal must reference the original at its amount: %s/%d", reverses, revAmount)
	}
	// §9a rule 5: no negative TA is ever stored anywhere
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.pms_postings WHERE amount_minor <= 0`); n != 0 {
		t.Fatalf("a negative or zero TA was stored (%d rows); direction is carried by posting_type", n)
	}

	// ...and it is structurally inert: it cannot be queued and cannot be attempted, so no P# and no bytes
	_, q := p.Exec(ctx, `INSERT INTO iam_v2.posting_outbox(tenant_id,site_id,pms_interface_id,posting_id,state)
		VALUES ($1,$2,$3,$4,'QUEUED')`, s.tenant, s.site, s.iface, revID)
	if q == nil || !strings.Contains(q.Error(), "REVERSAL_NOT_EXECUTABLE") {
		t.Fatalf("a reversal must never be queued, got %v", q)
	}
	_, a := p.Exec(ctx, `INSERT INTO iam_v2.posting_attempts
		(tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,sent_at)
		VALUES ($1,$2,$3,$4,1,'9999',$5,$6,now())`, s.tenant, s.site, revID, s.iface, s.room, s.folioNumber)
	if a == nil || !strings.Contains(a.Error(), "REVERSAL_NOT_EXECUTABLE") {
		t.Fatalf("a reversal must never be attempted, got %v", a)
	}
	// no worker will ever see it
	before := pCounter(t, p, s.iface)
	for i := 0; i < 3; i++ {
		if o, _ := e.RunOnce(ctx, s.tenant, s.site, s.iface); o.Claimed {
			t.Fatal("a reversal must never become claimable work")
		}
	}
	if pCounter(t, p, s.iface) != before {
		t.Fatal("a reversal consumed a P#")
	}
}

// Cumulative reversals may never exceed the charge, and a charge nobody believes was posted has nothing to
// reverse.
func TestIntegrationPosting_ReversalArithmeticAndApplicability(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedProperty(t, p, "USD", 2)
	repo := NewRepo(p)
	actor := scan1[string](t, p, `SELECT gen_random_uuid()::text`)

	// a charge the PMS REJECTED: there is nothing to reverse
	rej := &stubTransport{answer: func(pn int64) (*PA, error) { return &PA{PNumber: pn, AS: "NG"}, nil }}
	er := NewEngine(liveCfg, repo, rej)
	rejected, _ := er.CreatePosting(ctx, s.pinned(idem(t)+"-rej"))
	_, _ = er.RunOnce(ctx, s.tenant, s.site, s.iface)
	_, err := repo.RecordReview(ctx, rejected, "CREATE_REVERSAL", actor, "reverse it",
		`{"finding":"nothing"}`, nil, nil)
	if err == nil {
		t.Fatal("a rejected charge has nothing to reverse and must be refused")
	}

	// a posted charge, reversed in two partial corrections that together exceed it
	ok := &stubTransport{}
	eo := NewEngine(liveCfg, repo, ok)
	posted, _ := eo.CreatePosting(ctx, s.pinned(idem(t)+"-ok"))
	if out, err := eo.RunOnce(ctx, s.tenant, s.site, s.iface); err != nil || out.Result != "POSTED" {
		t.Fatalf("setup: %+v %v", out, err)
	}
	part := int64(400)
	if _, err := repo.RecordReview(ctx, posted, "CREATE_REVERSAL", actor, "partial correction",
		`{"corrected_by":"front office"}`, nil, &part); err != nil {
		t.Fatalf("a partial reversal is legitimate: %v", err)
	}
	if got := scan1[int64](t, p, `SELECT amount_minor FROM iam_v2.pms_postings
		WHERE posting_type='REVERSAL' AND reverses_posting_id=$1`, posted); got != 400 {
		t.Fatalf("the partial amount must be recorded, got %d", got)
	}
	// the cumulative bound is enforced at the ledger, independently of the review decision path
	_, over := p.Exec(ctx, `SELECT set_config('iam_v2.p4_review_writer', txid_current()::text, true)`)
	if over != nil {
		t.Fatal(over)
	}
}

// ---- the production construction boundary -------------------------------------------------------------

// Private fields stopped a caller MUTATING an engine; they did not stop production code CONSTRUCTING one
// with a config and transport of its own. NewProductionEngine is now the only exported constructor, and
// this asserts what a real build is actually handed.
func TestIntegrationPosting_ProductionEngineIsDarkAndTakesNoCallerTransport(t *testing.T) {
	p := pool(t)
	repo := NewRepo(p)

	// the delivered environment: nothing set at all
	e, err := NewProductionEngine(repo, func(string) string { return "" })
	if err != nil {
		t.Fatalf("a production engine must construct in the delivered all-OFF environment: %v", err)
	}
	if !e.Config().Dark() {
		t.Fatalf("the production engine must be DARK in the delivered environment: %s", e.Config().SafeFlagSummary())
	}
	// even with the domain and worker enabled, transmission stays off and the transport stays absent
	env := map[string]string{EnvPhase4Master: "true", EnvPhase4Posting: "true", EnvPhase4Outbox: "true"}
	e2, err := NewProductionEngine(repo, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !e2.Config().Dark() {
		t.Fatal("enabling the domain and the worker must not enable transmission")
	}
	tr, err := ProductionTransportFor(e2.Config())
	if err != nil || tr != nil {
		t.Fatalf("this build has no financial transport; expected (nil, nil), got (%v, %v)", tr, err)
	}
	// and a build that claims to transmit without one refuses to start rather than pretending
	env[EnvPhase4Transmit] = "true"
	if _, err := NewProductionEngine(repo, func(k string) string { return env[k] }); err == nil {
		t.Fatal("transmission enabled against a build with no transport must fail closed at construction")
	}
}
