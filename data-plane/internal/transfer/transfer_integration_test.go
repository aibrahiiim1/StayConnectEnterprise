//go:build integration && phase5

package transfer

// THE F9 SERIES, against a real PostgreSQL.
//
// A transfer is the one Phase-5 operation that ENDS a guest's live access, so almost every test here is
// about what it refuses to do and about what survives when it does act. The three properties that would be
// silently wrong if untested:
//
//   * a room move must not be recordable as a transfer (two Stays on one interface satisfy "two different
//     Stays", which is all the original CHECK required);
//   * a failed transfer must leave the guest exactly as they were — terminating the source and then failing
//     to land them anywhere is worse than refusing;
//   * concurrent transfers must produce one lineage row, and two operators transferring in OPPOSITE
//     directions between the same pair must not deadlock.

import (
	"context"
	"errors"
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
		t.Skip("PHASE3_TEST_DSN not set; skipping transfer integration")
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

const operator = "33333333-3333-3333-3333-333333333333"

type fx struct {
	tenant, site   string
	ifaceA, ifaceB string
	stayA, stayB   string
	stayA2         string // a SECOND stay on interface A — the room-move shape
	device         string
	pkg            string
}

// seed builds two PMS interfaces with an IN_HOUSE Stay on each, a second Stay on the first interface, and a
// zero-price no-posting package for the guest to land on.
func seed(t *testing.T, p *pgxpool.Pool) fx {
	t.Helper()
	ctx := context.Background()
	var f fx
	if err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  ia AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'protel-fias','ACTIVE' FROM si RETURNING id,tenant_id,site_id),
	  ib AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'protel-fias','ACTIVE' FROM si RETURNING id),
	  sa AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	           external_stay_identity,status,normalized_room_number)
	         SELECT gen_random_uuid(), ia.tenant_id, ia.site_id, ia.id,'RES-A','SA','IN_HOUSE','101' FROM ia RETURNING id),
	  sa2 AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	            external_stay_identity,status,normalized_room_number)
	          SELECT gen_random_uuid(), ia.tenant_id, ia.site_id, ia.id,'RES-A2','SA2','IN_HOUSE','102' FROM ia RETURNING id),
	  sb AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	           external_stay_identity,status,normalized_room_number)
	         SELECT gen_random_uuid(), ia.tenant_id, ia.site_id, ib.id,'RES-B','SB','IN_HOUSE','201' FROM ia, ib RETURNING id),
	  dv AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), ia.tenant_id, ia.site_id, gen_random_uuid(),'02:00:00:00:00:01'::macaddr FROM ia RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'XFER-PLAN' FROM si RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,
	            max_concurrent_devices,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id,1,4,'VALIDITY_WINDOW',1000000 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'XFER-GRACE',true,true FROM si RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
	            service_plan_revision_id,package_type,price_minor,settlement_methods)
	          SELECT gen_random_uuid(), si.tenant_id, si.id, ip.id,1, spr.id,'CHECKOUT_GRACE',0,
	                 ARRAY['NOT_REQUIRED']::text[] FROM si, ip, spr RETURNING id)
	SELECT (SELECT tenant_id FROM ia)::text,(SELECT site_id FROM ia)::text,(SELECT id FROM ia)::text,
	       (SELECT id FROM ib)::text,(SELECT id FROM sa)::text,(SELECT id FROM sb)::text,
	       (SELECT id FROM sa2)::text,(SELECT id FROM dv)::text,(SELECT id FROM ipr)::text`).
		Scan(&f.tenant, &f.site, &f.ifaceA, &f.ifaceB, &f.stayA, &f.stayB, &f.stayA2, &f.device, &f.pkg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, f.pkg); err != nil {
		t.Fatalf("seed current revision: %v", err)
	}
	return f
}

// grant gives a Stay a live entitlement with the device bound and an active session — the state a guest who
// is actually online is in, which is the only state a transfer is interesting in.
func grant(t *testing.T, p *pgxpool.Pool, f fx, stay string) (entitlement, session string) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var purchase string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,'ADMIN_GRANT',0,'GRANTED') RETURNING id::text`,
		f.tenant, f.site, f.pkg, stay).Scan(&purchase); err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,
		 time_accounting_mode,end_mode,window_ends_at,status)
		SELECT $1,$2,$3,$4,'{}'::jsonb, ipr.service_plan_revision_id, $5,'VALIDITY_WINDOW','VALIDITY_WINDOW',
		       now()+interval '4 hours','PENDING'
		  FROM iam_v2.internet_package_revisions ipr WHERE ipr.id=$5
		RETURNING id::text`, f.tenant, f.site, stay, purchase, f.pkg).Scan(&entitlement); err != nil {
		t.Fatalf("entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now(),NULL)`,
		entitlement); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
		(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
		VALUES ($1,$2,$3,$4,'AUTHORIZED', now(), now())`,
		f.tenant, f.site, entitlement, f.device); err != nil {
		t.Fatalf("bind device: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(tenant_id,site_id,entitlement_id,device_id,state) VALUES ($1,$2,$3,$4,'active')
		RETURNING id::text`, f.tenant, f.site, entitlement, f.device).Scan(&session); err != nil {
		t.Fatalf("session: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return entitlement, session
}

func req(f fx, from, to string) Request {
	return Request{Tenant: f.tenant, Site: f.site, FromStay: from, ToStay: to,
		Operator: operator, Reason: "guest moved to the other property", GraceValidFor: 2 * time.Hour}
}

// F9 baseline + F9-g: the access moves, and the guest never notices.
func TestIntegration_F9_TransferMovesAccessSeamlessly(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := New(p)
	fromEnt, session := grant(t, p, f, f.stayA)
	ctx := context.Background()

	out, err := s.Execute(ctx, req(f, f.stayA, f.stayB))
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if out.FromEntitlement != fromEnt {
		t.Fatalf("moved the wrong entitlement")
	}
	if out.DevicesRebound != 1 || out.SessionsRebound != 1 {
		t.Fatalf("rebound %d device(s) and %d session(s), want 1 and 1", out.DevicesRebound, out.SessionsRebound)
	}
	// THE SAME session row, re-pointed. A new session id would mean the guest was logged out and back in,
	// which is exactly what "seamless" excludes -- and it is what the kernel state is keyed on.
	var sessEnt, sessState string
	if err := p.QueryRow(ctx, `SELECT entitlement_id::text, state FROM iam_v2.sessions WHERE id=$1`, session).
		Scan(&sessEnt, &sessState); err != nil {
		t.Fatalf("session read: %v", err)
	}
	if sessEnt != out.ToEntitlement || sessState != "active" {
		t.Fatalf("the session did not move in place: entitlement=%s state=%s", sessEnt, sessState)
	}
	// The source is terminated AS TRANSFERRED — the state the lineage row requires.
	var status, reason string
	if err := p.QueryRow(ctx, `SELECT status, COALESCE(terminal_reason,'') FROM iam_v2.entitlements WHERE id=$1`,
		fromEnt).Scan(&status, &reason); err != nil {
		t.Fatalf("source read: %v", err)
	}
	if status != "TERMINATED" || reason != "TRANSFERRED" {
		t.Fatalf("source is %s/%s", status, reason)
	}
	// F9-d: NOT a supersession.
	var supersedes *string
	if err := p.QueryRow(ctx, `SELECT supersedes_entitlement_id::text FROM iam_v2.entitlements WHERE id=$1`,
		out.ToEntitlement).Scan(&supersedes); err != nil {
		t.Fatalf("destination read: %v", err)
	}
	if supersedes != nil {
		t.Fatalf("the transfer-created entitlement carries a supersedes pointer")
	}
	// The typed lineage and the stay link both exist.
	var links, transfers int
	if err := p.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM iam_v2.entitlement_transfers WHERE from_entitlement_id=$1),
		(SELECT count(*) FROM iam_v2.stay_links WHERE from_stay=$2 AND to_stay=$3 AND reason='CROSS_PMS_TRANSFER')`,
		fromEnt, f.stayA, f.stayB).Scan(&transfers, &links); err != nil {
		t.Fatalf("lineage read: %v", err)
	}
	if transfers != 1 || links != 1 {
		t.Fatalf("lineage: %d transfer(s), %d link(s)", transfers, links)
	}
	// No money moved and no posting was attempted.
	var amount int64
	var postings int
	if err := p.QueryRow(ctx, `SELECT
		(SELECT amount_minor FROM iam_v2.purchases pu JOIN iam_v2.entitlements e ON e.purchase_id=pu.id WHERE e.id=$1),
		(SELECT count(*) FROM iam_v2.pms_postings WHERE stay_id=$2)`, out.ToEntitlement, f.stayB).
		Scan(&amount, &postings); err != nil {
		t.Fatalf("financial read: %v", err)
	}
	if amount != 0 || postings != 0 {
		t.Fatalf("the transfer moved money: amount=%d postings=%d", amount, postings)
	}
}

// F9-b / F9-c: a room move is not a transfer, and refusing it leaves the guest untouched.
func TestIntegration_F9_SameInterfaceIsARoomMoveNotATransfer(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := New(p)
	fromEnt, session := grant(t, p, f, f.stayA)
	ctx := context.Background()

	if _, err := s.Execute(ctx, req(f, f.stayA, f.stayA2)); !errors.Is(err, ErrSameInterface) {
		t.Fatalf("a same-interface move was accepted as a transfer: %v", err)
	}
	// F9-c: the guest is EXACTLY as they were. A refusal that had already terminated the source would have
	// taken their access away to tell them no.
	var status string
	var sessState string
	if err := p.QueryRow(ctx, `SELECT e.status, se.state FROM iam_v2.entitlements e
		JOIN iam_v2.sessions se ON se.id=$2 WHERE e.id=$1`, fromEnt, session).Scan(&status, &sessState); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "ACTIVE" || sessState != "active" {
		t.Fatalf("a refused transfer disturbed the guest: entitlement=%s session=%s", status, sessState)
	}
	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_transfers WHERE from_entitlement_id=$1`,
		fromEnt).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("a refused transfer recorded lineage")
	}
}

// F9-a: the destination must already exist from verified PMS state, and no Stay is ever created.
func TestIntegration_F9_DestinationMustAlreadyExist(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := New(p)
	grant(t, p, f, f.stayA)
	ctx := context.Background()

	var before int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.stays WHERE tenant_id=$1`, f.tenant).Scan(&before); err != nil {
		t.Fatalf("count: %v", err)
	}
	_, err := s.Execute(ctx, req(f, f.stayA, "00000000-0000-0000-0000-0000000000ff"))
	if !errors.Is(err, ErrDestinationMissing) {
		t.Fatalf("a transfer to a non-existent stay: %v", err)
	}
	var after int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.stays WHERE tenant_id=$1`, f.tenant).Scan(&after); err != nil {
		t.Fatalf("count: %v", err)
	}
	if after != before {
		t.Fatalf("the refusal created %d stay(s)", after-before)
	}

	// A destination that exists but has checked out is refused too: there is nobody there to receive access.
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now()
		WHERE id=$1`, f.stayB); err != nil {
		t.Fatalf("check the destination out: %v", err)
	}
	if _, err := s.Execute(ctx, req(f, f.stayA, f.stayB)); !errors.Is(err, ErrDestinationNotEligible) {
		t.Fatalf("a transfer onto a departed stay: %v", err)
	}
}

// The destination must not already hold access: landing on it would require superseding across subjects.
func TestIntegration_F9_OccupiedDestinationIsRefused(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := New(p)
	grant(t, p, f, f.stayA)
	grant(t, p, f, f.stayB)
	if _, err := s.Execute(context.Background(), req(f, f.stayA, f.stayB)); !errors.Is(err, ErrDestinationOccupied) {
		t.Fatalf("a transfer onto an occupied stay: %v", err)
	}
}

// F9-f: idempotency. The same transfer twice is one lineage row and one answer.
func TestIntegration_F9_SecondTransferIsRefused(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := New(p)
	grant(t, p, f, f.stayA)
	ctx := context.Background()
	if _, err := s.Execute(ctx, req(f, f.stayA, f.stayB)); err != nil {
		t.Fatalf("first transfer: %v", err)
	}
	// The source now has no live access, so the second attempt is refused for that reason before it can
	// reach the uniqueness constraint — either way it is a refusal, never a second lineage row.
	if _, err := s.Execute(ctx, req(f, f.stayA, f.stayB)); err == nil {
		t.Fatalf("the same transfer succeeded twice")
	}
	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_transfers
		WHERE from_stay_id=$1 AND to_stay_id=$2`, f.stayA, f.stayB).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("%d lineage rows for one transfer", n)
	}
}

// F9-f, concurrently: many operators, one transfer.
func TestIntegration_F9_ConcurrentTransfersProduceExactlyOne(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	grant(t, p, f, f.stayA)

	const racers = 24
	var wg sync.WaitGroup
	results := make([]error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := New(p)
			<-start
			_, err := s.Execute(context.Background(), req(f, f.stayA, f.stayB))
			results[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for _, err := range results {
		if err == nil {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d of %d concurrent transfers succeeded, want exactly 1", won, racers)
	}
	var n int
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.entitlement_transfers
		WHERE from_stay_id=$1 AND to_stay_id=$2`, f.stayA, f.stayB).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("%d lineage rows after %d concurrent attempts", n, racers)
	}
}

// F9-i: operators transferring in OPPOSITE directions between the same pair of Stays.
//
// Locking the two Stays in CALLER order would let tx1 hold A while waiting for B and tx2 hold B while waiting
// for A — the classic pair, and the pair a busy property produces. The fixed id order removes the cycle.
//
// A single opposite pair almost never interleaves, so this drives MANY pairs at once against several
// independent Stay pairs: the point is to give the interleaving a real chance rather than to assert it
// happened. Every call must RETURN — a deadlock surfaces as SQLSTATE 40P01, and a lock cycle that PostgreSQL
// did not break surfaces as the timeout.
func TestIntegration_F9_OppositeDirectionsDoNotDeadlock(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()

	const pairs = 16
	type pair struct{ f fx }
	ps := make([]pair, pairs)
	for i := range ps {
		f := seed(t, p)
		grant(t, p, f, f.stayA)
		grant(t, p, f, f.stayB)
		ps[i] = pair{f: f}
	}

	done := make(chan error, pairs*2)
	start := make(chan struct{})
	for _, pr := range ps {
		f := pr.f
		go func() { <-start; _, err := New(p).Execute(ctx, req(f, f.stayA, f.stayB)); done <- err }()
		go func() { <-start; _, err := New(p).Execute(ctx, req(f, f.stayB, f.stayA)); done <- err }()
	}
	close(start)

	deadline := time.After(60 * time.Second)
	for i := 0; i < pairs*2; i++ {
		select {
		case err := <-done:
			// Both stays hold access, so BOTH directions are correctly refused by the occupied-destination
			// rule. That is not what is being measured: what matters is that the call returned, and that it
			// did not return a deadlock.
			if err == nil {
				t.Fatalf("a transfer onto an occupied stay succeeded")
			}
			if isDeadlock(err) {
				t.Fatalf("opposite-direction transfers deadlocked: %v", err)
			}
		case <-deadline:
			t.Fatalf("%d of %d opposite-direction transfers never returned; the lock order is not deterministic",
				pairs*2-i, pairs*2)
		}
	}
}

func isDeadlock(err error) bool {
	return err != nil && (contains(err.Error(), "deadlock") || contains(err.Error(), "40P01"))
}

func contains(h, n string) bool {
	return len(h) >= len(n) && (func() bool {
		for i := 0; i+len(n) <= len(h); i++ {
			if h[i:i+len(n)] == n {
				return true
			}
		}
		return false
	})()
}

// Authorization: an operator and a bounded reason, or nothing happens.
func TestIntegration_F9_RequiresAnOperatorAndAReason(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := New(p)
	fromEnt, _ := grant(t, p, f, f.stayA)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		mut  func(*Request)
	}{
		{"no operator", func(r *Request) { r.Operator = "" }},
		{"no reason", func(r *Request) { r.Reason = "" }},
		{"a reason too short to mean anything", func(r *Request) { r.Reason = "x" }},
		{"no grace window", func(r *Request) { r.GraceValidFor = 0 }},
		{"an absurd grace window", func(r *Request) { r.GraceValidFor = 400 * time.Hour }},
	} {
		r := req(f, f.stayA, f.stayB)
		tc.mut(&r)
		if _, err := s.Execute(ctx, r); !errors.Is(err, ErrNotAuthorized) {
			t.Fatalf("%s: err = %v, want ErrNotAuthorized", tc.name, err)
		}
	}
	var status string
	if err := p.QueryRow(ctx, `SELECT status FROM iam_v2.entitlements WHERE id=$1`, fromEnt).Scan(&status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "ACTIVE" {
		t.Fatalf("an unauthorized attempt disturbed the guest: %s", status)
	}
}

// FAIL CLOSED: with nothing to land the guest on, the transfer refuses rather than terminating the source.
func TestIntegration_F9_NoLandingPackageRefusesWithoutTakingAccessAway(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := New(p)
	fromEnt, session := grant(t, p, f, f.stayA)
	ctx := context.Background()
	// Retire the only eligible package by pointing the package at no current revision.
	if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages SET active=false
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, f.pkg); err != nil {
		t.Fatalf("retire the package: %v", err)
	}
	if _, err := s.Execute(ctx, req(f, f.stayA, f.stayB)); !errors.Is(err, ErrNoGracePackage) {
		t.Fatalf("a transfer with nowhere to land: %v", err)
	}
	var status, sessState string
	if err := p.QueryRow(ctx, `SELECT e.status, se.state FROM iam_v2.entitlements e
		JOIN iam_v2.sessions se ON se.id=$2 WHERE e.id=$1`, fromEnt, session).Scan(&status, &sessState); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "ACTIVE" || sessState != "active" {
		t.Fatalf("the guest lost access to a refusal: entitlement=%s session=%s", status, sessState)
	}
}

// F9-h: ambiguity is a signal. This package must never read a resolution to decide anything.
func TestIntegration_F9_AmbiguityIsNotAnAuthorization(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := New(p)
	grant(t, p, f, f.stayA)
	ctx := context.Background()

	// Record an AMBIGUOUS resolution naming both stays' guest network. If ambiguity were ever treated as
	// evidence, THIS is the state that would make an unauthorized transfer look justified.
	var gn string
	if err := p.QueryRow(ctx, `INSERT INTO public.guest_networks(id,tenant_id,site_id)
		VALUES (gen_random_uuid(),$1,$2) RETURNING id::text`, f.tenant, f.site).Scan(&gn); err != nil {
		t.Fatalf("seed network: %v", err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('auth_resolution')`); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.auth_resolutions
		(tenant_id,site_id,guest_network_id,resolved_stay_id,outcome_code)
		VALUES ($1,$2,$3,NULL,'AMBIGUOUS')`, f.tenant, f.site, gn); err != nil {
		t.Fatalf("seed resolution: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The transfer still needs everything it always needed. Ambiguity changed nothing, in either direction.
	r := req(f, f.stayA, f.stayA2) // same interface: still a room move
	if _, err := s.Execute(ctx, r); !errors.Is(err, ErrSameInterface) {
		t.Fatalf("an AMBIGUOUS resolution changed the answer: %v", err)
	}
	r2 := req(f, f.stayA, f.stayB)
	r2.Operator = ""
	if _, err := s.Execute(ctx, r2); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("an AMBIGUOUS resolution substituted for an operator: %v", err)
	}
}

// The preview tells the operator the same thing the execution would, before they commit to it.
func TestIntegration_F9_PreviewAgreesWithExecution(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := New(p)
	grant(t, p, f, f.stayA)
	ctx := context.Background()

	ok, err := s.PreviewTransfer(ctx, f.tenant, f.site, f.stayA, f.stayB)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if ok.Blocker != "" {
		t.Fatalf("preview blocked a transfer that would succeed: %s", ok.Blocker)
	}
	if ok.LiveDevices != 1 || ok.LiveSessions != 1 {
		t.Fatalf("preview miscounted what is about to move: %d device(s), %d session(s)",
			ok.LiveDevices, ok.LiveSessions)
	}
	if ok.FromRoom != "101" || ok.ToRoom != "201" {
		t.Fatalf("preview does not show the operator which rooms are involved: %q -> %q", ok.FromRoom, ok.ToRoom)
	}

	blocked, err := s.PreviewTransfer(ctx, f.tenant, f.site, f.stayA, f.stayA2)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if blocked.Blocker != ErrSameInterface.Error() {
		t.Fatalf("preview did not name the room-move blocker: %q", blocked.Blocker)
	}
	// ...and the execution agrees, which is the property that makes the preview worth showing.
	if _, err := s.Execute(ctx, req(f, f.stayA, f.stayA2)); !errors.Is(err, ErrSameInterface) {
		t.Fatalf("execution disagreed with the preview: %v", err)
	}
}
