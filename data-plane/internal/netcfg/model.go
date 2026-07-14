// Package netcfg is the shared model, validator and configuration renderer for
// StayConnect guest networks. It is pure (no side effects): given the intended
// guest-network definitions from the Site database it validates them and
// renders netplan, Kea, nftables and Unbound artifacts. netd owns the side
// effects (apply/rollback); this package makes the "what" testable in isolation.
package netcfg

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// DHCPMode / NetworkType / DNSMode mirror the site DB CHECK constraints.
type DHCPMode string

const (
	DHCPLocal    DHCPMode = "local"
	DHCPExternal DHCPMode = "external"
	DHCPRelay    DHCPMode = "relay"
	DHCPDisabled DHCPMode = "disabled"
)

type Pool struct {
	StartIP string `json:"start_ip"`
	EndIP   string `json:"end_ip"`
}

type Reservation struct {
	MAC        string `json:"mac"`
	ReservedIP string `json:"reserved_ip"`
	Hostname   string `json:"hostname,omitempty"`
	Enabled    bool   `json:"enabled"`
}

// GuestNetwork is the flattened intent for one guest network plus its pools
// and reservations. netd loads these from the DB and hands them here.
type GuestNetwork struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	SSIDLabel       string        `json:"ssid_label,omitempty"`
	Enabled         bool          `json:"enabled"`
	NetworkType     string        `json:"network_type"` // untagged | vlan
	ParentInterface string        `json:"parent_interface"`
	VLANID          int           `json:"vlan_id,omitempty"` // 0 when untagged
	BridgeName      string        `json:"bridge_name"`
	GatewayIP       string        `json:"gateway_ip"`  // 10.20.0.1
	SubnetCIDR      string        `json:"subnet_cidr"` // 10.20.0.0/22
	PrefixLen       int           `json:"prefix_len"`  // derived, 22
	DHCPMode        DHCPMode      `json:"dhcp_mode"`
	DNSMode         string        `json:"dns_mode"` // appliance | custom
	DNSServers      []string      `json:"dns_servers,omitempty"`
	DomainName      string        `json:"domain_name"`
	LeaseDefault    int           `json:"lease_default_seconds"`
	LeaseMin        int           `json:"lease_min_seconds"`
	LeaseMax        int           `json:"lease_max_seconds"`
	RelayTargets    []string      `json:"relay_targets,omitempty"`
	CaptiveEnabled  bool          `json:"captive_portal_enabled"`
	InternetEnabled bool          `json:"internet_access_enabled"`
	NATEnabled      bool          `json:"nat_enabled"`
	ClientIsolation bool          `json:"client_isolation_enabled"`
	Pools           []Pool        `json:"pools"`
	Reservations    []Reservation `json:"reservations"`
}

// Topology carries appliance-wide facts the renderers need.
type Topology struct {
	WANInterface  string // ens160 — masquerade egress
	MgmtInterface string // ens160/mgmt — protected
	MgmtAddr      string // mgmt IP the admin UI binds to (optional). When set,
	// admin 443 is accepted only to THIS address — so a
	// pilot where mgmt+WAN share one NIC still refuses the
	// admin UI on the WAN IP. Empty => interface-wide.
	MgmtCIDRs      []string // management subnets guests must never reach
	PortalHTTPPort int      // 8380
	PortalTLSPort  int      // 8343
}

func DefaultTopology() Topology {
	return Topology{
		WANInterface:   "ens160",
		MgmtInterface:  "ens160",
		MgmtCIDRs:      []string{"172.16.0.0/12", "192.168.0.0/16"},
		PortalHTTPPort: 8380,
		PortalTLSPort:  8343,
	}
}

// ---- naming --------------------------------------------------------------

// BridgeNameFor produces a stable, IFNAMSIZ-safe (<=15 char) bridge name.
// untagged -> "br-g-<8hex of id>", vlan -> "br-g<vlan>" (short, readable).
func BridgeNameFor(networkType string, vlanID int, id string) string {
	if networkType == "vlan" && vlanID > 0 {
		return fmt.Sprintf("br-g%d", vlanID) // br-g4094 = 7 chars max
	}
	h := shortHash(id)
	return "br-g-" + h // 5 + 8 = 13 chars
}

// VLANIfaceName is parent.<vlan>, capped so parent+.vlan stays <=15.
func VLANIfaceName(parent string, vlanID int) string {
	suffix := fmt.Sprintf(".%d", vlanID)
	max := 15 - len(suffix)
	if len(parent) > max {
		parent = parent[:max]
	}
	return parent + suffix
}

func shortHash(s string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}

// ---- IP helpers ----------------------------------------------------------

func ipToUint32(ip net.IP) (uint32, bool) {
	v4 := ip.To4()
	if v4 == nil {
		return 0, false
	}
	return uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3]), true
}

func networkAndBroadcast(cidr string) (network, broadcast net.IP, ipnet *net.IPNet, err error) {
	_, ipnet, err = net.ParseCIDR(cidr)
	if err != nil {
		return nil, nil, nil, err
	}
	network = ipnet.IP.To4()
	if network == nil {
		return nil, nil, nil, fmt.Errorf("only IPv4 subnets are supported")
	}
	bc := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		bc[i] = network[i] | ^ipnet.Mask[i]
	}
	return network, bc, ipnet, nil
}

// sortNetworks returns enabled networks in a stable order (VLAN then name) so
// rendered artifacts are deterministic.
func sortEnabled(nets []GuestNetwork) []GuestNetwork {
	out := make([]GuestNetwork, 0, len(nets))
	for _, n := range nets {
		if n.Enabled {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].VLANID != out[j].VLANID {
			return out[i].VLANID < out[j].VLANID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func joinNonEmpty(parts ...string) string {
	var b []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			b = append(b, p)
		}
	}
	return strings.Join(b, " ")
}
