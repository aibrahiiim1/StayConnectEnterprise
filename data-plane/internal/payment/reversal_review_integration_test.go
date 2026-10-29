//go:build integration

package payment

import (
	"context"
	"strconv"
	"sync"
	"testing"
)

// Audit AD-42 (P6 M3): a reversal the ledger refuses opens a review case, and the running-total subtraction is done by
// the database under the parent-row lock. Provider doubles only.

func capturedCharge(t *testing.T, ref string) (scope, *CheckoutEngine, *scriptedHosted, string, txnRow) {
	t.Helper()
	s, e, prov, _ := cardFixture(t, ModeTest)
	ctx := context.Background()
	st, err := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c")
	if err != nil {
		t.Fatal(err)
	}
	prov.status = StatusResult{State: CheckoutCaptured, ProviderTxnRef: ref, AmountMinor: s.amount, Currency: "USD"}
	if _, err := e.Reconcile(ctx, s.tenant, s.site, st.TransactionID); err != nil {
		t.Fatal(err)
	}
	txn, err := e.loadTxn(ctx, s.tenant, s.site, st.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	return s, e, prov, st.TransactionID, txn
}

func TestProviderReversalRefusedByTheLedgerOpensAReviewCase(t *testing.T) {
	s, e, _, charge, txn := capturedCharge(t, "pi_ad42a")
	admin := pool(t)
	ctx := context.Background()
	// A 400 refund, then the provider reports a lost dispute for the full 1000 on a charge of 1000.
	e.recordReversals(ctx, txn, []ProviderEvent{{EventID: "rf-ad42a", Kind: "REFUND", AmountMinor: 400, ProviderRef: "pi_ad42a"}})
	e.recordReversals(ctx, txn, []ProviderEvent{{EventID: "cb-ad42a", Kind: "CHARGEBACK", AmountMinor: s.amount, ProviderRef: "dp_ad42a"}})

	if got := count1(t, admin, `SELECT COALESCE(sum(amount_minor),0)::int FROM iam_v2.payment_transactions
		WHERE parent_transaction_id=$1 AND status='CAPTURED'`, charge); got != 400 {
		t.Fatalf("the refused chargeback must not be recorded as money; recorded %d, want 400", got)
	}
	if n := count1(t, admin, `SELECT count(*)::int FROM iam_v2.card_reversal_reviews
		WHERE charge_transaction_id=$1 AND kind='REVERSAL_REFUSED' AND resolution IS NULL AND reversal_kind='CHARGEBACK'
		  AND reversal_amount_minor=$2 AND detail LIKE '%refused%'`, charge, s.amount); n != 1 {
		t.Fatalf("one open REVERSAL_REFUSED case expected, got %d", n)
	}
	// The 6-hourly sweep repeats the same refusal: still exactly one case.
	e.recordReversals(ctx, txn, []ProviderEvent{{EventID: "cb-ad42a", Kind: "CHARGEBACK", AmountMinor: s.amount, ProviderRef: "dp_ad42a"}})
	if n := count1(t, admin, `SELECT count(*)::int FROM iam_v2.card_reversal_reviews WHERE charge_transaction_id=$1 AND kind='REVERSAL_REFUSED'`, charge); n != 1 {
		t.Fatalf("a repeated refusal must not open a second case, got %d", n)
	}
	// The case resolves like a dispute, once.
	var id string
	if err := admin.QueryRow(ctx, `SELECT id::text FROM iam_v2.card_reversal_reviews WHERE charge_transaction_id=$1`, charge).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var res string
	if err := admin.QueryRow(ctx, `SELECT iam_v2.p4_resolve_reversal_review($1,$2,$3,'KEEP_ACCESS','finance','chargeback entered by hand')`,
		s.tenant, s.site, id).Scan(&res); err != nil || res != "KEEP_ACCESS" {
		t.Fatalf("resolve: %q %v", res, err)
	}
	if _, err := admin.Exec(ctx, `UPDATE iam_v2.card_reversal_reviews SET detail='edited' WHERE id=$1`, id); err == nil {
		t.Fatal("a review's facts are final")
	}
}

func TestProviderReversalRunningTotalIsSubtractedUnderTheParentLock(t *testing.T) {
	s, e, _, charge, _ := capturedCharge(t, "pi_ad42b")
	admin := pool(t)
	ctx := context.Background()
	_ = s
	// Two passes report running totals 100 and 150 at the same time; the ledger ends at 150, never 250.
	var wg sync.WaitGroup
	for _, total := range []int64{100, 150} {
		wg.Add(1)
		go func(total int64) {
			defer wg.Done()
			var r string
			if err := e.Outcome.QueryRow(ctx, `SELECT iam_v2.p4_record_provider_reversal_v2($1,$2,$3,'REFUND',$4,true,$5,'pi_ad42b')`,
				s.tenant, s.site, charge, total, "run-"+strconv.FormatInt(total, 10)).Scan(&r); err != nil {
				t.Errorf("total %d: %v", total, err)
			}
		}(total)
	}
	wg.Wait()
	if got := count1(t, admin, `SELECT COALESCE(sum(amount_minor),0)::int FROM iam_v2.payment_transactions
		WHERE parent_transaction_id=$1 AND transaction_type='REFUND' AND status='CAPTURED'`, charge); got != 150 {
		t.Fatalf("running totals 100 and 150 end at 150 recorded, got %d", got)
	}
	// A lower or equal total adds nothing.
	var r string
	if err := e.Outcome.QueryRow(ctx, `SELECT iam_v2.p4_record_provider_reversal_v2($1,$2,$3,'REFUND',120,true,'run-120','pi_ad42b')`,
		s.tenant, s.site, charge).Scan(&r); err != nil || r != "NOTHING_NEW" {
		t.Fatalf("a lower running total records nothing: %q %v", r, err)
	}
}
