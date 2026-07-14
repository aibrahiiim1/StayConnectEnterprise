package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// FleetBase serves /cloud/v1/fleet — the vendor/group view of appliance
// health. Data comes from the appliance registry plus the aggregated
// fleet_telemetry pushed by sync agents. No guest data appears here.
type FleetBase struct {
	*Base
}

type FleetAppliance struct {
	ApplianceID  string          `json:"appliance_id"`
	TenantID     string          `json:"tenant_id"`
	SiteID       *string         `json:"site_id,omitempty"`
	Name         string          `json:"name"`
	Serial       string          `json:"serial"`
	Status       string          `json:"status"`
	Version      *string         `json:"version,omitempty"`
	LastSeenAt   *time.Time      `json:"last_seen_at,omitempty"`
	LicenseState *string         `json:"license_status,omitempty"`
	LicenseValid *time.Time      `json:"license_valid_until,omitempty"`
	LastHealth   json.RawMessage `json:"last_health,omitempty"`
	// Latest 'usage' telemetry (active_sessions, ...) + when it was reported —
	// feeds the Central license usage columns (current online guests, last sync).
	LastUsage   json.RawMessage `json:"last_usage,omitempty"`
	LastUsageAt *time.Time      `json:"last_usage_at,omitempty"`
}

func (b *FleetBase) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", b.list)
	r.Get("/{applianceID}/telemetry", b.telemetry)
	return r
}

func (b *FleetBase) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	if tenantID == "" {
		if s := auth.FromContext(r.Context()); s == nil || !s.IsSuperAdmin {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant scope required")
			return
		}
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	rows, err := b.DB.Query(ctx, `
        SELECT a.id, a.tenant_id, a.site_id::text, a.name, a.serial, a.status,
               a.version, a.last_seen_at,
               l.status, l.valid_until,
               (SELECT payload FROM fleet_telemetry ft
                 WHERE ft.appliance_id = a.id AND ft.kind = 'health'
                 ORDER BY ft.ts DESC LIMIT 1),
               (SELECT payload FROM fleet_telemetry ft
                 WHERE ft.appliance_id = a.id AND ft.kind = 'usage'
                 ORDER BY ft.ts DESC LIMIT 1),
               (SELECT ts FROM fleet_telemetry ft
                 WHERE ft.appliance_id = a.id AND ft.kind = 'usage'
                 ORDER BY ft.ts DESC LIMIT 1)
          FROM appliances a
          LEFT JOIN licenses l
                 ON l.site_id = a.site_id AND l.status IN ('active','suspended')
         WHERE ($1 = '' OR a.tenant_id::text = $1)
         ORDER BY a.tenant_id, a.site_id, a.name
    `, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []FleetAppliance
	for rows.Next() {
		var f FleetAppliance
		if err := rows.Scan(&f.ApplianceID, &f.TenantID, &f.SiteID, &f.Name, &f.Serial,
			&f.Status, &f.Version, &f.LastSeenAt, &f.LicenseState, &f.LicenseValid,
			&f.LastHealth, &f.LastUsage, &f.LastUsageAt); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, f)
	}
	WriteList(w, out, ListMeta{})
}

type TelemetryRow struct {
	TS      time.Time       `json:"ts"`
	Kind    string          `json:"kind"`
	Seq     int64           `json:"seq"`
	Payload json.RawMessage `json:"payload"`
}

func (b *FleetBase) telemetry(w http.ResponseWriter, r *http.Request) {
	applianceID := chi.URLParam(r, "applianceID")
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()

	// Tenant scoping: the appliance must be visible to the caller.
	var owner string
	if err := b.DB.QueryRow(ctx,
		`SELECT tenant_id FROM appliances WHERE id = $1`, applianceID).Scan(&owner); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	s := auth.FromContext(r.Context())
	if (s == nil || !s.IsSuperAdmin) && owner != tenantID {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}

	q := `SELECT ts, kind, seq, payload FROM fleet_telemetry WHERE appliance_id = $1`
	args := []any{applianceID}
	if kind := r.URL.Query().Get("kind"); kind != "" {
		q += ` AND kind = $2`
		args = append(args, kind)
	}
	q += ` ORDER BY ts DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, ParseLimit(r, 100, 1000))

	rows, err := b.DB.Query(ctx, q, args...)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []TelemetryRow
	for rows.Next() {
		var t TelemetryRow
		if err := rows.Scan(&t.TS, &t.Kind, &t.Seq, &t.Payload); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, t)
	}
	WriteList(w, out, ListMeta{})
}
