package main

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

var cairo = time.FixedZone("EET", 2*3600)

func mustWindow(t *testing.T, key string, now time.Time) overviewWindow {
	t.Helper()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	w, err := computeWindow(key, now, day)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// ------------------------------------------------------------------------------------------------ window --

func TestWindowsAreAlignedToLocalBoundariesAndEndWithTheCurrentBucket(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 37, 12, 0, cairo)

	w := mustWindow(t, "24h", now)
	if want := time.Date(2026, 9, 24, 15, 0, 0, 0, cairo); !w.End.Equal(want) {
		t.Errorf("24h end = %v, want %v (the bucket containing now)", w.End, want)
	}
	if w.N != 24 || w.Bucket != time.Hour || !w.Start.Equal(w.End.Add(-24*time.Hour)) {
		t.Errorf("24h window = %+v", w)
	}

	w = mustWindow(t, "7d", now)
	if want := time.Date(2026, 9, 24, 15, 0, 0, 0, cairo); !w.End.Equal(want) {
		t.Errorf("7d end = %v, want %v (3h buckets from local midnight: 12:00-15:00 holds 14:37)", w.End, want)
	}
	if w.N != 56 || w.End.Sub(w.Start) != 7*24*time.Hour {
		t.Errorf("7d window = %+v", w)
	}

	w = mustWindow(t, "30d", now)
	if want := time.Date(2026, 9, 25, 0, 0, 0, 0, cairo); !w.End.Equal(want) {
		t.Errorf("30d end = %v, want the end of today %v", w.End, want)
	}
	if w.N != 30 || w.End.Sub(w.Start) != 30*24*time.Hour {
		t.Errorf("30d window = %+v", w)
	}

	if _, err := computeWindow("90d", now, now); err == nil {
		t.Error("an unknown range was accepted; the window must stay bounded to the three offered")
	}
}

func TestNoRangeExceedsThirtyDays(t *testing.T) {
	for k, r := range overviewRanges {
		if d := time.Duration(r.Buckets) * r.Bucket; d > overviewMaxWindow {
			t.Errorf("range %s spans %v, beyond the %v the sign-in ledger retains and the samples can afford", k, d, overviewMaxWindow)
		}
	}
}

func TestBucketOfIsHalfOpen(t *testing.T) {
	w := mustWindow(t, "24h", time.Date(2026, 9, 24, 14, 37, 0, 0, cairo))
	if w.bucketOf(w.Start) != 0 {
		t.Error("the window start belongs to the first bucket")
	}
	if w.bucketOf(w.Start.Add(time.Hour)) != 1 {
		t.Error("a bucket boundary belongs to the later bucket")
	}
	if w.bucketOf(w.End) != -1 || w.bucketOf(w.Start.Add(-time.Nanosecond)) != -1 {
		t.Error("instants outside the window must not be placed in a bucket")
	}
}

// ------------------------------------------------------------------------------------- delta vs cumulative --

func TestSamplesAreDeltasAndABucketIsTheirSum(t *testing.T) {
	w := mustWindow(t, "24h", time.Date(2026, 9, 24, 14, 37, 0, 0, cairo))
	b0 := w.Start
	times := []time.Time{b0.Add(10 * time.Second), b0.Add(20 * time.Second), b0.Add(time.Hour + time.Second)}
	vals := []int64{100, 50, 7}

	got := perBucketBytes(w, times, vals, counterDelta)
	if got[0] != 150 || got[1] != 7 {
		t.Fatalf("delta buckets = %v, want [150 7 ...]: each sample is the bytes since the previous one", got[:2])
	}

	// The same rows read as if they were cumulative would difference them — and report a reset. This is the
	// mistake the overview must not make with this table, pinned so the difference is visible.
	cum := perBucketBytes(w, times, vals, counterCumulative)
	if cum[0] == got[0] {
		t.Fatal("cumulative and delta readings agree on data where they must not; the test proves nothing")
	}
}

func TestCumulativeCountersAreDifferencedWithResets(t *testing.T) {
	w := mustWindow(t, "24h", time.Date(2026, 9, 24, 14, 37, 0, 0, cairo))
	b0 := w.Start
	times := []time.Time{b0.Add(time.Second), b0.Add(2 * time.Second), b0.Add(time.Hour), b0.Add(time.Hour + time.Second)}
	// 1000 -> 1600 (+600) -> 1900 (+300, next bucket) -> reset to 40 (+40)
	vals := []int64{1000, 1600, 1900, 40}
	got := perBucketBytes(w, times, vals, counterCumulative)
	if got[0] != 600 {
		t.Errorf("bucket 0 = %d, want 600: the first reading carries history from before the window and adds nothing", got[0])
	}
	if got[1] != 340 {
		t.Errorf("bucket 1 = %d, want 340: +300, then a reset whose new reading (40) is the bytes since it", got[1])
	}
}

func TestTheSampleQuerySumsRowsRatherThanDifferencingThem(t *testing.T) {
	// The encoding decision lives in SQL. If someone "fixes" it to lag()/difference the rows, every bucket goes
	// wrong; this holds the query to the delta reading established from the writer.
	src, err := os.ReadFile("resources_overview.go")
	if err != nil {
		t.Fatal(err)
	}
	s := stripComments(string(src))
	if !strings.Contains(s, "sum(ar.bytes_down)") || !strings.Contains(s, "sum(ar.bytes_up)") {
		t.Error("the sample query no longer sums the rows")
	}
	if strings.Contains(s, "lag(ar.bytes") {
		t.Error("the sample query differences the rows; accounting_records holds deltas, not counters")
	}
	// And it must be index-led: session_id = ANY, never a bare time predicate.
	if !strings.Contains(s, "ar.session_id = ANY($1::uuid[])") {
		t.Error("the sample query is no longer led by session_id = ANY; it would scan the whole table")
	}
	if !strings.Contains(s, "SET LOCAL statement_timeout") {
		t.Error("the sample query runs without a statement timeout")
	}
}

func TestRollupAttributesTrafficToNetworksAndPackages(t *testing.T) {
	w := mustWindow(t, "24h", time.Date(2026, 9, 24, 14, 37, 0, 0, cairo))
	rows := []sampleBucketRow{
		{Bucket: 0, SessionID: "a", Down: 100, Up: 10},
		{Bucket: 3, SessionID: "a", Down: 50, Up: 5},
		{Bucket: 3, SessionID: "b", Down: 1000, Up: 0},
		{Bucket: 99, SessionID: "b", Down: 1 << 40}, // outside the window: ignored, never clamped in
	}
	attr := map[string]trafficAttribution{
		"a": {Interface: "br-guest", PackageID: "p1"},
		"b": {Interface: "br-staff", PackageID: "p2"},
	}
	r := rollupTraffic(w, rows, attr)
	if r.Down != 1150 || r.Up != 15 {
		t.Fatalf("totals = %d/%d, want 1150/15", r.Down, r.Up)
	}
	if r.SeriesDown[3] != 1050 || r.SeriesDown[0] != 100 {
		t.Errorf("series = %v", r.SeriesDown[:4])
	}
	if r.ByInterface["br-guest"] != [2]int64{150, 15} || r.ByPackage["p2"] != [2]int64{1000, 0} {
		t.Errorf("attribution = %v / %v", r.ByInterface, r.ByPackage)
	}
	if r.SessionsWithData != 2 {
		t.Errorf("sessions with data = %d, want 2", r.SessionsWithData)
	}
}

// ------------------------------------------------------------------------------------------ concurrency --

func tp(t time.Time) *time.Time { return &t }

func TestConcurrencyPeakAndAverageAreExact(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 30, 0, 0, cairo)
	w := mustWindow(t, "24h", now)
	h := func(i int) time.Time { return w.Start.Add(time.Duration(i) * time.Hour) }

	ivs := []sessionInterval{
		// bucket 0: two sessions overlap for the whole second half hour
		{Start: h(0), End: tp(h(1))},
		{Start: h(0).Add(30 * time.Minute), End: tp(h(1))},
		// bucket 2: back-to-back, half-open — never on at the same time
		{Start: h(2), End: tp(h(2).Add(30 * time.Minute))},
		{Start: h(2).Add(30 * time.Minute), End: tp(h(3))},
		// started before the window, still open at now
		{Start: w.Start.Add(-48 * time.Hour), End: nil},
	}
	peak, avg := concurrency(w, ivs)

	if peak[0] != 3 {
		t.Errorf("bucket 0 peak = %d, want 3 (two sessions plus the long-running one)", peak[0])
	}
	if avg[0] != 2.5 {
		t.Errorf("bucket 0 average = %v, want 2.5 (1 + 1 + 0.5 session-hours over one hour)", avg[0])
	}
	if peak[2] != 2 {
		t.Errorf("bucket 2 peak = %d, want 2: back-to-back sessions must not count as overlapping", peak[2])
	}
	if avg[2] != 2 {
		t.Errorf("bucket 2 average = %v, want 2", avg[2])
	}
	if peak[5] != 1 || avg[5] != 1 {
		t.Errorf("an idle bucket carries only the open session: peak %d avg %v", peak[5], avg[5])
	}
	// The current bucket is partial: averaging over its elapsed half hour, not the full hour.
	last := w.N - 1
	if avg[last] != 1 {
		t.Errorf("current partial bucket average = %v, want 1 (divided by elapsed time, not the nominal hour)", avg[last])
	}
}

func TestConcurrencyIgnoresSessionsOutsideTheWindow(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 30, 0, 0, cairo)
	w := mustWindow(t, "24h", now)
	ivs := []sessionInterval{{Start: w.Start.Add(-5 * time.Hour), End: tp(w.Start.Add(-time.Hour))}}
	peak, avg := concurrency(w, ivs)
	for i := range peak {
		if peak[i] != 0 || avg[i] != 0 {
			t.Fatalf("bucket %d = %d/%v for a session that ended before the window", i, peak[i], avg[i])
		}
	}
}

// ------------------------------------------------------------------------------------------ pool sizes --

func TestPoolSizeIsCorrectForAnyIPv4Range(t *testing.T) {
	cases := []struct {
		start, end string
		want       int64
		ok         bool
	}{
		{"192.168.77.100", "192.168.77.200", 101, true},
		{"10.0.0.5", "10.0.0.5", 1, true},
		// The ranges the fourth-octet arithmetic refused: across octet boundaries.
		{"10.20.0.10", "10.20.3.250", 3*256 + 250 - 10 + 1, true},
		{"172.16.0.0", "172.16.255.255", 65536, true},
		{"10.0.255.250", "10.1.0.5", 12, true},
		{"10.0.0.9", "10.0.0.1", 0, false}, // reversed
		{"fe80::1", "fe80::ff", 0, false},  // not IPv4
		{"nonsense", "10.0.0.1", 0, false},
	}
	for _, c := range cases {
		got, ok := ipv4RangeSize(c.start, c.end)
		if got != c.want || ok != c.ok {
			t.Errorf("ipv4RangeSize(%s, %s) = %d,%v; want %d,%v", c.start, c.end, got, ok, c.want, c.ok)
		}
	}
}

// ------------------------------------------------------------------------------------------------ leases --

func TestLeaseAggregationCountsOnlyCurrentLeasesPerNetworkAndPool(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cur := func(ip string, left int64) keaLeaseRow {
		// valid for 3600 s, obtained so that `left` seconds remain
		return keaLeaseRow{IPAddress: ip, State: 0, ValidLft: 3600, CLTT: now.Unix() - 3600 + left}
	}
	_, guest, _ := net.ParseCIDR("10.20.0.0/22")
	_, staff, _ := net.ParseCIDR("192.168.5.0/24")
	pool, _ := newIPv4Range("10.20.0.10", "10.20.3.250")
	nets := []leaseNetwork{
		{ID: "g", Subnet: guest, Pools: []ipv4Range{pool}},
		{ID: "s", Subnet: staff},
	}
	leases := []keaLeaseRow{
		cur("10.20.0.10", 3000),
		cur("10.20.2.1", 120),  // expiring within 10 minutes
		cur("10.20.0.2", 3000), // a reservation outside the pool: counted as a lease, not as pool usage
		{IPAddress: "10.20.0.11", State: 2, ValidLft: 3600, CLTT: now.Unix()},       // released
		{IPAddress: "10.20.0.12", State: 0, ValidLft: 600, CLTT: now.Unix() - 7200}, // lapsed, still state 0
		cur("192.168.5.20", 3000),
		cur("8.8.8.8", 3000), // matches no configured network
	}
	got, total, unmatched := aggregateLeases(leases, nets, now, 10*time.Minute)
	if total != 5 || unmatched != 1 {
		t.Fatalf("total/unmatched = %d/%d, want 5/1 (the released and lapsed rows are history)", total, unmatched)
	}
	g := got["g"]
	if g.Active != 3 || g.InPools != 2 || g.ExpiringSoon != 1 {
		t.Errorf("guest network = %+v, want active 3, in pool 2, expiring 1", *g)
	}
	if g.Pools[0].Active != 2 || g.Pools[0].Size != 1009 {
		t.Errorf("guest pool = %+v", g.Pools[0])
	}
	if got["s"].Active != 1 {
		t.Errorf("staff network active = %d, want 1", got["s"].Active)
	}
	if p := utilisationPct(g.InPools, g.Pools[0].Size); p == nil || *p != 0.2 {
		t.Errorf("utilisation = %v, want 0.2%%", p)
	}
	if utilisationPct(3, 0) != nil {
		t.Error("a network with no pool must have no utilisation, not 0%")
	}
}

// ------------------------------------------------------------------------------------- network blocks --

func sampleNets() ([]ovNetRow, []ovPoolRow) {
	_, sn, _ := net.ParseCIDR("10.20.0.0/22")
	return []ovNetRow{{
			ovNetwork: ovNetwork{ID: "g", Name: "Guest", Enabled: true, SubnetCIDR: "10.20.0.0/22", Bridge: "br-guest",
				DhcpMode: "local", DNSMode: "custom", DNSServers: []string{"1.1.1.1"}, CaptivePortal: true, Internet: true},
			subnet: sn,
		}},
		[]ovPoolRow{{NetworkID: "g", Start: "10.20.0.10", End: "10.20.0.19"}}
}

func TestNetworkBlocksWithholdLeaseAndDNSDetailWithoutTheNetworkKey(t *testing.T) {
	nets, pools := sampleNets()
	now := time.Unix(1_800_000_000, 0)
	leases := []keaLeaseRow{{IPAddress: "10.20.0.10", ValidLft: 3600, CLTT: now.Unix()}}
	nb, dh, dn := assembleNetworkBlocks(nets, pools, ovTraffic{}, leases, "", keaView{KeaConfigured: true, KeaHealthy: true}, true, false, now)

	if dh.Available || dh.Reason != reasonNotPermitted || dn.Available || dn.Reason != reasonNotPermitted {
		t.Fatalf("DHCP/DNS sent to a role without `network`: %+v / %+v", dh.section, dn.section)
	}
	r := nb.Rows[0]
	if r.ActiveLeases != nil || r.Utilisation != nil || r.LeasesKnown || len(r.DNSServers) != 0 {
		t.Errorf("lease or DNS detail leaked into the networks block: %+v", r)
	}
	if r.PoolSize != 10 {
		t.Errorf("pool size = %d, want 10 (configuration the dashboard already shows under reports)", r.PoolSize)
	}
	if nb.LeasesReason != reasonNotPermitted {
		t.Errorf("leases reason = %q", nb.LeasesReason)
	}
}

func TestNetworkBlocksReportLeasesOption114AndHonestDNS(t *testing.T) {
	nets, pools := sampleNets()
	now := time.Unix(1_800_000_000, 0)
	leases := []keaLeaseRow{{IPAddress: "10.20.0.10", ValidLft: 3600, CLTT: now.Unix()}}
	tr := ovTraffic{section: section{Available: true}, byIface: map[string][2]int64{"br-guest": {900, 100}}}
	nb, dh, dn := assembleNetworkBlocks(nets, pools, tr, leases, "", keaView{KeaConfigured: true, KeaHealthy: true}, true, true, now)

	r := nb.Rows[0]
	if r.ActiveLeases == nil || *r.ActiveLeases != 1 || r.Utilisation == nil || *r.Utilisation != 10 {
		t.Errorf("row = %+v", r)
	}
	if r.BytesDown != 900 || !r.TrafficKnown {
		t.Errorf("per-network traffic = %d (known %v)", r.BytesDown, r.TrafficKnown)
	}
	if len(dh.CaptiveOptionNetworks) != 1 || dh.CaptiveOptionNetworks[0] != "Guest" {
		t.Errorf("option 114 networks = %v", dh.CaptiveOptionNetworks)
	}
	if dh.ServerHealthy == nil || !*dh.ServerHealthy || dh.ActiveLeases != 1 {
		t.Errorf("dhcp = %+v", dh)
	}
	if dn.StatisticsCollected {
		t.Error("DNS query statistics are not collected anywhere; the block must not claim they are")
	}
	if len(dn.Networks) != 1 || dn.Networks[0].Mode != "custom" {
		t.Errorf("dns networks = %+v", dn.Networks)
	}
}

func TestAKeaThatCannotBeAskedIsReportedAsUnknownNotZero(t *testing.T) {
	nets, pools := sampleNets()
	nb, dh, _ := assembleNetworkBlocks(nets, pools, ovTraffic{}, nil, "network_controller_unreachable", keaView{}, false, true, time.Now())
	if dh.LeasesAvailable || dh.LeasesReason != "network_controller_unreachable" || dh.ServerHealthy != nil {
		t.Errorf("dhcp = %+v", dh)
	}
	if nb.Rows[0].ActiveLeases != nil || nb.Rows[0].Utilisation != nil {
		t.Error("lease figures were reported when the leases could not be read; that is a fabricated zero")
	}
}

// ------------------------------------------------------------------------------------------- attention --

func ids(items []attentionItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestAttentionIsEmptyWhenEverythingIsFine(t *testing.T) {
	in := attentionInput{
		PMS:      []attentionPMS{{Label: "Opera", Active: true, RoomAuthReady: true, Connected: true}},
		Services: []attentionService{{Name: "scd", State: stHealthy, Critical: true}, {Name: "kea", State: stWaiting, Critical: true}},
		KeaKnown: true, KeaConfigured: false, KeaHealthy: false,
		Pools:        []attentionPool{{Network: "Guest", Pct: 85}},
		Disks:        []attentionDisk{{Path: "/", Pct: 40}},
		LicenseKnown: true, LicenseInstalled: true, LicenseState: "Active",
	}
	if got := deriveAttention(in); len(got) != 0 {
		t.Fatalf("attention = %v, want none (85%% is the threshold, not over it; a waiting DHCP is not a fault)", ids(got))
	}
}

func TestAttentionRulesEachLinkToTheScreenThatFixesThem(t *testing.T) {
	in := attentionInput{
		PMS: []attentionPMS{
			{Label: "Opera", Active: true, RoomAuthReady: false, Connected: false, Reason: "SYNC_INCOMPLETE"},
			{Label: "Old", Active: false, RoomAuthReady: false},
		},
		PMSReviewCases: 3, PostingsReviewOpen: 1,
		Services: []attentionService{
			{Name: "portald", State: stFailed, Critical: true},
			{Name: "acctd", State: stRecovering, Critical: true},
		},
		KeaKnown: true, KeaConfigured: true, KeaHealthy: false,
		Pools:        []attentionPool{{Network: "Guest", Pct: 91.2}, {Network: "Lobby", Pct: 100}},
		Disks:        []attentionDisk{{Path: "/", Pct: 96}},
		LicenseKnown: true, LicenseInstalled: true, LicenseState: "GracePeriod",
		RevisionPending: true, SystemNetPending: true,
	}
	got := deriveAttention(in)
	want := map[string]string{
		"pms-not-ready:Opera":      "/pms-interfaces",
		"pms-review":               "/stay-events",
		"postings-review":          "/financial-review",
		"service:portald":          "/health",
		"service:acctd":            "/health",
		"dhcp-unhealthy":           "/health",
		"pool:Guest":               "/network/dhcp",
		"pool:Lobby":               "/network/dhcp",
		"disk:/":                   "/health",
		"license-grace":            "/appliance?section=license",
		"network-revision-pending": "/network/revisions",
		"system-network-pending":   "/network/system",
	}
	if len(got) != len(want) {
		t.Fatalf("attention = %v, want %d items", ids(got), len(want))
	}
	for _, it := range got {
		href, ok := want[it.ID]
		if !ok {
			t.Errorf("unexpected item %q", it.ID)
			continue
		}
		if it.Href != href || it.Action == "" || it.Title == "" {
			t.Errorf("%s = %+v, want a titled link to %s", it.ID, it, href)
		}
	}
	// Errors lead.
	seenWarn := false
	for _, it := range got {
		if it.Severity == "warn" {
			seenWarn = true
		} else if seenWarn {
			t.Fatalf("an error follows a warning: %v", ids(got))
		}
	}
	// The disconnected PMS is an error; the inactive one is not an item at all.
	for _, it := range got {
		if it.ID == "pms-not-ready:Opera" && it.Severity != "err" {
			t.Error("a disconnected PMS that blocks room sign-in must be an error")
		}
	}
}

func TestLicenceAttention(t *testing.T) {
	cases := map[string]struct {
		installed bool
		state     string
		id        string
	}{
		"missing":   {false, "Active", "license-missing"},
		"expired":   {true, "Expired", "license-expired"},
		"revoked":   {true, "Revoked", "license-revoked"},
		"suspended": {true, "Suspended", "license-suspended"},
	}
	for name, c := range cases {
		got := deriveAttention(attentionInput{LicenseKnown: true, LicenseInstalled: c.installed, LicenseState: c.state})
		if len(got) != 1 || got[0].ID != c.id {
			t.Errorf("%s: attention = %v, want [%s]", name, ids(got), c.id)
		}
	}
	if got := deriveAttention(attentionInput{LicenseKnown: false}); len(got) != 0 {
		t.Error("an unreadable licence must not be reported as a missing one")
	}
}

func TestAttentionNeverReadsABlockTheCallerWasRefused(t *testing.T) {
	util := 99.0
	busy := true
	o := overviewResp{}
	o.Networks.Rows = []ovNetwork{{Name: "Guest", Enabled: true, Utilisation: &util}}
	o.DHCP.section = section{Available: false, Reason: reasonNotPermitted}
	o.DHCP.ServerConfigured, o.DHCP.ServerHealthy = &busy, new(bool)
	o.Services.section = section{Available: false, Reason: reasonNotPermitted}
	o.Services.Services = []ovService{{Name: "scd", State: stFailed, Critical: true}}
	o.Appliance.License = ovLicense{section: section{Available: false, Reason: reasonNotPermitted}}
	o.Appliance.Resources = applianceResources{section: section{Available: false}, Disks: []diskFigures{{Path: "/", UsedPct: 99}}}
	o.Appliance.Revisions = ovRevisions{section: section{Available: false}, Pending: &ovRevision{Seq: 4}}
	if got := deriveAttention(attentionInputFrom(o)); len(got) != 0 {
		t.Fatalf("attention derived from refused blocks: %v", ids(got))
	}
}

// ---------------------------------------------------------------------------------------- permissions --

func TestTheOverviewIsMountedUnderReports(t *testing.T) {
	src, err := os.ReadFile("resources_site.go")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(src), "func (s *server) reportsRoutes()")
	if i < 0 {
		t.Fatal("reportsRoutes not found")
	}
	body := string(src[i:])
	for _, route := range []string{`r.Get("/overview", s.reportsOverview)`, `r.Get("/appliance-resources", s.reportsApplianceResources)`} {
		if !strings.Contains(body, route) {
			t.Errorf("%s is not registered on the reports router (the `reports` permission gate)", route)
		}
	}
}

func TestOverviewBlockKeysAreRealResourcesAndGateAsIntended(t *testing.T) {
	known := map[string]bool{}
	for _, perms := range rolePerms {
		for k := range perms {
			known[k] = true
		}
	}
	for block, key := range overviewBlockKeys {
		if !known[key] {
			t.Errorf("block %s is gated on %q, which no role holds; it would be unreadable for everyone but site_admin", block, key)
		}
	}
	for _, block := range []string{"dhcp", "dns", "networks.leases", "appliance.network", "appliance.revisions"} {
		if overviewBlockKeys[block] != "network" {
			t.Errorf("%s must require `network`", block)
		}
	}
	if overviewBlockKeys["services"] != "diagnostics" || overviewBlockKeys["appliance.resources"] != "diagnostics" {
		t.Error("services and resources must require `diagnostics`")
	}
	// The front desk reads reports and not network configuration: it must be refused the network blocks.
	desk := []string{"front_office_operator"}
	if !permFor(desk, "reports", permRead) {
		t.Fatal("front office lost reports read; the overview would be unreachable for the desk")
	}
	if permFor(desk, "network", permRead) {
		t.Error("front office now reads network; revisit which overview blocks it should see")
	}
	if !permFor([]string{"site_viewer"}, "network", permRead) || !permFor([]string{"hotel_it_manager"}, "diagnostics", permRead) {
		t.Error("the roles that own the network and diagnostics screens must see those blocks")
	}
}

func TestTheHandlerConsultsTheBlockKeys(t *testing.T) {
	src, err := os.ReadFile("resources_overview.go")
	if err != nil {
		t.Fatal(err)
	}
	s := stripComments(string(src))
	for _, k := range []string{`can("network")`, `can("diagnostics")`, `can("license")`} {
		if !strings.Contains(s, k) {
			t.Errorf("the handler no longer checks %s", k)
		}
	}
}
