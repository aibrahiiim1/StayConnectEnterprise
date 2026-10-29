package netcfg

import (
	"net"
	"sort"
	"strings"
)

// PrivateReach is the policy for destinations in private address space (AD-22). With Deny set, a client network may not
// reach any of PrivateRanges through the appliance unless the destination is in Allow; the walled garden and a network's
// custom DNS stay ahead of the drop (render_nft.go). It governs the forward chain only: the appliance's own management,
// WAN, DNS, DHCP and Central traffic never traverse it.
type PrivateReach struct {
	// Deny turns the private-destination drop on. False renders exactly what the appliance rendered before AD-22.
	Deny bool
	// Allow is the networks (IPv4 CIDR or single address) that stay reachable. Malformed entries are dropped by the render.
	Allow []string
}

// PrivateRanges are the destinations denied by default: RFC 1918, carrier-grade NAT (RFC 6598) and link-local (RFC 3927).
var PrivateRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16"}

// NormalizeAllow returns the well-formed, de-duplicated, sorted IPv4 networks of the list. One malformed element would
// break the whole atomic ruleset, so nothing unparsed is ever rendered.
func NormalizeAllow(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var n *net.IPNet
		if strings.Contains(raw, "/") {
			_, nn, err := net.ParseCIDR(raw)
			if err != nil || nn.IP.To4() == nil {
				continue
			}
			n = nn
		} else {
			ip := net.ParseIP(raw).To4()
			if ip == nil {
				continue
			}
			n = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
		}
		if ones, _ := n.Mask.Size(); ones == 0 {
			continue // 0.0.0.0/0 would switch the policy off without saying so
		}
		k := n.String()
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
