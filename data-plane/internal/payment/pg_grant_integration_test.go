//go:build integration

package payment

import (
	"context"
	"sync"
	"testing"
)

// Exactly-once against the real Phase-2 grant path: real CommerceEngine writing real iam_v2.entitlements rows,
// asserting a COUNT of entitlements per purchase of one or zero, never two.

// realGranter is the production granter; no double, since the claim is about production wiring.
func realGranter(t *testing.T) Granter {
	t.Helper()
	return SQLGranter{Pool: pool(t)}
}

func entitlements(t *testing.T, purchaseID string) int {
	t.Helper()
	return scan1[int](t, pool(t), `SELECT count(*)::int FROM iam_v2.entitlements WHERE purchase_id=$1`, purchaseID)
}

func TestIntegrationGrant_CaptureGrantsThroughThePhase2Path(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), realGranter(t))
	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Execute(ctx, s.tenant, s.site, in.ID)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out.EntitlementID == "" || out.AlreadyGranted {
		t.Fatalf("expected a first grant, got %+v", out)
	}
	if n := entitlements(t, s.purchase); n != 1 {
		t.Fatalf("expected exactly one entitlement, got %d", n)
	}
	if st := scan1[string](t, p, `SELECT state FROM iam_v2.purchases WHERE id=$1`, s.purchase); st != "GRANTED" {
		t.Fatalf("expected GRANTED, got %s", st)
	}
	if got := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.entitlements en
		JOIN iam_v2.purchases pu ON pu.id = en.purchase_id
	   WHERE en.purchase_id=$1 AND en.package_revision_id = pu.package_revision_id`, s.purchase); got != 1 {
		t.Fatal("the entitlement is not pinned to the purchase's package revision")
	}
}

func TestIntegrationGrant_ExactlyOnceUnderDuplicatesAndConcurrency(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), realGranter(t))
	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	ids := make(chan string, 13)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, _ := e.HandleProviderNotification(ctx,
				BuildNotification(in.ClientRef, "evt-race", OutcomeCaptured, "prv_"+in.ClientRef))
			ids <- o.EntitlementID
		}(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); o, _ := e.Execute(ctx, s.tenant, s.site, in.ID); ids <- o.EntitlementID }()
	wg.Wait()
	close(ids)
	for id := range ids {
		if id != "" && id != scan1[string](t, p, `SELECT id::text FROM iam_v2.entitlements WHERE purchase_id=$1`, s.purchase) {
			t.Fatalf("a concurrent caller was handed a different entitlement: %s", id)
		}
	}
	if n := entitlements(t, s.purchase); n != 1 {
		t.Fatalf("expected exactly one entitlement after the race, got %d", n)
	}
}

// Restart replay: a fresh engine over the same durable rows re-applies the event and grants nothing further.
func TestIntegrationGrant_RestartReplayGrantsNothingFurther(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e1 := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), realGranter(t))
	in, _ := e1.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if _, err := e1.Execute(ctx, s.tenant, s.site, in.ID); err != nil {
		t.Fatal(err)
	}
	first := entitlements(t, s.purchase)

	e2 := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), realGranter(t))
	for i := 0; i < 3; i++ {
		if _, err := e2.HandleProviderNotification(ctx,
			BuildNotification(in.ClientRef, "exec:"+in.ClientRef, OutcomeCaptured, "prv_"+in.ClientRef)); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
	if n := entitlements(t, s.purchase); n != first || n != 1 {
		t.Fatalf("a restart replay changed the entitlement count: %d -> %d", first, n)
	}
}

// FAILED and UNKNOWN grant ZERO: the grant is gated on the settlement because MANUAL_REVIEW is the state where nobody
// knows whether money moved.
func TestIntegrationGrant_FailedAndUnknownGrantZero(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		res  Result
		want string
	}{
		{"declined", Result{Outcome: OutcomeDeclined, ReasonCode: "do_not_honour"}, "FAILED"},
		{"unknown", Result{Outcome: OutcomeUnknown, ReasonCode: "timeout"}, "MANUAL_REVIEW"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := seedPaidChain(t, p)
			e := NewEngine(liveCfg, p, NewScriptedProvider(tc.res), realGranter(t))
			in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
			_, _ = e.Execute(ctx, s.tenant, s.site, in.ID)
			if st := scan1[string](t, p, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != tc.want {
				t.Fatalf("expected settlement %s, got %s", tc.want, st)
			}
			if n := entitlements(t, s.purchase); n != 0 {
				t.Fatalf("%s granted %d entitlements", tc.name, n)
			}
			// An out-of-band capture for the same intent cannot rescue it: the intent is terminal.
			_, _ = e.HandleProviderNotification(ctx,
				BuildNotification(in.ClientRef, "evt-late", OutcomeCaptured, ""))
			if n := entitlements(t, s.purchase); n != 0 {
				t.Fatalf("%s: a late callback granted %d entitlements", tc.name, n)
			}
		})
	}
}

// Substitution fails closed: the grant path resolves the purchase itself rather than accepting one.
func TestIntegrationGrant_SubstitutionFailsClosed(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	a := seedPaidChain(t, p)
	b := seedPaidChain(t, p)
	g := realGranter(t)

	if _, err := g.GrantSettledSettlement(ctx, a.tenant, a.site, b.settlement); err == nil {
		t.Fatal("a cross-tenant settlement was granted")
	}
	if _, err := g.GrantSettledSettlement(ctx, b.tenant, a.site, b.settlement); err == nil {
		t.Fatal("a cross-site settlement was granted")
	}
	if _, err := g.GrantSettledSettlement(ctx, b.tenant, b.site, b.settlement); err == nil {
		t.Fatal("an unsettled settlement was granted")
	}
	// The settlement is driven to SETTLED via a real captured charge, because forcing the status is refused by an
	// invariant and a workaround would test an impossible state.
	c := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})
	in, err := e.CreateChargeIntent(ctx, c.tenant, c.site, c.settlement, idem(t, "pending-origin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := beginExecution(t, p, in.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.p4_apply_provider_outcome($1,'evt-po','x','CAPTURED',NULL,'{}'::jsonb)`,
		in.ClientRef); err != nil {
		t.Fatal(err)
	}
	if st := scan1[string](t, p, `SELECT status FROM iam_v2.settlements WHERE id=$1`, c.settlement); st != "SETTLED" {
		t.Fatalf("setup: expected SETTLED, got %s", st)
	}
	// staging: a purchase write needs the commerce_intent controlled scope on the full schema
	guardedExec(t, p, "commerce_intent", `UPDATE iam_v2.purchases SET state='PENDING' WHERE id=$1`, c.purchase)
	if _, err := g.GrantSettledSettlement(ctx, c.tenant, c.site, c.settlement); err == nil {
		t.Fatal("a PENDING purchase took the paid grant path")
	}
	if n := entitlements(t, c.purchase); n != 0 {
		t.Fatalf("a PENDING-origin grant created %d entitlements", n)
	}
	if n := entitlements(t, b.purchase); n != 0 {
		t.Fatalf("substitution created %d entitlements", n)
	}
}
