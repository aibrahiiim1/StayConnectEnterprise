package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// A tagged client network's LIVE bridge takes its parent NIC's MAC address (VLAN 90 and VLAN 3: a phone got a lease by
// broadcast and then no unicast reached the appliance, because the kernel gave the bridge a random MAC).
func TestTaggedBridgeTakesTheParentsMACLive(t *testing.T) {
	macs := map[string]string{"ens192": "00:0c:29:6f:39:53", "br-g3": "1a:f5:c0:e4:3c:d5", "br-g90": "00:0c:29:6f:39:53", "br-gu": "fe:28:52:7c:b7:bd"}
	oldP, oldB := parentMACOf, bridgeMACOf
	parentMACOf, bridgeMACOf = func(i string) string { return macs[i] }, func(i string) string { return macs[i] }
	defer func() { parentMACOf, bridgeMACOf = oldP, oldB }()

	var cmds []string
	a := newTestApplier(t, newFakeKernel(t))
	a.runFn = func(_ context.Context, name string, args ...string) error {
		cmds = append(cmds, name+" "+strings.Join(args, " "))
		return nil
	}
	vlan := func(br string, id int) netcfg.GuestNetwork {
		return netcfg.GuestNetwork{NetworkType: "vlan", ParentInterface: "ens192", VLANID: id, BridgeName: br}
	}

	if changed, err := a.reconcileBridgeMAC(context.Background(), vlan("br-g3", 3)); err != nil || !changed {
		t.Fatalf("a bridge with a random MAC is corrected: %v %v", changed, err)
	}
	if len(cmds) != 1 || cmds[0] != "ip link set dev br-g3 address 00:0c:29:6f:39:53" {
		t.Fatalf("commands: %v", cmds)
	}
	cmds = nil
	if changed, _ := a.reconcileBridgeMAC(context.Background(), vlan("br-g90", 90)); changed || len(cmds) != 0 {
		t.Fatalf("a bridge that already has the MAC is left alone: %v", cmds)
	}
	untagged := netcfg.GuestNetwork{NetworkType: "untagged", ParentInterface: "ens192", BridgeName: "br-gu"}
	if changed, _ := a.reconcileBridgeMAC(context.Background(), untagged); changed || len(cmds) != 0 {
		t.Fatalf("an untagged network is never touched: %v", cmds)
	}
	for _, bad := range []string{"", "not-a-mac", "00:00:00:00:00:00", "01:00:5e:00:00:fb"} {
		macs["ens192"] = bad
		if changed, _ := a.reconcileBridgeMAC(context.Background(), vlan("br-g3", 3)); changed || len(cmds) != 0 {
			t.Fatalf("an unusable parent MAC %q must change nothing: %v", bad, cmds)
		}
	}
	macs["ens192"], macs["br-g3"] = "00:0c:29:6f:39:53", ""
	if changed, _ := a.reconcileBridgeMAC(context.Background(), vlan("br-g3", 3)); changed || len(cmds) != 0 {
		t.Fatalf("an unreadable bridge changes nothing: %v", cmds)
	}
}
