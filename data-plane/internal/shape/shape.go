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

	mu         sync.Mutex
	infraReady map[string]bool // bridge -> ingress/IFB redirect established this process
	bridgeMu   map[string]*sync.Mutex
}

func New() *Client {
	return &Client{
		TCPath:     "/usr/sbin/tc",
		IPPath:     "/usr/sbin/ip",
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
	pref := fmt.Sprintf("%d", filterPrefFor(ip))
	if err := c.run(ctx, "filter", "add", "dev", ifc, "protocol", "ip",
		"parent", RootParent, "pref", pref, "u32",
		"match", "ip", matchField, ip.String()+"/32", "flowid", cid); err != nil {
		return err
	}
	return nil
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
	out, err := exec.CommandContext(ctx, c.TCPath, "-s", "class", "show", "dev", device).Output()
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
	cmd := exec.CommandContext(ctx, c.TCPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tc %v: %w — %s", args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *Client) ipRun(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, c.IPPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %v: %w — %s", args, err, strings.TrimSpace(string(out)))
	}
	return nil
}
