package iamv2

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// THE PHASE-2 FREE-COMMERCE ACQUISITION PATH, GATED — proven against a real database, through CreateQuote
// and ConfirmFreePurchase rather than through the rule they call.
//
// This path shares no code with the PMS/stay grant. That is exactly why it needs its own proof: a rule that
// only one of two entry points consults is not an invariant, it is a coincidence.
//
// What has to be true, and each is a test below:
//
//   * a pre-existing immutable AGGREGATE_ONLINE_TIME revision is STORED AND UNCHANGED while the capability
//     is off -- gating acquisition is not a migration, and must not rewrite history;
//   * no new quote may be created for it while off;
//   * a quote created while ON cannot be confirmed after the capability goes off -- and the refusal consumes
//     nothing, so the guest's auth context and quote are exactly as they were;
//   * with the capability ON the same immutable revision is acquired normally;
//   * VALIDITY_WINDOW acquisition is untouched in every configuration.

func p6Engine(t *testing.T, db *pgxpool.Pool, aggregate bool) *CommerceEngine {
	t.Helper()
	e, err := NewCommerceEngine(
		CommerceConfig{MasterEnabled: true, PortalEnabled: true},
		NewPgCommerceRepository(db), NopObserver{},
		WithQuoteTTL(5*time.Minute), WithAggregateOnlineTime(aggregate))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func aggregateSeed(t *testing.T, db *pgxpool.Pool) seed {
	t.Helper()
	quota := int64(3600)
	return seedFreeCommerce(t, db, func(o *seedOpts) {
		o.timeMode = TimeModeAggregateOnlineTime
		o.timeQuota = &quota
	})
}

// counts returns the durable state a refusal must leave completely alone.
func counts(t *testing.T, db *pgxpool.Pool, s seed) (quotes, purchases, entitlements int, ctxConsumed bool) {
	t.Helper()
	ctx := context.Background()
	if err := db.QueryRow(ctx, `SELECT
		 (SELECT count(*) FROM iam_v2.offer_quotes),
		 (SELECT count(*) FROM iam_v2.purchases),
		 (SELECT count(*) FROM iam_v2.entitlements),
		 (SELECT consumed_at IS NOT NULL FROM iam_v2.auth_contexts WHERE id=$1)`,
		s.authCtxID).Scan(&quotes, &purchases, &entitlements, &ctxConsumed); err != nil {
		t.Fatalf("state: %v", err)
	}
	return
}

// planMode reads the immutable revision back, to prove the gate changed nothing about it.
func planMode(t *testing.T, db *pgxpool.Pool, s seed) (string, *int64) {
	t.Helper()
	var mode string
	var quota *int64
	if err := db.QueryRow(context.Background(),
		`SELECT time_accounting_mode, time_quota_seconds FROM iam_v2.service_plan_revisions WHERE id=$1`,
		s.planRevID).Scan(&mode, &quota); err != nil {
		t.Fatalf("plan revision: %v", err)
	}
	return mode, quota
}

// GATE OFF: no quote, and nothing anywhere is touched.
func TestPhase6AggregateQuoteIsRefusedWhileTheCapabilityIsOff(t *testing.T) {
	db := p2DB(t)
	s := aggregateSeed(t, db)
	before, _ := planMode(t, db, s)

	q, err := p6Engine(t, db, false).CreateQuote(context.Background(), req(s))
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.QuoteID != "" {
		t.Fatal("a quote was created for an aggregate revision while the capability was OFF")
	}
	if q.Reason != AcquisitionReasonAggregateDisabled {
		t.Fatalf("the refusal reason is %q, not the shared one", q.Reason)
	}

	quotes, purchases, ents, consumed := counts(t, db, s)
	if quotes != 0 || purchases != 0 || ents != 0 {
		t.Fatalf("a refused quote left state behind: quotes=%d purchases=%d entitlements=%d",
			quotes, purchases, ents)
	}
	if consumed {
		t.Fatal("a refused quote consumed the guest's auth context")
	}

	// THE REVISION IS UNCHANGED. Gating acquisition is not a migration.
	after, quota := planMode(t, db, s)
	if after != before || after != TimeModeAggregateOnlineTime {
		t.Fatalf("the immutable revision now reads %q (was %q)", after, before)
	}
	if quota == nil || *quota != 3600 {
		t.Fatalf("the revision's budget was altered: %v", quota)
	}
}

// A quote minted while the capability was ON must not be confirmable after it goes off -- and the refusal
// must consume nothing, or a guest loses their auth context to a feature that was switched off underneath
// them.
func TestPhase6AggregateConfirmIsRefusedAfterTheCapabilityGoesOff(t *testing.T) {
	db := p2DB(t)
	s := aggregateSeed(t, db)
	ctx := context.Background()

	q, err := p6Engine(t, db, true).CreateQuote(ctx, req(s))
	if err != nil || q.QuoteID == "" {
		t.Fatalf("the quote could not be created with the capability ON: %+v (%v)", q, err)
	}

	pr, err := p6Engine(t, db, false).ConfirmFreePurchase(ctx, ConfirmRequest{
		TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if pr.EntitlementID != "" {
		t.Fatal("an aggregate entitlement was created while the capability was OFF")
	}
	if pr.Reason != AcquisitionReasonAggregateDisabled {
		t.Fatalf("the refusal reason is %q", pr.Reason)
	}

	// NOTHING WAS CONSUMED. The quote is still usable and the auth context is intact, so turning the
	// capability back on lets the same guest continue rather than start again.
	var quoteConsumed, ctxConsumed bool
	var purchases, ents int
	if err := db.QueryRow(ctx, `SELECT
		 (SELECT consumed_at IS NOT NULL FROM iam_v2.offer_quotes WHERE id=$1),
		 (SELECT consumed_at IS NOT NULL FROM iam_v2.auth_contexts WHERE id=$2),
		 (SELECT count(*) FROM iam_v2.purchases),
		 (SELECT count(*) FROM iam_v2.entitlements)`,
		q.QuoteID, s.authCtxID).Scan(&quoteConsumed, &ctxConsumed, &purchases, &ents); err != nil {
		t.Fatal(err)
	}
	if quoteConsumed {
		t.Fatal("the refused confirm consumed the quote")
	}
	if ctxConsumed {
		t.Fatal("the refused confirm consumed the auth context")
	}
	if purchases != 0 || ents != 0 {
		t.Fatalf("the refused confirm left purchases=%d entitlements=%d", purchases, ents)
	}
}

// GATE ON: the same immutable revision is acquired normally, and the entitlement carries its mode.
func TestPhase6AggregateIsAcquirableWhileTheCapabilityIsOn(t *testing.T) {
	db := p2DB(t)
	s := aggregateSeed(t, db)
	ctx := context.Background()
	e := p6Engine(t, db, true)

	q, err := e.CreateQuote(ctx, req(s))
	if err != nil || q.QuoteID == "" || q.Reason != "ok" {
		t.Fatalf("quote refused with the capability ON: %+v (%v)", q, err)
	}
	pr, err := e.ConfirmFreePurchase(ctx, ConfirmRequest{
		TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
	if err != nil || pr.EntitlementID == "" {
		t.Fatalf("confirm refused with the capability ON: %+v (%v)", pr, err)
	}

	var mode string
	if err := db.QueryRow(ctx,
		`SELECT time_accounting_mode FROM iam_v2.entitlements WHERE id=$1`, pr.EntitlementID).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != TimeModeAggregateOnlineTime {
		t.Fatalf("the entitlement was created in %q, not the revision's own mode", mode)
	}
}

// VALIDITY_WINDOW IS UNTOUCHED, whichever way the Phase-6 capability is set. This is the regression that
// matters most: the gate must be invisible to everything that existed before it.
func TestPhase6GateLeavesValidityWindowAcquisitionUnchanged(t *testing.T) {
	for _, capable := range []bool{false, true} {
		db := p2DB(t)
		s := seedFreeCommerce(t, db, nil) // the default fixture is VALIDITY_WINDOW
		ctx := context.Background()
		e := p6Engine(t, db, capable)

		q, err := e.CreateQuote(ctx, req(s))
		if err != nil || q.QuoteID == "" || q.Reason != "ok" {
			t.Fatalf("capability=%v: a validity-window quote was refused: %+v (%v)", capable, q, err)
		}
		pr, err := e.ConfirmFreePurchase(ctx, ConfirmRequest{
			TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
		if err != nil || pr.EntitlementID == "" {
			t.Fatalf("capability=%v: a validity-window confirm was refused: %+v (%v)", capable, pr, err)
		}
		var mode string
		if err := db.QueryRow(ctx,
			`SELECT time_accounting_mode FROM iam_v2.entitlements WHERE id=$1`, pr.EntitlementID).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != TimeModeValidityWindow {
			t.Fatalf("capability=%v: a validity-window package produced %q", capable, mode)
		}
	}
}
