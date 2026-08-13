//go:build integration && phase4

// PHASE-4 TEST OWNERSHIP. The `phase4` tag is not decoration: these tests need the Phase-4 schema
// (migrations 0011-0026), and the PHASE-3 regression gate builds a database that stops at 0010. With
// only the `integration` tag they compiled into the Phase-3 gate's ./cmd/edged/ package and failed
// against a schema in which `financial_base_currency` and `p4_declare_financial_recovery` cannot exist
// -- a false failure that says nothing about Phase 3 and hides anything that would say something.
// The tag is what makes the schema requirement structural rather than a naming convention.

package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// THE ZERO-ATTEMPT OPERATOR PATH, at the API.
//
// The database function was delivered and proven in the previous milestone. What was missing is the part
// that only exists here: that a real operator, with a real session, in the right scope, can reach it — and
// that nobody else can. A safe path nobody can walk is not a path.
//
// Everything below uses the EXISTING financial-review authorization model: tenant+site scope, the actor
// taken from the session, a password step-up, and the same bounded, screened reason/evidence contract every
// other financial decision uses.

// seedZeroAttemptHold builds the exact state: a posting held by recovery, never transmitted, with its hold
// reconciled as CONFIRMED_NOT_COMPLETED. It returns the posting id.
func (f *apiFixture) seedZeroAttemptHold(t *testing.T, reconciled bool) string {
	t.Helper()
	ctx := context.Background()

	// Built directly rather than by reusing seedReviewablePosting, which transmits once. posting_attempts is
	// append-only and correctly refuses a DELETE, so a fixture that created an attempt could not un-create
	// it -- and a posting WITH an attempt is the ordinary review path, not the case under test.
	q := func(dst *string, sql string, args ...any) {
		t.Helper()
		if err := f.pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed zero-attempt posting: %v", err)
		}
	}
	var ifaceID, revID, stayID, folioID, purchaseID, settlementID, postingID, outboxID string
	q(&ifaceID, `INSERT INTO iam_v2.pms_interfaces(tenant_id,site_id,connector_kind)
		VALUES ($1,$2,'protel-fias') RETURNING id::text`, f.tenant, f.site)
	q(&revID, `INSERT INTO iam_v2.pms_interface_revisions
		(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config,
		 financial_base_currency,financial_base_currency_exponent)
		VALUES ($1,$2,$3,1,'UTC','GLOBALLY_UNIQUE','{}','USD',2) RETURNING id::text`,
		f.tenant, f.site, ifaceID)
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
	q(&stayID, `INSERT INTO iam_v2.stays
		(tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,posting_allowed)
		VALUES ($1,$2,$3,'R'||substr(md5(random()::text),1,8),'S'||substr(md5(random()::text),1,8),
		        'IN_HOUSE',true) RETURNING id::text`, f.tenant, f.site, ifaceID)
	q(&folioID, `INSERT INTO iam_v2.folios(tenant_id,site_id,pms_interface_id,external_folio_id)
		VALUES ($1,$2,$3,'F'||substr(md5(random()::text),1,8)) RETURNING id::text`, f.tenant, f.site, ifaceID)

	var planID, planRev, pkgID, pkgRev string
	q(&planID, `INSERT INTO iam_v2.service_plans(tenant_id,site_id,code)
		VALUES ($1,$2,'za-'||substr(md5(random()::text),1,8)) RETURNING id::text`, f.tenant, f.site)
	q(&planRev, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id,site_id,service_plan_id,revision_no,name,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'za',2,'VALIDITY_WINDOW',1000000) RETURNING id::text`, f.tenant, f.site, planID)
	q(&pkgID, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code)
		VALUES ($1,$2,'za-'||substr(md5(random()::text),1,8)) RETURNING id::text`, f.tenant, f.site)
	q(&pkgRev, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent)
		VALUES ($1,$2,$3,1,$4,'GENERAL',100,'USD',2) RETURNING id::text`, f.tenant, f.site, pkgID, planRev)
	q(&purchaseID, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,
		 amount_minor,currency,currency_exponent,state)
		VALUES ($1,$2,$3,$4,$5,'ADMIN_GRANT',100,'USD',2,'AWAITING_SETTLEMENT') RETURNING id::text`,
		f.tenant, f.site, pkgRev, ifaceID, stayID)
	q(&settlementID, `INSERT INTO iam_v2.settlements(tenant_id,site_id,purchase_id,method,status)
		VALUES ($1,$2,$3,'PMS_POSTING','REQUIRED') RETURNING id::text`, f.tenant, f.site, purchaseID)
	q(&postingID, `INSERT INTO iam_v2.pms_postings
		(tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,
		 posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'CHARGE',100,'USD',2,'za-'||substr(md5(random()::text),1,12))
		RETURNING id::text`, f.tenant, f.site, ifaceID, settlementID, purchaseID, stayID, folioID, revID)
	q(&outboxID, `INSERT INTO iam_v2.posting_outbox(tenant_id,site_id,pms_interface_id,posting_id,state)
		VALUES ($1,$2,$3,$4,'QUEUED') RETURNING id::text`, f.tenant, f.site, ifaceID, postingID)

	if n := f.scanInt(t, `SELECT count(*)::int FROM iam_v2.posting_attempts WHERE internal_posting_id=$1`,
		postingID); n != 0 {
		t.Fatalf("setup: the posting already has %d attempt(s)", n)
	}

	// Hold it exactly as recovery would, then reconcile the hold as CONFIRMED_NOT_COMPLETED.
	if _, err := f.pool.Exec(ctx,
		`UPDATE iam_v2.posting_outbox SET state='HELD_RECOVERY' WHERE id=$1`, outboxID); err != nil {
		t.Fatalf("hold the outbox row: %v", err)
	}
	var epoch int64
	if err := f.pool.QueryRow(ctx, `SELECT iam_v2.p4_declare_financial_recovery($1,$2,$3,$4)`,
		f.tenant, f.site, f.operator, "api test: zero-attempt restore").Scan(&epoch); err != nil {
		t.Fatalf("declare recovery: %v", err)
	}
	var holdID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.financial_recovery_holds
		(tenant_id,site_id,epoch,work_kind,work_id,held_status)
		VALUES ($1,$2,$3,'POSTING_OUTBOX',$4::uuid,'QUEUED')
		ON CONFLICT (tenant_id,site_id,epoch,work_kind,work_id) DO UPDATE SET held_status='QUEUED'
		RETURNING id::text`, f.tenant, f.site, epoch, outboxID).Scan(&holdID); err != nil {
		t.Fatalf("seed hold: %v", err)
	}
	// An unreconciled hold is left exactly as recovery leaves it. The resolution ledger is append-only --
	// p4_resolve_recovery_hold refuses to revise a conclusion about money in place -- so the unreconciled
	// case has to be SEEDED unreconciled rather than produced by undoing a resolution.
	if reconciled {
		if _, err := f.pool.Exec(ctx, `SELECT iam_v2.p4_resolve_recovery_hold($1::uuid,$2,$3::uuid,$4)`,
			holdID, "CONFIRMED_NOT_COMPLETED", f.operator,
			"checked the folio directly; this charge was never posted"); err != nil {
			t.Fatalf("resolve hold: %v", err)
		}
	}
	return postingID
}

func (f *apiFixture) scanInt(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	return n
}

func TestIntegrationZeroAttemptAPI_TheQueueShowsWhatTheReviewQueueCannot(t *testing.T) {
	f := newAPI(t, "payments_operator")
	postingID := f.seedZeroAttemptHold(t, true)

	// the ordinary review queue cannot show it: that queue keys on attempts and this posting has none
	code, body := f.do(t, "GET", "/financial-review/queue", nil)
	if code != 200 {
		t.Fatalf("review queue: %d", code)
	}
	for _, row := range rowsOf(body["queue"]) {
		if row["posting_id"] == postingID {
			t.Fatal("the zero-attempt posting appeared in the attempt-keyed review queue")
		}
	}

	code, body = f.do(t, "GET", "/financial-ops/recovery/zero-attempt", nil)
	if code != 200 {
		t.Fatalf("zero-attempt queue: %d %v", code, body)
	}
	var found map[string]any
	for _, m := range rowsOf(body["queue"]) {
		if m["posting_id"] == postingID {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("the zero-attempt posting is not listed anywhere an operator can see it: %v", body)
	}
	if found["eligible_for_retry_authorization"] != true {
		t.Fatalf("a reconciled zero-attempt posting is not shown as eligible: %v", found)
	}
	if found["hold_resolution"] != "CONFIRMED_NOT_COMPLETED" {
		t.Fatalf("the queue does not carry the evidence the decision rests on: %v", found)
	}
}

func TestIntegrationZeroAttemptAPI_AuthorizationNeedsStepUpEvidenceAndScope(t *testing.T) {
	f := newAPI(t, "payments_operator")
	postingID := f.seedZeroAttemptHold(t, true)
	path := "/financial-ops/recovery/zero-attempt/" + postingID + "/authorize"

	// no step-up
	if code, _ := f.do(t, "POST", path, map[string]any{
		"reason":   "folio checked; nothing was posted, so the charge must still go out",
		"evidence": map[string]any{"source_type": "PMS_FOLIO_INSPECTION", "reference": "folio-4471-line-7"},
	}); code != 401 {
		t.Fatalf("an authorization without step-up returned %d", code)
	}
	// no evidence — the whole decision rests on somebody having looked
	if code, _ := f.do(t, "POST", path, map[string]any{
		"reason":   "folio checked; nothing was posted, so the charge must still go out",
		"password": f.password,
	}); code != 400 {
		t.Fatalf("an authorization with no evidence returned %d", code)
	}
	// an actor cannot even be offered
	if code, _ := f.do(t, "POST", path, map[string]any{
		"reason":   "folio checked; nothing was posted",
		"evidence": map[string]any{"source_type": "PMS_FOLIO_INSPECTION", "reference": "folio-4471-line-7"},
		"password": f.password,
		"actor":    "00000000-0000-0000-0000-000000000000",
	}); code != 400 {
		t.Fatalf("a request naming its own author returned %d", code)
	}
	// nothing was recorded and nothing was released by any of that
	if n := f.scanInt(t, `SELECT count(*)::int FROM iam_v2.posting_review_actions WHERE posting_id=$1`,
		postingID); n != 0 {
		t.Fatalf("a refused authorization recorded %d decisions", n)
	}
	if st := f.scanStr(t, `SELECT state FROM iam_v2.posting_outbox WHERE posting_id=$1`, postingID); st != "HELD_RECOVERY" {
		t.Fatalf("a refused authorization released the posting: %s", st)
	}
}

// Same tenant, different site. This is the isolation a fresh-tenant-per-fixture test cannot see.
func TestIntegrationZeroAttemptAPI_AnotherSiteOfTheSameTenantCannotAuthorize(t *testing.T) {
	a := newAPI(t, "payments_operator")
	b := newAPIIn(t, a.tenant, "payments_operator")
	postingID := b.seedZeroAttemptHold(t, true)

	codeReal, bodyReal := a.doRaw(t, "POST",
		"/financial-ops/recovery/zero-attempt/"+postingID+"/authorize", map[string]any{
			"reason":   "authorizing another property's posting",
			"evidence": map[string]any{"source_type": "PMS_FOLIO_INSPECTION", "reference": "folio-0001-line-1"},
			"password": a.password,
		})
	codeFake, bodyFake := a.doRaw(t, "POST",
		"/financial-ops/recovery/zero-attempt/11111111-2222-3333-4444-555555555555/authorize",
		map[string]any{
			"reason":   "authorizing a posting that does not exist",
			"evidence": map[string]any{"source_type": "PMS_FOLIO_INSPECTION", "reference": "folio-0001-line-1"},
			"password": a.password,
		})
	if codeReal != 404 || codeFake != 404 {
		t.Fatalf("cross-site authorization leaked: real=%d fake=%d", codeReal, codeFake)
	}
	if bodyReal != bodyFake {
		t.Fatalf("an out-of-scope posting is distinguishable from an absent one:\n real=%s\n fake=%s",
			bodyReal, bodyFake)
	}
	if st := b.scanStr(t, `SELECT state FROM iam_v2.posting_outbox WHERE posting_id=$1`, postingID); st != "HELD_RECOVERY" {
		t.Fatalf("site B's posting was released by site A's operator: %s", st)
	}
}

// The whole point, end to end: the operator authorizes, NOTHING is transmitted, exactly one authorization
// exists, and a second attempt to authorize is refused.
func TestIntegrationZeroAttemptAPI_AuthorizesExactlyOnceAndTransmitsNothing(t *testing.T) {
	f := newAPI(t, "payments_operator")
	postingID := f.seedZeroAttemptHold(t, true)
	path := "/financial-ops/recovery/zero-attempt/" + postingID + "/authorize"
	req := map[string]any{
		"reason":   "checked the folio directly; nothing was posted, so the charge must still go out",
		"evidence": map[string]any{"source_type": "PMS_FOLIO_INSPECTION", "reference": "folio-4471-line-7"},
		"password": f.password,
	}

	code, body := f.do(t, "POST", path, req)
	if code != 200 {
		t.Fatalf("a valid authorization was refused: %d %v", code, body)
	}
	if body["transmitted"] != false {
		t.Fatalf("the API reported a transmission: %v", body)
	}
	if body["actor"] != f.operator {
		t.Fatalf("the recorded actor is %v, not the session operator", body["actor"])
	}

	// the posting is sendable, nothing has been sent, and no history was invented
	if st := f.scanStr(t, `SELECT state FROM iam_v2.posting_outbox WHERE posting_id=$1`, postingID); st != "QUEUED" {
		t.Fatalf("the authorized posting is %s", st)
	}
	if n := f.scanInt(t, `SELECT count(*)::int FROM iam_v2.posting_attempts WHERE internal_posting_id=$1`,
		postingID); n != 0 {
		t.Fatalf("the authorization manufactured %d attempt(s)", n)
	}
	if n := f.scanInt(t, `SELECT retry_authorized_attempt_no FROM iam_v2.posting_review_state
		WHERE posting_id=$1`, postingID); n != 1 {
		t.Fatalf("expected attempt 1 authorized, got %d", n)
	}
	if n := f.scanInt(t, `SELECT count(*)::int FROM iam_v2.posting_review_actions
		WHERE posting_id=$1 AND action='CONFIRM_NOT_POSTED_RETRY'`, postingID); n != 1 {
		t.Fatalf("expected exactly one audited decision, got %d", n)
	}

	// EXACTLY ONE. A second authorization is refused, because the posting is no longer held.
	if code, _ := f.do(t, "POST", path, req); code == 200 {
		t.Fatal("a second authorization was accepted; the path is not one-time")
	}
	if n := f.scanInt(t, `SELECT count(*)::int FROM iam_v2.posting_review_actions
		WHERE posting_id=$1 AND action='CONFIRM_NOT_POSTED_RETRY'`, postingID); n != 1 {
		t.Fatalf("a refused second authorization still recorded a decision: %d total", n)
	}

	// and it disappears from the queue, because it is no longer a stuck zero-attempt posting
	_, qbody := f.do(t, "GET", "/financial-ops/recovery/zero-attempt", nil)
	for _, r := range rowsOf(qbody["queue"]) {
		if r["posting_id"] == postingID {
			t.Fatal("an authorized posting is still listed as awaiting authorization")
		}
	}
}

// An unreconciled posting must not be authorizable, whatever the operator writes in the reason.
func TestIntegrationZeroAttemptAPI_AnUnreconciledPostingIsRefused(t *testing.T) {
	f := newAPI(t, "payments_operator")
	postingID := f.seedZeroAttemptHold(t, false)
	// It IS listed -- an operator must be able to see the item -- but not as eligible, and the reason is
	// visible rather than something they discover by being refused.
	_, qbody := f.do(t, "GET", "/financial-ops/recovery/zero-attempt", nil)
	var listed map[string]any
	for _, r := range rowsOf(qbody["queue"]) {
		if r["posting_id"] == postingID {
			listed = r
		}
	}
	if listed == nil {
		t.Fatalf("an unreconciled held posting is invisible to the operator: %v", qbody)
	}
	if listed["eligible_for_retry_authorization"] == true {
		t.Fatalf("an unreconciled posting is offered as eligible: %v", listed)
	}
	code, body := f.do(t, "POST", "/financial-ops/recovery/zero-attempt/"+postingID+"/authorize",
		map[string]any{
			"reason":   "I am confident nothing was posted",
			"evidence": map[string]any{"source_type": "PMS_FOLIO_INSPECTION", "reference": "folio-4471-line-7"},
			"password": f.password,
		})
	if code == 200 {
		t.Fatalf("an unreconciled posting was authorized: %v", body)
	}
	if st := f.scanStr(t, `SELECT state FROM iam_v2.posting_outbox WHERE posting_id=$1`, postingID); st != "HELD_RECOVERY" {
		t.Fatalf("an unreconciled posting was released: %s", st)
	}
}

// A role with no financial permission cannot see or use the surface at all.
func TestIntegrationZeroAttemptAPI_RequiresTheFinancialPermission(t *testing.T) {
	f := newAPI(t, "guest_relations_operator")
	if code, _ := f.doRaw(t, "GET", "/financial-ops/recovery/zero-attempt", nil); code != 403 && code != 404 {
		t.Fatalf("a non-financial role read the zero-attempt queue: %d", code)
	}
}

func (f *apiFixture) scanStr(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var v string
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("query: %v", err)
	}
	return v
}

// rowsOf reads a JSON array of objects defensively. A nil here means "the queue was empty", which is a
// legitimate answer and not a reason to panic in the middle of an assertion about something else.
func rowsOf(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// restart rebuilds the server the way a process restart does: a fresh router and a fresh in-memory session
// store -- so the previous session is gone -- over the same database. Nothing about an audited financial
// authorization may depend on process memory, and nothing may be re-sent because a process came back.
func (f *apiFixture) restart(t *testing.T, roles ...string) *apiFixture {
	t.Helper()
	f.srv.Close()
	n := &apiFixture{pool: f.pool, tenant: f.tenant, site: f.site, operator: f.operator, password: f.password}
	s := &server{db: f.pool, sessions: newSessionStore(2 * time.Hour), tenantID: f.tenant, siteID: f.site}
	n.sessTok = s.sessions.create(&session{OperatorID: f.operator, Email: "op@test.local", Roles: roles})
	r := chi.NewRouter()
	r.Route("/edge/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			mountResource(r, s, "financial-review", s.financialReviewRoutes)
			mountResource(r, s, "financial-ops", s.financialOpsRoutes)
		})
	})
	n.srv = httptest.NewServer(r)
	t.Cleanup(n.srv.Close)
	return n
}

// The other half of "end to end": what the authorization is FOR. A zero-attempt restore leaves a site unable
// to leave recovery -- the item can never be reconciled away, because the ordinary review queue cannot see
// it. These are the two states either side of that: refused while anything is undecided, releasable once the
// operator has decided, and unchanged by a restart in between.
func TestIntegrationZeroAttemptAPI_SurvivesRestartAndUnblocksRelease(t *testing.T) {
	f := newAPI(t, "payments_operator")
	postingID := f.seedZeroAttemptHold(t, true)

	code, _ := f.do(t, "POST", "/financial-ops/recovery/zero-attempt/"+postingID+"/authorize",
		map[string]any{
			"reason":   "checked the folio directly; nothing was posted, so the charge must still go out",
			"evidence": map[string]any{"source_type": "PMS_FOLIO_INSPECTION", "reference": "folio-4471-line-7"},
			"password": f.password,
		})
	if code != 200 {
		t.Fatalf("authorize: %d", code)
	}

	// ---- restart
	g := f.restart(t, "payments_operator")

	// the authorization is where it belongs -- in the database -- and the restart transmitted nothing
	if n := g.scanInt(t, `SELECT retry_authorized_attempt_no FROM iam_v2.posting_review_state
		WHERE posting_id=$1`, postingID); n != 1 {
		t.Fatalf("after a restart the authorization reads %d", n)
	}
	if n := g.scanInt(t, `SELECT count(*)::int FROM iam_v2.posting_attempts WHERE internal_posting_id=$1`,
		postingID); n != 0 {
		t.Fatalf("the restart produced %d attempt(s)", n)
	}
	if n := g.scanInt(t, `SELECT count(*)::int FROM iam_v2.posting_review_actions
		WHERE posting_id=$1 AND action='CONFIRM_NOT_POSTED_RETRY'`, postingID); n != 1 {
		t.Fatalf("the restart left %d audited decisions", n)
	}
	// and it is still not offered again
	_, qbody := g.do(t, "GET", "/financial-ops/recovery/zero-attempt", nil)
	for _, r := range rowsOf(qbody["queue"]) {
		if r["posting_id"] == postingID {
			t.Fatal("after a restart the authorized posting is offered for authorization again")
		}
	}

	// ---- and recovery can now be released, which is the point of the whole path.
	// Declaring recovery holds every rail, so the settlement behind this posting is held too and an operator
	// reconciles it like any other. That is ordinary recovery work; what the authorization changed is the one
	// item no surface could previously decide.
	_, hbody := g.do(t, "GET", "/financial-ops/recovery/holds", nil)
	for _, h := range rowsOf(hbody["holds"]) {
		if h["work_kind"] == "POSTING_OUTBOX" {
			t.Fatalf("the authorized posting hold is still open: %v", h)
		}
		if c, b := g.do(t, "POST", "/financial-ops/recovery/holds/"+h["hold_id"].(string)+"/resolve",
			map[string]any{"resolution": "CONFIRMED_NOT_COMPLETED", "password": g.password,
				"note": "no money moved against this " + h["work_kind"].(string) + "; checked at the desk",
			}); c != 200 {
			t.Fatalf("resolve remaining hold: %d %v", c, b)
		}
	}

	code, body := g.do(t, "POST", "/financial-ops/recovery/release", map[string]any{
		"note":     "the one zero-attempt posting was reconciled and authorized; nothing else is in flight",
		"password": g.password,
	})
	if code != 200 {
		t.Fatalf("recovery could not be released after the authorization: %d %v", code, body)
	}
	if body["released"] != true {
		t.Fatalf("release reported %v", body)
	}
	// releasing is not sending
	if n := g.scanInt(t, `SELECT count(*)::int FROM iam_v2.posting_attempts WHERE internal_posting_id=$1`,
		postingID); n != 0 {
		t.Fatalf("releasing recovery transmitted %d attempt(s)", n)
	}
}

// The negative either side of it: while the zero-attempt item is undecided, recovery does not release. This
// is what makes the authorization necessary rather than merely available.
func TestIntegrationZeroAttemptAPI_AnUndecidedItemHoldsRecoveryShut(t *testing.T) {
	f := newAPI(t, "payments_operator")
	f.seedZeroAttemptHold(t, false)

	code, body := f.do(t, "POST", "/financial-ops/recovery/release", map[string]any{
		"note":     "trying to leave recovery with an unreconciled zero-attempt posting",
		"password": f.password,
	})
	if code == 200 {
		t.Fatalf("recovery released with an undecided zero-attempt item: %v", body)
	}
	if n := f.scanInt(t, `SELECT count(*)::int FROM iam_v2.financial_epochs
		WHERE tenant_id=$1 AND site_id=$2 AND released_at IS NULL`, f.tenant, f.site); n != 1 {
		t.Fatalf("the epoch is no longer open after a refused release: %d open", n)
	}
}
