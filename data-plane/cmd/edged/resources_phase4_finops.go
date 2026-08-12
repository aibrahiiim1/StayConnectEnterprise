package main

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

// PHASE-4 FINANCIAL OPERATIONS API (WS-I read surface + WS-H operator actions).
//
// The Manual Review routes next door decide about ONE posting. These routes are about the financial
// subsystem as a whole: is it healthy, what has been paid, and -- after a restore -- what is being held and
// who releases it.
//
// AUTHORIZATION. Mounted under resourcePermission("financial-review"), the same permission the review
// surface uses, because the readership is the same: the people who reconcile money. The two RECOVERY
// actions additionally require a password step-up, for the same reason a review decision does -- they are
// assertions about what happened to real money, made by a named person.
//
// REDACTION. Every response here is either a bounded projection or the health struct, which cannot carry
// identifiers by construction. The payment history rows deliberately omit provider_ref and idempotency_key:
// an operator reconciling a settlement needs to know an amount was captured, not the correlation handle
// that would let them act on it out of band.
//
// DARK. These routes mount only when the Phase-4 master and review flags are on, exactly like the review
// routes, so on the delivered appliance they do not exist.
func (s *server) financialOpsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", s.getFinancialHealth)
	r.Get("/settlements", s.listSettlements)
	r.Get("/settlements/{id}", s.getSettlement)
	r.Get("/recovery", s.getRecoveryState)
	r.Get("/recovery/holds", s.listRecoveryHolds)
	r.Post("/recovery/holds/{id}/resolve", s.resolveRecoveryHold)
	r.Post("/recovery/release", s.releaseRecovery)
	return r
}

// ---------------------------------------------------------------- health

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

// ---------------------------------------------------------------- settlements and payments

type settlementRow struct {
	SettlementID  string `json:"settlement_id"`
	PurchaseID    string `json:"purchase_id"`
	Method        string `json:"method"`
	Status        string `json:"status"`
	PurchaseState string `json:"purchase_state"`
	AmountMinor   int64  `json:"amount_minor"`
	Currency      string `json:"currency"`
	Exponent      int16  `json:"currency_exponent"`
}

func (s *server) listSettlements(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var statusArg any
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		statusArg = v
	}
	rows, err := s.db.Query(ctx, `
SELECT settlement_id::text, purchase_id::text, method, status, purchase_state,
       coalesce(amount_minor,0), coalesce(currency,''), coalesce(currency_exponent,0)
  FROM iam_v2.v_financial_settlements
 WHERE tenant_id=$1 AND site_id=$2 AND ($3::text IS NULL OR status=$3::text)
 ORDER BY status, settlement_id LIMIT 200`, s.tenantID, s.siteID, statusArg)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []settlementRow{}
	for rows.Next() {
		var e settlementRow
		if err := rows.Scan(&e.SettlementID, &e.PurchaseID, &e.Method, &e.Status, &e.PurchaseState,
			&e.AmountMinor, &e.Currency, &e.Exponent); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"settlements": out, "limit": 200})
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
}

// getSettlement returns one settlement with its payment history: the charge, and every refund or chargeback
// that followed. This is the view an operator needs to answer "was this guest actually charged, and what
// has been given back".
func (s *server) getSettlement(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	var se settlementRow
	// Scoped by tenant AND site. A settlement id from another property of the same customer must read as
	// absent, not as forbidden -- the response is identical either way.
	err := s.db.QueryRow(ctx, `
SELECT settlement_id::text, purchase_id::text, method, status, purchase_state,
       coalesce(amount_minor,0), coalesce(currency,''), coalesce(currency_exponent,0)
  FROM iam_v2.v_financial_settlements
 WHERE tenant_id=$1 AND site_id=$2 AND settlement_id=$3::uuid`, s.tenantID, s.siteID, id).
		Scan(&se.SettlementID, &se.PurchaseID, &se.Method, &se.Status, &se.PurchaseState,
			&se.AmountMinor, &se.Currency, &se.Exponent)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"settlement": se,
		"payments":   pay,
		// The UI renders its affordances from this rather than deciding for itself what Phase 4 allows.
		"available_actions": []string{},
		"note": "Refund and chargeback initiation are NOT available from this surface in Phase 4. The " +
			"backend can record them, but no provider adapter exists and no operator-initiated refund " +
			"path has been authorized, so offering the button would imply a capability that is not there.",
	})
}

// ---------------------------------------------------------------- recovery

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
	// The actor is the AUTHENTICATED session, never a field in the body. A reconciliation decision that
	// could name its own author would be worthless as an audit record.
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
	writeJSON(w, http.StatusOK, map[string]any{"released": true, "epoch": epoch})
}

// ---------------------------------------------------------------- helpers

// stepUpActor re-authenticates the operator and returns their id from the SESSION.
//
// There is deliberately no actor parameter anywhere in this file's request types. An audit record whose
// author is supplied by the request is not an audit record, and the password is a step-up on top of an
// already-authenticated session -- it proves the person at the keyboard is still the session's owner, which
// a stolen cookie alone does not.
func (s *server) stepUpActor(w http.ResponseWriter, r *http.Request, password string) (string, bool) {
	if !s.reauth(r, password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return "", false
	}
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "no operator session")
		return "", false
	}
	return sess.OperatorID, true
}

// paymentEngine builds the payment runtime over this server's pool.
//
// It is constructed per request rather than held on the server because the engine is a thin coordinator
// with no state of its own, and because building it here keeps the DARK posture in one place: the same
// NewProductionEngine every other caller uses, reading the same environment.
func (s *server) paymentEngine() (*payment.Engine, error) {
	return payment.NewProductionEngine(s.db, nil)
}

// financialOpErr maps the payment package's typed refusals onto HTTP without leaking internals.
func financialOpErr(w http.ResponseWriter, err error) {
	switch payment.CodeOf(err) {
	case payment.ErrUntrustedInput:
		jsonErr(w, http.StatusBadRequest, "invalid", "the decision is missing something it requires")
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
