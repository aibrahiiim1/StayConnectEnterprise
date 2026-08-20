package main

// Guest session visibility and admin disconnect, over the SINGLE session authority.
//
// These reads used to come from public.sessions. That table was the superseded session domain and is gone;
// iam_v2.sessions is the authority, so the operator's view moves onto it rather than disappearing. The
// disconnect enforcement action still goes through scd, which owns nftables/tc state, exactly like
// portald's logout path.
//
// Two column names changed with the move and are aliased here rather than renamed in the API, so the
// operator surface keeps its contract: started_at is iam_v2.sessions.started, ended_at is ended. There is
// no last_activity_at in the current model -- liveness is derived from accounting, not stamped on the
// session row -- so it reports the session start until the first accounting tick would have moved it.

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

const sessionCols = `id, ip::text, mac::text, state, started AS started_at, started AS last_activity_at,
       ended AS ended_at, expires_at, end_reason, bytes_up, bytes_down`

func scanEdgeSession(row interface{ Scan(...any) error }, e *edgeSessionRow) error {
	return row.Scan(&e.ID, &e.IP, &e.MAC, &e.State, &e.StartedAt, &e.LastActivityAt,
		&e.EndedAt, &e.ExpiresAt, &e.EndReason, &e.BytesUp, &e.BytesDown)
}

func (s *server) sessionsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listGuestSessions)
	// PHASE 6 (DARK): the online-time budget view. Registered BEFORE the {id} pattern so the static path
	// wins, and mounted here rather than as its own resource because it is live access state -- exactly what
	// this resource already means -- so it inherits the role matrix instead of adding a row to it.
	if s.phase6.AggregateTimeOn() {
		r.Get("/aggregate-time", s.listAggregateTime)
	}
	r.Get("/{id}", s.getGuestSession)
	r.Post("/{id}/disconnect", s.disconnectGuestSession)
	return r
}

func (s *server) listGuestSessions(w http.ResponseWriter, r *http.Request) {
	var stateArg any
	if v := r.URL.Query().Get("state"); v != "" {
		// The authority's own vocabulary. iam_v2.sessions is 'active', 'PENDING_ENFORCEMENT' (durable but
		// not yet enforced) or 'ended'; the legacy domain called the last one 'closed'. The old spelling is
		// still accepted so an existing operator bookmark or script does not break, but it is translated
		// here rather than carried any deeper.
		switch v {
		case "active", "ended", "PENDING_ENFORCEMENT":
		case "closed":
			v = "ended"
		default:
			jsonErr(w, http.StatusBadRequest, "bad_request",
				"state must be active|PENDING_ENFORCEMENT|ended")
			return
		}
		stateArg = v
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
        SELECT `+sessionCols+`
          FROM iam_v2.sessions
         WHERE tenant_id = $1
           AND ($2::text IS NULL OR state = $2)
         ORDER BY started DESC
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
		`SELECT `+sessionCols+` FROM iam_v2.sessions WHERE id = $1 AND tenant_id = $2`,
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
		`SELECT host(ip), state FROM iam_v2.sessions WHERE id = $1 AND tenant_id = $2`,
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
