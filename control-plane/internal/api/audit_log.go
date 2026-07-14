package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type AuditEntry struct {
	TS         time.Time       `json:"ts"`
	TenantID   *string         `json:"tenant_id,omitempty"`
	ActorType  string          `json:"actor_type"`
	ActorID    *string         `json:"actor_id,omitempty"`
	Action     string          `json:"action"`
	TargetType *string         `json:"target_type,omitempty"`
	TargetID   *string         `json:"target_id,omitempty"`
	IP         *string         `json:"ip,omitempty"`
	UserAgent  *string         `json:"user_agent,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// listAudit handles GET /v1/tenants/{tenantID}/audit.
// Filters:
//
//	action=site.created (or comma-separated list)
//	from=RFC3339, to=RFC3339   (default: last 7 days)
//	limit (default 100, max 500)
func (b *Base) listAudit(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	if !b.ensureTenantAccess(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	q := r.URL.Query()
	now := time.Now().UTC()
	to := now
	from := now.Add(-7 * 24 * time.Hour)
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	if !from.Before(to) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "from must precede to")
		return
	}
	limit := ParseLimit(r, 100, 500)

	var actionsArg any
	if v := q.Get("action"); v != "" {
		// Allow comma-separated. Postgres ANY($::text[]).
		parts := strings.Split(v, ",")
		clean := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				clean = append(clean, p)
			}
		}
		if len(clean) > 0 {
			actionsArg = clean
		}
	}

	ctx, cancel := DBCtx(r)
	defer cancel()

	rows, err := b.DB.Query(ctx, `
        SELECT ts, tenant_id::text, actor_type, actor_id,
               action, target_type, target_id,
               host(ip), user_agent, payload
          FROM audit_log
         WHERE tenant_id = $1
           AND ts >= $2 AND ts < $3
           AND ($4::text[] IS NULL OR action = ANY($4))
         ORDER BY ts DESC
         LIMIT $5
    `, tenantID, from, to, actionsArg, limit)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.TS, &e.TenantID, &e.ActorType, &e.ActorID,
			&e.Action, &e.TargetType, &e.TargetID,
			&e.IP, &e.UserAgent, &e.Payload); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, e)
	}
	WriteList(w, out, ListMeta{})
}
