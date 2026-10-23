package main

// Financial manual review (contract §15). Mounted only when the Phase-4 master and admin flags are on; while dark
// the paths do not exist, so an unmounted route cannot leak a financial schema that is not live.
// The DB writer enforces the action catalog, state matrix, concurrency and the immutable ledger; none of that is
// authorization. Here: resourcePermission("financial-review"), the actor taken from the SESSION (the body has no
// actor field, so nothing can be spoofed), s.reauth step-up, and every query scoped by tenant AND site
// (tenant-only scoping would leave two sites under one tenant unisolated).

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// The exact §15 catalog and the only place an action name is accepted; an unknown action never reaches the database.
var reviewActions = map[string]struct {
	terminal        bool
	needsEvidence   bool
	needsAmountOpt  bool
	operatorSummary string
}{
	"CONFIRM_POSTED": {terminal: true, needsEvidence: true,
		operatorSummary: "The charge IS on the guest's reservation in the PMS. External evidence is mandatory."},
	"CONFIRM_NOT_POSTED_RETRY": {terminal: true, needsEvidence: true,
		operatorSummary: "The charge is NOT on the reservation in the PMS. Authorises exactly ONE further attempt, reusing the same business idempotency key."},
	"CONFIRM_NOT_POSTED_ABANDON": {terminal: true, needsEvidence: true,
		operatorSummary: "The charge is NOT on the reservation in the PMS and will not be retried. The posting is final."},
	"CREATE_REVERSAL": {terminal: true, needsEvidence: true, needsAmountOpt: true,
		operatorSummary: "Records a PASSIVE reversal ledger row referencing the original. It is NOT sent to the PMS: " +
			"programmatic reversal is capability=false in v1, so the actual correction is a manual Front Office operation."},
	"ESCALATE": {terminal: false, needsEvidence: false,
		operatorSummary: "Raises the posting for a second opinion. Decides nothing and may be repeated."},
}

func (s *server) financialReviewRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/queue", s.listReviewQueue)
	r.Get("/actions", s.listReviewActionCatalog)
	r.Get("/postings/{id}", s.getReviewPosting)
	r.Post("/postings/{id}/actions", s.postReviewAction)
	r.Get("/card-cases", s.listCardCases)
	r.Post("/card-cases/{id}/resolve", s.resolveCardCase)
	return r
}

// Card-dispute part of financial review alone, mounted when the PMS review surface is off: cases would
// otherwise be unreachable, so a chargeback could be neither kept nor ended.
func (s *server) cardCasesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/card-cases", s.listCardCases)
	r.Post("/card-cases/{id}/resolve", s.resolveCardCase)
	return r
}

type reviewActionDoc struct {
	Action        string `json:"action"`
	Terminal      bool   `json:"terminal"`
	NeedsEvidence bool   `json:"needs_evidence"`
	AcceptsAmount bool   `json:"accepts_amount"`
	Summary       string `json:"summary"`
}

func (s *server) listReviewActionCatalog(w http.ResponseWriter, r *http.Request) {
	out := make([]reviewActionDoc, 0, len(reviewActions))
	for _, name := range []string{"CONFIRM_POSTED", "CONFIRM_NOT_POSTED_RETRY", "CONFIRM_NOT_POSTED_ABANDON",
		"CREATE_REVERSAL", "ESCALATE"} {
		a := reviewActions[name]
		out = append(out, reviewActionDoc{Action: name, Terminal: a.terminal, NeedsEvidence: a.needsEvidence,
			AcceptsAmount: a.needsAmountOpt, Summary: a.operatorSummary})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"actions": out,
		"evidence_contract": map[string]any{
			"source_types":  evidenceSourceTypes,
			"max_reference": maxEvidenceReference,
			"max_note":      maxEvidenceNote,
			"max_reason":    maxReviewReason,
			"rejects": "credentials, tokens, API keys, card data, raw provider payloads and raw PMS frames, " +
				"in BOTH the evidence fields and the reason. Financial review evidence is an immutable audit " +
				"record: record a REFERENCE to the artefact, never its contents.",
			"guarantee": "structurally closed (no free-form member), length-bounded, single-line, and " +
				"heuristically screened for recognisable secret shapes. Screening is heuristic, not a proof.",
		},
		"note": "There is no generic approve action. Programmatic PMS reversal is disabled in v1: " +
			"CREATE_REVERSAL records an audited ledger row only, and the correction in the PMS is manual.",
	})
}

type reviewQueueRow struct {
	PostingID       string  `json:"posting_id"`
	InterfaceID     string  `json:"pms_interface_id"`
	ExecutionState  string  `json:"execution_state"`
	AmountMinor     int64   `json:"amount_minor"`
	Currency        string  `json:"currency"`
	Exponent        int16   `json:"currency_exponent"`
	LatestAttemptNo *int    `json:"latest_attempt_no"`
	LatestPNumber   *string `json:"latest_p_number"`
	LatestPAStatus  *string `json:"latest_pa_as_status"`
	OutboxState     *string `json:"outbox_state"`
	ReviewVersion   *int    `json:"review_version"`
	TerminalAction  *string `json:"terminal_review_action"`
	AwaitingReview  bool    `json:"awaiting_manual_review"`
	CreatedAt       string  `json:"created_at"`
}

const reviewQueueCols = `p.posting_id::text, p.pms_interface_id::text, p.execution_state, p.amount_minor,
	p.currency, p.currency_exponent, p.latest_attempt_no, p.latest_p_number, p.latest_pa_as_status,
	p.outbox_state, p.review_version, p.terminal_review_action,
	coalesce(p.awaiting_manual_review,false), p.created_at::text`

// Scope is the site's tenant, always.
func (s *server) listReviewQueue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var ifaceArg any
	if v := strings.TrimSpace(r.URL.Query().Get("pms_interface_id")); v != "" {
		ifaceArg = v
	}
	rows, err := s.db.Query(ctx, `SELECT `+reviewQueueCols+`
		FROM iam_v2.posting_execution_state p
		WHERE p.tenant_id=$1 AND p.site_id=$2
		  AND ($3::uuid IS NULL OR p.pms_interface_id=$3::uuid)
		  AND (coalesce(p.awaiting_manual_review,false)
		       OR p.outbox_state='HELD_RECOVERY'
		       OR coalesce(p.escalation_count,0) > 0)
		ORDER BY p.created_at ASC LIMIT 200`, s.tenantID, s.siteID, ifaceArg)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []reviewQueueRow{}
	for rows.Next() {
		var e reviewQueueRow
		if err := rows.Scan(&e.PostingID, &e.InterfaceID, &e.ExecutionState, &e.AmountMinor, &e.Currency,
			&e.Exponent, &e.LatestAttemptNo, &e.LatestPNumber, &e.LatestPAStatus, &e.OutboxState,
			&e.ReviewVersion, &e.TerminalAction, &e.AwaitingReview, &e.CreatedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type reviewAttempt struct {
	AttemptNo  int     `json:"attempt_no"`
	PNumber    string  `json:"p_number"`
	RN         string  `json:"rn"`
	GNumber    string  `json:"g_number"`
	Outcome    string  `json:"outcome"`
	PAStatus   *string `json:"pa_as_status"`
	SentAt     string  `json:"sent_at"`
	ResponseAt *string `json:"response_at"`
	// pmsd's bounded reason when the attempt was NOT_SENT (e.g. ROOM_CHANGED).
	NotSentReason *string `json:"not_sent_reason,omitempty"`
}

type reviewHistoryEntry struct {
	Action    string          `json:"action"`
	Actor     string          `json:"actor"`
	Reason    string          `json:"reason"`
	Evidence  json.RawMessage `json:"evidence"`
	CreatedAt string          `json:"created_at"`
}

// Returns no connector secret, no secret-generation material and no guest personal data beyond the room and folio numbers that are the targeting evidence.
func (s *server) getReviewPosting(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()

	var (
		q                                   reviewQueueRow
		hasUnknown, retryConsumed           bool
		unknownCount, attemptCount          int64
		freshness, retryAuthNo, reversalID  *string
		settlementID, purchaseID, stayID    string
		gNumber, revisionID, idempotencyKey string
		interfaceKind, targetModel, ifState string
		settlementStatus, purchaseState     string
		escalations                         int
	)
	err := s.db.QueryRow(ctx, `SELECT `+reviewQueueCols+`,
		coalesce(p.has_unknown_history,false), coalesce(p.retry_authorization_consumed,false),
		coalesce(p.unknown_attempt_count,0), coalesce(p.attempt_count,0), p.freshness_block,
		p.retry_authorized_attempt_no::text, rs.reversal_posting_id::text, coalesce(p.escalation_count,0),
		o.settlement_id::text, o.purchase_id::text, coalesce(o.stay_id::text,''),
		o.g_number, o.posting_interface_revision_id::text, o.idempotency_key,
		i.connector_kind, rev.posting_target_model, i.lifecycle_state,
		se.status, pu.state
		FROM iam_v2.posting_execution_state p
		JOIN iam_v2.pms_postings o ON o.id = p.posting_id
		JOIN iam_v2.pms_interfaces i ON i.id = o.pms_interface_id
		JOIN iam_v2.pms_interface_revisions rev ON rev.id = o.posting_interface_revision_id
		JOIN iam_v2.settlements se ON se.id = o.settlement_id
		JOIN iam_v2.purchases pu ON pu.id = o.purchase_id
		LEFT JOIN iam_v2.posting_review_state rs ON rs.posting_id = p.posting_id
		WHERE p.posting_id=$1 AND p.tenant_id=$2 AND p.site_id=$3`, id, s.tenantID, s.siteID).
		Scan(&q.PostingID, &q.InterfaceID, &q.ExecutionState, &q.AmountMinor, &q.Currency, &q.Exponent,
			&q.LatestAttemptNo, &q.LatestPNumber, &q.LatestPAStatus, &q.OutboxState, &q.ReviewVersion,
			&q.TerminalAction, &q.AwaitingReview, &q.CreatedAt,
			&hasUnknown, &retryConsumed, &unknownCount, &attemptCount, &freshness,
			&retryAuthNo, &reversalID, &escalations,
			&settlementID, &purchaseID, &stayID, &gNumber, &revisionID, &idempotencyKey,
			&interfaceKind, &targetModel, &ifState, &settlementStatus, &purchaseState)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such posting in this site")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	attempts := []reviewAttempt{}
	arows, err := s.db.Query(ctx, `SELECT attempt_no, p_number, rn, g_number, outcome, pa_as_status,
		sent_at::text, response_at::text,
		CASE WHEN outcome = 'NOT_SENT' THEN iam_v2.p4_attempt_not_sent_reason(id) END FROM iam_v2.posting_attempts
		WHERE internal_posting_id=$1 AND tenant_id=$2 AND site_id=$3 ORDER BY attempt_no`,
		id, s.tenantID, s.siteID)
	if err == nil {
		defer arows.Close()
		for arows.Next() {
			var a reviewAttempt
			if err := arows.Scan(&a.AttemptNo, &a.PNumber, &a.RN, &a.GNumber, &a.Outcome, &a.PAStatus,
				&a.SentAt, &a.ResponseAt, &a.NotSentReason); err == nil {
				attempts = append(attempts, a)
			}
		}
	}

	history := []reviewHistoryEntry{}
	hrows, err := s.db.Query(ctx, `SELECT action, actor::text, reason, evidence, created_at::text
		FROM iam_v2.posting_review_actions WHERE posting_id=$1 AND tenant_id=$2 AND site_id=$3
		ORDER BY created_at`, id, s.tenantID, s.siteID)
	if err == nil {
		defer hrows.Close()
		for hrows.Next() {
			var h reviewHistoryEntry
			if err := hrows.Scan(&h.Action, &h.Actor, &h.Reason, &h.Evidence, &h.CreatedAt); err == nil {
				history = append(history, h)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"posting": q,
		// The exact objects the charge was authorized against, not the current state of the world.
		"pinned_evidence": map[string]any{
			"settlement_id": settlementID, "purchase_id": purchaseID, "stay_id": stayID,
			"g_number": gNumber, "posting_interface_revision_id": revisionID,
			"idempotency_key": idempotencyKey, "connector_kind": interfaceKind,
			"posting_target_model": targetModel, "interface_lifecycle_state": ifState,
			"settlement_status": settlementStatus, "purchase_state": purchaseState,
		},
		"attempts": attempts,
		"review": map[string]any{
			"history": history, "version": q.ReviewVersion, "terminal_action": q.TerminalAction,
			"escalation_count": escalations, "retry_authorized_attempt_no": retryAuthNo,
			"retry_authorization_consumed": retryConsumed, "reversal_posting_id": reversalID,
		},
		"diagnostics": map[string]any{
			"attempt_count": attemptCount, "unknown_attempt_count": unknownCount,
			"has_unknown_history": hasUnknown, "interface_freshness_block": freshness,
		},
		"available_actions": s.availableActions(q, attempts),
		"limitations": []string{
			"Programmatic PMS reversal is capability=false in v1. CREATE_REVERSAL records an audited ledger " +
				"row only; the correction in the PMS itself is a manual Front Office operation.",
			"CONFIRM_NOT_POSTED_RETRY authorises exactly ONE further attempt and is consumed by it.",
		},
	})
}

// A convenience so the UI never offers a refused decision; the database re-checks every one and its answer decides.
func (s *server) availableActions(q reviewQueueRow, attempts []reviewAttempt) []string {
	if len(attempts) == 0 {
		return []string{"ESCALATE"} // nothing has been transmitted, so there is nothing to decide about
	}
	if q.TerminalAction != nil {
		return []string{"ESCALATE"} // already decided; only a second opinion remains
	}
	last := attempts[len(attempts)-1]
	if last.Outcome == "SENDING" {
		return []string{"ESCALATE"} // its outcome is not yet known
	}
	out := []string{"ESCALATE", "CONFIRM_POSTED", "CONFIRM_NOT_POSTED_ABANDON"}
	ackedOK := last.Outcome == "ACKED" && last.PAStatus != nil && *last.PAStatus == "OK"
	if !ackedOK {
		// retrying a charge the PMS acknowledged would post it twice
		out = append(out, "CONFIRM_NOT_POSTED_RETRY")
	}
	if last.Outcome == "UNKNOWN" || ackedOK {
		// something is believed to be on the folio, so a correction is meaningful
		out = append(out, "CREATE_REVERSAL")
	}
	return out
}

type reviewActionRequest struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	// Evidence is a closed structured shape: the append-only ledger cannot be redacted afterwards, so the shape must
	// make a secret or card data unrepresentable (phase4_review_evidence.go).
	Evidence reviewEvidenceInput `json:"evidence"`
	// The review version the screen was rendered from; a second concurrent operator is refused instead of deciding twice.
	ExpectedVersion *int `json:"expected_version"`
	// ReversalAmountMinor is meaningful only for CREATE_REVERSAL; nil reverses the whole charge.
	ReversalAmountMinor *int64 `json:"reversal_amount_minor"`
	// Password is the step-up (§15).
	Password string `json:"password"`
	// There is deliberately no actor field: the actor is the session.
}

func (s *server) postReviewAction(w http.ResponseWriter, r *http.Request) {
	sess := sessFrom(r.Context())
	if sess == nil {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	var in reviewActionRequest
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	spec, ok := reviewActions[in.Action]
	if !ok {
		jsonErr(w, http.StatusBadRequest, "unknown_action",
			"not in the approved review catalog; there is no generic approve action")
		return
	}
	reason, rErr := validateReason(in.Reason)
	if rErr != nil {
		jsonErr(w, http.StatusBadRequest, "reason_invalid", rErr.Error())
		return
	}
	evidence, evErr := validateEvidence(in.Evidence, spec.needsEvidence)
	if evErr != nil {
		jsonErr(w, http.StatusBadRequest, "evidence_invalid", evErr.Error())
		return
	}
	if in.ReversalAmountMinor != nil && in.Action != "CREATE_REVERSAL" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "only CREATE_REVERSAL carries an amount")
		return
	}
	// Step-up applies to every action including ESCALATE, so the audit record cannot be written from a stolen cookie.
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}

	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()

	// Scope check before the decision, by tenant AND site: an operator of one property must not decide another's money.
	var owned bool
	// 404, not 403, so the API does not confirm an out-of-scope posting exists.
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam_v2.pms_postings
		WHERE id=$1 AND tenant_id=$2 AND site_id=$3)`, id, s.tenantID, s.siteID).Scan(&owned); err != nil || !owned {
		jsonErr(w, http.StatusNotFound, "not_found", "no such posting in this site")
		return
	}

	// The decision and its consequence commit in one transaction: a terminal decision on a room-charge posting moves
	// the settlement (CONFIRM_POSTED settles and grants, ABANDON fails it, RETRY requeues the authorised attempt).
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the decision could not be recorded")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var actionID string
	err = tx.QueryRow(ctx,
		`SELECT iam_v2.record_posting_review_action($1,$2,$3,$4,$5::jsonb,$6,$7)::text`,
		id, in.Action, sess.OperatorID, reason, evidence,
		in.ExpectedVersion, in.ReversalAmountMinor).Scan(&actionID)
	if err != nil {
		code, msg := classifyReviewError(err)
		jsonErr(w, code, msg, reviewErrorMessage(err))
		return
	}
	var applied string
	if err := tx.QueryRow(ctx, `SELECT iam_v2.p4_posting_review_apply($1::uuid,$2::uuid,$3::uuid)`,
		s.tenantID, s.siteID, id).Scan(&applied); err != nil {
		code, msg := classifyReviewError(err)
		jsonErr(w, code, msg, reviewErrorMessage(err))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the decision could not be recorded")
		return
	}
	// Also written to the console audit log, so a money decision does not appear in only one of the two trails.
	s.audit(r, "financial_review.decided", "pms_posting", id, map[string]any{
		"action": in.Action, "action_id": actionID, "reason": reason, "applied": applied,
		"expected_version": in.ExpectedVersion, "reversal_amount_minor": in.ReversalAmountMinor,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"action_id": actionID,
		"action":    in.Action,
		"actor":     sess.OperatorID,
		"applied":   applied,
	})
}

func classifyReviewError(err error) (int, string) {
	m := err.Error()
	switch {
	case strings.Contains(m, "REVIEW_VERSION_STALE"):
		return http.StatusConflict, "stale_version"
	case strings.Contains(m, "REVIEW_CONFLICT"), strings.Contains(m, "REVIEW_ALREADY_DECIDED"):
		return http.StatusConflict, "already_decided"
	case strings.Contains(m, "REVIEW_RETRY_REFUSED"), strings.Contains(m, "REVIEW_REVERSAL_REFUSED"),
		strings.Contains(m, "REVIEW_NOT_APPLICABLE"):
		return http.StatusUnprocessableEntity, "not_applicable"
	case strings.Contains(m, "REVIEW_EVIDENCE_REQUIRED"), strings.Contains(m, "REVIEW_ACTOR_REASON_REQUIRED"):
		return http.StatusBadRequest, "evidence_required"
	case strings.Contains(m, "REVERSAL_EXCEEDS_CHARGE"):
		return http.StatusUnprocessableEntity, "reversal_exceeds_charge"
	case strings.Contains(m, "REVIEW_ACTION_UNKNOWN"):
		return http.StatusBadRequest, "unknown_action"
	}
	return http.StatusInternalServerError, "internal"
}

// The database's explanation names the rule that refused; it carries no secret or guest data (attempt numbers, states, amounts).
func reviewErrorMessage(err error) string {
	m := err.Error()
	if i := strings.Index(m, "ERROR: "); i >= 0 {
		m = m[i+len("ERROR: "):]
	}
	if i := strings.Index(m, " (SQLSTATE"); i >= 0 {
		m = m[:i]
	}
	if len(m) > 300 {
		m = m[:300]
	}
	return m
}
