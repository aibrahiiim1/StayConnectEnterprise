package main

// The apply-time validator for /19 client networks: width, overlap with another client network and overlap with the WAN/management
// subnet read from the live address list.

import (
	"context"
	"errors"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

func bigNet(id, name, br, cidr, gw string, vlan int) netcfg.GuestNetwork {
	return netcfg.GuestNetwork{ID: id, Name: name, Enabled: true, NetworkType: "vlan", ParentInterface: "ens192", VLANID: vlan,
		BridgeName: br, GatewayIP: gw, SubnetCIDR: cidr, DHCPMode: netcfg.DHCPLocal, DNSMode: "appliance",
		LeaseDefault: 3600, LeaseMin: 900, LeaseMax: 7200, CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
		Pools: []netcfg.Pool{{StartIP: gw[:len(gw)-1] + "10", EndIP: "10.77.31.200"}}}
}

func codesOf(res netcfg.ValidationResult) map[string]bool {
	m := map[string]bool{}
	for _, i := range res.Issues {
		m[i.Code] = true
	}
	return m
}

func TestApplyAcceptsASlash19AndRefusesWiderOnes(t *testing.T) {
	a := portalApplier(t, newFakeKernel(t), &fakePortalNet{})
	ctx := context.Background()
	ok := bigNet("a", "Big", "br-g50", "10.77.0.0/19", "10.77.0.1", 50)
	if res, _, err := a.validateWithPortal(ctx, []netcfg.GuestNetwork{ok}, nil, nil); err != nil || !res.OK {
		t.Fatalf("a /19 was refused at apply: err=%v %+v", err, res.Issues)
	}
	wide := bigNet("b", "TooBig", "br-g51", "10.76.0.0/18", "10.76.0.1", 51)
	wide.Pools = []netcfg.Pool{{StartIP: "10.76.0.10", EndIP: "10.76.0.200"}}
	if res, _, _ := a.validateWithPortal(ctx, []netcfg.GuestNetwork{wide}, nil, nil); res.OK || !codesOf(res)["subnet_too_large"] {
		t.Fatalf("a /18 was accepted at apply: %+v", res.Issues)
	}
	// a /19 on top of an existing /20 and a /24 inside it
	for _, other := range []string{"10.77.16.0/20", "10.77.31.0/24"} {
		o := bigNet("c", "Inner", "br-g52", other, other[:len(other)-4]+".1", 52)
		o.Pools = nil
		o.DHCPMode = netcfg.DHCPExternal
		if res, _, _ := a.validateWithPortal(ctx, []netcfg.GuestNetwork{ok, o}, nil, nil); res.OK || !codesOf(res)["subnet_overlap"] {
			t.Errorf("a /19 over %s was accepted at apply: %+v", other, res.Issues)
		}
	}
}

// The WAN/management address (192.168.60.25/24 on ens160 in the fixture) is read from the live address list: a /19 covering it is refused, a
// /19 beside it is not, and a client bridge's own address is not treated as the WAN.
func TestApplyRefusesASlash19OverTheWANAndManagementSubnet(t *testing.T) {
	a := portalApplier(t, newFakeKernel(t), &fakePortalNet{})
	ctx := context.Background()
	over := bigNet("w", "OverWAN", "br-g60", "192.168.32.0/19", "192.168.32.1", 60) // 192.168.32.0 - 192.168.63.255 contains 192.168.60.0/24
	over.Pools = []netcfg.Pool{{StartIP: "192.168.32.10", EndIP: "192.168.32.200"}}
	res, _, err := a.validateWithPortal(ctx, []netcfg.GuestNetwork{over}, nil, nil)
	if err != nil || res.OK || !codesOf(res)["subnet_overlaps_host"] {
		t.Fatalf("a /19 over the management subnet was accepted: err=%v %+v", err, res.Issues)
	}
	beside := bigNet("w", "Beside", "br-g61", "192.168.64.0/19", "192.168.64.1", 61)
	beside.Pools = []netcfg.Pool{{StartIP: "192.168.64.10", EndIP: "192.168.64.200"}}
	if res, _, err := a.validateWithPortal(ctx, []netcfg.GuestNetwork{beside}, nil, nil); err != nil || !res.OK {
		t.Fatalf("a /19 beside the management subnet was refused: err=%v %+v", err, res.Issues)
	}
	// an unreadable address list cannot show an overlap and does not block an apply
	a.hostAddrsFn = func(context.Context) ([]netcfg.HostAddr, error) { return nil, errors.New("ip: netlink busy") }
	if res, _, err := a.validateWithPortal(ctx, []netcfg.GuestNetwork{beside}, nil, nil); err != nil || !res.OK {
		t.Fatalf("an unreadable address list blocked the apply: err=%v %+v", err, res.Issues)
	}
}
