package main

// Guest session visibility and admin disconnect. Reads come straight from
// the site DB; the disconnect enforcement action goes through scd (which
// owns nftables/tc state) exactly like portald's logout path.

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type edgeSessionRow struct {
	ID             string     `json:"id"`
	IP             string     `json:"ip"`
	MAC            string     `json:"mac"`
	State          string     `json:"state"`
	StartedAt      time.Time  `json:"started_at"`
	LastActivityAt time.Time  `json:"last_activity_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	EndReason      *string    `json:"end_reason,omitempty"`
	BytesUp        int64      `json:"bytes_up"`
	BytesDown      int64      `json:"bytes_down"`
}

const sessionCols = `id, ip::text, mac::text, state, started_at, last_activity_at,
       ended_at, expires_at, end_reason, bytes_up, bytes_down`

func scanEdgeSession(row interface{ Scan(...any) error }, e *edgeSessionRow) error {
	return row.Scan(&e.ID, &e.IP, &e.MAC, &e.State, &e.StartedAt, &e.LastActivityAt,
		&e.EndedAt, &e.ExpiresAt, &e.EndReason, &e.BytesUp, &e.BytesDown)
}

func (s *server) sessionsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listGuestSessions)
	r.Get("/{id}", s.getGuestSession)
	r.Post("/{id}/disconnect", s.disconnectGuestSession)
	return r
}

func (s *server) listGuestSessions(w http.ResponseWriter, r *http.Request) {
	var stateArg any
	if v := r.URL.Query().Get("state"); v != "" {
		if v != "active" && v != "closed" {
			jsonErr(w, http.StatusBadRequest, "bad_request", "state must be active|closed")
			return
		}
		stateArg = v
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
        SELECT `+sessionCols+`
          FROM sessions
         WHERE tenant_id = $1
           AND ($2::text IS NULL OR state = $2)
         ORDER BY started_at DESC
         LIMIT 200
    `, s.tenantID, stateArg)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []edgeSessionRow
	for rows.Next() {
		var e edgeSessionRow
		if err := scanEdgeSession(rows, &e); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, e)
	}
	writeList(w, out)
}

func (s *server) getGuestSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var e edgeSessionRow
	err := scanEdgeSession(s.db.QueryRow(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE id = $1 AND tenant_id = $2`,
		id, s.tenantID), &e)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) disconnectGuestSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()

	var ip, state string
	err := s.db.QueryRow(ctx,
		`SELECT host(ip), state FROM sessions WHERE id = $1 AND tenant_id = $2`,
		id, s.tenantID).Scan(&ip, &state)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if state != "active" {
		jsonErr(w, http.StatusConflict, "conflict", "session is "+state+"; only active sessions can be disconnected")
		return
	}

	st, raw, err := s.scd.call(r.Context(), http.MethodPost, "/v1/sessions/revoke",
		map[string]string{"ip": ip, "reason": "admin"})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	if st != http.StatusOK {
		// Relay scd's verdict verbatim (e.g. session already gone in kernel).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		_, _ = w.Write(raw)
		return
	}
	s.audit(r, "session.disconnected", "session", id, map[string]any{"ip": ip})
	writeJSON(w, http.StatusOK, map[string]string{"session_id": id, "status": "disconnected"})
}
