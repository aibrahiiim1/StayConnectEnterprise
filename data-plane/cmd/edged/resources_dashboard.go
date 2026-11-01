package main

// Operational snapshot behind the dashboard, assembled in one read. Nothing is invented: where a figure is
// unavailable the section reports `available: false` with a reason rather than a misleading zero. Each section
// fails independently, since some read phase- or privilege-gated surfaces that differ per appliance. No guest is
// named. There is no hourly byte series here: attributing a session's whole-day total to its start hour would
// misplace traffic, so the time series lives in /reports/overview (resources_overview.go).

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

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
	DevicesOnline int64 `json:"devices_online"`
	// Distinct subjects with at least one device online: the figure a licence counts.
	GuestsOnline int64 `json:"guests_online"`
	// Sessions that started since local midnight, counted as sign-ins.
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
	RoomAuthSource string     `json:"room_auth_source,omitempty"`
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
	Interfaces    []dashPmsInterface `json:"interfaces"`
	EventsToday   int64              `json:"events_today"`
	EventsApplied int64              `json:"events_applied_today"`
	EventsReview  int64              `json:"events_needing_review"`
	// Outstanding cases that are not operational work (e.g. a departure for a stay never received). They cannot be
	// actioned here; counted apart from EventsReview so the two are never added together.
	HistoricalExceptions int64 `json:"historical_exceptions"`
}

type dashPostings struct {
	section
	// Postings are room charges sent to the PMS: today's counts by state, plus the two queues an operator drains.
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
	// The DHCP lease count is owned by Kea and read on the DHCP screen, not here.
	DevicesOnline int64 `json:"devices_online"`
	CaptivePortal bool  `json:"captive_portal_enabled"`
	Internet      bool  `json:"internet_access_enabled"`
	// Untagged (the port's native network) or vlan (tagged); a trunk port can carry both.
	NetworkType string `json:"network_type"`
	// Room sign-in on this network: the policy and the connections it uses. Archived networks are history and are not listed here at all.
	RoomPolicy string   `json:"room_sign_in_policy,omitempty"`
	RoomPMS    []string `json:"room_sign_in_connections,omitempty"`
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
	// Day boundary for every "today" figure, so the UI can say which day it means.
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

func (s *server) reportsDashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	out := dashboardResp{GeneratedAt: time.Now().UTC()}

	// The day boundary is the database's local midnight, which every "today" count is computed against.
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

func (s *server) dashSessions(ctx context.Context) (dashGuests, dashData, []dashHourBucket) {
	var g dashGuests
	var d dashData

	// One pass of aggregates rather than six round trips, because the dashboard polls.
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

	// generate_series supplies all 24 buckets so a quiet hour is a zero-height column, not a missing one.
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
		// A PMS-less appliance is a normal deployment, not a fault; zeroes would read as "the hotel is empty".
		o.section = section{Available: false, Reason: "no_pms_stay_data"}
		return o
	}
	o.section = section{Available: true}
	return o
}

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

	// Uses interfaceHealthRow, the same derivation as the PMS connection screen, so the two screens cannot disagree.
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
			row.RoomAuthSource = h.RoomAuthSource
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
		       count(*) FILTER (WHERE received_at >= date_trunc('day', now()) AND processing_status = 'APPLIED')
		  FROM iam_v2.stay_events WHERE tenant_id = $1 AND site_id = $2
	`, s.tenantID, s.siteID).Scan(&p.EventsToday, &p.EventsApplied)

	// Counts departure cases that still need a decision (an event in MANUAL_REVIEW with no disposition), not rows:
	// an unplaceable departure is restaged on every reconnect. pms_reconciliation_cases collapses the copies without dropping any.
	if err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM iam_v2.pms_reconciliation_cases c
		 WHERE c.tenant_id = $1 AND c.site_id = $2
		   AND EXISTS (SELECT 1 FROM iam_v2.pms_unanswered_review_events e
		                WHERE e.id = c.latest_event_id AND NOT `+pmsSupersededByRoster+`)
	`, s.tenantID, s.siteID).Scan(&p.EventsReview); err != nil {
		// The view does not exist yet; fall back to the row count rather than a reassuring zero.
		_ = s.db.QueryRow(ctx, `
			SELECT count(*) FROM iam_v2.stay_events
			 WHERE tenant_id = $1 AND site_id = $2 AND processing_status = 'MANUAL_REVIEW'
		`, s.tenantID, s.siteID).Scan(&p.EventsReview)
	}

	// Historical exceptions are deliberately not subtracted: they are a different kind of thing.
	_ = s.db.QueryRow(ctx, `
		SELECT count(*) FROM iam_v2.pms_unanswered_review_events e
		 WHERE e.tenant_id = $1 AND e.site_id = $2
		   AND e.event_type = 'GO' AND e.admission_kind = 'LIVE' AND NOT `+pmsSupersededByRoster+`
	`, s.tenantID, s.siteID).Scan(&p.HistoricalExceptions)

	// EventsReview must mean operational work only, or the two numbers overlap.
	if p.EventsReview >= p.HistoricalExceptions {
		p.EventsReview -= p.HistoricalExceptions
	}

	p.section = section{Available: true}
	return p
}

// The most likely block to be absent: the financial tables exist only where that capability is deployed, so an
// error is reported as "not in use" rather than propagated.
func (s *server) dashPostings(ctx context.Context) dashPostings {
	var p dashPostings
	// "Today" is measured from created_at, the only timestamp this derived read model carries (no updated_at), so
	// these are charges raised today and where each has got to.
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
	// Every text column is coalesced: they are nullable in the schema, a NULL made Scan fail, and the loop appends
	// only on success, so a half-configured network would vanish from the dashboard silently.
	rows, err := s.db.Query(ctx, `
		SELECT COALESCE(gn.name,''), COALESCE(gn.bridge_name,''), gn.enabled, gn.vlan_id, COALESCE(gn.network_type,''),
		       COALESCE((SELECT p.mode FROM iam_v2.guest_network_pms_policy p WHERE p.guest_network_id = gn.id), ''),
		       COALESCE((SELECT array_agg(i.display_label ORDER BY i.display_label) FROM iam_v2.guest_network_pms_map m
		                   JOIN iam_v2.pms_interfaces i ON i.id = m.pms_interface_id AND i.lifecycle_state <> 'DECOMMISSIONED'
		                  WHERE m.guest_network_id = gn.id), '{}'),
		       COALESCE(gn.subnet_cidr::text,''), COALESCE(gn.dhcp_mode,''),
		       gn.captive_portal_enabled, gn.internet_access_enabled,
		       -- THE POOL SIZE, summed across every range configured for the network. Counted from the stored
		       -- start/end addresses rather than from the subnet mask: a /22 with one small pool in it has 1,022
		       -- usable addresses and perhaps 50 issuable ones, and the number that matters is the second.
		       --
		       -- THE SAME-SUBNET GUARD USED TO EXCLUDE EVERY REAL POOL. It read
		       --     network(p.start_ip::cidr) = network(p.end_ip::cidr)
		       -- and an inet host cast to cidr carries a /32, so that compares 192.168.77.100/32 with
		       -- 192.168.77.200/32 and is true only when the pool is a SINGLE address. Every range of more than
		       -- one address failed it, the sum came back NULL, and the dashboard told the operator "no address
		       -- range is configured, so this appliance cannot hand out addresses here" about a network Kea was
		       -- actively serving 101 addresses from. During a guest pilot that is worse than silence: it sends
		       -- somebody to repair a working DHCP configuration.
		       --
		       -- AND THE FIX FOR THAT STILL ONLY COUNTED /24 RANGES. The arithmetic subtracted the FOURTH OCTET,
		       -- so it needed a guard that both ends share a /24 -- and that guard excluded (scored as zero)
		       -- every pool crossing an octet boundary, e.g. 10.20.0.10-10.20.3.250 on a /22. inet minus inet is
		       -- the integer distance between two addresses in Postgres, for any IPv4 range, so the size is
		       -- simply that distance plus one. No octet arithmetic, no /24 guard.
		       COALESCE((SELECT sum((p.end_ip - p.start_ip) + 1)
		                  FROM public.dhcp_pools p
		                 WHERE p.guest_network_id = gn.id
		                   AND family(p.start_ip) = 4 AND family(p.end_ip) = 4
		                   AND p.end_ip >= p.start_ip), 0)::bigint,
		       COALESCE((SELECT count(DISTINCT se.mac) FROM iam_v2.sessions se
		                  WHERE se.tenant_id = gn.tenant_id AND se.site_id = gn.site_id
		                    AND se.state = 'active'
		                    AND se.ingress_interface = gn.bridge_name
		                    AND NULLIF(gn.bridge_name,'') IS NOT NULL), 0)::bigint
		  FROM public.guest_networks gn
		 WHERE gn.tenant_id = $1 AND gn.site_id = $2
		   AND NOT (NOT gn.enabled AND EXISTS (SELECT 1 FROM iam_v2.network_archive a WHERE a.network_id = gn.id))
		 ORDER BY gn.enabled DESC, gn.name
	`, s.tenantID, s.siteID)
	if err != nil {
		// A query error is not an empty property: returning `[]` would read as "this site has no guest networks", so the cause is logged.
		slog.Warn("dashboard: guest-network section unavailable", "err", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var n dashNetwork
		if err := rows.Scan(&n.Name, &n.Bridge, &n.Enabled, &n.VlanID, &n.NetworkType, &n.RoomPolicy, &n.RoomPMS, &n.SubnetCIDR, &n.DhcpMode,
			&n.CaptivePortal, &n.Internet, &n.PoolAddresses, &n.DevicesOnline); err != nil {
			slog.Warn("dashboard: skipped a guest-network row", "err", err)
			continue
		}
		out = append(out, n)
	}
	return out
}

// Answers "what are guests actually on right now". Entitlements pin a package_revision_id (no package_id) and
// packages have no name column, so the join goes through the revision's `display` JSON.
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
