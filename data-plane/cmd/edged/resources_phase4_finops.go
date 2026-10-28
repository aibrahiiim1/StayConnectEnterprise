package main

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

// Financial operations API: health, payments and post-restore recovery. Mounted under
// resourcePermission("financial-review"); the two recovery actions also require a password step-up because they
// assert what happened to real money. Responses are bounded projections: payment history omits provider_ref and
// idempotency_key so an operator cannot act on the correlation handle out of band. Mounted only when the Phase-4
// master and review flags are on, like the review routes.
func (s *server) financialOpsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", s.getFinancialHealth)
	r.Get("/settlements", s.listSettlements)
	r.Get("/settlements/{id}", s.getSettlement)
	r.Get("/recovery", s.getRecoveryState)
	r.Get("/recovery/holds", s.listRecoveryHolds)
	r.Post("/recovery/holds/{id}/resolve", s.resolveRecoveryHold)
	r.Post("/recovery/release", s.releaseRecovery)
	// Lives here, not under financial-review: these postings are recovery holds reached from the recovery screen.
	r.Get("/recovery/zero-attempt", s.listZeroAttemptQueue)
	r.Post("/recovery/zero-attempt/{id}/authorize", s.authorizeZeroAttemptRetry)
	return r
}

func (s *server) getFinancialHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	eng, err := s.paymentEngine()
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "unavailable", "the financial runtime is not available")
		return
	}
	h, err := eng.Health(ctx, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "health query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"health": h,
		"note": "Counts, ages in seconds and fixed condition codes only. This surface carries no guest " +
			"data, no identifiers, no provider payloads and no credentials by construction.",
	})
}

type settlementRow struct {
	SettlementID  string     `json:"settlement_id"`
	PurchaseID    string     `json:"purchase_id"`
	Method        string     `json:"method"`
	Status        string     `json:"status"`
	PurchaseState string     `json:"purchase_state"`
	AmountMinor   int64      `json:"amount_minor"`
	Currency      string     `json:"currency"`
	Exponent      int16      `json:"currency_exponent"`
	PackageName   *string    `json:"package_name"`
	At            *time.Time `json:"at"`
	Source        string     `json:"source"`
	Room          *string    `json:"room"`
	// The PMS connection a room payment belongs to: a room number only identifies a room together with it.
	PMSConnection *string `json:"pms_interface_label,omitempty"`
}

const settlementSelect = `
SELECT v.settlement_id::text, v.purchase_id::text, v.method, v.status, v.purchase_state,
       coalesce(v.amount_minor,0), coalesce(v.currency,''), coalesce(v.currency_exponent,0),
       coalesce(r.display->>'name', ip.code), coalesce(q.consumed_at, e.activated_at),
       CASE WHEN p.stay_id IS NOT NULL THEN 'ROOM'
            WHEN e.voucher_id IS NOT NULL OR v.method = 'PREPAID' THEN 'VOUCHER'
            WHEN e.guest_account_id IS NOT NULL THEN 'ACCOUNT'
            WHEN e.anonymous_subject_id IS NOT NULL THEN 'OPEN'
            ELSE '' END,
       st.normalized_room_number,
       (SELECT NULLIF(pi.display_label,'') FROM iam_v2.pms_interfaces pi WHERE pi.id = st.pms_interface_id)
  FROM iam_v2.v_financial_settlements v
  JOIN iam_v2.purchases p ON p.id = v.purchase_id
  LEFT JOIN iam_v2.internet_package_revisions r ON r.id = p.package_revision_id
  LEFT JOIN iam_v2.internet_packages ip ON ip.id = r.package_id
  LEFT JOIN iam_v2.offer_quotes q ON q.id = p.offer_quote_id
  LEFT JOIN LATERAL (SELECT en.voucher_id, en.guest_account_id, en.anonymous_subject_id, en.activated_at
                       FROM iam_v2.entitlements en WHERE en.purchase_id = p.id LIMIT 1) e ON true
  LEFT JOIN iam_v2.stays st ON st.id = p.stay_id`

func scanSettlement(row interface{ Scan(...any) error }, e *settlementRow) error {
	return row.Scan(&e.SettlementID, &e.PurchaseID, &e.Method, &e.Status, &e.PurchaseState,
		&e.AmountMinor, &e.Currency, &e.Exponent, &e.PackageName, &e.At, &e.Source, &e.Room, &e.PMSConnection)
}

// Applied in SQL so a view pages over every payment, not just the page on screen.
var settlementViews = map[string]string{
	"attention": `v.status IN ('MANUAL_REVIEW','FAILED','REQUIRED','IN_PROGRESS') AND v.method <> 'NOT_REQUIRED'`,
	"paid":      `v.method IN ('ONLINE_PAYMENT','PMS_POSTING')`,
	"voucher":   `v.method = 'PREPAID'`,
	"free":      `v.method = 'NOT_REQUIRED'`,
}

type settlementCollected struct {
	Currency    string `json:"currency"`
	Exponent    int16  `json:"currency_exponent"`
	AmountMinor int64  `json:"amount_minor"`
}

// Describes every payment the status filter selects, not the page or the view, so view counts stay put.
type settlementsSummary struct {
	All       int                   `json:"all"`
	Attention int                   `json:"attention"`
	Paid      int                   `json:"paid"`
	Voucher   int                   `json:"voucher"`
	Free      int                   `json:"free"`
	Collected []settlementCollected `json:"collected"`
}

func (s *server) listSettlements(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var statusArg any
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		statusArg = v
	}
	viewClause := "true"
	if v := strings.TrimSpace(r.URL.Query().Get("view")); v != "" {
		c, ok := settlementViews[v]
		if !ok {
			jsonErr(w, http.StatusBadRequest, "bad_request", "view must be attention, paid, voucher or free")
			return
		}
		viewClause = c
	}
	pg, ok := readPage(w, r, 0)
	if !ok {
		return
	}
	scope := `
 WHERE v.tenant_id=$1 AND v.site_id=$2 AND ($3::text IS NULL OR v.status=$3::text)`

	sum := settlementsSummary{Collected: []settlementCollected{}}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int,
	       count(*) FILTER (WHERE `+settlementViews["attention"]+`)::int,
	       count(*) FILTER (WHERE `+settlementViews["paid"]+`)::int,
	       count(*) FILTER (WHERE `+settlementViews["voucher"]+`)::int,
	       count(*) FILTER (WHERE `+settlementViews["free"]+`)::int,
	       count(*) FILTER (WHERE `+viewClause+`)::int
	  FROM iam_v2.v_financial_settlements v`+scope, s.tenantID, s.siteID, statusArg).
		Scan(&sum.All, &sum.Attention, &sum.Paid, &sum.Voucher, &sum.Free, &total); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	crows, err := s.db.Query(ctx, `SELECT coalesce(v.currency,''), coalesce(max(v.currency_exponent),0)::int2,
	       coalesce(sum(v.amount_minor),0)::bigint
	  FROM iam_v2.v_financial_settlements v`+scope+`
	   AND v.status = 'SETTLED' AND `+settlementViews["paid"]+`
	 GROUP BY 1 ORDER BY 1`, s.tenantID, s.siteID, statusArg)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	for crows.Next() {
		var c settlementCollected
		if err := crows.Scan(&c.Currency, &c.Exponent, &c.AmountMinor); err != nil {
			crows.Close()
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		sum.Collected = append(sum.Collected, c)
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	rows, err := s.db.Query(ctx, settlementSelect+scope+`
   AND `+viewClause+`
 ORDER BY coalesce(q.consumed_at, e.activated_at) DESC NULLS LAST, v.settlement_id
 LIMIT $4 OFFSET $5`, s.tenantID, s.siteID, statusArg, pg.Fetch(), pg.Offset())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []settlementRow{}
	for rows.Next() {
		var e settlementRow
		if err := scanSettlement(rows, &e); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	out, more := trimPage(out, pg)
	// "settlements" and "limit" are the keys this answer always had; limit is the page size.
	writeJSON(w, http.StatusOK, map[string]any{
		"settlements": out, "limit": pg.Size,
		"page": pg.Page, "page_size": pg.Size, "meta": listMeta{HasMore: more}, "total": total, "summary": sum,
	})
}

type finopsPaymentRow struct {
	PaymentID   string  `json:"payment_id"`
	Type        string  `json:"transaction_type"`
	Status      string  `json:"status"`
	Provider    string  `json:"provider"`
	AmountMinor int64   `json:"amount_minor"`
	Currency    string  `json:"currency"`
	Exponent    int16   `json:"currency_exponent"`
	ParentID    *string `json:"parent_transaction_id"`
	// The provider's report of what it settled a captured card charge as, as the provider stated it; absent when not reported.
	Settlement *finopsSettlementReport `json:"settlement_report,omitempty"`
}

type finopsSettlementReport struct {
	Currency     string  `json:"settlement_currency"`
	AmountMinor  int64   `json:"settlement_amount_minor"`
	ExchangeRate *string `json:"exchange_rate,omitempty"`
	FeeMinor     *int64  `json:"fee_minor,omitempty"`
	NetMinor     *int64  `json:"net_minor,omitempty"`
	BalanceRef   *string `json:"provider_balance_ref,omitempty"`
	Converted    bool    `json:"converted"`
}

// One settlement with its payment history (the charge and every refund or chargeback that followed).
func (s *server) getSettlement(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	var se settlementRow
	// Scoped by tenant AND site; a settlement of another property must read as absent, not forbidden.
	err := scanSettlement(s.db.QueryRow(ctx, settlementSelect+`
 WHERE v.tenant_id=$1 AND v.site_id=$2 AND v.settlement_id=$3::uuid`, s.tenantID, s.siteID, id), &se)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "no such settlement")
		return
	}
	rows, err := s.db.Query(ctx, `
SELECT payment_id::text, transaction_type, status, provider, amount_minor, currency, currency_exponent,
       parent_transaction_id::text
  FROM iam_v2.v_financial_payments
 WHERE tenant_id=$1 AND site_id=$2 AND settlement_id=$3::uuid
 ORDER BY transaction_type DESC, payment_id`, s.tenantID, s.siteID, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	pay := []finopsPaymentRow{}
	for rows.Next() {
		var p finopsPaymentRow
		if err := rows.Scan(&p.PaymentID, &p.Type, &p.Status, &p.Provider, &p.AmountMinor, &p.Currency,
			&p.Exponent, &p.ParentID); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		pay = append(pay, p)
	}
	rows.Close()
	for i := range pay {
		if pay[i].Type != "CHARGE" {
			continue
		}
		var rep finopsSettlementReport
		var cur *string
		var amt *int64
		if err := s.db.QueryRow(ctx, `SELECT settlement_currency, settlement_amount_minor, exchange_rate, fee_minor, net_minor,
		       provider_balance_ref, converted FROM iam_v2.payment_settlement_reports
		 WHERE tenant_id=$1 AND site_id=$2 AND transaction_id=$3::uuid AND report_status='REPORTED'`,
			s.tenantID, s.siteID, pay[i].PaymentID).Scan(&cur, &amt, &rep.ExchangeRate, &rep.FeeMinor, &rep.NetMinor,
			&rep.BalanceRef, &rep.Converted); err == nil && cur != nil && amt != nil {
			rep.Currency, rep.AmountMinor = *cur, *amt
			pay[i].Settlement = &rep
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settlement":        se,
		"payments":          pay,
		"available_actions": []string{},
		"note": "Refund and chargeback initiation are NOT available from this surface in Phase 4. The " +
			"backend can record them, but no provider adapter exists and no operator-initiated refund " +
			"path has been authorized, so offering the button would imply a capability that is not there.",
	})
}

func (s *server) getRecoveryState(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	eng, err := s.paymentEngine()
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "unavailable", "the financial runtime is not available")
		return
	}
	st, err := eng.RecoveryStatusFor(ctx, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "recovery query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recovery":    st,
		"resolutions": []string{"CONFIRMED_COMPLETED", "CONFIRMED_NOT_COMPLETED", "ABANDONED", "ESCALATED"},
		"note": "Recovery is left only when every held item has been reconciled. There is no force " +
			"release, and resolving a hold records what happened -- it never re-sends anything.",
	})
}

type recoveryHoldRow struct {
	ID          string `json:"hold_id"`
	Kind        string `json:"work_kind"`
	WorkID      string `json:"work_id"`
	HeldStatus  string `json:"held_status"`
	AmountMinor *int64 `json:"amount_minor"`
	Currency    string `json:"currency"`
	HeldAt      string `json:"held_at"`
}

func (s *server) listRecoveryHolds(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	eng, err := s.paymentEngine()
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "unavailable", "the financial runtime is not available")
		return
	}
	holds, err := eng.OpenHolds(ctx, s.tenantID, s.siteID, 200)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "holds query failed")
		return
	}
	out := make([]recoveryHoldRow, 0, len(holds))
	for _, h := range holds {
		out = append(out, recoveryHoldRow{ID: h.ID, Kind: h.Kind, WorkID: h.WorkID, HeldStatus: h.HeldStatus,
			AmountMinor: h.AmountMinor, Currency: h.Currency, HeldAt: h.HeldAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"holds": out, "limit": 200})
}

type resolveHoldReq struct {
	Resolution string `json:"resolution"`
	Note       string `json:"note"`
	Password   string `json:"password"`
}

func (s *server) resolveRecoveryHold(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var req resolveHoldReq
	if err := decodeJSON(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed body")
		return
	}
	// The actor is the authenticated session, never a body field; a self-named author makes the audit worthless.
	actor, ok := s.stepUpActor(w, r, req.Password)
	if !ok {
		return
	}
	eng, err := s.paymentEngine()
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "unavailable", "the financial runtime is not available")
		return
	}
	if err := eng.ResolveHold(ctx, chi.URLParam(r, "id"), req.Resolution, actor, req.Note); err != nil {
		financialOpErr(w, err)
		return
	}
	s.audit(r, "financial_recovery.hold_resolved", "recovery_hold", chi.URLParam(r, "id"),
		map[string]any{"resolution": req.Resolution, "note": req.Note})
	writeJSON(w, http.StatusOK, map[string]any{"resolved": true})
}

type releaseReq struct {
	Note     string `json:"note"`
	Password string `json:"password"`
}

func (s *server) releaseRecovery(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var req releaseReq
	if err := decodeJSON(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed body")
		return
	}
	actor, ok := s.stepUpActor(w, r, req.Password)
	if !ok {
		return
	}
	eng, err := s.paymentEngine()
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "unavailable", "the financial runtime is not available")
		return
	}
	epoch, err := eng.ReleaseRecovery(ctx, s.tenantID, s.siteID, actor, req.Note)
	if err != nil {
		financialOpErr(w, err)
		return
	}
	s.audit(r, "financial_recovery.released", "financial_recovery", s.siteID,
		map[string]any{"epoch": epoch, "note": req.Note})
	writeJSON(w, http.StatusOK, map[string]any{"released": true, "epoch": epoch})
}

// Re-authenticates the operator and returns their id from the SESSION. No request type carries an actor: the
// password proves the person at the keyboard still owns the session, which a stolen cookie alone does not.
func (s *server) stepUpActor(w http.ResponseWriter, r *http.Request, password string) (string, bool) {
	if !s.reauth(r, password) {
		s.reauthRefused(w, r, "password confirmation required")
		return "", false
	}
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "no operator session")
		return "", false
	}
	return sess.OperatorID, true
}

// paymentEngine builds the engine once per process and reuses it: its configuration comes from the process
// environment, which does not change while edged runs, and each construction could open a new outcome
// connection pool that was never closed (performance audit D1-L6). A failed construction is not kept, so the
// next request tries again and the DARK posture is still decided in NewProductionEngine alone.
func (s *server) paymentEngine() (*payment.Engine, error) {
	paymentEngineMu.Lock()
	defer paymentEngineMu.Unlock()
	if e, ok := paymentEngines[s.db]; ok {
		return e, nil
	}
	e, err := newPaymentEngine(s.db)
	if err != nil {
		return nil, err
	}
	paymentEngines[s.db] = e
	return e, nil
}

// newPaymentEngine is a variable only so a test can count constructions.
var newPaymentEngine = func(pool *pgxpool.Pool) (*payment.Engine, error) { return payment.NewProductionEngine(pool, nil) }

var (
	paymentEngineMu sync.Mutex
	paymentEngines  = map[*pgxpool.Pool]*payment.Engine{}
)

func financialOpErr(w http.ResponseWriter, err error) {
	switch payment.CodeOf(err) {
	case payment.ErrUntrustedInput:
		// The database's own words: deterministic RECOVERY_* codes, never guest data; hiding them makes a correctable mistake unfixable.
		jsonErr(w, http.StatusBadRequest, "invalid", err.Error())
	case payment.ErrRecoveryHeld:
		jsonErr(w, http.StatusConflict, "holds_unresolved",
			"recovery is not released while held financial work is unreconciled")
	case payment.ErrNotExecutable:
		jsonErr(w, http.StatusConflict, "already_resolved", "this item has already been decided")
	case payment.ErrRepo:
		jsonErr(w, http.StatusInternalServerError, "internal", "the operation failed")
	default:
		jsonErr(w, http.StatusBadRequest, "invalid", "the request could not be applied")
	}
}

// A posting restored with no surviving attempts cannot appear in the ordinary review queue (it keys on attempts)
// and record_posting_review_action refuses it. This surface is the way out. Authorization is the existing
// financial-review model: step-up for the write, actor from the session, bounded and screened reason/evidence.

type zeroAttemptRow struct {
	PostingID    string  `json:"posting_id"`
	OutboxID     string  `json:"outbox_id"`
	InterfaceID  string  `json:"pms_interface_id"`
	AmountMinor  int64   `json:"amount_minor"`
	Currency     string  `json:"currency"`
	Exponent     int16   `json:"currency_exponent"`
	HoldID       *string `json:"hold_id"`
	HoldResolved *string `json:"hold_resolution"`
	RetryAttempt *int    `json:"retry_authorized_attempt_no"`
	Eligible     bool    `json:"eligible_for_retry_authorization"`
}

// Eligibility comes from the database view, not recomputed here: a screen that disagreed with the function would send operators to be refused.
func (s *server) listZeroAttemptQueue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
SELECT posting_id::text, outbox_id::text, pms_interface_id::text, amount_minor, currency, currency_exponent,
       hold_id::text, hold_resolution, retry_authorized_attempt_no,
       coalesce(eligible_for_retry_authorization, false)
  FROM iam_v2.v_zero_attempt_recovery_queue
 WHERE tenant_id=$1 AND site_id=$2
 ORDER BY posting_id LIMIT 200`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []zeroAttemptRow{}
	for rows.Next() {
		var e zeroAttemptRow
		if err := rows.Scan(&e.PostingID, &e.OutboxID, &e.InterfaceID, &e.AmountMinor, &e.Currency,
			&e.Exponent, &e.HoldID, &e.HoldResolved, &e.RetryAttempt, &e.Eligible); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queue": out,
		"limit": 200,
		// The evidence contract travels with the queue so the frontend has no drifting copy.
		"evidence_contract": map[string]any{"source_types": evidenceSourceTypeNames()},
		"note": "These postings were held by recovery and have never been transmitted. Authorizing a retry " +
			"sends nothing: it authorises exactly ONE future attempt, and only after an operator has " +
			"established that the folio was never charged.",
		"eligibility": "A posting is eligible only when its recovery hold was reconciled as " +
			"CONFIRMED_NOT_COMPLETED and no retry has already been authorized.",
	})
}

type zeroAttemptRetryReq struct {
	Reason   string              `json:"reason"`
	Evidence reviewEvidenceInput `json:"evidence"`
	Password string              `json:"password"`
}

// The operator route to the audited path. It transmits nothing: the DB records the decision in the review
// ledger, authorizes attempt one and returns the posting to the queue; no attempt row or P# is created.
func (s *server) authorizeZeroAttemptRetry(w http.ResponseWriter, r *http.Request) {
	var in zeroAttemptRetryReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed body")
		return
	}
	reason, rerr := validateReason(in.Reason)
	if rerr != nil {
		jsonErr(w, http.StatusBadRequest, "reason_invalid", rerr.Error())
		return
	}
	// Evidence is required: the authorization rests on somebody having looked at the folio, so "I checked" is not auditable.
	evidence, everr := validateEvidence(in.Evidence, true)
	if everr != nil {
		jsonErr(w, http.StatusBadRequest, "evidence_invalid", everr.Error())
		return
	}
	actor, ok := s.stepUpActor(w, r, in.Password)
	if !ok {
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")

	// Scope first, by tenant AND site: an operator of one property must not authorize another's money. 404, not
	// 403, so the API does not confirm an out-of-scope posting exists.
	var owned bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam_v2.pms_postings
		WHERE id=$1 AND tenant_id=$2 AND site_id=$3)`, id, s.tenantID, s.siteID).Scan(&owned); err != nil || !owned {
		jsonErr(w, http.StatusNotFound, "not_found", "no such posting in this site")
		return
	}

	var actionID string
	if err := s.db.QueryRow(ctx,
		`SELECT iam_v2.p4_authorize_zero_attempt_retry($1::uuid,$2::uuid,$3,$4::jsonb)::text`,
		id, actor, reason, evidence).Scan(&actionID); err != nil {
		code, msg := classifyReviewError(err)
		jsonErr(w, code, msg, reviewErrorMessage(err))
		return
	}
	s.audit(r, "financial_recovery.retry_authorized", "pms_posting", id,
		map[string]any{"action_id": actionID, "reason": reason})
	writeJSON(w, http.StatusOK, map[string]any{
		"action_id":             actionID,
		"authorized_attempt_no": 1,
		"actor":                 actor,
		"transmitted":           false,
		"note": "One attempt is authorized. Nothing has been sent, no attempt record and no P# were created, " +
			"and the authorization is consumed by the attempt it authorises.",
	})
}
