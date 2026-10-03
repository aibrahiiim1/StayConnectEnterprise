package main

import (
	"context"
	"net"
	"time"
)

// RESOLVING THE SOURCE DEVICE'S HARDWARE ADDRESS, WITHOUT ASKING THE DEVICE.
//
// THE DEFECT THIS CLOSES, found on PRE-LIVE during a Product-Owner test. The MAC behind a sign-in used to come
// from a single read of /proc/net/arp, and a miss was treated as "this device is not on a guest network" --
// answered with the technical refusal, which tells the guest to contact the front desk. But a miss does not mean
// that. The kernel's neighbour table is a CACHE: an entry is absent before the kernel has had reason to resolve
// the address, and it is reaped again when it goes unused. A client that has just taken a DHCP lease and
// submitted the form immediately is exactly the case that finds no entry -- and on the appliance where this was
// found, a large share of the neighbour entries for the guest subnet were sitting in FAILED state at the time.
//
// So a legitimate guest could be refused for a reason that had nothing to do with them, be told to go to
// Reception, and try again a moment later and succeed. That is the "repeated, unexplainable" refusal.
//
// WHAT THIS DOES NOT DO. It does not accept a hardware address from the client, and it does not weaken the
// check. The address still comes only from the kernel's own neighbour table. The one thing added is that the
// appliance now ASKS the kernel to resolve the neighbour before concluding that it cannot, which is what any
// other service on this box would do implicitly by simply sending the device a packet.
//
// WHY THE TIMING IS STILL UNIFORM. Every guest-visible non-success leaves at a fixed wall-clock offset
// (phase3FailureBudget, padded by phase3Budget.wait to an ABSOLUTE deadline). This resolution happens before that
// pad and is bounded well inside it, so a refusal that waited for a neighbour lookup and one that did not are
// indistinguishable to a caller with a stopwatch. A SUCCESS may take longer than it used to when the cache was
// cold; a success is already distinguishable from a refusal by its content, so that leaks nothing.
//
// It deliberately does NOT sleep on the budget's clock. The budget's single pad is the one wait the timing
// property is expressed and tested in terms of ("the handler waits out the budget exactly once"), and borrowing
// that clock here would add ten more waits to something whose whole value is being countable. This has its own
// sleep seam instead, so the budget still has exactly one.

const (
	// arpResolveWindow bounds the whole attempt. It sits far inside phase3FailureBudget (2500ms) less the
	// enforcement reserve (200ms), so the pad still decides when a refusal is written.
	arpResolveWindow = 400 * time.Millisecond
	// arpResolvePoll is how often the neighbour table is re-read while the kernel resolves.
	arpResolvePoll = 40 * time.Millisecond
)

// arpNudge asks the kernel to resolve a neighbour. The default sends one datagram to the discard port, which is
// enough to make the kernel ARP for the address; nothing is expected to receive it and nothing reads a reply.
// It is a seam so a test can drive resolution without a network.
type arpNudge func(ip net.IP)

// arpSleep is how the resolver waits between re-reads. A seam so a test neither sleeps for real nor disturbs the
// response-time budget's own clock.
type arpSleep func(d time.Duration)

func defaultArpNudge(ip net.IP) {
	// UDP needs no handshake and no listener on the other side: the kernel must resolve the next hop before it
	// can put the datagram on the wire, which is the entire purpose of sending it. Port 9 is discard.
	c, err := net.DialTimeout("udp4", net.JoinHostPort(ip.String(), "9"), 50*time.Millisecond)
	if err != nil {
		return
	}
	defer c.Close()
	_ = c.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	_, _ = c.Write([]byte{0})
}

// deviceMAC returns the hardware address the kernel has for ip, prompting resolution if it has none yet.
//
// The second return is false only when the kernel still cannot place the address after being asked -- which is
// the genuine "this device is not reachable on a guest network" case the refusal was always meant to describe.
func (h *handler) deviceMAC(ctx context.Context, ip net.IP) (net.HardwareAddr, bool) {
	if h.arpCache == nil {
		return nil, false
	}
	if mac, ok := h.arpCache(ip); ok {
		return mac, true
	}
	// Nothing cached. Ask the kernel, then watch for the entry to appear.
	if h.arpNudge != nil {
		h.arpNudge(ip)
	}
	sleep := h.arpSleep
	if sleep == nil {
		sleep = time.Sleep
	}
	for waited := time.Duration(0); waited < arpResolveWindow; waited += arpResolvePoll {
		if ctx.Err() != nil {
			return nil, false
		}
		sleep(arpResolvePoll)
		if mac, ok := h.arpCache(ip); ok {
			return mac, true
		}
	}
	return nil, false
}
