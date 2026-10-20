package main

// ASSISTED GUEST DATA REQUESTS (migration 0146): staff-supervised export and anonymisation of internet-usage
// records that no automatic rule can attribute to one person.
//
//	GET  /edge/v1/guest-data-rights/assisted                  the requests (open ones and the closed record)
//	POST /edge/v1/guest-data-rights/assisted                  open one: addresses, range, the reservation/guest/account it is about,
//	                                                          how the requester was verified, three statements   reason
//	GET  /edge/v1/guest-data-rights/assisted/{id}             one request
//	GET  /edge/v1/guest-data-rights/assisted/{id}/review      every record of the addresses in the range, with its class and
//	                                                          (for the ones that can be selected) exactly what it would disclose
//	POST /edge/v1/guest-data-rights/assisted/{id}/export      hand over the selected records            password + reason + reference
//	POST /edge/v1/guest-data-rights/assisted/{id}/erase       anonymise the selected records            password + reason + reference
//	POST /edge/v1/guest-data-rights/assisted/{id}/close       complete (after a hand-over) or cancel
//
// Same resource key as the automatic requests (guest-data-rights: site_admin and the IT manager). All the deciding
// is in the database: which records belong to the stated context, which are shared or in conflict, what a record
// would disclose, and whether anything changed since staff reviewed. This file validates the shape of a request,
// asks for the password, records who did what, and passes the selection through.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var assistedAnchorKinds = map[string]bool{"account": true, "principal": true, "stay": true, "stay_guest": true}

var assistedRecordKinds = map[string]bool{
	"session": true, "sign_in_attempt": true, "verification_access": true, "code_device_binding": true,
	"sign_in_restriction": true, "sign_in_pass": true, "sign_in_redirect": true, "device": true,
}

var assistedMethods = map[string]bool{
	"ID_CHECKED_IN_PERSON": true, "ACCOUNT_CONTROL_SHOWN": true, "CALLBACK_TO_KNOWN_CONTACT": true, "OTHER_DOCUMENTED": true,
}

const assistedMaxSelected = 2000

type assistedRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// assistedBodyLimit bounds every body on these routes (a selection of 2,000 records is about 200 KB).
func assistedBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
		next.ServeHTTP(w, r)
	})
}

func (s *server) assistedRoutes(parent chi.Router) {
	parent.Group(func(r chi.Router) {
		r.Use(assistedBodyLimit)
		r.Get("/assisted", s.listAssisted)
		r.Post("/assisted", s.openAssisted)
		r.Get("/assisted/{id}", s.getAssisted)
		r.Get("/assisted/{id}/review", s.reviewAssisted)
		r.Post("/assisted/{id}/export", s.exportAssisted)
		r.Post("/assisted/{id}/erase", s.eraseAssisted)
		r.Post("/assisted/{id}/close", s.closeAssisted)
	})
}

// rawJSON answers with a JSON document the database built.
func rawJSON(w http.ResponseWriter, code int, doc []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(doc)
}

func (s *server) assistedReady(w http.ResponseWriter) bool {
	if s.tenantID == "" || s.siteID == "" {
		jsonErr(w, http.StatusConflict, "awaiting_assignment", "this appliance has no site yet")
		return false
	}
	return true
}

func (s *server) listAssisted(w http.ResponseWriter, r *http.Request) {
	if !s.assistedReady(w) {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var doc []byte
	if err := s.db.QueryRow(ctx, `SELECT jsonb_build_object('requests', iam_v2.assisted_request_list($1::uuid,$2::uuid))::text`,
		s.tenantID, s.siteID).Scan(&doc); err != nil {
		assistedDBError(w, err, "the requests could not be read")
		return
	}
	rawJSON(w, http.StatusOK, doc)
}

func (s *server) getAssisted(w http.ResponseWriter, r *http.Request) {
	if !s.assistedReady(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "invalid_id", "id must be a request id")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var doc []byte
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.assisted_request_get($1::uuid,$2::uuid,$3::uuid)::text`, s.tenantID, s.siteID, id).Scan(&doc); err != nil {
		assistedDBError(w, err, "the request could not be read")
		return
	}
	rawJSON(w, http.StatusOK, doc)
}

func (s *server) reviewAssisted(w http.ResponseWriter, r *http.Request) {
	if !s.assistedReady(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "invalid_id", "id must be a request id")
		return
	}
	// A review shows what a hand-over would contain: it is recorded first, and refused if it cannot be.
	if err := s.auditErr(r, "guest_data.assisted_reviewed", "assisted_guest_request", id, nil); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the review could not be recorded, so it was not shown")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), guestDataEraseTimeout)
	defer cancel()
	var doc []byte
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.assisted_request_review_json($1::uuid,$2::uuid,$3::uuid)::text`, s.tenantID, s.siteID, id).Scan(&doc); err != nil {
		assistedDBError(w, err, "the review failed")
		return
	}
	rawJSON(w, http.StatusOK, doc)
}

type assistedOpenBody struct {
	Purpose            string        `json:"purpose"`
	Addresses          []string      `json:"addresses"`
	RangeFrom          string        `json:"range_from"`
	RangeTo            string        `json:"range_to"`
	Anchors            []assistedRef `json:"anchors"`
	Requester          string        `json:"requester"`
	VerificationMethod string        `json:"verification_method"`
	EvidenceNote       string        `json:"evidence_note"`
	Statements         struct {
		IdentityVerified        bool `json:"identity_verified"`
		DeviceShown             bool `json:"device_shown"`
		SoleUse                 bool `json:"sole_use"`
		PrivateAddressConfirmed bool `json:"private_address_confirmed"`
	} `json:"statements"`
	Reason string `json:"reason"`
}

func (s *server) openAssisted(w http.ResponseWriter, r *http.Request) {
	if !s.assistedReady(w) {
		return
	}
	var in assistedOpenBody
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if in.Purpose != "ACCESS" && in.Purpose != "ERASURE" {
		jsonErr(w, http.StatusBadRequest, "invalid_purpose", "purpose must be ACCESS or ERASURE")
		return
	}
	if len(in.Addresses) < 1 || len(in.Addresses) > 5 {
		jsonErr(w, http.StatusBadRequest, "invalid_addresses", "give 1 to 5 device addresses")
		return
	}
	from, err1 := time.Parse(time.RFC3339, in.RangeFrom)
	to, err2 := time.Parse(time.RFC3339, in.RangeTo)
	if err1 != nil || err2 != nil {
		jsonErr(w, http.StatusBadRequest, "invalid_range", "the range needs a start and an end")
		return
	}
	if len(in.Anchors) < 1 || len(in.Anchors) > 5 {
		jsonErr(w, http.StatusBadRequest, "invalid_anchors", "name the reservation, guest or account the records are about: a device address alone identifies nobody")
		return
	}
	for _, a := range in.Anchors {
		if !assistedAnchorKinds[a.Kind] || !uuidRe.MatchString(a.ID) {
			jsonErr(w, http.StatusBadRequest, "invalid_anchors", "each must be an account, identity, stay or stay guest from the search")
			return
		}
	}
	if !assistedMethods[in.VerificationMethod] {
		jsonErr(w, http.StatusBadRequest, "invalid_method", "say how the requester was verified")
		return
	}
	if !in.Statements.IdentityVerified {
		jsonErr(w, http.StatusBadRequest, "not_verified", "verify the requester before opening a request")
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", reasonBoundsMessage)
		return
	}
	actor := protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the request could not be attributed to an operator")
		return
	}
	anchors, _ := json.Marshal(in.Anchors)
	ctx, cancel := dbCtx(r)
	defer cancel()
	var doc []byte
	err := s.db.QueryRow(ctx, `SELECT public.assisted_request_open($1::uuid,$2::uuid,$3,$4,$5::text[],$6::timestamptz,$7::timestamptz,$8::jsonb,$9,$10,$11,
	      $12,$13,$14,$15,$16)::text`,
		s.tenantID, s.siteID, actor, in.Purpose, in.Addresses, from, to, string(anchors), in.Requester, in.VerificationMethod,
		in.EvidenceNote, in.Statements.IdentityVerified, in.Statements.DeviceShown, in.Statements.SoleUse,
		in.Statements.PrivateAddressConfirmed, in.Reason).Scan(&doc)
	if err != nil {
		assistedDBError(w, err, "the request could not be opened")
		return
	}
	var res struct {
		ID  string `json:"id"`
		Ref string `json:"ref"`
	}
	_ = json.Unmarshal(doc, &res)
	// The audit entry names the request and the reason (checked to name nobody); never an address or a person.
	s.audit(r, "guest_data.assisted_opened", "assisted_guest_request", res.ID, map[string]any{
		"reference": res.Ref, "purpose": in.Purpose, "verification_method": in.VerificationMethod, "reason": in.Reason})
	rawJSON(w, http.StatusCreated, doc)
}

type assistedActBody struct {
	Password  string        `json:"password"`
	Reason    string        `json:"reason"`
	Confirm   string        `json:"confirm"`
	Signature string        `json:"signature"`
	Selected  []assistedRef `json:"selected"`
}

// assistedAction validates the confirming body shared by export and erase and re-authenticates the operator.
func (s *server) assistedAction(w http.ResponseWriter, r *http.Request) (id string, in assistedActBody, selected []byte, actor string, ok bool) {
	if !s.assistedReady(w) {
		return
	}
	id = chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "invalid_id", "id must be a request id")
		return
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", reasonBoundsMessage)
		return
	}
	if strings.TrimSpace(in.Confirm) == "" {
		jsonErr(w, http.StatusBadRequest, "confirmation_required", "type the request reference to confirm")
		return
	}
	if len(in.Signature) != 32 {
		jsonErr(w, http.StatusBadRequest, "review_required", "review the records first")
		return
	}
	if len(in.Selected) < 1 || len(in.Selected) > assistedMaxSelected {
		jsonErr(w, http.StatusBadRequest, "nothing_selected", "select between 1 and 2000 records")
		return
	}
	for _, x := range in.Selected {
		if !assistedRecordKinds[x.Kind] || !uuidRe.MatchString(x.ID) {
			jsonErr(w, http.StatusBadRequest, "invalid_selection", "the selection holds a record that does not exist")
			return
		}
	}
	selected, _ = json.Marshal(in.Selected)
	actor, ok = s.portalStepUp(w, r, in.Password)
	return
}

func (s *server) exportAssisted(w http.ResponseWriter, r *http.Request) {
	id, in, selected, actor, ok := s.assistedAction(w, r)
	if !ok {
		return
	}
	if !s.assistedReasonOK(w, r, id, in.Reason) {
		return
	}
	// NO HAND-OVER UNRECORDED: the entry is written first, and the document is built only if it was.
	if err := s.auditErr(r, "guest_data.assisted_export_started", "assisted_guest_request", id,
		map[string]any{"reason": in.Reason, "records": len(in.Selected)}); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the hand-over could not be recorded, so nothing was prepared")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), guestDataEraseTimeout)
	defer cancel()
	var doc []byte
	if err := s.db.QueryRow(ctx, `SELECT public.assisted_request_export($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8::jsonb)::text`,
		s.tenantID, s.siteID, id, actor, in.Reason, in.Confirm, in.Signature, string(selected)).Scan(&doc); err != nil {
		s.audit(r, "guest_data.assisted_export_refused", "assisted_guest_request", id, map[string]any{"why": assistedWhy(err)})
		assistedDBError(w, err, "the hand-over failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="assisted-guest-data-`+time.Now().UTC().Format("20060102-150405")+`.json"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}

func (s *server) eraseAssisted(w http.ResponseWriter, r *http.Request) {
	id, in, selected, actor, ok := s.assistedAction(w, r)
	if !ok {
		return
	}
	if !s.assistedReasonOK(w, r, id, in.Reason) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), guestDataEraseTimeout)
	defer cancel()
	var doc []byte
	if err := s.ledgerQuery(ctx, &doc, `SELECT public.assisted_request_erase($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8::jsonb)::text`,
		s.tenantID, s.siteID, id, actor, in.Reason, in.Confirm, in.Signature, string(selected)); err != nil {
		if writeLedgerRefusal(w, err) {
			s.audit(r, "guest_data.assisted_erase_refused", "assisted_guest_request", id, map[string]any{"why": "ledger"})
			return
		}
		s.audit(r, "guest_data.assisted_erase_refused", "assisted_guest_request", id, map[string]any{"why": assistedWhy(err)})
		assistedDBError(w, err, "the erasure failed")
		return
	}
	var res struct {
		Ref    string         `json:"erasure_ref"`
		Counts map[string]any `json:"counts"`
	}
	_ = json.Unmarshal(doc, &res)
	s.audit(r, "guest_data.assisted_erased", "guest_data_erasure", res.Ref, map[string]any{
		"reason": in.Reason, "request": id, "counts": res.Counts})
	rawJSON(w, http.StatusOK, doc)
}

func (s *server) closeAssisted(w http.ResponseWriter, r *http.Request) {
	if !s.assistedReady(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "invalid_id", "id must be a request id")
		return
	}
	var in struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if in.Outcome != "COMPLETED" && in.Outcome != "CANCELLED" {
		jsonErr(w, http.StatusBadRequest, "invalid_outcome", "outcome must be COMPLETED or CANCELLED")
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", reasonBoundsMessage)
		return
	}
	actor := protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the request could not be attributed to an operator")
		return
	}
	if !s.assistedReasonOK(w, r, id, in.Reason) {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var doc []byte
	if err := s.ledgerQuery(ctx, &doc, `SELECT public.assisted_request_close($1::uuid,$2::uuid,$3::uuid,$4,$5,$6)::text`,
		s.tenantID, s.siteID, id, actor, in.Outcome, in.Reason); err != nil {
		if writeLedgerRefusal(w, err) {
			return
		}
		assistedDBError(w, err, "the request could not be closed")
		return
	}
	s.audit(r, "guest_data.assisted_"+strings.ToLower(in.Outcome), "assisted_guest_request", id, map[string]any{"reason": in.Reason})
	rawJSON(w, http.StatusOK, doc)
}

// assistedReasonOK refuses a reason that names the guest or carries an address, an email or a phone number BEFORE it is
// written anywhere: the activity log keeps what staff typed, so a refused reason must never reach it.
func (s *server) assistedReasonOK(w http.ResponseWriter, r *http.Request, id, reason string) bool {
	ctx, cancel := dbCtx(r)
	defer cancel()
	if _, err := s.db.Exec(ctx, `SELECT public.assisted_request_reason_check($1::uuid,$2::uuid,$3::uuid,$4)`, s.tenantID, s.siteID, id, reason); err != nil {
		assistedDBError(w, err, "the reason could not be checked")
		return false
	}
	return true
}

// assistedWhy is the database's reason for a refusal, for the activity log (never personal data).
func assistedWhy(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55006" {
		return pgErr.Detail
	}
	return "error"
}

var assistedRefusals = map[string]string{
	"closed":            "This request is already closed.",
	"changed":           "The records changed after you reviewed them. Review them again before confirming.",
	"not_listed":        "A selected record is not part of this request. Review the records again.",
	"not_eligible":      "A selected record cannot be processed: only records attributable to the person, or established as theirs, can be selected.",
	"blocked_recent":    "A selected record is less than a day old, so it cannot be erased yet.",
	"blocked_live":      "A selected record is still in use, so it cannot be erased now.",
	"device_incomplete": "A device can be selected only together with every record it carries.",
	"confirmation":      "The confirmation does not match the request reference.",
	"wrong_purpose":     "This request was opened for the other purpose.",
	"nothing_done":      "Nothing was handed over yet, so the request cannot be completed.", "other_site": "This guest identity was also used at another site, so it cannot be processed here.",
}

func assistedDBError(w http.ResponseWriter, err error, what string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55006" {
		msg, known := assistedRefusals[pgErr.Detail]
		if !known {
			msg = "The request was refused."
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "assisted_refused", "code": pgErr.Detail, "message": msg})
		return
	}
	if errors.As(err, &pgErr) && pgErr.Code == "23001" {
		jsonErr(w, http.StatusConflict, "assisted_refused", "The request cannot be changed.")
		return
	}
	guestDataDBError(w, err, what)
}
