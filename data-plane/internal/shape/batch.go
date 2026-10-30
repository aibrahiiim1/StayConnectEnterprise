package shape

// Batched tc operations for the managed sessions.
//
// Every tc invocation is a forked process (10-25 ms on the appliance), so the per-session calls cost O(N) forks per pass. `tc -batch -` reads
// many commands from one process. It is NOT a transaction: it stops at the first failing command and reports the line, leaving earlier lines
// applied. The batch calls therefore return an error for the whole batch and the caller falls back to the per-session path, which begins by
// clearing the session's slot (PrepareSessionIn) and so converges from any partial state; that is also how the failing session is attributed.

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
)

// BatchChunk bounds how many sessions one tc batch carries.
const BatchChunk = 32

// PrepItem is one PER_DEVICE session to prepare (class and leaf, no filters).
type PrepItem struct {
	IP       net.IP
	DownKbps int
	UpKbps   int
}

func (c *Client) tcBatch(ctx context.Context, force bool, lines []string) ([]byte, error) {
	if len(lines) == 0 {
		return nil, nil
	}
	if c.execIn == nil {
		return nil, fmt.Errorf("tc batch is not available")
	}
	args := []string{"-batch", "-"}
	if force {
		args = []string{"-force", "-batch", "-"}
	}
	out, err := c.execIn(ctx, []byte(strings.Join(lines, "\n")+"\n"), c.TCPath, args...)
	if err != nil {
		return out, fmt.Errorf("tc batch (%d commands): %w — %s", len(lines), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func classAddLines(ifc, parent, cid string, rateKbps, ceilKbps int) []string {
	minor := strings.SplitN(cid, ":", 2)[1]
	return []string{
		fmt.Sprintf("class add dev %s parent %s classid %s htb rate %dkbit ceil %dkbit burst 32k", ifc, parent, cid, rateKbps, ceilKbps),
		fmt.Sprintf("qdisc add dev %s parent %s handle %s: fq_codel flows %d", ifc, cid, minor, LeafFlows),
	}
}

// PrepareBatch creates the download and upload class of each item without a forwarding filter, assuming the slots are free (the caller has read
// that none is installed). It is PrepareSessionIn for PER_DEVICE sessions, in one process.
func (c *Client) PrepareBatch(ctx context.Context, bridge string, items []PrepItem) error {
	if err := c.EnsureBridgeInfra(ctx, bridge); err != nil {
		return err
	}
	const unlimitedKbps = 1_000_000
	ifb := IFBName(bridge)
	var lines []string
	for _, it := range items {
		cid := ClassidFor(bridge, it.IP)
		if cid == "" {
			return fmt.Errorf("ipv4 required, got %s", it.IP)
		}
		down, up := it.DownKbps, it.UpKbps
		if down <= 0 {
			down = unlimitedKbps
		}
		if up <= 0 {
			up = unlimitedKbps
		}
		lines = append(lines, classAddLines(bridge, RootParent, cid, down, down)...)
		lines = append(lines, classAddLines(ifb, RootParent, cid, up, up)...)
	}
	_, err := c.tcBatch(ctx, false, lines)
	return err
}

// ActivateBatch installs both forwarding filters of each address (ActivateSession, in one process).
func (c *Client) ActivateBatch(ctx context.Context, bridge string, ips []net.IP) error {
	ifb := IFBName(bridge)
	var lines []string
	for _, ip := range ips {
		cid := ClassidFor(bridge, ip)
		if cid == "" {
			return fmt.Errorf("ipv4 required, got %s", ip)
		}
		lines = append(lines,
			joinArgs(c.filterAddArgs(ctx, bridge, cid, "dst", ip)),
			joinArgs(c.filterAddArgs(ctx, ifb, cid, "src", ip)))
	}
	_, err := c.tcBatch(ctx, false, lines)
	return err
}

// ForwardingBatch reports, per address, whether the forwarding filter is installed in both directions, from one read of each device.
func (c *Client) ForwardingBatch(ctx context.Context, bridge string, ips []net.IP) (map[string]bool, error) {
	down, up := c.minorsWithFilter(ctx, bridge), c.minorsWithFilter(ctx, IFBName(bridge))
	res := make(map[string]bool, len(ips))
	for _, ip := range ips {
		m, ok := MinorFor(bridge, ip)
		res[ip.String()] = ok && down[m] && up[m]
	}
	return res, nil
}

// DeleteClassesBatch removes the classes (and their filters and leaves) of the given minors on the bridge and its IFB. Filters go first on both
// devices, so forwarding stops even if a class delete later fails. Individual refusals are ignored, as DeleteSessionClass ignores them: the
// caller proves removal with a read.
func (c *Client) DeleteClassesBatch(ctx context.Context, bridge string, minors []int) error {
	ifb := IFBName(bridge)
	var filters, rest []string
	for _, m := range minors {
		if !IsGuestMinor(m) {
			return fmt.Errorf("minor %d is outside the managed guest class range", m)
		}
		cid := classidOf(m)
		for _, dev := range []string{bridge, ifb} {
			for _, a := range c.filterDelArgs(ctx, dev, m) {
				filters = append(filters, joinArgs(a))
			}
			rest = append(rest,
				fmt.Sprintf("qdisc del dev %s parent %s", dev, cid),
				fmt.Sprintf("class del dev %s classid %s", dev, cid))
		}
	}
	unlock := c.lockBridge(bridge)
	defer unlock()
	out, err := c.tcBatch(ctx, true, append(filters, rest...))
	if err != nil && !bytes.Contains(out, []byte("Command failed")) && !bytes.Contains(out, []byte("RTNETLINK")) {
		// Not a refused command: the process itself did not run.
		return err
	}
	return nil
}
