//go:build integration

package payment

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// THE REFUND POLICY (Product Owner 2026-10-12, migration 0115) against the full production schema, through the
// real engine and the real outcome role. Runs under scripts/phase4-pg-integration.sh.

// capturedChain starts and captures one card purchase, returning its charge id and entitlement id.
func capturedChain(t *testing.T) (scope, *CheckoutEngine, *scriptedHosted, string, string) {
	t.Helper()
	s, e, prov, admin := cardFixture(t, ModeTest)
	ctx := context.Background()
	st, err := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c")
	if err != nil {
		t.Fatal(err)
	}
	prov.status = StatusResult{State: CheckoutCaptured, ProviderTxnRef: "pi_" + st.TransactionID[:8], AmountMinor: s.amount, Currency: "USD"}
	r, err := e.Reconcile(ctx, s.tenant, s.site, st.TransactionID)
	if err != nil || r.EntitlementID == "" {
		t.Fatalf("capture: %+v %v", r, err)
	}
	_ = admin
	return s, e, prov, st.TransactionID, r.EntitlementID
}

func entStatus(t *testing.T, ent string) (string, string) {
	t.Helper()
	var st, why string
	if err := pool(t).QueryRow(context.Background(), `SELECT status, COALESCE(terminal_reason,'') FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&st, &why); err != nil {
		t.Fatal(err)
	}
	return st, why
}

func TestCardRefundPolicy_AFullRefundEndsOnlyTheAccessItPaidFor(t *testing.T) {
	ctx := context.Background()
	s, e, prov, charge, ent := capturedChain(t)
	_, _, _, _, other := capturedChain(t) // an unrelated purchase that must never be touched
	admin := pool(t)
	sweep := func(total int64) {
		prov.status.Events = []ProviderEvent{{EventID: "refund:" + charge + ":" + strconv.FormatInt(total, 10),
			Kind: "REFUND", AmountMinor: total, ProviderRef: "pi_x", Cumulative: true}}
		e.SweepReversals(ctx, s.tenant, s.site, 24*time.Hour, 10)
	}

	// A PARTIAL refund changes nothing about access.
	sweep(300)
	if st, _ := entStatus(t, ent); st != "ACTIVE" {
		t.Fatalf("a partial refund must not end access: %s", st)
	}
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.refund_access_terminations WHERE entitlement_id=$1`, ent); n != 0 {
		t.Fatal("no evidence for a partial refund")
	}

	// The FULL refund ends exactly that entitlement, with evidence.
	sweep(s.amount)
	if st, why := entStatus(t, ent); st != "TERMINATED" || why != "REVOKED" {
		t.Fatalf("a full refund ends the access it paid for: %s/%s", st, why)
	}
	var captured, refunded int64
	if err := admin.QueryRow(ctx, `SELECT captured_minor, refunded_minor FROM iam_v2.refund_access_terminations WHERE entitlement_id=$1`, ent).
		Scan(&captured, &refunded); err != nil || captured != s.amount || refunded != s.amount {
		t.Fatalf("evidence: captured=%d refunded=%d %v", captured, refunded, err)
	}
	if st, _ := entStatus(t, other); st != "ACTIVE" {
		t.Fatalf("an unrelated entitlement must stay untouched: %s", st)
	}
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.sessions WHERE entitlement_id=$1 AND state IN ('active','PENDING_ENFORCEMENT')`, ent); n != 0 {
		t.Fatalf("its sessions end with it, %d left", n)
	}

	// Idempotent, and nothing is ever initiated towards the provider.
	sweep(s.amount)
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.refund_access_terminations WHERE entitlement_id=$1`, ent); n != 1 {
		t.Fatalf("one evidence row, got %d", n)
	}
	if prov.creates != 1 {
		t.Fatal("the refund policy never touches checkout creation")
	}
	// The evidence is append-only.
	if _, err := admin.Exec(ctx, `UPDATE iam_v2.refund_access_terminations SET refunded_minor=refunded_minor+1 WHERE entitlement_id=$1`, ent); err == nil {
		t.Fatal("refund termination evidence must be append-only")
	}
	if _, err := admin.Exec(ctx, `DELETE FROM iam_v2.refund_access_terminations WHERE entitlement_id=$1`, ent); err == nil {
		t.Fatal("refund termination evidence must never be deleted")
	}
}

func TestCardRefundPolicy_DisputesAndChargebacksAreReviewedNeverAutomatic(t *testing.T) {
	ctx := context.Background()
	s, e, prov, charge, ent := capturedChain(t)
	admin := pool(t)

	// An OPEN dispute: a case, no money, access unchanged.
	prov.status.Events = []ProviderEvent{{EventID: "dispute:dp_open", Kind: "DISPUTE", AmountMinor: s.amount, ProviderRef: "dp_open"}}
	e.SweepReversals(ctx, s.tenant, s.site, 24*time.Hour, 10)
	e.SweepReversals(ctx, s.tenant, s.site, 24*time.Hour, 10) // twice: one case
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.card_reversal_reviews WHERE charge_transaction_id=$1 AND kind='DISPUTE' AND resolution IS NULL`, charge); n != 1 {
		t.Fatalf("one open dispute case, got %d", n)
	}
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.payment_transactions WHERE parent_transaction_id=$1`, charge); n != 0 {
		t.Fatal("a dispute records no money")
	}
	// A LOST dispute: the chargeback is recorded (the settlement is reversed), a case is opened, access is NOT
	// ended automatically.
	prov.status.Events = []ProviderEvent{{EventID: "dp_lost", Kind: "CHARGEBACK", AmountMinor: s.amount, ProviderRef: "ch_x"}}
	e.SweepReversals(ctx, s.tenant, s.site, 24*time.Hour, 10)
	var ss string
	_ = admin.QueryRow(ctx, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement).Scan(&ss)
	if ss != "REVERSED" {
		t.Fatalf("the ledger records the chargeback: %s", ss)
	}
	if st, _ := entStatus(t, ent); st != "ACTIVE" {
		t.Fatalf("a chargeback must never end access by itself: %s", st)
	}
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.refund_access_terminations WHERE entitlement_id=$1`, ent); n != 0 {
		t.Fatal("a chargeback is not a refund termination")
	}
	var dispute, chargeback string
	_ = admin.QueryRow(ctx, `SELECT id::text FROM iam_v2.card_reversal_reviews WHERE charge_transaction_id=$1 AND kind='DISPUTE'`, charge).Scan(&dispute)
	_ = admin.QueryRow(ctx, `SELECT id::text FROM iam_v2.card_reversal_reviews WHERE charge_transaction_id=$1 AND kind='CHARGEBACK'`, charge).Scan(&chargeback)
	if dispute == "" || chargeback == "" {
		t.Fatalf("both cases open: dispute=%q chargeback=%q", dispute, chargeback)
	}

	// The outcome role can open cases but never resolve one.
	if _, err := e.Outcome.Exec(ctx, `SELECT iam_v2.p4_resolve_reversal_review($1,$2,$3,'END_ACCESS','x','y')`, s.tenant, s.site, chargeback); err == nil {
		t.Fatal("the payment outcome role must not resolve a review")
	}
	resolve := func(id, res, reason string) string {
		t.Helper()
		var out string
		if err := admin.QueryRow(ctx, `SELECT iam_v2.p4_resolve_reversal_review($1,$2,$3,$4,'payments:alice',$5)`,
			s.tenant, s.site, id, res, reason).Scan(&out); err != nil {
			t.Fatalf("resolve %s: %v", res, err)
		}
		return out
	}
	if _, err := admin.Exec(ctx, `SELECT iam_v2.p4_resolve_reversal_review($1,$2,$3,'KEEP_ACCESS','payments:alice','')`, s.tenant, s.site, dispute); err == nil {
		t.Fatal("a resolution needs a reason")
	}
	if got := resolve(dispute, "KEEP_ACCESS", "dispute won"); got != "KEEP_ACCESS" {
		t.Fatal(got)
	}
	if got := resolve(dispute, "END_ACCESS", "changed my mind"); got != "ALREADY_RESOLVED" {
		t.Fatalf("a resolution is final, got %s", got)
	}
	if st, _ := entStatus(t, ent); st != "ACTIVE" {
		t.Fatalf("KEEP_ACCESS keeps access: %s", st)
	}
	if got := resolve(chargeback, "END_ACCESS", "chargeback lost, ending access"); got != "END_ACCESS" {
		t.Fatal(got)
	}
	if st, why := entStatus(t, ent); st != "TERMINATED" || why != "REVOKED" {
		t.Fatalf("END_ACCESS ends the access the charge paid for: %s/%s", st, why)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM iam_v2.card_reversal_reviews WHERE id=$1`, chargeback); err == nil {
		t.Fatal("a review is never deleted")
	}
}
