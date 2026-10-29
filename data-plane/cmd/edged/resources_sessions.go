package main

// Guest session visibility and admin disconnect over iam_v2.sessions; disconnect enforcement goes through scd (owns nftables/tc).
// started_at/ended_at alias iam_v2.sessions.started/ended; there is no last_activity_at, so it reports the session start.
// Subject, package and plan come from the session's Entitlement (exactly one subject, by ent_one_subject).
// Deliberately absent: voucher code, guest principal email/phone and iam_v2.devices data. svc_edged has no grant on them,
// and widening it is a trust-boundary change.

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

	// How the guest proved who they were, as recorded at the time.
	CredentialMethod *string `json:"credential_method,omitempty"`

	// ingress_interface is the bridge name ("br-guest-30"); the owning guest_networks row carries the operator's name. Both are reported.
	IngressInterface *string `json:"ingress_interface,omitempty"`
	NetworkName      *string `json:"guest_network_name,omitempty"`

	// SubjectKind is a closed set: room | account | voucher | guest | open (a package chosen without signing in).
	// SubjectLabel is a room number or username; SubjectName the human name. Both may be absent; clients then fall back to the MAC.
	EntitlementID     string  `json:"entitlement_id,omitempty"`
	EntitlementStatus *string `json:"entitlement_status,omitempty"`
	SubjectKind       string  `json:"subject_kind,omitempty"`
	SubjectLabel      *string `json:"subject_label,omitempty"`
	SubjectName       *string `json:"subject_name,omitempty"`

	StayID      *string `json:"stay_id,omitempty"`
	Room        *string `json:"room,omitempty"`
	Reservation *string `json:"external_reservation_id,omitempty"`
	// The PMS connection the stay came from: a room number means nothing without it where a site has several.
	PMSInterface *string `json:"pms_interface_label,omitempty"`

	// The package is the guest-facing offer; the plan is the service behind it (its numbers explain slow sessions).
	PackageCode *string `json:"package_code,omitempty"`
	PackageName *string `json:"package_name,omitempty"`
	PlanCode    *string `json:"service_plan_code,omitempty"`
	DownKbps    *int32  `json:"down_kbps,omitempty"`
	UpKbps      *int32  `json:"up_kbps,omitempty"`
	MaxDevices  *int32  `json:"max_devices,omitempty"`

	// The allowance from the Entitlement's counters and the plan's quotas. Null quota means unmetered; clients must not render a meter.
	DataQuotaBytes   *int64 `json:"data_quota_bytes,omitempty"`
	DataUsedBytes    *int64 `json:"data_used_bytes,omitempty"`
	TimeQuotaSeconds *int64 `json:"time_quota_seconds,omitempty"`
	TimeUsedSeconds  *int64 `json:"time_used_seconds,omitempty"`

	ActiveDevices int `json:"active_devices"`

	// Times this client has had this package, counting this session: activated entitlements for the same subject on any revision of it. 0 with no resolvable entitlement.
	PackageAcquisitions int `json:"package_acquisitions"`
}

// sessionCols keeps the started AS started_at / ended AS ended_at aliases so the wire contract does not move.
// Every added join is LEFT: a session whose Entitlement, Stay or package row cannot be read must still be listed.
// The data allowance is COALESCE(e.data_quota_bytes, spr.data_quota_bytes), matching enforce.go and checkout.go:
// PER_STAY_NIGHT grants freeze their own allowance on the Entitlement, so the plan value alone would misstate it.
// Entitlements carry no time-quota override, so spr.time_quota_seconds is the only time source.
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
       (SELECT NULLIF(pi.display_label,'') FROM iam_v2.pms_interfaces pi WHERE pi.id = st.pms_interface_id),
       ip.code, NULLIF(ipr.display->>'name',''),
       sp.code, spr.down_kbps, spr.up_kbps, spr.max_concurrent_devices,
       COALESCE(e.data_quota_bytes, spr.data_quota_bytes), e.consumed_data_bytes,
       spr.time_quota_seconds, e.consumed_online_seconds,
       (SELECT count(DISTINCT d.mac)::int FROM iam_v2.sessions d
         WHERE d.entitlement_id = s.entitlement_id AND d.state = 'active'),
       -- Times this subject has had this package (any revision), up to and including this entitlement.
       CASE WHEN e.id IS NULL OR ipr.package_id IS NULL OR e.activated_at IS NULL THEN 0 ELSE
         (SELECT count(*)::int FROM iam_v2.entitlements e2
            JOIN iam_v2.internet_package_revisions r2
              ON r2.tenant_id = e2.tenant_id AND r2.site_id = e2.site_id AND r2.id = e2.package_revision_id
           WHERE e2.tenant_id = e.tenant_id AND e2.site_id = e.site_id
             AND r2.package_id = ipr.package_id
             AND e2.activated_at IS NOT NULL AND e2.activated_at <= e.activated_at
             AND e2.stay_id IS NOT DISTINCT FROM e.stay_id
             AND e2.guest_account_id IS NOT DISTINCT FROM e.guest_account_id
             AND e2.voucher_id IS NOT DISTINCT FROM e.voucher_id
             AND e2.guest_principal_id IS NOT DISTINCT FROM e.guest_principal_id
             AND e2.anonymous_subject_id IS NOT DISTINCT FROM e.anonymous_subject_id)
       END`

// sessionFrom is shared by the list and single-row read so they cannot describe a session differently.
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
		&e.StayID, &e.Room, &e.Reservation, &e.PMSInterface,
		&e.PackageCode, &e.PackageName,
		&e.PlanCode, &e.DownKbps, &e.UpKbps, &e.MaxDevices,
		&e.DataQuotaBytes, &e.DataUsedBytes, &e.TimeQuotaSeconds, &e.TimeUsedSeconds,
		&e.ActiveDevices, &e.PackageAcquisitions)
}

func (s *server) sessionsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listGuestSessions)
	// Online-time budget view: registered before the {id} pattern so the static path wins; it is live access state, so it inherits this resource's role matrix.
	if s.phase6.AggregateTimeOn() {
		// Recorded under its own name so navigation can tell whether this sub-capability is reachable.
		s.surfaces.add("sessions.aggregate-time")
		r.Get("/aggregate-time", s.listAggregateTime)
	} else {
		// A disabled feature must answer for itself; otherwise the request fell through to /{id} and returned HTTP 500 from comparing a uuid with "aggregate-time".
		r.Get("/aggregate-time", func(w http.ResponseWriter, _ *http.Request) {
			jsonErr(w, http.StatusNotFound, "not_enabled",
				"online-time budgets are not enabled on this appliance")
		})
	}
	r.Get("/{id}", s.getGuestSession)
	r.Post("/{id}/disconnect", s.disconnectGuestSession)
	return r
}

// sessionKindExpr is the single definition of subject kind, shared by the kind filter and subject_kind so they cannot disagree.
const sessionKindExpr = `CASE
         WHEN e.stay_id             IS NOT NULL THEN 'room'
         WHEN e.guest_account_id    IS NOT NULL THEN 'account'
         WHEN e.voucher_id          IS NOT NULL THEN 'voucher'
         WHEN e.guest_principal_id  IS NOT NULL THEN 'guest'
         WHEN e.anonymous_subject_id IS NOT NULL THEN 'open'
         ELSE ''
       END`

// Sessions are paged and searched on the server. The search text travels in X-Session-Search, never the URL, because edged logs request lines.
const sessionSearchHeader = "X-Session-Search"

// sessionMatch is the kind and search filter. $3 kind (NULL for every kind), $4 the ILIKE pattern (NULL for no search).
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

// sessionsSummary covers every session in the selected state, not the page and not narrowed by kind or search.
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
	// Window is the period applied to ENDED sessions (AD-56). Active and pending sessions are always listed.
	Window listWindowInfo `json:"window"`
}

func (s *server) listGuestSessions(w http.ResponseWriter, r *http.Request) {
	var stateArg any
	if v := r.URL.Query().Get("state"); v != "" {
		// iam_v2.sessions states are 'active', 'PENDING_ENFORCEMENT' and 'ended'; the old spelling 'closed' is still accepted and translated here.
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
	since, winInfo, werr := parseListWindow(r.URL.Query(), time.Now())
	if werr != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", werr.Error())
		return
	}
	var sinceArg any
	if since != nil {
		sinceArg = *since
	}
	// $5 is the window start (NULL: the whole history). It narrows ended sessions only: a live session is listed however old.
	args := []any{s.tenantID, stateArg, kindArg, likePattern(search), sinceArg}

	ctx, cancel := dbCtx(r)
	defer cancel()

	sum := sessionsSummary{Kinds: map[string]int{}}
	var total, kRoom, kAccount, kVoucher, kGuest, kOpen, kNone int
	if err := s.db.QueryRow(ctx, `
        SELECT count(*) FILTER (WHERE s.state = 'active')::int,
               count(DISTINCT COALESCE(e.id::text, s.mac::text)) FILTER (WHERE s.state = 'active')::int,
               count(DISTINCT (st.pms_interface_id::text || '|' || COALESCE(NULLIF(st.normalized_room_number,''), '')))
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
           AND ($5::timestamptz IS NULL OR s.state <> 'ended' OR s.started >= $5)
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
           AND ($5::timestamptz IS NULL OR s.state <> 'ended' OR s.started >= $5)
           AND `+sessionMatch+`
         ORDER BY s.started DESC, s.id
         LIMIT $6 OFFSET $7
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
	writeJSON(w, http.StatusOK, sessionsPage{pagedList: newPagedList(out, more, pg, &total), Summary: sum, Window: winInfo})
}

func (s *server) getGuestSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var e edgeSessionRow
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	err := scanEdgeSession(s.db.QueryRow(ctx,
		`SELECT `+sessionCols+` `+sessionFrom+` WHERE s.id = $1 AND s.tenant_id = $2 AND s.site_id = $3`,
		id, s.tenantID, s.siteID), &e)
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

	// The id is checked before it reaches SQL or scd, and the session must be THIS site's.
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	var ip *string
	var state string
	err := s.db.QueryRow(ctx,
		`SELECT host(ip), state FROM iam_v2.sessions WHERE id = $1 AND tenant_id = $2 AND site_id = $3`,
		id, s.tenantID, s.siteID).Scan(&ip, &state)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	// PENDING_ENFORCEMENT is disconnected like active: scd's revoke ends both.
	switch {
	case state == "ended":
		jsonErr(w, http.StatusConflict, "already_ended", "this session has already ended; the device is not online on it")
		return
	case state != "active" && state != "PENDING_ENFORCEMENT":
		jsonErr(w, http.StatusConflict, "conflict", "this session cannot be disconnected in its current state ("+state+")")
		return
	case ip == nil || *ip == "":
		jsonErr(w, http.StatusConflict, "no_address",
			"this session has no recorded address, so its network access cannot be withdrawn from here")
		return
	}

	st, raw, err := s.scd.call(r.Context(), http.MethodPost, "/v1/sessions/revoke",
		map[string]string{"ip": *ip, "reason": "admin"})
	if err != nil {
		internalErr(w, r, http.StatusBadGateway, "scd_unreachable", err)
		return
	}
	if st != http.StatusOK {
		// Relay scd's verdict verbatim (e.g. session already gone in kernel).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		_, _ = w.Write(raw)
		return
	}
	s.audit(r, "session.disconnected", "session", id, map[string]any{"ip": *ip, "state": state})
	writeJSON(w, http.StatusOK, map[string]string{"session_id": id, "status": "disconnected"})
}
