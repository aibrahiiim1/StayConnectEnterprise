package shape

// Per-session classification filters.
//
// The original scheme installed one u32 filter per session, each at its own priority (pref = class minor). Measured on a real
// kernel (docs/reviews/SHAPING_SCALE_BEYOND_2047.md) that has two limits: the kernel refuses the 2,048th priority on a device
// ("Filter already exists"), and every priority is a separate classifier the packet walks in turn, so the per-packet cost grows
// linearly (17,000 packets/s with 1,900 sessions against 260,000 with none).
//
// The current scheme installs one flower filter per session and direction, all at ONE priority (FlowerPref), with the class
// minor as the filter handle. Flower groups filters of the same match mask in one hash table, so a packet costs one lookup
// whatever the number of sessions, and a filter is addressed by its handle (create, delete, presence) without any table to
// size. A kernel without cls_flower falls back to the original scheme, with its original limit; NETD_SHAPE_CLASSIFIER=u32
// forces that fallback (rollback switch).

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	// FlowerPref is the priority every flower session filter shares. It is below any u32 pref (those are the class minors,
	// 0x1000 and up), so during an upgrade a flower filter is consulted first and a not-yet-replaced u32 filter still works.
	FlowerPref = 1
	// probeHandle is the filter handle the capability probe uses; it is below every guest minor.
	probeHandle = 0xfff
)

const (
	clsUnknown = iota
	clsFlower
	clsU32
)

type classifierState struct {
	mu   sync.Mutex
	mode int
}

// classifierFromEnv reads the rollback switch.
func classifierFromEnv() int {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("NETD_SHAPE_CLASSIFIER")), "u32") {
		return clsU32
	}
	return clsUnknown
}

// unsupportedClassifier recognises the kernel's refusal of a classifier it does not have.
var unsupportedClassifier = regexp.MustCompile(`(?i)(kind not found|unknown filter|unable to load classifier|classifier .*not (found|supported)|not supported)`)

// classifier returns the scheme to use, probing the kernel once on the given device (which must have its HTB root).
func (c *Client) classifier(ctx context.Context, dev string) int {
	c.cls.mu.Lock()
	defer c.cls.mu.Unlock()
	if c.cls.mode != clsUnknown {
		return c.cls.mode
	}
	if m := classifierFromEnv(); m != clsUnknown {
		c.cls.mode = m
		slog.Warn("shape: classifier forced to u32 by NETD_SHAPE_CLASSIFIER; limited to 2,047 sessions per bridge")
		return m
	}
	add := []string{"filter", "add", "dev", dev, "protocol", "ip", "parent", RootParent, "pref", strconv.Itoa(FlowerPref),
		"handle", fmt.Sprintf("0x%x", probeHandle), "flower", "dst_ip", "192.0.2.1/32", "classid", RootMajor + ":" + strconv.Itoa(probeHandle), "skip_hw"}
	out, err := c.exec(ctx, c.TCPath, add...)
	switch {
	case err == nil || bytes.Contains(out, []byte("already exists")):
		_, _ = c.exec(ctx, c.TCPath, "filter", "del", "dev", dev, "protocol", "ip", "parent", RootParent,
			"pref", strconv.Itoa(FlowerPref), "handle", fmt.Sprintf("0x%x", probeHandle), "flower")
		c.cls.mode = clsFlower
	case unsupportedClassifier.Match(out):
		slog.Warn("shape: kernel has no flower classifier; using one u32 filter per session (limit 2,047 per bridge)", "detail", strings.TrimSpace(string(out)))
		c.cls.mode = clsU32
	default:
		// Not a statement about the kernel (device or qdisc not ready): decide on the next call, and use the safe scheme for this one.
		slog.Warn("shape: classifier probe inconclusive", "dev", dev, "detail", strings.TrimSpace(string(out)))
		return clsU32
	}
	return c.cls.mode
}

// minorOfCid returns the minor of "1:xxxx".
func minorOfCid(cid string) int {
	i := strings.IndexByte(cid, ':')
	if i < 0 {
		return 0
	}
	m, _ := strconv.ParseInt(cid[i+1:], 16, 32)
	return int(m)
}

func flowerAddArgs(ifc, cid, field string, ip net.IP) []string {
	return []string{"filter", "add", "dev", ifc, "protocol", "ip", "parent", RootParent, "pref", strconv.Itoa(FlowerPref),
		"handle", fmt.Sprintf("0x%x", minorOfCid(cid)), "flower", field + "_ip", ip.String() + "/32", "classid", cid, "skip_hw"}
}

func u32AddArgs(ifc, cid, field string, ip net.IP) []string {
	return []string{"filter", "add", "dev", ifc, "protocol", "ip", "parent", RootParent, "pref", strconv.Itoa(minorOfCid(cid)), "u32",
		"match", "ip", field, ip.String() + "/32", "flowid", cid}
}

func flowerDelArgs(ifc string, minor int) []string {
	return []string{"filter", "del", "dev", ifc, "protocol", "ip", "parent", RootParent, "pref", strconv.Itoa(FlowerPref),
		"handle", fmt.Sprintf("0x%x", minor), "flower"}
}

func u32DelArgs(ifc string, minor int) []string {
	return []string{"filter", "del", "dev", ifc, "parent", RootParent, "pref", strconv.Itoa(minor), "protocol", "ip", "u32"}
}

// filterAddArgs is the tc command that classifies packets to/from ip (field "dst" on the bridge, "src" on the IFB) into cid.
func (c *Client) filterAddArgs(ctx context.Context, ifc, cid, field string, ip net.IP) []string {
	if c.classifier(ctx, ifc) == clsFlower {
		return flowerAddArgs(ifc, cid, field, ip)
	}
	return u32AddArgs(ifc, cid, field, ip)
}

// filterDelArgs lists the commands that remove a class's filter, in the order to try them: the current scheme's first, then the
// other one's, so a session classified before an upgrade (or before a rollback switch) is released as well.
func (c *Client) filterDelArgs(ctx context.Context, ifc string, minor int) [][]string {
	if c.classifier(ctx, ifc) == clsFlower {
		return [][]string{flowerDelArgs(ifc, minor), u32DelArgs(ifc, minor)}
	}
	return [][]string{u32DelArgs(ifc, minor), flowerDelArgs(ifc, minor)}
}

func joinArgs(a []string) string { return strings.Join(a, " ") }

var (
	flowerHandleRe = regexp.MustCompile(`\bflower\b.*\bhandle 0x([0-9a-f]+)\b`)
	u32PrefRe      = regexp.MustCompile(`\bpref (\d+) u32\b`)
)

// minorsWithFilter returns the class minors that have a forwarding filter installed on a device, in either scheme. A missing device
// has none. A single dump serves a whole batch of questions.
func (c *Client) minorsWithFilter(ctx context.Context, dev string) map[int]bool {
	out, err := c.exec(ctx, c.TCPath, "filter", "show", "dev", dev, "parent", RootParent)
	if err != nil {
		return map[int]bool{} // a missing device has no filters: definitely not forwarding
	}
	res := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if m := flowerHandleRe.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseInt(m[1], 16, 32); err == nil && IsGuestMinor(int(v)) {
				res[int(v)] = true
			}
			continue
		}
		if m := u32PrefRe.FindStringSubmatch(line); m != nil {
			if v, err := strconv.Atoi(m[1]); err == nil && IsGuestMinor(v) {
				res[v] = true
			}
		}
	}
	return res
}
