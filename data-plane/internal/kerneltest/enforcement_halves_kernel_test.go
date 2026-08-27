//go:build kernelgate

package kerneltest

// BOTH HALVES, ON ONE GUEST, WITH REAL PACKETS.
//
// The applier calls a Session enforced only when the nft gate is authorizing it AND an accountable tc class is
// classifying its traffic. Every other suite proves that arrangement against fakes, which is the right place
// to prove the ORDERING and the failure handling — but a fake cannot answer the two questions a guest and an
// invoice actually depend on:
//
//	is this device on the internet?
//	is what it sends being counted, on ITS class rather than the bridge's default?
//
// The appliance answered "yes" to neither for two real Room Logins while every proxy for those answers looked
// correct. So this test asks the kernel, with traffic: authorize, meter, send, read the counters back, revoke,
// and confirm the internet goes away.

import (
	"context"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

// TestKernel_EnforcedGuestIsBothAuthorizedAndAccounted drives the two surfaces netd drives, in netd's order —
// accountable class first, packet authorization second — and then measures the result in packets and bytes.
func TestKernel_EnforcedGuestIsBothAuthorizedAndAccounted(t *testing.T) {
	if !ifbOK {
		t.Skip("KG_IFB=0: ifb unavailable on this runner, so the accountable half cannot be driven; " +
			"see LIMITATIONS in the evidence artifact")
	}
	sh, gate := shapeClient(), nftClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() {
		_ = gate.DenyIn(context.Background(), nft.Phase3AuthV4, guestIface, ip)
		_ = sh.AbortSession(context.Background(), guestIface, ip)
	})

	// A guest with neither half has no internet. Establishing that first is what makes the rest evidence
	// rather than coincidence.
	if reaches(t) {
		t.Fatal("the guest reached the WAN before anything authorized them")
	}

	// HALF ONE: the accountable class, staged exactly as netd stages it. Prepared, then activated — a class
	// that classifies before it is accountable is the window this ordering exists to close.
	if err := sh.EnsureBridgeInfra(ctx(t), guestIface); err != nil {
		t.Fatalf("bridge infra: %v", err)
	}
	if err := sh.PrepareSession(ctx(t), guestIface, ip, 8000, 3000); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := sh.ActivateSession(ctx(t), guestIface, ip); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// Metered but not admitted is NOT enforcement, and it is the state a guest sees as "connected, no
	// internet". It must still be offline here.
	if reaches(t) {
		t.Fatal("a guest with a tc class but no nft authorization reached the WAN: the gate is not gating")
	}

	minor, ok := shape.MinorForIP(ip)
	if !ok {
		t.Fatalf("no class minor for %s", guestIP)
	}
	before, err := sh.ReadClasses(ctx(t), guestIface)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := before[minor]; !present {
		t.Fatalf("the accountable class was not installed: %v", before)
	}

	// HALF TWO: packet authorization, leased as netd leases it.
	if err := gate.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 60*time.Second); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if !reaches(t) {
		t.Fatal("a guest with both halves in force cannot reach the WAN")
	}

	// ...and the traffic lands on THIS guest's class. A bridge default class would carry the packets just as
	// happily and count them for nobody, which is how usage silently becomes unattributable. The traffic is
	// generated the same way the rest of this suite generates it, so a runner where ICMP behaves oddly fails
	// every counter test rather than only this one.
	for i := 0; i < 3; i++ {
		if !reaches(t) {
			t.Fatal("the authorized guest stopped reaching the WAN while sending its measured traffic")
		}
	}
	after, err := sh.ReadClasses(ctx(t), guestIface)
	if err != nil {
		t.Fatal(err)
	}
	if after[minor].Bytes <= before[minor].Bytes {
		t.Fatalf("the guest's own class counted no new bytes of real traffic (%d -> %d). The session would "+
			"look enforced and its usage would be attributed to nobody",
			before[minor].Bytes, after[minor].Bytes)
	}
	grew := after[minor].Bytes - before[minor].Bytes
	t.Logf("accountable class %d counted %d bytes of guest traffic", minor, grew)

	// REVOKING ONE HALF ENDS ACCESS. The class stays — teardown is a separate step — and that must not keep
	// the guest online for a moment longer.
	if err := gate.DenyIn(ctx(t), nft.Phase3AuthV4, guestIface, ip); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if reaches(t) {
		t.Fatal("the guest still reaches the WAN after their authorization was revoked")
	}
}
