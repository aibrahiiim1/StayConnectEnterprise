package main

// The request register (GDPR final mission): a request a guest or visitor made under the data-protection rules, the day it
// is due, and how it was answered.
//
//	GET  /edge/v1/guest-data-rights/register               the requests, open ones first, with days left
//	POST /edge/v1/guest-data-rights/register               record a request that was received
//	POST /edge/v1/guest-data-rights/register/{id}/extend   two more months for a complex request (once, with a reason)
//	POST /edge/v1/guest-data-rights/register/{id}/answer   close it: answered, partly refused, refused or withdrawn
//
// It is a record the staff keep and decides nothing: it does not run an export or an erasure. Answering removes the
// requester reference and returns it once, as the hotel's own file of the request.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *server) registerRoutes(parent chi.Router) {
	parent.Group(func(r chi.Router) {
		r.Use(assistedBodyLimit)
		r.Get("/register", s.listRegister)
		r.Post("/register", s.openRegister)
		r.Post("/register/{id}/extend", s.extendRegister)
		r.Post("/register/{id}/answer", s.answerRegister)
	})
}

func (s *server) listRegister(w http.ResponseWriter, r *http.Request) {
	if !s.assistedReady(w) {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var doc []byte
	if err := s.db.QueryRow(ctx, `SELECT jsonb_build_object('requests', iam_v2.request_register_list($1::uuid,$2::uuid))::text`,
		s.tenantID, s.siteID).Scan(&doc); err != nil {
		registerDBError(w, err, "the register could not be read")
		return
	}
	rawJSON(w, http.StatusOK, doc)
}

func (s *server) openRegister(w http.ResponseWriter, r *http.Request) {
	if !s.assistedReady(w) {
		return
	}
	var in struct {
		Kind               string `json:"kind"`
		ReceivedAt         string `json:"received_at"`
		Requester          string `json:"requester"`
		VerificationMethod string `json:"verification_method"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if in.Kind != "ACCESS" && in.Kind != "ERASURE" && in.Kind != "OTHER" {
		jsonErr(w, http.StatusBadRequest, "invalid_kind", "kind must be ACCESS, ERASURE or OTHER")
		return
	}
	if _, err := time.Parse("2006-01-02", in.ReceivedAt); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid_date", "received_at must be a date (YYYY-MM-DD)")
		return
	}
	actor := protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the request could not be attributed to an operator")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var doc []byte
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.request_register_open($1::uuid,$2::uuid,$3,$4,$5::date,$6,$7)::text`,
		s.tenantID, s.siteID, actor, in.Kind, in.ReceivedAt, in.Requester, in.VerificationMethod).Scan(&doc); err != nil {
		registerDBError(w, err, "the request could not be recorded")
		return
	}
	s.audit(r, "guest_data.request_recorded", "guest_data_request", "", map[string]any{"kind": in.Kind})
	rawJSON(w, http.StatusCreated, doc)
}

func (s *server) extendRegister(w http.ResponseWriter, r *http.Request) {
	if !s.assistedReady(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "invalid_id", "id must be a request id")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	actor := protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the request could not be attributed to an operator")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var doc []byte
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.request_register_extend($1::uuid,$2::uuid,$3::uuid,$4,$5)::text`,
		s.tenantID, s.siteID, id, actor, in.Reason).Scan(&doc); err != nil {
		registerDBError(w, err, "the request could not be extended")
		return
	}
	s.audit(r, "guest_data.request_extended", "guest_data_request", id, nil)
	rawJSON(w, http.StatusOK, doc)
}

func (s *server) answerRegister(w http.ResponseWriter, r *http.Request) {
	if !s.assistedReady(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "invalid_id", "id must be a request id")
		return
	}
	var in struct {
		Status      string `json:"status"`
		Outcome     string `json:"outcome"`
		Note        string `json:"note"`
		ErasureRef  string `json:"erasure_ref"`
		AssistedRef string `json:"assisted_ref"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	actor := protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the request could not be attributed to an operator")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var doc []byte
	if err := s.ledgerQuery(ctx, &doc, `SELECT iam_v2.request_register_answer($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9)::text`,
		s.tenantID, s.siteID, id, actor, in.Status, in.Outcome, in.Note, in.ErasureRef, in.AssistedRef); err != nil {
		if writeLedgerRefusal(w, err) {
			return
		}
		registerDBError(w, err, "the request could not be closed")
		return
	}
	s.audit(r, "guest_data.request_answered", "guest_data_request", id, map[string]any{"status": in.Status, "outcome": in.Outcome})
	rawJSON(w, http.StatusOK, doc)
}

func registerDBError(w http.ResponseWriter, err error, what string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55006" {
		msg := "The request was refused."
		switch pgErr.Detail {
		case "closed":
			msg = "This request is already answered."
		case "not_extendable":
			msg = "A request can be extended once, within its first month."
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "register_refused", "code": pgErr.Detail, "message": msg})
		return
	}
	guestDataDBError(w, err, what)
}
