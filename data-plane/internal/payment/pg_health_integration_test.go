//go:build integration

package payment

import (
	"context"
	"strings"
	"testing"
)

// OBSERVABILITY, and the redaction proof that makes it trustworthy.
//
// The redaction test is written the way the standing rule requires: it seeds recognisable secrets and guest
// data into the rows the health query reads, then asserts none of it -- and nothing identifier-shaped --
// appears anywhere in the rendered output. It was verified to FAIL against a build in which a single raw
// field (the provider reference) was added to FinancialHealth, so it is a check that can fail.

func TestIntegrationHealth_ReportsTheContractRequiredSignals(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeUnknown, ReasonCode: "timeout"}), &fakeGranter{})

	h, err := e.Health(ctx, s.tenant, s.site)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !h.PaymentAccountConfigured {
		t.Fatal("a configured site reports no payment account")
	}
	if h.SettlementsRequired != 1 {
		t.Fatalf("expected one REQUIRED settlement, got %d", h.SettlementsRequired)
	}
	if h.Status != "OK" {
		t.Fatalf("a healthy site reports %s (%v)", h.Status, h.Reasons)
	}

	// drive an UNKNOWN and watch the status become actionable
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	_, _ = e.Execute(ctx, s.tenant, s.site, in.ID)
	h, err = e.Health(ctx, s.tenant, s.site)
	if err != nil {
		t.Fatal(err)
	}
	if h.PaymentsUnknown != 1 || h.SettlementsManualReview != 1 {
		t.Fatalf("UNKNOWN is invisible in health: %+v", h)
	}
	if h.Status != "ATTENTION_REQUIRED" {
		t.Fatalf("an UNKNOWN outcome did not raise the status: %s", h.Status)
	}
	if !hasReason(h.Reasons, ReasonUnknownOutcomes) || !hasReason(h.Reasons, ReasonSettlementsWaiting) {
		t.Fatalf("the reasons are not actionable: %v", h.Reasons)
	}

	// recovery outranks everything
	actor := scan1[string](t, p, `SELECT gen_random_uuid()::text`)
	if _, err := e.DeclareRecovery(ctx, s.tenant, s.site, actor, "reconciling after a restore drill"); err != nil {
		t.Fatal(err)
	}
	h, _ = e.Health(ctx, s.tenant, s.site)
	if h.Status != "HELD" || !h.RecoveryActive {
		t.Fatalf("recovery did not dominate the status: %+v", h)
	}
	if h.RecoveryHoldsOpen == 0 {
		t.Fatal("recovery reports no open holds")
	}
}

func TestIntegrationHealth_NoSecretGuestOrIdentifierDataInAnyOutput(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)

	// Seed material that MUST NOT be able to escape. These are placed exactly where a careless
	// implementation would pick them up: on the account configuration and in the review evidence.
	const secret = "sk_live_SUPERSECRETKEY"
	const guest = "Mr Wolfgang Amadeus Guestname"
	if _, err := p.Exec(ctx, `UPDATE iam_v2.payment_provider_accounts
		SET display_name = $2, merchant_account_ref = $3 WHERE id = $1`,
		s.merchant, guest, "acct_"+secret); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))

	h, err := e.Health(ctx, s.tenant, s.site)
	if err != nil {
		t.Fatal(err)
	}
	js, err := h.JSON()
	if err != nil {
		t.Fatal(err)
	}
	prom := h.PrometheusText("stayconnect_financial")

	for name, out := range map[string]string{"json": string(js), "prometheus": prom} {
		for _, forbidden := range []string{secret, guest, in.ClientRef, in.ID, s.merchant, s.tenant, s.site,
			s.purchase, s.settlement, "acct_"} {
			if strings.Contains(out, forbidden) {
				t.Fatalf("%s output leaked %q", name, forbidden)
			}
		}
		if ContainsIdentifier(out) {
			t.Fatalf("%s output contains identifier-shaped data:\n%s", name, out)
		}
	}
	// and the reason vocabulary is closed: nothing outside the known set can appear
	for _, r := range h.Reasons {
		if !knownReasons[r] {
			t.Fatalf("an unknown reason code escaped: %q", r)
		}
	}
	h.addReason("SOMETHING_A_CALLER_MADE_UP")
	for _, r := range h.Reasons {
		if r == "SOMETHING_A_CALLER_MADE_UP" {
			t.Fatal("the reason vocabulary is not closed")
		}
	}
}

func hasReason(rs []string, want string) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}
