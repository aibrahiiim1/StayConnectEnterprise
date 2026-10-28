// Package shape owns the per-session HTB shaping classes and byte counters on each guest bridge B.
// Download is an HTB class + u32 dst-filter on B's egress root; upload is redirected from B's ingress to a
// per-bridge IFB device (the WAN only sees the SNAT'd appliance IP) with a src-filter there.
//
// classid minor = 0x1000 + (((ip[2]&0x0f)<<8) | ip[3]) is unique within a guest subnet up to /20, and each
// bridge/ifb serves one guest network, so (device, minor) is globally unique for acctd's attribution.
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
	RootParent     = "1:"
	GuestMinorBase = 0x1000
	GuestMinorMax  = 0x1fff
	// SHARED group classes use their own band above every per-session minor so classids never collide.
	GroupMinorBase = 0x2000
	GroupMinorMax  = 0x2fff
)

type Client struct {
	TCPath string
	IPPath string

	// exec is a field so tests can assert the tc commands without a kernel.
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

func MinorForIP(ip net.IP) (int, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false
	}
	host := (int(ip4[2]&0x0f) << 8) | int(ip4[3])
	return GuestMinorBase + host, true
}

func ClassidForIP(ip net.IP) string {
	minor, ok := MinorForIP(ip)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s:%x", RootMajor, minor)
}

// GroupMinorFor hashes an entitlement id to its SHARED group minor so the applier can name the group from
// the plan alone after a restart. On a collision the applier refuses to install a group whose class belongs
// to another entitlement, rather than pooling two accounts' bandwidth.
func GroupMinorFor(entitlementID string) (int, bool) {
	id := strings.TrimSpace(entitlementID)
	if id == "" {
		return 0, false
	}
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for i := 0; i < len(id); i++ {
		h ^= uint32(id[i])
		h *= prime32
	}
	span := uint32(GroupMinorMax - GroupMinorBase + 1)
	return GroupMinorBase + int(h%span), true
}

func GroupClassid(entitlementID string) string {
	minor, ok := GroupMinorFor(entitlementID)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s:%x", RootMajor, minor)
}

// IFBName maps br-lan -> ifb-lan, br-g301 -> ifb-g301, bounded to IFNAMSIZ (15).
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

// EnsureBridgeInfra is idempotent. It never replaces an existing egress root qdisc, so live per-session
// download classes are preserved.
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
	_ = c.ipRun(ctx, "link", "add", ifb, "type", "ifb") // ignore "exists"
	if err := c.ipRun(ctx, "link", "set", ifb, "up"); err != nil {
		return fmt.Errorf("ifb up %s: %w", ifb, err)
	}
	// Use `add`, not `replace`: the IFB qdisc persists across restarts and `replace root htb` fails on an
	// existing htb root, while `add` is a harmless no-op that preserves live classes.
	_ = c.run(ctx, "qdisc", "add", "dev", ifb, "root", "handle", "1:", "htb", "default", "1")
	// Bridge egress root: add only if absent (replacing would drop live per-session classes). Nothing else
	// primes it, so a boot with no tc state is rebuilt by the first submit.
	_ = c.run(ctx, "qdisc", "add", "dev", bridge, "root", "handle", "1:", "htb", "default", "1")
	// Rebuild ingress so exactly one redirect filter exists (duplicates would double-count upload); deleting
	// it drops only its own filters.
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

// AddSession installs classes and filters in one call; downKbps/upKbps <= 0 mean "no cap".
func (c *Client) AddSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error {
	cid := ClassidForIP(ip)
	if cid == "" {
		return fmt.Errorf("ipv4 required, got %s", ip)
	}
	if err := c.EnsureBridgeInfra(ctx, bridge); err != nil {
		return err
	}
	_ = c.DeleteSession(ctx, bridge, ip)

	const unlimitedKbps = 1_000_000
	upRate := upKbps
	if upRate <= 0 {
		upRate = unlimitedKbps
	}
	downRate := downKbps
	if downRate <= 0 {
		downRate = unlimitedKbps
	}

	ifb := IFBName(bridge)
	if err := c.addClassAndFilter(ctx, bridge, cid, downRate, "dst", ip); err != nil {
		return fmt.Errorf("download add on %s: %w", bridge, err)
	}
	if err := c.addClassAndFilter(ctx, ifb, cid, upRate, "src", ip); err != nil {
		_ = c.removeClassAndFilter(ctx, bridge, cid, ip)
		return fmt.Errorf("upload add on %s: %w", ifb, err)
	}
	return nil
}

// Managed (netd) sessions never use AddSession: no packet may be classified into a class until its counter
// series has a durable accounting origin, so provisioning is staged as Prepare (classes, no filters),
// Activate (filters), Abort (remove and prove gone), RemoveClassification and ReRate.

// PrepareSession creates the download and upload classes WITHOUT forwarding filters, after clearing any
// stale leaf from a crashed attempt.
func (c *Client) PrepareSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error {
	return c.PrepareSessionIn(ctx, bridge, ip, downKbps, upKbps, "")
}

// PrepareSessionIn: empty groupCid is PER_DEVICE (class off the root); non-empty is SHARED, where the
// caller must have created the group and the session becomes a child with a small floor and the group's ceiling.
func (c *Client) PrepareSessionIn(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int, groupCid string) error {
	cid := ClassidForIP(ip)
	if cid == "" {
		return fmt.Errorf("ipv4 required, got %s", ip)
	}
	if err := c.EnsureBridgeInfra(ctx, bridge); err != nil {
		return err
	}
	// Clear any stale class+filter for this slot so preparation starts from nothing forwarding.
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
	parent, downFloor, upFloor := RootParent, downRate, upRate
	if groupCid != "" {
		parent = groupCid
		downFloor, upFloor = sharedFloorKbps(downRate), sharedFloorKbps(upRate)
	}
	if err := c.addClassUnder(ctx, bridge, parent, cid, downFloor, downRate); err != nil {
		return fmt.Errorf("prepare download class on %s: %w", bridge, err)
	}
	if err := c.addClassUnder(ctx, ifb, parent, cid, upFloor, upRate); err != nil {
		// Roll back the download class so a failed prepare leaves no half-class.
		c.removeClassByPref(ctx, bridge, cid, filterPrefFor(ip))
		return fmt.Errorf("prepare upload class on %s: %w", ifb, err)
	}
	return nil
}

// ActivateSession installs both forwarding filters; if the second fails the first is removed, so a session
// never forwards in one direction only.
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

// AbortSession removes classes and filters and proves the class is gone on both devices; an error means
// removal is unproven and the caller should escalate to RemoveClassification.
func (c *Client) AbortSession(ctx context.Context, bridge string, ip net.IP) error {
	// Filters first, so forwarding stops even if a class delete later fails.
	_ = c.removeFilter(ctx, bridge, ip)
	_ = c.removeFilter(ctx, IFBName(bridge), ip)
	_ = c.removeClassAndFilter(ctx, bridge, ClassidForIP(ip), ip)
	_ = c.removeClassAndFilter(ctx, IFBName(bridge), ClassidForIP(ip), ip)
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

// RemoveClassification strips the classification filters in both directions so a leftover class meters
// nothing. It does NOT deny internet access: the guest keeps forwarding, unshaped and uncounted, through the
// default class; only the nft authorization gate denies access. Call it only after packet authorization is removed.
func (c *Client) RemoveClassification(ctx context.Context, bridge string, ip net.IP) error {
	e1 := c.removeFilter(ctx, bridge, ip)
	e2 := c.removeFilter(ctx, IFBName(bridge), ip)
	if e1 != nil {
		return e1
	}
	return e2
}

// ReRateSession uses `tc class change`, never delete+add, so byte counters are preserved and a re-rate does
// not look like a counter reset. It fails if the class is absent. It is the PER_DEVICE re-rate; a SHARED session
// is re-rated with ReRateSessionIn.
func (c *Client) ReRateSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error {
	return c.ReRateSessionIn(ctx, bridge, ip, downKbps, upKbps, "")
}

// ReRateSessionIn re-rates a session with the same shape PrepareSessionIn gave it: empty groupCid is PER_DEVICE
// (rate == ceil == the plan's rate, off the root); a SHARED session keeps its group parent, its small floor as rate
// and the group's rate as ceiling. Re-rating a SHARED session to rate == ceil == the whole allowance would guarantee
// every device the full allowance, since an HTB class within its own rate never borrows from its parent
// (audit Phase 8 M4 / Phase 6 M4).
func (c *Client) ReRateSessionIn(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int, groupCid string) error {
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
	parent, downFloor, upFloor := RootParent, downRate, upRate
	if groupCid != "" {
		parent = groupCid
		downFloor, upFloor = sharedFloorKbps(downRate), sharedFloorKbps(upRate)
	}
	if err := c.changeClassUnder(ctx, bridge, parent, cid, downFloor, downRate); err != nil {
		return fmt.Errorf("re-rate download on %s: %w", bridge, err)
	}
	if err := c.changeClassUnder(ctx, IFBName(bridge), parent, cid, upFloor, upRate); err != nil {
		return fmt.Errorf("re-rate upload on %s: %w", IFBName(bridge), err)
	}
	return nil
}

// SessionForwarding reports whether the forwarding filter is installed in both directions (post-activation check).
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

func (c *Client) filterPresent(ctx context.Context, dev string, ip net.IP) (bool, error) {
	out, err := c.exec(ctx, c.TCPath, "filter", "show", "dev", dev, "parent", RootParent)
	if err != nil {
		// A missing device has no filters: definitely not forwarding.
		return false, nil
	}
	want := fmt.Sprintf("pref %d ", filterPrefFor(ip))
	return bytes.Contains(out, []byte(want)), nil
}

func (c *Client) DeleteSession(ctx context.Context, bridge string, ip net.IP) error {
	cid := ClassidForIP(ip)
	if cid == "" {
		return nil
	}
	_ = c.removeClassAndFilter(ctx, bridge, cid, ip)
	_ = c.removeClassAndFilter(ctx, IFBName(bridge), cid, ip)
	return nil
}

// DeleteSessionClass removes a managed class by minor, for reconciling classes whose session (and IP) is
// no longer known.
func (c *Client) DeleteSessionClass(ctx context.Context, bridge string, minor int) error {
	if minor < GuestMinorBase || minor > GuestMinorMax {
		// The root and default classes are infrastructure; removing them would take the bridge down.
		return fmt.Errorf("minor %d is outside the managed guest class range", minor)
	}
	cid := fmt.Sprintf("%s:%x", RootMajor, minor)
	unlock := c.lockBridge(bridge)
	defer unlock()
	c.removeClassByPref(ctx, bridge, cid, minor)
	c.removeClassByPref(ctx, IFBName(bridge), cid, minor)
	return nil
}

func (c *Client) removeClassByPref(ctx context.Context, ifc, cid string, pref int) {
	_ = c.run(ctx, "filter", "del", "dev", ifc, "parent", RootParent,
		"pref", strconv.Itoa(pref), "protocol", "ip", "u32")
	_ = c.run(ctx, "qdisc", "del", "dev", ifc, "parent", cid)
	_ = c.run(ctx, "class", "del", "dev", ifc, "classid", cid)
}

// filterPrefFor is unique within a device because each device serves one subnet.
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

// addClass creates the class and leaf qdisc without a filter, so it receives no packets.
func (c *Client) addClass(ctx context.Context, ifc, cid string, kbps int) error {
	return c.addClassUnder(ctx, ifc, RootParent, cid, kbps, kbps)
}

// addClassUnder takes separate rate and ceiling because SHARED depends on it: PER_DEVICE is rate == ceil,
// while a SHARED session has a small guaranteed floor and the group's ceiling, so HTB lends spare capacity
// dynamically instead of throttling a guest while their idle device holds a fixed slice.
func (c *Client) addClassUnder(ctx context.Context, ifc, parent, cid string, rateKbps, ceilKbps int) error {
	rate := fmt.Sprintf("%dkbit", rateKbps)
	ceil := fmt.Sprintf("%dkbit", ceilKbps)
	if err := c.run(ctx, "class", "add", "dev", ifc, "parent", parent,
		"classid", cid, "htb", "rate", rate, "ceil", ceil, "burst", "32k"); err != nil {
		return err
	}
	minor := strings.SplitN(cid, ":", 2)[1]
	if err := c.run(ctx, "qdisc", "add", "dev", ifc, "parent", cid,
		"handle", minor+":", "fq_codel"); err != nil {
		return err
	}
	return nil
}

// EnsureGroupClass creates or re-rates a SHARED group's aggregate class. `class add` fails on an existing
// classid, so it re-rates in place, keeping counters. The group has no filter of its own.
func (c *Client) EnsureGroupClass(ctx context.Context, ifc, groupCid string, kbps int) error {
	if err := c.addClassUnder(ctx, ifc, RootParent, groupCid, kbps, kbps); err == nil {
		return nil
	}
	// Already there (or unusable): re-rate; a genuinely broken class surfaces here.
	return c.run(ctx, "class", "change", "dev", ifc, "parent", RootParent,
		"classid", groupCid, "htb", "rate", fmt.Sprintf("%dkbit", kbps), "ceil", fmt.Sprintf("%dkbit", kbps),
		"burst", "32k")
}

// sharedFloorKbps is a session's guaranteed rate inside a SHARED group. It is deliberately small, NOT the
// aggregate divided by device count: dividing would reserve capacity per device and cap an active guest at
// their share while idle devices held the rest.
func sharedFloorKbps(groupKbps int) int {
	const floor = 64
	if groupKbps <= floor {
		return groupKbps
	}
	if tenth := groupKbps / 10; tenth > floor {
		return tenth
	}
	return floor
}

// changeClassUnder re-rates in place; `class change` preserves byte counters, delete+add would reset them.
func (c *Client) changeClassUnder(ctx context.Context, ifc, parent, cid string, rateKbps, ceilKbps int) error {
	return c.run(ctx, "class", "change", "dev", ifc, "parent", parent,
		"classid", cid, "htb", "rate", fmt.Sprintf("%dkbit", rateKbps), "ceil", fmt.Sprintf("%dkbit", ceilKbps), "burst", "32k")
}

// addFilter installs the filter that makes a prepared class start carrying the guest's packets.
func (c *Client) addFilter(ctx context.Context, ifc, cid, matchField string, ip net.IP) error {
	pref := fmt.Sprintf("%d", filterPrefFor(ip))
	return c.run(ctx, "filter", "add", "dev", ifc, "protocol", "ip",
		"parent", RootParent, "pref", pref, "u32",
		"match", "ip", matchField, ip.String()+"/32", "flowid", cid)
}

// removeFilter deletes only the filter, stopping forwarding into the class while keeping its counters.
func (c *Client) removeFilter(ctx context.Context, ifc string, ip net.IP) error {
	pref := fmt.Sprintf("%d", filterPrefFor(ip))
	return c.run(ctx, "filter", "del", "dev", ifc, "parent", RootParent,
		"pref", pref, "protocol", "ip", "u32")
}

func (c *Client) removeClassAndFilter(ctx context.Context, ifc, cid string, ip net.IP) error {
	// Filter first, otherwise the class delete is EBUSY.
	pref := fmt.Sprintf("%d", filterPrefFor(ip))
	_ = c.run(ctx, "filter", "del", "dev", ifc, "parent", RootParent,
		"pref", pref, "protocol", "ip", "u32")
	_ = c.run(ctx, "qdisc", "del", "dev", ifc, "parent", cid)
	_ = c.run(ctx, "class", "del", "dev", ifc, "classid", cid)
	return nil
}

type ClassBytes struct {
	Bytes   uint64
	Packets uint64
}

// classHeaderRe matches e.g. "class htb 1:10cd parent 1: ...".
var classHeaderRe = regexp.MustCompile(`^class htb (\w+):([0-9a-f]+)\b`)

var sentRe = regexp.MustCompile(`^\s*Sent\s+(\d+)\s+bytes\s+(\d+)\s+pkt\b`)

// ReadClasses returns counters for every guest-session class on a device, keyed by full minor. tc has no
// JSON for class stats, so the text form is parsed.
func (c *Client) ReadClasses(ctx context.Context, device string) (map[int]ClassBytes, error) {
	out, err := c.exec(ctx, c.TCPath, "-s", "class", "show", "dev", device)
	if err != nil {
		// Only an absent device means "no classes". Any other failure must be an error: treating it as empty
		// made reconciliation re-create every class (new epoch, restarted counters, lost bytes).
		if deviceAbsent(out) {
			return map[int]ClassBytes{}, nil
		}
		return nil, fmt.Errorf("tc class show dev %s: %w — %s", device, err, strings.TrimSpace(string(out)))
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

func deviceAbsent(out []byte) bool {
	return bytes.Contains(out, []byte("Cannot find device"))
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
