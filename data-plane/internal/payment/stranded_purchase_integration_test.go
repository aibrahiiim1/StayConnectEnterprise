//go:build integration

package payment

// A purchase AWAITING_SETTLEMENT whose intent was refused must not wait forever, and editing an account's merchant or
// credentials while a checkout is open must not make the reconciler ask with the new identity and mark a paid checkout
// FAILED.

import (
	"context"
	"strings"
	"testing"
)

func consumeQuoteAgo(t *testing.T, s scope, ago string) {
	t.Helper()
	admin := pool(t)
	guarded(t, admin, "commerce_intent", `UPDATE iam_v2.offer_quotes q SET consumed_at = now() - $2::interval
		  FROM iam_v2.purchases pu WHERE pu.id = $1 AND q.id = pu.offer_quote_id RETURNING q.id::text`, s.purchase, ago)
}

func purchaseAndSettlement(t *testing.T, s scope) (string, string) {
	t.Helper()
	admin := pool(t)
	var pst, sst string
	if err := admin.QueryRow(context.Background(), `SELECT pu.state, se.status FROM iam_v2.purchases pu
		JOIN iam_v2.settlements se ON se.purchase_id = pu.id WHERE pu.id = $1`, s.purchase).Scan(&pst, &sst); err != nil {
		t.Fatal(err)
	}
	return pst, sst
}

func TestUnstartedCardPurchaseFailsOnlyAfterExpiryAndGraceAndOnlyWithoutATransaction(t *testing.T) {
	ctx := context.Background()
	withTxn, e, _, admin := cardFixture(t, ModeTest)
	if _, err := e.StartCheckout(ctx, withTxn.tenant, withTxn.site, withTxn.settlement, "WiFi", "http://p/r", "http://p/c"); err != nil {
		t.Fatalf("start: %v", err)
	}
	consumeQuoteAgo(t, withTxn, "5 hours")

	stale := seedCardChain(t, admin)
	consumeQuoteAgo(t, stale, "3 hours") // older than 30 min expiry + 60 min grace
	fresh := seedCardChain(t, admin)
	consumeQuoteAgo(t, fresh, "20 minutes") // the client may still be paying

	for _, s := range []scope{stale, fresh} {
		n, err := e.FailUnstartedPurchases(ctx, s.tenant, s.site)
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		_ = n
	}
	if n, err := e.FailUnstartedPurchases(ctx, withTxn.tenant, withTxn.site); err != nil || n != 0 {
		t.Fatalf("a purchase WITH a payment transaction belongs to the reconciler: n=%d err=%v", n, err)
	}

	if p, se := purchaseAndSettlement(t, stale); p != "FAILED" || se != "FAILED" {
		t.Fatalf("the stranded purchase was not failed: purchase=%s settlement=%s", p, se)
	}
	if p, se := purchaseAndSettlement(t, fresh); p != "AWAITING_SETTLEMENT" || se != "REQUIRED" {
		t.Fatalf("a purchase younger than expiry+grace was touched: purchase=%s settlement=%s", p, se)
	}
	if p, se := purchaseAndSettlement(t, withTxn); p != "AWAITING_SETTLEMENT" || se == "FAILED" {
		t.Fatalf("a purchase with a transaction was touched: purchase=%s settlement=%s", p, se)
	}
	if n := count1(t, admin, `SELECT count(*)::int FROM iam_v2.card_purchase_sweeps WHERE purchase_id=$1
		AND reason='NO_PAYMENT_TRANSACTION' AND waited_seconds > 3600`, stale.purchase); n != 1 {
		t.Fatalf("the sweep was not recorded once (%d)", n)
	}
	if n, err := e.FailUnstartedPurchases(ctx, stale.tenant, stale.site); err != nil || n != 0 {
		t.Fatalf("a second sweep changed something: n=%d err=%v", n, err)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM iam_v2.card_purchase_sweeps WHERE purchase_id=$1`, stale.purchase); err == nil {
		t.Fatal("a sweep record could be deleted")
	}
	// The floor: no caller can sweep a purchase the client may still be paying for.
	var dummy int
	err := e.Outcome.QueryRow(ctx, `SELECT iam_v2.p4_fail_unstarted_card_purchases($1,$2,60)`, fresh.tenant, fresh.site).Scan(&dummy)
	if err == nil || !strings.Contains(err.Error(), "CARD_SWEEP_AGE") {
		t.Fatalf("a 60 s sweep must be refused, got %v", err)
	}
	// Least privilege: the execution credential cannot fail purchases.
	if err := e.Runtime.QueryRow(ctx, `SELECT iam_v2.p4_fail_unstarted_card_purchases($1,$2,3600)`, fresh.tenant, fresh.site).Scan(&dummy); err == nil {
		t.Fatal("the runtime credential must not hold the sweep")
	}
}

func TestAccountIdentityIsPinnedWhileACheckoutIsOpen(t *testing.T) {
	ctx := context.Background()
	s, e, prov, admin := cardFixture(t, ModeTest)
	st, err := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	merchant := scan1[string](t, admin, `SELECT merchant_account_ref FROM iam_v2.payment_provider_accounts WHERE id=$1`, s.merchant)
	save := func(merchantRef, display string, creds Credentials) error {
		_, err := SaveAccount(ctx, admin, e.Key, s.tenant, s.site, s.merchant, "stripe", merchantRef, display, "USD", ModeTest,
			"ACTIVE", true, creds, "test-operator", "edit")
		return err
	}
	if err := save(merchant+"_new", "Stripe", nil); err == nil || !strings.Contains(err.Error(), "PAYMENT_ACCOUNT_CHECKOUTS_OPEN") {
		t.Fatalf("a merchant change with an open checkout must be refused, got %v", err)
	}
	if got := scan1[string](t, admin, `SELECT merchant_account_ref FROM iam_v2.payment_provider_accounts WHERE id=$1`, s.merchant); got != merchant {
		t.Fatalf("the refused change was written: %s", got)
	}
	if err := save(merchant, "Stripe", Credentials{"secret_key": "sk_test_rotated"}); err == nil ||
		!strings.Contains(err.Error(), "PAYMENT_ACCOUNT_CHECKOUTS_OPEN") {
		t.Fatalf("a credential rotation with an open checkout must be refused, got %v", err)
	}
	if err := save(merchant, "Stripe renamed", nil); err != nil {
		t.Fatalf("a rename must be allowed with an open checkout: %v", err)
	}
	prov.status = StatusResult{State: CheckoutDeclined}
	if r, _ := e.Reconcile(ctx, s.tenant, s.site, st.TransactionID); r.TransactionStatus != "FAILED" {
		t.Fatalf("the checkout did not finish: %+v", r)
	}
	if err := save(merchant+"_new", "Stripe", Credentials{"secret_key": "sk_test_rotated"}); err != nil {
		t.Fatalf("with no open checkout the account may change: %v", err)
	}
}
