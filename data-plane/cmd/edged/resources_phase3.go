package main

// Hotel-Admin surface: Stays, Stay Events, Folios, PMS resolutions, Checkout-Grace and operational alerts.
// Mounted only when the Phase-3 master and admin flags are both on; while dark the paths do not exist, so an
// unmounted route cannot leak a schema that is not live. Resolution rows carry outcome codes and identifiers only,
// never a guest's name or room; stay detail is a separate role-gated surface.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"

	"github.com/stayconnect/enterprise/data-plane/internal/grace"
)

type stayRow struct {
	ID                  string     `json:"id"`
	Interface           string     `json:"pms_interface_id"`
	InterfaceLabel      string     `json:"pms_interface_label,omitempty"`
	Reservation         string     `json:"external_reservation_id"`
	Room                *string    `json:"room,omitempty"`
	Status              string     `json:"status"`
	LifecycleVersion    int        `json:"lifecycle_version"`
	Arrival             *time.Time `json:"arrival,omitempty"`
	Departure           *time.Time `json:"departure,omitempty"`
	EffectiveCheckoutAt *time.Time `json:"effective_checkout_at,omitempty"`
	PostingAllowed      bool       `json:"posting_allowed"`
	Occupants           int        `json:"occupants"`

	// The primary guest's name, already persisted on stay_guests and shown on the detail view.
	PrimaryGuest *string `json:"primary_guest,omitempty"`

	// Why posting is blocked, and where that decision came from.
	PostingBlockReason *string `json:"posting_block_reason,omitempty"`
	PostingSource      *string `json:"posting_permission_source,omitempty"`

	// Occupancy freshness: how recently the PMS confirmed this stay; the evidence guest authentication is judged against.
	OccupancyEvidenceAt *time.Time `json:"occupancy_evidence_at,omitempty"`

	// Hotel attributes the schema carries; they depend on the connector (the Protel FIAS feed leaves them null), so
	// they are omitempty and the UI renders them only when present.
	VIP         *bool   `json:"vip,omitempty"`
	RoomType    *string `json:"room_type,omitempty"`
	RatePlan    *string `json:"rate_plan,omitempty"`
	TravelAgent *string `json:"travel_agent,omitempty"`

	// The live entitlement (PENDING, ACTIVE or SUSPENDED); a stay whose access has ended reports none. iam_v2
	// enforces at most one live entitlement per stay (ent_live_stay).
	AccessStatus        *string `json:"access_status,omitempty"`
	AccessPackageCode   *string `json:"access_package_code,omitempty"`
	AccessPackageName   *string `json:"access_package_name,omitempty"`
	AccessPlanCode      *string `json:"access_plan_code,omitempty"`
	AccessDownKbps      *int32  `json:"access_down_kbps,omitempty"`
	AccessUpKbps        *int32  `json:"access_up_kbps,omitempty"`
	AccessMaxDevices    *int32  `json:"access_max_devices,omitempty"`
	AccessActiveDevices int     `json:"access_active_devices"`
	// Devices holding a place (entitlement_devices AUTHORIZED): the device limit is enforced against this, not the
	// online count, since a device keeps its place after signing out.
	AccessAuthorizedDevices int `json:"access_authorized_devices"`

	// True when the caller lacks pms-stay-identity: guest names, the reservation number and the room sign-in credential are withheld.
	IdentityHidden bool `json:"identity_hidden,omitempty"`
}

const stayCols = `s.id::text, s.pms_interface_id::text, COALESCE(i.display_label,''),
       s.external_reservation_id,
       s.normalized_room_number, s.status, s.lifecycle_version, s.arrival, s.departure,
       s.effective_checkout_at, s.posting_allowed,
       (SELECT count(*) FROM iam_v2.stay_guests g WHERE g.stay_id = s.id)::int,
       (SELECT COALESCE(NULLIF(g.display_name,''), NULLIF(g.last_name_norm,''))
          FROM iam_v2.stay_guests g WHERE g.stay_id = s.id
         ORDER BY g.is_primary DESC LIMIT 1),
       s.posting_block_reason, s.posting_permission_source, s.occupancy_evidence_at,
       s.vip, s.room_type, s.rate_plan, s.travel_agent,
       ent.status, pkg.code, NULLIF(pkgr.display->>'name',''),
       plan.code, planr.down_kbps, planr.up_kbps, planr.max_concurrent_devices,
       COALESCE((SELECT count(DISTINCT d.mac) FROM iam_v2.sessions d
                  WHERE d.entitlement_id = ent.id AND d.state = 'active'), 0)::int,
       COALESCE((SELECT count(*) FROM iam_v2.entitlement_devices ed
                  WHERE ed.entitlement_id = ent.id AND ed.status = 'AUTHORIZED'), 0)::int`

// The FROM clause every stay query shares. Every join is LEFT so a stay with no interface row or entitlement still
// lists. The entitlement join filters live statuses in the ON clause: in a WHERE it would stop being a left join.
const stayFrom = `FROM iam_v2.stays s
       LEFT JOIN iam_v2.pms_interfaces i ON i.id = s.pms_interface_id
       LEFT JOIN iam_v2.entitlements ent
              ON ent.tenant_id = s.tenant_id AND ent.site_id = s.site_id AND ent.stay_id = s.id
             AND ent.status IN ('PENDING','ACTIVE','SUSPENDED')
       LEFT JOIN iam_v2.internet_package_revisions pkgr
              ON pkgr.tenant_id = ent.tenant_id AND pkgr.site_id = ent.site_id
             AND pkgr.id = ent.package_revision_id
       LEFT JOIN iam_v2.internet_packages pkg
              ON pkg.tenant_id = pkgr.tenant_id AND pkg.site_id = pkgr.site_id AND pkg.id = pkgr.package_id
       LEFT JOIN iam_v2.service_plan_revisions planr
              ON planr.tenant_id = ent.tenant_id AND planr.site_id = ent.site_id
             AND planr.id = ent.service_plan_revision_id
       LEFT JOIN iam_v2.service_plans plan
              ON plan.tenant_id = planr.tenant_id AND plan.site_id = planr.site_id
             AND plan.id = planr.service_plan_id`

func scanStay(row interface{ Scan(...any) error }, e *stayRow) error {
	return row.Scan(&e.ID, &e.Interface, &e.InterfaceLabel, &e.Reservation, &e.Room, &e.Status,
		&e.LifecycleVersion, &e.Arrival, &e.Departure, &e.EffectiveCheckoutAt, &e.PostingAllowed,
		&e.Occupants, &e.PrimaryGuest, &e.PostingBlockReason, &e.PostingSource, &e.OccupancyEvidenceAt,
		&e.VIP, &e.RoomType, &e.RatePlan, &e.TravelAgent,
		&e.AccessStatus, &e.AccessPackageCode, &e.AccessPackageName,
		&e.AccessPlanCode, &e.AccessDownKbps, &e.AccessUpKbps, &e.AccessMaxDevices,
		&e.AccessActiveDevices, &e.AccessAuthorizedDevices)
}

// May this caller see guest names and reservation numbers? IT manager and desk roles; site_admin always; site viewers not.
func canSeeStayIdentity(r *http.Request) bool {
	sess := sessFrom(r.Context())
	return sess != nil && permFor(sess.Roles, "pms-stay-identity", permRead)
}

func hideStayIdentity(e *stayRow) {
	e.Reservation = ""
	e.PrimaryGuest = nil
	e.IdentityHidden = true
}

func (s *server) pmsStaysRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listStays)
	r.Get("/travel-agents", s.listTravelAgents)
	r.Get("/{id}", s.getStay)
	return r
}

// Stays are paged and searched on the server; totals count every matching stay. The search text travels in a
// header (X-Stay-Search), not the URL, because chi's request logger logs every request line and a name in the
// query string would land in the log. The surface is read-only, so a POST body is not an option.
const (
	staysPageSizeDefault = 50
	staysPageSizeMax     = 200
	staySearchHeader     = "X-Stay-Search"
	staySearchMaxLen     = 100
)

// One PMS connection's share of the stays the current filters match (every connection is listed, with zero when it matches none).
type staysByInterface struct {
	ID        string `json:"pms_interface_id"`
	Label     string `json:"label"`
	Lifecycle string `json:"lifecycle_state"`
	Total     int    `json:"total"`
	InHouse   int    `json:"in_house"`
}

type staysSummary struct {
	Total         int `json:"total"`
	WithInternet  int `json:"with_internet"`
	DevicesOnline int `json:"devices_online"`
	Arriving      int `json:"arriving"`
	VIP           int `json:"vip"`
}

type staysPage struct {
	Data     []stayRow    `json:"data"`
	Meta     listMeta     `json:"meta"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
	Summary  staysSummary `json:"summary"`
	// The same filters without the connection filter, so the connection tabs can show what each would hold.
	ByInterface []staysByInterface `json:"by_interface"`
}

// The WHERE clause the page and its totals share. $1 tenant, $2 status, $3 search pattern, $4 VIP only, $5 travel agent (exact), $6 PMS connection.
const stayFilter = `WHERE s.tenant_id=$1 AND ($2::text IS NULL OR s.status=$2)
		  AND ($4::bool IS NOT TRUE OR s.vip IS TRUE)
		  AND ($5::text IS NULL OR s.travel_agent = $5)
		  AND ($6::uuid IS NULL OR s.pms_interface_id = $6::uuid)
		  AND ($3::text IS NULL
		       OR s.normalized_room_number ILIKE $3
		       OR s.external_reservation_id ILIKE $3
		       OR s.travel_agent ILIKE $3
		       OR pkgr.display->>'name' ILIKE $3
		       OR pkg.code ILIKE $3
		       OR EXISTS (SELECT 1 FROM iam_v2.stay_guests g
		                   WHERE g.stay_id = s.id
		                     AND (g.display_name ILIKE $3 OR g.last_name_norm ILIKE $3)))`

// For a caller who may not see names or reservations: searching by them would reveal whether a guest is staying here, so search covers only what they can see.
const stayFilterNoIdentity = `WHERE s.tenant_id=$1 AND ($2::text IS NULL OR s.status=$2)
		  AND ($4::bool IS NOT TRUE OR s.vip IS TRUE)
		  AND ($5::text IS NULL OR s.travel_agent = $5)
		  AND ($6::uuid IS NULL OR s.pms_interface_id = $6::uuid)
		  AND ($3::text IS NULL
		       OR s.normalized_room_number ILIKE $3
		       OR s.travel_agent ILIKE $3
		       OR pkgr.display->>'name' ILIKE $3
		       OR pkg.code ILIKE $3)`

func (s *server) listStays(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	var statusArg any
	if v := qv.Get("status"); v != "" {
		switch v {
		case "RESERVED", "IN_HOUSE", "CHECKED_OUT", "POST_STAY_ACTIVE", "CANCELLED", "NO_SHOW":
			statusArg = v
		default:
			jsonErr(w, http.StatusBadRequest, "bad_request", "unknown stay status")
			return
		}
	}
	page, size := 1, staysPageSizeDefault
	if v := qv.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > pageMax {
			jsonErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("page must be a whole number from 1 to %d", pageMax))
			return
		}
		page = n
	}
	if v := qv.Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > staysPageSizeMax {
			jsonErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("page_size must be between 1 and %d", staysPageSizeMax))
			return
		}
		size = n
	}
	var searchArg any
	if v := headerSearchText(r, staySearchHeader); v != "" {
		if len([]rune(v)) > staySearchMaxLen {
			jsonErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("search is limited to %d characters", staySearchMaxLen))
			return
		}
		searchArg = "%" + likeEscape(v) + "%"
	}
	var vipArg any
	if qv.Get("vip") == "true" {
		vipArg = true
	}
	var agentArg any
	if v := strings.TrimSpace(qv.Get("travel_agent")); v != "" {
		agentArg = v
	}
	var ifaceArg any
	if v := strings.TrimSpace(qv.Get("pms_interface_id")); v != "" {
		if !uuidRe.MatchString(v) {
			jsonErr(w, http.StatusBadRequest, "bad_request", "pms_interface_id must be a connection id")
			return
		}
		ifaceArg = v
	}
	args := []any{s.tenantID, statusArg, searchArg, vipArg, agentArg, ifaceArg}
	identity := canSeeStayIdentity(r)
	filter := stayFilter
	if !identity {
		filter = stayFilterNoIdentity
	}

	ctx, cancel := dbCtx(r)
	defer cancel()

	var sum staysSummary
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int,
		       count(*) FILTER (WHERE ent.id IS NOT NULL)::int,
		       COALESCE(sum((SELECT count(DISTINCT d.mac) FROM iam_v2.sessions d
		                       WHERE d.entitlement_id = ent.id AND d.state = 'active')), 0)::int,
		       count(*) FILTER (WHERE s.status = 'RESERVED')::int,
		       count(*) FILTER (WHERE s.vip IS TRUE)::int
		`+stayFrom+`
		`+filter, args...).Scan(&sum.Total, &sum.WithInternet, &sum.DevicesOnline, &sum.Arriving, &sum.VIP); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	// Ordered by arrival because iam_v2.stays has no updated_at; room number breaks ties so a page boundary is stable.
	rows, err := s.db.Query(ctx, `SELECT `+stayCols+` `+stayFrom+`
		`+filter+`
		ORDER BY s.arrival DESC NULLS LAST, COALESCE(i.display_label,''), s.normalized_room_number, s.id
		LIMIT $7 OFFSET $8`, append(args, size+1, (page-1)*size)...)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []stayRow{}
	for rows.Next() {
		var e stayRow
		if err := scanStay(rows, &e); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		if !identity {
			hideStayIdentity(&e)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	more := len(out) > size
	if more {
		out = out[:size]
	}
	by, err := s.staysByInterface(ctx, filter, args)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, staysPage{Data: out, Meta: listMeta{HasMore: more}, Page: page, PageSize: size, Summary: sum, ByInterface: by})
}

// staysByInterface counts the stays the current filters match under each PMS connection, ignoring the connection filter itself, so the tabs show what choosing each would hold.
// Every configured connection that is not retired is listed, with zero when it matches none (one that was never configured has nothing to show); a retired one appears only while it holds matching stays.
func (s *server) staysByInterface(ctx context.Context, filter string, args []any) ([]staysByInterface, error) {
	open := append([]any{}, args...)
	open[5] = nil
	rows, err := s.db.Query(ctx, `SELECT s.pms_interface_id::text, COALESCE(i.display_label,''), COALESCE(i.lifecycle_state,''),
		       count(*)::int, count(*) FILTER (WHERE s.status='IN_HOUSE')::int
		`+stayFrom+`
		`+filter+`
		GROUP BY s.pms_interface_id, i.display_label, i.lifecycle_state`, open...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	out := []staysByInterface{}
	for rows.Next() {
		var e staysByInterface
		if err := rows.Scan(&e.ID, &e.Label, &e.Lifecycle, &e.Total, &e.InHouse); err != nil {
			return nil, err
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	live, err := s.db.Query(ctx, `SELECT id::text, COALESCE(display_label,''), lifecycle_state FROM iam_v2.pms_interfaces
		WHERE tenant_id=$1 AND site_id=$2 AND lifecycle_state <> 'DECOMMISSIONED'
		  AND (current_revision_id IS NOT NULL OR lifecycle_state <> 'AUTH_DISABLED')`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	defer live.Close()
	for live.Next() {
		var e staysByInterface
		if err := live.Scan(&e.ID, &e.Label, &e.Lifecycle); err != nil {
			return nil, err
		}
		if !seen[e.ID] {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Label), strings.ToLower(out[j].Label)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, live.Err()
}

// Every travel agent the PMS has named on a stay, with how many stays are in house under it. Company names, not guest data.
func (s *server) listTravelAgents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT s.travel_agent,
		       count(*) FILTER (WHERE s.status = 'IN_HOUSE')::int,
		       count(*)::int
		  FROM iam_v2.stays s
		 WHERE s.tenant_id = $1 AND NULLIF(btrim(s.travel_agent), '') IS NOT NULL
		 GROUP BY s.travel_agent
		 ORDER BY lower(s.travel_agent)
		 LIMIT 2000`, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	type agent struct {
		Name    string `json:"name"`
		InHouse int    `json:"in_house"`
		Stays   int    `json:"stays"`
	}
	out := []agent{}
	for rows.Next() {
		var a agent
		if err := rows.Scan(&a.Name, &a.InHouse, &a.Stays); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, a)
	}
	writeList(w, out)
}

type stayOccupant struct {
	Display   *string `json:"display_name,omitempty"`
	IsPrimary bool    `json:"is_primary"`
}

// One reasoned stop on room charging for the stay, active or cleared. There are no folios: a room charge targets the reservation.
type stayPostingBlock struct {
	Reason          string     `json:"reason"`
	Source          string     `json:"source"`
	PAStatus        *string    `json:"pa_as_status,omitempty"`
	Note            *string    `json:"note,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	CreatedBy       *string    `json:"created_by,omitempty"` // operator e-mail, ADMIN_BLOCK only
	ClearedAt       *time.Time `json:"cleared_at,omitempty"`
	ClearedBySource *string    `json:"cleared_by_source,omitempty"`
	ClearedBy       *string    `json:"cleared_by,omitempty"` // operator e-mail, ADMIN_BLOCK only
	ClearedReason   *string    `json:"cleared_reason,omitempty"`
}

type stayDetail struct {
	stayRow
	Occupants     []stayOccupant     `json:"occupant_list"`
	PostingBlocks []stayPostingBlock `json:"posting_blocks"`
	// A room charge for this stay is pending, being sent or under review; only one may exist at a time.
	RoomChargeOpen bool `json:"room_charge_open"`
}

func (s *server) getStay(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// A malformed id is "no such stay", not a cast error rendered as a 500.
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "stay not found")
		return
	}
	identity := canSeeStayIdentity(r)
	ctx, cancel := dbCtx(r)
	defer cancel()
	var d stayDetail
	err := scanStay(s.db.QueryRow(ctx, `SELECT `+stayCols+` `+stayFrom+`
		WHERE s.id=$1 AND s.tenant_id=$2`, id, s.tenantID), &d.stayRow)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "stay not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	d.Occupants = []stayOccupant{}
	rows, err := s.db.Query(ctx, `SELECT display_name, is_primary FROM iam_v2.stay_guests
		WHERE stay_id=$1 ORDER BY is_primary DESC, display_name NULLS LAST`, id)
	if err == nil {
		for rows.Next() {
			var o stayOccupant
			if rows.Scan(&o.Display, &o.IsPrimary) == nil {
				if !identity {
					o.Display = nil
				}
				d.Occupants = append(d.Occupants, o)
			}
		}
		rows.Close()
	}
	d.PostingBlocks = []stayPostingBlock{}
	brows, err := s.db.Query(ctx, `SELECT b.reason, b.source, b.pa_as_status, b.note, b.created_at, oc.email,
		       b.cleared_at, b.cleared_by_source, ox.email, b.cleared_reason
		  FROM iam_v2.stay_posting_blocks b
		  LEFT JOIN public.operators oc ON oc.id = b.created_by
		  LEFT JOIN public.operators ox ON ox.id = b.cleared_by
		 WHERE b.stay_id=$1 AND b.tenant_id=$2
		 ORDER BY (b.cleared_at IS NULL) DESC, b.created_at DESC LIMIT 20`, id, s.tenantID)
	if err == nil {
		for brows.Next() {
			var b stayPostingBlock
			if brows.Scan(&b.Reason, &b.Source, &b.PAStatus, &b.Note, &b.CreatedAt, &b.CreatedBy,
				&b.ClearedAt, &b.ClearedBySource, &b.ClearedBy, &b.ClearedReason) == nil {
				d.PostingBlocks = append(d.PostingBlocks, b)
			}
		}
		brows.Close()
	}
	_ = s.db.QueryRow(ctx, `SELECT iam_v2.p4_stay_room_charge_open($1::uuid)`, id).Scan(&d.RoomChargeOpen)
	if !identity {
		hideStayIdentity(&d.stayRow)
	}
	writeJSON(w, http.StatusOK, d)
}

type stayEventRow struct {
	ID        string `json:"id"`
	Interface string `json:"pms_interface_id"`
	// The PMS's own identifier for this message; kept as the handle support and the PMS vendor share.
	Identity   string     `json:"external_event_identity"`
	Type       string     `json:"event_type"`
	Status     string     `json:"processing_status"`
	ReviewCode *string    `json:"review_code,omitempty"`
	StayID     *string    `json:"stay_id,omitempty"`
	PMSAt      *time.Time `json:"pms_timestamp_utc,omitempty"`
	ReceivedAt time.Time  `json:"received_at"`

	// What the event is about, when matched to a stay (room, reservation, primary guest via LEFT JOIN). Absent, not
	// blank, on an unmatched event: that is why a PENDING or MANUAL_REVIEW row is waiting.
	InterfaceLabel string  `json:"pms_interface_label,omitempty"`
	Room           *string `json:"room,omitempty"`
	Reservation    *string `json:"external_reservation_id,omitempty"`
	PrimaryGuest   *string `json:"primary_guest,omitempty"`
	StayStatus     *string `json:"stay_status,omitempty"`

	// A review message that carries a decision (acknowledged, linked, expired, or answered by the roster) no longer needs one.
	Decided            bool    `json:"decided"`
	Decision           *string `json:"decision,omitempty"`
	MessageReservation *string `json:"message_reservation,omitempty"`
	MessageRoom        *string `json:"message_room,omitempty"`
	IdentityHidden     bool    `json:"identity_hidden,omitempty"`
}

func (s *server) pmsEventsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listStayEvents)
	return r
}

// PMS activity is paged and searched on the server. The search text travels in X-Pms-Event-Search, never the URL, because edged logs request lines.
const pmsEventSearchHeader = "X-Pms-Event-Search"

// Both joins are LEFT: an event not matched to a stay is the one an operator most needs to see.
const stayEventFrom = `FROM iam_v2.stay_events ev
		LEFT JOIN iam_v2.pms_interfaces i ON i.id = ev.pms_interface_id
		LEFT JOIN iam_v2.stays st
		       ON st.tenant_id = ev.tenant_id AND st.site_id = ev.site_id AND st.id = ev.stay_id`

// The status and search filter. $2 processing status (NULL for every one), $3 the ILIKE pattern (NULL for no search).
const stayEventMatch = `($2::text IS NULL OR ev.processing_status = $2)
		  AND ($4::bool IS NOT TRUE OR (ev.processing_status = 'MANUAL_REVIEW'
		       AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions x WHERE x.stay_event_id = ev.id)))
		  AND ($3::text IS NULL
		       OR st.normalized_room_number ILIKE $3
		       OR upper(btrim(COALESCE(ev.payload->>'room',''))) ILIKE $3
		       OR ev.event_type ILIKE $3
		       OR ($5::bool AND (st.external_reservation_id ILIKE $3
		           OR ev.external_event_identity ILIKE $3
		           OR btrim(COALESCE(ev.payload->>'reservation','')) ILIKE $3
		           OR EXISTS (SELECT 1 FROM iam_v2.stay_guests g
		                       WHERE g.stay_id = st.id
		                         AND (g.display_name ILIKE $3 OR g.last_name_norm ILIKE $3)))))`

// Counts every message the appliance holds whatever the filter, so the tiles answer "is the feed alive and is anything waiting".
type stayEventsSummary struct {
	Applied int `json:"applied"`
	Pending int `json:"pending"`
	// Review counts every message ever placed in review; Undecided only those with no disposition (the "needs a decision" tile).
	Review    int        `json:"manual_review"`
	Undecided int        `json:"undecided"`
	Rejected  int        `json:"rejected"`
	Unmatched int        `json:"unmatched"`
	Newest    *time.Time `json:"newest_received_at,omitempty"`
}

type stayEventsPage struct {
	pagedList[stayEventRow]
	Summary stayEventsSummary `json:"summary"`
}

func (s *server) listStayEvents(w http.ResponseWriter, r *http.Request) {
	var statusArg any
	if v := r.URL.Query().Get("processing_status"); v != "" {
		switch v {
		case "PENDING", "APPLIED", "SKIPPED_DUPLICATE", "MANUAL_REVIEW", "REJECTED":
			statusArg = v
		default:
			jsonErr(w, http.StatusBadRequest, "bad_request", "unknown processing status")
			return
		}
	}
	pg, ok := readPage(w, r, 0)
	if !ok {
		return
	}
	search, ok := readSearch(w, r, pmsEventSearchHeader)
	if !ok {
		return
	}
	identity := canSeeStayIdentity(r)
	args := []any{s.tenantID, statusArg, likePattern(search), r.URL.Query().Get("undecided") == "true", identity}
	ctx, cancel := dbCtx(r)
	defer cancel()

	var sum stayEventsSummary
	var total int
	if err := s.db.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE ev.processing_status = 'APPLIED')::int,
			count(*) FILTER (WHERE ev.processing_status = 'PENDING')::int,
			count(*) FILTER (WHERE ev.processing_status = 'MANUAL_REVIEW')::int,
			count(*) FILTER (WHERE ev.processing_status = 'MANUAL_REVIEW'
			                   AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions x
			                                    WHERE x.stay_event_id = ev.id))::int,
			count(*) FILTER (WHERE ev.processing_status = 'REJECTED')::int,
			count(*) FILTER (WHERE ev.stay_id IS NULL)::int,
			max(ev.received_at),
			count(*) FILTER (WHERE `+stayEventMatch+`)::int
		`+stayEventFrom+`
		WHERE ev.tenant_id=$1`, args...).Scan(&sum.Applied, &sum.Pending, &sum.Review, &sum.Undecided, &sum.Rejected,
		&sum.Unmatched, &sum.Newest, &total); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	rows, err := s.db.Query(ctx, `SELECT ev.id::text, ev.pms_interface_id::text, ev.external_event_identity,
			ev.event_type, ev.processing_status, ev.review_code, ev.stay_id::text,
			ev.pms_timestamp_utc, ev.received_at,
			COALESCE(i.display_label,''),
			st.normalized_room_number, st.external_reservation_id,
			(SELECT COALESCE(NULLIF(g.display_name,''), NULLIF(g.last_name_norm,''))
			   FROM iam_v2.stay_guests g WHERE g.stay_id = st.id
			  ORDER BY g.is_primary DESC LIMIT 1),
			st.status,
			res.disposition,
			NULLIF(btrim(COALESCE(ev.payload->>'reservation','')),''),
			NULLIF(upper(btrim(COALESCE(ev.payload->>'room',''))),'')
		`+stayEventFrom+`
		LEFT JOIN iam_v2.pms_case_resolutions res ON res.stay_event_id = ev.id
		WHERE ev.tenant_id=$1 AND `+stayEventMatch+`
		ORDER BY ev.received_at DESC, ev.id
		LIMIT $6 OFFSET $7`, append(args, pg.Fetch(), pg.Offset())...)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []stayEventRow{}
	for rows.Next() {
		var e stayEventRow
		if err := rows.Scan(&e.ID, &e.Interface, &e.Identity, &e.Type, &e.Status, &e.ReviewCode,
			&e.StayID, &e.PMSAt, &e.ReceivedAt,
			&e.InterfaceLabel, &e.Room, &e.Reservation, &e.PrimaryGuest, &e.StayStatus,
			&e.Decision, &e.MessageReservation, &e.MessageRoom); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		e.Decided = e.Decision != nil
		if !identity {
			// The PMS's own event identity can embed the reservation number, so it goes too.
			e.Identity, e.Reservation, e.PrimaryGuest, e.MessageReservation = "", nil, nil, nil
			e.IdentityHidden = true
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	out, more := trimPage(out, pg)
	writeJSON(w, http.StatusOK, stayEventsPage{pagedList: newPagedList(out, more, pg, &total), Summary: sum})
}

type resolutionRow struct {
	ID           string `json:"id"`
	GuestNetwork string `json:"guest_network_id"`
	// The network's name is what an operator can act on (a bare uuid hides "all failures are on one network"); the
	// uuid is still sent as the handle for support and cross-referencing.
	GuestNetworkName *string   `json:"guest_network_name,omitempty"`
	Outcome          string    `json:"outcome_code"`
	Resolved         bool      `json:"resolved"`
	ResolvedAt       time.Time `json:"resolved_at"`
}

func (s *server) pmsResolutionsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listResolutions)
	return r
}

// How many of the most recent checks the summary describes; a year of history would bury a fault that started this morning.
const resolutionSummaryWindow = 200

type resolutionOutcomeCount struct {
	Outcome string `json:"outcome_code"`
	Count   int    `json:"count"`
}

type resolutionNetworkCount struct {
	GuestNetwork     string  `json:"guest_network_id"`
	GuestNetworkName *string `json:"guest_network_name,omitempty"`
	Total            int     `json:"total"`
	Verified         int     `json:"verified"`
}

type resolutionsSummary struct {
	Window   int                      `json:"window"`
	Total    int                      `json:"total"`
	Verified int                      `json:"verified"`
	Newest   *time.Time               `json:"newest_resolved_at,omitempty"`
	Outcomes []resolutionOutcomeCount `json:"outcomes"`
	Networks []resolutionNetworkCount `json:"networks"`
}

type resolutionsPage struct {
	pagedList[resolutionRow]
	Summary resolutionsSummary `json:"summary"`
}

func (s *server) listResolutions(w http.ResponseWriter, r *http.Request) {
	pg, ok := readPage(w, r, 0)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// Deliberately no stay identity, guest name, room or reservation: a failed resolution must not become a way to
	// enumerate who is staying. The guest network's name is infrastructure, not guest data, so it is included.
	rows, err := s.db.Query(ctx, `SELECT ar.id::text, ar.guest_network_id::text, gn.name, ar.outcome_code,
			(ar.resolved_stay_id IS NOT NULL), ar.resolved_at
		FROM iam_v2.auth_resolutions ar
		LEFT JOIN public.guest_networks gn
		       ON gn.tenant_id = ar.tenant_id AND gn.site_id = ar.site_id AND gn.id = ar.guest_network_id
		WHERE ar.tenant_id=$1
		ORDER BY ar.resolved_at DESC, ar.id
		LIMIT $2 OFFSET $3`, s.tenantID, pg.Fetch(), pg.Offset())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []resolutionRow{}
	for rows.Next() {
		var e resolutionRow
		if err := rows.Scan(&e.ID, &e.GuestNetwork, &e.GuestNetworkName, &e.Outcome, &e.Resolved, &e.ResolvedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	rows.Close()
	out, more := trimPage(out, pg)

	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.auth_resolutions WHERE tenant_id=$1`,
		s.tenantID).Scan(&total); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	// One grouped read (GROUPING SETS) gives both breakdowns in a single pass over the same window.
	sum := resolutionsSummary{Window: resolutionSummaryWindow, Outcomes: []resolutionOutcomeCount{}, Networks: []resolutionNetworkCount{}}
	srows, err := s.db.Query(ctx, `WITH recent AS (
			SELECT ar.guest_network_id::text AS gnid, gn.name AS gname, ar.outcome_code,
			       (ar.resolved_stay_id IS NOT NULL) AS verified, ar.resolved_at
			  FROM iam_v2.auth_resolutions ar
			  LEFT JOIN public.guest_networks gn
			         ON gn.tenant_id = ar.tenant_id AND gn.site_id = ar.site_id AND gn.id = ar.guest_network_id
			 WHERE ar.tenant_id=$1
			 ORDER BY ar.resolved_at DESC, ar.id
			 LIMIT $2)
		SELECT GROUPING(outcome_code)::int, GROUPING(gnid)::int, COALESCE(outcome_code,''), COALESCE(gnid,''),
		       max(gname), count(*)::int, count(*) FILTER (WHERE verified)::int, max(resolved_at)
		  FROM recent
		 GROUP BY GROUPING SETS ((outcome_code), (gnid), ())
		 ORDER BY count(*) DESC, 3, 4`, s.tenantID, resolutionSummaryWindow)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer srows.Close()
	for srows.Next() {
		var byOutcome, byNetwork, n, verified int
		var outcome, gnid string
		var gname *string
		var newest *time.Time
		if err := srows.Scan(&byOutcome, &byNetwork, &outcome, &gnid, &gname, &n, &verified, &newest); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		// GROUPING() is 0 for the column a row is grouped by: an outcome row, a network row, or the total.
		switch {
		case byOutcome == 0:
			sum.Outcomes = append(sum.Outcomes, resolutionOutcomeCount{Outcome: outcome, Count: n})
		case byNetwork == 0:
			sum.Networks = append(sum.Networks, resolutionNetworkCount{GuestNetwork: gnid, GuestNetworkName: gname, Total: n, Verified: verified})
		default:
			sum.Total, sum.Verified, sum.Newest = n, verified, newest
		}
	}
	if err := srows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, resolutionsPage{pagedList: newPagedList(out, more, pg, &total), Summary: sum})
}

type checkoutGraceConfig struct {
	PackageRevisionID    *string `json:"grace_package_revision_id,omitempty"`
	DurationSeconds      int     `json:"grace_duration_seconds"`
	DownKbps             int     `json:"grace_down_kbps"`
	UpKbps               int     `json:"grace_up_kbps"`
	DataQuotaBytes       int64   `json:"grace_data_quota_bytes"`
	DeviceLimit          int     `json:"grace_device_limit"`
	DeviceLimitPolicy    string  `json:"grace_device_limit_policy"`
	EligibilityWindowSec int     `json:"eligibility_window_seconds"`
	ConfigVersion        int     `json:"config_version"`
}

// Publishing requires the version the operator was looking at, a password step-up and a bounded reason, because it
// changes what every departing guest gets.
type checkoutGracePublishReq struct {
	checkoutGraceConfig
	ExpectedConfigVersion *int   `json:"expected_config_version"`
	Password              string `json:"password"`
	ReasonCode            string `json:"reason_code"`
}

// What the enforcement path can honour. DISCONNECT_OLDEST and ADMIN_APPROVAL stay capability-disabled: offering them would publish a policy that silently degrades.
var supportedDeviceLimitPolicies = []string{"REJECT_NEW_DEVICE"}

// Stored for eligibility_window_seconds when the console sends none; the converter does not read it.
const graceEligibilityWindowUnused = 86400

func (s *server) checkoutGraceConfigRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getCheckoutGraceConfig)
	r.Get("/packages", s.listGracePackages)
	r.Get("/history", s.listGraceHistory)
	r.Put("/", s.putCheckoutGraceConfig)
	return r
}

// One selectable Checkout-Grace Package Revision, described by its own immutable attributes, so the published policy and the package agree by construction.
type gracePackageOption struct {
	PackageRevisionID string `json:"package_revision_id"`
	PackageCode       string `json:"package_code"`
	RevisionNo        int    `json:"revision_no"`
	PlanRevisionID    string `json:"service_plan_revision_id"`
	PlanCode          string `json:"service_plan_code"`
	PlanRevisionNo    int    `json:"service_plan_revision_no"`
	DownKbps          int    `json:"down_kbps"`
	UpKbps            int    `json:"up_kbps"`
	DataQuotaBytes    int64  `json:"data_quota_bytes"`
	DeviceLimit       int    `json:"device_limit"`
	DeviceLimitPolicy string `json:"device_limit_policy"`
	TimeAccounting    string `json:"time_accounting_mode"`
	DurationSeconds   int    `json:"grace_duration_seconds"`
	EndMode           string `json:"end_mode"`
	PolicyVersion     string `json:"policy_version"`
	SettlementMode    string `json:"settlement_mode"`
	IsCurrent         bool   `json:"is_current"`
	IsActive          bool   `json:"is_active"`
	Selected          bool   `json:"selected"`
}

// Returns exactly the revisions publication would accept: iam_v2.selectable_grace_packages applies the same
// validator. Failures are logged with a bounded reason and omitted; a malformed duration policy excludes only that candidate.
func (s *server) listGracePackages(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var selected *string
	_ = s.db.QueryRow(ctx, `SELECT grace_package_revision_id::text FROM iam_v2.site_checkout_grace_config
		WHERE tenant_id=$1 AND site_id=$2`, s.tenantID, s.siteID).Scan(&selected)

	rows, err := s.db.Query(ctx, `SELECT package_revision_id::text, package_code, revision_no,
			service_plan_revision_id::text, service_plan_code, service_plan_revision_no,
			down_kbps, up_kbps, data_quota_bytes, device_limit, device_limit_policy, time_accounting_mode,
			COALESCE(grace_duration_seconds, 0), COALESCE(end_mode, ''), COALESCE(policy_version, ''),
			settlement_mode, is_current, is_active, mismatch_reason
		FROM iam_v2.selectable_grace_packages($1,$2)`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []gracePackageOption{}
	excluded := map[string]string{}
	for rows.Next() {
		var o gracePackageOption
		var mismatch *string
		if err := rows.Scan(&o.PackageRevisionID, &o.PackageCode, &o.RevisionNo,
			&o.PlanRevisionID, &o.PlanCode, &o.PlanRevisionNo,
			&o.DownKbps, &o.UpKbps, &o.DataQuotaBytes, &o.DeviceLimit, &o.DeviceLimitPolicy, &o.TimeAccounting,
			&o.DurationSeconds, &o.EndMode, &o.PolicyVersion,
			&o.SettlementMode, &o.IsCurrent, &o.IsActive, &mismatch); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		if mismatch != nil {
			excluded[o.PackageCode] = *mismatch
			continue
		}
		o.Selected = selected != nil && *selected == o.PackageRevisionID
		out = append(out, o)
	}
	if len(excluded) > 0 {
		slog.Info("checkout-grace: packages excluded from selection", "site_id", s.siteID, "reasons", excluded)
	}
	writeList(w, out)
}

func (s *server) getCheckoutGraceConfig(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var c checkoutGraceConfig
	err := s.db.QueryRow(ctx, `SELECT grace_package_revision_id::text, grace_duration_seconds, grace_down_kbps,
			grace_up_kbps, grace_data_quota_bytes, grace_device_limit, grace_device_limit_policy,
			eligibility_window_seconds, config_version
		FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`, s.tenantID, s.siteID).
		Scan(&c.PackageRevisionID, &c.DurationSeconds, &c.DownKbps, &c.UpKbps, &c.DataQuotaBytes,
			&c.DeviceLimit, &c.DeviceLimitPolicy, &c.EligibilityWindowSec, &c.ConfigVersion)
	if isNoRows(err) {
		// Nothing published is version 0, not a failure. The effective policy is always reported, because guests of
		// an unconfigured site still get the built-in Emergency fallback and the operator must be able to see that.
		writeJSON(w, http.StatusOK, map[string]any{
			"published":                 false,
			"config_version":            0,
			"supported_device_policies": supportedDeviceLimitPolicies,
			"effective":                 emergencyEffective(),
			"emergency_history":         s.emergencyGraceHistory(ctx),
		})
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"published":                 true,
		"config_version":            c.ConfigVersion,
		"supported_device_policies": supportedDeviceLimitPolicies,
		"policy":                    c,
		"effective": map[string]any{
			"source":                     "PUBLISHED",
			"duration_seconds":           c.DurationSeconds,
			"down_kbps":                  c.DownKbps,
			"up_kbps":                    c.UpKbps,
			"data_quota_bytes":           c.DataQuotaBytes,
			"device_limit":               c.DeviceLimit,
			"device_limit_policy":        c.DeviceLimitPolicy,
			"eligibility_window_seconds": c.EligibilityWindowSec,
			"config_version":             c.ConfigVersion,
		},
		// Reported even when a policy is published: earlier conversions may exist, and hiding them would make the alert history unexplainable.
		"emergency_history": s.emergencyGraceHistory(ctx),
	})
}

// What a departing guest actually receives at a site with no published policy. Numbers come from
// grace.BuiltinEmergencyPolicy(), not a second copy, which would drift.
func emergencyEffective() map[string]any {
	p := grace.BuiltinEmergencyPolicy()
	return map[string]any{
		"source":              "EMERGENCY_FALLBACK",
		"duration_seconds":    p.DurationSeconds,
		"down_kbps":           p.DownKbps,
		"up_kbps":             p.UpKbps,
		"data_quota_bytes":    p.DataQuotaBytes,
		"device_limit":        p.DeviceLimit,
		"device_limit_policy": p.DeviceLimitPolicy,
		"policy_version":      "EMERGENCY_GRACE_V1",
	}
}

// Counts conversions that already fell back. The audit table is not present in every schema, so a read error
// yields a zero count, never an error page.
func (s *server) emergencyGraceHistory(ctx context.Context) map[string]any {
	out := map[string]any{"count": 0}
	var n int
	var last *time.Time
	if err := s.db.QueryRow(ctx, `
		SELECT count(*), max(created_at)
		  FROM iam_v2.checkout_grace_audit
		 WHERE tenant_id=$1 AND site_id=$2 AND trigger='EMERGENCY_GRACE'`,
		s.tenantID, s.siteID).Scan(&n, &last); err != nil {
		return out
	}
	out["count"] = n
	if last != nil {
		out["last_at"] = last
	}
	return out
}

func (s *server) putCheckoutGraceConfig(w http.ResponseWriter, r *http.Request) {
	var in checkoutGracePublishReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if in.ExpectedConfigVersion == nil {
		jsonErr(w, http.StatusBadRequest, "expected_version_required",
			"expected_config_version is required so a concurrent publication cannot be silently overwritten")
		return
	}
	if strings.TrimSpace(in.ReasonCode) == "" {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason code is required to publish")
		return
	}
	// The package is derived, not chosen: the operator publishes policy, and the plan and package revisions that
	// express it exactly are materialised in the same transaction. The checkout validator demands exact equality
	// between policy and pinned revision, and the grace package is a reserved system package the operator publisher refuses to create.
	if in.PackageRevisionID != nil && strings.TrimSpace(*in.PackageRevisionID) != "" {
		jsonErr(w, http.StatusBadRequest, "package_not_accepted",
			"grace_package_revision_id is not accepted: publish the policy and the system derives the package "+
				"that matches it exactly, because a hand-picked package can only accidentally satisfy the "+
				"checkout validator")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "an operator identity is required")
		return
	}
	// The eligibility window is not an operator choice: the checkout converter never reads it (eligibility is proven
	// from the entitlement's state at the boundary), so an omitted value publishes the schema default.
	if in.EligibilityWindowSec == 0 {
		in.EligibilityWindowSec = graceEligibilityWindowUnused
	}
	if in.DurationSeconds <= 0 || in.DownKbps <= 0 || in.UpKbps <= 0 || in.DeviceLimit <= 0 || in.EligibilityWindowSec <= 0 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "duration, rates, device limit and eligibility window must be positive")
		return
	}
	supported := false
	for _, p := range supportedDeviceLimitPolicies {
		if in.DeviceLimitPolicy == p {
			supported = true
		}
	}
	if !supported {
		jsonErr(w, http.StatusBadRequest, "policy_unsupported",
			"only REJECT_NEW_DEVICE is implemented; the others are capability-disabled")
		return
	}

	if !graceReasonCode.MatchString(in.ReasonCode) {
		jsonErr(w, http.StatusBadRequest, "reason_invalid",
			"reason_code must be a bounded machine code matching ^[A-Z][A-Z0-9_]{0,63}$, not free text")
		return
	}
	repo, ok := s.commerceRepo.(iamv2.GracePublishRepository)
	if !ok || repo == nil {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled",
			"the commercial catalog is not available, so the grace package cannot be derived")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	// One transaction end to end, so a refusal leaves no half-built grace catalog.
	version, pkgRev, err := iamv2.PublishSystemGracePolicy(ctx, repo, iamv2.GracePublishRequest{
		TenantID: s.tenantID, SiteID: s.siteID,
		Policy: iamv2.SystemGracePolicy{
			DurationSeconds:          in.DurationSeconds,
			DownKbps:                 in.DownKbps,
			UpKbps:                   in.UpKbps,
			DataQuotaBytes:           in.DataQuotaBytes,
			DeviceLimit:              in.DeviceLimit,
			DeviceLimitPolicy:        in.DeviceLimitPolicy,
			EligibilityWindowSeconds: in.EligibilityWindowSec,
		},
		ActorOperatorID: sess.OperatorID, ReasonCode: in.ReasonCode,
		ExpectedVersion: *in.ExpectedConfigVersion,
	})
	if err != nil {
		// A bad policy is the operator's to fix; a stale version is a 409 so the UI reloads instead of overwriting a publication.
		var de *iamv2.Error
		if errors.As(err, &de) && de.Code == iamv2.ErrInvalidInput {
			jsonErr(w, http.StatusBadRequest, "validation", de.Msg)
			return
		}
		status, code := graceFailureStatus(err.Error())
		jsonErr(w, status, code, "the checkout-grace policy was refused")
		return
	}
	// The derived revision is recorded as provenance of what guests will receive.
	s.audit(r, "checkout_grace.published", "site_checkout_grace_config", s.siteID,
		map[string]any{"config_version": version, "reason_code": in.ReasonCode,
			"derived_package_revision_id": pkgRev})
	writeJSON(w, http.StatusOK, map[string]any{"config_version": version})
}

// One published version as provenance: who, when, why, and the exact terms it put in force.
type graceHistoryEntry struct {
	ConfigVersion     int            `json:"config_version"`
	PublishedAt       time.Time      `json:"published_at"`
	ActorDisplay      string         `json:"actor"`
	ReasonCode        string         `json:"reason_code"`
	Policy            map[string]any `json:"policy"`
	DerivedRevisionID string         `json:"derived_package_revision_id,omitempty"`
}

// Newest first. The snapshot is read from the row, not recomputed: a history re-derived from today's config describes nothing.
func (s *server) listGraceHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	// Fail soft, and say so. The ledger is written by a SECURITY DEFINER function and svc_edged holds no SELECT on
	// it, so availability is reported honestly: "no policy ever published" differs from "I cannot see the record".
	unavailable := func(err error) {
		slog.Error("checkout-grace: publication history unreadable; the page is served without it",
			"site_id", s.siteID, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"data": []graceHistoryEntry{}, "meta": listMeta{}, "available": false,
			"reason": "the service account cannot read the publication ledger on this appliance",
		})
	}
	rows, err := s.db.Query(ctx, `
		SELECT p.config_version, p.published_at, COALESCE(p.reason_code,''),
		       COALESCE(NULLIF(op.display_name,''), op.email, 'operator ' || left(p.actor::text,8)),
		       p.policy_snapshot, COALESCE(p.grace_package_revision_id::text,'')
		  FROM iam_v2.checkout_grace_policy_publications p
		  LEFT JOIN public.operators op ON op.id = p.actor
		 WHERE p.tenant_id=$1 AND p.site_id=$2
		 ORDER BY p.config_version DESC LIMIT 100`, s.tenantID, s.siteID)
	if err != nil {
		unavailable(err)
		return
	}
	defer rows.Close()
	out := []graceHistoryEntry{}
	for rows.Next() {
		var e graceHistoryEntry
		var snap []byte
		if err := rows.Scan(&e.ConfigVersion, &e.PublishedAt, &e.ReasonCode, &e.ActorDisplay,
			&snap, &e.DerivedRevisionID); err != nil {
			unavailable(err)
			return
		}
		_ = json.Unmarshal(snap, &e.Policy)
		out = append(out, e)
	}
	// pgx defers most query errors to here; log them so the table or privilege is named.
	if err := rows.Err(); err != nil {
		unavailable(err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "meta": listMeta{}, "available": true})
}

// A version conflict is a 409 so the UI can reload and show what changed instead of overwriting it.
func graceFailureStatus(msg string) (int, string) {
	switch {
	case strings.Contains(msg, "GRACE_VERSION_CONFLICT"):
		return http.StatusConflict, "version_conflict"
	case strings.Contains(msg, "GRACE_ACTOR_INVALID"):
		return http.StatusForbidden, "actor_invalid"
	case strings.Contains(msg, "GRACE_PACKAGE_INVALID"):
		return http.StatusBadRequest, "package_invalid"
	case strings.Contains(msg, "GRACE_POLICY_UNSUPPORTED"):
		return http.StatusBadRequest, "policy_unsupported"
	default:
		return http.StatusBadRequest, "bad_request"
	}
}

type alertRow struct {
	AuditID          string    `json:"audit_id"`
	StayID           string    `json:"stay_id"`
	LifecycleVersion int       `json:"lifecycle_version"`
	AlertCode        string    `json:"alert_code"`
	Trigger          string    `json:"trigger"`
	ReasonCode       *string   `json:"reason_code,omitempty"`
	BoundaryAt       time.Time `json:"boundary_at"`
	ClockSuspect     bool      `json:"boundary_clock_suspect"`
	CreatedAt        time.Time `json:"created_at"`
	// State + Seq are what an operator sends back on the next action; without them two operators could overwrite each other.
	State          string     `json:"state"`
	Seq            int64      `json:"seq"`
	StateChangedAt *time.Time `json:"state_changed_at,omitempty"`
}

func (s *server) operationalAlertsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listAlerts)
	r.Post("/{auditID}/acknowledge", s.alertAction("ACKNOWLEDGED"))
	r.Post("/{auditID}/resolve", s.alertAction("RESOLVED"))
	return r
}

func (s *server) listAlerts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	// Only alerts whose lifecycle head is not RESOLVED, with that head state so the next action can be optimistic.
	rows, err := s.db.Query(ctx, `SELECT audit_id::text, stay_id::text, lifecycle_version, alert_code, trigger,
			reason_code, boundary_at, boundary_clock_suspect, created_at, alert_state, alert_seq, state_changed_at
		FROM iam_v2.active_operational_alerts WHERE tenant_id=$1 AND site_id=$2
		ORDER BY created_at DESC LIMIT 200`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []alertRow{}
	for rows.Next() {
		var a alertRow
		if err := rows.Scan(&a.AuditID, &a.StayID, &a.LifecycleVersion, &a.AlertCode, &a.Trigger,
			&a.ReasonCode, &a.BoundaryAt, &a.ClockSuspect, &a.CreatedAt,
			&a.State, &a.Seq, &a.StateChangedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, a)
	}
	writeList(w, out)
}

type alertActionReq struct {
	// Required: an action sent without it is taken on an unknown world and the API refuses to guess.
	ExpectedState string `json:"expected_state"`
	ReasonCode    string `json:"reason_code"`
}

// Records ACKNOWLEDGED or RESOLVED through the one controlled database operation, which owns the lock,
// sequence, legal edges, actor check and optimistic state match.
func (s *server) alertAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auditID := chi.URLParam(r, "auditID")
		var in alertActionReq
		if err := decodeJSON(r, &in); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
			return
		}
		switch in.ExpectedState {
		case "OPEN", "ACKNOWLEDGED":
		default:
			jsonErr(w, http.StatusBadRequest, "bad_request", "expected_state must be OPEN or ACKNOWLEDGED")
			return
		}
		sess := sessFrom(r.Context())
		if sess == nil || sess.OperatorID == "" {
			jsonErr(w, http.StatusUnauthorized, "unauthorized", "an operator identity is required")
			return
		}
		var reasonArg any
		if in.ReasonCode != "" {
			reasonArg = in.ReasonCode
		}
		ctx, cancel := dbCtx(r)
		defer cancel()
		var seq int64
		err := s.db.QueryRow(ctx, `SELECT iam_v2.record_alert_action($1,$2,$3::uuid,$4,$5::uuid,$6,$7)`,
			s.tenantID, s.siteID, auditID, action, sess.OperatorID, reasonArg, in.ExpectedState).Scan(&seq)
		if err != nil {
			status, code := alertFailureStatus(err.Error())
			jsonErr(w, status, code, "the alert action was refused")
			return
		}
		s.audit(r, "operational_alert."+strings.ToLower(action), "checkout_grace_audit", auditID,
			map[string]any{"seq": seq, "expected_state": in.ExpectedState})
		writeJSON(w, http.StatusOK, map[string]any{"audit_id": auditID, "action": action, "seq": seq})
	}
}

// The database is the authority on why something was refused; this only maps the prefixes to HTTP.
func alertFailureStatus(msg string) (int, string) {
	switch {
	case strings.Contains(msg, "ALERT_NOT_FOUND"):
		return http.StatusNotFound, "not_found"
	case strings.Contains(msg, "ALERT_STATE_CONFLICT"):
		return http.StatusConflict, "state_conflict"
	case strings.Contains(msg, "ALERT_ACTOR_INVALID"):
		return http.StatusForbidden, "actor_invalid"
	case strings.Contains(msg, "ALERT_ACTION_INVALID"):
		return http.StatusBadRequest, "bad_request"
	default:
		return http.StatusConflict, "conflict"
	}
}
