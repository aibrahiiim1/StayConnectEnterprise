package netcfg

import (
	"fmt"
	"net"
	"strings"
)

const (
	// MinSubnetPrefix is the widest client subnet: /19, 8190 usable addresses (see the subnet_too_large rule). This is ADDRESS CAPACITY of
	// one broadcast domain, not a promise of 8190 simultaneously served clients. The shaper names a class per host of the subnet and
	// supports up to /17 (internal/shape minor.go); /19 is the product limit on top of that.
	MinSubnetPrefix = 19
	// MaxSubnetPrefix is the narrowest client subnet: /30 (a gateway and one host).
	MaxSubnetPrefix = 30
	// MinLeaseSeconds / MaxLeaseSeconds bound every DHCP lease timer that is set.
	MinLeaseSeconds = 60
	MaxLeaseSeconds = 7 * 24 * 3600
)

type Issue struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ValidationResult struct {
	OK     bool    `json:"ok"`
	Issues []Issue `json:"issues"`
}

func (r *ValidationResult) add(field, code, msg string) {
	r.Issues = append(r.Issues, Issue{Field: field, Code: code, Message: msg})
	r.OK = false
}

// ValidateOne checks a single network in isolation; prefix is the field path prefix.
func ValidateOne(n GuestNetwork, topo Topology, availIfaces, knownIfaces map[string]bool, prefix string) []Issue {
	var r ValidationResult
	r.OK = true
	f := func(s string) string { return prefix + s }

	if strings.TrimSpace(n.Name) == "" {
		r.add(f("name"), "required", "network name is required")
	}
	// The text and name fields below reach the generated nftables, netplan, Kea and Unbound files. Only an enabled network is ever
	// rendered, so a disabled one that has not been cleaned up yet cannot reach a file and must not block every other change;
	// enabling it applies these checks.
	if n.Enabled {
		if n.Name != "" && strings.TrimSpace(n.Name) != "" {
			if why := CheckNetworkName(n.Name); why != "" {
				r.add(f("name"), "invalid_name", why)
			}
		}
		if n.BridgeName != "" {
			if why := CheckIfaceName(n.BridgeName); why != "" {
				r.add(f("bridge_name"), "invalid_bridge_name", why)
			}
		}
		if n.ParentInterface != "" {
			if why := CheckIfaceName(n.ParentInterface); why != "" {
				r.add(f("parent_interface"), "invalid_interface_name", why)
			}
		}
		if why := CheckDomainName(n.DomainName); why != "" {
			r.add(f("domain_name"), "invalid_domain", why)
		}
		for i, res := range n.Reservations {
			if why := CheckDomainName(res.Hostname); why != "" {
				r.add(fmt.Sprintf("%sreservations[%d].hostname", prefix, i), "invalid_hostname", why)
			}
		}
	}
	if n.ParentInterface == "" {
		r.add(f("parent_interface"), "required", "a parent interface is required")
	} else if n.Enabled && availIfaces != nil && !availIfaces[n.ParentInterface] {
		// availIfaces is the set of interfaces the appliance has right now: an ENABLED network on a vanished interface cannot come
		// up, so it is refused here rather than at netplan/Kea time. A disabled network is not brought up, so its absent parent
		// is not a reason to refuse every other change (it is checked when the network is enabled). KnownIfaces lets the message
		// tell a never-had typo from a lost interface.
		if knownIfaces != nil && knownIfaces[n.ParentInterface] {
			r.add(f("parent_interface"), "interface_not_present",
				fmt.Sprintf("interface %q is recorded on this appliance but is NOT PRESENT now — it was not "+
					"seen by the latest interface scan. Reconnect or restore it, or choose an interface that "+
					"is present.", n.ParentInterface))
		} else {
			r.add(f("parent_interface"), "interface_not_found",
				fmt.Sprintf("interface %q was not found on the appliance", n.ParentInterface))
		}
	}
	// Never attach a guest network to the management/WAN interface without an
	// explicit override (which this validator does not grant).
	if n.ParentInterface != "" && (n.ParentInterface == topo.MgmtInterface || n.ParentInterface == topo.WANInterface) {
		r.add(f("parent_interface"), "protected_interface",
			fmt.Sprintf("interface %q is the management/WAN interface and is protected; choose a guest interface", n.ParentInterface))
	}

	switch n.NetworkType {
	case "vlan":
		if n.VLANID < 1 || n.VLANID > 4094 {
			r.add(f("vlan_id"), "vlan_out_of_range", "VLAN id must be between 1 and 4094")
		}
	case "untagged":
		if n.VLANID != 0 {
			r.add(f("vlan_id"), "vlan_on_untagged", "an untagged network must not set a VLAN id")
		}
	default:
		r.add(f("network_type"), "bad_network_type", "network_type must be 'untagged' or 'vlan'")
	}

	gw := net.ParseIP(n.GatewayIP)
	if gw == nil || gw.To4() == nil {
		r.add(f("gateway_ip"), "bad_gateway", "gateway_ip must be a valid IPv4 address")
	}
	network, broadcast, ipnet, err := networkAndBroadcast(n.SubnetCIDR)
	if err != nil {
		r.add(f("subnet_cidr"), "bad_subnet", "subnet_cidr must be a valid IPv4 CIDR")
	} else {
		if gw != nil {
			if !ipnet.Contains(gw) {
				r.add(f("gateway_ip"), "gateway_outside_subnet",
					fmt.Sprintf("gateway %s is not inside %s", n.GatewayIP, n.SubnetCIDR))
			}
			if gw.Equal(network) {
				r.add(f("gateway_ip"), "gateway_is_network", "gateway_ip must not be the network address")
			}
			if gw.Equal(broadcast) {
				r.add(f("gateway_ip"), "gateway_is_broadcast", "gateway_ip must not be the broadcast address")
			}
		}
		for _, iss := range CheckClientSubnetMask(ipnet) {
			r.add(f("subnet_cidr"), iss.Code, iss.Message)
		}
	}

	// Each set lease timer must lie within Kea-sane bounds; negative or absurdly short or long leases must not reach Kea.
	for _, lt := range []struct {
		field string
		v     int
	}{{"lease_default_seconds", n.LeaseDefault}, {"lease_min_seconds", n.LeaseMin}, {"lease_max_seconds", n.LeaseMax}} {
		if lt.v != 0 && (lt.v < MinLeaseSeconds || lt.v > MaxLeaseSeconds) {
			r.add(f(lt.field), "lease_out_of_range",
				fmt.Sprintf("lease times must be between %d seconds and %d seconds (7 days)", MinLeaseSeconds, MaxLeaseSeconds))
		}
	}

	if n.LeaseMin > 0 && n.LeaseDefault > 0 && n.LeaseMin > n.LeaseDefault {
		r.add(f("lease_min_seconds"), "bad_lease_timers", "min lease must be <= default lease")
	}
	if n.LeaseMax > 0 && n.LeaseDefault > 0 && n.LeaseMax < n.LeaseDefault {
		r.add(f("lease_max_seconds"), "bad_lease_timers", "max lease must be >= default lease")
	}

	if n.DNSMode == "custom" {
		if len(n.DNSServers) == 0 {
			r.add(f("dns_servers"), "dns_required", "custom DNS mode requires at least one DNS server")
		}
		for i, d := range n.DNSServers {
			if ip := net.ParseIP(d); ip == nil || ip.To4() == nil {
				r.add(fmt.Sprintf("%sdns_servers[%d]", prefix, i), "bad_dns", "DNS server must be a valid IPv4 address")
			}
		}
	}

	// Portal URL (option 114), only for local+captive networks. HTTPS is allowed in one form: while the portal's HTTPS
	// is active, https://CANONICAL/captive-portal/api; otherwise the option is omitted. Any other https URL would
	// hand a device a TLS endpoint the appliance has no certificate for.
	if n.DHCPMode == DHCPLocal && n.CaptiveEnabled && gw != nil {
		url := CaptivePortalURL(n.GatewayIP, topo)
		if url != "" && strings.HasPrefix(url, "https://") {
			want, ok := captiveAPIURL(topo)
			if !ok || url != want {
				r.add(f("portal_url"), "portal_https",
					"captive-portal option 114 may use HTTPS only as https://<canonical portal hostname>/captive-portal/api "+
						"while the portal's HTTPS is active; otherwise it must be the plain-HTTP gateway URL")
			}
		}
	}

	if n.DHCPMode == DHCPRelay {
		if len(n.RelayTargets) == 0 {
			r.add(f("relay_targets"), "relay_required", "relay mode requires at least one relay target")
		}
		for i, t := range n.RelayTargets {
			if ip := net.ParseIP(t); ip == nil || ip.To4() == nil {
				r.add(fmt.Sprintf("%srelay_targets[%d]", prefix, i), "bad_relay_target", "relay target must be a valid IPv4 address")
			}
		}
	}

	if n.DHCPMode == DHCPLocal {
		validatePools(&r, n, ipnet, gw, network, broadcast, prefix)
		validateReservations(&r, n, ipnet, prefix)
	}
	return r.Issues
}

// CheckClientSubnet validates only the width of a client network's subnet text. It is the one rule shared by the netd validator and
// the edged API, so a subnet that the apply would refuse is refused when it is entered. An empty list means acceptable.
func CheckClientSubnet(cidr string) []Issue {
	_, _, ipnet, err := networkAndBroadcast(cidr)
	if err != nil {
		return []Issue{{Field: "subnet_cidr", Code: "bad_subnet", Message: "subnet_cidr must be a valid IPv4 CIDR"}}
	}
	return CheckClientSubnetMask(ipnet)
}

// CheckClientSubnetMask is CheckClientSubnet for an already parsed subnet.
func CheckClientSubnetMask(ipnet *net.IPNet) []Issue {
	var out []Issue
	ones, bits := ipnet.Mask.Size()
	if bits == 32 && ones > MaxSubnetPrefix {
		out = append(out, Issue{Field: "subnet_cidr", Code: "subnet_too_small",
			Message: "subnet is too small to host a gateway and a DHCP pool (use /30 or larger)"})
	}
	// The widest subnet the shaper can tell apart: a client's tc class is named by its host index in the subnet
	// (internal/shape MinorFor); the extension band reaches /17 and this rule holds the product at MinSubnetPrefix.
	if bits == 32 && ones < MinSubnetPrefix {
		out = append(out, Issue{Field: "subnet_cidr", Code: "subnet_too_large",
			Message: fmt.Sprintf("subnet is larger than /%d (%d addresses): one client network is limited to /%d, "+
				"because per-client speed limits and usage cannot tell clients apart in a wider subnet and a single "+
				"broadcast domain that large is not supported. Split it into several client networks.",
				MinSubnetPrefix, SubnetHosts(MinSubnetPrefix), MinSubnetPrefix)})
	}
	return out
}

// SubnetHosts is the number of usable host addresses in a prefix (network and broadcast excluded): /19 -> 8190.
func SubnetHosts(prefix int) int {
	if prefix < 0 || prefix > 30 {
		return 0
	}
	return (1 << uint(32-prefix)) - 2
}

// ValidateHostOverlap refuses an enabled client network whose subnet overlaps a subnet the appliance itself carries on its WAN or
// management interface (the same NIC on a two-port appliance). Client networks live on the LAN port; an overlap with the WAN/management
// subnet would route the client network's addresses into the uplink. host is the appliance's current IPv4 address list.
func ValidateHostOverlap(nets []GuestNetwork, topo Topology, host []HostAddr) []Issue {
	var out []Issue
	for _, n := range sortEnabled(nets) {
		_, _, ipnet, err := networkAndBroadcast(n.SubnetCIDR)
		if err != nil {
			continue
		}
		for _, h := range host {
			if h.Iface == "" || (h.Iface != topo.WANInterface && h.Iface != topo.MgmtInterface) {
				continue
			}
			_, hsub, err := net.ParseCIDR(h.CIDR)
			if err != nil || hsub.IP.To4() == nil {
				continue
			}
			if hsub.Contains(ipnet.IP) || ipnet.Contains(hsub.IP) {
				out = append(out, Issue{Field: fmt.Sprintf("networks[%s].subnet_cidr", n.Name), Code: "subnet_overlaps_host",
					Message: fmt.Sprintf("subnet %s overlaps %s, the WAN/management network of this appliance (%s); a client network must not share addresses with it",
						n.SubnetCIDR, hsub, h.Iface)})
				break
			}
		}
	}
	return out
}

func validatePools(r *ValidationResult, n GuestNetwork, ipnet *net.IPNet, gw, network, broadcast net.IP, prefix string) {
	if len(n.Pools) == 0 {
		r.add(prefix+"pools", "no_pool", "local DHCP requires at least one address pool")
		return
	}
	type rng struct{ lo, hi uint32 }
	var ranges []rng
	for i, p := range n.Pools {
		fp := fmt.Sprintf("%spools[%d]", prefix, i)
		s := net.ParseIP(p.StartIP)
		e := net.ParseIP(p.EndIP)
		if s == nil || s.To4() == nil {
			r.add(fp+".start_ip", "bad_ip", "pool start is not a valid IPv4 address")
			continue
		}
		if e == nil || e.To4() == nil {
			r.add(fp+".end_ip", "bad_ip", "pool end is not a valid IPv4 address")
			continue
		}
		su, _ := ipToUint32(s)
		eu, _ := ipToUint32(e)
		if su > eu {
			r.add(fp, "pool_reversed", "pool start is greater than pool end")
			continue
		}
		if ipnet != nil && (!ipnet.Contains(s) || !ipnet.Contains(e)) {
			r.add(fp, "pool_outside_subnet", fmt.Sprintf("the DHCP pool is outside %s", n.SubnetCIDR))
			continue
		}
		if network != nil {
			nu, _ := ipToUint32(network)
			bu, _ := ipToUint32(broadcast)
			if su <= nu && nu <= eu {
				r.add(fp, "pool_contains_network", "the pool must not include the network address")
			}
			if su <= bu && bu <= eu {
				r.add(fp, "pool_contains_broadcast", "the pool must not include the broadcast address")
			}
		}
		if gw != nil {
			gu, _ := ipToUint32(gw)
			if su <= gu && gu <= eu {
				r.add(fp, "pool_contains_gateway", "the pool must not include the gateway address")
			}
		}
		for _, o := range ranges {
			if su <= o.hi && o.lo <= eu {
				r.add(fp, "pool_overlap", "this pool overlaps another pool in the same subnet")
				break
			}
		}
		ranges = append(ranges, rng{su, eu})
	}
}

func validateReservations(r *ValidationResult, n GuestNetwork, ipnet *net.IPNet, prefix string) {
	seenMAC := map[string]bool{}
	seenIP := map[string]bool{}
	type rng struct{ lo, hi uint32 }
	var pools []rng
	for _, p := range n.Pools {
		if s := net.ParseIP(p.StartIP); s != nil {
			if e := net.ParseIP(p.EndIP); e != nil {
				su, _ := ipToUint32(s)
				eu, _ := ipToUint32(e)
				pools = append(pools, rng{su, eu})
			}
		}
	}
	for i, res := range n.Reservations {
		if !res.Enabled {
			continue
		}
		fp := fmt.Sprintf("%sreservations[%d]", prefix, i)
		if _, err := net.ParseMAC(res.MAC); err != nil {
			r.add(fp+".mac", "bad_mac", "reservation MAC is not valid")
		} else if seenMAC[strings.ToLower(res.MAC)] {
			r.add(fp+".mac", "dup_reservation_mac", "duplicate reservation MAC in this network")
		} else {
			seenMAC[strings.ToLower(res.MAC)] = true
		}
		ip := net.ParseIP(res.ReservedIP)
		if ip == nil || ip.To4() == nil {
			r.add(fp+".reserved_ip", "bad_ip", "reserved IP is not a valid IPv4 address")
			continue
		}
		if seenIP[res.ReservedIP] {
			r.add(fp+".reserved_ip", "dup_reservation_ip", "duplicate reserved IP in this network")
		}
		seenIP[res.ReservedIP] = true
		if ipnet != nil && !ipnet.Contains(ip) {
			r.add(fp+".reserved_ip", "reservation_outside_subnet", fmt.Sprintf("reserved IP is outside %s", n.SubnetCIDR))
		}
		iu, _ := ipToUint32(ip)
		for _, p := range pools {
			if p.lo <= iu && iu <= p.hi {
				r.add(fp+".reserved_ip", "reservation_in_pool",
					"reserved IP falls inside a dynamic pool; move it outside the pool or shrink the pool")
				break
			}
		}
	}
}

// ValidateSet checks networks for cross-network conflicts: duplicate VLAN on a parent, duplicate bridge names and
// overlapping enabled subnets. Issues is non-nil so it serializes as [] rather than null.
func ValidateSet(nets []GuestNetwork, topo Topology, availIfaces, knownIfaces map[string]bool) ValidationResult {
	res := ValidationResult{OK: true, Issues: []Issue{}}
	for i := range nets {
		for _, iss := range ValidateOne(nets[i], topo, availIfaces, knownIfaces, fmt.Sprintf("networks[%d].", i)) {
			res.Issues = append(res.Issues, iss)
			res.OK = false
		}
	}

	enabled := sortEnabled(nets)
	seenVLAN := map[string]int{}
	seenBridge := map[string]int{}
	for i, n := range enabled {
		if n.VLANID > 0 {
			key := n.ParentInterface + "/" + itoa(n.VLANID)
			if _, ok := seenVLAN[key]; ok {
				res.add(fmt.Sprintf("networks[%s]", n.Name), "duplicate_vlan",
					fmt.Sprintf("VLAN %d is already used on interface %s", n.VLANID, n.ParentInterface))
			}
			seenVLAN[key] = i
		}
		if n.BridgeName != "" {
			if _, ok := seenBridge[n.BridgeName]; ok {
				res.add(fmt.Sprintf("networks[%s]", n.Name), "duplicate_bridge",
					fmt.Sprintf("bridge name %s is used by more than one network", n.BridgeName))
			}
			seenBridge[n.BridgeName] = i
		}
	}
	type ent struct {
		name   string
		lo, hi uint32
	}
	var ents []ent
	for _, n := range enabled {
		_, _, ipnet, err := networkAndBroadcast(n.SubnetCIDR)
		if err != nil {
			continue
		}
		lo, _ := ipToUint32(ipnet.IP.To4())
		bc := make(net.IP, 4)
		for i := 0; i < 4; i++ {
			bc[i] = ipnet.IP.To4()[i] | ^ipnet.Mask[i]
		}
		hi, _ := ipToUint32(bc)
		for _, e := range ents {
			if lo <= e.hi && e.lo <= hi {
				res.add(fmt.Sprintf("networks[%s].subnet_cidr", n.Name), "subnet_overlap",
					fmt.Sprintf("subnet %s overlaps subnet of network %q; overlapping guest subnets on one appliance are not allowed", n.SubnetCIDR, e.name))
			}
		}
		ents = append(ents, ent{n.Name, lo, hi})
	}
	return res
}

// PortalURLFor builds the plain-HTTP captive-portal URL for a gateway, straight to portald and never Caddy;
// CaptivePortalURL makes the state-aware choice.
func PortalURLFor(gatewayIP string, httpPort int) string {
	return fmt.Sprintf("http://%s:%d/", gatewayIP, httpPort)
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
