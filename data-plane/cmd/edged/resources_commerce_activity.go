package main

// PACKAGE ACTIVITY — what the internet packages actually did, for the operator who runs them.
//
// WHY A NEW READ PATH. The guest-activity listing starts from OFFER QUOTES, and most access on this appliance
// is never quoted: a voucher, a guest account's automatic grant, checkout grace, emergency grace and a staff
// grant all create a PURCHASE with no quote (purchases.trigger says which). Starting from quotes made every
// one of those grants invisible and made every visible row claim the guest "was offered" something. So this
// view starts from PURCHASES -- the record of every grant, whatever produced it -- and walks forward:
//
//     purchases  ->  entitlements (the access it produced: status, activation, end, usage counters)
//                ->  sessions     (how the guest actually connected, what it moved, on how many devices)
//     and, only where a quote genuinely exists, offer_quotes (so "offered" is said only when it is true).
//
// NOTHING HERE IS INVENTED. purchases carry no timestamp of their own, so a grant is placed in time by the
// moment its access was activated or, failing that, the moment its quote was taken. A purchase with neither
// cannot be placed in a range at all; it is counted and reported as "undated" rather than given a time. An
// offer quote records when it expires and when it was taken, never when it was made, so no offer time is shown.
//
// PRIVACY is the same as the screens this replaces: a room always travels with the PMS interface that gives
// it meaning, and no guest name, account username, voucher code, device address or credential is returned.
// The search matches room and reservation only.
//
// PRIVILEGES: every relation read here is granted SELECT to svc_edged by the Gate-P allowlist -- purchases,
// entitlements, offer_quotes, sessions, stays, pms_interfaces, internet_packages, internet_package_revisions,
// service_plan_revisions. commerce_activity_test.go checks both the tables and the columns against the
// production baseline so a typo or an ungranted column is a test failure rather than a 500 on the appliance.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// activitySources are purchases_trigger_check, verbatim, with the words an operator reads.
var activitySources = map[string]string{
	"GUEST_SELECTION":      "Chosen on the portal",
	"VOUCHER_REDEMPTION":   "Voucher",
	"ACCOUNT_AUTO_GRANT":   "Guest account",
	"OTP_SOCIAL_DEFAULT":   "Email, phone or social sign-in",
	"CHECKOUT_GRACE":       "After check-out grace",
	"EMERGENCY_GRACE":      "Emergency grace",
	"POST_STAY_CONVERSION": "After-stay access",
	"CROSS_PMS_TRANSFER":   "Moved between property systems",
	"ADMIN_GRANT":          "Granted by staff",
	"RENEWAL":              "Renewal",
}

func sourceLabel(trigger string) string {
	if l, ok := activitySources[trigger]; ok {
		return l
	}
	return strings.ToLower(strings.ReplaceAll(trigger, "_", " "))
}

const (
	activityDefaultLimit = 50
	activityMaxLimit     = 200
	activityMaxOffset    = 1_000_000
	activityMaxSpan      = 400 * 24 * time.Hour
	activityMaxQueryLen  = 64
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// activityFilter is everything the operator can narrow the view by. Tenant and site are NOT here: they come
// from the appliance's own assignment (s.tenantID / s.siteID) and nothing in a request can move them.
type activityFilter struct {
	From, To  time.Time
	PackageID string
	Status    string // "" | active | ended | other
	Source    string // "" | a purchases.trigger value
	Q         string // room or reservation
	Limit     int
	Offset    int
}

// parseActivityFilter reads and bounds the query string. A value it cannot use is a 400 naming the parameter,
// never a silently different query.
func parseActivityFilter(q url.Values, now time.Time) (activityFilter, error) {
	f := activityFilter{To: now.UTC(), Limit: activityDefaultLimit}
	switch rng := q.Get("range"); rng {
	case "", "7d":
		f.From = f.To.Add(-7 * 24 * time.Hour)
	case "24h":
		f.From = f.To.Add(-24 * time.Hour)
	case "30d":
		f.From = f.To.Add(-30 * 24 * time.Hour)
	case "custom":
		from, err1 := time.Parse(time.RFC3339, q.Get("from"))
		to, err2 := time.Parse(time.RFC3339, q.Get("to"))
		if err1 != nil || err2 != nil {
			return f, errors.New("a custom range needs from and to as RFC 3339 times")
		}
		f.From, f.To = from.UTC(), to.UTC()
	default:
		return f, fmt.Errorf("range must be 24h, 7d, 30d or custom, not %q", rng)
	}
	if !f.From.Before(f.To) {
		return f, errors.New("the range must start before it ends")
	}
	if f.To.Sub(f.From) > activityMaxSpan {
		return f, errors.New("the range may cover at most 400 days")
	}
	if v := q.Get("package_id"); v != "" {
		if !uuidRe.MatchString(v) {
			return f, errors.New("package_id is not a package identifier")
		}
		f.PackageID = v
	}
	switch v := q.Get("status"); v {
	case "", "all":
	case "active", "ended", "other":
		f.Status = v
	default:
		return f, fmt.Errorf("status must be active, ended or other, not %q", v)
	}
	if v := q.Get("source"); v != "" && v != "all" {
		if _, ok := activitySources[v]; !ok {
			return f, fmt.Errorf("unknown source %q", v)
		}
		f.Source = v
	}
	f.Q = strings.TrimSpace(q.Get("q"))
	if len(f.Q) > activityMaxQueryLen {
		f.Q = f.Q[:activityMaxQueryLen]
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return f, errors.New("limit must be a positive number")
		}
		f.Limit = min(n, activityMaxLimit)
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return f, errors.New("offset must be zero or more")
		}
		f.Offset = min(n, activityMaxOffset)
	}
	return f, nil
}

// likeEscape makes the operator's search text literal inside ILIKE, so "4_2" does not match "402".
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// activityBaseSQL is the grant set with the operator's filters applied, as two CTEs: `act` (every purchase at
// this site, joined forward) and `f` (the filtered rows). $1 tenant, $2 site, $3 from, $4 to; further
// parameters follow in args. withStatus=false drops the status filter, which is how the status chip counts are
// computed over everything else the operator chose.
func activityBaseSQL(f activityFilter, tenantID, siteID string, withStatus bool) (string, []any) {
	args := []any{tenantID, siteID, f.From, f.To}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }

	where := []string{
		// A grant is in the range when its access overlaps it: it began before the range ended, and it had not
		// ended before the range began. Access that is still live has no end yet.
		"a.occurred_at IS NOT NULL",
		"a.occurred_at < $4",
		"COALESCE(a.terminated_at, CASE WHEN a.status IN ('ACTIVE','SUSPENDED','PENDING') THEN 'infinity'::timestamptz END, a.occurred_at) >= $3",
	}
	if f.PackageID != "" {
		where = append(where, "a.package_id = "+arg(f.PackageID)+"::uuid")
	}
	if f.Source != "" {
		where = append(where, "a.trigger = "+arg(f.Source))
	}
	if f.Q != "" {
		p := arg(likeEscape(f.Q))
		where = append(where, "(a.room ILIKE '%' || "+p+" || '%' OR a.reservation ILIKE '%' || "+p+" || '%')")
	}
	if withStatus {
		switch f.Status {
		case "active":
			where = append(where, "a.status = 'ACTIVE'")
		case "ended":
			where = append(where, "a.status = 'TERMINATED'")
		case "other":
			where = append(where, "(a.entitlement_id IS NULL OR a.status IN ('PENDING','SUSPENDED'))")
		}
	}

	sql := `
	WITH act AS (
	  SELECT p.id AS purchase_id, p.trigger, p.state AS purchase_state, p.amount_minor,
	         CASE WHEN p.currency IS NOT NULL THEN p.currency ELSE ipr.currency END AS currency,
	         CASE WHEN p.currency IS NOT NULL THEN p.currency_exponent ELSE ipr.currency_exponent END AS currency_exponent,
	         ipr.id AS revision_id, ipr.revision_no, ipr.package_type,
	         NULLIF(ipr.display->>'name', '') AS package_name,
	         ip.id AS package_id, ip.code AS package_code, ip.is_system,
	         e.id AS entitlement_id, e.status, e.terminal_reason, e.activated_at, e.terminated_at,
	         e.consumed_data_bytes, e.consumed_online_seconds,
	         COALESCE(e.data_quota_bytes, spr.data_quota_bytes) AS quota_bytes,
	         e.is_emergency_grace,
	         CASE WHEN e.guest_account_id IS NOT NULL THEN 'GUEST_ACCOUNT'
	              WHEN e.voucher_id IS NOT NULL THEN 'VOUCHER'
	              WHEN e.guest_principal_id IS NOT NULL THEN 'SIGN_IN'
	              WHEN COALESCE(e.stay_id, p.stay_id) IS NOT NULL THEN 'STAY'
	              ELSE '' END AS subject_kind,
	         st.id AS stay_id, st.normalized_room_number AS room, st.external_reservation_id AS reservation,
	         pi.display_label AS pms_interface,
	         (q.id IS NOT NULL) AS had_offer, q.consumed_at AS offer_taken_at, q.expires_at AS offer_expires_at,
	         COALESCE(e.activated_at, q.consumed_at) AS occurred_at,
	         spr.name AS plan_name
	    FROM iam_v2.purchases p
	    JOIN iam_v2.internet_package_revisions ipr
	      ON ipr.id = p.package_revision_id AND ipr.tenant_id = p.tenant_id AND ipr.site_id = p.site_id
	    JOIN iam_v2.internet_packages ip
	      ON ip.id = ipr.package_id AND ip.tenant_id = p.tenant_id AND ip.site_id = p.site_id
	    LEFT JOIN iam_v2.entitlements e
	      ON e.purchase_id = p.id AND e.tenant_id = p.tenant_id AND e.site_id = p.site_id
	    LEFT JOIN iam_v2.offer_quotes q
	      ON q.id = p.offer_quote_id AND q.tenant_id = p.tenant_id AND q.site_id = p.site_id
	    LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
	    LEFT JOIN iam_v2.stays st
	      ON st.id = COALESCE(e.stay_id, p.stay_id) AND st.tenant_id = p.tenant_id AND st.site_id = p.site_id
	    LEFT JOIN iam_v2.pms_interfaces pi ON pi.id = st.pms_interface_id
	   WHERE p.tenant_id = $1 AND p.site_id = $2
	), f AS (
	  SELECT a.* FROM act a
	   WHERE ` + strings.Join(where, "\n\t     AND ") + `
	)`
	return sql, args
}

// activityListSQL pages the filtered set, newest first, and attaches each page row's session totals.
//
// The session aggregate runs for the PAGE only, not for every matching grant: the window count is taken first,
// the page is cut, and only then are sessions summed.
func activityListSQL(f activityFilter, tenantID, siteID string) (string, []any) {
	base, args := activityBaseSQL(f, tenantID, siteID, true)
	args = append(args, f.Limit, f.Offset)
	lim, off := "$"+strconv.Itoa(len(args)-1), "$"+strconv.Itoa(len(args))
	return base + `, page AS (
	  SELECT f.*, count(*) OVER () AS total FROM f
	   ORDER BY f.occurred_at DESC NULLS LAST, f.purchase_id
	   LIMIT ` + lim + ` OFFSET ` + off + `
	)
	SELECT page.purchase_id::text, COALESCE(page.entitlement_id::text, ''),
	       page.package_id::text, page.package_code, COALESCE(page.package_name, ''), page.is_system,
	       page.revision_id::text, page.revision_no, page.package_type,
	       page.amount_minor, COALESCE(page.currency, ''), page.currency_exponent::int,
	       page.trigger, page.purchase_state, COALESCE(page.status, ''), COALESCE(page.terminal_reason, ''),
	       COALESCE(page.is_emergency_grace, false), page.subject_kind,
	       COALESCE(page.stay_id::text, ''), COALESCE(page.room, ''), COALESCE(page.pms_interface, ''),
	       COALESCE(page.reservation, ''),
	       page.had_offer, page.offer_taken_at, page.offer_expires_at,
	       page.activated_at, page.terminated_at, page.occurred_at,
	       page.quota_bytes, page.consumed_data_bytes, page.consumed_online_seconds,
	       COALESCE(page.plan_name, ''),
	       COALESCE(ss.sessions, 0), COALESCE(ss.devices, 0),
	       COALESCE(ss.bytes_down, 0), COALESCE(ss.bytes_up, 0),
	       ss.first_started, ss.last_seen, COALESCE(ss.online_now, false), COALESCE(ss.first_method, ''),
	       page.total
	  FROM page
	  LEFT JOIN LATERAL (
	      SELECT count(*)::int AS sessions, count(DISTINCT s.device_id)::int AS devices,
	             sum(s.bytes_down)::bigint AS bytes_down, sum(s.bytes_up)::bigint AS bytes_up,
	             min(s.started) AS first_started, max(COALESCE(s.ended, s.started)) AS last_seen,
	             bool_or(s.state = 'active') AS online_now,
	             (array_agg(s.credential_method ORDER BY s.started))[1] AS first_method
	        FROM iam_v2.sessions s
	       WHERE s.entitlement_id = page.entitlement_id AND s.tenant_id = $1 AND s.site_id = $2
	  ) ss ON page.entitlement_id IS NOT NULL
	 ORDER BY page.occurred_at DESC NULLS LAST, page.purchase_id`, args
}

// activitySummarySQL aggregates the filtered set, ignoring the status filter (status is what the chips count).
// One row: in-range total, started in range, the three status counts, measured bytes, and two JSON rankings.
func activitySummarySQL(f activityFilter, tenantID, siteID string) (string, []any) {
	base, args := activityBaseSQL(f, tenantID, siteID, false)
	return base + `
	SELECT (SELECT count(*) FROM f),
	       (SELECT count(*) FROM f WHERE f.occurred_at >= $3),
	       (SELECT count(*) FROM f WHERE f.status = 'ACTIVE'),
	       (SELECT count(*) FROM f WHERE f.status = 'TERMINATED'),
	       (SELECT count(*) FROM f WHERE f.entitlement_id IS NULL OR f.status IN ('PENDING','SUSPENDED')),
	       (SELECT COALESCE(sum(s.bytes_down + s.bytes_up), 0)::bigint FROM iam_v2.sessions s
	         WHERE s.tenant_id = $1 AND s.site_id = $2 AND s.entitlement_id IN (SELECT entitlement_id FROM f)),
	       (SELECT count(*) FROM iam_v2.entitlements e
	         WHERE e.tenant_id = $1 AND e.site_id = $2 AND e.status = 'ACTIVE'),
	       (SELECT count(*) FROM act WHERE act.occurred_at IS NULL),
	       (SELECT COALESCE(json_agg(x ORDER BY x.grants DESC, x.code), '[]'::json) FROM (
	           SELECT f.package_id::text AS package_id, f.package_code AS code,
	                  COALESCE(max(f.package_name), '') AS name, bool_or(f.is_system) AS is_system,
	                  count(*) AS grants
	             FROM f GROUP BY f.package_id, f.package_code) x),
	       (SELECT COALESCE(json_agg(y ORDER BY y.grants DESC, y.source), '[]'::json) FROM (
	           SELECT f.trigger AS source, count(*) AS grants FROM f GROUP BY f.trigger) y),
	       -- Site-wide, whatever the filters: how many guests each package is serving right now. The package
	       -- list shows it per row, and it is the same count as active_now split by package.
	       (SELECT COALESCE(json_agg(z), '[]'::json) FROM (
	           SELECT ipr.package_id::text AS package_id, count(*) AS grants
	             FROM iam_v2.entitlements e
	             JOIN iam_v2.internet_package_revisions ipr ON ipr.id = e.package_revision_id
	            WHERE e.tenant_id = $1 AND e.site_id = $2 AND e.status = 'ACTIVE'
	            GROUP BY ipr.package_id) z)`, args
}

// ---------------------------------------------------------------------------------------- shapes --------

type activityRow struct {
	PurchaseID        string `json:"purchase_id"`
	EntitlementID     string `json:"entitlement_id,omitempty"`
	PackageID         string `json:"package_id"`
	PackageCode       string `json:"package_code"`
	PackageName       string `json:"package_name"`
	SystemPackage     bool   `json:"system_package,omitempty"`
	PackageRevisionID string `json:"package_revision_id"`
	RevisionNo        int    `json:"revision_no"`
	PackageType       string `json:"package_type,omitempty"`

	// The price the PURCHASE recorded, with the currency and exponent recorded alongside it (the revision's
	// pair when the purchase carries none). CurrencyExponent is nil when neither recorded one.
	PriceMinor       int64  `json:"price_minor"`
	Currency         string `json:"currency,omitempty"`
	CurrencyExponent *int   `json:"currency_exponent,omitempty"`

	Source        string `json:"source"`
	SourceLabel   string `json:"source_label"`
	PurchaseState string `json:"purchase_state"`
	// Status is the entitlement's (PENDING, ACTIVE, SUSPENDED, TERMINATED), or NOT_GRANTED when the purchase
	// produced no access at all -- the case worth investigating.
	Status         string `json:"status"`
	EndReason      string `json:"end_reason,omitempty"`
	EmergencyGrace bool   `json:"emergency_grace,omitempty"`

	// WHO, only as far as the existing screens already say: a stay's room with its PMS interface, or the kind
	// of sign-in. Never a name, a username, a voucher code or a device.
	SignInKind   string `json:"sign_in_kind,omitempty"` // STAY | GUEST_ACCOUNT | VOUCHER | SIGN_IN
	StayID       string `json:"stay_id,omitempty"`
	Room         string `json:"room,omitempty"`
	PMSInterface string `json:"pms_interface,omitempty"`
	Reservation  string `json:"reservation,omitempty"`

	// The offer, ONLY when a quote exists. No offer time: the quote does not record one.
	HadOffer       bool       `json:"had_offer"`
	OfferTakenAt   *time.Time `json:"offer_taken_at,omitempty"`
	OfferExpiresAt *time.Time `json:"offer_expires_at,omitempty"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`

	ServicePlan           string `json:"service_plan,omitempty"`
	QuotaBytes            *int64 `json:"quota_bytes,omitempty"`
	ConsumedDataBytes     *int64 `json:"consumed_data_bytes,omitempty"`
	ConsumedOnlineSeconds *int64 `json:"consumed_online_seconds,omitempty"`

	Sessions       int        `json:"sessions"`
	Devices        int        `json:"devices"`
	BytesDown      int64      `json:"bytes_down"`
	BytesUp        int64      `json:"bytes_up"`
	FirstSessionAt *time.Time `json:"first_session_at,omitempty"`
	LastSessionAt  *time.Time `json:"last_session_at,omitempty"`
	OnlineNow      bool       `json:"online_now"`
	SignInMethod   string     `json:"sign_in_method,omitempty"`

	// UsageHref is where the stay's own usage view lives, when there is a stay to look at.
	UsageHref string `json:"usage_href,omitempty"`
}

type activityPackageCount struct {
	PackageID string `json:"package_id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	IsSystem  bool   `json:"is_system"`
	Grants    int64  `json:"grants"`
}

type activitySourceCount struct {
	Source string `json:"source"`
	Label  string `json:"label"`
	Grants int64  `json:"grants"`
}

type activitySummary struct {
	InRange        int64                  `json:"in_range"`
	StartedInRange int64                  `json:"started_in_range"`
	StatusCounts   map[string]int64       `json:"status_counts"`
	DataBytes      int64                  `json:"data_bytes"`
	ActiveNow      int64                  `json:"active_now"`
	Undated        int64                  `json:"undated"`
	ByPackage      []activityPackageCount `json:"by_package"`
	BySource       []activitySourceCount  `json:"by_source"`
	// ActiveByPackage is site-wide and ignores every filter: guests each package is serving right now.
	ActiveByPackage []activeByPackage `json:"active_by_package"`
}

type activeByPackage struct {
	PackageID string `json:"package_id"`
	Grants    int64  `json:"grants"`
}

type activityResponse struct {
	Data    []activityRow   `json:"data"`
	Meta    activityMeta    `json:"meta"`
	Summary activitySummary `json:"summary"`
	Range   activityRange   `json:"range"`
}

type activityMeta struct {
	Total   int64 `json:"total"`
	Limit   int   `json:"limit"`
	Offset  int   `json:"offset"`
	HasMore bool  `json:"has_more"`
}

type activityRange struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// packageDisplayName is what a row is called when its revision has no display name: the system grace packages
// carry reserved codes nobody should have to read, so they are named by what they are.
func packageDisplayName(name, code string, system bool, trigger string) string {
	if name != "" {
		return name
	}
	if system {
		switch trigger {
		case "EMERGENCY_GRACE":
			return "Emergency grace"
		default:
			return "After check-out grace"
		}
	}
	return code
}

// ------------------------------------------------------------------------------------- handlers --------

func (s *server) listPackageActivity(w http.ResponseWriter, r *http.Request) {
	if !s.commerceCfg.AdminOn() {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	f, err := parseActivityFilter(r.URL.Query(), time.Now())
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	out := activityResponse{Data: []activityRow{}, Range: activityRange{From: f.From, To: f.To}}
	out.Meta.Limit, out.Meta.Offset = f.Limit, f.Offset

	listSQL, listArgs := activityListSQL(f, s.tenantID, s.siteID)
	rows, err := s.db.Query(ctx, listSQL, listArgs...)
	if err != nil {
		slog.Error("package activity read failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "package activity could not be read")
		return
	}
	for rows.Next() {
		var x activityRow
		var status string
		var total int64
		if err := rows.Scan(&x.PurchaseID, &x.EntitlementID,
			&x.PackageID, &x.PackageCode, &x.PackageName, &x.SystemPackage,
			&x.PackageRevisionID, &x.RevisionNo, &x.PackageType,
			&x.PriceMinor, &x.Currency, &x.CurrencyExponent,
			&x.Source, &x.PurchaseState, &status, &x.EndReason,
			&x.EmergencyGrace, &x.SignInKind,
			&x.StayID, &x.Room, &x.PMSInterface, &x.Reservation,
			&x.HadOffer, &x.OfferTakenAt, &x.OfferExpiresAt,
			&x.StartedAt, &x.EndedAt, &x.OccurredAt,
			&x.QuotaBytes, &x.ConsumedDataBytes, &x.ConsumedOnlineSeconds,
			&x.ServicePlan,
			&x.Sessions, &x.Devices, &x.BytesDown, &x.BytesUp,
			&x.FirstSessionAt, &x.LastSessionAt, &x.OnlineNow, &x.SignInMethod,
			&total); err != nil {
			rows.Close()
			slog.Error("package activity scan failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "package activity could not be read")
			return
		}
		finishActivityRow(&x, status)
		out.Meta.Total = total
		out.Data = append(out.Data, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Error("package activity read failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "package activity could not be read")
		return
	}
	if len(out.Data) == 0 && f.Offset > 0 {
		// Past the end: the window count is only known from a row, so ask for it directly rather than report 0.
		cntSQL, cntArgs := activityBaseSQL(f, s.tenantID, s.siteID, true)
		_ = s.db.QueryRow(ctx, cntSQL+` SELECT count(*) FROM f`, cntArgs...).Scan(&out.Meta.Total)
	}
	out.Meta.HasMore = int64(f.Offset+len(out.Data)) < out.Meta.Total

	sumSQL, sumArgs := activitySummarySQL(f, s.tenantID, s.siteID)
	var active, ended, other int64
	var byPkg, bySrc, activeBy []byte
	if err := s.db.QueryRow(ctx, sumSQL, sumArgs...).Scan(
		&out.Summary.InRange, &out.Summary.StartedInRange, &active, &ended, &other,
		&out.Summary.DataBytes, &out.Summary.ActiveNow, &out.Summary.Undated, &byPkg, &bySrc, &activeBy); err != nil {
		slog.Error("package activity summary failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "package activity could not be summarised")
		return
	}
	out.Summary.StatusCounts = map[string]int64{"active": active, "ended": ended, "other": other}
	out.Summary.ByPackage, out.Summary.BySource = decodeActivityRankings(byPkg, bySrc)
	out.Summary.ActiveByPackage = []activeByPackage{}
	_ = json.Unmarshal(activeBy, &out.Summary.ActiveByPackage)
	writeJSON(w, http.StatusOK, out)
}

// finishActivityRow applies the decisions that are the same for every row, and is unit-tested on its own.
func finishActivityRow(x *activityRow, entitlementStatus string) {
	x.SourceLabel = sourceLabel(x.Source)
	x.PackageName = packageDisplayName(x.PackageName, x.PackageCode, x.SystemPackage, x.Source)
	if x.EntitlementID == "" {
		x.Status = "NOT_GRANTED"
	} else {
		x.Status = entitlementStatus
	}
	// "Offered" is said only when a quote exists; without one the offer fields stay empty.
	if !x.HadOffer {
		x.OfferTakenAt, x.OfferExpiresAt = nil, nil
	}
	if x.StayID != "" {
		x.UsageHref = "/usage?stay=" + url.QueryEscape(x.StayID)
	}
}

func decodeActivityRankings(byPkg, bySrc []byte) ([]activityPackageCount, []activitySourceCount) {
	pk := []activityPackageCount{}
	sr := []activitySourceCount{}
	_ = json.Unmarshal(byPkg, &pk)
	_ = json.Unmarshal(bySrc, &sr)
	for i := range pk {
		trigger := ""
		if pk[i].IsSystem && strings.Contains(pk[i].Code, "emergency") {
			trigger = "EMERGENCY_GRACE"
		}
		pk[i].Name = packageDisplayName(pk[i].Name, pk[i].Code, pk[i].IsSystem, trigger)
	}
	for i := range sr {
		sr[i].Label = sourceLabel(sr[i].Source)
	}
	return pk, sr
}

// ---------------------------------------------------------------------------------- deletability ------

func (s *server) getCommercialPackageDeletability(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such package at this property")
		return
	}
	d, disabled, err := s.commerce.PackageDeletability(r.Context(), s.tenantID, s.siteID, id)
	writeDeletability(w, d, disabled, err, "no such package at this property")
}

func (s *server) getServicePlanDeletability(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such service plan at this property")
		return
	}
	d, disabled, err := s.commerce.PlanDeletability(r.Context(), s.tenantID, s.siteID, id)
	writeDeletability(w, d, disabled, err, "no such service plan at this property")
}

func writeDeletability(w http.ResponseWriter, d iamv2.Deletability, disabled bool, err error, notFound string) {
	if disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	if errors.Is(err, iamv2.ErrCommerceNotFound) {
		jsonErr(w, http.StatusNotFound, "not_found", notFound)
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the references could not be read")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ---------------------------------------------------------------------------------- deletion ----------

// deleteCommercialPackage permanently removes a package that nothing has ever used.
//
// The decision is the database's (iam_v2.internet_package_delete_unused, migration 0091): it locks the package,
// re-counts every reference and refuses on any of them, so a guest buying the package a second before this
// runs makes it a 409, never a lost purchase. This handler adds the step-up, the bounded reason and the audit.
func (s *server) deleteCommercialPackage(w http.ResponseWriter, r *http.Request) {
	s.deleteCatalogueItem(w, r, true)
}

// deleteServicePlan permanently removes a service plan no package version and no grant has ever used.
func (s *server) deleteServicePlan(w http.ResponseWriter, r *http.Request) {
	s.deleteCatalogueItem(w, r, false)
}

func (s *server) deleteCatalogueItem(w http.ResponseWriter, r *http.Request, pkg bool) {
	noun, target, action := "service plan", "service_plan", "service_plan.deleted"
	if pkg {
		noun, target, action = "package", "commercial_package", "commercial_package.deleted"
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such "+noun+" at this property")
		return
	}
	var in struct {
		Password string `json:"password"`
		Reason   string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	actor, ok := s.stepUpActor(w, r, in.Password)
	if !ok {
		return
	}
	var (
		d        iamv2.Deletability
		disabled bool
		err      error
	)
	if pkg {
		d, disabled, err = s.commerce.DeletePackage(r.Context(), s.tenantID, s.siteID, id, actor, in.Reason)
	} else {
		d, disabled, err = s.commerce.DeletePlan(r.Context(), s.tenantID, s.siteID, id, actor, in.Reason)
	}
	switch {
	case disabled:
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
	case errors.Is(err, iamv2.ErrCatalogueInUse):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "in_use", "message": "this " + noun + " is in use and cannot be deleted; disable it instead",
			"deletability": d,
		})
	case errors.Is(err, iamv2.ErrCommerceNotFound):
		jsonErr(w, http.StatusNotFound, "not_found", "no such "+noun+" at this property")
	case errors.Is(err, iamv2.ErrCatalogueDeleteNeedsReason):
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
	case errors.Is(err, iamv2.ErrCatalogueDeleteUnavailable):
		jsonErr(w, http.StatusConflict, "delete_unavailable", "this appliance's database does not support deletion yet")
	case err != nil:
		jsonErr(w, http.StatusInternalServerError, "internal", "the "+noun+" could not be deleted")
	default:
		s.audit(r, action, target, id, map[string]any{"reason": in.Reason})
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
	}
}
