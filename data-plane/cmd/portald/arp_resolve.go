package main

import (
	"context"
	"net"
	"time"
)

// Resolves the source device's MAC by asking the kernel to resolve the neighbour before concluding it is absent: the neighbour table is a cache, so a miss right after a DHCP lease is not evidence of a foreign device.
// The address still comes only from the kernel, never the client. Resolution runs before the failure-budget pad and well inside it so refusals stay indistinguishable by timing; it has its own sleep seam so the budget keeps exactly one wait.

const (
	// arpResolveWindow sits well inside phase3FailureBudget less the enforcement reserve, so the pad still decides when a refusal is written.
	arpResolveWindow = 400 * time.Millisecond
	arpResolvePoll   = 40 * time.Millisecond
)

// arpNudge sends one datagram to the discard port to make the kernel ARP; a seam for tests.
type arpNudge func(ip net.IP)

// arpSleep is a seam so tests neither sleep nor disturb the budget's clock.
type arpSleep func(d time.Duration)

func defaultArpNudge(ip net.IP) {
	// UDP needs no listener: the kernel must resolve the next hop before sending (port 9 is discard).
	c, err := net.DialTimeout("udp4", net.JoinHostPort(ip.String(), "9"), 50*time.Millisecond)
	if err != nil {
		return
	}
	defer c.Close()
	_ = c.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	_, _ = c.Write([]byte{0})
}

// deviceMAC returns false only when the kernel still cannot place the address after being asked.
func (h *handler) deviceMAC(ctx context.Context, ip net.IP) (net.HardwareAddr, bool) {
	if h.arpCache == nil {
		return nil, false
	}
	if mac, ok := h.arpCache(ip); ok {
		return mac, true
	}
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
