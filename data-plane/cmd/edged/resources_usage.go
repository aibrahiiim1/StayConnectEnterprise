package main

// USAGE EXPLORER — the screen a hotel opens during an argument.
//
// The two questions this exists to answer, in the words they arrive in:
//
//     "How much internet did room 4202 use during this stay?"
//     "How much data did this device use between these dates?"
//
// The first is asked of an ACCESS SOURCE -- whatever granted the access: a client account, a voucher or a
// Hotel room/stay (see "by access source" at the end of this file). Room and stay detail belongs only to the
// stay source.
//
// EVERY NUMBER HERE IS MEASURED, NEVER DERIVED. There is no estimation, no interpolation and no filling of
// gaps. A stay with no sessions reports no usage rather than zero-with-an-asterisk, and a session whose
// samples stop reports what was sampled. A figure an operator cannot defend line by line is worse than no
// figure at all, because it will be quoted back at them by a guest who has their own numbers.
//
// WHAT THE NUMBERS COME FROM
// --------------------------
//   iam_v2.sessions             the per-session byte totals, start, end and end reason. Written by acctd from
//                               the accounting pipeline; this is the authoritative session record.
//   iam_v2.accounting_records   the durable samples behind those totals. The timeline view reads these, so a
//                               disputed total can be shown as the samples that make it up.
//   iam_v2.entitlements         the quota that applied and why access ended, plus the link to the stay.
//   iam_v2.stays               the room, the reservation and the PMS interface that scopes them.
//   iam_v2.devices             the MAC an operator can read off a handset.
//
// TWO RULES THIS FILE IS BUILT AROUND, because both are easy to break by accident here:
//
//   A ROOM NUMBER IS NOT AN IDENTITY. Room "4202" means nothing on its own — two properties behind two PMS
//   interfaces can both have one. Every room lookup in this file is scoped by pms_interface_id AND resolves
//   to a STAY, which is the thing that actually has an occupant, a period and usage. A room search that
//   matched across interfaces would be a cross-property data leak wearing a convenience feature's clothes.
//
//   A MAC IS NOT A PERSON. The device view reports what a device did and which stays it was associated with.
//   It does not name a guest, and the wording it feeds says "device" throughout. A hotel investigating a
//   dispute needs to know which handset burned the quota; it does not need, and must not be handed, an
//   identity claim the data cannot support.

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
	// Find the stay to investigate: by room, reservation, or simply what used the most this week.
	r.Get("/stays", s.listUsageStays)
	// One stay, in full: totals, quota, devices, sessions, why access ended.
	r.Get("/stays/{stay_id}", s.getStayUsage)
	// One device, over a period the operator chooses.
	r.Get("/devices/{mac}", s.getDeviceUsage)
	// The samples behind one session's total, for when the total itself is what is disputed.
	r.Get("/sessions/{session_id}/samples", s.listSessionSamples)
	// BY ACCESS SOURCE: whatever granted the access -- a client account, a voucher or a Hotel room/stay.
	// /stays above stays as it was (deep links and older screens use it); these are the generic form.
	r.Get("/sources", s.listUsageSources)
	r.Get("/sources/{type}/{id}", s.getSourceUsage)
	return r
}

// ---------------------------------------------------------------------------------- shared shapes --------

type usageTotals struct {
	BytesDown int64 `json:"bytes_down"`
	BytesUp   int64 `json:"bytes_up"`
	BytesAll  int64 `json:"bytes_total"`
	Sessions  int   `json:"sessions"`
	Devices   int   `json:"devices"`
}

type usageStayRow struct {
	StayID string `json:"stay_id"`
	// Room and interface travel TOGETHER, always. See the rule at the top of this file.
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

// parseWindow reads an explicit from/to, defaulting to the last 30 days.
//
// A DISPUTE HAS A PERIOD, and it is usually not "all time". Defaulting to everything would make the first
// answer the operator sees the least useful one, and on an appliance that has been running for a year it
// would also be the slowest.
func parseWindow(r *http.Request) (from, to time.Time) {
	to = time.Now().UTC()
	from = to.AddDate(0, 0, -30)
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t.UTC()
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t.UTC()
		}
	}
	return from, to
}

func queryLimit(r *http.Request, def, max int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		return min(v, max)
	}
	return def
}

// ---------------------------------------------------------------------------- finding the stay -----------

// listUsageStays finds stays to investigate.
//
// Ordered by what was actually used, descending, because an operator arriving with "someone is hammering the
// wifi" has no room number yet — and one arriving WITH a room number filters to it. Both journeys start here.
func (s *server) listUsageStays(w http.ResponseWriter, r *http.Request) {
	from, to := parseWindow(r)
	limit := queryLimit(r, 50, 200)

	// The room/reservation filter is one field in the UI, because an operator has "4202" or "BK-88213" in
	// front of them and should not have to know which kind of thing it is.
	q := strings.TrimSpace(r.URL.Query().Get("q"))

	rows, err := s.db.Query(r.Context(), `
		WITH stay_usage AS (
		  SELECT e.stay_id,
		         COALESCE(SUM(se.bytes_down), 0) AS down,
		         COALESCE(SUM(se.bytes_up), 0)   AS up,
		         COUNT(se.id)                    AS sessions,
		         COUNT(DISTINCT se.device_id)    AS devices,
		         MAX(e.data_quota_bytes)         AS quota,
		         MAX(e.consumed_data_bytes)      AS consumed,
		         MAX(e.terminal_reason)          AS end_reason
		    FROM iam_v2.entitlements e
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

// ------------------------------------------------------------------------- one stay, in full -------------

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

// stayHeaderSQL reads one stay, with the room ALWAYS carrying its interface, and the allowance of the most
// recent entitlement granted under it.
const stayHeaderSQL = `
		SELECT st.id::text,
		       COALESCE(st.normalized_room_number, ''),
		       COALESCE(pi.display_label, pi.id::text),
		       COALESCE(st.external_reservation_id, ''),
		       st.status, st.arrival, st.departure, st.effective_checkout_at,
		       e.data_quota_bytes, e.consumed_data_bytes, COALESCE(e.terminal_reason, ''),
		       COALESCE(spr.name, '')
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
}

// errNoSuchSource is "this site has no record of it" -- a 404, never a 500.
var errNoSuchSource = errors.New("no such access source at this site")

// readStayUsage reads one stay in full. It is the stay case of both /usage/stays/{id} and
// /usage/sources/stay/{id}, so the two can never disagree.
func (s *server) readStayUsage(ctx context.Context, stayID string) (stayUsageDetail, error) {
	out := stayUsageDetail{Devices: []stayDeviceRow{}, Sessions: []staySessionRow{}}
	if err := s.db.QueryRow(ctx, stayHeaderSQL, stayID, s.tenantID, s.siteID).Scan(
		&out.Stay.StayID, &out.Stay.Room, &out.Stay.PMSInterface, &out.Stay.Reservation,
		&out.Stay.Status, &out.Stay.Arrival, &out.Stay.Departure, &out.Stay.EffectiveOut,
		&out.Stay.QuotaBytes, &out.Stay.ConsumedBytes, &out.Stay.EndReason, &out.Plan); err != nil {
		// As before: an unknown or malformed stay id reads as "no such stay", not as a failure.
		return out, errNoSuchSource
	}
	devices, sessions, totals, err := s.subjectDevicesAndSessions(ctx, "stay", stayID)
	if err != nil {
		return out, err
	}
	out.Devices, out.Sessions, out.Stay.Totals = devices, sessions, totals
	return out, nil
}

// getStayUsage answers "how much internet did this room use during this stay".
func (s *server) getStayUsage(w http.ResponseWriter, r *http.Request) {
	out, err := s.readStayUsage(r.Context(), chi.URLParam(r, "stay_id"))
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

// subjectDevicesSQL is the per-device breakdown of one access source. A source usually has two or three
// devices and the dispute is normally about one of them, so this is the breakdown an operator reads first.
// The subject column comes from usageSubjectColumn, never from the request.
func subjectDevicesSQL(col string) string {
	return `
		SELECT COALESCE(d.mac::text, ''),
		       COALESCE(SUM(se.bytes_down),0), COALESCE(SUM(se.bytes_up),0),
		       COUNT(se.id), MIN(se.started), MAX(COALESCE(se.ended, se.started))
		  FROM iam_v2.sessions se
		  JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
		  LEFT JOIN iam_v2.devices d ON d.id = se.device_id
		 WHERE e.` + col + ` = $1 AND se.tenant_id = $2 AND se.site_id = $3
		 GROUP BY d.mac
		 ORDER BY 2 DESC`
}

// subjectSessionsSQL is the sessions of one access source, newest first: when access started, when it ended
// and why.
func subjectSessionsSQL(col string) string {
	return `
		SELECT se.id::text, COALESCE(d.mac::text,''), COALESCE(se.ip::text,''),
		       COALESCE(se.credential_method,''), se.state, se.started, se.ended,
		       COALESCE(se.end_reason,''), se.bytes_down, se.bytes_up
		  FROM iam_v2.sessions se
		  JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
		  LEFT JOIN iam_v2.devices d ON d.id = se.device_id
		 WHERE e.` + col + ` = $1 AND se.tenant_id = $2 AND se.site_id = $3
		 ORDER BY se.started DESC NULLS LAST LIMIT 200`
}

// subjectDevicesAndSessions reads the device breakdown and the sessions of one access source, and totals them.
// The totals are the sum of the device rows, which are the sum of the sessions: nothing else is added.
func (s *server) subjectDevicesAndSessions(ctx context.Context, kind, id string) ([]stayDeviceRow, []staySessionRow, usageTotals, error) {
	devices, sessions := []stayDeviceRow{}, []staySessionRow{}
	var t usageTotals
	col, ok := usageSubjectColumn[kind]
	if !ok {
		return devices, sessions, t, errors.New("unknown access source type")
	}
	drows, err := s.db.Query(ctx, subjectDevicesSQL(col), id, s.tenantID, s.siteID)
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

	srows, err := s.db.Query(ctx, subjectSessionsSQL(col), id, s.tenantID, s.siteID)
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

// ------------------------------------------------------------------------ one device, one period ---------

// macFromPath reads the {mac} path parameter, whatever shape the caller sent it in.
//
// WHY THIS IS NOT JUST chi.URLParam. A MAC contains colons, and a colon is a reserved character in a URL
// path, so a correct client escapes it: encodeURIComponent turns 7e:58:… into 7e%3A58%3A…. chi hands back
// the RAW segment, still escaped, and `$1::macaddr` then fails to cast. The screen reported "this appliance
// has no record of that device" about a device with twelve sessions and 576 MB of traffic, because a cast
// error and an empty result had been given the same answer.
//
// The client was not wrong and neither was the database. The server was simply reading an encoded string as
// if it were a literal one. So: unescape first, then PARSE it as hardware address rather than trusting the
// text -- which also makes the endpoint accept the dash and dot notations an operator might paste from a
// router or a switch, and normalise every one of them to the single form Postgres stores.
//
// The second return value distinguishes "you did not give me a MAC" from "I have no record of it". Those are
// different answers and only one of them is about the device.
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
		// PostgreSQL's macaddr is six bytes. An EUI-64 or an Infiniband address parses here but cannot be
		// stored there, so refusing it now is better than a cast error later.
		return "", false
	}
	return hw.String(), true
}

// getDeviceUsage answers "how much did this device use between these dates".
//
// It reports a DEVICE. It does not name a guest, and it does not imply the MAC identifies one -- it lists the
// stays the device was associated with over the period, which is a different and defensible claim.
func (s *server) getDeviceUsage(w http.ResponseWriter, r *http.Request) {
	mac, ok := macFromPath(r)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "bad_mac",
			"that is not a device address. A device address looks like 7a:1b:2c:3d:4e:5f.")
		return
	}
	from, to := parseWindow(r)

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

	// THREE OUTCOMES, THREE ANSWERS. Collapsing them into one 404 is what hid this: a device with twelve
	// sessions was reported as unknown because the query had errored, not because the row was absent.
	switch err := s.db.QueryRow(r.Context(),
		`SELECT first_seen, last_seen FROM iam_v2.devices
		  WHERE mac = $1::macaddr AND tenant_id = $2 AND site_id = $3`,
		mac, s.tenantID, s.siteID).Scan(&out.FirstSeen, &out.LastSeen); {
	case err == nil:
		// found
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

// ------------------------------------------------------------- the samples behind one total --------------

type usageSample struct {
	At        time.Time `json:"sampled_at"`
	BytesDown int64     `json:"bytes_down"`
	BytesUp   int64     `json:"bytes_up"`
}

// listSessionSamples is what makes a disputed total defensible: the durable samples it was summed from.
func (s *server) listSessionSamples(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "session_id")
	rows, err := s.db.Query(r.Context(), `
		SELECT sampled_at, bytes_down, bytes_up
		  FROM iam_v2.accounting_records
		 WHERE session_id = $1 AND tenant_id = $2 AND site_id = $3
		 ORDER BY sampled_at ASC
		 LIMIT 2000`, id, s.tenantID, s.siteID)
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
	// The sum is returned WITH the samples so the screen can show that they add up to the session total
	// rather than asking the operator to trust that they do.
	writeJSON(w, http.StatusOK, map[string]any{
		"samples": out, "sample_count": len(out),
		"bytes_down": down, "bytes_up": up, "bytes_total": down + up,
	})
}

// ------------------------------------------------------------------ by access source -------------------
//
// AN ACCESS SOURCE is the thing that granted access: the SUBJECT of the entitlement a session ran under,
// which is exactly one of stay, client account, voucher or guest principal (ent_one_subject). The totals are
// the same sums of iam_v2.sessions bytes as everywhere else in this file -- the grouping changes, the
// accounting does not.
//
// WHAT EACH SOURCE MAY SAY ABOUT ITSELF, and why:
//
//   stay     room + PMS interface (always together), reservation, stay status, arrival, departure. Exactly
//            what /usage/stays already says.
//   account  the client account's USERNAME. Operators already read it on Client accounts, where they created
//            it; it is the handle they will be given at the desk ("my login is 4202smith"). The account's
//            display name, notes and password hash are NOT read: a display name is a guest name.
//   voucher  the CARD REFERENCE -- the voucher's id, which the voucher sheet already prints as "Card
//            reference". Never the code, and nothing from iam_v2.vouchers: svc_edged holds no SELECT there
//            (deploy/gatep/svc-voucher-iamv2-grants.sql), so the batch is not named. The reference lives on
//            the entitlement itself.
//
//   The fourth subject, the email / phone / social sign-in (guest_principal_id), is NOT offered: what
//   identifies it lives in iam_v2.guest_principals and guest_principal_identities, which svc_edged may not
//   read. A source the operator cannot recognise is not a source worth listing. Its sessions still appear
//   under "By device".

// usageSubjectColumn maps a source type to the entitlement column that holds it. It is the ONLY way a source
// type reaches SQL text: the request picks a key, never a column.
var usageSubjectColumn = map[string]string{
	"stay":    "stay_id",
	"account": "guest_account_id",
	"voucher": "voucher_id",
	// A package chosen without signing in: the anonymous access subject (0095). Identified only by its
	// opaque reference -- it carries no name, no MAC and no identity factor to show.
	"open": "anonymous_subject_id",
}

type usageSourceRow struct {
	Type string `json:"source_type"`
	ID   string `json:"source_id"`
	// account only
	Username string `json:"account_username,omitempty"`
	// stay only. Room and interface travel TOGETHER, always.
	Room         string     `json:"room,omitempty"`
	PMSInterface string     `json:"pms_interface,omitempty"`
	Reservation  string     `json:"reservation,omitempty"`
	StayStatus   string     `json:"stay_status,omitempty"`
	Arrival      *time.Time `json:"arrival,omitempty"`
	Departure    *time.Time `json:"departure,omitempty"`
	EffectiveOut *time.Time `json:"effective_checkout_at,omitempty"`
	// every source
	Totals        usageTotals `json:"totals"`
	QuotaBytes    *int64      `json:"quota_bytes,omitempty"`
	ConsumedBytes *int64      `json:"consumed_bytes,omitempty"`
	EndReason     string      `json:"end_reason,omitempty"`
	LastActivity  *time.Time  `json:"last_activity,omitempty"`
}

// usageSourceListSQL lists access sources with their measured totals over [from, to).
// $1 tenant, $2 site, $3 from, $4 to, $5 source type (empty for every type), $6 the search text, LIKE-escaped.
// The search matches what each source can honestly be searched by: a stay by room or reservation, an account
// by username, a voucher by the start of its card reference.
func usageSourceListSQL(limit int) string {
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
		         MAX(e.data_quota_bytes)         AS quota,
		         MAX(e.consumed_data_bytes)      AS consumed,
		         MAX(e.terminal_reason)          AS end_reason,
		         MAX(se.started)                 AS last_started
		    FROM iam_v2.entitlements e
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
		 LIMIT ` + strconv.Itoa(limit)
}

// parseSourceType reads a source type. An empty value (or "all") means every type; anything unknown is
// refused rather than silently widened to "every type".
func parseSourceType(v string) (string, bool) {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" || v == "all" {
		return "", true
	}
	_, ok := usageSubjectColumn[v]
	return v, ok
}

// normaliseSourceQuery trims the search text and, because the voucher screens print a shortened card
// reference as "3fa85f64…", drops a trailing ellipsis so a pasted reference still matches.
func normaliseSourceQuery(q string) string {
	q = strings.TrimSpace(q)
	q = strings.TrimSuffix(q, "…")
	q = strings.TrimSuffix(q, "...")
	return strings.TrimSpace(q)
}

// listUsageSources lists access sources, heaviest first.
func (s *server) listUsageSources(w http.ResponseWriter, r *http.Request) {
	kind, ok := parseSourceType(r.URL.Query().Get("type"))
	if !ok {
		jsonErr(w, http.StatusBadRequest, "bad_type",
			"the access source type must be one of: stay, account, voucher, open")
		return
	}
	from, to := parseWindow(r)
	limit := queryLimit(r, 50, 200)
	q := likeEscape(normaliseSourceQuery(r.URL.Query().Get("q")))

	rows, err := s.db.Query(r.Context(), usageSourceListSQL(limit),
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
	writeList(w, out)
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
		       en.data_quota_bytes, en.consumed_data_bytes, COALESCE(en.terminal_reason, ''),
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
		       x.data_quota_bytes, x.consumed_data_bytes, COALESCE(x.terminal_reason, ''),
		       x.status, COALESCE(spr.name, '')
		  FROM iam_v2.entitlements x
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = x.service_plan_revision_id
		 WHERE x.voucher_id = $1 AND x.tenant_id = $2 AND x.site_id = $3
		 ORDER BY x.activated_at DESC NULLS LAST LIMIT 1`

// openHeaderSQL is voucherHeaderSQL for the anonymous access subject: its most recent entitlement here.
const openHeaderSQL = `
		SELECT x.anonymous_subject_id::text,
		       x.data_quota_bytes, x.consumed_data_bytes, COALESCE(x.terminal_reason, ''),
		       x.status, COALESCE(spr.name, '')
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
	out, err := s.readSourceUsage(r.Context(), kind, id)
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

func (s *server) readSourceUsage(ctx context.Context, kind, id string) (sourceUsageDetail, error) {
	out := sourceUsageDetail{Devices: []stayDeviceRow{}, Sessions: []staySessionRow{}}
	out.Source.Type = kind

	var err error
	switch kind {
	case "stay":
		st, e := s.readStayUsage(ctx, id)
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
	devices, sessions, totals, err := s.subjectDevicesAndSessions(ctx, kind, id)
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
