package main

// Pure functions of their arguments (no database, socket or clock), kept apart from the queries so ordinary
// `go test` covers the overview's arithmetic; integration tests are compiled by CI but not run.

import (
	"errors"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// overviewRange is one choice on the range selector; bucket widths give a readable number of points (24, 56, 30).
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

// overviewMaxWindow is a hard ceiling, not a knob: the sign-in attempt ledger keeps thirty days and a longer
// accounting query would be too expensive to poll.
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

// computeWindow aligns buckets to the database's local midnight (dayStart). The last bucket contains `now` and is
// partial; callers that average over a bucket divide by its elapsed part, not its nominal width.
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

func (w overviewWindow) edges() []time.Time {
	out := make([]time.Time, w.N+1)
	for i := range out {
		out[i] = w.Start.Add(time.Duration(i) * w.Bucket)
	}
	return out
}

func (w overviewWindow) bucketStarts() []time.Time {
	e := w.edges()
	return e[:w.N]
}

func (w overviewWindow) bucketOf(t time.Time) int {
	if t.Before(w.Start) || !t.Before(w.End) {
		return -1
	}
	return int(t.Sub(w.Start) / w.Bucket)
}

// Accounting samples are deltas: ingest_absolute_counters is the only writer allowed into iam_v2.accounting_records
// and stores the bytes moved since the previous observation. A bucket's bytes are therefore the SUM of the rows
// sampled inside it; differencing consecutive rows would report nonsense. A row lands in the bucket of its
// sampled_at, so at most one tick's traffic can fall in the following bucket.
type sampleBucketRow struct {
	Bucket    int
	SessionID string
	Down      int64
	Up        int64
}

// counterMode: only counterDelta occurs in this schema; the cumulative branch is kept written down and tested.
type counterMode int

const (
	counterDelta counterMode = iota
	counterCumulative
)

// perBucketBytes: for cumulative counters a decrease is a counter reset, and the first reading contributes nothing
// because it includes unknown earlier traffic.
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

type sessionInterval struct {
	Start time.Time
	End   *time.Time // nil = still open
}

// concurrency returns per-bucket peak and time-weighted average open sessions (one session is one device).
// Intervals are half-open [start, end): a session ending at 14:00 and one starting then never overlapped, so ends
// are processed before starts at the same instant. An open session runs to `now`.
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

// ipv4RangeSize counts addresses in [start, end] inclusive as 32-bit integers, so pools crossing an octet boundary work.
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

// keaLeaseRow deliberately omits the hardware address and hostname: this surface reports counts, and a field it never parses cannot leak.
type keaLeaseRow struct {
	IPAddress string `json:"ip-address"`
	State     int    `json:"state"`
	CLTT      int64  `json:"cltt"`
	ValidLft  int64  `json:"valid-lft"`
}

const keaLeaseExpired = 2

// leaseIsCurrent mirrors netd's rule: Kea's memfile keeps lapsed rows with state 0 until the address is reused,
// so a lease is current only if not expired and its lifetime has not run out.
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

// aggregateLeases matches by subnet membership, not Kea's subnet-id (positional, shifts when a network is added);
// leases matching no network are counted as unmatched.
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

// utilisationPct is nil when there is no pool, since "0 %" of nothing would read as an empty pool.
func utilisationPct(inPool, size int64) *float64 {
	if size <= 0 {
		return nil
	}
	v := math.Round(float64(inPool)/float64(size)*1000) / 10
	return &v
}

type attentionItem struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // "warn" | "err"
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Href     string `json:"href"`
	Action   string `json:"action"`
}

// Presentation thresholds only; nothing on the appliance acts on them.
const (
	attentionPoolPct     = 85.0
	attentionDiskPct     = 85.0
	attentionDiskCritPct = 95.0
)

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
	RoomSignInIssues   []rsIssue
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
	// PortalHTTPS is set only for a caller who may read portal-https; nil means unknown or not permitted and raises nothing.
	PortalHTTPS *portalHTTPSCheck
	// DefaultDBPassword is set only for a caller who may read diagnostics.
	DefaultDBPassword bool
	// CardUnsellable: live packages sold by card that no client can actually pay by card.
	CardUnsellable []attentionPackage
}

// attentionPackage is a card-sold package whose sale cannot complete: no default card account, or one in another currency.
type attentionPackage struct {
	Name, Currency, AccountCurrency string
	NoAccount                       bool
}

var serviceLabels = map[string]string{
	"scd": "Session controller", "edged": "Admin service", "netd": "Network controller",
	"portald": "Client Portal", "acctd": "Usage accounting", "hotel-admin": "Admin web app",
	"caddy": "Web front end", "kea": "DHCP server", "unbound": "DNS resolver", "postgres": "Site database",
}

func serviceLabel(name string) string {
	if l, ok := serviceLabels[name]; ok {
		return l
	}
	return name
}

// deriveAttention lists only what a person must act on; merely informative items are excluded so the list is not skimmed.
func deriveAttention(in attentionInput) []attentionItem {
	out := []attentionItem{}
	add := func(it attentionItem) { out = append(out, it) }

	for _, it := range in.RoomSignInIssues {
		add(attentionItem{ID: it.ID, Severity: it.Severity, Title: it.Title, Detail: it.Detail, Href: it.Href, Action: it.Action})
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
	// Card is never converted, so a package in a currency the card account does not settle is not offered at all.
	for _, p := range in.CardUnsellable {
		detail := "Clients cannot buy it by card, so without a voucher or a room sign-in they are offered nothing."
		if p.NoAccount {
			detail = "There is no active default card account. " + detail
		} else {
			detail = "It is priced in " + p.Currency + " but the default card account settles " + p.AccountCurrency +
				" and does not convert. " + detail + " Price it in " + p.AccountCurrency +
				", or use a card account that settles " + p.Currency + "."
		}
		add(attentionItem{ID: "package-card:" + p.Name, Severity: "warn",
			Title:  p.Name + " cannot be bought by card",
			Detail: detail, Href: "/payment-methods", Action: "Open payment methods"})
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
		case !in.LicenseInstalled || in.LicenseState == "none":
			add(attentionItem{ID: "license-missing", Severity: "warn",
				Title:  "This appliance is not activated yet",
				Detail: "Clients cannot sign in until it is activated and licensed.",
				Href:   "/appliance", Action: "Open appliance"})
		case in.LicenseState == "wrong_hardware":
			add(attentionItem{ID: "license-wrong-hardware", Severity: "err",
				Title:  "The installed licence belongs to a different appliance",
				Detail: "New clients cannot sign in. Ask your OneGate vendor for a licence for this appliance.",
				Href:   "/appliance", Action: "Open licence"})
		case in.LicenseState == "expiring":
			add(attentionItem{ID: "license-expiring", Severity: "warn",
				Title:  "The licence expires soon",
				Detail: "Ask your OneGate vendor to renew it.",
				Href:   "/appliance", Action: "Open licence"})
		case in.LicenseState == "grace":
			add(attentionItem{ID: "license-grace", Severity: "warn",
				Title:  "The licence is in its renewal grace period",
				Detail: "New sign-ins stop when the grace period ends.",
				Href:   "/appliance", Action: "Open licence"})
		case in.LicenseState == "suspended":
			add(attentionItem{ID: "license-suspended", Severity: "err",
				Title:  "The licence is suspended",
				Detail: "New clients cannot sign in.",
				Href:   "/appliance", Action: "Open licence"})
		case in.LicenseState == "expired" || in.LicenseState == "revoked":
			add(attentionItem{ID: "license-" + in.LicenseState, Severity: "err",
				Title:  "The licence is " + in.LicenseState,
				Detail: "New clients cannot sign in.",
				Href:   "/appliance", Action: "Open licence"})
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
	if it, ok := portalHTTPSAttention(in.PortalHTTPS); ok {
		add(it)
	}
	if in.DefaultDBPassword {
		add(defaultDBPasswordAttention())
	}
	// Errors first; stable within each so the list does not reshuffle between polls.
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
