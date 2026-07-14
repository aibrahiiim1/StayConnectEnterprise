package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/transport"
)

type Session struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id"`
	SiteID         string     `json:"site_id"`
	ApplianceID    string     `json:"appliance_id"`
	GuestID        string     `json:"guest_id"`
	VoucherID      *string    `json:"voucher_id,omitempty"`
	IP             string     `json:"ip"`
	MAC            string     `json:"mac"`
	State          string     `json:"state"`
	EndReason      *string    `json:"end_reason,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	LastActivityAt time.Time  `json:"last_activity_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	BytesUp        int64      `json:"bytes_up"`
	BytesDown      int64      `json:"bytes_down"`
}

type SessionsBase struct {
	*Base
	Transport transport.ApplianceTransport
}

func (s *SessionsBase) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	r.Get("/", s.list)
	r.Get("/{id}", s.get)
	r.Post("/{id}/disconnect", s.disconnect)
	return r
}

// list supports filters via query:
//
//	state=active|closed|suspended
//	site_id=<uuid>
//	appliance_id=<uuid>
//	since=RFC3339   // only sessions started after
//	q=<substring>   // matched against ip::text or mac::text
func (s *SessionsBase) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	q := r.URL.Query()
	ctx, cancel := DBCtx(r)
	defer cancel()

	limit := ParseLimit(r, 50, 200)
	curT, curI, err := DecodeCursor(q.Get("cursor"))
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	var tArg, iArg any
	if !curT.IsZero() {
		tArg = curT
	}
	if curI != "" {
		iArg = curI
	}

	var stateArg, siteArg, appArg, searchArg any
	if v := q.Get("state"); v != "" {
		stateArg = v
	}
	if v := q.Get("site_id"); v != "" {
		siteArg = v
	}
	if v := q.Get("appliance_id"); v != "" {
		appArg = v
	}
	if v := q.Get("q"); v != "" {
		searchArg = "%" + v + "%"
	}
	var sinceArg any
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			sinceArg = t
		}
	}

	rows, err := s.DB.Query(ctx, `
        SELECT id, tenant_id, site_id, appliance_id, guest_id, voucher_id,
               host(ip), mac::text, state, end_reason,
               started_at, last_activity_at, ended_at, bytes_up, bytes_down
          FROM sessions
         WHERE tenant_id = $1
           AND ($2::text  IS NULL OR state        = $2)
           AND ($3::uuid  IS NULL OR site_id      = $3)
           AND ($4::uuid  IS NULL OR appliance_id = $4)
           AND ($5::timestamptz IS NULL OR started_at >= $5)
           AND ($6::text  IS NULL OR host(ip) ILIKE $6 OR mac::text ILIKE $6)
           AND ($7::timestamptz IS NULL OR (started_at, id) < ($7, $8::uuid))
         ORDER BY started_at DESC, id DESC
         LIMIT $9
    `, tenantID, stateArg, siteArg, appArg, sinceArg, searchArg, tArg, iArg, limit+1)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var ss Session
		if err := rows.Scan(
			&ss.ID, &ss.TenantID, &ss.SiteID, &ss.ApplianceID, &ss.GuestID, &ss.VoucherID,
			&ss.IP, &ss.MAC, &ss.State, &ss.EndReason,
			&ss.StartedAt, &ss.LastActivityAt, &ss.EndedAt, &ss.BytesUp, &ss.BytesDown,
		); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, ss)
	}
	meta := ListMeta{}
	if len(out) > limit {
		last := out[limit-1]
		meta.HasMore = true
		meta.Cursor = EncodeCursor(last.StartedAt, last.ID)
		out = out[:limit]
	}
	WriteList(w, out, meta)
}

func (s *SessionsBase) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var ss Session
	err := s.DB.QueryRow(ctx, `
        SELECT id, tenant_id, site_id, appliance_id, guest_id, voucher_id,
               host(ip), mac::text, state, end_reason,
               started_at, last_activity_at, ended_at, bytes_up, bytes_down
          FROM sessions WHERE id = $1 AND tenant_id = $2
    `, id, tenantID).Scan(
		&ss.ID, &ss.TenantID, &ss.SiteID, &ss.ApplianceID, &ss.GuestID, &ss.VoucherID,
		&ss.IP, &ss.MAC, &ss.State, &ss.EndReason,
		&ss.StartedAt, &ss.LastActivityAt, &ss.EndedAt, &ss.BytesUp, &ss.BytesDown,
	)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "session not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, ss)
}

type disconnectReq struct {
	Reason string `json:"reason,omitempty"`
}

// disconnect asks the appliance owning this session to revoke it.
// The appliance updates the DB (ends the session) and tears down nft/tc.
func (s *SessionsBase) disconnect(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")

	var req disconnectReq
	if r.ContentLength > 0 {
		if err := DecodeJSON(r, &req); err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
			return
		}
	}
	if req.Reason == "" {
		req.Reason = "admin"
	}

	ctx, cancel := DBCtx(r)
	defer cancel()
	var ip, applianceID, state string
	err := s.DB.QueryRow(ctx, `
        SELECT host(ip), appliance_id, state
          FROM sessions WHERE id = $1 AND tenant_id = $2
    `, id, tenantID).Scan(&ip, &applianceID, &state)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "session not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	if state != "active" {
		Fail(w, r, http.StatusConflict, CodeConflict, "session not active")
		return
	}

	if err := s.Transport.Revoke(r.Context(), applianceID, ip, req.Reason); err != nil {
		Fail(w, r, http.StatusBadGateway, CodeBadGateway, "appliance unreachable — session not revoked yet")
		return
	}
	audit.Op(r.Context(), s.DB, r, "session.disconnected", "session", id, map[string]any{
		"_tenant_id": tenantID, "ip": ip, "reason": req.Reason,
	})
	WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
