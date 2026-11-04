package main

// Look up, export and erase one guest; resource key guest-data-rights (site_admin and IT manager only).
// The database functions decide what belongs to the guest and what is live, so export and erasure share one
// definition. Audit rows name the record id or erasure reference, never the guest's name or address.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const guestDataEraseTimeout = ledgerRouteTimeout

const guestDataSearchHeader = "X-Guest-Data-Search"

// stay_guest is one guest of a stay with more than one guest named on it.
var guestDataKinds = map[string]bool{"account": true, "principal": true, "stay": true, "stay_guest": true}

func (s *server) guestDataRightsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(assistedBodyLimit)
	r.Get("/search", s.searchGuestData)
	r.Get("/erasures", s.listGuestDataErasures)
	r.Post("/export", s.exportGuestData)
	r.Post("/erase", s.eraseGuestData)
	s.assistedRoutes(r)
	s.registerRoutes(r)
	return r
}

func (s *server) searchGuestData(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "guest data")
		return
	}
	// The search travels in a header: it is a name, email or phone number, and URLs end up in logs and browser histories.
	q, ok := readSearch(w, r, guestDataSearchHeader)
	if !ok {
		return
	}
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
	// The search returns at most 50 records (the database asks for 51 to know whether there are more).
	truncated := len(out) > 50
	if truncated {
		out = out[:50]
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": out, "truncated": truncated})
}

func (s *server) listGuestDataErasures(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "guest data")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT erasure_ref, subject_kind, erased_by, erase_reason, counts, erased_at
	     FROM iam_v2.guest_data_erasures WHERE tenant_id=$1 AND site_id=$2 ORDER BY erased_at DESC LIMIT 201`,
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
	if rows.Err() != nil {
		guestDataDBError(w, rows.Err(), "the erasures could not be read")
		return
	}
	// The register shows the newest 200; the page says so, so an older reference is looked for elsewhere (the export of the
	// database, or the activity log) and not believed missing.
	truncated := len(out) > 200
	if truncated {
		out = out[:200]
	}
	writeJSON(w, http.StatusOK, map[string]any{"erasures": out, "truncated": truncated})
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
	actor, ok := s.stepUpStrict(w, r, in.Password)
	return in, actor, ok
}

func (s *server) exportGuestData(w http.ResponseWriter, r *http.Request) {
	in, _, ok := s.guestDataAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), guestDataEraseTimeout)
	defer cancel()
	// The reason must name nobody: it is kept in the activity log, which erasure does not remove.
	if _, err := s.db.Exec(ctx, `SELECT public.guest_data_reason_check($1::uuid,$2::uuid,$3,$4::uuid,$5)`,
		s.tenantID, s.siteID, in.Kind, in.ID, in.Reason); err != nil && !isUndefinedObject(err) {
		// Recorded without the reason: it is the very thing that was refused.
		s.audit(r, "guest_data.export_refused", in.Kind, in.ID, map[string]any{"why": assistedWhy(err)})
		if !writeGuestDataLive(w, err) {
			guestDataDBError(w, err, "the export failed")
		}
		return
	}
	var doc []byte
	if err := s.db.QueryRow(ctx, `SELECT public.guest_data_export($1::uuid,$2::uuid,$3,$4::uuid)::text`,
		s.tenantID, s.siteID, in.Kind, in.ID).Scan(&doc); err != nil {
		// A refused or failed export is recorded too: who tried, for which record, and why it did not happen.
		s.audit(r, "guest_data.export_failed", in.Kind, in.ID, map[string]any{"reason": in.Reason})
		if writeGuestDataLive(w, err) {
			return
		}
		guestDataDBError(w, err, "the export failed")
		return
	}
	// NO EXPORT UNRECORDED: the file leaves only once its audit entry is written.
	if err := s.auditErr(r, "guest_data.exported", in.Kind, in.ID, map[string]any{"reason": in.Reason}); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the export could not be recorded, so it was not sent")
		return
	}
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
		Ref           string         `json:"erasure_ref"`
		Counts        map[string]any `json:"counts"`
		PackagesEnded int            `json:"packages_ended"`
		SessionsEnded int            `json:"sessions_ended"`
		// DevicesWithdrawn counts only the sessions scd confirmed off the network at once; the rest converge on enforcement's next pass.
		DevicesWithdrawn int `json:"devices_withdrawn"`
	}
	var opID string
	if sess := sessFrom(r.Context()); sess != nil {
		opID = sess.OperatorID
	}
	var packages int
	var ended []string
	var addrs map[string]string
	endOwn := func(tx pgx.Tx) error {
		var err error
		packages, ended, addrs, err = s.endOwnAccessForErasure(ctx, tx, in.Kind, in.ID, opID)
		return err
	}
	err := s.ledgerQueryAfter(ctx, endOwn, &res, `SELECT public.guest_data_erase($1::uuid,$2::uuid,$3,$4::uuid,$5,$6)`,
		s.tenantID, s.siteID, in.Kind, in.ID, actor, in.Reason)
	if err != nil && writeLedgerRefusal(w, err) {
		s.audit(r, "guest_data.erase_failed", in.Kind, in.ID, map[string]any{"why": "ledger"})
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55006" {
		s.audit(r, "guest_data.erase_refused", in.Kind, in.ID, map[string]any{"live": strings.Split(pgErr.Detail, ",")})
		writeGuestDataLive(w, err)
		return
	}
	if err != nil {
		s.audit(r, "guest_data.erase_failed", in.Kind, in.ID, map[string]any{"why": assistedWhy(err)})
		guestDataDBError(w, err, "the erasure failed")
		return
	}
	// The access was ended in the erasure's own transaction; this only takes the devices off the network at once.
	kernel := s.withdrawKernel(r, ended, addrs)
	res.PackagesEnded, res.SessionsEnded = packages, len(ended)
	res.DevicesWithdrawn, _ = kernel["withdrawn"].(int)
	s.audit(r, "guest_data.erased", "guest_data_erasure", res.Ref, map[string]any{
		"subject_kind": in.Kind, "reason": in.Reason, "counts": res.Counts,
		"packages_ended": packages, "sessions_ended": len(ended), "kernel": kernel["summary"]})
	writeJSON(w, http.StatusOK, res)
}

// erasureEndReason is stored with each ended package; the guest's own reason is kept with the erasure only.
const erasureEndReason = "Ended to erase the guest's data"

// endOwnAccessForErasure ends, through the staff End Package writer, the access whose subject is the account or identity being
// erased: its own entitlements and their sessions. A room's access belongs to everyone in the room and is never ended here
// (0145); a stay therefore stays refused while its room access runs. Returns the packages ended, the sessions they ended and those
// sessions' addresses, read before the erasure clears them so the devices can be withdrawn at once; they are kept in memory only.
func (s *server) endOwnAccessForErasure(ctx context.Context, tx pgx.Tx, kind, id, operator string) (int, []string, map[string]string, error) {
	col := map[string]string{"account": "guest_account_id", "principal": "guest_principal_id"}[kind]
	if col == "" {
		return 0, nil, nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM iam_v2.entitlements
	     WHERE tenant_id=$1 AND site_id=$2 AND `+col+`=$3::uuid AND status <> 'TERMINATED' ORDER BY id`, s.tenantID, s.siteID, id)
	if err != nil {
		return 0, nil, nil, err
	}
	ents, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ents) == 0 {
		return 0, nil, nil, err
	}
	if !uuidRe.MatchString(operator) {
		return 0, nil, nil, errors.New("the erasure could not be attributed to an operator")
	}
	var ended []string
	for _, ent := range ents {
		var sessions []string
		if err := tx.QueryRow(ctx, `SELECT sessions::text[] FROM iam_v2.admin_end_package($1,$2,$3::uuid,$4::uuid,$5,gen_random_uuid())`,
			s.tenantID, s.siteID, ent, operator, erasureEndReason).Scan(&sessions); err != nil {
			return 0, nil, nil, err
		}
		ended = append(ended, sessions...)
	}
	addrs := map[string]string{}
	if len(ended) > 0 {
		rows, err := tx.Query(ctx, `SELECT id::text, host(ip) FROM iam_v2.sessions
		     WHERE tenant_id=$1 AND site_id=$2 AND id = ANY($3::uuid[]) AND ip IS NOT NULL`, s.tenantID, s.siteID, ended)
		if err != nil {
			return 0, nil, nil, err
		}
		for rows.Next() {
			var sid, ip string
			if err := rows.Scan(&sid, &ip); err != nil {
				rows.Close()
				return 0, nil, nil, err
			}
			addrs[sid] = ip
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, nil, nil, err
		}
	}
	return len(ents), ended, addrs, nil
}

// writeGuestDataLive answers a refusal the database gave with its reasons (55006): the guest is still live, the
// identity belongs to another site too, or the records changed while being locked. False for any other error.
func writeGuestDataLive(w http.ResponseWriter, err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55006" {
		return false
	}
	reasons := strings.Split(pgErr.Detail, ",")
	msg := "This guest is still active, so nothing was done."
	switch {
	case len(reasons) == 1 && reasons[0] == "other_site":
		msg = "This guest identity was also used at another site, so it cannot be exported or erased here."
	case len(reasons) == 1 && reasons[0] == "busy":
		msg = "This guest's records changed while the request ran. Try again."
	}
	writeJSON(w, http.StatusConflict, map[string]any{"error": "guest_data_live", "live": reasons, "message": msg})
	return true
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
		// An unexpected failure is logged by class and message only (never a DETAIL line, which can carry values), so the next one
		// is not a mystery behind a generic "the erasure failed".
		if errors.As(err, &pgErr) {
			slog.Warn("guest data request failed", "what", what, "sqlstate", pgErr.Code, "msg", pgErr.Message)
		} else {
			msg := err.Error()
			if len(msg) > 200 {
				msg = msg[:200]
			}
			slog.Warn("guest data request failed", "what", what, "err", msg)
		}
		jsonErr(w, http.StatusInternalServerError, "internal", what)
	}
}
