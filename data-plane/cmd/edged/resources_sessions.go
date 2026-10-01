package main

// Guest session visibility and admin disconnect, over the SINGLE session authority.
//
// These reads used to come from public.sessions. That table was the superseded session domain and is gone;
// iam_v2.sessions is the authority, so the operator's view moves onto it rather than disappearing. The
// disconnect enforcement action still goes through scd, which owns nftables/tc state, exactly like
// portald's logout path.
//
// Two column names changed with the move and are aliased here rather than renamed in the API, so the
// operator surface keeps its contract: started_at is iam_v2.sessions.started, ended_at is ended. There is
// no last_activity_at in the current model -- liveness is derived from accounting, not stamped on the
// session row -- so it reports the session start until the first accounting tick would have moved it.
//
// ---------------------------------------------------------------------------------------------------------
// WHO IS THIS SESSION? The list used to answer with an IP and a MAC address.
//
// That is the one question the screen exists to answer and the only question it could not answer. A duty
// manager looking at "a4:83:e7:2f:11:c0 · 10.80.4.62" cannot tell whether that is room 412, a staff laptop on
// a username, or a conference voucher — so the Active sessions screen could show that twelve devices were
// online and nothing about who they belonged to, and "disconnect the guest in 318" was not a thing the product
// could do.
//
// Every session already hangs off an Entitlement, and an Entitlement has EXACTLY ONE subject by database
// constraint (ent_one_subject): a Stay, a guest account, a voucher or a guest principal. So the subject is
// knowable without any schema change — it just was not being read. The same join reaches the Internet package
// and Service plan the session was granted from, which is the other thing an operator was asked to find out by
// cross-referencing three screens.
//
// WHAT IS DELIBERATELY ABSENT, and why it is absent rather than missing:
//
//   * a voucher CODE. svc_edged holds no privilege on iam_v2.vouchers — by design, since the admin service is
//     not the voucher authority — so a voucher-backed session reports its kind and nothing more. Widening that
//     grant is a trust-boundary change, not a UI improvement, so the surface says "Voucher" honestly instead.
//   * a guest principal's email or phone, for the same reason: iam_v2.guest_principals is not readable here.
//   * anything from iam_v2.devices, which is also not granted. The MAC and IP on the session row are the
//     device facts this service may have, and they are the ones it reports.

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type edgeSessionRow struct {
	ID             string     `json:"id"`
	IP             string     `json:"ip"`
	MAC            string     `json:"mac"`
	State          string     `json:"state"`
	StartedAt      time.Time  `json:"started_at"`
	LastActivityAt time.Time  `json:"last_activity_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	EndReason      *string    `json:"end_reason,omitempty"`
	BytesUp        int64      `json:"bytes_up"`
	BytesDown      int64      `json:"bytes_down"`

	// HOW THE GUEST PROVED WHO THEY WERE, as the session recorded it at the time.
	CredentialMethod *string `json:"credential_method,omitempty"`

	// WHICH GUEST NETWORK THE DEVICE IS ON. sessions.ingress_interface is the bridge the traffic arrived on,
	// which is an implementation name ("br-guest-30"); the guest_networks row that owns that bridge carries the
	// name the operator gave it. Reported as both, because a network engineer diagnosing a VLAN wants the
	// bridge and a duty manager wants "Guest Wi-Fi".
	IngressInterface *string `json:"ingress_interface,omitempty"`
	NetworkName      *string `json:"guest_network_name,omitempty"`

	// THE SUBJECT — the single answer to "who is this".
	//
	// SubjectKind is a closed set: room | account | voucher | guest | open (a package chosen without signing in). SubjectLabel is what to show (a room
	// number, a username); SubjectName is the human name when one is known. Both may be absent, and a client
	// that falls back to the MAC address when they are is behaving correctly.
	EntitlementID     string  `json:"entitlement_id,omitempty"`
	EntitlementStatus *string `json:"entitlement_status,omitempty"`
	SubjectKind       string  `json:"subject_kind,omitempty"`
	SubjectLabel      *string `json:"subject_label,omitempty"`
	SubjectName       *string `json:"subject_name,omitempty"`

	// Stay context, present only for a room-authenticated session.
	StayID      *string `json:"stay_id,omitempty"`
	Room        *string `json:"room,omitempty"`
	Reservation *string `json:"external_reservation_id,omitempty"`

	// WHAT THEY WERE GIVEN. The package is the guest-facing offer; the plan is the service behind it. An
	// operator answering "why is this guest slow" needs the plan's numbers, not the package's name.
	PackageCode *string `json:"package_code,omitempty"`
	PackageName *string `json:"package_name,omitempty"`
	PlanCode    *string `json:"service_plan_code,omitempty"`
	DownKbps    *int32  `json:"down_kbps,omitempty"`
	UpKbps      *int32  `json:"up_kbps,omitempty"`
	MaxDevices  *int32  `json:"max_devices,omitempty"`

	// THE ALLOWANCE AND WHAT IS LEFT OF IT, from the Entitlement's own counters and the plan's quotas. Null
	// quota means unmetered on that axis; the client must not render a meter for an allowance that does not
	// exist.
	DataQuotaBytes   *int64 `json:"data_quota_bytes,omitempty"`
	DataUsedBytes    *int64 `json:"data_used_bytes,omitempty"`
	TimeQuotaSeconds *int64 `json:"time_quota_seconds,omitempty"`
	TimeUsedSeconds  *int64 `json:"time_used_seconds,omitempty"`

	// How many devices this same Entitlement currently has online. "3 of 4 devices" is the fact behind a guest
	// complaining that their fourth device is refused.
	ActiveDevices int `json:"active_devices"`
}

// sessionCols keeps the historical alias pairs (started AS started_at, ended AS ended_at) so the wire contract
// does not move, and adds the subject/offer projection described above.
//
// Every added join is LEFT. A session whose Entitlement, Stay or package row cannot be read must still appear
// in the list: an operator hunting a device they cannot account for is exactly who needs the row that will not
// resolve, and dropping it would make the screen quietly incomplete.
//
// THE DATA ALLOWANCE SHOWN IS THE ONE BEING ENFORCED. It reads COALESCE(e.data_quota_bytes,
// spr.data_quota_bytes), which is character-for-character what enforce.go and checkout.go decide a guest's
// limit from. A PER_STAY_NIGHT grant freezes its own allowance onto the Entitlement at grant time — nights ×
// the configured GB — and the pinned plan revision answers for every entitlement granted before that mode
// existed. Projecting spr.data_quota_bytes alone, as this did, showed the property the plan's underlying
// number instead: a guest granted 7 GB for a seven-night stay was displayed as "used 1.2 GB of 100 MB", which
// reads as a guest hugely over their limit who has somehow not been cut off. The usage beside it
// (e.consumed_data_bytes) was always the Entitlement's, so the two halves of the meter described different
// things. There is deliberately no equivalent for time: entitlements carry no time_quota_seconds override, so
// the plan revision is the only source and spr.time_quota_seconds is already correct.
const sessionCols = `s.id, s.ip::text, s.mac::text, s.state,
       s.started AS started_at, s.started AS last_activity_at,
       s.ended AS ended_at, s.expires_at, s.end_reason, s.bytes_up, s.bytes_down,
       NULLIF(s.credential_method,''), NULLIF(s.ingress_interface,''), gn.name,
       COALESCE(e.id::text,''), e.status,
       ` + sessionKindExpr + ` AS subject_kind,
       COALESCE(NULLIF(st.normalized_room_number,''), ga.username) AS subject_label,
       COALESCE(
         (SELECT COALESCE(NULLIF(g.display_name,''), NULLIF(g.last_name_norm,''))
            FROM iam_v2.stay_guests g WHERE g.stay_id = st.id
           ORDER BY g.is_primary DESC LIMIT 1),
         NULLIF(ga.display_name,'')
       ) AS subject_name,
       st.id::text, st.normalized_room_number, st.external_reservation_id,
       ip.code, NULLIF(ipr.display->>'name',''),
       sp.code, spr.down_kbps, spr.up_kbps, spr.max_concurrent_devices,
       COALESCE(e.data_quota_bytes, spr.data_quota_bytes), e.consumed_data_bytes,
       spr.time_quota_seconds, e.consumed_online_seconds,
       (SELECT count(DISTINCT d.mac)::int FROM iam_v2.sessions d
         WHERE d.entitlement_id = s.entitlement_id AND d.state = 'active')`

// sessionFrom is shared by the list and the single-row read so the two cannot drift into describing the same
// session differently.
const sessionFrom = `FROM iam_v2.sessions s
       LEFT JOIN iam_v2.entitlements e
              ON e.tenant_id = s.tenant_id AND e.site_id = s.site_id AND e.id = s.entitlement_id
       LEFT JOIN iam_v2.stays st
              ON st.tenant_id = e.tenant_id AND st.site_id = e.site_id AND st.id = e.stay_id
       LEFT JOIN iam_v2.guest_access_accounts ga
              ON ga.tenant_id = e.tenant_id AND ga.site_id = e.site_id AND ga.id = e.guest_account_id
       LEFT JOIN iam_v2.internet_package_revisions ipr
              ON ipr.tenant_id = e.tenant_id AND ipr.site_id = e.site_id AND ipr.id = e.package_revision_id
       LEFT JOIN iam_v2.internet_packages ip
              ON ip.tenant_id = ipr.tenant_id AND ip.site_id = ipr.site_id AND ip.id = ipr.package_id
       LEFT JOIN iam_v2.service_plan_revisions spr
              ON spr.tenant_id = e.tenant_id AND spr.site_id = e.site_id AND spr.id = e.service_plan_revision_id
       LEFT JOIN iam_v2.service_plans sp
              ON sp.tenant_id = spr.tenant_id AND sp.site_id = spr.site_id AND sp.id = spr.service_plan_id
       LEFT JOIN public.guest_networks gn
              ON gn.tenant_id = s.tenant_id AND gn.site_id = s.site_id
             AND gn.bridge_name = NULLIF(s.ingress_interface,'')`

func scanEdgeSession(row interface{ Scan(...any) error }, e *edgeSessionRow) error {
	return row.Scan(&e.ID, &e.IP, &e.MAC, &e.State, &e.StartedAt, &e.LastActivityAt,
		&e.EndedAt, &e.ExpiresAt, &e.EndReason, &e.BytesUp, &e.BytesDown,
		&e.CredentialMethod, &e.IngressInterface, &e.NetworkName,
		&e.EntitlementID, &e.EntitlementStatus, &e.SubjectKind, &e.SubjectLabel, &e.SubjectName,
		&e.StayID, &e.Room, &e.Reservation,
		&e.PackageCode, &e.PackageName,
		&e.PlanCode, &e.DownKbps, &e.UpKbps, &e.MaxDevices,
		&e.DataQuotaBytes, &e.DataUsedBytes, &e.TimeQuotaSeconds, &e.TimeUsedSeconds,
		&e.ActiveDevices)
}

func (s *server) sessionsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listGuestSessions)
	// PHASE 6 (DARK): the online-time budget view. Registered BEFORE the {id} pattern so the static path
	// wins, and mounted here rather than as its own resource because it is live access state -- exactly what
	// this resource already means -- so it inherits the role matrix instead of adding a row to it.
	if s.phase6.AggregateTimeOn() {
		// A SUB-CAPABILITY OF A MOUNTED RESOURCE. Online-time budgets live under "sessions", which is always
		// mounted, so the resource name alone cannot tell the navigation whether this screen has anything
		// behind it. Recorded under its own name for the same reason mountResource records the others: where
		// it becomes reachable is the only place that cannot drift.
		s.surfaces.add("sessions.aggregate-time")
		r.Get("/aggregate-time", s.listAggregateTime)
	} else {
		// A FEATURE THAT IS OFF MUST SAY SO, NOT BE MISREAD AS A SESSION ID.
		//
		// Without this the request fell through to /{id} below, which looked up the guest session whose id is
		// the literal string "aggregate-time", asked PostgreSQL to compare a uuid column with it, and
		// answered the operator HTTP 500 "query failed". The Online-time screen therefore reported a server
		// error for a capability this appliance simply does not run. The route now exists in both states and
		// tells the truth in the disabled one.
		r.Get("/aggregate-time", func(w http.ResponseWriter, _ *http.Request) {
			jsonErr(w, http.StatusNotFound, "not_enabled",
				"online-time budgets are not enabled on this appliance")
		})
	}
	r.Get("/{id}", s.getGuestSession)
	r.Post("/{id}/disconnect", s.disconnectGuestSession)
	return r
}

// sessionKindExpr is the one definition of a session's subject kind. The list's kind filter and the projected
// subject_kind use this same text, so filtering and displaying cannot disagree about what a row is.
const sessionKindExpr = `CASE
         WHEN e.stay_id             IS NOT NULL THEN 'room'
         WHEN e.guest_account_id    IS NOT NULL THEN 'account'
         WHEN e.voucher_id          IS NOT NULL THEN 'voucher'
         WHEN e.guest_principal_id  IS NOT NULL THEN 'guest'
         WHEN e.anonymous_subject_id IS NOT NULL THEN 'open'
         ELSE ''
       END`

// SESSIONS ARE PAGED AND SEARCHED ON THE SERVER.
//
// The list used to be the 200 most recent sessions, searched and counted in the browser, so on a busy evening
// a device that signed in earlier was not on the screen at all and the tiles counted only what had loaded. The
// search text travels in X-Session-Search, never the URL: it is a room, a username or a guest's name, and
// edged logs request lines.
const sessionSearchHeader = "X-Session-Search"

// sessionMatch is the kind and search filter. $3 kind (NULL for every kind), $4 the ILIKE pattern (NULL for no
// search). It matches what the screen shows a row by: room, guest name, username, reservation, address,
// device, package and network.
const sessionMatch = `($3::text IS NULL OR ` + sessionKindExpr + ` = $3)
           AND ($4::text IS NULL
                OR st.normalized_room_number ILIKE $4
                OR st.external_reservation_id ILIKE $4
                OR ga.username ILIKE $4
                OR ga.display_name ILIKE $4
                OR s.ip::text ILIKE $4
                OR s.mac::text ILIKE $4
                OR ip.code ILIKE $4
                OR ipr.display->>'name' ILIKE $4
                OR gn.name ILIKE $4
                OR EXISTS (SELECT 1 FROM iam_v2.stay_guests g
                            WHERE g.stay_id = st.id
                              AND (g.display_name ILIKE $4 OR g.last_name_norm ILIKE $4)))`

// sessionsSummary describes every session in the selected state -- not the page, and not narrowed by the kind
// or search filter, so the tiles and the per-kind counts stay put while the operator narrows the table.
type sessionsSummary struct {
	Devices int            `json:"devices_online"`
	Clients int            `json:"clients_online"`
	Rooms   int            `json:"rooms_online"`
	Bytes   int64          `json:"bytes_total"`
	Kinds   map[string]int `json:"kinds"`
}

type sessionsPage struct {
	pagedList[edgeSessionRow]
	Summary sessionsSummary `json:"summary"`
}

func (s *server) listGuestSessions(w http.ResponseWriter, r *http.Request) {
	var stateArg any
	if v := r.URL.Query().Get("state"); v != "" {
		// The authority's own vocabulary. iam_v2.sessions is 'active', 'PENDING_ENFORCEMENT' (durable but
		// not yet enforced) or 'ended'; the legacy domain called the last one 'closed'. The old spelling is
		// still accepted so an existing operator bookmark or script does not break, but it is translated
		// here rather than carried any deeper.
		switch v {
		case "active", "ended", "PENDING_ENFORCEMENT":
		case "closed":
			v = "ended"
		default:
			jsonErr(w, http.StatusBadRequest, "bad_request",
				"state must be active|PENDING_ENFORCEMENT|ended")
			return
		}
		stateArg = v
	}
	var kindArg any
	switch v := r.URL.Query().Get("kind"); v {
	case "":
	case "room", "account", "voucher", "guest", "open":
		kindArg = v
	default:
		jsonErr(w, http.StatusBadRequest, "bad_request", "kind must be room|account|voucher|guest|open")
		return
	}
	pg, ok := readPage(w, r, 0)
	if !ok {
		return
	}
	search, ok := readSearch(w, r, sessionSearchHeader)
	if !ok {
		return
	}
	args := []any{s.tenantID, stateArg, kindArg, likePattern(search)}

	ctx, cancel := dbCtx(r)
	defer cancel()

	sum := sessionsSummary{Kinds: map[string]int{}}
	var total, kRoom, kAccount, kVoucher, kGuest, kOpen, kNone int
	if err := s.db.QueryRow(ctx, `
        SELECT count(*) FILTER (WHERE s.state = 'active')::int,
               count(DISTINCT COALESCE(e.id::text, s.mac::text)) FILTER (WHERE s.state = 'active')::int,
               count(DISTINCT COALESCE(NULLIF(st.normalized_room_number,''), ga.username))
                     FILTER (WHERE s.state = 'active' AND e.stay_id IS NOT NULL)::int,
               COALESCE(sum(s.bytes_down + s.bytes_up), 0)::bigint,
               count(*) FILTER (WHERE `+sessionMatch+`)::int,
               count(*) FILTER (WHERE `+sessionKindExpr+` = 'room')::int,
               count(*) FILTER (WHERE `+sessionKindExpr+` = 'account')::int,
               count(*) FILTER (WHERE `+sessionKindExpr+` = 'voucher')::int,
               count(*) FILTER (WHERE `+sessionKindExpr+` = 'guest')::int,
               count(*) FILTER (WHERE `+sessionKindExpr+` = 'open')::int,
               count(*) FILTER (WHERE `+sessionKindExpr+` = '')::int
          `+sessionFrom+`
         WHERE s.tenant_id = $1
           AND ($2::text IS NULL OR s.state = $2)
    `, args...).Scan(&sum.Devices, &sum.Clients, &sum.Rooms, &sum.Bytes, &total,
		&kRoom, &kAccount, &kVoucher, &kGuest, &kOpen, &kNone); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	sum.Kinds = map[string]int{"room": kRoom, "account": kAccount, "voucher": kVoucher, "guest": kGuest, "open": kOpen, "": kNone}

	// s.id breaks ties on the start time, so a page boundary is stable.
	rows, err := s.db.Query(ctx, `
        SELECT `+sessionCols+`
          `+sessionFrom+`
         WHERE s.tenant_id = $1
           AND ($2::text IS NULL OR s.state = $2)
           AND `+sessionMatch+`
         ORDER BY s.started DESC, s.id
         LIMIT $5 OFFSET $6
    `, append(args, pg.Fetch(), pg.Offset())...)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []edgeSessionRow
	for rows.Next() {
		var e edgeSessionRow
		if err := scanEdgeSession(rows, &e); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	out, more := trimPage(out, pg)
	writeJSON(w, http.StatusOK, sessionsPage{pagedList: newPagedList(out, more, pg, &total), Summary: sum})
}

func (s *server) getGuestSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var e edgeSessionRow
	err := scanEdgeSession(s.db.QueryRow(ctx,
		`SELECT `+sessionCols+` `+sessionFrom+` WHERE s.id = $1 AND s.tenant_id = $2`,
		id, s.tenantID), &e)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) disconnectGuestSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()

	var ip, state string
	err := s.db.QueryRow(ctx,
		`SELECT host(ip), state FROM iam_v2.sessions WHERE id = $1 AND tenant_id = $2`,
		id, s.tenantID).Scan(&ip, &state)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if state != "active" {
		jsonErr(w, http.StatusConflict, "conflict", "session is "+state+"; only active sessions can be disconnected")
		return
	}

	st, raw, err := s.scd.call(r.Context(), http.MethodPost, "/v1/sessions/revoke",
		map[string]string{"ip": ip, "reason": "admin"})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	if st != http.StatusOK {
		// Relay scd's verdict verbatim (e.g. session already gone in kernel).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		_, _ = w.Write(raw)
		return
	}
	s.audit(r, "session.disconnected", "session", id, map[string]any{"ip": ip})
	writeJSON(w, http.StatusOK, map[string]string{"session_id": id, "status": "disconnected"})
}
