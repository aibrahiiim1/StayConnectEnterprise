package main

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Site-wide settings + the read-mostly resources: auth-methods, walled
// garden, portal branding, payments, audit, reports, backups.

// ----- auth-methods (tenants.auth_methods jsonb) -------------------------------

func (s *server) authMethodsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := dbCtx(req)
		defer cancel()
		var raw []byte
		if err := s.db.QueryRow(ctx,
			`SELECT auth_methods FROM tenants WHERE id = $1`, s.tenantID).Scan(&raw); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "auth_methods load failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
	r.Put("/", func(w http.ResponseWriter, req *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "body must be a JSON object")
			return
		}
		raw, _ := json.Marshal(body)
		ctx, cancel := dbCtx(req)
		defer cancel()
		if _, err := s.db.Exec(ctx,
			`UPDATE tenants SET auth_methods = $1::jsonb, updated_at = now() WHERE id = $2`,
			string(raw), s.tenantID); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "auth_methods update failed")
			return
		}
		s.audit(req, "auth_methods.updated", "tenant", s.tenantID, map[string]any{"methods": body})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
	return r
}

// ----- walled garden ---------------------------------------------------------------

type wgRule struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Value       string    `json:"value"`
	Ports       []int     `json:"ports,omitempty"`
	Description *string   `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func validWGRule(kind, value string) bool {
	switch kind {
	case "ip":
		ip := net.ParseIP(value)
		return ip != nil && ip.To4() != nil
	case "cidr":
		_, n, err := net.ParseCIDR(value)
		return err == nil && n.IP.To4() != nil
	case "domain":
		v := strings.TrimSpace(value)
		return v != "" && !strings.ContainsAny(v, " /:") && strings.Contains(v, ".")
	}
	return false
}

func (s *server) walledGardenRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := dbCtx(req)
		defer cancel()
		rows, err := s.db.Query(ctx, `
            SELECT id, kind, value, COALESCE(ports, '{}'), description, created_at
              FROM walled_garden_rules WHERE tenant_id = $1 ORDER BY created_at DESC
        `, s.tenantID)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		defer rows.Close()
		var out []wgRule
		for rows.Next() {
			var rr wgRule
			if err := rows.Scan(&rr.ID, &rr.Kind, &rr.Value, &rr.Ports, &rr.Description, &rr.CreatedAt); err != nil {
				jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
				return
			}
			out = append(out, rr)
		}
		writeList(w, out)
	})
	r.Post("/", func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			Kind        string `json:"kind"`
			Value       string `json:"value"`
			Ports       []int  `json:"ports"`
			Description string `json:"description"`
		}
		if err := decodeJSON(req, &in); err != nil || !validWGRule(in.Kind, in.Value) {
			jsonErr(w, http.StatusBadRequest, "bad_request", "kind must be domain|ip|cidr with a valid value")
			return
		}
		for _, p := range in.Ports {
			if p < 1 || p > 65535 {
				jsonErr(w, http.StatusBadRequest, "bad_request", "ports must be 1..65535")
				return
			}
		}
		ctx, cancel := dbCtx(req)
		defer cancel()
		var id string
		err := s.db.QueryRow(ctx, `
            INSERT INTO walled_garden_rules (tenant_id, site_id, kind, value, ports, description)
            VALUES ($1, NULL, $2, $3, NULLIF($4::int[], '{}'), NULLIF($5,''))
            RETURNING id
        `, s.tenantID, in.Kind, in.Value, in.Ports, in.Description).Scan(&id)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
			return
		}
		s.audit(req, "walled_garden.created", "walled_garden_rule", id,
			map[string]any{"kind": in.Kind, "value": in.Value})
		s.pokeSCD(req, "/v1/admin/walled-garden/reload")
		writeJSON(w, http.StatusCreated, map[string]string{"id": id})
	})
	r.Delete("/{id}", func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "id")
		ctx, cancel := dbCtx(req)
		defer cancel()
		tag, err := s.db.Exec(ctx,
			`DELETE FROM walled_garden_rules WHERE id = $1 AND tenant_id = $2`, id, s.tenantID)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
			return
		}
		if tag.RowsAffected() == 0 {
			jsonErr(w, http.StatusNotFound, "not_found", "rule not found")
			return
		}
		s.audit(req, "walled_garden.deleted", "walled_garden_rule", id, nil)
		s.pokeSCD(req, "/v1/admin/walled-garden/reload")
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})
	return r
}

// ----- portal branding (tenants.branding jsonb) -------------------------------------

func (s *server) brandingRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := dbCtx(req)
		defer cancel()
		var raw []byte
		if err := s.db.QueryRow(ctx,
			`SELECT branding FROM tenants WHERE id = $1`, s.tenantID).Scan(&raw); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "branding load failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
	r.Put("/", func(w http.ResponseWriter, req *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "body must be a JSON object")
			return
		}
		raw, _ := json.Marshal(body)
		ctx, cancel := dbCtx(req)
		defer cancel()
		if _, err := s.db.Exec(ctx,
			`UPDATE tenants SET branding = $1::jsonb, updated_at = now() WHERE id = $2`,
			string(raw), s.tenantID); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "branding update failed")
			return
		}
		s.audit(req, "branding.updated", "tenant", s.tenantID, nil)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
	return r
}

// ----- payments (read-only history; refunds are a Stripe-console action for now) ----

type paymentRow struct {
	ID              string     `json:"id"`
	Status          string     `json:"status"`
	AmountCents     int64      `json:"amount_cents"`
	Currency        string     `json:"currency"`
	StripeSessionID string     `json:"stripe_session_id"`
	VoucherID       *string    `json:"voucher_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

func (s *server) paymentsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := dbCtx(req)
		defer cancel()
		rows, err := s.db.Query(ctx, `
            SELECT id, status, amount_cents, currency, stripe_session_id,
                   voucher_id::text, created_at, completed_at
              FROM payments WHERE tenant_id = $1
             ORDER BY created_at DESC LIMIT 200
        `, s.tenantID)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		defer rows.Close()
		var out []paymentRow
		for rows.Next() {
			var p paymentRow
			if err := rows.Scan(&p.ID, &p.Status, &p.AmountCents, &p.Currency,
				&p.StripeSessionID, &p.VoucherID, &p.CreatedAt, &p.CompletedAt); err != nil {
				jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
				return
			}
			out = append(out, p)
		}
		writeList(w, out)
	})
	return r
}

// ----- local audit log ----------------------------------------------------------------

type auditRow struct {
	TS         time.Time       `json:"ts"`
	ActorType  string          `json:"actor_type"`
	ActorID    *string         `json:"actor_id,omitempty"`
	Action     string          `json:"action"`
	TargetType *string         `json:"target_type,omitempty"`
	TargetID   *string         `json:"target_id,omitempty"`
	IP         *string         `json:"ip,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

func (s *server) auditRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		limit := 100
		if v, err := strconv.Atoi(req.URL.Query().Get("limit")); err == nil && v > 0 {
			limit = min(v, 500)
		}
		q := `SELECT ts, actor_type, actor_id, action, target_type, target_id, ip::text, payload
                FROM audit_log WHERE tenant_id = $1`
		args := []any{s.tenantID}
		if actions := req.URL.Query().Get("action"); actions != "" {
			q += ` AND action = ANY($2)`
			args = append(args, strings.Split(actions, ","))
		}
		q += ` ORDER BY ts DESC LIMIT ` + strconv.Itoa(limit)

		ctx, cancel := dbCtx(req)
		defer cancel()
		rows, err := s.db.Query(ctx, q, args...)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		defer rows.Close()
		var out []auditRow
		for rows.Next() {
			var a auditRow
			if err := rows.Scan(&a.TS, &a.ActorType, &a.ActorID, &a.Action,
				&a.TargetType, &a.TargetID, &a.IP, &a.Payload); err != nil {
				jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
				return
			}
			out = append(out, a)
		}
		writeList(w, out)
	})
	return r
}

// ----- reports --------------------------------------------------------------------------

func (s *server) reportsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/summary", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := dbCtx(req)
		defer cancel()
		out := map[string]any{}

		var active, today, sess7d, vUnused, vActive int64
		var upToday, downToday int64
		_ = s.db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE state = 'active'`).Scan(&active)
		_ = s.db.QueryRow(ctx, `
            SELECT count(*), COALESCE(sum(bytes_up),0), COALESCE(sum(bytes_down),0)
              FROM sessions WHERE started_at >= date_trunc('day', now())
        `).Scan(&today, &upToday, &downToday)
		_ = s.db.QueryRow(ctx,
			`SELECT count(*) FROM sessions WHERE started_at >= now() - interval '7 days'`).Scan(&sess7d)
		_ = s.db.QueryRow(ctx,
			`SELECT count(*) FILTER (WHERE state = 'unused'), count(*) FILTER (WHERE state = 'active') FROM vouchers`).
			Scan(&vUnused, &vActive)

		out["active_sessions"] = active
		out["sessions_today"] = today
		out["bytes_up_today"] = upToday
		out["bytes_down_today"] = downToday
		out["sessions_7d"] = sess7d
		out["vouchers_unused"] = vUnused
		out["vouchers_active"] = vActive

		type topPlan struct {
			TemplateID string `json:"template_id"`
			Name       string `json:"name"`
			Sessions   int64  `json:"sessions"`
		}
		var top []topPlan
		rows, err := s.db.Query(ctx, `
            SELECT t.id, t.name, count(se.id) AS n
              FROM sessions se
              JOIN vouchers v ON v.id = se.voucher_id
              JOIN ticket_templates t ON t.id = v.template_id
             WHERE se.started_at >= now() - interval '7 days'
             GROUP BY t.id, t.name ORDER BY n DESC LIMIT 5
        `)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var tp topPlan
				if rows.Scan(&tp.TemplateID, &tp.Name, &tp.Sessions) == nil {
					top = append(top, tp)
				}
			}
		}
		out["top_plans_7d"] = top
		writeJSON(w, http.StatusOK, out)
	})
	return r
}

// ----- backups ------------------------------------------------------------------------------

type backupRow struct {
	ID         string     `json:"id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Status     string     `json:"status"`
	Kind       string     `json:"kind"`
	Path       *string    `json:"path,omitempty"`
	SizeBytes  *int64     `json:"size_bytes,omitempty"`
	Error      *string    `json:"error,omitempty"`
}

func (s *server) backupsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := dbCtx(req)
		defer cancel()
		rows, err := s.db.Query(ctx, `
            SELECT id, started_at, finished_at, status, kind, path, size_bytes, error
              FROM backup_records ORDER BY started_at DESC LIMIT 50
        `)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		defer rows.Close()
		var out []backupRow
		for rows.Next() {
			var b backupRow
			if err := rows.Scan(&b.ID, &b.StartedAt, &b.FinishedAt, &b.Status, &b.Kind,
				&b.Path, &b.SizeBytes, &b.Error); err != nil {
				jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
				return
			}
			out = append(out, b)
		}
		writeList(w, out)
	})
	return r
}

// pokeSCD fires a best-effort admin action on scd (reload pokes). Failures
// are logged into the request-scoped audit payload but never fail the write:
// the periodic reconcilers guarantee convergence anyway.
func (s *server) pokeSCD(req *http.Request, path string) {
	if _, _, err := s.scd.call(req.Context(), http.MethodPost, path, nil); err != nil {
		s.audit(req, "scd.poke_failed", "scd", path, map[string]any{"error": err.Error()})
	}
}
