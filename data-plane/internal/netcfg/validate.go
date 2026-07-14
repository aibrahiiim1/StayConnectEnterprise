package netcfg

import (
	"fmt"
	"net"
	"strings"
)

// Issue is a single structured validation problem (surfaced to the API).
type Issue struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationResult aggregates issues across all networks being applied.
type ValidationResult struct {
	OK     bool    `json:"ok"`
	Issues []Issue `json:"issues"`
}

func (r *ValidationResult) add(field, code, msg string) {
	r.Issues = append(r.Issues, Issue{Field: field, Code: code, Message: msg})
	r.OK = false
}

// ValidateOne checks a single network in isolation (shape, CIDR, pools,
// reservations, timers, VLAN, portal). prefix is the field path prefix.
func ValidateOne(n GuestNetwork, topo Topology, availIfaces map[string]bool, prefix string) []Issue {
	var r ValidationResult
	r.OK = true
	f := func(s string) string { return prefix + s }

	if strings.TrimSpace(n.Name) == "" {
		r.add(f("name"), "required", "network name is required")
	}
	if n.ParentInterface == "" {
		r.add(f("parent_interface"), "required", "a parent interface is required")
	} else if availIfaces != nil && !availIfaces[n.ParentInterface] {
		r.add(f("parent_interface"), "interface_not_found",
			fmt.Sprintf("interface %q was not found on the appliance", n.ParentInterface))
	}
	// Never attach a guest network to the management/WAN interface without an
	// explicit override (which this validator does not grant).
	if n.ParentInterface != "" && (n.ParentInterface == topo.MgmtInterface || n.ParentInterface == topo.WANInterface) {
		r.add(f("parent_interface"), "protected_interface",
			fmt.Sprintf("interface %q is the management/WAN interface and is protected; choose a guest interface", n.ParentInterface))
	}

	// VLAN
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

	// Gateway + subnet
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
		ones, bits := ipnet.Mask.Size()
		if bits == 32 && ones > 30 {
			r.add(f("subnet_cidr"), "subnet_too_small",
				"subnet is too small to host a gateway and a DHCP pool (use /30 or larger)")
		}
	}

	// Lease timers
	if n.LeaseMin > 0 && n.LeaseDefault > 0 && n.LeaseMin > n.LeaseDefault {
		r.add(f("lease_min_seconds"), "bad_lease_timers", "min lease must be <= default lease")
	}
	if n.LeaseMax > 0 && n.LeaseDefault > 0 && n.LeaseMax < n.LeaseDefault {
		r.add(f("lease_max_seconds"), "bad_lease_timers", "max lease must be >= default lease")
	}

	// DNS
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

	// Portal URL (option 114) — only relevant for local+captive networks.
	if n.DHCPMode == DHCPLocal && n.CaptiveEnabled && gw != nil {
		url := PortalURLFor(n.GatewayIP, topo.PortalHTTPPort)
		if strings.HasPrefix(url, "https://") {
			r.add(f("portal_url"), "portal_https", "captive-portal option 114 must use plain HTTP, not HTTPS")
		}
	}

	// Relay
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

	// Pools (only meaningful for local DHCP)
	if n.DHCPMode == DHCPLocal {
		validatePools(&r, n, ipnet, gw, network, broadcast, prefix)
		validateReservations(&r, n, ipnet, prefix)
	}
	return r.Issues
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
		// overlap with earlier pools
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
	// pool ranges for conflict detection
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

// ValidateSet checks a whole set of networks for cross-network conflicts on
// one appliance: duplicate VLAN on the same parent, duplicate bridge names,
// and overlapping enabled subnets (no VRF yet). Issues is always non-nil so
// it serializes as [] rather than null.
func ValidateSet(nets []GuestNetwork, topo Topology, availIfaces map[string]bool) ValidationResult {
	res := ValidationResult{OK: true, Issues: []Issue{}}
	for i := range nets {
		for _, iss := range ValidateOne(nets[i], topo, availIfaces, fmt.Sprintf("networks[%d].", i)) {
			res.Issues = append(res.Issues, iss)
			res.OK = false
		}
	}

	enabled := sortEnabled(nets)
	// duplicate VLAN per parent
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
	// overlapping enabled subnets
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

// PortalURLFor builds the plain-HTTP captive-portal URL (option 114) for a
// network's gateway. Always HTTP straight to portald — never HTTPS/Caddy.
func PortalURLFor(gatewayIP string, httpPort int) string {
	return fmt.Sprintf("http://%s:%d/", gatewayIP, httpPort)
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
