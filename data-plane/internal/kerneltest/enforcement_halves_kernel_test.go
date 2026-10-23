//go:build kernelgate

package kerneltest

// Both halves on one guest with real packets: a Session is enforced only when the nft gate authorizes it and an
// accountable tc class classifies it; only the kernel can show the guest is online and metered on its own class.

import (
	"context"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

// Drives netd's two surfaces in netd's order (class first, authorization second) and measures packets and bytes.
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

	if reaches(t) {
		t.Fatal("the guest reached the WAN before anything authorized them")
	}

	// Half one: the accountable class, prepared then activated; classifying before it is accountable is the window
	// this order closes.
	if err := sh.EnsureBridgeInfra(ctx(t), guestIface); err != nil {
		t.Fatalf("bridge infra: %v", err)
	}
	if err := sh.PrepareSession(ctx(t), guestIface, ip, 8000, 3000); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := sh.ActivateSession(ctx(t), guestIface, ip); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// Metered but not admitted is not enforcement and must still be offline.
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

	if err := gate.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 60*time.Second); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if !reaches(t) {
		t.Fatal("a guest with both halves in force cannot reach the WAN")
	}

	// Traffic must land on this guest's class; a bridge default class would carry it and count it for nobody.
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

	// Revoking one half ends access even though the class stays.
	if err := gate.DenyIn(ctx(t), nft.Phase3AuthV4, guestIface, ip); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if reaches(t) {
		t.Fatal("the guest still reaches the WAN after their authorization was revoked")
	}
}
