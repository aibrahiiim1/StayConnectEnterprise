package main

// Usage Explorer: measured usage per access source (client account, voucher or Hotel room/stay) and per device.
// Every number is measured, never estimated or interpolated; a stay with no sessions reports no usage.
// Sources: iam_v2.sessions (authoritative per-session byte totals), accounting_records (the samples behind them),
// entitlements (quota, end reason, stay link), stays, devices (MAC).
// A room number is not an identity: every room lookup is scoped by pms_interface_id and resolves to a stay, or it would leak across properties.
// A MAC is not a person: the device view names no guest, only the stays the device was associated with.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func (s *server) usageRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/stays", s.listUsageStays)
	r.Get("/stays/{stay_id}", s.getStayUsage)
	r.Get("/devices/{mac}", s.getDeviceUsage)
	r.Get("/sessions/{session_id}/samples", s.listSessionSamples)
	// By access source: the generic form of /stays, which stays as is for deep links and older screens.
	r.Get("/sources", s.listUsageSources)
	r.Get("/sources/{type}/{id}", s.getSourceUsage)
	return r
}

type usageTotals struct {
	BytesDown int64 `json:"bytes_down"`
	BytesUp   int64 `json:"bytes_up"`
	BytesAll  int64 `json:"bytes_total"`
	Sessions  int   `json:"sessions"`
	Devices   int   `json:"devices"`
}

type usageStayRow struct {
	StayID string `json:"stay_id"`
	// Room and interface always travel together.
	Room          string      `json:"room"`
	PMSInterface  string      `json:"pms_interface"`
	Reservation   string      `json:"reservation,omitempty"`
	Status        string      `json:"stay_status"`
	Arrival       *time.Time  `json:"arrival,omitempty"`
	Departure     *time.Time  `json:"departure,omitempty"`
	EffectiveOut  *time.Time  `json:"effective_checkout_at,omitempty"`
	Totals        usageTotals `json:"totals"`
	QuotaBytes    *int64      `json:"quota_bytes,omitempty"`
	ConsumedBytes *int64      `json:"consumed_bytes,omitempty"`
	EndReason     string      `json:"end_reason,omitempty"`
}

const usageSearchHeader = "X-Usage-Search"

// parseWindow reads an explicit from/to, defaulting to the last 30 days (a dispute has a period; all-time is slowest and least useful).
// parseWindow reads ?from and ?to (RFC 3339; default the last 30 days). A value that does not parse, or a window
// that ends before it starts, is an error: it used to be ignored silently, so the operator saw a different period
// from the one asked for (audit P9 L12).
func parseWindow(r *http.Request) (from, to time.Time, err error) {
	to = time.Now().UTC()
	from = to.AddDate(0, 0, -30)
	if v := r.URL.Query().Get("from"); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return from, to, errors.New("from must be an RFC 3339 time")
		}
		from = t.UTC()
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return from, to, errors.New("to must be an RFC 3339 time")
		}
		to = t.UTC()
	}
	if to.Before(from) {
		return from, to, errors.New("to must not be before from")
	}
	return from, to, nil
}

// readWindow is parseWindow for a handler: ok=false means a 400 was written.
func readWindow(w http.ResponseWriter, r *http.Request) (from, to time.Time, ok bool) {
	from, to, err := parseWindow(r)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return from, to, false
	}
	return from, to, true
}

func queryLimit(r *http.Request, def, max int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		return min(v, max)
	}
	return def
}

// listUsageStays orders by use, descending: an operator without a room number starts there, and one with a room filters.
func (s *server) listUsageStays(w http.ResponseWriter, r *http.Request) {
	from, to, ok := readWindow(w, r)
	if !ok {
		return
	}
	limit := queryLimit(r, 50, 200)

	// The room/reservation filter is one UI field. The search travels only in X-Usage-Search: a ?q= would put
	// guest search text in URLs and proxy logs (audit P9 L11).
	q, ok := readSearch(w, r, usageSearchHeader)
	if !ok {
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		WITH stay_usage AS (
		  SELECT e.stay_id,
		         COALESCE(SUM(se.bytes_down), 0) AS down,
		         COALESCE(SUM(se.bytes_up), 0)   AS up,
		         COUNT(se.id)                    AS sessions,
		         COUNT(DISTINCT se.device_id)    AS devices,
		         MAX(COALESCE(e.data_quota_bytes, spr.data_quota_bytes)) AS quota,
		         MAX(e.consumed_data_bytes)      AS consumed,
		         MAX(e.terminal_reason)          AS end_reason
		    FROM iam_v2.entitlements e
		    -- THE ENFORCED ALLOWANCE (internal/enforce): the entitlement's own frozen quota, else its plan's.
		    LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		    LEFT JOIN iam_v2.sessions se
		           ON se.entitlement_id = e.id
		          AND se.started < $4 AND (se.ended IS NULL OR se.ended > $3)
		   WHERE e.tenant_id = $1 AND e.site_id = $2 AND e.stay_id IS NOT NULL
		   GROUP BY e.stay_id
		)
		SELECT st.id::text,
		       COALESCE(st.normalized_room_number, ''),
		       COALESCE(pi.display_label, pi.id::text),
		       COALESCE(st.external_reservation_id, ''),
		       st.status,
		       st.arrival, st.departure, st.effective_checkout_at,
		       u.down, u.up, u.sessions, u.devices, u.quota, u.consumed, COALESCE(u.end_reason, '')
		  FROM stay_usage u
		  JOIN iam_v2.stays st ON st.id = u.stay_id
		  LEFT JOIN iam_v2.pms_interfaces pi ON pi.id = st.pms_interface_id
		 WHERE ($5 = '' OR st.normalized_room_number ILIKE '%' || $5 || '%'
		                OR st.external_reservation_id ILIKE '%' || $5 || '%')
		 ORDER BY (u.down + u.up) DESC, st.arrival DESC NULLS LAST
		 LIMIT `+strconv.Itoa(limit),
		s.tenantID, s.siteID, from, to, q)
	if err != nil {
		slog.Error("usage stay list failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	defer rows.Close()

	out := []usageStayRow{}
	for rows.Next() {
		var x usageStayRow
		var quota, consumed *int64
		if err := rows.Scan(&x.StayID, &x.Room, &x.PMSInterface, &x.Reservation, &x.Status,
			&x.Arrival, &x.Departure, &x.EffectiveOut,
			&x.Totals.BytesDown, &x.Totals.BytesUp, &x.Totals.Sessions, &x.Totals.Devices,
			&quota, &consumed, &x.EndReason); err != nil {
			slog.Error("usage stay scan failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
			return
		}
		x.Totals.BytesAll = x.Totals.BytesDown + x.Totals.BytesUp
		x.QuotaBytes, x.ConsumedBytes = quota, consumed
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		slog.Error("usage stay read failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	writeList(w, out)
}

type stayDeviceRow struct {
	MAC       string     `json:"mac"`
	BytesDown int64      `json:"bytes_down"`
	BytesUp   int64      `json:"bytes_up"`
	BytesAll  int64      `json:"bytes_total"`
	Sessions  int        `json:"sessions"`
	FirstSeen *time.Time `json:"first_seen,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
}

type staySessionRow struct {
	SessionID string     `json:"session_id"`
	MAC       string     `json:"mac,omitempty"`
	IP        string     `json:"ip,omitempty"`
	Method    string     `json:"credential_method,omitempty"`
	State     string     `json:"state"`
	Started   *time.Time `json:"started,omitempty"`
	Ended     *time.Time `json:"ended,omitempty"`
	EndReason string     `json:"end_reason,omitempty"`
	BytesDown int64      `json:"bytes_down"`
	BytesUp   int64      `json:"bytes_up"`
	BytesAll  int64      `json:"bytes_total"`
}

// stayHeaderSQL reads one stay, the room always carrying its interface, with the allowance of its most recent entitlement.
const stayHeaderSQL = `
		SELECT st.id::text,
		       COALESCE(st.normalized_room_number, ''),
		       COALESCE(pi.display_label, pi.id::text),
		       COALESCE(st.external_reservation_id, ''),
		       st.status, st.arrival, st.departure, st.effective_checkout_at,
		       COALESCE(e.data_quota_bytes, spr.data_quota_bytes), e.consumed_data_bytes,
		       COALESCE(e.terminal_reason, ''), COALESCE(spr.name, '')
		  FROM iam_v2.stays st
		  LEFT JOIN iam_v2.pms_interfaces pi ON pi.id = st.pms_interface_id
		  LEFT JOIN LATERAL (
		      SELECT * FROM iam_v2.entitlements en
		       WHERE en.stay_id = st.id AND en.tenant_id = st.tenant_id
		       ORDER BY en.activated_at DESC NULLS LAST LIMIT 1) e ON true
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		 WHERE st.id = $1 AND st.tenant_id = $2 AND st.site_id = $3`

type stayUsageDetail struct {
	Stay     usageStayRow     `json:"stay"`
	Plan     string           `json:"service_plan,omitempty"`
	Devices  []stayDeviceRow  `json:"devices"`
	Sessions []staySessionRow `json:"sessions"`
	From     time.Time        `json:"from"`
	To       time.Time        `json:"to"`
}

// errNoSuchSource is "this site has no record of it" -- a 404, never a 500.
var errNoSuchSource = errors.New("no such access source at this site")

// readStayUsage is the stay case of both /usage/stays/{id} and /usage/sources/stay/{id}, so they cannot disagree.
func (s *server) readStayUsage(ctx context.Context, stayID string, from, to time.Time) (stayUsageDetail, error) {
	out := stayUsageDetail{Devices: []stayDeviceRow{}, Sessions: []staySessionRow{}, From: from, To: to}
	if err := s.db.QueryRow(ctx, stayHeaderSQL, stayID, s.tenantID, s.siteID).Scan(
		&out.Stay.StayID, &out.Stay.Room, &out.Stay.PMSInterface, &out.Stay.Reservation,
		&out.Stay.Status, &out.Stay.Arrival, &out.Stay.Departure, &out.Stay.EffectiveOut,
		&out.Stay.QuotaBytes, &out.Stay.ConsumedBytes, &out.Stay.EndReason, &out.Plan); err != nil {
		return out, errNoSuchSource
	}
	devices, sessions, totals, err := s.subjectDevicesAndSessions(ctx, "stay", stayID, from, to)
	if err != nil {
		return out, err
	}
	out.Devices, out.Sessions, out.Stay.Totals = devices, sessions, totals
	return out, nil
}

func (s *server) getStayUsage(w http.ResponseWriter, r *http.Request) {
	from, to, ok := readWindow(w, r)
	if !ok {
		return
	}
	out, err := s.readStayUsage(r.Context(), chi.URLParam(r, "stay_id"), from, to)
	switch {
	case errors.Is(err, errNoSuchSource):
		jsonErr(w, http.StatusNotFound, "not_found", "no such stay at this site")
		return
	case err != nil:
		slog.Error("stay usage failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// subjectDevicesSQL is the per-device breakdown of one access source. The subject column comes from usageSubjectColumn, never the request.
func subjectDevicesSQL(col string) string {
	return `
		SELECT COALESCE(d.mac::text, ''),
		       COALESCE(SUM(se.bytes_down),0), COALESCE(SUM(se.bytes_up),0),
		       COUNT(se.id), MIN(se.started), MAX(COALESCE(se.ended, se.started))
		  FROM iam_v2.sessions se
		  JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
		  LEFT JOIN iam_v2.devices d ON d.id = se.device_id
		 WHERE e.` + col + ` = $1 AND se.tenant_id = $2 AND se.site_id = $3
		   AND se.started < $5 AND (se.ended IS NULL OR se.ended > $4)
		 GROUP BY d.mac
		 ORDER BY 2 DESC`
}

// subjectSessionsSQL is the sessions of one access source, newest first.
func subjectSessionsSQL(col string) string {
	return `
		SELECT se.id::text, COALESCE(d.mac::text,''), COALESCE(se.ip::text,''),
		       COALESCE(se.credential_method,''), se.state, se.started, se.ended,
		       COALESCE(se.end_reason,''), se.bytes_down, se.bytes_up
		  FROM iam_v2.sessions se
		  JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
		  LEFT JOIN iam_v2.devices d ON d.id = se.device_id
		 WHERE e.` + col + ` = $1 AND se.tenant_id = $2 AND se.site_id = $3
		   AND se.started < $5 AND (se.ended IS NULL OR se.ended > $4)
		 ORDER BY se.started DESC NULLS LAST LIMIT 200`
}

// subjectDevicesAndSessions reads the device breakdown and sessions of one source over the list's [from, to) period, so detail and list totals agree.
// Totals are the sum of the device rows, which are the sum of the sessions.
func (s *server) subjectDevicesAndSessions(ctx context.Context, kind, id string, from, to time.Time) ([]stayDeviceRow, []staySessionRow, usageTotals, error) {
	devices, sessions := []stayDeviceRow{}, []staySessionRow{}
	var t usageTotals
	col, ok := usageSubjectColumn[kind]
	if !ok {
		return devices, sessions, t, errors.New("unknown access source type")
	}
	drows, err := s.db.Query(ctx, subjectDevicesSQL(col), id, s.tenantID, s.siteID, from, to)
	if err != nil {
		return devices, sessions, t, err
	}
	for drows.Next() {
		var d stayDeviceRow
		if err := drows.Scan(&d.MAC, &d.BytesDown, &d.BytesUp, &d.Sessions, &d.FirstSeen, &d.LastSeen); err != nil {
			drows.Close()
			return devices, sessions, t, err
		}
		d.BytesAll = d.BytesDown + d.BytesUp
		devices = append(devices, d)
		t.BytesDown += d.BytesDown
		t.BytesUp += d.BytesUp
		t.Sessions += d.Sessions
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return devices, sessions, t, err
	}
	t.Devices = len(devices)
	t.BytesAll = t.BytesDown + t.BytesUp

	srows, err := s.db.Query(ctx, subjectSessionsSQL(col), id, s.tenantID, s.siteID, from, to)
	if err != nil {
		return devices, sessions, t, err
	}
	defer srows.Close()
	for srows.Next() {
		var x staySessionRow
		if err := srows.Scan(&x.SessionID, &x.MAC, &x.IP, &x.Method, &x.State,
			&x.Started, &x.Ended, &x.EndReason, &x.BytesDown, &x.BytesUp); err != nil {
			return devices, sessions, t, err
		}
		x.BytesAll = x.BytesDown + x.BytesUp
		sessions = append(sessions, x)
	}
	return devices, sessions, t, srows.Err()
}

// macFromPath reads the {mac} path parameter. chi returns the raw segment, still escaped (7e%3A58%3A...), so unescape first, then parse it
// as a hardware address (accepting dash and dot notation, normalised to Postgres' form). The second return value separates "not a MAC" from "no record of it".
func macFromPath(r *http.Request) (string, bool) {
	raw := chi.URLParam(r, "mac")
	if unescaped, err := url.PathUnescape(raw); err == nil {
		raw = unescaped
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	hw, err := net.ParseMAC(raw)
	if err != nil || len(hw) != 6 {
		// PostgreSQL macaddr is six bytes; EUI-64 and Infiniband addresses parse but cannot be stored, so refuse them now.
		return "", false
	}
	return hw.String(), true
}

// getDeviceUsage reports a DEVICE over a period, listing the stays it was associated with; it does not claim the MAC identifies a guest.
func (s *server) getDeviceUsage(w http.ResponseWriter, r *http.Request) {
	mac, ok := macFromPath(r)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "bad_mac",
			"that is not a device address. A device address looks like 7a:1b:2c:3d:4e:5f.")
		return
	}
	from, to, ok := readWindow(w, r)
	if !ok {
		return
	}

	var out struct {
		MAC       string           `json:"mac"`
		From      time.Time        `json:"from"`
		To        time.Time        `json:"to"`
		FirstSeen *time.Time       `json:"first_seen,omitempty"`
		LastSeen  *time.Time       `json:"last_seen,omitempty"`
		Totals    usageTotals      `json:"totals"`
		Sessions  []staySessionRow `json:"sessions"`
		Stays     []usageStayRow   `json:"stays"`
	}
	out.MAC, out.From, out.To = mac, from, to
	out.Sessions, out.Stays = []staySessionRow{}, []usageStayRow{}

	// Three outcomes, three answers: a query error must not be reported as an unknown device.
	switch err := s.db.QueryRow(r.Context(),
		`SELECT first_seen, last_seen FROM iam_v2.devices
		  WHERE mac = $1::macaddr AND tenant_id = $2 AND site_id = $3`,
		mac, s.tenantID, s.siteID).Scan(&out.FirstSeen, &out.LastSeen); {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows):
		jsonErr(w, http.StatusNotFound, "not_found", "this appliance has no record of that device")
		return
	default:
		slog.Error("device lookup failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the device record could not be read")
		return
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT se.id::text, COALESCE(se.ip::text,''), COALESCE(se.credential_method,''),
		       se.state, se.started, se.ended, COALESCE(se.end_reason,''),
		       se.bytes_down, se.bytes_up,
		       COALESCE(st.id::text,''), COALESCE(st.normalized_room_number,''),
		       COALESCE(pi.display_label, COALESCE(pi.id::text,''))
		  FROM iam_v2.sessions se
		  JOIN iam_v2.devices d ON d.id = se.device_id
		  LEFT JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
		  LEFT JOIN iam_v2.stays st ON st.id = e.stay_id
		  LEFT JOIN iam_v2.pms_interfaces pi ON pi.id = st.pms_interface_id
		 WHERE d.mac = $1::macaddr AND se.tenant_id = $2 AND se.site_id = $3
		   AND se.started < $5 AND (se.ended IS NULL OR se.ended > $4)
		 ORDER BY se.started DESC NULLS LAST LIMIT 500`,
		mac, s.tenantID, s.siteID, from, to)
	if err != nil {
		slog.Error("device usage failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var x staySessionRow
		var stayID, room, iface string
		if err := rows.Scan(&x.SessionID, &x.IP, &x.Method, &x.State, &x.Started, &x.Ended,
			&x.EndReason, &x.BytesDown, &x.BytesUp, &stayID, &room, &iface); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
			return
		}
		x.MAC, x.BytesAll = mac, x.BytesDown+x.BytesUp
		out.Sessions = append(out.Sessions, x)
		out.Totals.BytesDown += x.BytesDown
		out.Totals.BytesUp += x.BytesUp
		out.Totals.Sessions++
		if stayID != "" && !seen[stayID] {
			seen[stayID] = true
			out.Stays = append(out.Stays, usageStayRow{StayID: stayID, Room: room, PMSInterface: iface})
		}
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	out.Totals.BytesAll = out.Totals.BytesDown + out.Totals.BytesUp
	out.Totals.Devices = 1
	writeJSON(w, http.StatusOK, out)
}

type usageSample struct {
	At        time.Time `json:"sampled_at"`
	BytesDown int64     `json:"bytes_down"`
	BytesUp   int64     `json:"bytes_up"`
}

const sessionSamplesMax = 2000

// listSessionSamples returns the durable samples a session total was summed from.
func (s *server) listSessionSamples(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "session_id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT sampled_at, bytes_down, bytes_up
		  FROM iam_v2.accounting_records
		 WHERE session_id = $1 AND tenant_id = $2 AND site_id = $3
		 ORDER BY sampled_at ASC
		 LIMIT `+strconv.Itoa(sessionSamplesMax+1), id, s.tenantID, s.siteID)
	if err != nil {
		slog.Error("session samples failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the accounting samples could not be read")
		return
	}
	defer rows.Close()
	out := []usageSample{}
	var down, up int64
	for rows.Next() {
		var x usageSample
		if err := rows.Scan(&x.At, &x.BytesDown, &x.BytesUp); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the accounting samples could not be read")
			return
		}
		down += x.BytesDown
		up += x.BytesUp
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the accounting samples could not be read")
		return
	}
	// Raw samples are kept only a couple of days once rolled up (migration 0240, AD-48); an older session shows its
	// per-minute totals instead, labelled as such, until those too pass their retention.
	grain := "sample"
	if len(out) == 0 {
		mrows, merr := s.db.Query(ctx, `
			SELECT bucket, bytes_down, bytes_up
			  FROM iam_v2.accounting_usage_minute
			 WHERE session_id = $1 AND tenant_id = $2 AND site_id = $3
			 ORDER BY bucket ASC
			 LIMIT `+strconv.Itoa(sessionSamplesMax+1), id, s.tenantID, s.siteID)
		if merr == nil {
			for mrows.Next() {
				var x usageSample
				if mrows.Scan(&x.At, &x.BytesDown, &x.BytesUp) != nil {
					break
				}
				down += x.BytesDown
				up += x.BytesUp
				out = append(out, x)
				grain = "minute"
			}
			mrows.Close()
		}
	}
	// Past sessionSamplesMax the samples no longer add up to the total, so the response must say it is a cut list.
	truncated := len(out) > sessionSamplesMax
	if truncated {
		x := out[sessionSamplesMax]
		down -= x.BytesDown
		up -= x.BytesUp
		out = out[:sessionSamplesMax]
	}
	// The sum is returned with the samples so the screen can show they add up to the session total.
	writeJSON(w, http.StatusOK, map[string]any{
		"samples": out, "sample_count": len(out), "truncated": truncated, "grain": grain,
		"bytes_down": down, "bytes_up": up, "bytes_total": down + up,
	})
}

// By access source: the subject of the entitlement a session ran under (stay, client account, voucher or guest principal; ent_one_subject).
// Totals are the same iam_v2.sessions sums as elsewhere in this file.
// stay: room + PMS interface (always together), reservation, status, arrival, departure.
// account: the USERNAME only, never display name, notes or password hash (a display name is a guest name).
// voucher: the card reference (the voucher id); never the code, and nothing from iam_v2.vouchers (svc_edged has no SELECT there).
// The guest-principal source (email/phone/social) is not offered: its identity tables are unreadable here. Its sessions still appear under "By device".

// usageSubjectColumn maps a source type to its entitlement column; it is the only way a source type reaches SQL text.
var usageSubjectColumn = map[string]string{
	"stay":    "stay_id",
	"account": "guest_account_id",
	"voucher": "voucher_id",
	// A package chosen without signing in: the anonymous access subject, identified only by its opaque reference.
	"open": "anonymous_subject_id",
}

type usageSourceRow struct {
	Type     string `json:"source_type"`
	ID       string `json:"source_id"`
	Username string `json:"account_username,omitempty"`
	// stay only. Room and interface travel TOGETHER, always.
	Room          string      `json:"room,omitempty"`
	PMSInterface  string      `json:"pms_interface,omitempty"`
	Reservation   string      `json:"reservation,omitempty"`
	StayStatus    string      `json:"stay_status,omitempty"`
	Arrival       *time.Time  `json:"arrival,omitempty"`
	Departure     *time.Time  `json:"departure,omitempty"`
	EffectiveOut  *time.Time  `json:"effective_checkout_at,omitempty"`
	Totals        usageTotals `json:"totals"`
	QuotaBytes    *int64      `json:"quota_bytes,omitempty"`
	ConsumedBytes *int64      `json:"consumed_bytes,omitempty"`
	EndReason     string      `json:"end_reason,omitempty"`
	LastActivity  *time.Time  `json:"last_activity,omitempty"`
}

// usageSourceListSQL lists access sources with measured totals over [from, to).
// $1 tenant, $2 site, $3 from, $4 to, $5 source type (empty for every type), $6 LIKE-escaped search text.
// The search matches what each source can honestly be searched by: a stay by room or reservation, an account by username, a voucher by card-reference prefix.
func usageSourceListSQL(limit, offset int) string {
	return `
		WITH src AS (
		  SELECT CASE WHEN e.stay_id IS NOT NULL THEN 'stay'
		              WHEN e.guest_account_id IS NOT NULL THEN 'account'
		              WHEN e.voucher_id IS NOT NULL THEN 'voucher'
		              ELSE 'open' END                                   AS kind,
		         COALESCE(e.stay_id, e.guest_account_id, e.voucher_id, e.anonymous_subject_id) AS subject_id,
		         COALESCE(SUM(se.bytes_down), 0) AS down,
		         COALESCE(SUM(se.bytes_up), 0)   AS up,
		         COUNT(se.id)                    AS sessions,
		         COUNT(DISTINCT se.device_id)    AS devices,
		         MAX(COALESCE(e.data_quota_bytes, spr.data_quota_bytes)) AS quota,
		         MAX(e.consumed_data_bytes)      AS consumed,
		         MAX(e.terminal_reason)          AS end_reason,
		         MAX(se.started)                 AS last_started
		    FROM iam_v2.entitlements e
		    -- THE ENFORCED ALLOWANCE (internal/enforce): the entitlement's own frozen quota, else its plan's.
		    LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		    LEFT JOIN iam_v2.sessions se
		           ON se.entitlement_id = e.id
		          AND se.started < $4 AND (se.ended IS NULL OR se.ended > $3)
		   WHERE e.tenant_id = $1 AND e.site_id = $2
		     AND e.guest_principal_id IS NULL
		     AND ($5 = ''
		          OR ($5 = 'stay'    AND e.stay_id IS NOT NULL)
		          OR ($5 = 'account' AND e.guest_account_id IS NOT NULL)
		          OR ($5 = 'voucher' AND e.voucher_id IS NOT NULL)
		          OR ($5 = 'open'    AND e.anonymous_subject_id IS NOT NULL))
		   GROUP BY 1, 2
		)
		SELECT u.kind, u.subject_id::text,
		       COALESCE(ga.username, ''),
		       COALESCE(st.normalized_room_number, ''),
		       COALESCE(pi.display_label, pi.id::text, ''),
		       COALESCE(st.external_reservation_id, ''),
		       COALESCE(st.status, ''),
		       st.arrival, st.departure, st.effective_checkout_at,
		       u.down, u.up, u.sessions, u.devices, u.quota, u.consumed, COALESCE(u.end_reason, ''),
		       u.last_started
		  FROM src u
		  LEFT JOIN iam_v2.stays st
		         ON u.kind = 'stay' AND st.id = u.subject_id AND st.tenant_id = $1
		  LEFT JOIN iam_v2.pms_interfaces pi ON pi.id = st.pms_interface_id
		  LEFT JOIN iam_v2.guest_access_accounts ga
		         ON u.kind = 'account' AND ga.id = u.subject_id AND ga.tenant_id = $1 AND ga.site_id = $2
		 WHERE ($6 = ''
		        OR (u.kind = 'stay' AND (st.normalized_room_number ILIKE '%' || $6 || '%'
		                             OR st.external_reservation_id ILIKE '%' || $6 || '%'))
		        OR (u.kind = 'account' AND ga.username ILIKE '%' || $6 || '%')
		        OR (u.kind = 'voucher' AND u.subject_id::text ILIKE $6 || '%')
		        OR (u.kind = 'open'    AND u.subject_id::text ILIKE $6 || '%'))
		 ORDER BY (u.down + u.up) DESC, u.last_started DESC NULLS LAST, u.kind, u.subject_id
		 LIMIT ` + strconv.Itoa(limit) + ` OFFSET ` + strconv.Itoa(offset)
}

// parseSourceType: an empty value or "all" means every type; an unknown one is refused, not widened.
func parseSourceType(v string) (string, bool) {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" || v == "all" {
		return "", true
	}
	_, ok := usageSubjectColumn[v]
	return v, ok
}

// normaliseSourceQuery trims the search and drops a trailing ellipsis (voucher screens print "3fa85f64…") so a pasted reference still matches.
func normaliseSourceQuery(q string) string {
	q = strings.TrimSpace(q)
	q = strings.TrimSuffix(q, "…")
	q = strings.TrimSuffix(q, "...")
	return strings.TrimSpace(q)
}

func (s *server) listUsageSources(w http.ResponseWriter, r *http.Request) {
	kind, ok := parseSourceType(r.URL.Query().Get("type"))
	if !ok {
		jsonErr(w, http.StatusBadRequest, "bad_type",
			"the access source type must be one of: stay, account, voucher, open")
		return
	}
	from, to, ok := readWindow(w, r)
	if !ok {
		return
	}
	// Paged by ?page and ?page_size (?limit= still sizes the page for older clients, up to 200); ordered by use then identity so page boundaries are stable.
	pg, ok := readPage(w, r, 200)
	if !ok {
		return
	}
	// The search text travels only in X-Usage-Search (a username or room number; edged logs request lines), and
	// readSearch bounds its length (audit P9 L11).
	raw, ok := readSearch(w, r, usageSearchHeader)
	if !ok {
		return
	}
	q := likeEscape(normaliseSourceQuery(raw))

	rows, err := s.db.Query(r.Context(), usageSourceListSQL(pg.Fetch(), pg.Offset()),
		s.tenantID, s.siteID, from, to, kind, q)
	if err != nil {
		slog.Error("usage source list failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	defer rows.Close()

	out := []usageSourceRow{}
	for rows.Next() {
		var x usageSourceRow
		if err := rows.Scan(&x.Type, &x.ID, &x.Username,
			&x.Room, &x.PMSInterface, &x.Reservation, &x.StayStatus,
			&x.Arrival, &x.Departure, &x.EffectiveOut,
			&x.Totals.BytesDown, &x.Totals.BytesUp, &x.Totals.Sessions, &x.Totals.Devices,
			&x.QuotaBytes, &x.ConsumedBytes, &x.EndReason, &x.LastActivity); err != nil {
			slog.Error("usage source scan failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
			return
		}
		x.Totals.BytesAll = x.Totals.BytesDown + x.Totals.BytesUp
		out = append(out, finishSourceRow(x))
	}
	if err := rows.Err(); err != nil {
		slog.Error("usage source read failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	out, more := trimPage(out, pg)
	writeJSON(w, http.StatusOK, newPagedList(out, more, pg, nil))
}

// finishSourceRow clears every field that does not belong to the row's type, so a stay field can never
// appear on an account or a voucher even if a join were ever widened.
func finishSourceRow(x usageSourceRow) usageSourceRow {
	if x.Type != "stay" {
		x.Room, x.PMSInterface, x.Reservation, x.StayStatus = "", "", "", ""
		x.Arrival, x.Departure, x.EffectiveOut = nil, nil, nil
	}
	if x.Type != "account" {
		x.Username = ""
	}
	return x
}

// accountHeaderSQL reads one client account's username and the allowance of its most recent entitlement.
// Only the username: never display_name, notes or password_hash.
const accountHeaderSQL = `
		SELECT ga.id::text, ga.username,
		       COALESCE(en.data_quota_bytes, spr.data_quota_bytes), en.consumed_data_bytes,
		       COALESCE(en.terminal_reason, ''),
		       COALESCE(en.status, ''), COALESCE(spr.name, '')
		  FROM iam_v2.guest_access_accounts ga
		  LEFT JOIN LATERAL (
		      SELECT x.data_quota_bytes, x.consumed_data_bytes, x.terminal_reason, x.status,
		             x.service_plan_revision_id
		        FROM iam_v2.entitlements x
		       WHERE x.guest_account_id = ga.id AND x.tenant_id = ga.tenant_id AND x.site_id = ga.site_id
		       ORDER BY x.activated_at DESC NULLS LAST LIMIT 1) en ON true
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = en.service_plan_revision_id
		 WHERE ga.id = $1 AND ga.tenant_id = $2 AND ga.site_id = $3`

// voucherHeaderSQL reads the most recent entitlement granted by one voucher. The voucher row itself is not
// read (svc_edged may not); a voucher that granted nothing at this site has no usage and reads as not found.
const voucherHeaderSQL = `
		SELECT x.voucher_id::text,
		       COALESCE(x.data_quota_bytes, spr.data_quota_bytes), x.consumed_data_bytes,
		       COALESCE(x.terminal_reason, ''), x.status, COALESCE(spr.name, '')
		  FROM iam_v2.entitlements x
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = x.service_plan_revision_id
		 WHERE x.voucher_id = $1 AND x.tenant_id = $2 AND x.site_id = $3
		 ORDER BY x.activated_at DESC NULLS LAST LIMIT 1`

// openHeaderSQL is voucherHeaderSQL for the anonymous access subject: its most recent entitlement here.
const openHeaderSQL = `
		SELECT x.anonymous_subject_id::text,
		       COALESCE(x.data_quota_bytes, spr.data_quota_bytes), x.consumed_data_bytes,
		       COALESCE(x.terminal_reason, ''), x.status, COALESCE(spr.name, '')
		  FROM iam_v2.entitlements x
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = x.service_plan_revision_id
		 WHERE x.anonymous_subject_id = $1 AND x.tenant_id = $2 AND x.site_id = $3
		 ORDER BY x.activated_at DESC NULLS LAST LIMIT 1`

type sourceUsageDetail struct {
	Source       usageSourceRow   `json:"source"`
	Plan         string           `json:"service_plan,omitempty"`
	AccessStatus string           `json:"access_status,omitempty"`
	Devices      []stayDeviceRow  `json:"devices"`
	Sessions     []staySessionRow `json:"sessions"`
	From         time.Time        `json:"from"`
	To           time.Time        `json:"to"`
}

// getSourceUsage is one access source, in full: totals, allowance, why access ended, devices, sessions.
// The stay case IS the stay endpoint's reader, so a stay reads the same whichever way it is opened.
func (s *server) getSourceUsage(w http.ResponseWriter, r *http.Request) {
	kind, ok := parseSourceType(chi.URLParam(r, "type"))
	if !ok || kind == "" {
		jsonErr(w, http.StatusBadRequest, "bad_type",
			"the access source type must be one of: stay, account, voucher, open")
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such access source at this site")
		return
	}
	from, to, ok := readWindow(w, r)
	if !ok {
		return
	}
	out, err := s.readSourceUsage(r.Context(), kind, id, from, to)
	switch {
	case errors.Is(err, errNoSuchSource):
		jsonErr(w, http.StatusNotFound, "not_found", "no such access source at this site")
		return
	case err != nil:
		slog.Error("source usage failed", "type", kind, "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) readSourceUsage(ctx context.Context, kind, id string, from, to time.Time) (sourceUsageDetail, error) {
	out := sourceUsageDetail{Devices: []stayDeviceRow{}, Sessions: []staySessionRow{}, From: from, To: to}
	out.Source.Type = kind

	var err error
	switch kind {
	case "stay":
		st, e := s.readStayUsage(ctx, id, from, to)
		if e != nil {
			return out, e
		}
		out.Source = usageSourceRow{
			Type: "stay", ID: st.Stay.StayID,
			Room: st.Stay.Room, PMSInterface: st.Stay.PMSInterface, Reservation: st.Stay.Reservation,
			StayStatus: st.Stay.Status, Arrival: st.Stay.Arrival, Departure: st.Stay.Departure,
			EffectiveOut: st.Stay.EffectiveOut, Totals: st.Stay.Totals,
			QuotaBytes: st.Stay.QuotaBytes, ConsumedBytes: st.Stay.ConsumedBytes, EndReason: st.Stay.EndReason,
		}
		if len(st.Sessions) > 0 {
			out.Source.LastActivity = st.Sessions[0].Started
		}
		out.Plan, out.Devices, out.Sessions = st.Plan, st.Devices, st.Sessions
		return out, nil
	case "account":
		err = s.db.QueryRow(ctx, accountHeaderSQL, id, s.tenantID, s.siteID).Scan(
			&out.Source.ID, &out.Source.Username, &out.Source.QuotaBytes, &out.Source.ConsumedBytes,
			&out.Source.EndReason, &out.AccessStatus, &out.Plan)
	case "voucher":
		err = s.db.QueryRow(ctx, voucherHeaderSQL, id, s.tenantID, s.siteID).Scan(
			&out.Source.ID, &out.Source.QuotaBytes, &out.Source.ConsumedBytes,
			&out.Source.EndReason, &out.AccessStatus, &out.Plan)
	case "open":
		err = s.db.QueryRow(ctx, openHeaderSQL, id, s.tenantID, s.siteID).Scan(
			&out.Source.ID, &out.Source.QuotaBytes, &out.Source.ConsumedBytes,
			&out.Source.EndReason, &out.AccessStatus, &out.Plan)
	default:
		return out, errNoSuchSource
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return out, errNoSuchSource
	}
	if err != nil {
		return out, err
	}
	devices, sessions, totals, err := s.subjectDevicesAndSessions(ctx, kind, id, from, to)
	if err != nil {
		return out, err
	}
	out.Devices, out.Sessions, out.Source.Totals = devices, sessions, totals
	if len(sessions) > 0 {
		out.Source.LastActivity = sessions[0].Started
	}
	out.Source = finishSourceRow(out.Source)
	return out, nil
}
