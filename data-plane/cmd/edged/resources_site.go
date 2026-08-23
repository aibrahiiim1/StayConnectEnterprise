package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Site-wide settings + the read-mostly resources: auth-methods, walled
// garden, portal branding, payments, audit, reports, backups.

// ----- auth-methods (tenants.auth_methods jsonb) -------------------------------

// pmsSignInModes are the verification values a site may accept alongside the room number, mapped to the
// wire values the portal and resolver already use.
//
// "either" is ABSENT deliberately. It is still accepted from an existing stored configuration and still
// behaves exactly as it did, but it is not offered as a new choice: it means last-name-or-reservation and
// the portal decides which by guessing at the shape of what was typed, so a surname containing a digit is
// submitted as a reservation number and fails.
//
// "room_any" is the supported way to accept more than one kind of identifier, and it is NOT a revival of
// "either". The distinction is where the decision happens. "either" made the BROWSER guess which field the
// guest meant and send one of them; "room_any" sends the value as typed and has the server compare it against
// all three PMS-derived fields at once, with the existing ambiguity rule failing closed when a value matches
// more than one Stay. Nothing is inferred from the shape of what was typed.
var pmsSignInModes = map[string]bool{
	"room_lastname":    true,
	"room_firstname":   true,
	"room_reservation": true,
	"room_any":         true,
}

// authMethodsRoutes reads and writes the site's guest sign-in configuration.
//
// THE WRITE IS A MERGE, NOT A REPLACEMENT. It used to marshal whatever the caller posted straight over the
// column, so any client that sent a partial object silently deleted every key it did not know about —
// including provider blocks and any future method. The screen that drives this only owns the methods it
// renders, so the server merges per top-level key and leaves everything else exactly as it found it.
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
		var patch map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&patch); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "body must be a JSON object")
			return
		}
		if err := validateAuthMethodsPatch(patch); err != nil {
			jsonErr(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		ctx, cancel := dbCtx(req)
		defer cancel()

		// Read-modify-write inside one transaction: two operators saving different methods at the same
		// moment must not lose one of the changes.
		tx, err := s.db.Begin(ctx)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "auth_methods update failed")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		var currentRaw []byte
		if err := tx.QueryRow(ctx,
			`SELECT auth_methods FROM tenants WHERE id = $1 FOR UPDATE`, s.tenantID).Scan(&currentRaw); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "auth_methods load failed")
			return
		}
		merged := map[string]json.RawMessage{}
		if len(currentRaw) > 0 {
			if err := json.Unmarshal(currentRaw, &merged); err != nil {
				// Refuse rather than overwrite: a column that does not parse may hold configuration this
				// build does not understand, and replacing it would destroy it.
				jsonErr(w, http.StatusConflict, "conflict",
					"the stored sign-in configuration could not be read; refusing to overwrite it")
				return
			}
		}
		for k, v := range patch {
			merged[k] = v
		}
		out, _ := json.Marshal(merged)
		if _, err := tx.Exec(ctx,
			`UPDATE tenants SET auth_methods = $1::jsonb, updated_at = now() WHERE id = $2`,
			string(out), s.tenantID); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "auth_methods update failed")
			return
		}
		if err := tx.Commit(ctx); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "auth_methods update failed")
			return
		}
		// The audit records WHICH methods were touched, never the whole blob: it may carry provider
		// identifiers, and an audit row is not the place to copy them.
		changed := make([]string, 0, len(patch))
		for k := range patch {
			changed = append(changed, k)
		}
		sort.Strings(changed)
		s.audit(req, "auth_methods.updated", "tenant", s.tenantID, map[string]any{"methods_changed": changed})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
	return r
}

// validateAuthMethodsPatch refuses a patch the portal could not act on, so a bad value is reported here
// rather than becoming a sign-in method that silently never works.
func validateAuthMethodsPatch(patch map[string]json.RawMessage) error {
	rawPMS, ok := patch["pms"]
	if !ok {
		return nil
	}
	var cfg struct {
		Enabled bool   `json:"enabled"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(rawPMS, &cfg); err != nil {
		return errors.New("pms must be an object")
	}
	if !cfg.Enabled {
		return nil // a disabled method needs no mode
	}
	// "either" stays ACCEPTED so an existing stored configuration can be saved back unchanged; it is simply
	// not offered as a new choice.
	if cfg.Mode == "either" {
		return nil
	}
	if !pmsSignInModes[cfg.Mode] {
		return errors.New("pms.mode must be room_lastname, room_firstname or room_reservation")
	}
	return nil
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
		// AN UNASSIGNED APPLIANCE HAS NO TENANT, AND THAT IS NOT AN INTERNAL ERROR.
		//
		// Before enrollment/claim/signed assignment, s.tenantID is empty. This query then asks Postgres to
		// compare a uuid column against '', which fails, and the operator was told "branding load failed" --
		// a message that describes a broken system rather than an unconfigured one, on a factory-clean
		// appliance where being unconfigured is the expected state.
		if s.tenantID == "" {
			writeAwaitingAssignment(w, "portal branding")
			return
		}
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

// PAYMENTS ARE NOT SERVED FROM HERE ANY MORE.
//
// This was a read-only list over public.payments -- a Stripe-session record keyed to a superseded voucher and
// access plan. The current financial surface is the Phase-4 one in resources_phase4_finops.go, over
// iam_v2.payment_transactions and iam_v2.v_financial_settlements, which is where refunds, chargebacks and
// settlement state actually live. Keeping a second, thinner list beside it meant an operator could read a
// payment history that the financial authority did not recognise.

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

		// COUNTED FROM THE SINGLE SESSION AUTHORITY. These read iam_v2.sessions; they used to read
		// public.sessions and public.vouchers, which is why an operator watching this page during the
		// IAM-v2 trial saw zero activity while guests were online.
		var active, today, sess7d, vUnused, vActive int64
		var upToday, downToday int64
		_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.sessions
		     WHERE tenant_id=$1 AND state = 'active'`, s.tenantID).Scan(&active)
		_ = s.db.QueryRow(ctx, `
            SELECT count(*), COALESCE(sum(bytes_up),0), COALESCE(sum(bytes_down),0)
              FROM iam_v2.sessions WHERE tenant_id=$1 AND started >= date_trunc('day', now())
        `, s.tenantID).Scan(&today, &upToday, &downToday)
		_ = s.db.QueryRow(ctx,
			`SELECT count(*) FROM iam_v2.sessions
			  WHERE tenant_id=$1 AND started >= now() - interval '7 days'`, s.tenantID).Scan(&sess7d)
		// iam_v2 vouchers carry a status rather than the legacy state vocabulary: an unredeemed voucher is
		// ISSUED, and one that has been redeemed is REDEEMED. "active" in the old sense -- a voucher with a
		// live session -- is a property of the entitlement now, not of the voucher.
		_ = s.db.QueryRow(ctx, `
		    SELECT count(*) FILTER (WHERE status = 'ISSUED'),
		           count(*) FILTER (WHERE status = 'REDEEMED')
		      FROM iam_v2.vouchers WHERE tenant_id=$1`, s.tenantID).Scan(&vUnused, &vActive)

		out["active_sessions"] = active
		out["sessions_today"] = today
		out["bytes_up_today"] = upToday
		out["bytes_down_today"] = downToday
		out["sessions_7d"] = sess7d
		out["vouchers_unused"] = vUnused
		out["vouchers_active"] = vActive

		// TOP PACKAGES, not top legacy plans. The name kept its wire key so the dashboard card does not
		// break, but the thing being counted is the internet package an entitlement was granted from --
		// the current commercial object -- rather than a ticket_template.
		type topPlan struct {
			TemplateID string `json:"template_id"`
			Name       string `json:"name"`
			Sessions   int64  `json:"sessions"`
		}
		var top []topPlan
		rows, err := s.db.Query(ctx, `
            SELECT p.id::text, p.name, count(se.id) AS n
              FROM iam_v2.sessions se
              JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
              JOIN iam_v2.internet_packages p ON p.id = e.package_id
             WHERE se.tenant_id = $1 AND se.started >= now() - interval '7 days'
             GROUP BY p.id, p.name ORDER BY n DESC LIMIT 5
        `, s.tenantID)
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
