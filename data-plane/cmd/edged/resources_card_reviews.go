package main

// CARD DISPUTES AND CHARGEBACKS ARE CASES (Product-Owner policy 2026-10-12, migration 0115).
//
// A chargeback, or a dispute the provider reports, never ends a client's access by itself. scd opens a case;
// an operator holding financial-review WRITE (the payments operator) resolves it once, explicitly:
//
//	KEEP_ACCESS  nothing else changes (for example, the dispute was won)
//	END_ACCESS   the access that charge paid for is ended (reason REVOKED), its devices and sessions closed
//
// Both take the operator's password and a reason, are recorded on the case by the definer and audited here.
// A full REFUND needs no case: the policy ends the access it paid for and records the evidence, listed here too.

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type cardCaseOut struct {
	ID               string     `json:"id"`
	Kind             string     `json:"kind"`
	SettlementID     string     `json:"settlement_id"`
	ChargeID         string     `json:"charge_transaction_id"`
	ProviderRef      string     `json:"provider_ref"`
	OpenedAt         time.Time  `json:"opened_at"`
	AmountMinor      int64      `json:"amount_minor"`
	Currency         string     `json:"currency"`
	EntitlementState string     `json:"entitlement_status,omitempty"`
	Resolution       string     `json:"resolution,omitempty"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy       string     `json:"resolved_by,omitempty"`
	ResolutionReason string     `json:"resolution_reason,omitempty"`
}

type refundEndOut struct {
	EntitlementID string    `json:"entitlement_id"`
	SettlementID  string    `json:"settlement_id"`
	CapturedMinor int64     `json:"captured_minor"`
	RefundedMinor int64     `json:"refunded_minor"`
	Currency      string    `json:"currency"`
	TerminatedAt  time.Time `json:"terminated_at"`
}

// GET /financial-review/card-cases
func (s *server) listCardCases(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
	  SELECT c.id::text, c.kind, c.settlement_id::text, c.charge_transaction_id::text, c.provider_ref, c.opened_at,
	         t.amount_minor, t.currency, COALESCE(e.status,''), COALESCE(c.resolution,''), c.resolved_at,
	         COALESCE(c.resolved_by,''), COALESCE(c.resolution_reason,'')
	    FROM iam_v2.card_reversal_reviews c
	    JOIN iam_v2.payment_transactions t ON t.id = c.charge_transaction_id
	    LEFT JOIN iam_v2.settlements se ON se.id = c.settlement_id
	    LEFT JOIN iam_v2.entitlements e ON e.purchase_id = se.purchase_id
	   WHERE c.tenant_id=$1 AND c.site_id=$2
	   ORDER BY (c.resolution IS NULL) DESC, c.opened_at DESC
	   LIMIT 200`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "card_cases_unreadable", err.Error())
		return
	}
	defer rows.Close()
	cases := []cardCaseOut{}
	for rows.Next() {
		var c cardCaseOut
		if err := rows.Scan(&c.ID, &c.Kind, &c.SettlementID, &c.ChargeID, &c.ProviderRef, &c.OpenedAt, &c.AmountMinor,
			&c.Currency, &c.EntitlementState, &c.Resolution, &c.ResolvedAt, &c.ResolvedBy, &c.ResolutionReason); err != nil {
			jsonErr(w, http.StatusInternalServerError, "card_cases_unreadable", err.Error())
			return
		}
		cases = append(cases, c)
	}
	refunds := []refundEndOut{}
	rr, err := s.db.Query(ctx, `
	  SELECT x.entitlement_id::text, x.settlement_id::text, x.captured_minor, x.refunded_minor, t.currency, x.terminated_at
	    FROM iam_v2.refund_access_terminations x JOIN iam_v2.payment_transactions t ON t.id = x.charge_transaction_id
	   WHERE x.tenant_id=$1 AND x.site_id=$2 ORDER BY x.terminated_at DESC LIMIT 200`, s.tenantID, s.siteID)
	if err == nil {
		defer rr.Close()
		for rr.Next() {
			var x refundEndOut
			if rr.Scan(&x.EntitlementID, &x.SettlementID, &x.CapturedMinor, &x.RefundedMinor, &x.Currency, &x.TerminatedAt) == nil {
				refunds = append(refunds, x)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cases": cases, "refund_terminations": refunds})
}

// POST /financial-review/card-cases/{id}/resolve {resolution, reason, password}
func (s *server) resolveCardCase(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Resolution string `json:"resolution"`
		Reason     string `json:"reason"`
		Password   string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if (in.Resolution != "KEEP_ACCESS" && in.Resolution != "END_ACCESS") || reason == "" || len(reason) > 500 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "resolution KEEP_ACCESS or END_ACCESS, and a reason, are required")
		return
	}
	id := chi.URLParam(r, "id")
	if !codeChallengeRE.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad id")
		return
	}
	actor, ok := s.paymentStepUp(w, r, in.Password)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var out string
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.p4_resolve_reversal_review($1,$2,$3::uuid,$4,$5,$6)`,
		s.tenantID, s.siteID, id, in.Resolution, actor, reason).Scan(&out); err != nil {
		jsonErr(w, http.StatusInternalServerError, "resolve_failed", err.Error())
		return
	}
	switch out {
	case "NOT_FOUND":
		jsonErr(w, http.StatusNotFound, "not_found", "no such case")
		return
	case "ALREADY_RESOLVED":
		jsonErr(w, http.StatusConflict, "already_resolved", "this case was already resolved")
		return
	}
	s.audit(r, "card_case.resolved", "card_reversal_review", id, map[string]any{"resolution": in.Resolution, "reason": reason})
	writeJSON(w, http.StatusOK, map[string]any{"resolution": out})
}
