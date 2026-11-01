// Package netcfg is the pure model, validator and renderer for guest networks (netplan, Kea, nftables, Unbound);
// netd owns the side effects.
package netcfg

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

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

// GuestNetwork is the flattened intent for one guest network, loaded from the DB.
type GuestNetwork struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	SSIDLabel       string        `json:"ssid_label,omitempty"`
	Enabled         bool          `json:"enabled"`
	NetworkType     string        `json:"network_type"` // untagged | vlan
	ParentInterface string        `json:"parent_interface"`
	VLANID          int           `json:"vlan_id,omitempty"` // 0 when untagged
	BridgeName      string        `json:"bridge_name"`
	GatewayIP       string        `json:"gateway_ip"`
	SubnetCIDR      string        `json:"subnet_cidr"`
	PrefixLen       int           `json:"prefix_len"`
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

type Topology struct {
	WANInterface  string // ens160 — masquerade egress
	MgmtInterface string // ens160/mgmt — protected
	// MgmtAddr, when set, is the only address admin 443 is accepted on, so a shared mgmt+WAN NIC still refuses the
	// admin UI on the WAN IP; empty means interface-wide.
	MgmtAddr       string
	MgmtCIDRs      []string // management subnets guests must never reach
	PortalHTTPPort int      // 8380

	// PrivateReach is the AD-22 policy for private destinations a client may reach through the appliance. The zero value
	// is the previous behaviour (only MgmtCIDRs are blocked), so a Topology built without it renders as before.
	PrivateReach PrivateReach

	// MACBinding keys the Phase-3 forward accept on (bridge, IP, MAC) instead of (bridge, IP) (AD-23 a). False is the previous
	// behaviour and the emergency fallback; the zero value is false.
	MACBinding bool

	// PortalTLSPort is the captive portal's HTTPS port, or zero when the appliance serves none. It stays zero: the
	// portal's HTTPS listener is reachable only behind PortalVIP, never behind a gateway, so a guest's 443 to any other
	// host is never redirected.
	// Zero renders a silent drop of an unauthenticated guest's outbound TCP 443 (render_nft.go), not a reset: Chromium
	// opens its captive-portal login tab only after an HTTPS load times out or fails (captive_portal_tab_reloader.cc),
	// and a reset cancels that check.
	PortalTLSPort int

	// PortalVIP is the portal address: one RFC 1918 IPv4 /32 on the dummy interface PortalIface, answered only from
	// guest interfaces on TCP 443 (-> portald :8343) and 80 (-> :8380), never routed, NATed or reachable from WAN or
	// management. "" means none. It is one fixed address because public DNS gives every guest the same answer. netd
	// fills it from public.portal_https_settings and records it in the revision bundle (PortalVIPBundleFile) so a
	// rollback restores it.
	PortalVIP string

	// PortalTLS is the portal's HTTPS state as scd last established it; it changes at runtime and only DHCP option 114
	// reads it. The nftables render ignores it: whether HTTPS is active must never change what the firewall admits.
	PortalTLS PortalTLS
}

// PortalTLS is the subset of public.portal_tls_state that decides what guests are told about the portal.
type PortalTLS struct {
	// HTTPSActive is scd's verdict: hostname, valid certificate and portal address are all up; false means the HTTP fallback.
	HTTPSActive       bool   `json:"https_active"`
	CanonicalHostname string `json:"canonical_hostname,omitempty"`
	// OneGateHostname is <label>.gw.onegate.work, allocated by Central.
	OneGateHostname string `json:"onegate_hostname,omitempty"`
	// CustomDomain is the hotel's portal domain, filled ONLY while its status is VERIFIED.
	CustomDomain string `json:"custom_domain,omitempty"`
	// SharedHostname is the fleet's shared Client Portal name while this appliance uses it (scd keeps a
	// certificate row for it). Every appliance answers it locally with its OWN portal address.
	SharedHostname string `json:"shared_hostname,omitempty"`
}

const (
	// PortalIface is the dummy interface carrying PortalVIP/32; netd alone creates and deletes it.
	PortalIface = "ogportal0"
	// PortalAddressTLSPort is portald's TLS listener. It is reached only through the portal address's 443.
	PortalAddressTLSPort = 8343
	// PortalVIPBundleFile records in a revision bundle the portal address that revision was applied with ("" when none).
	PortalVIPBundleFile = "portal-ip"
)

func DefaultTopology() Topology {
	return Topology{
		WANInterface:   "ens160",
		MgmtInterface:  "ens160",
		MgmtCIDRs:      []string{"172.16.0.0/12", "192.168.0.0/16"},
		PortalHTTPPort: 8380,
		PortalTLSPort:  0,
	}
}

// BridgeNameFor produces a stable, IFNAMSIZ-safe (<=15 char) bridge name.
// untagged -> "br-g-<8hex of id>", vlan -> "br-g<vlan>" (short, readable).
func BridgeNameFor(networkType string, vlanID int, id string) string {
	if networkType == "vlan" && vlanID > 0 {
		return fmt.Sprintf("br-g%d", vlanID)
	}
	h := shortHash(id)
	return "br-g-" + h
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

// sortNetworks returns enabled networks in a stable order (VLAN then name) so artifacts are deterministic.
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
