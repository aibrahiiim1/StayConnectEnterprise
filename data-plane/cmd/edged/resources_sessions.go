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
	// SubjectKind is a closed set: room | account | voucher | guest. SubjectLabel is what to show (a room
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
       CASE
         WHEN e.stay_id             IS NOT NULL THEN 'room'
         WHEN e.guest_account_id    IS NOT NULL THEN 'account'
         WHEN e.voucher_id          IS NOT NULL THEN 'voucher'
         WHEN e.guest_principal_id  IS NOT NULL THEN 'guest'
         ELSE ''
       END AS subject_kind,
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
		r.Get("/aggregate-time", s.listAggregateTime)
	}
	r.Get("/{id}", s.getGuestSession)
	r.Post("/{id}/disconnect", s.disconnectGuestSession)
	return r
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
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
        SELECT `+sessionCols+`
          `+sessionFrom+`
         WHERE s.tenant_id = $1
           AND ($2::text IS NULL OR s.state = $2)
         ORDER BY s.started DESC
         LIMIT 200
    `, s.tenantID, stateArg)
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
	writeList(w, out)
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
