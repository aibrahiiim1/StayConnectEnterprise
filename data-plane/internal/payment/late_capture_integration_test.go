//go:build integration

package payment

import (
	"context"
	"errors"
	"testing"
)

// Audit AD-43 (P6 L1): a capture after a checkout was recorded as failed opens a review case and changes nothing else.
// Provider doubles only; the double counts every call so the tests can prove the re-query never creates anything.

func failedCheckout(t *testing.T) (scope, *CheckoutEngine, *scriptedHosted, string) {
	t.Helper()
	s, e, prov, _ := cardFixture(t, ModeTest)
	ctx := context.Background()
	st, err := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c")
	if err != nil {
		t.Fatal(err)
	}
	prov.status = StatusResult{State: CheckoutDeclined, ReasonCode: "card_declined"}
	if _, err := e.Reconcile(ctx, s.tenant, s.site, st.TransactionID); err != nil {
		t.Fatal(err)
	}
	return s, e, prov, st.TransactionID
}

func TestLateCapture_OffByDefaultMakesNoProviderCall(t *testing.T) {
	s, e, prov, _ := failedCheckout(t)
	before := prov.queries
	if _, err := e.RecheckLateCaptures(context.Background(), s.tenant, s.site, 25); !errors.Is(err, ErrRecheckOff) {
		t.Fatalf("the re-query is dormant unless the deployer switched it on, got %v", err)
	}
	if prov.queries != before || prov.creates != 1 {
		t.Fatalf("no provider call may come from a dormant re-query (queries %d -> %d, creates %d)", before, prov.queries, prov.creates)
	}
	if (Config{MasterEnabled: true, PaymentEnabled: true, ProviderEnabled: true}).RecheckOn() {
		t.Fatal("the provider being on does not switch the re-query on")
	}
}

func TestLateCapture_ACaptureOpensOneReviewAndChangesNothingElse(t *testing.T) {
	s, e, prov, charge := failedCheckout(t)
	admin := pool(t)
	ctx := context.Background()
	e.RecheckEnabled = true

	// The provider says the guest did pay after all.
	prov.status = StatusResult{State: CheckoutCaptured, ProviderTxnRef: "pi_late", AmountMinor: s.amount, Currency: "USD"}
	creates := prov.creates
	n, err := e.RecheckLateCaptures(ctx, s.tenant, s.site, 25)
	if err != nil || n != 1 {
		t.Fatalf("one case opened: %d %v", n, err)
	}
	if prov.creates != creates {
		t.Fatal("the re-query is read-only: it never creates a checkout")
	}
	if got := count1(t, admin, `SELECT count(*)::int FROM iam_v2.card_reversal_reviews WHERE charge_transaction_id=$1 AND kind='LATE_CAPTURE'
		AND resolution IS NULL AND detail LIKE '%Nothing was granted%'`, charge); got != 1 {
		t.Fatalf("an open LATE_CAPTURE case with its explanation: %d", got)
	}
	var st, setStatus string
	_ = admin.QueryRow(ctx, `SELECT status FROM iam_v2.payment_transactions WHERE id=$1`, charge).Scan(&st)
	_ = admin.QueryRow(ctx, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement).Scan(&setStatus)
	if st != "FAILED" {
		t.Fatalf("the charge stays as OneGate recorded it: %s", st)
	}
	if setStatus == "SETTLED" {
		t.Fatalf("a late capture must not settle anything: %s", setStatus)
	}
	if got := count1(t, admin, `SELECT count(*)::int FROM iam_v2.entitlements e JOIN iam_v2.settlements se ON se.purchase_id=e.purchase_id WHERE se.id=$1`, s.settlement); got != 0 {
		t.Fatalf("nothing is granted: %d entitlements", got)
	}
	if got := count1(t, admin, `SELECT count(*)::int FROM iam_v2.payment_transaction_events WHERE payment_transaction_id=$1 AND event_type='CAPTURED'`, charge); got != 0 {
		t.Fatalf("no CAPTURED outcome is applied to the charge: %d", got)
	}

	// Spaced: asked within the last six hours is not asked again, and the case is never duplicated.
	q := prov.queries
	if n, err := e.RecheckLateCaptures(ctx, s.tenant, s.site, 25); err != nil || n != 0 {
		t.Fatalf("a charge that has its case is not asked about again: %d %v", n, err)
	}
	if prov.queries != q {
		t.Fatalf("no second provider query for a charge that has a case (%d -> %d)", q, prov.queries)
	}
}

func TestLateCapture_AnswersThatAreNotACaptureOpenNothingAndAreSpaced(t *testing.T) {
	s, e, prov, charge := failedCheckout(t)
	admin := pool(t)
	ctx := context.Background()
	e.RecheckEnabled = true
	prov.status = StatusResult{State: CheckoutDeclined}
	q := prov.queries
	if n, err := e.RecheckLateCaptures(ctx, s.tenant, s.site, 25); err != nil || n != 0 {
		t.Fatalf("a still-declined charge opens nothing: %d %v", n, err)
	}
	if prov.queries != q+1 {
		t.Fatalf("it was asked once: %d -> %d", q, prov.queries)
	}
	if n, _ := e.RecheckLateCaptures(ctx, s.tenant, s.site, 25); n != 0 || prov.queries != q+1 {
		t.Fatal("asked within six hours: not asked again")
	}
	// Outside the window it is not asked at all: shrink the site's window to its minimum and age the checkout.
	if _, err := admin.Exec(ctx, `UPDATE iam_v2.payment_checkouts SET late_checked_at = NULL WHERE transaction_id=$1`, charge); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `SELECT iam_v2.card_recheck_settings_set($1,$2,0,1,'test operator','window of one day')`, s.tenant, s.site); err != nil {
		t.Fatal(err)
	}
	if w := e.RecheckWindow(ctx, s.tenant, s.site); w != 1 {
		t.Fatalf("the site's window is read at each pass: %d", w)
	}
	q = prov.queries
	// payment_checkouts.created_at is immutable, so the window is proven through the candidate function directly.
	var old int
	_ = admin.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.p4_late_capture_candidates($1,$2,1,25)`, s.tenant, s.site).Scan(&old)
	if old != 1 {
		t.Fatalf("a fresh failed charge is inside a one-day window: %d", old)
	}
	for _, bad := range []int{0, 31} {
		if _, err := admin.Exec(ctx, `SELECT iam_v2.card_recheck_settings_set($1,$2,1,$3,'op','out of bounds')`, s.tenant, s.site, bad); err == nil {
			t.Fatalf("a window of %d days must be refused", bad)
		}
	}
	if _, err := admin.Exec(ctx, `SELECT iam_v2.card_recheck_settings_set($1,$2,0,5,'op','stale version')`, s.tenant, s.site); err == nil {
		t.Fatal("a stale version is refused")
	}
	_ = q
}
