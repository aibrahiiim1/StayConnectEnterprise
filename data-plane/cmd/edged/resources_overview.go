package main

// GET /edge/v1/reports/overview?range=24h|7d|30d — THE OVERVIEW.
//
// /reports/dashboard answers "what does tonight look like". This answers the wider question a duty manager or an
// IT manager asks of a property: how busy has it been, how much did guests use, is anybody failing to get in,
// what are they on, are the networks and pools healthy, and is the appliance itself all right. It is additive:
// /reports/dashboard is untouched and still served.
//
// The rules are the dashboard's rules, and they are what keeps the figures defensible:
//
//  1. NOTHING IS INVENTED. Every figure traces to a row, a lease or a kernel counter this service may read. What
//     is not measured anywhere — voucher, account and one-time-code sign-in FAILURES, DNS query statistics — is
//     named as not recorded, never estimated.
//  2. EVERY BLOCK FAILS ALONE. Each block carries its own `available`/`reason`. A Kea that cannot be reached
//     blanks the lease figures and nothing else.
//  3. A BLOCK THE CALLER'S ROLE MAY NOT READ IS NOT SENT. The route is gated on `reports`; the blocks drawn from
//     other surfaces are additionally gated on that surface's own key (network, diagnostics, license), and a
//     role without it receives `not_permitted_for_role` rather than the data. Attention items are derived only
//     from blocks the caller could read, so they cannot leak around the gate.
//  4. NO GUEST IS NAMED, and no address, MAC or room appears. Leases are counted, never listed.
//  5. EVERY QUERY IS BOUNDED: by the window (at most 30 days), by a LIMIT, and — for the one expensive read, the
//     accounting samples — by a statement timeout and a short-lived cache.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------------------------------------
// wire shapes
// ---------------------------------------------------------------------------------------------------------

const reasonNotPermitted = "not_permitted_for_role"

type ovMethod struct {
	Method string  `json:"method"`
	Total  int64   `json:"total"`
	Series []int64 `json:"series"`
}

type ovConcurrency struct {
	section
	// Definition is stated in the payload because "concurrent devices" can mean two things and a chart that
	// does not say which is a chart two people will read differently.
	Definition  string    `json:"definition"`
	Peak        []int     `json:"peak"`
	Average     []float64 `json:"average"`
	PeakInRange int       `json:"peak_in_range"`
}

type ovHeatmap struct {
	Rows    []string  `json:"rows"`
	Columns []string  `json:"columns"`
	Values  [][]int64 `json:"values"`
}

type ovGuests struct {
	section
	DevicesOnline int64         `json:"devices_online"`
	GuestsOnline  int64         `json:"guests_online"`
	SignIns       int64         `json:"sign_ins"`
	UniqueDevices int64         `json:"unique_devices"`
	SignInsSeries []int64       `json:"sign_ins_series"`
	ByMethod      []ovMethod    `json:"sign_ins_by_method"`
	Concurrency   ovConcurrency `json:"concurrency"`
	Heatmap       ovHeatmap     `json:"heatmap"`
}

type ovTrafficRow struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	BytesDown int64  `json:"bytes_down"`
	BytesUp   int64  `json:"bytes_up"`
}

type ovTraffic struct {
	section
	BytesDown   int64          `json:"bytes_down"`
	BytesUp     int64          `json:"bytes_up"`
	SeriesDown  []int64        `json:"series_down"`
	SeriesUp    []int64        `json:"series_up"`
	TopNetworks []ovTrafficRow `json:"top_networks"`
	TopPackages []ovTrafficRow `json:"top_packages"`
	Sessions    int            `json:"sessions_with_traffic"`
	ComputedAt  time.Time      `json:"computed_at"`
	Source      string         `json:"source"`
	// byIface is every network's figure, not only the top eight, for the per-network table.
	byIface map[string][2]int64
}

type ovResultRow struct {
	Result string `json:"result"`
	Count  int64  `json:"count"`
}

type ovLatency struct {
	Samples int64    `json:"samples"`
	P50ms   *float64 `json:"p50_ms"`
	P95ms   *float64 `json:"p95_ms"`
}

type ovSignIn struct {
	section
	Total          int64         `json:"total"`
	Verified       int64         `json:"verified"`
	ByResult       []ovResultRow `json:"by_result"`
	SeriesVerified []int64       `json:"series_verified"`
	SeriesFailed   []int64       `json:"series_failed"`
	Latency        ovLatency     `json:"latency"`
	// Covered names what the attempt ledger records; NotRecorded names what nothing on this appliance records.
	Covered     string   `json:"covered"`
	NotRecorded []string `json:"not_recorded"`
}

type ovPackageRow struct {
	PackageID string `json:"package_id"`
	Name      string `json:"name"`
	Code      string `json:"code,omitempty"`
	Count     int64  `json:"count"`
}

type ovReasonRow struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

type ovPackages struct {
	section
	ActiveNow    []ovPackageRow `json:"active_now"`
	Grants       []ovPackageRow `json:"grants"`
	Terminations []ovReasonRow  `json:"terminations"`
}

type ovPMS struct {
	dashPms
	ActiveInterfaces int           `json:"active_interfaces"`
	ReadyInterfaces  int           `json:"ready_interfaces"`
	Occupancy        dashOccupancy `json:"occupancy"`
	Postings         dashPostings  `json:"postings"`
}

type ovNetwork struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	VlanID        *int32   `json:"vlan_id,omitempty"`
	SubnetCIDR    string   `json:"subnet_cidr"`
	Bridge        string   `json:"bridge_name"`
	DhcpMode      string   `json:"dhcp_mode"`
	DNSMode       string   `json:"dns_mode"`
	DNSServers    []string `json:"dns_servers"`
	CaptivePortal bool     `json:"captive_portal_enabled"`
	Internet      bool     `json:"internet_access_enabled"`
	PoolSize      int64    `json:"pool_size"`
	// InvalidPools counts configured ranges that are not a valid IPv4 range, so a zero pool size caused by a
	// bad row is distinguishable from "no range configured".
	InvalidPools  int      `json:"invalid_pools,omitempty"`
	DevicesOnline int64    `json:"devices_online"`
	LeasesKnown   bool     `json:"leases_known"`
	ActiveLeases  *int64   `json:"active_leases,omitempty"`
	LeasesInPool  *int64   `json:"leases_in_pool,omitempty"`
	ExpiringSoon  *int64   `json:"expiring_soon,omitempty"`
	Utilisation   *float64 `json:"utilisation_pct,omitempty"`
	TrafficKnown  bool     `json:"traffic_known"`
	BytesDown     int64    `json:"bytes_down"`
	BytesUp       int64    `json:"bytes_up"`
}

type ovNetworks struct {
	section
	Rows         []ovNetwork `json:"rows"`
	LeasesReason string      `json:"leases_reason,omitempty"`
}

type ovPool struct {
	Network string   `json:"network"`
	Start   string   `json:"start"`
	End     string   `json:"end"`
	Size    int64    `json:"size"`
	Active  int64    `json:"active"`
	Pct     *float64 `json:"utilisation_pct,omitempty"`
}

type ovDHCP struct {
	section
	ServerConfigured      *bool    `json:"server_configured,omitempty"`
	ServerHealthy         *bool    `json:"server_healthy,omitempty"`
	ServerDetail          string   `json:"server_detail,omitempty"`
	LeasesAvailable       bool     `json:"leases_available"`
	LeasesReason          string   `json:"leases_reason,omitempty"`
	ActiveLeases          int64    `json:"active_leases"`
	UnmatchedLeases       int64    `json:"unmatched_leases"`
	ExpiringSoon          int64    `json:"expiring_soon"`
	ExpiringWithinSeconds int64    `json:"expiring_within_seconds"`
	Pools                 []ovPool `json:"pools"`
	CaptiveOptionNetworks []string `json:"captive_option_networks"`
	LocalNetworks         int      `json:"local_dhcp_networks"`
}

type ovDNSNetwork struct {
	Network string   `json:"network"`
	Mode    string   `json:"mode"`
	Servers []string `json:"servers"`
}

type ovDNS struct {
	section
	ResolverState       string         `json:"resolver_state"`
	ResolverHealthy     *bool          `json:"resolver_healthy,omitempty"`
	ResolverDetail      string         `json:"resolver_detail,omitempty"`
	LastHealthyAt       *time.Time     `json:"last_healthy_at,omitempty"`
	Networks            []ovDNSNetwork `json:"networks"`
	StatisticsCollected bool           `json:"statistics_collected"`
}

type ovService struct {
	Name            string     `json:"name"`
	Label           string     `json:"label"`
	State           string     `json:"state"`
	HealthOK        *bool      `json:"health_ok"`
	Critical        bool       `json:"critical"`
	Detail          string     `json:"detail,omitempty"`
	RestartCount    int64      `json:"restart_count"`
	LastHealthyAt   *time.Time `json:"last_healthy_at,omitempty"`
	FailuresInRange int64      `json:"failures_in_range"`
	ManualRestarts  int64      `json:"manual_restarts_in_range"`
}

type ovServices struct {
	section
	Overall  string         `json:"overall"`
	Counts   map[string]int `json:"counts"`
	Services []ovService    `json:"services"`
}

type ovLicense struct {
	section
	State      string `json:"state,omitempty"`
	Installed  bool   `json:"installed"`
	ValidUntil string `json:"valid_until,omitempty"`
	GraceUntil string `json:"grace_until,omitempty"`
}

type ovNetworkConfig struct {
	section
	WANInterface      string    `json:"wan_interface,omitempty"`
	WANMode           string    `json:"wan_mode,omitempty"`
	WANAddress        string    `json:"wan_address,omitempty"`
	WANGateway        string    `json:"wan_gateway,omitempty"`
	WANLinkUp         bool      `json:"wan_link_up"`
	GatewayReachable  *bool     `json:"gateway_reachable,omitempty"`
	InternetReachable *bool     `json:"internet_reachable,omitempty"`
	LANBridge         string    `json:"lan_bridge,omitempty"`
	LANAddress        string    `json:"lan_address,omitempty"`
	LANLinkUp         bool      `json:"lan_link_up"`
	SystemPending     bool      `json:"system_change_pending"`
	CheckedAt         time.Time `json:"checked_at"`
}

type ovRevision struct {
	Seq             int64      `json:"seq"`
	State           string     `json:"state"`
	AppliedAt       *time.Time `json:"applied_at,omitempty"`
	ConfirmedAt     *time.Time `json:"confirmed_at,omitempty"`
	ConfirmDeadline *time.Time `json:"confirm_deadline,omitempty"`
}

type ovRevisions struct {
	section
	LatestActive *ovRevision `json:"latest_active,omitempty"`
	Pending      *ovRevision `json:"pending,omitempty"`
}

type ovAppliance struct {
	Version   string             `json:"version"`
	License   ovLicense          `json:"license"`
	Network   ovNetworkConfig    `json:"network"`
	Revisions ovRevisions        `json:"revisions"`
	Resources applianceResources `json:"resources"`
}

type overviewResp struct {
	GeneratedAt   time.Time       `json:"generated_at"`
	Range         string          `json:"range"`
	WindowStart   time.Time       `json:"window_start"`
	WindowEnd     time.Time       `json:"window_end"`
	BucketSeconds int64           `json:"bucket_seconds"`
	Buckets       []time.Time     `json:"buckets"`
	Timezone      string          `json:"timezone"`
	Guests        ovGuests        `json:"guests"`
	Traffic       ovTraffic       `json:"traffic"`
	SignIn        ovSignIn        `json:"sign_in_outcomes"`
	Packages      ovPackages      `json:"packages"`
	PMS           ovPMS           `json:"pms"`
	Networks      ovNetworks      `json:"networks"`
	DHCP          ovDHCP          `json:"dhcp"`
	DNS           ovDNS           `json:"dns"`
	Services      ovServices      `json:"services"`
	Appliance     ovAppliance     `json:"appliance"`
	Attention     []attentionItem `json:"attention"`
}

// overviewBlockKeys is the permission each block needs IN ADDITION to `reports`, which gates the route. It is
// a table rather than scattered if-statements so the test can hold it against the role matrix.
var overviewBlockKeys = map[string]string{
	"dhcp":                "network",
	"dns":                 "network",
	"networks.leases":     "network",
	"appliance.network":   "network",
	"appliance.revisions": "network",
	"services":            "diagnostics",
	"appliance.resources": "diagnostics",
	"appliance.license":   "license",
}

// ---------------------------------------------------------------------------------------------------------
// bounded reads
// ---------------------------------------------------------------------------------------------------------

// overviewStatementTimeout bounds each statement against the accounting samples. The samples table grows by up
// to one row per active device per second and has no time index, so a mistaken plan must be cut off rather
// than left to hold a connection the portal needs.
const overviewStatementTimeout = 4 * time.Second

// overviewSessionCap bounds the candidate-session read. Beyond it the traffic and concurrency figures are
// reported as unavailable for this range — a truncated candidate list would silently under-report.
const overviewSessionCap = 100000

// overviewSampleBatch is how many session ids go into one `= ANY($1)` probe of the samples index.
const overviewSampleBatch = 1000

func (s *server) boundedRead(ctx context.Context, timeout time.Duration, fn func(pgx.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = "+strconv.FormatInt(timeout.Milliseconds(), 10)); err != nil {
		return err
	}
	return fn(tx)
}

func isStatementTimeout(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "57014" // query_canceled
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// ttlCache holds the few results that are expensive to compute and cheap to be slightly stale. The key always
// includes the window's end, so a new bucket starting is a new key rather than a stale hit.
type ttlCache struct {
	mu sync.Mutex
	m  map[string]ttlEntry
}

type ttlEntry struct {
	at time.Time
	v  any
}

func (c *ttlCache) get(key string, ttl time.Duration) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Since(e.at) > ttl {
		return nil, false
	}
	return e.v, true
}

func (c *ttlCache) put(key string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]ttlEntry{}
	}
	// Bounded: three ranges times a handful of keys. Anything older than an hour is dropped on write.
	for k, e := range c.m {
		if time.Since(e.at) > time.Hour {
			delete(c.m, k)
		}
	}
	c.m[key] = ttlEntry{at: time.Now(), v: v}
}

var overviewCache ttlCache

var trafficTTL = map[string]time.Duration{"24h": time.Minute, "7d": 5 * time.Minute, "30d": 10 * time.Minute}

// ---------------------------------------------------------------------------------------------------------
// the handler
// ---------------------------------------------------------------------------------------------------------

func (s *server) reportsOverview(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("range"))
	if key == "" {
		key = "24h"
	}
	if _, ok := overviewRanges[key]; !ok {
		jsonErr(w, http.StatusBadRequest, "bad_range", errUnknownRange.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	// The window is placed on the DATABASE's clock and local midnight, which is what every timestamp below is
	// compared with; the browser's clock and timezone play no part.
	var now, dayStart time.Time
	var tz string
	if err := s.db.QueryRow(ctx, `SELECT now(), date_trunc('day', now()), current_setting('TimeZone')`).
		Scan(&now, &dayStart, &tz); err != nil {
		slog.Warn("overview: database clock unavailable", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "database_unavailable", "the site database could not be read")
		return
	}
	win, err := computeWindow(key, now, dayStart)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_range", err.Error())
		return
	}

	sess := sessFrom(r.Context())
	can := func(res string) bool { return sess != nil && permFor(sess.Roles, res, permRead) }
	canNetwork, canDiag, canLicense := can("network"), can("diagnostics"), can("license")

	out := overviewResp{
		GeneratedAt: time.Now().UTC(), Range: win.Range, WindowStart: win.Start, WindowEnd: win.End,
		BucketSeconds: int64(win.Bucket / time.Second), Buckets: win.bucketStarts(), Timezone: tz,
	}

	// The slow, independent reads run alongside the database blocks.
	var wg sync.WaitGroup
	var leases []keaLeaseRow
	var leasesErr string
	var kea keaView
	var keaReachable bool
	if canNetwork {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leases, leasesErr = s.overviewLeases(ctx)
			kctx, kc := context.WithTimeout(ctx, 4*time.Second)
			defer kc()
			kea, keaReachable = keaFromNetd(kctx, s)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			out.Appliance.Network = s.overviewSystemNetwork(ctx)
		}()
	}
	if canLicense {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out.Appliance.License = s.overviewLicense(ctx)
		}()
	}
	if canDiag {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out.Appliance.Resources = collectApplianceResources()
		}()
	}

	cands, candErr := s.overviewCandidates(ctx, win)
	out.Guests = s.overviewGuests(ctx, win, cands, candErr)
	out.Traffic = s.overviewTraffic(ctx, win, cands, candErr)
	out.SignIn = s.overviewSignIn(ctx, win)
	out.Packages = s.overviewPackages(ctx, win)
	out.PMS = s.overviewPMS(ctx)
	nets, pools := s.overviewNetworkRows(ctx)
	if canDiag {
		out.Services = s.overviewServices(ctx, win)
	} else {
		out.Services = ovServices{section: section{Available: false, Reason: reasonNotPermitted}, Services: []ovService{}}
	}
	if canNetwork {
		out.Appliance.Revisions = s.overviewRevisions(ctx)
	}
	wg.Wait()

	out.Appliance.Version = version
	if !canNetwork {
		out.Appliance.Network = ovNetworkConfig{section: section{Available: false, Reason: reasonNotPermitted}}
		out.Appliance.Revisions = ovRevisions{section: section{Available: false, Reason: reasonNotPermitted}}
	}
	if !canLicense {
		out.Appliance.License = ovLicense{section: section{Available: false, Reason: reasonNotPermitted}}
	}
	if !canDiag {
		out.Appliance.Resources = applianceResources{section: section{Available: false, Reason: reasonNotPermitted},
			Disks: []diskFigures{}}
	}

	out.Networks, out.DHCP, out.DNS = assembleNetworkBlocks(nets, pools, out.Traffic, leases, leasesErr,
		kea, keaReachable, canNetwork, time.Now())
	out.Traffic.TopNetworks = nameTrafficNetworks(out.Traffic.TopNetworks, nets)
	if canNetwork {
		out.DNS = s.withResolverHealth(ctx, out.DNS)
	}

	out.Attention = deriveAttention(attentionInputFrom(out))
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------------------------------------
// guests and traffic
// ---------------------------------------------------------------------------------------------------------

type overviewCandidate struct {
	ID        string
	Started   time.Time
	Ended     *time.Time
	Interface string
	PackageID string
	HasBytes  bool
}

// overviewCandidates reads every session that overlaps the window, once, for both concurrency and traffic.
//
// iam_v2.sessions has no index on started/ended, so this is a scan of the site's sessions — the same scan the
// existing dashboard's aggregate makes on every poll, and a table that grows by one row per sign-in rather than
// per second. The LIMIT is the cap above plus one, so exceeding it is detectable rather than silent.
func (s *server) overviewCandidates(ctx context.Context, win overviewWindow) ([]overviewCandidate, error) {
	rows, err := s.db.Query(ctx, `
		SELECT se.id::text, se.started, se.ended, COALESCE(se.ingress_interface, ''),
		       COALESCE(r.package_id::text, ''), (se.bytes_down + se.bytes_up) > 0
		  FROM iam_v2.sessions se
		  LEFT JOIN iam_v2.entitlements e
		         ON e.tenant_id = se.tenant_id AND e.site_id = se.site_id AND e.id = se.entitlement_id
		  LEFT JOIN iam_v2.internet_package_revisions r
		         ON r.tenant_id = e.tenant_id AND r.site_id = e.site_id AND r.id = e.package_revision_id
		 WHERE se.tenant_id = $1 AND se.site_id = $2
		   AND se.started < $4 AND (se.ended IS NULL OR se.ended >= $3)
		 LIMIT `+strconv.Itoa(overviewSessionCap+1),
		s.tenantID, s.siteID, win.Start, win.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]overviewCandidate, 0, 256)
	for rows.Next() {
		var c overviewCandidate
		if err := rows.Scan(&c.ID, &c.Started, &c.Ended, &c.Interface, &c.PackageID, &c.HasBytes); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > overviewSessionCap {
		return nil, errTooManySessions
	}
	return out, nil
}

var errTooManySessions = errors.New("too many sessions in range")

func candidateReason(err error) string {
	if errors.Is(err, errTooManySessions) {
		return "too_many_sessions_in_range"
	}
	return "sessions_unreadable"
}

func (s *server) overviewGuests(ctx context.Context, win overviewWindow, cands []overviewCandidate, candErr error) ovGuests {
	g := ovGuests{SignInsSeries: make([]int64, win.N), ByMethod: []ovMethod{}}
	g.Concurrency = ovConcurrency{
		Definition: "Per interval: the most devices connected at the same instant (peak), and the time-weighted " +
			"average number connected. One session is one device.",
		Peak: []int{}, Average: []float64{},
	}
	g.Heatmap = ovHeatmap{
		Rows:    []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"},
		Columns: make([]string, 24),
		Values:  make([][]int64, 7),
	}
	for i := range g.Heatmap.Columns {
		g.Heatmap.Columns[i] = twoDigit(i)
	}
	for i := range g.Heatmap.Values {
		g.Heatmap.Values[i] = make([]int64, 24)
	}

	// Online now, with the dashboard's own definitions (so the two screens cannot disagree), plus the distinct
	// devices that had any session overlapping the window.
	if err := s.db.QueryRow(ctx, `
		SELECT count(DISTINCT mac) FILTER (WHERE state = 'active'),
		       count(DISTINCT entitlement_id) FILTER (WHERE state = 'active'),
		       count(DISTINCT device_id) FILTER (WHERE started < $4 AND (ended IS NULL OR ended >= $3))
		  FROM iam_v2.sessions
		 WHERE tenant_id = $1 AND site_id = $2
	`, s.tenantID, s.siteID, win.Start, win.End).Scan(&g.DevicesOnline, &g.GuestsOnline, &g.UniqueDevices); err != nil {
		slog.Warn("overview: guests unavailable", "err", err)
		g.section = section{Available: false, Reason: "sessions_unreadable"}
		return g
	}

	// Sign-ins over time by method. A sign-in is a session STARTING; every session begins with one.
	methods := map[string]*ovMethod{}
	rows, err := s.db.Query(ctx, `
		SELECT floor(extract(epoch FROM (started - $3::timestamptz)) / $5)::int,
		       COALESCE(NULLIF(credential_method, ''), 'UNKNOWN'), count(*)
		  FROM iam_v2.sessions
		 WHERE tenant_id = $1 AND site_id = $2 AND started >= $3 AND started < $4
		 GROUP BY 1, 2
	`, s.tenantID, s.siteID, win.Start, win.End, win.Bucket.Seconds())
	if err == nil {
		for rows.Next() {
			var b int
			var m string
			var n int64
			if rows.Scan(&b, &m, &n) != nil || b < 0 || b >= win.N {
				continue
			}
			mm := methods[m]
			if mm == nil {
				mm = &ovMethod{Method: m, Series: make([]int64, win.N)}
				methods[m] = mm
			}
			mm.Series[b] += n
			mm.Total += n
			g.SignInsSeries[b] += n
			g.SignIns += n
		}
		rows.Close()
	}
	for _, m := range methods {
		g.ByMethod = append(g.ByMethod, *m)
	}
	sort.Slice(g.ByMethod, func(i, j int) bool {
		if g.ByMethod[i].Total == g.ByMethod[j].Total {
			return g.ByMethod[i].Method < g.ByMethod[j].Method
		}
		return g.ByMethod[i].Total > g.ByMethod[j].Total
	})

	// Day-of-week x hour, in the database's local time. ISO day-of-week: Monday = 1.
	if hrows, err := s.db.Query(ctx, `
		SELECT extract(isodow FROM started)::int, extract(hour FROM started)::int, count(*)
		  FROM iam_v2.sessions
		 WHERE tenant_id = $1 AND site_id = $2 AND started >= $3 AND started < $4
		 GROUP BY 1, 2
	`, s.tenantID, s.siteID, win.Start, win.End); err == nil {
		for hrows.Next() {
			var d, h int
			var n int64
			if hrows.Scan(&d, &h, &n) == nil && d >= 1 && d <= 7 && h >= 0 && h < 24 {
				g.Heatmap.Values[d-1][h] += n
			}
		}
		hrows.Close()
	}

	if candErr != nil {
		g.Concurrency.section = section{Available: false, Reason: candidateReason(candErr)}
	} else {
		ivs := make([]sessionInterval, len(cands))
		for i, c := range cands {
			ivs[i] = sessionInterval{Start: c.Started, End: c.Ended}
		}
		g.Concurrency.Peak, g.Concurrency.Average = concurrency(win, ivs)
		for _, p := range g.Concurrency.Peak {
			if p > g.Concurrency.PeakInRange {
				g.Concurrency.PeakInRange = p
			}
		}
		g.Concurrency.section = section{Available: true}
	}
	g.section = section{Available: true}
	return g
}

// overviewTraffic reads the accounting samples for the window.
//
// HOW THE SAMPLES ARE READ WITHOUT SCANNING THE TABLE. iam_v2.accounting_records has two indexes — its primary
// key and the UNIQUE (session_id, sample_seq) constraint — and none on sampled_at. A predicate on sampled_at
// alone therefore plans as a sequential scan of every sample the appliance has ever recorded (Seq Scan on
// accounting_records, Filter: sampled_at >= ... ), which grows by up to one row per active device per second.
// So the candidate sessions are chosen first, from iam_v2.sessions (above), and the samples are then probed by
// `session_id = ANY($1)` in batches: that predicate is the leading column of the unique index, which gives
//
//	Bitmap Heap Scan on accounting_records ar
//	  Recheck Cond: (session_id = ANY ($1))
//	  Filter: ((sampled_at >= $2) AND (sampled_at < $3))
//	  ->  Bitmap Index Scan on accounting_records_session_id_sample_seq_key
//	        Index Cond: (session_id = ANY ($1))
//
// (or an Index Scan on the same index for a small batch). The cost is then proportional to the samples of the
// sessions in the window, not to the table's history. That is the EXPECTED plan from the index definitions; it
// was not measured on the appliance from this workstation, which is why every statement additionally runs under
// SET LOCAL statement_timeout, and a timeout reports the block as unavailable instead of hanging the page.
//
// Only sessions that recorded any bytes are probed: the ingestion function adds each row's delta to the
// session's own totals in the same transaction, so a session whose totals are zero has no non-zero samples.
func (s *server) overviewTraffic(ctx context.Context, win overviewWindow, cands []overviewCandidate, candErr error) ovTraffic {
	t := ovTraffic{SeriesDown: make([]int64, win.N), SeriesUp: make([]int64, win.N),
		TopNetworks: []ovTrafficRow{}, TopPackages: []ovTrafficRow{}, Source: "accounting_samples"}
	if candErr != nil {
		t.section = section{Available: false, Reason: candidateReason(candErr)}
		return t
	}
	cacheKey := s.tenantID + "|" + s.siteID + "|traffic|" + win.Range + "|" + win.End.UTC().Format(time.RFC3339)
	if v, ok := overviewCache.get(cacheKey, trafficTTL[win.Range]); ok {
		return v.(ovTraffic)
	}

	attr := map[string]trafficAttribution{}
	ids := make([]string, 0, len(cands))
	for _, c := range cands {
		if !c.HasBytes {
			continue
		}
		attr[c.ID] = trafficAttribution{Interface: c.Interface, PackageID: c.PackageID}
		ids = append(ids, c.ID)
	}

	var rowsOut []sampleBucketRow
	for i := 0; i < len(ids); i += overviewSampleBatch {
		batch := ids[i:min(i+overviewSampleBatch, len(ids))]
		err := s.boundedRead(ctx, overviewStatementTimeout, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT floor(extract(epoch FROM (ar.sampled_at - $2::timestamptz)) / $4)::int,
				       ar.session_id::text,
				       COALESCE(sum(ar.bytes_down), 0)::bigint, COALESCE(sum(ar.bytes_up), 0)::bigint
				  FROM iam_v2.accounting_records ar
				 WHERE ar.session_id = ANY($1::uuid[])
				   AND ar.tenant_id = $5 AND ar.site_id = $6
				   AND ar.sampled_at >= $2 AND ar.sampled_at < $3
				 GROUP BY 1, 2`,
				batch, win.Start, win.End, win.Bucket.Seconds(), s.tenantID, s.siteID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var r sampleBucketRow
				if err := rows.Scan(&r.Bucket, &r.SessionID, &r.Down, &r.Up); err != nil {
					return err
				}
				rowsOut = append(rowsOut, r)
			}
			return rows.Err()
		})
		if err != nil {
			slog.Warn("overview: traffic unavailable", "range", win.Range, "err", err)
			reason := "samples_unreadable"
			if isStatementTimeout(err) {
				reason = "range_too_large_for_time_budget"
			}
			t.section = section{Available: false, Reason: reason}
			return t
		}
	}

	roll := rollupTraffic(win, rowsOut, attr)
	t.BytesDown, t.BytesUp = roll.Down, roll.Up
	t.SeriesDown, t.SeriesUp = roll.SeriesDown, roll.SeriesUp
	t.Sessions = roll.SessionsWithData
	t.byIface = roll.ByInterface
	for iface, v := range roll.ByInterface {
		t.TopNetworks = append(t.TopNetworks, ovTrafficRow{Key: iface, BytesDown: v[0], BytesUp: v[1]})
	}
	names := s.packageNames(ctx)
	for pkg, v := range roll.ByPackage {
		t.TopPackages = append(t.TopPackages, ovTrafficRow{Key: pkg, Name: names[pkg], BytesDown: v[0], BytesUp: v[1]})
	}
	sortTraffic(t.TopNetworks)
	sortTraffic(t.TopPackages)
	t.TopNetworks = t.TopNetworks[:min(len(t.TopNetworks), 8)]
	t.TopPackages = t.TopPackages[:min(len(t.TopPackages), 8)]
	t.ComputedAt = time.Now().UTC()
	t.section = section{Available: true}
	overviewCache.put(cacheKey, t)
	return t
}

func sortTraffic(rows []ovTrafficRow) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i].BytesDown+rows[i].BytesUp, rows[j].BytesDown+rows[j].BytesUp
		if a == b {
			return rows[i].Key < rows[j].Key
		}
		return a > b
	})
}

func (s *server) packageNames(ctx context.Context) map[string]string {
	out := map[string]string{}
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, COALESCE(NULLIF(cur.display->>'name',''), p.code)
		  FROM iam_v2.internet_packages p
		  LEFT JOIN iam_v2.internet_package_revisions cur ON cur.id = p.current_revision_id
		 WHERE p.tenant_id = $1 AND p.site_id = $2
		 LIMIT 1000`, s.tenantID, s.siteID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil {
			out[id] = name
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------------------------
// sign-in outcomes
// ---------------------------------------------------------------------------------------------------------

// overviewSignIn reads the room sign-in attempt ledger. It records ROOM (PMS) attempts only, with a thirty-day
// retention — which is why no range here exceeds thirty days. Successful sign-ins for every method are counted
// from sessions in the guests block; FAILED voucher, account and one-time-code attempts are recorded nowhere on
// this appliance, and the payload says so instead of implying there were none.
func (s *server) overviewSignIn(ctx context.Context, win overviewWindow) ovSignIn {
	o := ovSignIn{
		ByResult: []ovResultRow{}, SeriesVerified: make([]int64, win.N), SeriesFailed: make([]int64, win.N),
		Covered:     "Room sign-in attempts (room number and guest details checked against the PMS guest list).",
		NotRecorded: []string{"VOUCHER failures", "ACCOUNT failures", "OTP failures"},
	}
	rows, err := s.db.Query(ctx, `
		SELECT floor(extract(epoch FROM (occurred_at - $3::timestamptz)) / $5)::int, result, count(*)
		  FROM iam_v2.sign_in_attempts
		 WHERE tenant_id = $1 AND site_id = $2 AND occurred_at >= $3 AND occurred_at < $4
		 GROUP BY 1, 2`, s.tenantID, s.siteID, win.Start, win.End, win.Bucket.Seconds())
	if err != nil {
		o.section = section{Available: false, Reason: "sign_in_attempts_unreadable"}
		return o
	}
	by := map[string]int64{}
	for rows.Next() {
		var b int
		var res string
		var n int64
		if rows.Scan(&b, &res, &n) != nil {
			continue
		}
		by[res] += n
		o.Total += n
		if b >= 0 && b < win.N {
			if res == "VERIFIED" {
				o.SeriesVerified[b] += n
			} else {
				o.SeriesFailed[b] += n
			}
		}
	}
	rows.Close()
	o.Verified = by["VERIFIED"]
	for res, n := range by {
		o.ByResult = append(o.ByResult, ovResultRow{Result: res, Count: n})
	}
	sort.Slice(o.ByResult, func(i, j int) bool {
		if o.ByResult[i].Count == o.ByResult[j].Count {
			return o.ByResult[i].Result < o.ByResult[j].Result
		}
		return o.ByResult[i].Count > o.ByResult[j].Count
	})

	var p50, p95 *float64
	if err := s.db.QueryRow(ctx, `
		SELECT count(latency_ms),
		       percentile_cont(0.5)  WITHIN GROUP (ORDER BY latency_ms),
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)
		  FROM iam_v2.sign_in_attempts
		 WHERE tenant_id = $1 AND site_id = $2 AND occurred_at >= $3 AND occurred_at < $4
		   AND latency_ms IS NOT NULL`, s.tenantID, s.siteID, win.Start, win.End).
		Scan(&o.Latency.Samples, &p50, &p95); err == nil {
		o.Latency.P50ms, o.Latency.P95ms = p50, p95
	}
	o.section = section{Available: true}
	return o
}

// ---------------------------------------------------------------------------------------------------------
// packages
// ---------------------------------------------------------------------------------------------------------

func (s *server) overviewPackages(ctx context.Context, win overviewWindow) ovPackages {
	p := ovPackages{ActiveNow: []ovPackageRow{}, Grants: []ovPackageRow{}, Terminations: []ovReasonRow{}}
	byPackage := func(filter string, args ...any) ([]ovPackageRow, error) {
		rows, err := s.db.Query(ctx, `
			SELECT COALESCE(r.package_id::text, ''),
			       COALESCE(NULLIF(cur.display->>'name',''), NULLIF(r.display->>'name',''), pk.code, ''),
			       COALESCE(pk.code, ''), count(*)
			  FROM iam_v2.entitlements e
			  LEFT JOIN iam_v2.internet_package_revisions r
			         ON r.tenant_id = e.tenant_id AND r.site_id = e.site_id AND r.id = e.package_revision_id
			  LEFT JOIN iam_v2.internet_packages pk ON pk.id = r.package_id
			  LEFT JOIN iam_v2.internet_package_revisions cur ON cur.id = pk.current_revision_id
			 WHERE e.tenant_id = $1 AND e.site_id = $2 AND `+filter+`
			 GROUP BY 1, 2, 3
			 ORDER BY 4 DESC, 2
			 LIMIT 10`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []ovPackageRow{}
		for rows.Next() {
			var r ovPackageRow
			if err := rows.Scan(&r.PackageID, &r.Name, &r.Code, &r.Count); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	var err error
	if p.ActiveNow, err = byPackage(`e.status = 'ACTIVE'`, s.tenantID, s.siteID); err != nil {
		slog.Warn("overview: packages unavailable", "err", err)
		p.section = section{Available: false, Reason: "entitlements_unreadable"}
		p.ActiveNow = []ovPackageRow{}
		return p
	}
	if g, err := byPackage(`e.activated_at >= $3 AND e.activated_at < $4`, s.tenantID, s.siteID, win.Start, win.End); err == nil {
		p.Grants = g
	}
	if rows, err := s.db.Query(ctx, `
		SELECT COALESCE(NULLIF(terminal_reason, ''), 'UNSPECIFIED'), count(*)
		  FROM iam_v2.entitlements
		 WHERE tenant_id = $1 AND site_id = $2 AND terminated_at >= $3 AND terminated_at < $4
		 GROUP BY 1 ORDER BY 2 DESC LIMIT 12`, s.tenantID, s.siteID, win.Start, win.End); err == nil {
		for rows.Next() {
			var r ovReasonRow
			if rows.Scan(&r.Reason, &r.Count) == nil {
				p.Terminations = append(p.Terminations, r)
			}
		}
		rows.Close()
	}
	p.section = section{Available: true}
	return p
}

// ---------------------------------------------------------------------------------------------------------
// PMS — the dashboard's own derivation, called, not repeated
// ---------------------------------------------------------------------------------------------------------

func (s *server) overviewPMS(ctx context.Context) ovPMS {
	p := ovPMS{dashPms: s.dashPMS(ctx), Occupancy: s.dashOccupancy(ctx), Postings: s.dashPostings(ctx)}
	for _, i := range p.Interfaces {
		if i.Lifecycle == "ACTIVE" {
			p.ActiveInterfaces++
			if i.RoomAuthReady {
				p.ReadyInterfaces++
			}
		}
	}
	return p
}

// ---------------------------------------------------------------------------------------------------------
// networks, DHCP, DNS
// ---------------------------------------------------------------------------------------------------------

type ovNetRow struct {
	ovNetwork
	subnet *net.IPNet
}

type ovPoolRow struct {
	NetworkID  string
	Start, End string
}

func (s *server) overviewNetworkRows(ctx context.Context) ([]ovNetRow, []ovPoolRow) {
	rows, err := s.db.Query(ctx, `
		SELECT gn.id::text, COALESCE(gn.name,''), gn.enabled, gn.vlan_id,
		       COALESCE(gn.subnet_cidr::text,''), COALESCE(gn.bridge_name,''),
		       COALESCE(gn.dhcp_mode,''), COALESCE(gn.dns_mode,''), COALESCE(gn.dns_servers::text,'[]'),
		       gn.captive_portal_enabled, gn.internet_access_enabled,
		       COALESCE((SELECT count(DISTINCT se.mac) FROM iam_v2.sessions se
		                  WHERE se.tenant_id = gn.tenant_id AND se.site_id = gn.site_id
		                    AND se.state = 'active'
		                    AND se.ingress_interface = gn.bridge_name
		                    AND NULLIF(gn.bridge_name,'') IS NOT NULL), 0)::bigint
		  FROM public.guest_networks gn
		 WHERE gn.tenant_id = $1 AND gn.site_id = $2
		 ORDER BY gn.enabled DESC, gn.name
		 LIMIT 200`, s.tenantID, s.siteID)
	if err != nil {
		slog.Warn("overview: guest networks unavailable", "err", err)
		return nil, nil
	}
	nets := []ovNetRow{}
	for rows.Next() {
		var n ovNetRow
		var dns string
		if err := rows.Scan(&n.ID, &n.Name, &n.Enabled, &n.VlanID, &n.SubnetCIDR, &n.Bridge, &n.DhcpMode,
			&n.DNSMode, &dns, &n.CaptivePortal, &n.Internet, &n.DevicesOnline); err != nil {
			slog.Warn("overview: skipped a guest-network row", "err", err)
			continue
		}
		n.DNSServers = decodeServers(dns)
		if _, cidr, err := net.ParseCIDR(n.SubnetCIDR); err == nil {
			n.subnet = cidr
		}
		nets = append(nets, n)
	}
	rows.Close()

	pools := []ovPoolRow{}
	prow, err := s.db.Query(ctx, `
		SELECT p.guest_network_id::text, host(p.start_ip), host(p.end_ip)
		  FROM public.dhcp_pools p
		  JOIN public.guest_networks gn ON gn.id = p.guest_network_id
		 WHERE gn.tenant_id = $1 AND gn.site_id = $2
		 ORDER BY p.sort_order, p.start_ip
		 LIMIT 2000`, s.tenantID, s.siteID)
	if err == nil {
		for prow.Next() {
			var p ovPoolRow
			if prow.Scan(&p.NetworkID, &p.Start, &p.End) == nil {
				pools = append(pools, p)
			}
		}
		prow.Close()
	}
	return nets, pools
}

func decodeServers(raw string) []string {
	out := []string{}
	var list []any
	if json.Unmarshal([]byte(raw), &list) != nil {
		return out
	}
	for _, v := range list {
		switch x := v.(type) {
		case string:
			if strings.TrimSpace(x) != "" {
				out = append(out, strings.TrimSpace(x))
			}
		case map[string]any:
			if a, ok := x["address"].(string); ok && a != "" {
				out = append(out, a)
			}
		}
	}
	return out
}

func (s *server) overviewLeases(ctx context.Context) ([]keaLeaseRow, string) {
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	code, body, err := s.netd.call(lctx, http.MethodGet, "/v1/leases", nil)
	if err != nil {
		return nil, "network_controller_unreachable"
	}
	if code != http.StatusOK {
		return nil, "dhcp_leases_unreadable"
	}
	var v struct {
		Leases []keaLeaseRow `json:"leases"`
	}
	if json.Unmarshal(body, &v) != nil {
		return nil, "dhcp_leases_unreadable"
	}
	return v.Leases, ""
}

// leaseExpiringWithin is the display definition of "expiring soon" on the overview: nothing acts on it.
const leaseExpiringWithin = 10 * time.Minute

// assembleNetworkBlocks joins configuration, leases and traffic into the networks, DHCP and DNS blocks. It is a
// pure function of its inputs so the permission rule — no lease or configuration detail for a role without
// `network` — is testable without a database.
func assembleNetworkBlocks(nets []ovNetRow, pools []ovPoolRow, traffic ovTraffic, leases []keaLeaseRow,
	leasesErr string, kea keaView, keaReachable, canNetwork bool, now time.Time) (ovNetworks, ovDHCP, ovDNS) {

	nb := ovNetworks{Rows: []ovNetwork{}}
	dh := ovDHCP{Pools: []ovPool{}, CaptiveOptionNetworks: []string{},
		ExpiringWithinSeconds: int64(leaseExpiringWithin / time.Second)}
	dn := ovDNS{Networks: []ovDNSNetwork{}, ResolverState: "unknown"}

	if nets == nil {
		nb.section = section{Available: false, Reason: "guest_networks_unreadable"}
		dh.section, dn.section = nb.section, nb.section
		if !canNetwork {
			dh.section = section{Available: false, Reason: reasonNotPermitted}
			dn.section = dh.section
		}
		return nb, dh, dn
	}

	byNet := map[string][]ipv4Range{}
	invalid := map[string]int{}
	for _, p := range pools {
		r, ok := newIPv4Range(p.Start, p.End)
		if !ok {
			invalid[p.NetworkID]++
			continue
		}
		byNet[p.NetworkID] = append(byNet[p.NetworkID], r)
	}

	trafficByIface := map[string][2]int64{}
	if traffic.Available {
		for k, v := range traffic.byIface {
			trafficByIface[k] = v
		}
	}

	leaseNets := make([]leaseNetwork, 0, len(nets))
	for _, n := range nets {
		if n.DhcpMode == "local" {
			leaseNets = append(leaseNets, leaseNetwork{ID: n.ID, Subnet: n.subnet, Pools: byNet[n.ID]})
		}
	}
	leaseCounts := map[string]*leaseNetworkCount{}
	leasesOK := canNetwork && leasesErr == ""
	if leasesOK {
		var total, unmatched int64
		leaseCounts, total, unmatched = aggregateLeases(leases, leaseNets, now, leaseExpiringWithin)
		dh.ActiveLeases, dh.UnmatchedLeases = total, unmatched
	}

	for _, n := range nets {
		row := n.ovNetwork
		for _, r := range byNet[n.ID] {
			row.PoolSize += r.Size
		}
		row.InvalidPools = invalid[n.ID]
		if n.Bridge != "" {
			if v, ok := trafficByIface[n.Bridge]; ok {
				row.BytesDown, row.BytesUp = v[0], v[1]
			}
			row.TrafficKnown = traffic.Available
		}
		if !canNetwork {
			// The configuration detail this role may not read is withheld; what remains is what the
			// dashboard's networks list has always shown under `reports`.
			row.DNSServers = []string{}
		} else if c, ok := leaseCounts[n.ID]; ok && leasesOK {
			a, in, soon := c.Active, c.InPools, c.ExpiringSoon
			row.LeasesKnown = true
			row.ActiveLeases, row.LeasesInPool, row.ExpiringSoon = &a, &in, &soon
			row.Utilisation = utilisationPct(in, row.PoolSize)
			dh.ExpiringSoon += soon
			for _, pc := range c.Pools {
				dh.Pools = append(dh.Pools, ovPool{Network: n.Name, Start: pc.Start, End: pc.End, Size: pc.Size,
					Active: pc.Active, Pct: utilisationPct(pc.Active, pc.Size)})
			}
		}
		if row.DNSServers == nil {
			row.DNSServers = []string{}
		}
		nb.Rows = append(nb.Rows, row)

		if canNetwork && n.Enabled {
			if n.DhcpMode == "local" {
				dh.LocalNetworks++
				// Option 114 (RFC 8910) is rendered into a local network's Kea subnet exactly when the captive
				// portal is enabled on it (netcfg.RenderKeaDhcp4).
				if n.CaptivePortal {
					dh.CaptiveOptionNetworks = append(dh.CaptiveOptionNetworks, n.Name)
				}
			}
			dn.Networks = append(dn.Networks, ovDNSNetwork{Network: n.Name, Mode: n.DNSMode, Servers: row.DNSServers})
		}
	}
	nb.section = section{Available: true}
	if !canNetwork {
		nb.LeasesReason = reasonNotPermitted
	} else if leasesErr != "" {
		nb.LeasesReason = leasesErr
	}

	if !canNetwork {
		dh.section = section{Available: false, Reason: reasonNotPermitted}
		dn.section = dh.section
		return nb, dh, dn
	}
	dh.section = section{Available: true}
	dh.LeasesAvailable = leasesOK
	dh.LeasesReason = leasesErr
	if keaReachable {
		c, h := kea.KeaConfigured, kea.KeaHealthy
		dh.ServerConfigured, dh.ServerHealthy, dh.ServerDetail = &c, &h, sanitizeDetail(kea.KeaDetail)
	} else {
		dh.ServerDetail = "The network controller could not be asked about the DHCP server."
	}
	dn.section = section{Available: true}
	return nb, dh, dn
}

// withResolverHealth fills the resolver state from the persisted health record — the one the health
// supervisor writes after its own 127.0.0.1:53 round trip. It deliberately runs no probe of its own.
func (s *server) withResolverHealth(ctx context.Context, dn ovDNS) ovDNS {
	if !dn.Available {
		return dn
	}
	for _, h := range s.allHealth(ctx) {
		if h.Service == "unbound" {
			dn.ResolverState = h.State
			dn.ResolverHealthy = h.HealthOK
			dn.ResolverDetail = h.HealthDetail
			dn.LastHealthyAt = h.LastHealthyAt
		}
	}
	return dn
}

// ---------------------------------------------------------------------------------------------------------
// services, appliance
// ---------------------------------------------------------------------------------------------------------

func (s *server) overviewServices(ctx context.Context, win overviewWindow) ovServices {
	out := ovServices{Services: []ovService{}, Counts: map[string]int{}}
	all := s.allHealth(ctx)
	if len(all) == 0 {
		out.section = section{Available: false, Reason: "no_health_records_yet"}
		return out
	}
	out.Overall, out.Counts = overallHealth(all)

	type counts struct{ failures, restarts int64 }
	inRange := map[string]counts{}
	if rows, err := s.db.Query(ctx, `
		SELECT service,
		       count(*) FILTER (WHERE event = 'failure_detected'),
		       count(*) FILTER (WHERE event = 'manual_restart')
		  FROM appliance_recovery_events
		 WHERE created_at >= $1 AND created_at < $2
		 GROUP BY service
		 LIMIT 100`, win.Start, win.End); err == nil {
		for rows.Next() {
			var name string
			var c counts
			if rows.Scan(&name, &c.failures, &c.restarts) == nil {
				inRange[name] = c
			}
		}
		rows.Close()
	}
	for _, h := range all {
		c := inRange[h.Service]
		out.Services = append(out.Services, ovService{
			Name: h.Service, Label: serviceLabel(h.Service), State: h.State, HealthOK: h.HealthOK,
			Critical: h.Critical, Detail: h.HealthDetail, RestartCount: h.RestartCount,
			LastHealthyAt: h.LastHealthyAt, FailuresInRange: c.failures, ManualRestarts: c.restarts,
		})
	}
	out.section = section{Available: true}
	return out
}

func (s *server) overviewLicense(ctx context.Context) ovLicense {
	lctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	code, raw, err := s.scd.call(lctx, http.MethodGet, "/v1/license/status", nil)
	if err != nil || code != http.StatusOK {
		return ovLicense{section: section{Available: false, Reason: "session_controller_unreachable"}}
	}
	var v struct {
		State      string  `json:"state"`
		Installed  bool    `json:"installed"`
		LicenseID  string  `json:"license_id"`
		ValidUntil string  `json:"valid_until"`
		GraceUntil *string `json:"grace_until"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ovLicense{section: section{Available: false, Reason: "license_unreadable"}}
	}
	// A real signed licence only: the permissive commissioning mode reports "Active" with no licence id, and
	// that must not read as licensed (the same rule the /health summary applies).
	l := ovLicense{section: section{Available: true}, State: v.State, Installed: v.Installed && v.LicenseID != "",
		ValidUntil: v.ValidUntil}
	if v.GraceUntil != nil {
		l.GraceUntil = *v.GraceUntil
	}
	return l
}

// overviewSystemNetwork reads the WAN/LAN state from netd. That read includes a gateway and an internet
// reachability probe, so it is cached for a minute: an overview left open must not ping out every thirty
// seconds on its behalf.
func (s *server) overviewSystemNetwork(ctx context.Context) ovNetworkConfig {
	key := "sysnet"
	if v, ok := overviewCache.get(key, time.Minute); ok {
		return v.(ovNetworkConfig)
	}
	nctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	code, raw, err := s.netd.call(nctx, http.MethodGet, "/v1/system-network", nil)
	if err != nil || code != http.StatusOK {
		return ovNetworkConfig{section: section{Available: false, Reason: "network_controller_unreachable"}}
	}
	var v struct {
		WAN struct {
			Interface    string `json:"interface"`
			Mode         string `json:"mode"`
			IP           string `json:"ip"`
			PrefixLen    int    `json:"prefix_len"`
			Gateway      string `json:"gateway"`
			LinkUp       bool   `json:"link_up"`
			Connectivity struct {
				GatewayReachable bool `json:"gateway_reachable"`
				InternetOK       bool `json:"internet_ok"`
			} `json:"connectivity"`
		} `json:"wan"`
		LAN struct {
			Bridge    string `json:"bridge"`
			IP        string `json:"ip"`
			PrefixLen int    `json:"prefix_len"`
			LinkUp    bool   `json:"link_up"`
		} `json:"lan"`
		Pending *json.RawMessage `json:"pending"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ovNetworkConfig{section: section{Available: false, Reason: "network_state_unreadable"}}
	}
	gw, inet := v.WAN.Connectivity.GatewayReachable, v.WAN.Connectivity.InternetOK
	out := ovNetworkConfig{
		section:      section{Available: true},
		WANInterface: v.WAN.Interface, WANMode: v.WAN.Mode, WANAddress: withPrefix(v.WAN.IP, v.WAN.PrefixLen),
		WANGateway: v.WAN.Gateway, WANLinkUp: v.WAN.LinkUp, GatewayReachable: &gw, InternetReachable: &inet,
		LANBridge: v.LAN.Bridge, LANAddress: withPrefix(v.LAN.IP, v.LAN.PrefixLen), LANLinkUp: v.LAN.LinkUp,
		SystemPending: v.Pending != nil && string(*v.Pending) != "null",
		CheckedAt:     time.Now().UTC(),
	}
	if v.WAN.Gateway == "" {
		out.GatewayReachable = nil
	}
	overviewCache.put(key, out)
	return out
}

func withPrefix(ip string, plen int) string {
	if ip == "" {
		return ""
	}
	if plen <= 0 {
		return ip
	}
	return ip + "/" + strconv.Itoa(plen)
}

func (s *server) overviewRevisions(ctx context.Context) ovRevisions {
	out := ovRevisions{}
	read := func(where string) (*ovRevision, error) {
		var r ovRevision
		err := s.db.QueryRow(ctx, `
			SELECT seq, state, applied_at, confirmed_at, confirm_deadline
			  FROM network_config_revisions WHERE `+where+` ORDER BY seq DESC LIMIT 1`).
			Scan(&r.Seq, &r.State, &r.AppliedAt, &r.ConfirmedAt, &r.ConfirmDeadline)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &r, nil
	}
	var err error
	if out.LatestActive, err = read(`state = 'active'`); err != nil {
		out.section = section{Available: false, Reason: "revisions_unreadable"}
		return out
	}
	out.Pending, _ = read(`state = 'pending_confirmation'`)
	out.section = section{Available: true}
	return out
}

// attentionInputFrom reads only blocks the caller received, so an item can never reveal a fact from a block
// the caller's role was refused.
func attentionInputFrom(o overviewResp) attentionInput {
	in := attentionInput{}
	if o.PMS.Available {
		for _, i := range o.PMS.Interfaces {
			in.PMS = append(in.PMS, attentionPMS{Label: i.Label, Active: i.Lifecycle == "ACTIVE",
				RoomAuthReady: i.RoomAuthReady, Connected: i.Transport == "CONNECTED", Reason: i.RoomAuthReason})
		}
		in.PMSReviewCases = o.PMS.EventsReview
	}
	if o.PMS.Postings.Available {
		in.PostingsReviewOpen = o.PMS.Postings.ReviewOpen
	}
	if o.Services.Available {
		for _, sv := range o.Services.Services {
			in.Services = append(in.Services, attentionService{Name: sv.Name, State: sv.State, Critical: sv.Critical})
		}
	}
	if o.DHCP.Available && o.DHCP.ServerConfigured != nil && o.DHCP.ServerHealthy != nil {
		in.KeaKnown, in.KeaConfigured, in.KeaHealthy = true, *o.DHCP.ServerConfigured, *o.DHCP.ServerHealthy
		// The service list already raises Kea when its supervisor record is unhealthy; do not say it twice.
		for _, sv := range in.Services {
			if sv.Name == "kea" && sv.Critical && sv.State != stHealthy && sv.State != stWaiting {
				in.KeaKnown = false
			}
		}
	}
	if o.DHCP.Available {
		for _, n := range o.Networks.Rows {
			if n.Enabled && n.Utilisation != nil {
				in.Pools = append(in.Pools, attentionPool{Network: n.Name, Pct: *n.Utilisation})
			}
		}
	}
	if o.Appliance.Resources.Available {
		for _, d := range o.Appliance.Resources.Disks {
			if d.SameFilesystemAs == "" {
				in.Disks = append(in.Disks, attentionDisk{Path: d.Path, Pct: d.UsedPct})
			}
		}
	}
	if o.Appliance.License.Available {
		in.LicenseKnown, in.LicenseInstalled, in.LicenseState = true, o.Appliance.License.Installed, o.Appliance.License.State
	}
	if o.Appliance.Revisions.Available && o.Appliance.Revisions.Pending != nil {
		in.RevisionPending, in.RevisionSeq = true, o.Appliance.Revisions.Pending.Seq
	}
	if o.Appliance.Network.Available {
		in.SystemNetPending = o.Appliance.Network.SystemPending
	}
	return in
}

// nameTrafficNetworks labels each traffic row with the guest network whose bridge carried it. A session with
// no recorded ingress bridge keeps an empty name, and the UI says it could not be attributed.
func nameTrafficNetworks(rows []ovTrafficRow, nets []ovNetRow) []ovTrafficRow {
	byBridge := map[string]string{}
	for _, n := range nets {
		if n.Bridge != "" {
			byBridge[n.Bridge] = n.Name
		}
	}
	out := make([]ovTrafficRow, len(rows))
	for i, r := range rows {
		r.Name = byBridge[r.Key]
		out[i] = r
	}
	return out
}

func twoDigit(i int) string {
	if i < 10 {
		return "0" + strconv.Itoa(i)
	}
	return strconv.Itoa(i)
}
