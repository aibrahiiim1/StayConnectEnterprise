package main

// GUEST DATA RIGHTS: look up, export and erase one guest (migration 0128; GDPR remediation plan C3 and C4;
// Product-Owner decisions D-C3 and D-C4).
//
//	GET  /edge/v1/guest-data-rights/search?q=   client accounts, guest identities and stays matching a search
//	GET  /edge/v1/guest-data-rights/erasures    the erasures that ran (reference, who, why, what was anonymised)
//	POST /edge/v1/guest-data-rights/export      one guest's data as a JSON document           password + reason
//	POST /edge/v1/guest-data-rights/erase       anonymise one guest; refused while live         password + reason
//
// Its own resource key, guest-data-rights: site_admin and the IT manager only (D-C4). Staff check the guest's
// identity and hand the export over; there is no guest self-service. All the deciding -- what belongs to the
// guest, what is still live, what each column becomes -- is in the database functions, so export and erasure
// share one definition of "this guest". Audit rows name the record id (export) or the erasure reference
// (erasure), never the guest's name or address; the search term is not audited at all.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const guestDataEraseTimeout = time.Minute

// stay_guest is one guest of a stay with more than one guest named on it (migration 0141, GDPR audit C3).
var guestDataKinds = map[string]bool{"account": true, "principal": true, "stay": true, "stay_guest": true}

func (s *server) guestDataRightsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/search", s.searchGuestData)
	r.Get("/erasures", s.listGuestDataErasures)
	r.Post("/export", s.exportGuestData)
	r.Post("/erase", s.eraseGuestData)
	return r
}

func (s *server) searchGuestData(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "guest data")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 || len(q) > 200 {
		jsonErr(w, http.StatusBadRequest, "invalid_search", "enter 2 to 200 characters")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT o_kind, o_id::text, COALESCE(o_label,''), o_detail, o_status, o_last_activity
	     FROM iam_v2.guest_data_find($1::uuid,$2::uuid,$3)`, s.tenantID, s.siteID, q)
	if err != nil {
		guestDataDBError(w, err, "the search failed")
		return
	}
	defer rows.Close()
	type match struct {
		Kind         string     `json:"kind"`
		ID           string     `json:"id"`
		Label        string     `json:"label"`
		Detail       *string    `json:"detail"`
		Status       *string    `json:"status"`
		LastActivity *time.Time `json:"last_activity"`
	}
	out := []match{}
	for rows.Next() {
		var m match
		if err := rows.Scan(&m.Kind, &m.ID, &m.Label, &m.Detail, &m.Status, &m.LastActivity); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the search failed")
			return
		}
		out = append(out, m)
	}
	if rows.Err() != nil {
		guestDataDBError(w, rows.Err(), "the search failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": out})
}

func (s *server) listGuestDataErasures(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "guest data")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT erasure_ref, subject_kind, erased_by, erase_reason, counts, erased_at
	     FROM iam_v2.guest_data_erasures WHERE tenant_id=$1 AND site_id=$2 ORDER BY erased_at DESC LIMIT 50`,
		s.tenantID, s.siteID)
	if err != nil {
		guestDataDBError(w, err, "the erasures could not be read")
		return
	}
	defer rows.Close()
	type erasure struct {
		Ref      string         `json:"erasure_ref"`
		Kind     string         `json:"subject_kind"`
		ErasedBy string         `json:"erased_by"`
		Reason   string         `json:"reason"`
		Counts   map[string]any `json:"counts"`
		ErasedAt time.Time      `json:"erased_at"`
	}
	out := []erasure{}
	for rows.Next() {
		var e erasure
		if err := rows.Scan(&e.Ref, &e.Kind, &e.ErasedBy, &e.Reason, &e.Counts, &e.ErasedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the erasures could not be read")
			return
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"erasures": out})
}

type guestDataRequest struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Reason   string `json:"reason"`
	Password string `json:"password"`
}

// guestDataAction validates the body and re-authenticates the operator; it answers the request itself on failure.
func (s *server) guestDataAction(w http.ResponseWriter, r *http.Request) (guestDataRequest, string, bool) {
	var in guestDataRequest
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return in, "", false
	}
	if !guestDataKinds[in.Kind] {
		jsonErr(w, http.StatusBadRequest, "invalid_kind", "kind must be account, principal, stay or stay_guest")
		return in, "", false
	}
	if !uuidRe.MatchString(in.ID) {
		jsonErr(w, http.StatusBadRequest, "invalid_id", "id must be a record id from the search")
		return in, "", false
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return in, "", false
	}
	if s.tenantID == "" || s.siteID == "" {
		jsonErr(w, http.StatusConflict, "awaiting_assignment", "this appliance has no site yet")
		return in, "", false
	}
	actor, ok := s.portalStepUp(w, r, in.Password)
	return in, actor, ok
}

func (s *server) exportGuestData(w http.ResponseWriter, r *http.Request) {
	in, _, ok := s.guestDataAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), guestDataEraseTimeout)
	defer cancel()
	var doc []byte
	if err := s.db.QueryRow(ctx, `SELECT public.guest_data_export($1::uuid,$2::uuid,$3,$4::uuid)::text`,
		s.tenantID, s.siteID, in.Kind, in.ID).Scan(&doc); err != nil {
		guestDataDBError(w, err, "the export failed")
		return
	}
	s.audit(r, "guest_data.exported", in.Kind, in.ID, map[string]any{"reason": in.Reason})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="guest-data-`+time.Now().UTC().Format("20060102-150405")+`.json"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}

func (s *server) eraseGuestData(w http.ResponseWriter, r *http.Request) {
	in, actor, ok := s.guestDataAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), guestDataEraseTimeout)
	defer cancel()
	var res struct {
		Ref    string         `json:"erasure_ref"`
		Counts map[string]any `json:"counts"`
	}
	err := s.db.QueryRow(ctx, `SELECT public.guest_data_erase($1::uuid,$2::uuid,$3,$4::uuid,$5,$6)`,
		s.tenantID, s.siteID, in.Kind, in.ID, actor, in.Reason).Scan(&res)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55006" {
		reasons := strings.Split(pgErr.Detail, ",")
		s.audit(r, "guest_data.erase_refused", in.Kind, in.ID, map[string]any{"live": reasons})
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "guest_data_live", "live": reasons,
			"message": "This guest is still active, so nothing was erased.",
		})
		return
	}
	if err != nil {
		guestDataDBError(w, err, "the erasure failed")
		return
	}
	s.audit(r, "guest_data.erased", "guest_data_erasure", res.Ref, map[string]any{
		"subject_kind": in.Kind, "reason": in.Reason, "counts": res.Counts})
	writeJSON(w, http.StatusOK, res)
}

func guestDataDBError(w http.ResponseWriter, err error, what string) {
	var pgErr *pgconn.PgError
	switch {
	case isUndefinedObject(err):
		jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have guest data rights yet")
	case errors.As(err, &pgErr) && pgErr.Code == "P0002":
		jsonErr(w, http.StatusNotFound, "not_found", "that guest record does not exist on this site")
	case errors.As(err, &pgErr) && pgErr.Code == "22023":
		jsonErr(w, http.StatusBadRequest, "invalid", pgErr.Message)
	default:
		jsonErr(w, http.StatusInternalServerError, "internal", what)
	}
}
