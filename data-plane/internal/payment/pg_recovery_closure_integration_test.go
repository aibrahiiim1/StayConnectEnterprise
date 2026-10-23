//go:build integration

package payment

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// After recovery begins the underlying posting_outbox row must stop being claimable, not just be listed in a ledger,
// and the claim path must lose the race with a polling worker, which would otherwise re-send a charge the folio already
// has.

func seedOutboxPosting(t *testing.T, p *pgxpool.Pool, s scope) (postingID, outboxID, ifaceID string) {
	t.Helper()
	ctx := context.Background()
	q := func(dst *string, sql string, args ...any) {
		t.Helper()
		if err := p.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed outbox: %v", err)
		}
	}
	var revID string
	q(&ifaceID, `INSERT INTO iam_v2.pms_interfaces(tenant_id,site_id,connector_kind)
		VALUES ($1,$2,'protel-fias') RETURNING id::text`, s.tenant, s.site)
	// A CHARGE targets the stay's reservation, so the revision carries posting_target_model='RESERVATION'.
	q(&revID, `INSERT INTO iam_v2.pms_interface_revisions
		(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,posting_target_model,config,
		 financial_base_currency,financial_base_currency_exponent)
		VALUES ($1,$2,$3,1,'UTC','RESERVATION','{}','USD',2) RETURNING id::text`,
		s.tenant, s.site, ifaceID)
	if _, err := p.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2 WHERE id=$1`,
		ifaceID, revID); err != nil {
		t.Fatal(err)
	}
	// Runtime freshness axes: a posting is refused against an interface with unknown runtime, so this row is needed to
	// test the recovery hold rather than the freshness gate.
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id,site_id,pms_interface_id,pinned_revision_id,credential_mode,runtime_generation,
		 transport_status,last_connected_at,last_heartbeat_at,continuity_status,last_valid_event_at,
		 sync_status,last_complete_sync_at,resync_generation_seq,published_resync_generation)
		VALUES ($1,$2,$3,$4,'NONE',1,'CONNECTED',now(),now(),'CONTINUOUS',now(),'IN_SYNC',now(),0,0)`,
		s.tenant, s.site, ifaceID, revID); err != nil {
		t.Fatal(err)
	}
	stayID := guarded(t, p, "stay", `INSERT INTO iam_v2.stays
		(tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status)
		VALUES ($1,$2,$3,'R'||substr(md5(random()::text),1,8),'S'||substr(md5(random()::text),1,8),
		        'IN_HOUSE') RETURNING id::text`, s.tenant, s.site, ifaceID)
	// The posting's purchase must carry the same pms_interface_id (composite FK), so the posting rail gets its own
	// purchase and settlement.
	postPurchase := guarded(t, p, "commerce_intent", `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,
		 amount_minor,currency,currency_exponent,state)
		SELECT $1,$2,pu.package_revision_id,$3,$4,'ADMIN_GRANT',100,'USD',2,'AWAITING_SETTLEMENT'
		  FROM iam_v2.purchases pu WHERE pu.id=$5 RETURNING id::text`,
		s.tenant, s.site, ifaceID, stayID, s.purchase)
	postSettlement := guarded(t, p, "commerce_intent", `INSERT INTO iam_v2.settlements(tenant_id,site_id,purchase_id,method,status)
		VALUES ($1,$2,$3,'PMS_POSTING','REQUIRED') RETURNING id::text`, s.tenant, s.site, postPurchase)
	q(&postingID, `INSERT INTO iam_v2.pms_postings
		(tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,g_number,
		 posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key)
		SELECT $1,$2,$3,$4,$5,st.id,st.external_reservation_id,$7,'CHARGE',100,'USD',2,'idem-'||substr(md5(random()::text),1,12)
		  FROM iam_v2.stays st WHERE st.id=$6
		RETURNING id::text`,
		s.tenant, s.site, ifaceID, postSettlement, postPurchase, stayID, revID)
	q(&outboxID, `INSERT INTO iam_v2.posting_outbox(tenant_id,site_id,pms_interface_id,posting_id,state)
		VALUES ($1,$2,$3,$4,'QUEUED') RETURNING id::text`, s.tenant, s.site, ifaceID, postingID)
	return postingID, outboxID, ifaceID
}

func claim(ctx context.Context, p *pgxpool.Pool, outboxID string) error {
	_, err := p.Exec(ctx,
		`UPDATE iam_v2.posting_outbox SET state='IN_FLIGHT' WHERE id=$1 AND state='QUEUED'`, outboxID)
	return err
}

func TestIntegrationRecoveryClosure_ExistingPostingWorkBecomesNonSendable(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := recoverySite(t, p)
	_, outboxID, _ := seedOutboxPosting(t, p, s)
	e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})

	if err := claim(ctx, p, outboxID); err != nil {
		t.Fatalf("baseline: a healthy site could not claim its own posting: %v", err)
	}
	if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "IN_FLIGHT" {
		t.Fatalf("baseline claim did not take: %s", st)
	}

	simulateRestore(t, p, s)
	if got, err := e.ReconcileEpoch(ctx, s.tenant, s.site); err != nil || got != "RECOVERY_ENTERED" {
		t.Fatalf("recovery entry: %s %v", got, err)
	}

	if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "HELD_RECOVERY" {
		t.Fatalf("the outbox row is still %s; the hold is a ledger entry only", st)
	}
	if err := claim(ctx, p, outboxID); err != nil {
		t.Fatalf("the claim errored rather than simply matching nothing: %v", err)
	}
	if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "HELD_RECOVERY" {
		t.Fatalf("a worker reclaimed a held posting: %s", st)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.posting_outbox SET state='QUEUED' WHERE id=$1`, outboxID); err == nil {
		t.Fatal("held posting work was returned to the queue without a reconciliation decision")
	}
}

func TestIntegrationRecoveryClosure_AWorkerRacingRecoveryEntryCannotEscape(t *testing.T) {
	p := pool(t)
	ctx := context.Background()

	// Run both orderings repeatedly: the invariant is that neither outcome leaves a sendable posting.
	for i := 0; i < 12; i++ {
		s := recoverySite(t, p)
		_, outboxID, _ := seedOutboxPosting(t, p, s)
		simulateRestore(t, p, s)
		e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})

		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() { defer wg.Done(); <-start; _ = claim(ctx, p, outboxID) }()
		go func() { defer wg.Done(); <-start; _, _ = e.ReconcileEpoch(ctx, s.tenant, s.site) }()
		close(start)
		wg.Wait()

		st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID)
		if st != "HELD_RECOVERY" {
			t.Fatalf("round %d: the race left the posting %s -- a worker escaped the hold", i, st)
		}
		if active, _ := e.RecoveryActive(ctx, s.tenant, s.site); !active {
			t.Fatalf("round %d: recovery did not take", i)
		}
	}
}

func TestIntegrationRecoveryClosure_OperatorDeclarationFreezesEveryRail(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := recoverySite(t, p)
	_, outboxID, _ := seedOutboxPosting(t, p, s)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})
	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	actor := seedOperator(t, p, s.tenant)

	if _, err := e.DeclareRecovery(ctx, s.tenant, s.site, actor,
		"the provider dashboard disagrees with our settlement record"); err != nil {
		t.Fatal(err)
	}
	if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "HELD_RECOVERY" {
		t.Fatalf("an operator declaration left the posting rail %s", st)
	}
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); CodeOf(err) != ErrRecoveryHeld {
		t.Fatalf("an operator declaration did not hold the payment rail: %v", err)
	}
	kinds := map[string]bool{}
	holds, _ := e.OpenHolds(ctx, s.tenant, s.site, 50)
	for _, h := range holds {
		kinds[h.Kind] = true
	}
	for _, want := range []string{"POSTING_OUTBOX", "PAYMENT_TRANSACTION", "SETTLEMENT"} {
		if !kinds[want] {
			t.Fatalf("an operator declaration did not hold the %s rail (%v)", want, kinds)
		}
	}
}

func TestIntegrationRecoveryClosure_ReconciliationHasRailSpecificConsequences(t *testing.T) {
	p := pool(t)
	ctx := context.Background()

	t.Run("CONFIRMED_COMPLETED leaves no sendable command", func(t *testing.T) {
		s := recoverySite(t, p)
		_, outboxID, _ := seedOutboxPosting(t, p, s)
		e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})
		simulateRestore(t, p, s)
		if _, err := e.ReconcileEpoch(ctx, s.tenant, s.site); err != nil {
			t.Fatal(err)
		}
		actor := seedOperator(t, p, s.tenant)
		holds, _ := e.OpenHolds(ctx, s.tenant, s.site, 50)
		for _, h := range holds {
			if h.Kind != "POSTING_OUTBOX" {
				continue
			}
			if err := e.ResolveHold(ctx, h.ID, "CONFIRMED_COMPLETED", actor,
				"the folio already shows this charge; confirmed in the PMS"); err != nil {
				t.Fatal(err)
			}
		}
		if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "DONE" {
			t.Fatalf("a posting confirmed as already completed is still %s -- it can be sent again", st)
		}
	})

	t.Run("CONFIRMED_NOT_COMPLETED never re-queues", func(t *testing.T) {
		s := recoverySite(t, p)
		_, outboxID, _ := seedOutboxPosting(t, p, s)
		e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})
		simulateRestore(t, p, s)
		if _, err := e.ReconcileEpoch(ctx, s.tenant, s.site); err != nil {
			t.Fatal(err)
		}
		actor := seedOperator(t, p, s.tenant)
		holds, _ := e.OpenHolds(ctx, s.tenant, s.site, 50)
		for _, h := range holds {
			res := "CONFIRMED_NOT_COMPLETED"
			if err := e.ResolveHold(ctx, h.ID, res, actor,
				"checked the folio and the provider; nothing was posted or charged"); err != nil {
				t.Fatalf("%s %s: %v", h.Kind, h.ID, err)
			}
		}
		// It stays held: re-queueing would be the automatic replay recovery exists to prevent; retry is only via the
		// audited review authorization.
		if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "HELD_RECOVERY" {
			t.Fatalf("a not-completed posting was moved to %s by a reconciliation decision", st)
		}
		epoch, err := e.ReleaseRecovery(ctx, s.tenant, s.site, actor,
			"every held item reconciled against the folio and the provider")
		if err != nil {
			t.Fatalf("release: %v", err)
		}
		if epoch == 0 {
			t.Fatal("release returned no epoch")
		}
		if err := claim(ctx, p, outboxID); err != nil {
			t.Fatal(err)
		}
		if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "HELD_RECOVERY" {
			t.Fatalf("releasing recovery made a held posting sendable again: %s", st)
		}
	})

	t.Run("a payment conclusion never becomes an automatic capture", func(t *testing.T) {
		s := recoverySite(t, p)
		e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})
		in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "pay"))
		if err != nil {
			t.Fatal(err)
		}
		if err := beginExecution(t, p, in.ID); err != nil {
			t.Fatal(err)
		}
		simulateRestore(t, p, s)
		if _, err := e.ReconcileEpoch(ctx, s.tenant, s.site); err != nil {
			t.Fatal(err)
		}
		actor := seedOperator(t, p, s.tenant)
		holds, _ := e.OpenHolds(ctx, s.tenant, s.site, 50)
		for _, h := range holds {
			if h.Kind != "PAYMENT_TRANSACTION" {
				continue
			}
			// Even certain operator knowledge that money moved must not become a capture, which would bypass the
			// authenticated provider boundary.
			if err := e.ResolveHold(ctx, h.ID, "CONFIRMED_COMPLETED", actor,
				"the provider dashboard shows this charge as captured"); err != nil {
				t.Fatal(err)
			}
		}
		if st := scan1[string](t, p, `SELECT status FROM iam_v2.payment_transactions WHERE id=$1`, in.ID); st != "UNKNOWN" {
			t.Fatalf("a recovery conclusion moved a payment to %s", st)
		}
		if st := scan1[string](t, p, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != "MANUAL_REVIEW" {
			t.Fatalf("an ambiguous payment did not route to manual review: %s", st)
		}
		if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.entitlements WHERE purchase_id=$1`, s.purchase); n != 0 {
			t.Fatal("a recovery conclusion granted access")
		}
	})
}

func TestIntegrationRecoveryClosure_ReleaseVerifiesTheUnderlyingState(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := recoverySite(t, p)
	_, outboxID, ifaceID := seedOutboxPosting(t, p, s)
	e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})
	simulateRestore(t, p, s)
	if _, err := e.ReconcileEpoch(ctx, s.tenant, s.site); err != nil {
		t.Fatal(err)
	}
	actor := seedOperator(t, p, s.tenant)
	holds, _ := e.OpenHolds(ctx, s.tenant, s.site, 50)
	for _, h := range holds {
		if err := e.ResolveHold(ctx, h.ID, "CONFIRMED_COMPLETED", actor,
			"confirmed against the folio and the provider dashboard"); err != nil {
			t.Fatal(err)
		}
	}
	// The record diverges from the resolution (something re-queues the row); a resolution count is blind to that, hence
	// release interrogates the records.
	if _, err := p.Exec(ctx, `UPDATE iam_v2.posting_outbox SET state='QUEUED' WHERE id=$1 AND state='DONE'`,
		outboxID); err != nil {
		t.Fatalf("staging the divergence: %v", err)
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.financial_recovery_holds
		WHERE tenant_id=$1 AND site_id=$2 AND resolution IS NULL`, s.tenant, s.site); n != 0 {
		t.Fatalf("setup: %d holds are still open, so this would not test the divergence", n)
	}
	if _, err := e.ReleaseRecovery(ctx, s.tenant, s.site, actor,
		"all holds resolved"); CodeOf(err) != ErrRecoveryHeld {
		t.Fatalf("recovery was released with a sendable posting still queued: %v", err)
	}
	_ = ifaceID

}

func TestIntegrationRecoveryClosure_AForgedActorIsRefused(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	a := recoverySite(t, p)
	b := recoverySite(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})
	simulateRestore(t, p, a)
	if _, err := e.ReconcileEpoch(ctx, a.tenant, a.site); err != nil {
		t.Fatal(err)
	}
	holds, _ := e.OpenHolds(ctx, a.tenant, a.site, 10)
	if len(holds) == 0 {
		t.Fatal("nothing was held")
	}

	if err := e.ResolveHold(ctx, holds[0].ID, "CONFIRMED_COMPLETED",
		"11111111-2222-3333-4444-555555555555",
		"a decision attributed to nobody"); CodeOf(err) != ErrUntrustedInput {
		t.Fatalf("an invented author was accepted: %v", err)
	}
	foreign := seedOperator(t, p, b.tenant)
	if err := e.ResolveHold(ctx, holds[0].ID, "CONFIRMED_COMPLETED", foreign,
		"a decision attributed to another tenant's operator"); CodeOf(err) != ErrUntrustedInput {
		t.Fatalf("another tenant's operator authored a decision: %v", err)
	}
	if r := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.financial_recovery_holds
		WHERE id=$1 AND resolution IS NOT NULL`, holds[0].ID); r != 0 {
		t.Fatal("a forged decision was recorded")
	}
	own := seedOperator(t, p, a.tenant)
	if err := e.ResolveHold(ctx, holds[0].ID, "CONFIRMED_COMPLETED", own,
		"confirmed against the folio and the provider dashboard"); err != nil {
		t.Fatalf("the site's own operator was refused: %v", err)
	}
}
