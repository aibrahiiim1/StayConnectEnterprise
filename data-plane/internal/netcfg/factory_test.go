package netcfg

import (
	"net"
	"testing"
)

// The factory network is the one the Product Owner specified, and it passes the same validation as any network an operator creates.
func TestFactoryNetworkIsTheSpecifiedOneAndValid(t *testing.T) {
	n := Factory("ens192")
	n.BridgeName = BridgeNameFor("untagged", 0, "factory")
	if n.SubnetCIDR != "10.20.0.0/19" || n.GatewayIP != "10.20.0.1" || n.NetworkType != "untagged" || n.DHCPMode != DHCPLocal {
		t.Fatalf("%+v", n)
	}
	if len(n.Pools) != 1 || n.Pools[0].StartIP != "10.20.0.10" || n.Pools[0].EndIP != "10.20.31.254" {
		t.Fatalf("pool %+v", n.Pools)
	}
	if iss := ValidateOne(n, topoT(), map[string]bool{"ens192": true}, nil, ""); len(iss) != 0 {
		t.Fatalf("the factory network does not validate: %+v", iss)
	}
	if !n.CaptiveEnabled || !n.InternetEnabled || !n.NATEnabled || n.DNSMode != "appliance" {
		t.Fatalf("default services are not prepared: %+v", n)
	}
	if got := poolSizeOf(n.Pools); got != 8181 {
		t.Fatalf("pool holds %d addresses, want 8181", got)
	}
}

// The portal address candidates must not fall inside the factory subnet, or activating the network would break the portal.
func TestFactorySubnetDoesNotContainAPortalAddressCandidate(t *testing.T) {
	for _, c := range []string{"10.255.255.1", "10.254.254.1", "10.253.253.1", "10.252.252.1"} {
		if iss := ValidatePortalIP(c, []GuestNetwork{Factory("ens192")}, nil); len(iss) != 0 {
			t.Fatalf("%s conflicts with the factory network: %+v", c, iss)
		}
	}
}

func poolSizeOf(p []Pool) int {
	total := 0
	for _, x := range p {
		s, _ := ipToUint32(net.ParseIP(x.StartIP))
		e, _ := ipToUint32(net.ParseIP(x.EndIP))
		total += int(e-s) + 1
	}
	return total
}
