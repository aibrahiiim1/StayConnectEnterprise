package main

// THE ARITHMETIC BEHIND THE OVERVIEW, KEPT APART FROM THE QUERIES.
//
// Everything in this file is a pure function of its arguments: no database, no socket, no clock of its own. That
// is deliberate. The overview's figures are only as trustworthy as the arithmetic that turns rows into buckets,
// intervals into concurrency, address ranges into pool sizes and leases into utilisation — and the repository's
// integration tests are compiled by CI but not run by it. So the parts that can be wrong in a way a reviewer
// would not see are here, where the ordinary `go test` exercises them on every build.

import (
	"errors"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------------------------------------
// the time window
// ---------------------------------------------------------------------------------------------------------

// overviewRange is one choice on the range selector. The bucket widths are chosen so each range draws a chart
// with a readable number of points (24, 56, 30) rather than one per sample.
type overviewRange struct {
	Key     string
	Bucket  time.Duration
	Buckets int
}

var overviewRanges = map[string]overviewRange{
	"24h": {Key: "24h", Bucket: time.Hour, Buckets: 24},
	"7d":  {Key: "7d", Bucket: 3 * time.Hour, Buckets: 56},
	"30d": {Key: "30d", Bucket: 24 * time.Hour, Buckets: 30},
}

// overviewMaxWindow is the hard ceiling on any window this endpoint will read. It is not a knob: the sign-in
// attempt ledger keeps thirty days, and a longer window over the accounting samples would be a query nobody
// could afford to poll.
const overviewMaxWindow = 30 * 24 * time.Hour

type overviewWindow struct {
	Range  string
	Start  time.Time
	End    time.Time
	Now    time.Time
	Bucket time.Duration
	N      int
}

var errUnknownRange = errors.New("range must be one of 24h, 7d or 30d")

// computeWindow places the buckets. They are ALIGNED to the database's local midnight (dayStart), so an hourly
// bucket is 14:00-15:00 rather than 14:37-15:37 and a daily bucket is a calendar day at the property. The last
// bucket is the one containing `now`, and is therefore partial; callers that average over a bucket divide by the
// elapsed part of it, not by its nominal width.
func computeWindow(key string, now, dayStart time.Time) (overviewWindow, error) {
	r, ok := overviewRanges[key]
	if !ok {
		return overviewWindow{}, errUnknownRange
	}
	if dayStart.After(now) {
		dayStart = now.Truncate(24 * time.Hour)
	}
	k := int64(now.Sub(dayStart) / r.Bucket)
	end := dayStart.Add(time.Duration(k+1) * r.Bucket)
	start := end.Add(-time.Duration(r.Buckets) * r.Bucket)
	w := overviewWindow{Range: r.Key, Start: start, End: end, Now: now, Bucket: r.Bucket, N: r.Buckets}
	if w.End.Sub(w.Start) > overviewMaxWindow {
		return overviewWindow{}, errors.New("window exceeds the maximum the overview reads")
	}
	return w, nil
}

// edges returns the N+1 bucket boundaries.
func (w overviewWindow) edges() []time.Time {
	out := make([]time.Time, w.N+1)
	for i := range out {
		out[i] = w.Start.Add(time.Duration(i) * w.Bucket)
	}
	return out
}

// bucketStarts is what the UI labels its x-axis with.
func (w overviewWindow) bucketStarts() []time.Time {
	e := w.edges()
	return e[:w.N]
}

// bucketOf returns the bucket index for t, or -1 when t falls outside the window.
func (w overviewWindow) bucketOf(t time.Time) int {
	if t.Before(w.Start) || !t.Before(w.End) {
		return -1
	}
	return int(t.Sub(w.Start) / w.Bucket)
}

// ---------------------------------------------------------------------------------------------------------
// traffic buckets from accounting samples
// ---------------------------------------------------------------------------------------------------------

// ACCOUNTING SAMPLES ARE DELTAS. This was established from the writer, not assumed from the reader:
//
//   - The only function allowed to insert into iam_v2.accounting_records is ingest_absolute_counters — the
//     p3_controlled_writer_only trigger resolves the table's owner through exactly that signature and refuses
//     every other writer. It receives ABSOLUTE counters, compares them with the locked checkpoint, and stores
//     v_up := p_abs_up - prev_bytes_up (and the same for down). What lands in the row is the bytes moved since
//     the previous accepted observation.
//   - The older iam_base ingest_sample stores the absolute counter in the row instead, and it is precisely the
//     path that trigger shuts out; it survives only as a scratch fixture.
//   - Every existing reader already treats rows as deltas: the usage screen sums them into the session total,
//     and the quota-crossing derivation runs a cumulative SUM() over them.
//
// So a bucket's bytes are the SUM of the rows sampled inside it. Differencing consecutive rows — the right move
// for a cumulative counter — would here subtract one interval's traffic from the next and report nonsense.
//
// The row is attributed to the bucket its sampled_at falls in. A delta covers the interval since the previous
// sample (one tick, normally a second), so at most one tick's traffic can land in the bucket after the one it
// was moved in. That is the resolution of the measurement, stated rather than hidden.
type sampleBucketRow struct {
	Bucket    int
	SessionID string
	Down      int64
	Up        int64
}

// counterMode names the two ways a sample series can be encoded. Only counterDelta occurs in this schema; the
// cumulative branch exists so the arithmetic for the other encoding is written down, tested, and cannot be
// reached for by accident.
type counterMode int

const (
	counterDelta counterMode = iota
	counterCumulative
)

// perBucketBytes turns ONE session's ordered samples into bytes per bucket under the given encoding. For deltas
// it sums; for cumulative counters it differences consecutive readings, treating a decrease as a counter reset
// (the new reading is then the bytes since the reset). The first cumulative reading contributes nothing, because
// its value includes traffic from before the window and nothing says how much.
func perBucketBytes(w overviewWindow, times []time.Time, values []int64, mode counterMode) []int64 {
	out := make([]int64, w.N)
	var prev int64
	have := false
	for i, t := range times {
		v := values[i]
		var d int64
		switch mode {
		case counterDelta:
			d = v
		case counterCumulative:
			switch {
			case !have:
				d = 0
			case v >= prev:
				d = v - prev
			default:
				d = v // counter reset
			}
			prev, have = v, true
		}
		if b := w.bucketOf(t); b >= 0 && d > 0 {
			out[b] += d
		}
	}
	return out
}

// trafficAttribution says what a session is attributed to for the "top networks" and "top packages" lists.
type trafficAttribution struct {
	Interface string
	PackageID string
}

type trafficRollup struct {
	Down, Up         int64
	SeriesDown       []int64
	SeriesUp         []int64
	ByInterface      map[string][2]int64
	ByPackage        map[string][2]int64
	SessionsWithData int
}

// rollupTraffic folds per-(bucket, session) sums into the series and the two attributions.
func rollupTraffic(w overviewWindow, rows []sampleBucketRow, attr map[string]trafficAttribution) trafficRollup {
	r := trafficRollup{
		SeriesDown: make([]int64, w.N), SeriesUp: make([]int64, w.N),
		ByInterface: map[string][2]int64{}, ByPackage: map[string][2]int64{},
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Bucket < 0 || row.Bucket >= w.N {
			continue
		}
		r.SeriesDown[row.Bucket] += row.Down
		r.SeriesUp[row.Bucket] += row.Up
		r.Down += row.Down
		r.Up += row.Up
		a := attr[row.SessionID]
		ni := r.ByInterface[a.Interface]
		ni[0] += row.Down
		ni[1] += row.Up
		r.ByInterface[a.Interface] = ni
		np := r.ByPackage[a.PackageID]
		np[0] += row.Down
		np[1] += row.Up
		r.ByPackage[a.PackageID] = np
		if !seen[row.SessionID] && row.Down+row.Up > 0 {
			seen[row.SessionID] = true
			r.SessionsWithData++
		}
	}
	return r
}

// ---------------------------------------------------------------------------------------------------------
// concurrency from session intervals
// ---------------------------------------------------------------------------------------------------------

type sessionInterval struct {
	Start time.Time
	End   *time.Time // nil = still open
}

// concurrency computes, per bucket, the PEAK number of sessions open at the same instant and the TIME-WEIGHTED
// AVERAGE number open. Both are exact for the intervals given: the peak by a sweep over start/end events, the
// average as session-seconds inside the bucket divided by the bucket's elapsed seconds.
//
// Intervals are half-open [start, end): a session that ends at 14:00 and one that starts at 14:00 were never
// on at the same time, so ends are processed before starts at the same instant. An open session runs to `now`.
// One session is one device, so these are connected devices.
func concurrency(w overviewWindow, ivs []sessionInterval) (peak []int, avg []float64) {
	edges := w.edges()
	peak = make([]int, w.N)
	avg = make([]float64, w.N)
	secs := make([]float64, w.N)

	type ev struct {
		t time.Time
		d int
	}
	evs := make([]ev, 0, 2*len(ivs))
	for _, iv := range ivs {
		s := iv.Start
		e := w.Now
		if iv.End != nil {
			e = *iv.End
		}
		if e.After(w.Now) {
			e = w.Now
		}
		if s.Before(w.Start) {
			s = w.Start
		}
		if e.After(w.End) {
			e = w.End
		}
		if !e.After(s) {
			continue
		}
		evs = append(evs, ev{s, +1}, ev{e, -1})
		// session-seconds per bucket
		for b := w.bucketOf(s); b >= 0 && b < w.N && edges[b].Before(e); b++ {
			lo, hi := edges[b], edges[b+1]
			if s.After(lo) {
				lo = s
			}
			if e.Before(hi) {
				hi = e
			}
			if hi.After(lo) {
				secs[b] += hi.Sub(lo).Seconds()
			}
		}
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].t.Equal(evs[j].t) {
			return evs[i].d < evs[j].d // ends before starts
		}
		return evs[i].t.Before(evs[j].t)
	})

	cur, b := 0, 0
	for i, e := range evs {
		if !e.t.Before(w.End) {
			break
		}
		for b < w.N-1 && !e.t.Before(edges[b+1]) {
			b++
			// The count carried INTO this bucket held at its start whenever the event is strictly later.
			if edges[b].Before(e.t) && cur > peak[b] {
				peak[b] = cur
			}
		}
		cur += e.d
		lastAtInstant := i == len(evs)-1 || !evs[i+1].t.Equal(e.t)
		if (e.d > 0 || lastAtInstant) && cur > peak[b] {
			peak[b] = cur
		}
	}
	for b < w.N-1 {
		b++
		if edges[b].Before(w.Now) && cur > peak[b] {
			peak[b] = cur
		}
	}

	for i := 0; i < w.N; i++ {
		hi := edges[i+1]
		if hi.After(w.Now) {
			hi = w.Now
		}
		span := hi.Sub(edges[i]).Seconds()
		if span > 0 {
			avg[i] = math.Round(secs[i]/span*100) / 100
		}
	}
	return peak, avg
}

// ---------------------------------------------------------------------------------------------------------
// address pools
// ---------------------------------------------------------------------------------------------------------

func ipv4ToUint(s string) (uint32, bool) {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return 0, false
	}
	v4 := ip.To4()
	if v4 == nil {
		return 0, false
	}
	return uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3]), true
}

// ipv4RangeSize is the number of addresses in [start, end], inclusive, for ANY IPv4 range.
//
// The dashboard used to subtract fourth octets, which is only arithmetic inside one /24, and so it refused (and
// scored as zero) every pool that crossed an octet boundary — 10.20.0.10-10.20.3.250 on a /22 was reported as
// "no address range is configured". The addresses are 32-bit integers; the size is their difference plus one.
func ipv4RangeSize(start, end string) (int64, bool) {
	a, ok1 := ipv4ToUint(start)
	b, ok2 := ipv4ToUint(end)
	if !ok1 || !ok2 || b < a {
		return 0, false
	}
	return int64(b) - int64(a) + 1, true
}

type ipv4Range struct {
	Start, End string
	lo, hi     uint32
	Size       int64
}

func newIPv4Range(start, end string) (ipv4Range, bool) {
	n, ok := ipv4RangeSize(start, end)
	if !ok {
		return ipv4Range{}, false
	}
	lo, _ := ipv4ToUint(start)
	hi, _ := ipv4ToUint(end)
	return ipv4Range{Start: start, End: end, lo: lo, hi: hi, Size: n}, true
}

func (r ipv4Range) contains(ip uint32) bool { return ip >= r.lo && ip <= r.hi }

// ---------------------------------------------------------------------------------------------------------
// DHCP leases
// ---------------------------------------------------------------------------------------------------------

// keaLeaseRow is the part of a Kea lease the overview reads. The hardware address and hostname are
// deliberately not decoded: this surface reports counts, and a field it never parses is one it cannot leak.
type keaLeaseRow struct {
	IPAddress string `json:"ip-address"`
	State     int    `json:"state"`
	CLTT      int64  `json:"cltt"`
	ValidLft  int64  `json:"valid-lft"`
}

// keaLeaseExpired is Kea's state for a released or expired lease (netd uses the same constant).
const keaLeaseExpired = 2

// leaseIsCurrent mirrors netd's rule exactly: memfile keeps lapsed rows with state 0 until the address is
// reused, so a lease is current only if it is not in the expired state AND its lifetime has not run out.
// Counting the lapsed rows was measured to overstate occupancy by ninety-odd addresses on the PRE-LIVE unit.
func leaseIsCurrent(l keaLeaseRow, now time.Time) bool {
	if l.State == keaLeaseExpired || l.ValidLft <= 0 || l.CLTT <= 0 {
		return false
	}
	return l.CLTT+l.ValidLft > now.Unix()
}

type leaseNetwork struct {
	ID     string
	Subnet *net.IPNet
	Pools  []ipv4Range
}

type leasePoolCount struct {
	Start, End string
	Size       int64
	Active     int64
}

type leaseNetworkCount struct {
	Active       int64 // current leases anywhere in the subnet (pools and reservations)
	InPools      int64 // current leases inside a configured pool range
	ExpiringSoon int64
	Pools        []leasePoolCount
}

// aggregateLeases counts current leases per guest network (by subnet membership, not by Kea's subnet-id, which
// is positional in the rendered config and changes when a network is added) and per pool range. Leases that
// match no configured network are counted as unmatched rather than dropped silently.
func aggregateLeases(leases []keaLeaseRow, nets []leaseNetwork, now time.Time, expiringWithin time.Duration) (map[string]*leaseNetworkCount, int64, int64) {
	out := map[string]*leaseNetworkCount{}
	for _, n := range nets {
		c := &leaseNetworkCount{Pools: make([]leasePoolCount, len(n.Pools))}
		for i, p := range n.Pools {
			c.Pools[i] = leasePoolCount{Start: p.Start, End: p.End, Size: p.Size}
		}
		out[n.ID] = c
	}
	var total, unmatched int64
	soon := now.Add(expiringWithin).Unix()
	for _, l := range leases {
		if !leaseIsCurrent(l, now) {
			continue
		}
		total++
		ip := net.ParseIP(strings.TrimSpace(l.IPAddress))
		u, ok := ipv4ToUint(l.IPAddress)
		if ip == nil || !ok {
			unmatched++
			continue
		}
		matched := false
		for _, n := range nets {
			if n.Subnet == nil || !n.Subnet.Contains(ip) {
				continue
			}
			matched = true
			c := out[n.ID]
			c.Active++
			if l.CLTT+l.ValidLft <= soon {
				c.ExpiringSoon++
			}
			for i, p := range n.Pools {
				if p.contains(u) {
					c.InPools++
					c.Pools[i].Active++
					break
				}
			}
			break
		}
		if !matched {
			unmatched++
		}
	}
	return out, total, unmatched
}

// utilisationPct is leases-in-pool over pool size, as a percentage with one decimal. nil when there is no pool,
// because "0 %" of nothing would read as an empty pool.
func utilisationPct(inPool, size int64) *float64 {
	if size <= 0 {
		return nil
	}
	v := math.Round(float64(inPool)/float64(size)*1000) / 10
	return &v
}

// ---------------------------------------------------------------------------------------------------------
// what needs attention
// ---------------------------------------------------------------------------------------------------------

type attentionItem struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // "warn" | "err"
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Href     string `json:"href"`
	Action   string `json:"action"`
}

// Thresholds for the two utilisation warnings. They are presentation thresholds on a read-only overview, not
// operational behaviour: nothing on the appliance acts on them.
const (
	attentionPoolPct     = 85.0
	attentionDiskPct     = 85.0
	attentionDiskCritPct = 95.0
)

type attentionPMS struct {
	Label         string
	Active        bool
	RoomAuthReady bool
	Connected     bool
	Reason        string
}

type attentionService struct {
	Name     string
	State    string
	Critical bool
}

type attentionPool struct {
	Network string
	Pct     float64
}

type attentionDisk struct {
	Path string
	Pct  float64
}

type attentionInput struct {
	PMS                []attentionPMS
	PMSReviewCases     int64
	PostingsReviewOpen int64
	Services           []attentionService
	KeaConfigured      bool
	KeaHealthy         bool
	KeaKnown           bool
	Pools              []attentionPool
	Disks              []attentionDisk
	LicenseKnown       bool
	LicenseInstalled   bool
	LicenseState       string
	RevisionPending    bool
	RevisionSeq        int64
	SystemNetPending   bool
}

var serviceLabels = map[string]string{
	"scd": "Session controller", "edged": "Admin service", "netd": "Network controller",
	"portald": "Guest portal", "acctd": "Usage accounting", "hotel-admin": "Admin web app",
	"caddy": "Web front end", "kea": "DHCP server", "unbound": "DNS resolver", "postgres": "Site database",
}

func serviceLabel(name string) string {
	if l, ok := serviceLabels[name]; ok {
		return l
	}
	return name
}

// deriveAttention turns the overview's facts into the short list of things a person must act on. Each item
// names what it stops and links to the screen that fixes it. Things that are merely worth knowing are NOT here:
// a list that mixes the two trains people to skim it.
func deriveAttention(in attentionInput) []attentionItem {
	out := []attentionItem{}
	add := func(it attentionItem) { out = append(out, it) }

	for _, p := range in.PMS {
		if !p.Active || p.RoomAuthReady {
			continue
		}
		sev := "err"
		if p.Connected {
			sev = "warn"
		}
		label := p.Label
		if label == "" {
			label = "PMS connection"
		}
		detail := "Guests cannot sign in with their room number on this connection until it is ready."
		if p.Reason != "" {
			detail += " Reason: " + strings.ToLower(strings.ReplaceAll(p.Reason, "_", " ")) + "."
		}
		add(attentionItem{ID: "pms-not-ready:" + label, Severity: sev,
			Title: label + " is not ready for room sign-in", Detail: detail,
			Href: "/pms-interfaces", Action: "Open PMS connection"})
	}
	if in.PMSReviewCases > 0 {
		add(attentionItem{ID: "pms-review", Severity: "warn",
			Title:  plural(in.PMSReviewCases, "PMS message is", "PMS messages are") + " waiting for a decision",
			Detail: "They could not be applied automatically.",
			Href:   "/stay-events", Action: "Review messages"})
	}
	if in.PostingsReviewOpen > 0 {
		add(attentionItem{ID: "postings-review", Severity: "warn",
			Title: plural(in.PostingsReviewOpen, "room charge is", "room charges are") + " waiting for a manual decision",
			Href:  "/financial-review", Action: "Open charge review"})
	}
	for _, s := range in.Services {
		if !s.Critical {
			continue
		}
		switch s.State {
		case stFailed, stCrashLoop, stDegraded:
			add(attentionItem{ID: "service:" + s.Name, Severity: "err",
				Title:  serviceLabel(s.Name) + " is " + strings.ReplaceAll(s.State, "_", " "),
				Detail: "A service this appliance depends on is not healthy.",
				Href:   "/health", Action: "Open diagnostics"})
		case stRecovering:
			add(attentionItem{ID: "service:" + s.Name, Severity: "warn",
				Title: serviceLabel(s.Name) + " is recovering",
				Href:  "/health", Action: "Open diagnostics"})
		}
	}
	if in.KeaKnown && in.KeaConfigured && !in.KeaHealthy {
		add(attentionItem{ID: "dhcp-unhealthy", Severity: "err",
			Title:  "The DHCP server is not healthy",
			Detail: "New devices may not receive an address.",
			Href:   "/health", Action: "Open diagnostics"})
	}
	for _, p := range in.Pools {
		if p.Pct <= attentionPoolPct {
			continue
		}
		sev := "warn"
		if p.Pct >= 100 {
			sev = "err"
		}
		add(attentionItem{ID: "pool:" + p.Network, Severity: sev,
			Title:  "Address pool on " + p.Network + " is " + pct1(p.Pct) + " full",
			Detail: "When it is full, new devices on this network cannot get an address.",
			Href:   "/network/dhcp", Action: "Open DHCP"})
	}
	for _, d := range in.Disks {
		if d.Pct <= attentionDiskPct {
			continue
		}
		sev := "warn"
		if d.Pct >= attentionDiskCritPct {
			sev = "err"
		}
		add(attentionItem{ID: "disk:" + d.Path, Severity: sev,
			Title:  "Disk " + d.Path + " is " + pct1(d.Pct) + " full",
			Detail: "Backups, logs and usage records need free space.",
			Href:   "/health", Action: "Open diagnostics"})
	}
	if in.LicenseKnown {
		switch {
		case !in.LicenseInstalled:
			add(attentionItem{ID: "license-missing", Severity: "warn",
				Title:  "This appliance has no signed licence installed",
				Detail: "Activate it before the property opens.",
				Href:   "/appliance?section=setup", Action: "Finish setup"})
		case in.LicenseState == "GracePeriod":
			add(attentionItem{ID: "license-grace", Severity: "warn",
				Title:  "The licence is in its renewal grace period",
				Detail: "New sign-ins stop when the grace period ends.",
				Href:   "/appliance?section=license", Action: "Open licence"})
		case in.LicenseState == "Suspended" || in.LicenseState == "Restricted":
			add(attentionItem{ID: "license-suspended", Severity: "err",
				Title: "The licence is suspended",
				Href:  "/appliance?section=license", Action: "Open licence"})
		case in.LicenseState == "Expired" || in.LicenseState == "Revoked":
			add(attentionItem{ID: "license-" + strings.ToLower(in.LicenseState), Severity: "err",
				Title:  "The licence is " + strings.ToLower(in.LicenseState),
				Detail: "New guests cannot sign in.",
				Href:   "/appliance?section=license", Action: "Open licence"})
		}
	}
	if in.RevisionPending {
		add(attentionItem{ID: "network-revision-pending", Severity: "warn",
			Title:  "A network change is waiting to be confirmed",
			Detail: "If it is not confirmed in time it is rolled back automatically.",
			Href:   "/network/revisions", Action: "Review change"})
	}
	if in.SystemNetPending {
		add(attentionItem{ID: "system-network-pending", Severity: "warn",
			Title:  "A WAN/LAN change is waiting to be confirmed",
			Detail: "If it is not confirmed in time it is rolled back automatically.",
			Href:   "/network/system", Action: "Open WAN/LAN"})
	}
	// Errors first, then warnings; stable within each, so the list does not reshuffle between polls.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity == "err" && out[j].Severity != "err" })
	return out
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa64(n) + " " + many
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func pct1(v float64) string {
	r := math.Round(v*10) / 10
	if r == math.Trunc(r) {
		return strconv.FormatFloat(r, 'f', 0, 64) + "%"
	}
	return strconv.FormatFloat(r, 'f', 1, 64) + "%"
}
