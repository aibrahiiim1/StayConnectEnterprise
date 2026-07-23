// Package shape owns the per-session HTB shaping classes and byte-accounting
// counters for guest sessions, on each guest's ACTUAL network interfaces.
//
// Topology, per guest bridge B (br-lan for legacy/untagged, br-g<vlan> for tagged):
//
//	DOWNLOAD (internet -> guest): egresses B with dst = guest IP.
//	  -> HTB class + u32 dst-filter on B's egress root qdisc (handle 1:).
//
//	UPLOAD (guest -> internet): ingresses B with src = guest IP, then routes and
//	is SNAT'd before leaving on the WAN, so the WAN egress only ever sees the
//	appliance's IP. To measure the REAL guest src (pre-NAT) we redirect B's
//	ingress to a per-bridge IFB device and shape/count there.
//	  -> HTB class + u32 src-filter on ifb-<B>'s egress root qdisc (handle 1:).
//
// classid minor = 0x1000 + (((ip[2]&0x0f)<<8) | ip[3]) is derived from the IP
// host bits, so it is unique within any guest subnet up to /20 (covers the /22
// pools this product issues). The (device, minor) pair is globally unique
// because each bridge/ifb serves exactly one guest network — which is what lets
// acctd attribute traffic to the right session with no cross-network collision.
// This removes the legacy assumption that all guests live on br-lan/10.10.0.0/24.
package shape

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	RootMajor      = "1"
	RootParent     = "1:" // per-session classes hang directly off the root qdisc
	GuestMinorBase = 0x1000
	GuestMinorMax  = 0x1fff
)

type Client struct {
	TCPath string
	IPPath string

	// exec runs one external command and returns its combined output. It is a field so the staged
	// provisioning can be proven at the command level — that a re-rate is `class change` (counter-preserving)
	// and not delete+add, that a prepared class has no filter — without a kernel. Production uses realExec.
	exec func(ctx context.Context, name string, args ...string) ([]byte, error)

	mu         sync.Mutex
	infraReady map[string]bool // bridge -> ingress/IFB redirect established this process
	bridgeMu   map[string]*sync.Mutex
}

func realExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func New() *Client {
	return &Client{
		TCPath:     "/usr/sbin/tc",
		IPPath:     "/usr/sbin/ip",
		exec:       realExec,
		infraReady: map[string]bool{},
		bridgeMu:   map[string]*sync.Mutex{},
	}
}

// MinorForIP derives the collision-free HTB minor for a guest IP. Returns
// (0,false) for non-IPv4.
func MinorForIP(ip net.IP) (int, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false
	}
	host := (int(ip4[2]&0x0f) << 8) | int(ip4[3])
	return GuestMinorBase + host, true
}

// ClassidForIP returns "1:<minor-hex>" for a guest IP, or "" for non-IPv4.
func ClassidForIP(ip net.IP) string {
	minor, ok := MinorForIP(ip)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s:%x", RootMajor, minor)
}

// IFBName maps a guest bridge to its paired IFB device (upload measurement).
// br-lan -> ifb-lan, br-g301 -> ifb-g301. Bounded to IFNAMSIZ (15).
func IFBName(bridge string) string {
	name := "ifb-" + strings.TrimPrefix(bridge, "br-")
	if len(name) > 15 {
		name = name[:15]
	}
	return name
}

func (c *Client) lockBridge(bridge string) func() {
	c.mu.Lock()
	m, ok := c.bridgeMu[bridge]
	if !ok {
		m = &sync.Mutex{}
		c.bridgeMu[bridge] = m
	}
	c.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// EnsureBridgeInfra makes a guest bridge measurable: an HTB root on its egress
// (download) and an IFB device fed by an ingress->egress redirect (upload).
// Idempotent and safe to call on every authorize and on boot reconcile. It
// never touches the bridge's egress root qdisc if one already exists (netd
// primes it), so existing per-session download classes are preserved.
func (c *Client) EnsureBridgeInfra(ctx context.Context, bridge string) error {
	if bridge == "" {
		return fmt.Errorf("empty bridge")
	}
	unlock := c.lockBridge(bridge)
	defer unlock()

	c.mu.Lock()
	ready := c.infraReady[bridge]
	c.mu.Unlock()
	if ready {
		return nil
	}

	ifb := IFBName(bridge)
	// IFB device for upload measurement.
	_ = c.ipRun(ctx, "link", "add", ifb, "type", "ifb") // ignore "exists"
	if err := c.ipRun(ctx, "link", "set", ifb, "up"); err != nil {
		return fmt.Errorf("ifb up %s: %w", ifb, err)
	}
	// Root HTB on the IFB. Use `add` (idempotent, ignore "exists") NOT `replace`:
	// an IFB device and its qdisc persist across an scd restart, and `tc qdisc
	// replace root htb` fails on an already-htb root ("Change operation not
	// supported by specified qdisc"). `add` creates it on a fresh device and is a
	// harmless no-op when it is already there — preserving any live classes.
	_ = c.run(ctx, "qdisc", "add", "dev", ifb, "root", "handle", "1:", "htb", "default", "1")
	// Bridge egress root for download classes — add only if absent (never
	// replace: that would drop live per-session classes). netd/tc-setup
	// usually primed it already.
	_ = c.run(ctx, "qdisc", "add", "dev", bridge, "root", "handle", "1:", "htb", "default", "1")
	// Bridge ingress: rebuild cleanly so we hold EXACTLY ONE redirect filter,
	// even across a daemon restart (duplicate redirects would double-count
	// upload). Deleting the ingress qdisc drops only its own filters; the
	// egress root and the IFB classes live elsewhere and are untouched.
	_ = c.run(ctx, "qdisc", "del", "dev", bridge, "ingress")
	if err := c.run(ctx, "qdisc", "add", "dev", bridge, "handle", "ffff:", "ingress"); err != nil {
		return fmt.Errorf("ingress qdisc %s: %w", bridge, err)
	}
	if err := c.run(ctx, "filter", "add", "dev", bridge, "parent", "ffff:",
		"protocol", "all", "u32", "match", "u32", "0", "0",
		"action", "mirred", "egress", "redirect", "dev", ifb); err != nil {
		return fmt.Errorf("ingress redirect %s->%s: %w", bridge, ifb, err)
	}

	c.mu.Lock()
	c.infraReady[bridge] = true
	c.mu.Unlock()
	return nil
}

// AddSession creates the upload+download HTB classes & filters for ip on its
// guest bridge. downKbps/upKbps of 0 or negative are treated as "no cap".
func (c *Client) AddSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error {
	cid := ClassidForIP(ip)
	if cid == "" {
		return fmt.Errorf("ipv4 required, got %s", ip)
	}
	if err := c.EnsureBridgeInfra(ctx, bridge); err != nil {
		return err
	}
	// Remove any stale leaf (leftover from a crash) so we can re-add cleanly.
	_ = c.DeleteSession(ctx, bridge, ip)

	const unlimitedKbps = 1_000_000 // 1 Gbps ceiling if uncapped
	upRate := upKbps
	if upRate <= 0 {
		upRate = unlimitedKbps
	}
	downRate := downKbps
	if downRate <= 0 {
		downRate = unlimitedKbps
	}

	ifb := IFBName(bridge)
	// DOWNLOAD — bridge egress, dst = guest IP.
	if err := c.addClassAndFilter(ctx, bridge, cid, downRate, "dst", ip); err != nil {
		return fmt.Errorf("download add on %s: %w", bridge, err)
	}
	// UPLOAD — IFB egress (bridge ingress redirect), src = guest IP.
	if err := c.addClassAndFilter(ctx, ifb, cid, upRate, "src", ip); err != nil {
		_ = c.removeClassAndFilter(ctx, bridge, cid, ip)
		return fmt.Errorf("upload add on %s: %w", ifb, err)
	}
	return nil
}

// ---- staged, accountable-before-forwarding provisioning ---------------------
//
// AddSession above installs the class AND its forwarding filter in one call, so the moment it returns the
// guest's packets are already being classified into that class. That is wrong for a MANAGED Phase-3 class:
// nothing may be forwarded through it until its counter series has a durable, authoritative accounting
// origin. So the Phase-3 path (netd) never calls AddSession — it stages provisioning:
//
//	PrepareSession   creates the download and upload classes WITHOUT any forwarding filter. The classes exist
//	                 and their absolute counters can be read, but no guest packet is classified into them —
//	                 the guest's traffic still flows through the bridge's default class, exactly as it did
//	                 before this session was ever prepared. This is the state an origin is registered against.
//	ActivateSession  installs both forwarding filters, so from this instant the guest's traffic is classified
//	                 into the (now accountable) class. It rolls the first filter back if the second fails, so a
//	                 half-activated session never forwards in only one direction.
//	AbortSession     removes filters and classes in both directions and PROVES they are gone, for the
//	                 fail-closed path: anything that did not fully provision leaves nothing forwarding.
//	DenyForwarding   the last-resort quarantine: strip the forwarding filters even if the classes cannot be
//	                 removed, so a class that will not delete is left provably non-forwarding.
//	ReRateSession    changes an already-active class's rate IN PLACE (`tc class change`, never delete+add), so
//	                 an ordinary re-rate preserves the byte counters and the class keeps its generation.

// PrepareSession creates the download+upload HTB classes for ip WITHOUT their guest forwarding filters. The
// prepared classes carry no guest packets; they exist only to be read (for the accounting origin) and later
// activated. It clears any stale leaf first so a crashed prior attempt cannot survive as a half-class.
func (c *Client) PrepareSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error {
	cid := ClassidForIP(ip)
	if cid == "" {
		return fmt.Errorf("ipv4 required, got %s", ip)
	}
	if err := c.EnsureBridgeInfra(ctx, bridge); err != nil {
		return err
	}
	// Remove any stale class+filter for this slot (a crashed prepare, or a different session that held the
	// same minor) so preparation starts from nothing forwarding.
	_ = c.DeleteSession(ctx, bridge, ip)

	const unlimitedKbps = 1_000_000
	downRate, upRate := downKbps, upKbps
	if downRate <= 0 {
		downRate = unlimitedKbps
	}
	if upRate <= 0 {
		upRate = unlimitedKbps
	}
	ifb := IFBName(bridge)
	if err := c.addClass(ctx, bridge, cid, downRate); err != nil {
		return fmt.Errorf("prepare download class on %s: %w", bridge, err)
	}
	if err := c.addClass(ctx, ifb, cid, upRate); err != nil {
		// Roll the download class back so a failed prepare leaves no half-class behind.
		c.removeClassByPref(ctx, bridge, cid, filterPrefFor(ip))
		return fmt.Errorf("prepare upload class on %s: %w", ifb, err)
	}
	return nil
}

// ActivateSession installs the download and upload forwarding filters for an already-prepared class. Only
// after this does the class carry guest packets. If the second filter fails, the first is removed, so a
// session is never left forwarding in one direction only.
func (c *Client) ActivateSession(ctx context.Context, bridge string, ip net.IP) error {
	cid := ClassidForIP(ip)
	if cid == "" {
		return fmt.Errorf("ipv4 required, got %s", ip)
	}
	ifb := IFBName(bridge)
	if err := c.addFilter(ctx, bridge, cid, "dst", ip); err != nil {
		return fmt.Errorf("activate download filter on %s: %w", bridge, err)
	}
	if err := c.addFilter(ctx, ifb, cid, "src", ip); err != nil {
		_ = c.removeFilter(ctx, bridge, ip)
		return fmt.Errorf("activate upload filter on %s: %w", ifb, err)
	}
	return nil
}

// AbortSession removes both classes and both filters and then PROVES the class is gone on both devices. It is
// the fail-closed cleanup: after it returns nil, nothing for this ip forwards or is countable. If it cannot
// prove removal it returns an error, so the caller can escalate to DenyForwarding.
func (c *Client) AbortSession(ctx context.Context, bridge string, ip net.IP) error {
	// Filters first, so forwarding stops even if a class delete later fails.
	_ = c.removeFilter(ctx, bridge, ip)
	_ = c.removeFilter(ctx, IFBName(bridge), ip)
	_ = c.removeClassAndFilter(ctx, bridge, cidClass(ip), ip)
	_ = c.removeClassAndFilter(ctx, IFBName(bridge), cidClass(ip), ip)
	minor, ok := MinorForIP(ip)
	if !ok {
		return nil
	}
	for _, dev := range []string{bridge, IFBName(bridge)} {
		classes, err := c.ReadClasses(ctx, dev)
		if err != nil {
			return fmt.Errorf("abort: could not confirm class removal on %s: %w", dev, err)
		}
		if _, still := classes[minor]; still {
			return fmt.Errorf("abort: class %d still present on %s after removal", minor, dev)
		}
	}
	return nil
}

// DenyForwarding is the last-resort quarantine used when AbortSession could not prove the classes gone: strip
// the forwarding filters in both directions so, whatever state the classes are in, no guest packet is
// classified into them. A class with no filter forwards nothing.
func (c *Client) DenyForwarding(ctx context.Context, bridge string, ip net.IP) error {
	e1 := c.removeFilter(ctx, bridge, ip)
	e2 := c.removeFilter(ctx, IFBName(bridge), ip)
	if e1 != nil {
		return e1
	}
	return e2
}

// ReRateSession changes an already-active class's rate/ceil in place on both directions. It uses
// `tc class change`, never delete+add, so the class's byte counters are preserved — an ordinary re-rate must
// not look like a counter reset. It fails if the class is not already present (the caller then provisions).
func (c *Client) ReRateSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error {
	cid := ClassidForIP(ip)
	if cid == "" {
		return fmt.Errorf("ipv4 required, got %s", ip)
	}
	const unlimitedKbps = 1_000_000
	downRate, upRate := downKbps, upKbps
	if downRate <= 0 {
		downRate = unlimitedKbps
	}
	if upRate <= 0 {
		upRate = unlimitedKbps
	}
	if err := c.changeClass(ctx, bridge, cid, downRate); err != nil {
		return fmt.Errorf("re-rate download on %s: %w", bridge, err)
	}
	if err := c.changeClass(ctx, IFBName(bridge), cid, upRate); err != nil {
		return fmt.Errorf("re-rate upload on %s: %w", IFBName(bridge), err)
	}
	return nil
}

// cidClass is ClassidForIP but panics-free for callers that already validated ip.
func cidClass(ip net.IP) string { return ClassidForIP(ip) }

// SessionForwarding reports whether the guest forwarding filter is installed in BOTH directions — i.e. the
// class is actually classifying packets, not merely prepared. It is the post-activation verification: a class
// that exists but whose filter never took is a class that silently forwards nothing.
func (c *Client) SessionForwarding(ctx context.Context, bridge string, ip net.IP) (bool, error) {
	down, err := c.filterPresent(ctx, bridge, ip)
	if err != nil {
		return false, err
	}
	up, err := c.filterPresent(ctx, IFBName(bridge), ip)
	if err != nil {
		return false, err
	}
	return down && up, nil
}

// filterPresent reports whether the per-IP u32 filter (identified by its pref) is installed on a device.
func (c *Client) filterPresent(ctx context.Context, dev string, ip net.IP) (bool, error) {
	out, err := c.exec(ctx, c.TCPath, "filter", "show", "dev", dev, "parent", RootParent)
	if err != nil {
		// A missing device has no filters; that is a definite "not forwarding", not an error to propagate.
		return false, nil
	}
	want := fmt.Sprintf("pref %d ", filterPrefFor(ip))
	return bytes.Contains(out, []byte(want)), nil
}

// DeleteSession removes the classes & filters for ip on both directions
// (no-op if absent).
func (c *Client) DeleteSession(ctx context.Context, bridge string, ip net.IP) error {
	cid := ClassidForIP(ip)
	if cid == "" {
		return nil
	}
	_ = c.removeClassAndFilter(ctx, bridge, cid, ip)
	_ = c.removeClassAndFilter(ctx, IFBName(bridge), cid, ip)
	return nil
}

// DeleteSessionClass removes a managed class by MINOR, on both directions of a
// bridge. It exists for reconciliation: a class left behind by a session that no
// longer exists — after a crash, or a restart that lost the in-memory map — is
// only identifiable by its minor, because the IP that produced it is no longer
// known anywhere. Deleting by pref/classid needs no IP: the add path derives
// both deterministically from the same minor.
func (c *Client) DeleteSessionClass(ctx context.Context, bridge string, minor int) error {
	if minor < GuestMinorBase || minor > GuestMinorMax {
		// Refuse to touch anything outside the guest range. The root and default
		// classes are appliance infrastructure, not per-session state, and a
		// reconciliation that removed them would take the whole bridge down.
		return fmt.Errorf("minor %d is outside the managed guest class range", minor)
	}
	cid := fmt.Sprintf("%s:%x", RootMajor, minor)
	unlock := c.lockBridge(bridge)
	defer unlock()
	c.removeClassByPref(ctx, bridge, cid, minor)
	c.removeClassByPref(ctx, IFBName(bridge), cid, minor)
	return nil
}

// removeClassByPref is removeClassAndFilter without needing the IP: the filter
// pref and the classid are both functions of the minor alone.
func (c *Client) removeClassByPref(ctx context.Context, ifc, cid string, pref int) {
	_ = c.run(ctx, "filter", "del", "dev", ifc, "parent", RootParent,
		"pref", strconv.Itoa(pref), "protocol", "ip", "u32")
	_ = c.run(ctx, "qdisc", "del", "dev", ifc, "parent", cid)
	_ = c.run(ctx, "class", "del", "dev", ifc, "classid", cid)
}

// filterPrefFor returns a per-IP u32 filter pref, unique within a device
// because each device serves one subnet and the minor is host-unique there.
func filterPrefFor(ip net.IP) int {
	minor, ok := MinorForIP(ip)
	if !ok {
		return 0
	}
	return minor
}

func (c *Client) addClassAndFilter(ctx context.Context, ifc, cid string, kbps int, matchField string, ip net.IP) error {
	if err := c.addClass(ctx, ifc, cid, kbps); err != nil {
		return err
	}
	return c.addFilter(ctx, ifc, cid, matchField, ip)
}

// addClass creates the HTB class and its leaf qdisc on a device, WITHOUT any classifying filter. A class with
// no filter receives no packets, so this is the "prepared, non-forwarding" half of provisioning.
func (c *Client) addClass(ctx context.Context, ifc, cid string, kbps int) error {
	rate := fmt.Sprintf("%dkbit", kbps)
	if err := c.run(ctx, "class", "add", "dev", ifc, "parent", RootParent,
		"classid", cid, "htb", "rate", rate, "ceil", rate, "burst", "32k"); err != nil {
		return err
	}
	minor := strings.SplitN(cid, ":", 2)[1]
	if err := c.run(ctx, "qdisc", "add", "dev", ifc, "parent", cid,
		"handle", minor+":", "fq_codel"); err != nil {
		return err
	}
	return nil
}

// changeClass adjusts an existing class's rate/ceil in place. `class change` preserves the class's byte
// counters — unlike delete+add, which resets them — so an ordinary re-rate does not look like a series reset.
func (c *Client) changeClass(ctx context.Context, ifc, cid string, kbps int) error {
	rate := fmt.Sprintf("%dkbit", kbps)
	return c.run(ctx, "class", "change", "dev", ifc, "parent", RootParent,
		"classid", cid, "htb", "rate", rate, "ceil", rate, "burst", "32k")
}

// addFilter installs the u32 classifying filter that directs the guest's packets into the class. This is the
// step that makes a prepared class start forwarding.
func (c *Client) addFilter(ctx context.Context, ifc, cid, matchField string, ip net.IP) error {
	pref := fmt.Sprintf("%d", filterPrefFor(ip))
	return c.run(ctx, "filter", "add", "dev", ifc, "protocol", "ip",
		"parent", RootParent, "pref", pref, "u32",
		"match", "ip", matchField, ip.String()+"/32", "flowid", cid)
}

// removeFilter deletes just the classifying filter (by its per-IP pref), leaving the class in place. Removing
// the filter stops forwarding into the class without destroying its counters.
func (c *Client) removeFilter(ctx context.Context, ifc string, ip net.IP) error {
	pref := fmt.Sprintf("%d", filterPrefFor(ip))
	return c.run(ctx, "filter", "del", "dev", ifc, "parent", RootParent,
		"pref", pref, "protocol", "ip", "u32")
}

func (c *Client) removeClassAndFilter(ctx context.Context, ifc, cid string, ip net.IP) error {
	// Remove the filter first (otherwise class delete is EBUSY). We add with a
	// deterministic pref per-IP so we can delete by pref without listing.
	pref := fmt.Sprintf("%d", filterPrefFor(ip))
	_ = c.run(ctx, "filter", "del", "dev", ifc, "parent", RootParent,
		"pref", pref, "protocol", "ip", "u32")
	_ = c.run(ctx, "qdisc", "del", "dev", ifc, "parent", cid)
	_ = c.run(ctx, "class", "del", "dev", ifc, "classid", cid)
	return nil
}

// ClassBytes is a point-in-time counter for one HTB class on one device.
type ClassBytes struct {
	Bytes   uint64
	Packets uint64
}

// classHeaderRe matches the leading line of a tc class stanza:
//
//	"class htb 1:10cd parent 1: ..."
var classHeaderRe = regexp.MustCompile(`^class htb (\w+):([0-9a-f]+)\b`)

// sentRe matches the " Sent N bytes M pkt ..." line inside a class stanza.
var sentRe = regexp.MustCompile(`^\s*Sent\s+(\d+)\s+bytes\s+(\d+)\s+pkt\b`)

// ReadClasses returns the byte/packet counters for every guest-session class on
// a device (a bridge for download, an ifb for upload), keyed by full minor
// (e.g. 0x1067). Non-guest classes and a missing device are ignored/empty.
// Ubuntu 22.04's tc has no JSON for class stats, so we parse the text form.
func (c *Client) ReadClasses(ctx context.Context, device string) (map[int]ClassBytes, error) {
	out, err := c.exec(ctx, c.TCPath, "-s", "class", "show", "dev", device)
	if err != nil {
		// Device may not exist yet (no sessions on this bridge) — treat as empty.
		return map[int]ClassBytes{}, nil
	}
	res := map[int]ClassBytes{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	curMinor := -1
	for sc.Scan() {
		line := sc.Text()
		if m := classHeaderRe.FindStringSubmatch(line); m != nil {
			curMinor = -1
			minor64, err := strconv.ParseInt(m[2], 16, 32)
			if err != nil {
				continue
			}
			minor := int(minor64)
			if minor < GuestMinorBase || minor > GuestMinorMax {
				continue
			}
			curMinor = minor
			res[curMinor] = ClassBytes{}
			continue
		}
		if curMinor < 0 {
			continue
		}
		if m := sentRe.FindStringSubmatch(line); m != nil {
			cb := res[curMinor]
			if b, err := strconv.ParseUint(m[1], 10, 64); err == nil {
				cb.Bytes = b
			}
			if p, err := strconv.ParseUint(m[2], 10, 64); err == nil {
				cb.Packets = p
			}
			res[curMinor] = cb
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

func (c *Client) run(ctx context.Context, args ...string) error {
	out, err := c.exec(ctx, c.TCPath, args...)
	if err != nil {
		return fmt.Errorf("tc %v: %w — %s", args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *Client) ipRun(ctx context.Context, args ...string) error {
	out, err := c.exec(ctx, c.IPPath, args...)
	if err != nil {
		return fmt.Errorf("ip %v: %w — %s", args, err, strings.TrimSpace(string(out)))
	}
	return nil
}
