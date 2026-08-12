package main

// Phase-4 Financial Manual Review — the operator surface required by FINAL Contract §15.
//
// DARK BY DEFAULT, exactly like the Phase-3 surface: these routes are mounted only when the Phase-4 master
// flag AND its admin flag are both ON. While dark the paths do not exist at all, rather than existing and
// answering "disabled" — an unmounted route cannot leak a financial schema that is not live yet.
//
// WHAT THIS ADDS OVER THE DATABASE WRITER. iam_v2.record_posting_review_action already enforces the action
// catalog, the action/state matrix, reviewer concurrency, single-use retry authorization and the immutable
// ledger. None of that is authorization. §15 additionally requires that a decision be made by an
// authenticated operator who holds `financial-review` write and who has just re-entered their password,
// with a mandatory reason and real evidence. That is what lives here:
//
//	authorization   resourcePermission("financial-review") + the role matrix in auth.go
//	identity        the actor is taken from the SESSION and never from the request body
//	step-up         s.reauth(r, password) against the operator's own stored hash
//	scope           every query is scoped by tenant AND SITE, not tenant alone
//
// The actor binding is the part worth being explicit about: the request body carries no actor field at
// all. There is nothing for a caller to spoof, because there is no parameter to put a forged identity in.
//
// SCOPE. Every financial query below filters on tenant_id AND site_id. An earlier version filtered on
// tenant alone, and its test happened to give each fixture a fresh tenant AND a fresh site — so it proved
// cross-TENANT isolation and silently proved nothing about two sites under one tenant, which is the
// arrangement a multi-property customer actually has. The tests now build exactly that case.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// reviewActions is the EXACT §15 catalog. There is no generic approve action, and this map is the only
// place the API will accept an action name from — an unknown action never reaches the database.
var reviewActions = map[string]struct {
	terminal        bool
	needsEvidence   bool
	needsAmountOpt  bool
	operatorSummary string
}{
	"CONFIRM_POSTED": {terminal: true, needsEvidence: true,
		operatorSummary: "The charge IS on the guest folio. External evidence is mandatory."},
	"CONFIRM_NOT_POSTED_RETRY": {terminal: true, needsEvidence: true,
		operatorSummary: "The charge is NOT on the folio. Authorises exactly ONE further attempt, reusing the same business idempotency key."},
	"CONFIRM_NOT_POSTED_ABANDON": {terminal: true, needsEvidence: true,
		operatorSummary: "The charge is NOT on the folio and will not be retried. The posting is final."},
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
	return r
}

// ---------------------------------------------------------------- catalog

type reviewActionDoc struct {
	Action        string `json:"action"`
	Terminal      bool   `json:"terminal"`
	NeedsEvidence bool   `json:"needs_evidence"`
	AcceptsAmount bool   `json:"accepts_amount"`
	Summary       string `json:"summary"`
}

// listReviewActionCatalog lets the operator UI render exactly the approved actions and their meaning,
// rather than hard-coding a second copy of the catalog in the frontend that could drift from §15.
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
		// The UI renders its evidence form from this, so the allowlist cannot drift between the two.
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
			"CREATE_REVERSAL records an audited ledger row only, and the folio correction is manual.",
	})
}

// ---------------------------------------------------------------- queue

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

// listReviewQueue returns the postings an operator has to act on: anything UNKNOWN and undecided, anything
// parked in HELD_RECOVERY, and anything already escalated. Scope is the site's tenant, always.
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

// ---------------------------------------------------------------- detail

type reviewAttempt struct {
	AttemptNo  int     `json:"attempt_no"`
	PNumber    string  `json:"p_number"`
	RN         string  `json:"rn"`
	GNumber    string  `json:"g_number"`
	Outcome    string  `json:"outcome"`
	PAStatus   *string `json:"pa_as_status"`
	SentAt     string  `json:"sent_at"`
	ResponseAt *string `json:"response_at"`
}

type reviewHistoryEntry struct {
	Action    string          `json:"action"`
	Actor     string          `json:"actor"`
	Reason    string          `json:"reason"`
	Evidence  json.RawMessage `json:"evidence"`
	CreatedAt string          `json:"created_at"`
}

// getReviewPosting returns everything §15 says an operator needs before deciding: the derived state, the
// PINNED evidence the charge was authorized against, the full attempt history with its P# and PA outcome,
// and the immutable review history.
//
// It deliberately returns NO connector secret, no secret-generation material and no guest personal data
// beyond the room and folio numbers that are the financial targeting evidence itself.
func (s *server) getReviewPosting(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()

	var (
		q                                     reviewQueueRow
		hasUnknown, retryConsumed             bool
		unknownCount, attemptCount            int64
		freshness, retryAuthNo, reversalID    *string
		settlementID, purchaseID, stayID      string
		folioID, revisionID, idempotencyKey   string
		interfaceKind, folioStrategy, ifState string
		settlementStatus, purchaseState       string
		escalations                           int
	)
	err := s.db.QueryRow(ctx, `SELECT `+reviewQueueCols+`,
		coalesce(p.has_unknown_history,false), coalesce(p.retry_authorization_consumed,false),
		coalesce(p.unknown_attempt_count,0), coalesce(p.attempt_count,0), p.freshness_block,
		p.retry_authorized_attempt_no::text, rs.reversal_posting_id::text, coalesce(p.escalation_count,0),
		o.settlement_id::text, o.purchase_id::text, coalesce(o.stay_id::text,''),
		coalesce(o.folio_id::text,''), o.posting_interface_revision_id::text, o.idempotency_key,
		i.connector_kind, rev.folio_identity_strategy, i.lifecycle_state,
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
			&settlementID, &purchaseID, &stayID, &folioID, &revisionID, &idempotencyKey,
			&interfaceKind, &folioStrategy, &ifState, &settlementStatus, &purchaseState)
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
		sent_at::text, response_at::text FROM iam_v2.posting_attempts
		WHERE internal_posting_id=$1 AND tenant_id=$2 AND site_id=$3 ORDER BY attempt_no`,
		id, s.tenantID, s.siteID)
	if err == nil {
		defer arows.Close()
		for arows.Next() {
			var a reviewAttempt
			if err := arows.Scan(&a.AttemptNo, &a.PNumber, &a.RN, &a.GNumber, &a.Outcome, &a.PAStatus,
				&a.SentAt, &a.ResponseAt); err == nil {
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
		// The PINNED evidence: the exact objects this charge was authorized against. An operator deciding
		// whether money moved needs to see what it was attached to, not the current state of the world.
		"pinned_evidence": map[string]any{
			"settlement_id": settlementID, "purchase_id": purchaseID, "stay_id": stayID,
			"folio_id": folioID, "posting_interface_revision_id": revisionID,
			"idempotency_key": idempotencyKey, "connector_kind": interfaceKind,
			"folio_identity_strategy": folioStrategy, "interface_lifecycle_state": ifState,
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
				"row only; the folio correction itself is a manual Front Office operation.",
			"CONFIRM_NOT_POSTED_RETRY authorises exactly ONE further attempt and is consumed by it.",
		},
	})
}

// availableActions narrows the catalog to what is actually applicable, so the UI never offers an operator
// a decision the database will refuse. It is a CONVENIENCE, not the enforcement: the database re-checks
// every one of these, and it is the database's answer that decides.
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

// ---------------------------------------------------------------- the decision

type reviewActionRequest struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	// Evidence is a CLOSED structured shape, not a blob. §15 requires it for every terminal action, and
	// §11 forbids secrets and card data in audit payloads — an append-only ledger cannot be redacted
	// afterwards, so the shape itself has to make a secret unrepresentable. See phase4_review_evidence.go.
	Evidence reviewEvidenceInput `json:"evidence"`
	// ExpectedVersion is the review version the operator's screen was rendered from. Supplying it turns
	// "two operators clicked at the same time" into a refusal for the second, rather than two decisions.
	ExpectedVersion *int `json:"expected_version"`
	// ReversalAmountMinor is meaningful only for CREATE_REVERSAL; nil reverses the whole charge.
	ReversalAmountMinor *int64 `json:"reversal_amount_minor"`
	// Password is the step-up. §15 requires password re-authentication for every review action.
	Password string `json:"password"`
	//
	// NOTE: there is deliberately NO actor field. The actor is the authenticated session, and a request
	// cannot name someone else.
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
	// STEP-UP. §15 requires password re-authentication for every review action, including ESCALATE: the
	// audit record names an operator, and it should not be possible to write one from a stolen cookie.
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}

	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()

	// Scope check BEFORE the decision: the posting must belong to this site's tenant. Without this, a
	// valid operator of one property could decide another property's money.
	var owned bool
	// tenant AND site: a valid operator of one property must not decide another property's money, even
	// when both properties belong to the same customer. The response is 404 rather than 403 so the API
	// does not confirm that an out-of-scope posting exists.
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam_v2.pms_postings
		WHERE id=$1 AND tenant_id=$2 AND site_id=$3)`, id, s.tenantID, s.siteID).Scan(&owned); err != nil || !owned {
		jsonErr(w, http.StatusNotFound, "not_found", "no such posting in this site")
		return
	}

	var actionID string
	err := s.db.QueryRow(ctx,
		`SELECT iam_v2.record_posting_review_action($1,$2,$3,$4,$5::jsonb,$6,$7)::text`,
		id, in.Action, sess.OperatorID, reason, evidence,
		in.ExpectedVersion, in.ReversalAmountMinor).Scan(&actionID)
	if err != nil {
		code, msg := classifyReviewError(err)
		jsonErr(w, code, msg, reviewErrorMessage(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"action_id": actionID,
		"action":    in.Action,
		"actor":     sess.OperatorID,
	})
}

// classifyReviewError maps the database's own financial refusals onto HTTP, so an operator sees a
// conflict as a conflict rather than as an opaque 500.
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

// reviewErrorMessage returns the database's own explanation, which names the rule that refused and is what
// an operator can act on. It carries no secret and no guest personal data — these refusals talk about
// attempt numbers, states and amounts.
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
