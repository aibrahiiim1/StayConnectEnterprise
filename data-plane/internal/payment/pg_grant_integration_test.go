//go:build integration

package payment

import (
	"context"
	"sync"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// Exactly-once against the REAL Phase-2 grant path.
//
// The tests above use a recording double to isolate the payment machine. These use the actual
// CommerceEngine writing actual iam_v2.entitlements rows, because "we reuse the Phase-2 path" is a claim
// about the production wiring and a double cannot prove it. The assertion throughout is a COUNT of
// entitlement rows for the purchase: one, or zero, and never two.

func realGranter(t *testing.T) Granter {
	t.Helper()
	e, err := iamv2.NewCommerceEngine(
		iamv2.CommerceConfig{MasterEnabled: true, PortalEnabled: true},
		iamv2.NewPgCommerceRepository(pool(t)), iamv2.NopObserver{})
	if err != nil {
		t.Fatalf("commerce engine: %v", err)
	}
	return CommerceGranter{Engine: e}
}

func entitlements(t *testing.T, purchaseID string) int {
	t.Helper()
	return scan1[int](t, pool(t), `SELECT count(*)::int FROM iam_v2.entitlements WHERE purchase_id=$1`, purchaseID)
}

// One capture, one entitlement, written by the Phase-2 path.
func TestIntegrationGrant_CaptureGrantsThroughThePhase2Path(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), realGranter(t))
	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, s.merchant, idem(t, "1"))
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
	// the purchase advanced through the same MarkPurchaseGranted the free path uses
	if st := scan1[string](t, p, `SELECT state FROM iam_v2.purchases WHERE id=$1`, s.purchase); st != "GRANTED" {
		t.Fatalf("expected GRANTED, got %s", st)
	}
	// and the entitlement carries the pins from the quote, not from anything a caller supplied
	if got := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.entitlements en
		JOIN iam_v2.purchases pu ON pu.id = en.purchase_id
	   WHERE en.purchase_id=$1 AND en.package_revision_id = pu.package_revision_id`, s.purchase); got != 1 {
		t.Fatal("the entitlement is not pinned to the purchase's package revision")
	}
}

// Duplicate commands, duplicate callbacks and concurrent callbacks converge on ONE entitlement.
func TestIntegrationGrant_ExactlyOnceUnderDuplicatesAndConcurrency(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), realGranter(t))
	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, s.merchant, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); err != nil {
		t.Fatal(err)
	}

	// 12 concurrent deliveries of the same capture, plus a re-execution of the same command
	var wg sync.WaitGroup
	ids := make(chan string, 13)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, _ := e.ApplyCallback(ctx, s.tenant, "test-double", s.merchant, in.ClientRef,
				"evt-race", "payment.captured", "CAPTURED", "prv_"+in.ClientRef, nil)
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

// Restart replay: a brand-new engine over the same durable rows re-applies the same provider event and
// grants nothing further. This is the crash-recovery case -- the process that granted is gone.
func TestIntegrationGrant_RestartReplayGrantsNothingFurther(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e1 := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), realGranter(t))
	in, _ := e1.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, s.merchant, idem(t, "1"))
	if _, err := e1.Execute(ctx, s.tenant, s.site, in.ID); err != nil {
		t.Fatal(err)
	}
	first := entitlements(t, s.purchase)

	// a fresh engine, as after a restart, with no memory of anything
	e2 := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), realGranter(t))
	for i := 0; i < 3; i++ {
		if _, err := e2.ApplyCallback(ctx, s.tenant, "test-double", s.merchant, in.ClientRef,
			"exec:"+in.ClientRef, "execution_result", "CAPTURED", "prv_"+in.ClientRef, nil); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
	if n := entitlements(t, s.purchase); n != first || n != 1 {
		t.Fatalf("a restart replay changed the entitlement count: %d -> %d", first, n)
	}
}

// FAILED and UNKNOWN grant ZERO. This is the whole reason the grant is gated on the settlement rather than
// on the charge: MANUAL_REVIEW is precisely the state in which nobody knows whether money moved.
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
			in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, s.merchant, idem(t, "1"))
			_, _ = e.Execute(ctx, s.tenant, s.site, in.ID)
			if st := scan1[string](t, p, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != tc.want {
				t.Fatalf("expected settlement %s, got %s", tc.want, st)
			}
			if n := entitlements(t, s.purchase); n != 0 {
				t.Fatalf("%s granted %d entitlements", tc.name, n)
			}
			// and an out-of-band capture claiming the same intent cannot rescue it either: the intent is
			// terminal, so there is nothing left for a late callback to move.
			_, _ = e.ApplyCallback(ctx, s.tenant, "test-double", s.merchant, in.ClientRef,
				"evt-late", "payment.captured", "CAPTURED", "", nil)
			if n := entitlements(t, s.purchase); n != 0 {
				t.Fatalf("%s: a late callback granted %d entitlements", tc.name, n)
			}
		})
	}
}

// Substitution fails closed: a settlement cannot be pointed at a different tenant's or site's purchase, and
// the grant path resolves the purchase itself rather than accepting one.
func TestIntegrationGrant_SubstitutionFailsClosed(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	a := seedPaidChain(t, p)
	b := seedPaidChain(t, p)
	g := realGranter(t)

	// site A's tenant asking to grant site B's purchase
	if _, err := g.GrantSettledPurchase(ctx, a.tenant, a.site, b.purchase); err == nil {
		t.Fatal("a cross-tenant purchase was granted")
	}
	// the right tenant, the wrong site
	if _, err := g.GrantSettledPurchase(ctx, b.tenant, a.site, b.purchase); err == nil {
		t.Fatal("a cross-site purchase was granted")
	}
	// and the correct owner still cannot grant while the settlement has not reached SETTLED
	if _, err := g.GrantSettledPurchase(ctx, b.tenant, b.site, b.purchase); err == nil {
		t.Fatal("an unsettled purchase was granted")
	}
	if n := entitlements(t, b.purchase); n != 0 {
		t.Fatalf("substitution created %d entitlements", n)
	}
}
