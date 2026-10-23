//go:build integration

package payment

import (
	"context"
	"strings"
	"testing"
)

// Provider-managed conversion on the real SQL pipeline: a USD package on a GBP account is refused until the account is
// marked provider-converts, and the provider's settlement report is recorded once, as stated.
func TestIntegration_ProviderManagedConversionKeepsPresentmentAndRecordsTheProvidersSettlement(t *testing.T) {
	admin := pool(t)
	s := seedCardChain(t, admin)
	ctx := context.Background()
	k := testKey(t)
	acct, err := SaveAccount(ctx, admin, k, s.tenant, s.site, "", "stripe", "acct_cv_"+runNonce+s.site[:8], "Stripe", "GBP", ModeTest,
		"ACTIVE", true, Credentials{"secret_key": "sk_test_x"}, "test-operator", "conversion proof")
	if err != nil {
		t.Fatalf("save account: %v", err)
	}
	prov := &scriptedHosted{status: StatusResult{State: CheckoutOpen}}
	e := &CheckoutEngine{Runtime: rolePool(t, "PHASE4_RUNTIME_DSN"), Outcome: rolePool(t, "PHASE4_OUTCOME_DSN"),
		Adapters: map[string]HostedCheckoutProvider{"stripe": prov}, Key: k, Health: NewProviderHealth()}

	if _, err := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://portal/return", "http://portal/cancel"); err == nil {
		t.Fatal("a USD charge on a GBP account must be refused without provider conversion")
	}
	if prov.creates != 0 {
		t.Fatal("nothing may reach the provider")
	}
	var on bool
	if err := admin.QueryRow(ctx, `SELECT iam_v2.payment_account_set_conversion($1,$2,$3::uuid,true,'test-operator','proof')`,
		s.tenant, s.site, acct).Scan(&on); err != nil || !on {
		t.Fatalf("set conversion: %v", err)
	}
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.payment_account_conversion_changes WHERE account_id=$1::uuid AND new_value`, acct); n != 1 {
		t.Fatal("the change must be logged")
	}
	st, err := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://portal/return", "http://portal/cancel")
	if err != nil || !strings.HasPrefix(st.RedirectURL, "https://") {
		t.Fatalf("start with conversion: %+v %v", st, err)
	}
	if prov.last.Currency != "USD" || prov.last.AmountMinor != s.amount {
		t.Fatalf("presentment must be the package currency and price: %+v", prov.last)
	}
	prov.status = StatusResult{State: CheckoutCaptured, ProviderTxnRef: "pi_cv", AmountMinor: s.amount, Currency: "usd",
		Settlement: &SettlementReport{Currency: "gbp", AmountMinor: 823, ExchangeRate: "0.783912", FeeMinor: 44, NetMinor: 779, BalanceRef: "txn_cv"}}
	for i := 0; i < 3; i++ {
		if _, err := e.Reconcile(ctx, s.tenant, s.site, st.TransactionID); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	var pc, sc, rate, ref, status string
	var pa, sa, fee, net int64
	var converted bool
	if err := admin.QueryRow(ctx, `SELECT presentment_currency, presentment_amount_minor, settlement_currency, settlement_amount_minor,
	       exchange_rate, fee_minor, net_minor, provider_balance_ref, converted, report_status
	  FROM iam_v2.payment_settlement_reports WHERE transaction_id=$1::uuid`, st.TransactionID).
		Scan(&pc, &pa, &sc, &sa, &rate, &fee, &net, &ref, &converted, &status); err != nil {
		t.Fatalf("settlement report: %v", err)
	}
	if pc != "USD" || pa != s.amount || sc != "GBP" || sa != 823 || rate != "0.783912" || fee != 44 || net != 779 || ref != "txn_cv" || !converted || status != "REPORTED" {
		t.Fatalf("report %s %d %s %d %s %d %d %s %v %s", pc, pa, sc, sa, rate, fee, net, ref, converted, status)
	}
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.payment_settlement_reports WHERE transaction_id=$1::uuid`, st.TransactionID); n != 1 {
		t.Fatalf("one report per charge, got %d", n)
	}
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.payment_transactions WHERE id=$1::uuid AND currency='USD' AND amount_minor=$2 AND status='CAPTURED'`, st.TransactionID, s.amount); n != 1 {
		t.Fatal("the charge keeps its USD presentment")
	}
	if _, err := admin.Exec(ctx, `UPDATE iam_v2.payment_settlement_reports SET exchange_rate='1' WHERE transaction_id=$1::uuid`, st.TransactionID); err == nil {
		t.Fatal("a settlement report is append-only")
	}
	pm, err := SaveAccount(ctx, admin, k, s.tenant, s.site, "", "paymob", "pm_cv_"+runNonce+s.site[:8], "Paymob", "EGP", ModeTest,
		"ACTIVE", false, Credentials{"secret_key": "x", "api_key": "y", "integration_id": "1", "region": "egypt"}, "test-operator", "conversion proof")
	if err != nil {
		t.Fatalf("save paymob: %v", err)
	}
	if _, err := admin.Exec(ctx, `SELECT iam_v2.payment_account_set_conversion($1,$2,$3::uuid,true,'test-operator','proof')`, s.tenant, s.site, pm); err == nil ||
		!strings.Contains(err.Error(), "PAYMENT_CONVERSION_UNSUPPORTED") {
		t.Fatalf("paymob must be refused: %v", err)
	}
}
