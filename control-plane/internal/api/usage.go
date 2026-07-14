package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// -----------------------------------------------------------------------------
// Shared helpers
// -----------------------------------------------------------------------------

// allowedBuckets maps the short `?bucket=` form to a Postgres interval literal
// safe to interpolate (we do NOT pass user input straight into SQL).
var allowedBuckets = map[string]string{
	"1m":  "1 minute",
	"5m":  "5 minutes",
	"15m": "15 minutes",
	"1h":  "1 hour",
	"6h":  "6 hours",
	"1d":  "1 day",
	"1w":  "7 days",
}

func parseBucket(s string) (string, string, error) {
	if s == "" {
		return "1h", allowedBuckets["1h"], nil
	}
	iv, ok := allowedBuckets[s]
	if !ok {
		return "", "", fmt.Errorf("invalid bucket; allowed: 1m,5m,15m,1h,6h,1d,1w")
	}
	return s, iv, nil
}

func parseTZ(r *http.Request) (*time.Location, string, error) {
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, tz, fmt.Errorf("invalid tz %q", tz)
	}
	return loc, tz, nil
}

// parseRange returns [from,to). If either is missing, defaults to "this month"
// anchored in tz.
func parseRange(r *http.Request, loc *time.Location) (time.Time, time.Time, error) {
	q := r.URL.Query()
	now := time.Now().In(loc)
	defaultFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	defaultTo := now
	from := defaultFrom
	to := defaultTo
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return from, to, fmt.Errorf("invalid from")
		}
		from = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return from, to, fmt.Errorf("invalid to")
		}
		to = t
	}
	if !from.Before(to) {
		return from, to, fmt.Errorf("from must precede to")
	}
	return from, to, nil
}

func parseIntQ(r *http.Request, key string, def, min, max int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// -----------------------------------------------------------------------------
// Routes
// -----------------------------------------------------------------------------

// UsageRoutes is mounted at /v1/tenants/{tenantID}/usage from TenantsRoutes.
func (b *Base) UsageRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/timeseries", b.usageTimeseries)
	r.Get("/summary", b.usageSummary)
	r.Get("/top-sites", b.usageTopSites)
	r.Get("/top-appliances", b.usageTopAppliances)
	return r
}

// -----------------------------------------------------------------------------
// Timeseries
// -----------------------------------------------------------------------------

type TimeseriesPoint struct {
	Bucket         time.Time `json:"bucket"`
	BytesUp        int64     `json:"bytes_up"`
	BytesDown      int64     `json:"bytes_down"`
	ActiveSessions int64     `json:"active_sessions"`
}

type TimeseriesResp struct {
	TZ     string            `json:"tz"`
	Bucket string            `json:"bucket"`
	From   time.Time         `json:"from"`
	To     time.Time         `json:"to"`
	Source string            `json:"source"`
	Scope  string            `json:"scope"`
	Totals TimeseriesTotals  `json:"totals"`
	Points []TimeseriesPoint `json:"points"`
}

type TimeseriesTotals struct {
	BytesUp    int64 `json:"bytes_up"`
	BytesDown  int64 `json:"bytes_down"`
	TotalBytes int64 `json:"total_bytes"`
}

func (b *Base) usageTimeseries(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	if !b.ensureTenantAccess(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	loc, tz, err := parseTZ(r)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	from, to, err := parseRange(r, loc)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	bucketKey, bucketIv, err := parseBucket(r.URL.Query().Get("bucket"))
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}

	ctx, cancel := DBCtx(r)
	defer cancel()

	// Scope enforcement (site/appliance validated + site-operator restricted).
	siteID, applID, scope, ok := b.resolveUsageScope(ctx, w, r, tenantID)
	if !ok {
		return
	}

	// Historical chart from SANITIZED telemetry only (fleet_telemetry, kind=
	// 'usage'). No raw sessions/accounting. bucketIv is a whitelisted literal.
	query := fmt.Sprintf(`
        SELECT time_bucket(INTERVAL '%s', ft.ts, $4) AS bucket,
               COALESCE(SUM((payload->>'bytes_up_today')::bigint),0),
               COALESCE(SUM((payload->>'bytes_down_today')::bigint),0),
               COALESCE(SUM((payload->>'active_sessions')::bigint),0)
          FROM fleet_telemetry ft
         WHERE ft.tenant_id = $1 AND ft.kind = 'usage'
           AND ft.ts >= $2 AND ft.ts < $3
           AND ($5::uuid IS NULL OR ft.site_id = $5)
           AND ($6::uuid IS NULL OR ft.appliance_id = $6)
         GROUP BY bucket
         ORDER BY bucket ASC
    `, bucketIv)

	rows, err := b.DB.Query(ctx, query, tenantID, from, to, tz, nullUUID(siteID), nullUUID(applID))
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "telemetry timeseries failed")
		return
	}
	defer rows.Close()

	points := []TimeseriesPoint{}
	var totalUp, totalDown int64
	for rows.Next() {
		var p TimeseriesPoint
		if err := rows.Scan(&p.Bucket, &p.BytesUp, &p.BytesDown, &p.ActiveSessions); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		totalUp += p.BytesUp
		totalDown += p.BytesDown
		points = append(points, p)
	}
	WriteJSON(w, http.StatusOK, TimeseriesResp{
		TZ:     tz,
		Bucket: bucketKey,
		From:   from,
		To:     to,
		Source: "fleet_telemetry(kind=usage)",
		Scope:  scope,
		Totals: TimeseriesTotals{
			BytesUp:    totalUp,
			BytesDown:  totalDown,
			TotalBytes: totalUp + totalDown,
		},
		Points: points,
	})
}

// -----------------------------------------------------------------------------
// Summary
// -----------------------------------------------------------------------------

type SummaryResp struct {
	TZ             string    `json:"tz"`
	PeriodStart    time.Time `json:"period_start"`
	PeriodEnd      time.Time `json:"period_end"`
	BytesUp        int64     `json:"bytes_up"`
	BytesDown      int64     `json:"bytes_down"`
	TotalBytes     int64     `json:"total_bytes"`
	ActiveSessions int64     `json:"active_sessions"`
	SessionsToday  int64     `json:"sessions_today"`
	CapBytes       *int64    `json:"cap_bytes,omitempty"`
	CapUsedPercent *float64  `json:"cap_used_percent,omitempty"`
	// Source documents that these figures come from SANITIZED telemetry
	// (fleet_telemetry, kind='usage'), never the legacy raw sessions table.
	Source string `json:"source"`
	// Scope is the effective scope the numbers were computed for.
	Scope string `json:"scope"`
}

// nullUUID makes an empty string a SQL NULL so `$n::uuid IS NULL` filters work.
func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// resolveUsageScope validates optional ?site_id / ?appliance_id against the
// tenant and returns the effective scope string. A site/appliance that is not
// part of the tenant yields 403 (no silent cross-scope read), enforcing scope
// in the backend rather than via hidden UI state.
func (b *Base) resolveUsageScope(ctx context.Context, w http.ResponseWriter, r *http.Request, tenantID string) (siteID, applID, scope string, ok bool) {
	sess := auth.FromContext(r.Context())
	siteID = r.URL.Query().Get("site_id")
	applID = r.URL.Query().Get("appliance_id")
	if applID != "" {
		var tid, sid string
		if err := b.DB.QueryRow(ctx, `SELECT tenant_id::text, site_id::text FROM appliances WHERE id=$1`, applID).Scan(&tid, &sid); err != nil || tid != tenantID {
			Fail(w, r, http.StatusForbidden, CodeForbidden, "appliance not in tenant scope")
			return "", "", "", false
		}
		if !sess.EnsureSiteAccess(sid) {
			Fail(w, r, http.StatusForbidden, CodeForbidden, "appliance not in your site scope")
			return "", "", "", false
		}
		if siteID == "" {
			siteID = sid
		}
		return siteID, applID, "appliance:" + applID, true
	}
	if siteID != "" {
		var tid string
		if err := b.DB.QueryRow(ctx, `SELECT tenant_id::text FROM sites WHERE id=$1`, siteID).Scan(&tid); err != nil || tid != tenantID {
			Fail(w, r, http.StatusForbidden, CodeForbidden, "site not in tenant scope")
			return "", "", "", false
		}
		if !sess.EnsureSiteAccess(siteID) {
			Fail(w, r, http.StatusForbidden, CodeForbidden, "site not in your site scope")
			return "", "", "", false
		}
		return siteID, "", "site:" + siteID, true
	}
	// No explicit site filter: a site-bound operator (not tenant-wide) is
	// implicitly restricted to their own site(s) — do not leak the whole tenant.
	if sess != nil && !sess.IsSuperAdmin && !sess.TenantWide && len(sess.SiteIDs) > 0 {
		return sess.SiteIDs[0], "", "site:" + sess.SiteIDs[0], true
	}
	return "", "", "tenant:" + tenantID, true
}

func (b *Base) usageSummary(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	if !b.ensureTenantAccess(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	loc, tz, err := parseTZ(r)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	now := time.Now().In(loc)
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	periodEnd := now

	ctx, cancel := DBCtx(r)
	defer cancel()

	// Effective scope: optional site_id/appliance_id narrow the tenant view.
	// They must belong to this tenant, else 403 (no silent cross-scope leak).
	siteID, applID, scope, ok := b.resolveUsageScope(ctx, w, r, tenantID)
	if !ok {
		return
	}

	resp := SummaryResp{
		TZ:          tz,
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
		Source:      "fleet_telemetry(kind=usage)", // SANITIZED — never raw sessions
		Scope:       scope,
	}

	// All figures come from SANITIZED per-appliance telemetry: the latest
	// 'usage' reading per appliance in scope, summed. No raw sessions/accounting.
	if err := b.DB.QueryRow(ctx, `
        WITH latest AS (
          SELECT DISTINCT ON (appliance_id) appliance_id, payload
            FROM fleet_telemetry
           WHERE tenant_id = $1 AND kind = 'usage'
             AND ($2::uuid IS NULL OR site_id = $2)
             AND ($3::uuid IS NULL OR appliance_id = $3)
           ORDER BY appliance_id, ts DESC
        )
        SELECT COALESCE(SUM((payload->>'active_sessions')::bigint),0),
               COALESCE(SUM((payload->>'sessions_today')::bigint),0),
               COALESCE(SUM((payload->>'bytes_up_today')::bigint),0),
               COALESCE(SUM((payload->>'bytes_down_today')::bigint),0)
          FROM latest
    `, tenantID, nullUUID(siteID), nullUUID(applID)).
		Scan(&resp.ActiveSessions, &resp.SessionsToday, &resp.BytesUp, &resp.BytesDown); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "telemetry summary failed")
		return
	}
	resp.TotalBytes = resp.BytesUp + resp.BytesDown

	// Cap — from plan limit max_bandwidth_gb_per_month.
	capGB, err := GetIntLimit(ctx, b.DB, tenantID, "max_bandwidth_gb_per_month")
	if err == nil && capGB > 0 {
		capBytes := capGB * 1024 * 1024 * 1024
		resp.CapBytes = &capBytes
		pct := 0.0
		if capBytes > 0 {
			pct = float64(resp.TotalBytes) / float64(capBytes) * 100.0
		}
		resp.CapUsedPercent = &pct
	}
	WriteJSON(w, http.StatusOK, resp)
}

// -----------------------------------------------------------------------------
// Top-sites / Top-appliances
// -----------------------------------------------------------------------------

type TopRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	BytesUp    int64  `json:"bytes_up"`
	BytesDown  int64  `json:"bytes_down"`
	TotalBytes int64  `json:"total_bytes"`
}

type TopResp struct {
	TZ   string    `json:"tz"`
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Rows []TopRow  `json:"rows"`
}

func (b *Base) usageTopSites(w http.ResponseWriter, r *http.Request)      { b.topBy(w, r, "site") }
func (b *Base) usageTopAppliances(w http.ResponseWriter, r *http.Request) { b.topBy(w, r, "appliance") }

// topBy handles both top-sites and top-appliances — same query shape with a
// different GROUP BY / join table.
func (b *Base) topBy(w http.ResponseWriter, r *http.Request, kind string) {
	tenantID := chi.URLParam(r, "tenantID")
	if !b.ensureTenantAccess(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	loc, tz, err := parseTZ(r)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	from, to, err := parseRange(r, loc)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	topN := parseIntQ(r, "top_n", 10, 1, 100)

	ctx, cancel := DBCtx(r)
	defer cancel()

	// SANITIZED telemetry only: latest 'usage' reading per appliance in the
	// window, grouped by site/appliance. No raw sessions/accounting.
	var sql string
	switch kind {
	case "site":
		sql = `
        WITH latest AS (
          SELECT DISTINCT ON (appliance_id) appliance_id, site_id, payload
            FROM fleet_telemetry
           WHERE tenant_id=$1 AND kind='usage' AND ts>=$2 AND ts<$3
           ORDER BY appliance_id, ts DESC
        )
        SELECT s.id::text, COALESCE(s.name,''),
               COALESCE(SUM((l.payload->>'bytes_up_today')::bigint),0),
               COALESCE(SUM((l.payload->>'bytes_down_today')::bigint),0)
          FROM latest l JOIN sites s ON s.id = l.site_id
         GROUP BY s.id, s.name
         ORDER BY SUM((l.payload->>'bytes_up_today')::bigint + (l.payload->>'bytes_down_today')::bigint) DESC
         LIMIT $4`
	case "appliance":
		sql = `
        WITH latest AS (
          SELECT DISTINCT ON (appliance_id) appliance_id, payload
            FROM fleet_telemetry
           WHERE tenant_id=$1 AND kind='usage' AND ts>=$2 AND ts<$3
           ORDER BY appliance_id, ts DESC
        )
        SELECT a.id::text, COALESCE(a.name,''),
               COALESCE(SUM((l.payload->>'bytes_up_today')::bigint),0),
               COALESCE(SUM((l.payload->>'bytes_down_today')::bigint),0)
          FROM latest l JOIN appliances a ON a.id = l.appliance_id
         GROUP BY a.id, a.name
         ORDER BY SUM((l.payload->>'bytes_up_today')::bigint + (l.payload->>'bytes_down_today')::bigint) DESC
         LIMIT $4`
	default:
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "bad kind")
		return
	}

	rows, err := b.DB.Query(ctx, sql, tenantID, from, to, topN)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []TopRow
	for rows.Next() {
		var tr TopRow
		if err := rows.Scan(&tr.ID, &tr.Name, &tr.BytesUp, &tr.BytesDown); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		tr.TotalBytes = tr.BytesUp + tr.BytesDown
		out = append(out, tr)
	}
	if out == nil {
		out = []TopRow{}
	}
	WriteJSON(w, http.StatusOK, TopResp{TZ: tz, From: from, To: to, Rows: out})
}
