package netcfg

// /19 client network support (Product Owner decision, 2026-10-30): the widest client network is /19 = 8190 usable ADDRESSES. That is
// address capacity of one broadcast domain, not a claim of 8190 simultaneously served clients.

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
)

func baseOf(prefix int) string { return fmt.Sprintf("10.20.0.0/%d", prefix) }

// Every prefix from /8 to /32: /19../30 are accepted as a subnet, wider is subnet_too_large, narrower is subnet_too_small.
func TestEveryPrefixBoundary(t *testing.T) {
	for p := 8; p <= 32; p++ {
		iss := CheckClientSubnet(baseOf(p))
		codes := issueCodes(iss)
		switch {
		case p < 19:
			if !codes["subnet_too_large"] || len(iss) != 1 {
				t.Errorf("/%d: want exactly subnet_too_large, got %+v", p, iss)
			}
		case p > 30:
			if !codes["subnet_too_small"] || len(iss) != 1 {
				t.Errorf("/%d: want exactly subnet_too_small, got %+v", p, iss)
			}
		default:
			if len(iss) != 0 {
				t.Errorf("/%d was refused: %+v", p, iss)
			}
		}
		// the full validator agrees with the shared rule
		n := goodNet("a", "A", "br-g20", baseOf(p), "10.20.0.1", 20)
		full := issueCodes(ValidateOne(n, DefaultTopology(), nil, nil, ""))
		if full["subnet_too_large"] != codes["subnet_too_large"] || full["subnet_too_small"] != codes["subnet_too_small"] {
			t.Errorf("/%d: ValidateOne disagrees with CheckClientSubnet: %v vs %v", p, full, codes)
		}
	}
	if iss := CheckClientSubnet("not-a-cidr"); len(iss) != 1 || iss[0].Code != "bad_subnet" {
		t.Errorf("text that is not a CIDR: %+v", iss)
	}
	if iss := CheckClientSubnet("fd00::/64"); len(iss) != 1 || iss[0].Code != "bad_subnet" {
		t.Errorf("an IPv6 subnet must be refused: %+v", iss)
	}
}

func TestWidestAndHostCapacity(t *testing.T) {
	if MinSubnetPrefix != 19 {
		t.Fatalf("the widest client network is /19, constant says /%d", MinSubnetPrefix)
	}
	for p, want := range map[int]int{19: 8190, 20: 4094, 24: 254, 30: 2} {
		if got := SubnetHosts(p); got != want {
			t.Errorf("SubnetHosts(%d) = %d, want %d", p, got, want)
		}
	}
	// the refusal says the limit and the capacity honestly, and never promises concurrent clients
	msg := CheckClientSubnet("10.20.0.0/18")[0].Message
	for _, want := range []string{"/19", "8190", "Split it into several client networks"} {
		if !strings.Contains(msg, want) {
			t.Errorf("subnet_too_large message lacks %q: %s", want, msg)
		}
	}
}

// A complete /19 network validates: a pool across nearly the whole range, a reservation at the very top, the gateway at the bottom.
func TestAFullSlash19NetworkIsValid(t *testing.T) {
	n := goodNet("a", "Big", "br-g20", "10.20.0.0/19", "10.20.0.1", 20)
	n.Pools = []Pool{{StartIP: "10.20.0.10", EndIP: "10.20.31.200"}}
	n.Reservations = []Reservation{{MAC: "aa:bb:cc:00:00:01", ReservedIP: "10.20.31.254", Enabled: true},
		{MAC: "aa:bb:cc:00:00:02", ReservedIP: "10.20.31.201", Enabled: true}}
	if iss := ValidateOne(n, DefaultTopology(), nil, nil, ""); len(iss) != 0 {
		t.Fatalf("a valid /19 network was refused: %+v", iss)
	}
	// the exclusions hold at /19 scale: network (10.20.0.0), broadcast (10.20.31.255), gateway, pool outside, reservation in pool
	for name, mut := range map[string]func(*GuestNetwork){
		"pool_contains_network":   func(g *GuestNetwork) { g.Pools = []Pool{{StartIP: "10.20.0.0", EndIP: "10.20.0.50"}} },
		"pool_contains_broadcast": func(g *GuestNetwork) { g.Pools = []Pool{{StartIP: "10.20.31.0", EndIP: "10.20.31.255"}} },
		"pool_contains_gateway":   func(g *GuestNetwork) { g.Pools = []Pool{{StartIP: "10.20.0.1", EndIP: "10.20.0.50"}} },
		"pool_outside_subnet":     func(g *GuestNetwork) { g.Pools = []Pool{{StartIP: "10.20.31.250", EndIP: "10.20.32.5"}} },
		"gateway_is_broadcast":    func(g *GuestNetwork) { g.GatewayIP = "10.20.31.255" },
		"gateway_is_network":      func(g *GuestNetwork) { g.GatewayIP = "10.20.0.0" },
		"gateway_outside_subnet":  func(g *GuestNetwork) { g.GatewayIP = "10.20.32.1" },
		"reservation_in_pool": func(g *GuestNetwork) {
			g.Reservations = []Reservation{{MAC: "aa:bb:cc:00:00:03", ReservedIP: "10.20.31.100", Enabled: true}}
		},
		"reservation_outside_subnet": func(g *GuestNetwork) {
			g.Reservations = []Reservation{{MAC: "aa:bb:cc:00:00:03", ReservedIP: "10.20.32.9", Enabled: true}}
		},
	} {
		g := n
		mut(&g)
		if !issueCodes(ValidateOne(g, DefaultTopology(), nil, nil, ""))[name] {
			t.Errorf("%s was not reported on a /19", name)
		}
	}
}

func TestSlash19OverlapIsRefused(t *testing.T) {
	big := goodNet("big", "Big", "br-g20", "10.20.0.0/19", "10.20.0.1", 20)
	for name, other := range map[string]string{
		"a /20 inside the lower half": "10.20.0.0/20",
		"a /20 inside the upper half": "10.20.16.0/20",
		"a /24 inside":                "10.20.31.0/24",
		"a /24 at the very top":       "10.20.31.0/24",
		"a /18 containing it":         "10.20.0.0/18",
		"the same /19":                "10.20.0.0/19",
	} {
		o := goodNet("o", "Other", "br-g21", other, strings.TrimSuffix(other[:strings.Index(other, "/")], ".0")+".1", 21)
		res := ValidateSet([]GuestNetwork{big, o}, DefaultTopology(), nil, nil)
		if !issueCodes(res.Issues)["subnet_overlap"] {
			t.Errorf("%s: overlap with the /19 was not reported: %+v", name, res.Issues)
		}
	}
	for name, other := range map[string]string{"the next /19": "10.20.32.0/19", "the previous /19": "10.19.224.0/19", "a /24 just above": "10.20.32.0/24"} {
		o := goodNet("o", "Other", "br-g21", other, other[:strings.Index(other, "/")-1]+"1", 21)
		res := ValidateSet([]GuestNetwork{big, o}, DefaultTopology(), nil, nil)
		if issueCodes(res.Issues)["subnet_overlap"] {
			t.Errorf("%s: adjacent subnets were reported as overlapping: %+v", name, res.Issues)
		}
	}
	// a disabled network does not block
	o := goodNet("o", "Other", "br-g21", "10.20.0.0/24", "10.20.0.1", 21)
	o.Enabled = false
	if issueCodes(ValidateSet([]GuestNetwork{big, o}, DefaultTopology(), nil, nil).Issues)["subnet_overlap"] {
		t.Error("a disabled network blocked the /19")
	}
}

// Management and WAN share one NIC (ens160). A /19 over their subnet is refused, a neighbour is not.
func TestSlash19AgainstWANAndManagementSubnet(t *testing.T) {
	topo := DefaultTopology() // WAN = management = ens160
	host := []HostAddr{
		{Iface: "ens160", CIDR: "172.21.96.97/24"},
		{Iface: "br-g20", CIDR: "10.20.0.1/20"}, // a client bridge: not a WAN/management subnet, so ignored here
		{Iface: "lo", CIDR: "127.0.0.1/8"},
		{Iface: "docker0", CIDR: "172.17.0.1/16"},
	}
	over := goodNet("w", "Over", "br-g30", "172.21.96.0/19", "172.21.96.1", 30)
	got := ValidateHostOverlap([]GuestNetwork{over}, topo, host)
	if len(got) != 1 || got[0].Code != "subnet_overlaps_host" || !strings.Contains(got[0].Message, "172.21.96.0/24") {
		t.Fatalf("a /19 over the WAN/management /24 was not refused: %+v", got)
	}
	// a /19 inside a larger WAN network is refused as well (containment the other way round)
	wide := []HostAddr{{Iface: "ens160", CIDR: "10.20.0.5/16"}}
	if got := ValidateHostOverlap([]GuestNetwork{goodNet("w", "In", "br-g30", "10.20.0.0/19", "10.20.0.1", 30)}, topo, wide); len(got) != 1 {
		t.Fatalf("a /19 inside the WAN /16 was not refused: %+v", got)
	}
	// the adjacent /19 and the unrelated 10.20.0.0/19 do not overlap
	for _, cidr := range []string{"172.21.128.0/19", "172.21.64.0/19", "10.20.0.0/19"} {
		ok := goodNet("w", "Ok", "br-g30", cidr, strings.TrimSuffix(cidr[:strings.Index(cidr, "/")], ".0")+".1", 30)
		if got := ValidateHostOverlap([]GuestNetwork{ok}, topo, host); len(got) != 0 {
			t.Errorf("%s was reported as overlapping the WAN: %+v", cidr, got)
		}
	}
	// a disabled network is not checked
	over.Enabled = false
	if got := ValidateHostOverlap([]GuestNetwork{over}, topo, host); len(got) != 0 {
		t.Errorf("a disabled network was checked: %+v", got)
	}
	// the management interface counts even when it is a different NIC from the WAN
	topo2 := DefaultTopology()
	topo2.MgmtInterface = "ens224"
	mgmtHost := []HostAddr{{Iface: "ens224", CIDR: "192.168.60.25/24"}}
	m := goodNet("m", "M", "br-g31", "192.168.32.0/19", "192.168.32.1", 31)
	m.SubnetCIDR = "192.168.32.0/19" // 192.168.32.0 - 192.168.63.255 contains 192.168.60.0/24
	if got := ValidateHostOverlap([]GuestNetwork{m}, topo2, mgmtHost); len(got) != 1 {
		t.Errorf("a /19 over the management subnet was not refused: %+v", got)
	}
}

func slash19Net() GuestNetwork {
	n := goodNet("n19", "Big", "br-g20", "10.20.0.0/19", "10.20.0.1", 20)
	n.Pools = []Pool{{StartIP: "10.20.0.10", EndIP: "10.20.31.200"}, {StartIP: "10.20.31.210", EndIP: "10.20.31.250"}}
	n.Reservations = []Reservation{{MAC: "aa:bb:cc:00:00:01", ReservedIP: "10.20.31.254", Hostname: "top-of-range", Enabled: true}}
	n.ClientIsolation = true
	return n
}

// Kea: the subnet, the pools across the whole /19, the reservation at the top, gateway/router and option 114, lease timers.
func TestKeaConfigForASlash19(t *testing.T) {
	n := slash19Net()
	topo := DefaultTopology()
	raw, err := RenderKeaFile([]GuestNetwork{n}, topo, "/var/lib/kea/kea-leases4.csv", "/run/kea/kea4-ctrl.sock")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Dhcp4 struct {
			Interfaces struct {
				Interfaces []string `json:"interfaces"`
			} `json:"interfaces-config"`
			Lease struct {
				Type    string `json:"type"`
				Persist bool   `json:"persist"`
				LFC     int    `json:"lfc-interval"`
			} `json:"lease-database"`
			Subnets []struct {
				ID          int                     `json:"id"`
				Subnet      string                  `json:"subnet"`
				Interface   string                  `json:"interface"`
				Valid       int                     `json:"valid-lifetime"`
				MinValid    int                     `json:"min-valid-lifetime"`
				MaxValid    int                     `json:"max-valid-lifetime"`
				Pools       []struct{ Pool string } `json:"pools"`
				Options     []map[string]any        `json:"option-data"`
				Reservation []map[string]string     `json:"reservations"`
			} `json:"subnet4"`
		} `json:"Dhcp4"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Dhcp4.Subnets) != 1 {
		t.Fatalf("subnets: %d", len(doc.Dhcp4.Subnets))
	}
	s := doc.Dhcp4.Subnets[0]
	if s.Subnet != "10.20.0.0/19" || s.Interface != "br-g20" || s.Valid != 3600 || s.MinValid != 900 || s.MaxValid != 7200 {
		t.Errorf("subnet %+v", s)
	}
	if len(s.Pools) != 2 || s.Pools[0].Pool != "10.20.0.10 - 10.20.31.200" || s.Pools[1].Pool != "10.20.31.210 - 10.20.31.250" {
		t.Errorf("pools %+v", s.Pools)
	}
	if len(s.Reservation) != 1 || s.Reservation[0]["ip-address"] != "10.20.31.254" {
		t.Errorf("reservations %+v", s.Reservation)
	}
	if doc.Dhcp4.Interfaces.Interfaces[0] != "br-g20/10.20.0.1" {
		t.Errorf("interface binding %v", doc.Dhcp4.Interfaces.Interfaces)
	}
	var routers, opt114 bool
	for _, o := range s.Options {
		if o["name"] == "routers" && o["data"] == "10.20.0.1" {
			routers = true
		}
		if c, _ := o["code"].(float64); c == 114 && o["data"] == "http://10.20.0.1:8380/" {
			opt114 = true
		}
	}
	if !routers || !opt114 {
		t.Errorf("router/option 114 missing: %+v", s.Options)
	}
	// The lease database is the memfile: 8190 leases is a few hundred KB of CSV, handled with the same settings as a /24.
	if doc.Dhcp4.Lease.Type != "memfile" || !doc.Dhcp4.Lease.Persist || doc.Dhcp4.Lease.LFC != 3600 {
		t.Errorf("lease database %+v", doc.Dhcp4.Lease)
	}
}

// nftables: a /19 is one element in the subnet sets and one masquerade rule; the ruleset does not grow with the number of hosts, and the
// management pin and isolation rules are unchanged.
func TestNftablesForASlash19(t *testing.T) {
	topo := DefaultTopology()
	topo.MgmtAddr = "172.21.96.97"
	n := slash19Net()
	out := string(RenderNftables([]GuestNetwork{n}, topo))
	for _, want := range []string{
		"elements = { 10.20.0.0/19 }",
		"ip saddr 10.20.0.0/19 oifname \"ens160\" masquerade",
		"iifname \"ens160\" ip daddr 172.21.96.97 tcp dport 443 accept",
		"iifname @guest_interfaces ip daddr @guest_subnets drop comment \"inter-guest isolation\"",
		"iifname \"br-g20\" oifname \"br-g20\" drop comment \"client isolation\"",
		"iifname \"br-g20\" ip daddr 10.20.0.1 udp dport 53 accept",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	// no host-sized structure: the /19 ruleset is no larger than a /24 by more than the text of the prefix
	small := slash19Net()
	small.SubnetCIDR = "10.20.0.0/24"
	small.Pools = []Pool{{StartIP: "10.20.0.10", EndIP: "10.20.0.200"}}
	small.Reservations = nil
	a, b := len(RenderNftables([]GuestNetwork{n}, topo)), len(RenderNftables([]GuestNetwork{small}, topo))
	if d := a - b; d < -8 || d > 8 {
		t.Errorf("the /19 ruleset differs from the /24 ruleset by %d bytes; it must not scale with hosts", d)
	}
	// the authorisation sets are dynamic (netd fills them), so a /19 needs no static size: element count is bounded only by memory.
	if strings.Contains(out, "set phase3_auth_mac_ipv4 {\n\t\ttype ifname . ipv4_addr . ether_addr\n\t\tsize ") {
		t.Error("the authorisation set carries a fixed size that would cap a /19")
	}
	// the per-source sets are sized well above 8190 hosts
	if !strings.Contains(out, "set dns_preauth_rl {\n\t\ttype ipv4_addr\n\t\tsize 65535") || !strings.Contains(out, "set portal_conn_per_src {\n\t\ttype ipv4_addr\n\t\tsize 65535") {
		t.Error("the per-source limiter sets are not sized for a /19")
	}
}

func TestNetplanGatewayPrefixForASlash19(t *testing.T) {
	n := slash19Net()
	y := string(RenderNetplan([]GuestNetwork{n}))
	if !strings.Contains(y, "- 10.20.0.1/19") {
		t.Errorf("the bridge address must carry the /19 prefix:\n%s", y)
	}
	if gatewayCIDR(n) != "10.20.0.1/19" {
		t.Errorf("gatewayCIDR = %s", gatewayCIDR(n))
	}
}

// Two /19 networks with the gateway at the bottom of each coexist: separate subnets, separate bridges, separate Kea subnets with stable ids.
func TestTwoSlash19NetworksCoexist(t *testing.T) {
	a := slash19Net()
	b := goodNet("n19b", "Big2", "br-g21", "10.20.32.0/19", "10.20.32.1", 21)
	b.Pools = []Pool{{StartIP: "10.20.32.10", EndIP: "10.20.63.200"}}
	res := ValidateSet([]GuestNetwork{a, b}, DefaultTopology(), nil, nil)
	if !res.OK {
		t.Fatalf("two adjacent /19 networks were refused: %+v", res.Issues)
	}
	ids := StableSubnetIDs([]GuestNetwork{a, b})
	if len(ids) != 2 || ids["n19"] == ids["n19b"] {
		t.Errorf("subnet ids %v", ids)
	}
	_, an, _ := net.ParseCIDR(a.SubnetCIDR)
	_, bn, _ := net.ParseCIDR(b.SubnetCIDR)
	if an.Contains(bn.IP) || bn.Contains(an.IP) {
		t.Fatal("test setup: the networks overlap")
	}
}
