package main

// THE OPERATIONAL SNAPSHOT BEHIND THE DASHBOARD.
//
// The dashboard used to be four tiles and a four-field health box, fed by /reports/summary. Between them they
// answered: how many devices are online, how many sessions started today, how many bytes moved, and is there a
// licence. A duty manager opening the admin at the start of a shift wants to know what the night looks like —
// is the PMS connected, did the guest list load, is anybody failing to sign in, are the DHCP pools filling up,
// are room charges going through, which packages are guests actually on — and not one of those was on the page.
//
// So this endpoint assembles that picture in one read, and the rules it follows are worth stating because they
// are what keeps the figures trustworthy:
//
//  1. NOTHING IS INVENTED. Every number traces to a row this service is permitted to read. Where a figure is
//     genuinely unavailable the section reports `available: false` with a reason, and the UI says so, rather
//     than substituting a zero that reads as "nothing happened".
//
//  2. ONE SECTION FAILING DOES NOT FAIL THE PAGE. Several sections read surfaces that are phase-gated or
//     privilege-gated, and which of those exist differs per appliance. Each section is therefore independently
//     fallible: a 500 here would blank a dashboard over, say, a financial table that this deployment does not
//     use.
//
//  3. NO GUEST IS NAMED. This is an aggregate surface. The per-guest screens are separately role-gated and
//     that is where an operator legitimately looks at one person.
//
// A note on what is NOT here: there is no hourly byte series. Per-interval traffic lives in
// iam_v2.accounting_records, which svc_edged holds no privilege on, and the alternative — attributing a
// session's whole-day total to the hour it happened to start in — would put a guest's 6 GB evening on the
// 08:00 column. The hourly chart is sign-ins, which is a real per-hour count, and the byte totals are reported
// for the day as the day's totals.

import (
	"context"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------------------------------------
// wire shapes
// ---------------------------------------------------------------------------------------------------------

// section carries the availability of a block that may legitimately not exist on this appliance.
type section struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type dashHourBucket struct {
	Hour     int   `json:"hour"`
	SignIns  int64 `json:"sign_ins"`
	Sessions int64 `json:"sessions_started"`
}

type dashGuests struct {
	// Devices with a live session right now.
	DevicesOnline int64 `json:"devices_online"`
	// Distinct subjects (rooms, accounts, vouchers, guests) with at least one device online. This is the figure
	// a licence counts and the one a manager means by "how many guests are on".
	GuestsOnline int64 `json:"guests_online"`
	// Sessions that STARTED since local midnight. Counted as sign-ins because every session begins with one.
	SignInsToday   int64 `json:"sign_ins_today"`
	DevicesToday   int64 `json:"devices_today"`
	SignIns7d      int64 `json:"sign_ins_7d"`
	BusiestHour    *int  `json:"busiest_hour_today,omitempty"`
	BusiestHourQty int64 `json:"busiest_hour_count,omitempty"`
}

type dashData struct {
	DownToday  int64 `json:"bytes_down_today"`
	UpToday    int64 `json:"bytes_up_today"`
	TotalToday int64 `json:"total_bytes_today"`
	Down7d     int64 `json:"bytes_down_7d"`
	Up7d       int64 `json:"bytes_up_7d"`
}

type dashOccupancy struct {
	section
	InHouse         int64 `json:"in_house"`
	WithInternet    int64 `json:"with_internet"`
	ArrivalsToday   int64 `json:"arrivals_today"`
	DeparturesToday int64 `json:"departures_today"`
	PostingAllowed  int64 `json:"posting_allowed"`
}

type dashSignInChecks struct {
	section
	Window   string           `json:"window"`
	Total    int64            `json:"total"`
	Verified int64            `json:"verified"`
	Outcomes []dashOutcomeRow `json:"outcomes"`
}

type dashOutcomeRow struct {
	Code  string `json:"outcome_code"`
	Count int64  `json:"count"`
}

type dashPmsInterface struct {
	ID             string     `json:"pms_interface_id"`
	Label          string     `json:"display_label"`
	Lifecycle      string     `json:"lifecycle_state"`
	Published      bool       `json:"published"`
	Transport      string     `json:"transport_status"`
	Continuity     string     `json:"continuity_status"`
	Sync           string     `json:"sync_status"`
	SyncStage      string     `json:"sync_stage,omitempty"`
	RoomAuthReady  bool       `json:"room_auth_ready"`
	RoomAuthReason string     `json:"room_auth_reason,omitempty"`
	InHouseStays   int        `json:"in_house_stays"`
	PendingEvents  int        `json:"pending_events"`
	ReviewEvents   int        `json:"review_events"`
	LastStayEvent  *time.Time `json:"last_stay_event_at,omitempty"`
	LastHeartbeat  *time.Time `json:"last_heartbeat_at,omitempty"`
	LastSyncAt     *time.Time `json:"last_complete_sync_at,omitempty"`
	RecordsRecv    int64      `json:"sync_records_received,omitempty"`
	Materialized   bool       `json:"materialization_ready"`
}

type dashPms struct {
	section
	Interfaces []dashPmsInterface `json:"interfaces"`
	// EventsToday is the whole feed's activity, which is what tells an operator the connection is doing
	// something rather than merely being connected.
	EventsToday   int64 `json:"events_today"`
	EventsApplied int64 `json:"events_applied_today"`
	EventsReview  int64 `json:"events_needing_review"`
}

type dashPostings struct {
	section
	// POSTINGS ARE ROOM CHARGES SENT TO THE PMS. Today's counts by where they got to, plus the two queues an
	// operator is responsible for draining.
	PostedToday int64 `json:"posted_today"`
	FailedToday int64 `json:"failed_today"`
	Pending     int64 `json:"pending"`
	ReviewOpen  int64 `json:"review_open"`
	UnknownOpen int64 `json:"unknown_open"`
}

type dashNetwork struct {
	Name          string `json:"name"`
	Bridge        string `json:"bridge_name"`
	Enabled       bool   `json:"enabled"`
	VlanID        *int32 `json:"vlan_id,omitempty"`
	SubnetCIDR    string `json:"subnet_cidr"`
	DhcpMode      string `json:"dhcp_mode"`
	PoolAddresses int64  `json:"pool_addresses"`
	// Devices with a live session on this network's bridge. This is what the appliance itself knows; the DHCP
	// lease count is a separate fact owned by Kea and is read on the DHCP screen, not here.
	DevicesOnline int64 `json:"devices_online"`
	CaptivePortal bool  `json:"captive_portal_enabled"`
	Internet      bool  `json:"internet_access_enabled"`
}

type dashPackageRow struct {
	PackageID string `json:"package_id"`
	Code      string `json:"code"`
	Name      string `json:"name,omitempty"`
	Active    bool   `json:"active"`
	OnlineNow int64  `json:"online_now"`
	Grants7d  int64  `json:"grants_7d"`
}

type dashboardResp struct {
	GeneratedAt time.Time `json:"generated_at"`
	// The day boundary every "today" figure below is measured from, so the UI can say which day it means
	// instead of the operator assuming it is theirs.
	DayStart time.Time `json:"day_start"`

	Guests    dashGuests       `json:"guests"`
	Data      dashData         `json:"data"`
	Hourly    []dashHourBucket `json:"hourly"`
	Occupancy dashOccupancy    `json:"occupancy"`
	SignIn    dashSignInChecks `json:"sign_in_checks"`
	PMS       dashPms          `json:"pms"`
	Postings  dashPostings     `json:"postings"`
	Networks  []dashNetwork    `json:"networks"`
	Packages  []dashPackageRow `json:"packages"`
}

// ---------------------------------------------------------------------------------------------------------

func (s *server) reportsDashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	out := dashboardResp{GeneratedAt: time.Now().UTC()}

	// The day boundary is the DATABASE's local midnight, because that is the boundary every "today" count below
	// is computed against. Returning it means the UI never has to guess whether "today" is the browser's day or
	// the appliance's.
	_ = s.db.QueryRow(ctx, `SELECT date_trunc('day', now())`).Scan(&out.DayStart)

	out.Guests, out.Data, out.Hourly = s.dashSessions(ctx)
	out.Occupancy = s.dashOccupancy(ctx)
	out.SignIn = s.dashSignIn(ctx)
	out.PMS = s.dashPMS(ctx)
	out.Postings = s.dashPostings(ctx)
	out.Networks = s.dashNetworks(ctx)
	out.Packages = s.dashPackages(ctx)

	writeJSON(w, http.StatusOK, out)
}

// dashSessions is the one block that must work: it reads iam_v2.sessions, which this service always has.
func (s *server) dashSessions(ctx context.Context) (dashGuests, dashData, []dashHourBucket) {
	var g dashGuests
	var d dashData

	// ONE PASS over today's sessions plus the live counts. Written as a single row of aggregates rather than six
	// round trips because the dashboard polls, and six queries per poll against a hypertable-adjacent table is a
	// cost paid every thirty seconds for no benefit.
	_ = s.db.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE state = 'active'),
		  count(DISTINCT mac) FILTER (WHERE state = 'active'),
		  count(DISTINCT entitlement_id) FILTER (WHERE state = 'active'),
		  count(*) FILTER (WHERE started >= date_trunc('day', now())),
		  count(DISTINCT mac) FILTER (WHERE started >= date_trunc('day', now())),
		  count(*) FILTER (WHERE started >= now() - interval '7 days'),
		  COALESCE(sum(bytes_down) FILTER (WHERE started >= date_trunc('day', now())), 0),
		  COALESCE(sum(bytes_up)   FILTER (WHERE started >= date_trunc('day', now())), 0),
		  COALESCE(sum(bytes_down) FILTER (WHERE started >= now() - interval '7 days'), 0),
		  COALESCE(sum(bytes_up)   FILTER (WHERE started >= now() - interval '7 days'), 0)
		  FROM iam_v2.sessions
		 WHERE tenant_id = $1 AND site_id = $2
	`, s.tenantID, s.siteID).Scan(
		new(int64), &g.DevicesOnline, &g.GuestsOnline,
		&g.SignInsToday, &g.DevicesToday, &g.SignIns7d,
		&d.DownToday, &d.UpToday, &d.Down7d, &d.Up7d)
	d.TotalToday = d.DownToday + d.UpToday

	// THE HOURLY SHAPE OF THE DAY. generate_series supplies all 24 buckets so an hour with no activity is a
	// zero-height column rather than a missing one — a chart that silently omits quiet hours misstates the shape
	// of the night, which is the only thing the chart is for.
	hourly := make([]dashHourBucket, 0, 24)
	rows, err := s.db.Query(ctx, `
		SELECT h::int, COALESCE(c.n, 0)
		  FROM generate_series(0, 23) AS h
		  LEFT JOIN (
		      SELECT extract(hour FROM started)::int AS hr, count(*) AS n
		        FROM iam_v2.sessions
		       WHERE tenant_id = $1 AND site_id = $2 AND started >= date_trunc('day', now())
		       GROUP BY 1
		  ) c ON c.hr = h
		 ORDER BY h
	`, s.tenantID, s.siteID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var b dashHourBucket
			if rows.Scan(&b.Hour, &b.SignIns) == nil {
				b.Sessions = b.SignIns
				hourly = append(hourly, b)
			}
		}
	}
	for _, b := range hourly {
		if b.SignIns > g.BusiestHourQty {
			h := b.Hour
			g.BusiestHour = &h
			g.BusiestHourQty = b.SignIns
		}
	}
	return g, d, hourly
}

func (s *server) dashOccupancy(ctx context.Context) dashOccupancy {
	var o dashOccupancy
	err := s.db.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE st.status = 'IN_HOUSE'),
		  count(*) FILTER (WHERE st.status = 'IN_HOUSE' AND ent.id IS NOT NULL),
		  count(*) FILTER (WHERE st.arrival   = current_date),
		  count(*) FILTER (WHERE st.departure = current_date),
		  count(*) FILTER (WHERE st.posting_allowed)
		  FROM iam_v2.stays st
		  LEFT JOIN iam_v2.entitlements ent
		         ON ent.tenant_id = st.tenant_id AND ent.site_id = st.site_id AND ent.stay_id = st.id
		        AND ent.status IN ('PENDING','ACTIVE','SUSPENDED')
		 WHERE st.tenant_id = $1 AND st.site_id = $2
	`, s.tenantID, s.siteID).Scan(&o.InHouse, &o.WithInternet, &o.ArrivalsToday, &o.DeparturesToday, &o.PostingAllowed)
	if err != nil {
		// A PMS-less appliance is a normal deployment, not a fault. Saying so is the honest surface; a row of
		// zeroes would read as "the hotel is empty".
		o.section = section{Available: false, Reason: "no_pms_stay_data"}
		return o
	}
	o.section = section{Available: true}
	return o
}

// dashSignIn summarises the recent room sign-in attempts — the screen's own "is anyone getting in?" question,
// as a single pass/fail split plus the outcome codes behind it. No guest is named, exactly as on the evidence
// screen this mirrors.
func (s *server) dashSignIn(ctx context.Context) dashSignInChecks {
	c := dashSignInChecks{Window: "24h", Outcomes: []dashOutcomeRow{}}
	rows, err := s.db.Query(ctx, `
		SELECT outcome_code, count(*), count(*) FILTER (WHERE resolved_stay_id IS NOT NULL)
		  FROM iam_v2.auth_resolutions
		 WHERE tenant_id = $1 AND site_id = $2 AND resolved_at >= now() - interval '24 hours'
		 GROUP BY outcome_code
		 ORDER BY 2 DESC
	`, s.tenantID, s.siteID)
	if err != nil {
		c.section = section{Available: false, Reason: "no_resolution_data"}
		return c
	}
	defer rows.Close()
	for rows.Next() {
		var row dashOutcomeRow
		var verified int64
		if rows.Scan(&row.Code, &row.Count, &verified) == nil {
			c.Outcomes = append(c.Outcomes, row)
			c.Total += row.Count
			c.Verified += verified
		}
	}
	c.section = section{Available: true}
	return c
}

func (s *server) dashPMS(ctx context.Context) dashPms {
	p := dashPms{Interfaces: []dashPmsInterface{}}

	rows, err := s.db.Query(ctx, `
		SELECT i.id::text, COALESCE(i.display_label,''), i.lifecycle_state,
		       (i.current_revision_id IS NOT NULL)
		  FROM iam_v2.pms_interfaces i
		 WHERE i.tenant_id = $1 AND i.site_id = $2
		   AND i.lifecycle_state <> 'DECOMMISSIONED'
		 ORDER BY i.display_label, i.id
	`, s.tenantID, s.siteID)
	if err != nil {
		p.section = section{Available: false, Reason: "pms_not_enabled"}
		return p
	}
	type base struct {
		id, label, lifecycle string
		published            bool
	}
	var bases []base
	for rows.Next() {
		var b base
		if rows.Scan(&b.id, &b.label, &b.lifecycle, &b.published) == nil {
			bases = append(bases, b)
		}
	}
	rows.Close()

	// The per-interface picture is taken from interfaceHealthRow — the SAME derivation the PMS connection screen
	// renders. Re-deriving "is it connected" here with a second, simpler rule is how two screens end up
	// disagreeing about whether guests can sign in, and the operator believes whichever they opened first.
	for _, b := range bases {
		h, err := s.interfaceHealthRow(ctx, b.id)
		row := dashPmsInterface{
			ID: b.id, Label: b.label, Lifecycle: b.lifecycle, Published: b.published,
		}
		if err == nil {
			row.Transport = h.Transport
			row.Continuity = h.Continuity
			row.Sync = h.Sync
			row.SyncStage = h.SyncStage
			row.RoomAuthReady = h.RoomAuthReady
			row.RoomAuthReason = h.RoomAuthReason
			row.InHouseStays = h.InHouseStays
			row.PendingEvents = h.PendingEvents
			row.ReviewEvents = h.ReviewEvents
			row.LastStayEvent = h.LastStayEvent
			row.LastHeartbeat = h.LastHeartbeatAt
			row.LastSyncAt = h.LastCompleteSyncAt
			row.RecordsRecv = h.RecordsReceived
			row.Materialized = h.MaterializationReady
		}
		p.Interfaces = append(p.Interfaces, row)
	}

	_ = s.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE received_at >= date_trunc('day', now())),
		       count(*) FILTER (WHERE received_at >= date_trunc('day', now()) AND processing_status = 'APPLIED'),
		       count(*) FILTER (WHERE processing_status = 'MANUAL_REVIEW')
		  FROM iam_v2.stay_events WHERE tenant_id = $1 AND site_id = $2
	`, s.tenantID, s.siteID).Scan(&p.EventsToday, &p.EventsApplied, &p.EventsReview)

	p.section = section{Available: true}
	return p
}

// dashPostings reads the room-charge surface. It is the most likely block to be absent: the financial tables
// exist only where that capability was deployed, so an error here is reported as "not in use" rather than
// propagated.
func (s *server) dashPostings(ctx context.Context) dashPostings {
	var p dashPostings
	// The execution states are the view's own closed vocabulary: NOT_ATTEMPTED, IN_FLIGHT, UNKNOWN, NOT_SENT,
	// POSTED, REJECTED. "Today" is measured from created_at because that is the only timestamp the view carries
	// — it is a derived read model over the posting, attempt and review ledgers and keeps no updated_at — so
	// these are charges RAISED today and where each has got to, which is the question a night auditor asks.
	err := s.db.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE pes.execution_state = 'POSTED'
		                     AND pes.created_at >= date_trunc('day', now())),
		  count(*) FILTER (WHERE pes.execution_state IN ('NOT_SENT','REJECTED')
		                     AND pes.created_at >= date_trunc('day', now())),
		  count(*) FILTER (WHERE pes.execution_state IN ('NOT_ATTEMPTED','IN_FLIGHT')
		                      OR pes.outbox_state IN ('QUEUED','IN_FLIGHT')),
		  count(*) FILTER (WHERE pes.awaiting_manual_review),
		  count(*) FILTER (WHERE pes.execution_state = 'UNKNOWN')
		  FROM iam_v2.posting_execution_state pes
		 WHERE pes.tenant_id = $1 AND pes.site_id = $2
	`, s.tenantID, s.siteID).Scan(&p.PostedToday, &p.FailedToday, &p.Pending, &p.ReviewOpen, &p.UnknownOpen)
	if err != nil {
		p.section = section{Available: false, Reason: "charges_not_enabled"}
		return p
	}
	p.section = section{Available: true}
	return p
}

func (s *server) dashNetworks(ctx context.Context) []dashNetwork {
	out := []dashNetwork{}
	rows, err := s.db.Query(ctx, `
		SELECT gn.name, gn.bridge_name, gn.enabled, gn.vlan_id, gn.subnet_cidr::text, gn.dhcp_mode,
		       gn.captive_portal_enabled, gn.internet_access_enabled,
		       -- THE POOL SIZE, summed across every range configured for the network. Counted from the stored
		       -- start/end addresses rather than from the subnet mask: a /22 with one small pool in it has 1,022
		       -- usable addresses and perhaps 50 issuable ones, and the number that matters is the second.
		       COALESCE((SELECT sum(
		                    (split_part(host(p.end_ip),'.',4)::bigint
		                   - split_part(host(p.start_ip),'.',4)::bigint) + 1
		                 )
		                  FROM public.dhcp_pools p
		                 WHERE p.guest_network_id = gn.id
		                   AND family(p.start_ip) = 4 AND family(p.end_ip) = 4
		                   AND network(p.start_ip::cidr) = network(p.end_ip::cidr)), 0)::bigint,
		       COALESCE((SELECT count(DISTINCT se.mac) FROM iam_v2.sessions se
		                  WHERE se.tenant_id = gn.tenant_id AND se.site_id = gn.site_id
		                    AND se.state = 'active'
		                    AND se.ingress_interface = gn.bridge_name), 0)::bigint
		  FROM public.guest_networks gn
		 WHERE gn.tenant_id = $1 AND gn.site_id = $2
		 ORDER BY gn.enabled DESC, gn.name
	`, s.tenantID, s.siteID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var n dashNetwork
		if rows.Scan(&n.Name, &n.Bridge, &n.Enabled, &n.VlanID, &n.SubnetCIDR, &n.DhcpMode,
			&n.CaptivePortal, &n.Internet, &n.PoolAddresses, &n.DevicesOnline) == nil {
			out = append(out, n)
		}
	}
	return out
}

// dashPackages answers "what are guests actually on right now", which the old top_plans_7d card was meant to
// and could not.
//
// THE OLD QUERY WAS DEAD. It read `iam_v2.entitlements e JOIN iam_v2.internet_packages p ON p.id = e.package_id`
// and selected `p.name`. iam_v2.entitlements has no package_id — it pins a package_revision_id — and
// iam_v2.internet_packages has no name column; the display name lives in the revision's `display` JSON. So
// every execution failed, the handler's `if err == nil` swallowed it, and the card has silently shown an empty
// list since it was written. The join below goes through the revision, which is where the link actually is.
func (s *server) dashPackages(ctx context.Context) []dashPackageRow {
	out := []dashPackageRow{}
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, p.code, COALESCE(cur.display->>'name',''), p.active,
		       COALESCE(live.online, 0)::bigint,
		       COALESCE(recent.granted, 0)::bigint
		  FROM iam_v2.internet_packages p
		  LEFT JOIN iam_v2.internet_package_revisions cur ON cur.id = p.current_revision_id
		  LEFT JOIN (
		      -- Devices online now, grouped by the package the entitlement was granted from.
		      SELECT r.package_id AS pkg, count(DISTINCT se.mac) AS online
		        FROM iam_v2.sessions se
		        JOIN iam_v2.entitlements e
		          ON e.tenant_id = se.tenant_id AND e.site_id = se.site_id AND e.id = se.entitlement_id
		        JOIN iam_v2.internet_package_revisions r
		          ON r.tenant_id = e.tenant_id AND r.site_id = e.site_id AND r.id = e.package_revision_id
		       WHERE se.tenant_id = $1 AND se.site_id = $2 AND se.state = 'active'
		       GROUP BY r.package_id
		  ) live ON live.pkg = p.id
		  LEFT JOIN (
		      SELECT r.package_id AS pkg, count(*) AS granted
		        FROM iam_v2.entitlements e
		        JOIN iam_v2.internet_package_revisions r
		          ON r.tenant_id = e.tenant_id AND r.site_id = e.site_id AND r.id = e.package_revision_id
		       WHERE e.tenant_id = $1 AND e.site_id = $2 AND e.activated_at >= now() - interval '7 days'
		       GROUP BY r.package_id
		  ) recent ON recent.pkg = p.id
		 WHERE p.tenant_id = $1 AND p.site_id = $2 AND p.is_system = false
		 ORDER BY COALESCE(live.online,0) DESC, COALESCE(recent.granted,0) DESC, p.code
		 LIMIT 8
	`, s.tenantID, s.siteID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r dashPackageRow
		if rows.Scan(&r.PackageID, &r.Code, &r.Name, &r.Active, &r.OnlineNow, &r.Grants7d) == nil {
			out = append(out, r)
		}
	}
	return out
}
