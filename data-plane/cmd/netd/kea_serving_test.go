package main

import (
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// hexLE renders a dotted-quad the way /proc/net/udp does: little-endian hex. Getting this wrong would make
// the listening check always report "not listening" and restart Kea on every apply.
func TestHexLEMatchesProcNetUdp(t *testing.T) {
	cases := map[string]string{
		"192.168.77.1": "014DA8C0",
		"10.20.0.1":    "0100140A",
		"0.0.0.0":      "00000000",
		"127.0.0.1":    "0100007F",
	}
	for ip, want := range cases {
		if got := hexLE(ip); !strings.EqualFold(got, want) {
			t.Errorf("hexLE(%s) = %s, want %s", ip, got, want)
		}
	}
	if hexLE("not-an-ip") != "" {
		t.Error("a malformed address should render empty, not a bogus match")
	}
}

// Only networks this appliance actually serves DHCP for are expected to have a socket. A relayed or
// externally-served network has no Kea socket by design, and demanding one would restart Kea forever.
func TestOnlyLocallyServedNetworksNeedAKeaSocket(t *testing.T) {
	a := &applier{}
	nets := []netcfg.GuestNetwork{
		{ID: "a", Enabled: true, DHCPMode: netcfg.DHCPLocal, GatewayIP: "192.168.77.1", BridgeName: "br-g-a"},
		{ID: "b", Enabled: true, DHCPMode: "relay", GatewayIP: "192.168.78.1", BridgeName: "br-g-b"},
		{ID: "c", Enabled: false, DHCPMode: netcfg.DHCPLocal, GatewayIP: "192.168.79.1", BridgeName: "br-g-c"},
	}
	missing := a.keaGatewaysNotServed(nets)
	// None of these are really listening in a unit test, so the locally-served one must be reported and the
	// other two must not.
	if len(missing) != 1 || missing[0] != "192.168.77.1" {
		t.Fatalf("only the locally-served, enabled network should be required; got %v", missing)
	}
}
