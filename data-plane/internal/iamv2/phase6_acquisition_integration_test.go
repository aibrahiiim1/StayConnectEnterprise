package iamv2

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The free-commerce path shares no code with the PMS/stay grant, so it gets its own proof, through CreateQuote
// and ConfirmFreePurchase against a real database.

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

// counts returns the durable state a refusal must leave untouched.
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

	// Gating acquisition must not rewrite the immutable revision.
	after, quota := planMode(t, db, s)
	if after != before || after != TimeModeAggregateOnlineTime {
		t.Fatalf("the immutable revision now reads %q (was %q)", after, before)
	}
	if quota == nil || *quota != 3600 {
		t.Fatalf("the revision's budget was altered: %v", quota)
	}
}

// A quote minted while the capability was ON must not confirm after it goes off, and the refusal must consume
// nothing.
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

	// The quote and auth context stay usable, so re-enabling the capability lets the same guest continue.
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

// The gate must be invisible to VALIDITY_WINDOW acquisition whichever way the capability is set.
func TestPhase6GateLeavesValidityWindowAcquisitionUnchanged(t *testing.T) {
	for _, capable := range []bool{false, true} {
		db := p2DB(t)
		s := seedFreeCommerce(t, db, nil)
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
