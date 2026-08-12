//go:build integration

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Real HTTP + real PostgreSQL contract tests for the Phase-4 Financial Manual Review surface.
//
// The database already refuses a bad DECISION — the action catalog, the action/state matrix, reviewer
// concurrency and single-use retry authorization are all enforced by iam_v2.record_posting_review_action,
// and iam_v2_scratch/phase4_0011_financial.sh proves that. None of it is AUTHORIZATION. What these tests
// prove is the half that only exists at the API: that the decision came from an authenticated operator who
// holds financial-review write, who has just re-entered their password, and whose identity was taken from
// the session rather than from anything the request could say.
//
// They need a disposable database carrying 0011+0012+0013 (scripts/phase4-pg-integration.sh builds one).

// seedReviewablePosting builds a posting in the fixture's own tenant, transmitted once and left UNKNOWN —
// the state an operator is actually asked to decide about.
func (f *apiFixture) seedReviewablePosting(t *testing.T, outcome, asStatus string) (postingID, ifaceID string) {
	t.Helper()
	ctx := context.Background()
	uniq := time.Now().UnixNano()

	var revID, planRev, pkgRev, mapID, stayID, folioID, purchaseID, settleID string
	q := func(dst *string, sql string, args ...any) {
		t.Helper()
		if err := f.pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed (%s): %v", sql[:40], err)
		}
	}
	q(&ifaceID, `INSERT INTO iam_v2.pms_interfaces(tenant_id,site_id,connector_kind)
		VALUES ($1,$2,'protel-fias') RETURNING id::text`, f.tenant, f.site)
	q(&revID, `INSERT INTO iam_v2.pms_interface_revisions
		(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config,
		 financial_base_currency,financial_base_currency_exponent)
		VALUES ($1,$2,$3,1,'UTC','GLOBALLY_UNIQUE',
		 '{"heartbeat_timeout_ms":60000,"feed_freshness_ms":300000,"complete_sync_ms":3600000}','USD',2)
		RETURNING id::text`, f.tenant, f.site, ifaceID)
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2 WHERE id=$1`,
		ifaceID, revID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id,site_id,pms_interface_id,pinned_revision_id,credential_mode,runtime_generation,
		 transport_status,last_connected_at,last_heartbeat_at,continuity_status,last_valid_event_at,
		 sync_status,last_complete_sync_at,resync_generation_seq,published_resync_generation)
		VALUES ($1,$2,$3,$4,'NONE',1,'CONNECTED',now(),now(),'CONTINUOUS',now(),'IN_SYNC',now(),0,0)`,
		f.tenant, f.site, ifaceID, revID); err != nil {
		t.Fatal(err)
	}

	var planID, pkgID string
	q(&planID, `INSERT INTO iam_v2.service_plans(tenant_id,site_id,code) VALUES ($1,$2,$3)
		RETURNING id::text`, f.tenant, f.site, fmt.Sprintf("P%d", uniq))
	q(&planRev, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id,site_id,service_plan_id,revision_no,name,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'plan',2,'VALIDITY_WINDOW',1000000) RETURNING id::text`, f.tenant, f.site, planID)
	q(&pkgID, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code) VALUES ($1,$2,$3)
		RETURNING id::text`, f.tenant, f.site, fmt.Sprintf("K%d", uniq))
	q(&pkgRev, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent)
		VALUES ($1,$2,$3,1,$4,'GENERAL',1000,'USD',2) RETURNING id::text`, f.tenant, f.site, pkgID, planRev)
	q(&mapID, `INSERT INTO iam_v2.package_settlement_mappings
		(tenant_id,site_id,package_revision_id,pms_interface_id,mapping_revision,posting_code)
		VALUES ($1,$2,$3,$4,1,'WIFI') RETURNING id::text`, f.tenant, f.site, pkgRev, ifaceID)
	q(&stayID, `INSERT INTO iam_v2.stays
		(tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
		 normalized_room_number,status,posting_allowed)
		VALUES ($1,$2,$3,$4,'S1','1421','IN_HOUSE',true) RETURNING id::text`,
		f.tenant, f.site, ifaceID, fmt.Sprintf("R%d", uniq))
	q(&folioID, `INSERT INTO iam_v2.folios(tenant_id,site_id,pms_interface_id,external_folio_id)
		VALUES ($1,$2,$3,'5') RETURNING id::text`, f.tenant, f.site, ifaceID)
	q(&purchaseID, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,settlement_mapping_id,trigger,
		 amount_minor,currency,currency_exponent,state)
		VALUES ($1,$2,$3,$4,$5,$6,'VOUCHER_REDEMPTION',1000,'USD',2,'GRANTED') RETURNING id::text`,
		f.tenant, f.site, pkgRev, ifaceID, stayID, mapID)
	q(&settleID, `INSERT INTO iam_v2.settlements(tenant_id,site_id,purchase_id,method,status)
		VALUES ($1,$2,$3,'PMS_POSTING','REQUIRED') RETURNING id::text`, f.tenant, f.site, purchaseID)
	q(&postingID, `INSERT INTO iam_v2.pms_postings
		(tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,
		 posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'CHARGE',1000,'USD',2,$9) RETURNING id::text`,
		f.tenant, f.site, ifaceID, settleID, purchaseID, stayID, folioID, revID, fmt.Sprintf("idem-%d", uniq))
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.posting_attempts
		(tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,sent_at,
		 outcome,pa_as_status,response_at)
		VALUES ($1,$2,$3,$4,1,$5,'1421','5',now(),$6,$7,now())`,
		f.tenant, f.site, postingID, ifaceID, fmt.Sprintf("%d", uniq%100000), outcome,
		nullIfEmpty(asStatus)); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	return postingID, ifaceID
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func reviewBody(action, reason string, extra map[string]any) map[string]any {
	b := map[string]any{
		"action": action, "reason": reason,
		"evidence": map[string]any{
			"source_type": "PMS_FOLIO_INSPECTION",
			"reference":   "folio-1421-line-7",
			"note":        "checked the folio at the desk",
		},
		"password": "operator-step-up-pw",
	}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// ---------------------------------------------------------------- authorization

func TestIntegrationReviewAPI_RequiresFinancialReviewWrite(t *testing.T) {
	// voucher_operator has no financial-review entry at all
	f := newAPI(t, "voucher_operator")
	id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")
	if code, _ := f.do(t, "GET", "/financial-review/queue", nil); code != 403 {
		t.Fatalf("a role without financial-review must not read the queue, got %d", code)
	}
	if code, _ := f.do(t, "POST", "/financial-review/postings/"+id+"/actions",
		reviewBody("CONFIRM_POSTED", "no permission", nil)); code != 403 {
		t.Fatalf("a role without financial-review must not decide, got %d", code)
	}
}

func TestIntegrationReviewAPI_ReadOnlyRoleCannotDecide(t *testing.T) {
	// hotel_it_manager sees the queue as integration evidence but must not decide it
	f := newAPI(t, "hotel_it_manager")
	id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")
	if code, _ := f.do(t, "GET", "/financial-review/queue", nil); code != 200 {
		t.Fatalf("a read role must be able to see the queue, got %d", code)
	}
	if code, _ := f.do(t, "POST", "/financial-review/postings/"+id+"/actions",
		reviewBody("CONFIRM_POSTED", "read-only role", nil)); code != 403 {
		t.Fatalf("a read-only role must not decide, got %d", code)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*)::int FROM iam_v2.posting_review_actions WHERE posting_id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a refused decision must write nothing, found %d review rows", n)
	}
}

func TestIntegrationReviewAPI_PaymentsOperatorCanDecide(t *testing.T) {
	f := newAPI(t, "payments_operator")
	id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")
	code, body := f.do(t, "POST", "/financial-review/postings/"+id+"/actions",
		reviewBody("CONFIRM_POSTED", "folio inspected at the desk", nil))
	if code != 200 {
		t.Fatalf("payments_operator holds financial-review write per section 15, got %d: %v", code, body)
	}
}

// ---------------------------------------------------------------- identity

// The decisive property: the recorded actor is the SESSION operator, and there is no way to say otherwise.
//
// Two things are proved, because they are separate guarantees. First, the request schema has no actor field
// and decoding is strict, so a body that tries to name someone is rejected outright rather than having its
// extra field quietly ignored. Second — and this is the one that matters for the audit ledger — a perfectly
// ordinary valid decision records the AUTHENTICATED operator.
func TestIntegrationReviewAPI_ActorComesFromTheSessionNeverTheRequest(t *testing.T) {
	f := newAPI(t, "payments_operator")
	forged := "99999999-9999-9999-9999-999999999999"

	for _, field := range []string{"actor", "actor_id", "operator_id"} {
		id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")
		code, _ := f.do(t, "POST", "/financial-review/postings/"+id+"/actions",
			reviewBody("CONFIRM_POSTED", "attempting to name another actor", map[string]any{field: forged}))
		if code != 400 {
			t.Fatalf("a body carrying %q must be rejected outright, got %d", field, code)
		}
		var n int
		if err := f.pool.QueryRow(context.Background(),
			`SELECT count(*)::int FROM iam_v2.posting_review_actions WHERE posting_id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("a rejected spoof attempt wrote %d ledger rows", n)
		}
	}

	// and the ordinary path attributes the decision to the session
	id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")
	code, body := f.do(t, "POST", "/financial-review/postings/"+id+"/actions",
		reviewBody("CONFIRM_POSTED", "folio inspected at the desk", nil))
	if code != 200 {
		t.Fatalf("the decision itself is valid; got %d: %v", code, body)
	}
	if got, _ := body["actor"].(string); got != f.operator {
		t.Fatalf("the response must attribute the decision to the session operator, got %q", got)
	}
	var actor string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT actor::text FROM iam_v2.posting_review_actions WHERE posting_id=$1`, id).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != f.operator {
		t.Fatalf("the immutable ledger must record the AUTHENTICATED operator, got %s", actor)
	}
	if actor == forged {
		t.Fatal("a forged actor reached the financial audit ledger")
	}
}

// ---------------------------------------------------------------- step-up

func TestIntegrationReviewAPI_RequiresPasswordStepUp(t *testing.T) {
	f := newAPI(t, "payments_operator")
	id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")
	for _, tc := range []struct{ name, pw string }{
		{"no password", ""},
		{"wrong password", "not-the-operator-password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := reviewBody("CONFIRM_POSTED", "step-up missing", nil)
			b["password"] = tc.pw
			code, _ := f.do(t, "POST", "/financial-review/postings/"+id+"/actions", b)
			if code != 401 {
				t.Fatalf("section 15 requires password re-authentication, got %d", code)
			}
		})
	}
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*)::int FROM iam_v2.posting_review_actions WHERE posting_id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a decision refused at step-up must write nothing, found %d rows", n)
	}
}

// ---------------------------------------------------------------- catalog, reason, evidence

func TestIntegrationReviewAPI_CatalogReasonAndEvidence(t *testing.T) {
	f := newAPI(t, "payments_operator")
	id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")

	if code, _ := f.do(t, "POST", "/financial-review/postings/"+id+"/actions",
		reviewBody("APPROVE", "generic approve", nil)); code != 400 {
		t.Fatalf("there is no generic approve action, got %d", code)
	}
	b := reviewBody("CONFIRM_POSTED", "   ", nil)
	if code, _ := f.do(t, "POST", "/financial-review/postings/"+id+"/actions", b); code != 400 {
		t.Fatalf("a reason is mandatory, got %d", code)
	}
	b = reviewBody("CONFIRM_POSTED", "no evidence supplied", nil)
	b["evidence"] = map[string]any{}
	if code, _ := f.do(t, "POST", "/financial-review/postings/"+id+"/actions", b); code != 400 {
		t.Fatalf("a terminal decision must record evidence, got %d", code)
	}
	// ESCALATE decides nothing, so it does not require evidence
	b = reviewBody("ESCALATE", "second opinion please", nil)
	b["evidence"] = map[string]any{}
	if code, body := f.do(t, "POST", "/financial-review/postings/"+id+"/actions", b); code != 200 {
		t.Fatalf("ESCALATE must not require evidence, got %d: %v", code, body)
	}

	code, body := f.do(t, "GET", "/financial-review/actions", nil)
	if code != 200 {
		t.Fatalf("catalog: %d", code)
	}
	items, _ := body["actions"].([]any)
	if len(items) != 5 {
		t.Fatalf("the catalog is exactly the five section-15 actions, got %d", len(items))
	}
}

// ---------------------------------------------------------------- the action/state matrix

func TestIntegrationReviewAPI_AckedOKChargeCannotBeRetried(t *testing.T) {
	f := newAPI(t, "payments_operator")
	id, _ := f.seedReviewablePosting(t, "ACKED", "OK")
	code, body := f.do(t, "POST", "/financial-review/postings/"+id+"/actions",
		reviewBody("CONFIRM_NOT_POSTED_RETRY", "operator believes it failed", nil))
	if code != 422 {
		t.Fatalf("retrying an ACKed-OK charge would post it twice; expected 422, got %d: %v", code, body)
	}
	// and the detail view must not have offered it in the first place
	_, detail := f.do(t, "GET", "/financial-review/postings/"+id, nil)
	for _, a := range detail["available_actions"].([]any) {
		if a == "CONFIRM_NOT_POSTED_RETRY" {
			t.Fatal("the UI must not offer a retry for a charge the PMS acknowledged")
		}
	}
}

func TestIntegrationReviewAPI_StaleVersionIsAConflict(t *testing.T) {
	f := newAPI(t, "payments_operator")
	id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")
	stale := 7
	code, _ := f.do(t, "POST", "/financial-review/postings/"+id+"/actions",
		reviewBody("CONFIRM_POSTED", "acting on a stale screen", map[string]any{"expected_version": stale}))
	if code != 409 {
		t.Fatalf("a decision against a stale version must be a conflict, got %d", code)
	}
}

func TestIntegrationReviewAPI_SecondIncompatibleDecisionIsRefused(t *testing.T) {
	f := newAPI(t, "payments_operator")
	id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")
	if code, _ := f.do(t, "POST", "/financial-review/postings/"+id+"/actions",
		reviewBody("CONFIRM_NOT_POSTED_ABANDON", "not on the folio", nil)); code != 200 {
		t.Fatal("first decision should commit")
	}
	if code, _ := f.do(t, "POST", "/financial-review/postings/"+id+"/actions",
		reviewBody("CONFIRM_POSTED", "changed my mind", nil)); code != 409 {
		t.Fatalf("a second, incompatible decision must be refused as a conflict, got %d", code)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*)::int FROM iam_v2.posting_review_actions WHERE posting_id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("exactly one decision may be recorded, found %d", n)
	}
}

// ---------------------------------------------------------------- passive reversal

func TestIntegrationReviewAPI_CreateReversalIsPassiveAndNonExecutable(t *testing.T) {
	f := newAPI(t, "payments_operator")
	ctx := context.Background()
	id, iface := f.seedReviewablePosting(t, "ACKED", "OK")

	code, body := f.do(t, "POST", "/financial-review/postings/"+id+"/actions",
		reviewBody("CREATE_REVERSAL", "front office corrected the folio by hand", nil))
	if code != 200 {
		t.Fatalf("CREATE_REVERSAL is a section-15 action and must be implementable, got %d: %v", code, body)
	}
	var revID string
	var amount int64
	if err := f.pool.QueryRow(ctx, `SELECT id::text, amount_minor FROM iam_v2.pms_postings
		WHERE posting_type='REVERSAL' AND reverses_posting_id=$1`, id).Scan(&revID, &amount); err != nil {
		t.Fatalf("the passive ledger row must exist: %v", err)
	}
	if amount != 1000 {
		t.Fatalf("a full reversal records the original amount, got %d", amount)
	}
	// §9a rule 5: no negative TA anywhere
	var neg int
	if err := f.pool.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.pms_postings WHERE amount_minor <= 0`).
		Scan(&neg); err != nil {
		t.Fatal(err)
	}
	if neg != 0 {
		t.Fatalf("a non-positive TA was stored (%d rows)", neg)
	}
	// structurally inert
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.posting_outbox
		(tenant_id,site_id,pms_interface_id,posting_id,state) VALUES ($1,$2,$3,$4,'QUEUED')`,
		f.tenant, f.site, iface, revID); err == nil {
		t.Fatal("a reversal must never be queued")
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.posting_attempts
		(tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,sent_at)
		VALUES ($1,$2,$3,$4,1,'7777','1421','5',now())`, f.tenant, f.site, revID, iface); err == nil {
		t.Fatal("a reversal must never be attempted")
	}
	// and the operator surface says so, in words an operator can act on
	_, detail := f.do(t, "GET", "/financial-review/postings/"+id, nil)
	lim := fmt.Sprint(detail["limitations"])
	if !contains(lim, "manual Front Office") {
		t.Fatalf("the surface must state the v1 manual-correction limitation, got %q", lim)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// ---------------------------------------------------------------- scope

func TestIntegrationReviewAPI_AnotherTenantsPostingIsNotVisible(t *testing.T) {
	a := newAPI(t, "payments_operator")
	b := newAPI(t, "payments_operator")
	otherID, _ := b.seedReviewablePosting(t, "UNKNOWN", "")

	if code, _ := a.do(t, "GET", "/financial-review/postings/"+otherID, nil); code != 404 {
		t.Fatalf("another tenant's posting must not be readable, got %d", code)
	}
	if code, _ := a.do(t, "POST", "/financial-review/postings/"+otherID+"/actions",
		reviewBody("CONFIRM_POSTED", "cross-tenant attempt", nil)); code != 404 {
		t.Fatalf("another tenant's posting must not be decidable, got %d", code)
	}
	var n int
	if err := b.pool.QueryRow(context.Background(),
		`SELECT count(*)::int FROM iam_v2.posting_review_actions WHERE posting_id=$1`, otherID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a cross-tenant decision reached the ledger (%d rows)", n)
	}
}

// THE CASE THE EARLIER TEST MISSED. Every fixture used to get a fresh tenant AND a fresh site, so
// "another site's posting is not visible" was only ever exercised across tenants — and the queries were
// scoped by tenant alone, so they would have passed while leaking between two sites of the SAME customer.
// That is the arrangement a multi-property hotel group actually has.
func TestIntegrationReviewAPI_SameTenantDifferentSiteIsIsolated(t *testing.T) {
	a := newAPI(t, "payments_operator")
	b := newAPIIn(t, a.tenant, "payments_operator") // SAME tenant, different site
	if a.tenant != b.tenant {
		t.Fatalf("fixture error: the two sites must share a tenant (%s vs %s)", a.tenant, b.tenant)
	}
	if a.site == b.site {
		t.Fatal("fixture error: the two sites must differ")
	}

	mine, _ := a.seedReviewablePosting(t, "UNKNOWN", "")
	theirs, _ := b.seedReviewablePosting(t, "UNKNOWN", "")

	// Site A's queue must contain its own posting and NOT site B's.
	code, body := a.do(t, "GET", "/financial-review/queue", nil)
	if code != 200 {
		t.Fatalf("queue: %d", code)
	}
	items, _ := body["items"].([]any)
	sawMine, sawTheirs := false, false
	for _, it := range items {
		row, _ := it.(map[string]any)
		switch row["posting_id"] {
		case mine:
			sawMine = true
		case theirs:
			sawTheirs = true
		}
	}
	if !sawMine {
		t.Fatal("the queue must contain the operator's own site's posting")
	}
	if sawTheirs {
		t.Fatal("the queue LEAKED a posting owned by another site of the same tenant")
	}

	// Detail, and every nested evidence query behind it, must not resolve it either.
	if code, _ := a.do(t, "GET", "/financial-review/postings/"+theirs, nil); code != 404 {
		t.Fatalf("another site's posting must not be readable, got %d", code)
	}
	// ...and it must not be DECIDABLE, with nothing written.
	if code, _ := a.do(t, "POST", "/financial-review/postings/"+theirs+"/actions",
		reviewBody("CONFIRM_POSTED", "cross-site attempt", nil)); code != 404 {
		t.Fatalf("another site's posting must not be decidable, got %d", code)
	}
	var n int
	if err := b.pool.QueryRow(context.Background(),
		`SELECT count(*)::int FROM iam_v2.posting_review_actions WHERE posting_id=$1`, theirs).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a cross-site decision reached the ledger (%d rows)", n)
	}
	// and the refusal must not confirm existence: the same 404 as a genuinely absent posting
	absent := "00000000-0000-0000-0000-000000000000"
	c1, _ := a.do(t, "GET", "/financial-review/postings/"+theirs, nil)
	c2, _ := a.do(t, "GET", "/financial-review/postings/"+absent, nil)
	if c1 != c2 {
		t.Fatalf("an out-of-scope posting (%d) must be indistinguishable from an absent one (%d)", c1, c2)
	}
}

// ---------------------------------------------------------------- evidence exposure

// The detail view must carry what an operator needs and nothing that would leak the connector.
func TestIntegrationReviewAPI_DetailExposesEvidenceButNoSecrets(t *testing.T) {
	f := newAPI(t, "payments_operator")
	id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")
	code, body := f.do(t, "GET", "/financial-review/postings/"+id, nil)
	if code != 200 {
		t.Fatalf("detail: %d", code)
	}
	pinned, _ := body["pinned_evidence"].(map[string]any)
	for _, k := range []string{"settlement_id", "purchase_id", "stay_id", "folio_id",
		"posting_interface_revision_id", "idempotency_key", "folio_identity_strategy"} {
		if pinned[k] == nil || pinned[k] == "" {
			t.Fatalf("the pinned evidence must include %s", k)
		}
	}
	if len(body["attempts"].([]any)) != 1 {
		t.Fatal("the attempt history must be present")
	}
	blob := fmt.Sprint(body)
	for _, banned := range []string{"ciphertext", "nonce", "encryption_key_id", "password_hash",
		"secret", "endpoint"} {
		if contains(blob, banned) {
			t.Fatalf("the review surface leaked %q", banned)
		}
	}
}

// ---------------------------------------------------------------- evidence contract

// §15 requires evidence; §11 forbids secrets and card data in audit payloads. An append-only ledger cannot
// be redacted afterwards, so the shape has to make a secret unrepresentable in the first place.
func TestIntegrationReviewAPI_EvidenceIsStructuredAndRefusesSecrets(t *testing.T) {
	f := newAPI(t, "payments_operator")

	adversarial := []struct {
		name string
		ev   map[string]any
	}{
		{"unknown source type", map[string]any{"source_type": "SOMETHING_ELSE", "reference": "x"}},
		{"missing reference", map[string]any{"source_type": "PMS_REPORT", "reference": "  "}},
		{"a password in the note", map[string]any{"source_type": "PMS_REPORT", "reference": "r1",
			"note": "the pms password is hunter2"}},
		{"an api key in the reference", map[string]any{"source_type": "PMS_REPORT",
			"reference": "sk_live_51H8xQ2eZvKYlo2C"}},
		{"a bearer token", map[string]any{"source_type": "PROVIDER_DASHBOARD", "reference": "r1",
			"note": "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"}},
		{"something shaped like a card number", map[string]any{"source_type": "PROVIDER_DASHBOARD",
			"reference": "r1", "note": "card 4111 1111 1111 1111 was charged"}},
		{"a cvv reference", map[string]any{"source_type": "PROVIDER_DASHBOARD", "reference": "r1",
			"note": "cvv mismatch reported"}},
		{"a raw FIAS frame", map[string]any{"source_type": "PMS_REPORT", "reference": "r1",
			"note": "PS|RN1421|G#5|TA1000|"}},
		{"a raw JSON payload", map[string]any{"source_type": "PROVIDER_DASHBOARD", "reference": "r1",
			"note": "{\"provider\":\"stripe\",\"secret\":\"x\"}"}},
		{"an unbounded note", map[string]any{"source_type": "PMS_REPORT", "reference": "r1",
			"note": strings.Repeat("x", 501)}},
		{"an over-long reference", map[string]any{"source_type": "PMS_REPORT",
			"reference": strings.Repeat("r", 121)}},
		{"a multi-line note", map[string]any{"source_type": "PMS_REPORT", "reference": "r1",
			"note": "line one\nline two"}},
		{"OTHER_DOCUMENTED with no note", map[string]any{"source_type": "OTHER_DOCUMENTED", "reference": "r1"}},
		{"a future verification time", map[string]any{"source_type": "PMS_REPORT", "reference": "r1",
			"verified_at": "2099-01-01T00:00:00Z"}},
	}
	for _, tc := range adversarial {
		t.Run(tc.name, func(t *testing.T) {
			id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")
			b := reviewBody("CONFIRM_POSTED", "adversarial evidence", nil)
			b["evidence"] = tc.ev
			code, resp := f.do(t, "POST", "/financial-review/postings/"+id+"/actions", b)
			if code != 400 {
				t.Fatalf("expected the evidence to be refused, got %d: %v", code, resp)
			}
			var n int
			if err := f.pool.QueryRow(context.Background(),
				`SELECT count(*)::int FROM iam_v2.posting_review_actions WHERE posting_id=$1`, id).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("refused evidence still reached the immutable ledger (%d rows)", n)
			}
		})
	}

	// the positive case: a real reconciliation record is accepted and stored in its canonical shape
	id, _ := f.seedReviewablePosting(t, "UNKNOWN", "")
	b := reviewBody("CONFIRM_POSTED", "folio inspected", nil)
	b["evidence"] = map[string]any{
		"source_type": "PMS_FOLIO_INSPECTION", "reference": "folio-1421/line-7",
		"note": "charge present on the guest folio", "verified_at": "2026-08-12T07:00:00Z",
	}
	if code, resp := f.do(t, "POST", "/financial-review/postings/"+id+"/actions", b); code != 200 {
		t.Fatalf("legitimate evidence must be accepted, got %d: %v", code, resp)
	}
	var stored string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT evidence::text FROM iam_v2.posting_review_actions WHERE posting_id=$1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PMS_FOLIO_INSPECTION", "folio-1421/line-7", "2026-08-12T07:00:00Z"} {
		if !contains(stored, want) {
			t.Fatalf("the canonical evidence must retain %q, got %s", want, stored)
		}
	}
}
