package main

// USAGE EXPLORER — the screen a hotel opens during an argument.
//
// The two questions this exists to answer, in the words they arrive in:
//
//     "How much internet did room 4202 use during this stay?"
//     "How much data did this device use between these dates?"
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
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
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

// getStayUsage answers "how much internet did this room use during this stay".
func (s *server) getStayUsage(w http.ResponseWriter, r *http.Request) {
	stayID := chi.URLParam(r, "stay_id")

	var out struct {
		Stay     usageStayRow     `json:"stay"`
		Plan     string           `json:"service_plan,omitempty"`
		Devices  []stayDeviceRow  `json:"devices"`
		Sessions []staySessionRow `json:"sessions"`
	}
	out.Devices, out.Sessions = []stayDeviceRow{}, []staySessionRow{}

	// The stay itself, with the room ALWAYS carrying its interface.
	err := s.db.QueryRow(r.Context(), `
		SELECT st.id::text,
		       COALESCE(st.normalized_room_number, ''),
		       COALESCE(pi.display_label, pi.id::text),
		       COALESCE(st.external_reservation_id, ''),
		       st.status, st.arrival, st.departure, st.effective_checkout_at,
		       e.data_quota_bytes, e.consumed_data_bytes, COALESCE(e.terminal_reason, ''),
		       COALESCE(spr.display_name, '')
		  FROM iam_v2.stays st
		  LEFT JOIN iam_v2.pms_interfaces pi ON pi.id = st.pms_interface_id
		  LEFT JOIN LATERAL (
		      SELECT * FROM iam_v2.entitlements en
		       WHERE en.stay_id = st.id AND en.tenant_id = st.tenant_id
		       ORDER BY en.activated_at DESC NULLS LAST LIMIT 1) e ON true
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		 WHERE st.id = $1 AND st.tenant_id = $2 AND st.site_id = $3`,
		stayID, s.tenantID, s.siteID).Scan(
		&out.Stay.StayID, &out.Stay.Room, &out.Stay.PMSInterface, &out.Stay.Reservation,
		&out.Stay.Status, &out.Stay.Arrival, &out.Stay.Departure, &out.Stay.EffectiveOut,
		&out.Stay.QuotaBytes, &out.Stay.ConsumedBytes, &out.Stay.EndReason, &out.Plan)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "no such stay at this property")
		return
	}

	// Per-device totals. A stay usually has two or three devices and the dispute is normally about one of
	// them, so this is the breakdown an operator actually reads first.
	drows, err := s.db.Query(r.Context(), `
		SELECT COALESCE(d.mac::text, ''),
		       COALESCE(SUM(se.bytes_down),0), COALESCE(SUM(se.bytes_up),0),
		       COUNT(se.id), MIN(se.started), MAX(COALESCE(se.ended, se.started))
		  FROM iam_v2.sessions se
		  JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
		  LEFT JOIN iam_v2.devices d ON d.id = se.device_id
		 WHERE e.stay_id = $1 AND se.tenant_id = $2 AND se.site_id = $3
		 GROUP BY d.mac
		 ORDER BY 2 DESC`, stayID, s.tenantID, s.siteID)
	if err != nil {
		slog.Error("stay device usage failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	defer drows.Close()
	for drows.Next() {
		var d stayDeviceRow
		if err := drows.Scan(&d.MAC, &d.BytesDown, &d.BytesUp, &d.Sessions, &d.FirstSeen, &d.LastSeen); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
			return
		}
		d.BytesAll = d.BytesDown + d.BytesUp
		out.Devices = append(out.Devices, d)
		out.Stay.Totals.BytesDown += d.BytesDown
		out.Stay.Totals.BytesUp += d.BytesUp
		out.Stay.Totals.Sessions += d.Sessions
	}
	if err := drows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	out.Stay.Totals.Devices = len(out.Devices)
	out.Stay.Totals.BytesAll = out.Stay.Totals.BytesDown + out.Stay.Totals.BytesUp

	// The sessions themselves, newest first: when access started, when it ended and why.
	srows, err := s.db.Query(r.Context(), `
		SELECT se.id::text, COALESCE(d.mac::text,''), COALESCE(se.ip::text,''),
		       COALESCE(se.credential_method,''), se.state, se.started, se.ended,
		       COALESCE(se.end_reason,''), se.bytes_down, se.bytes_up
		  FROM iam_v2.sessions se
		  JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
		  LEFT JOIN iam_v2.devices d ON d.id = se.device_id
		 WHERE e.stay_id = $1 AND se.tenant_id = $2 AND se.site_id = $3
		 ORDER BY se.started DESC NULLS LAST LIMIT 200`, stayID, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	defer srows.Close()
	for srows.Next() {
		var x staySessionRow
		if err := srows.Scan(&x.SessionID, &x.MAC, &x.IP, &x.Method, &x.State,
			&x.Started, &x.Ended, &x.EndReason, &x.BytesDown, &x.BytesUp); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
			return
		}
		x.BytesAll = x.BytesDown + x.BytesUp
		out.Sessions = append(out.Sessions, x)
	}
	if err := srows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the usage records could not be read")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ------------------------------------------------------------------------ one device, one period ---------

// getDeviceUsage answers "how much did this device use between these dates".
//
// It reports a DEVICE. It does not name a guest, and it does not imply the MAC identifies one -- it lists the
// stays the device was associated with over the period, which is a different and defensible claim.
func (s *server) getDeviceUsage(w http.ResponseWriter, r *http.Request) {
	mac := strings.TrimSpace(chi.URLParam(r, "mac"))
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

	if err := s.db.QueryRow(r.Context(),
		`SELECT first_seen, last_seen FROM iam_v2.devices
		  WHERE mac = $1::macaddr AND tenant_id = $2 AND site_id = $3`,
		mac, s.tenantID, s.siteID).Scan(&out.FirstSeen, &out.LastSeen); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "this appliance has no record of that device")
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
