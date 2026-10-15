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

	// PortalTLSPort is the captive portal's HTTPS port, or ZERO when this appliance does not serve one.
	//
	// It is zero here, and that is a statement of fact rather than a default waiting to be filled in.
	// portald serves TLS only if a certificate exists at PORTALD_CERT, no tooling in this repository has
	// ever provisioned one, and /etc/stayconnect/tls does not exist on the appliance. The firewall, however,
	// redirected every unauthenticated guest's port-443 connection to that port regardless -- so a guest who
	// opened an HTTPS site before signing in was sent to a listener that has never existed.
	//
	// Nothing here introduces HTTPS interception, a certificate or a PKI model: a captive portal that
	// terminated TLS would have to present a certificate for somebody else's domain, which is precisely what
	// is NOT being built. Zero means the appliance does not pretend to serve that port, and the renderer
	// stops emitting rules that depend on it.
	//
	// It STAYS zero now that the portal has an HTTPS listener (contract ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN
	// §2.1). That listener is reachable ONLY behind the portal address below -- traffic a guest addresses to
	// PortalVIP -- and never behind a gateway: a guest's connection to any other host on 443 is still never
	// redirected, because the appliance holds a certificate only for its own portal names.
	//
	// What a zero DOES render is a silent drop of an unauthenticated guest's outbound TCP 443 (render_nft.go),
	// not a reset. Chromium opens its captive-portal login tab only after an HTTPS load times out, fails TLS,
	// shows a certificate error or trips its 30-second slow-SSL timer (captive_portal_tab_reloader.cc); a reset
	// cancels that check, so the reset this used to render hid the login tab from desktop Chrome and Edge users
	// who opened an https:// site. HTTPS-Upgrades falls back to http:// on any network error, a timeout included.
	PortalTLSPort int

	// PortalVIP is the portal address: one RFC 1918 IPv4 /32 that exists only on the dummy interface
	// PortalIface, answered only from guest interfaces on TCP 443 (-> portald :8343) and TCP 80 (-> portald
	// :8380), never routed, never NATed, never reachable from the WAN or management side. "" means this
	// appliance has none, and the renderer emits nothing for it. It is the address the portal's public
	// hostname resolves to, which is why it is one fixed address rather than each network's gateway: public
	// DNS gives one answer to every guest on every network.
	//
	// netd fills it from public.portal_https_settings at apply time and records it in the revision bundle
	// (file PortalVIPBundleFile), so a rollback restores the address that revision was applied with.
	PortalVIP string

	// PortalTLS is the portal's HTTPS state as scd last established it (public.portal_tls_state). It changes
	// at runtime without a network revision; only DHCP option 114 (RenderKeaDhcp4) reads it. The nftables
	// render deliberately ignores it: whether HTTPS is active must never change what the firewall admits.
	PortalTLS PortalTLS
}

// PortalTLS is the subset of public.portal_tls_state that decides what guests are told about the portal.
type PortalTLS struct {
	// HTTPSActive is scd's verdict: a canonical hostname exists, its certificate is installed and valid, and
	// the portal address is up. False means the approved HTTP fallback is in force.
	HTTPSActive bool `json:"https_active"`
	// CanonicalHostname is the name the portal is served under while HTTPS is active.
	CanonicalHostname string `json:"canonical_hostname,omitempty"`
	// OneGateHostname is <label>.gw.onegate.work, allocated by Central.
	OneGateHostname string `json:"onegate_hostname,omitempty"`
	// CustomDomain is the hotel's portal domain, filled ONLY while its status is VERIFIED.
	CustomDomain string `json:"custom_domain,omitempty"`
}

const (
	// PortalIface is the dummy interface that carries PortalVIP/32. It is created, re-addressed and deleted by
	// netd alone; nothing else on the appliance may own an interface by this name.
	PortalIface = "ogportal0"
	// PortalAddressTLSPort is portald's TLS listener. It is reached only through the portal address's 443.
	PortalAddressTLSPort = 8343
	// PortalVIPBundleFile is the file in a revision bundle that records the portal address the revision was
	// applied with ("" when none), so rollback and boot reconcile can restore exactly that address.
	PortalVIPBundleFile = "portal-ip"
)

func DefaultTopology() Topology {
	return Topology{
		WANInterface:   "ens160",
		MgmtInterface:  "ens160",
		MgmtCIDRs:      []string{"172.16.0.0/12", "192.168.0.0/16"},
		PortalHTTPPort: 8380,
		// No TLS portal on this appliance. See the field comment: this is a fact, not an unset default.
		PortalTLSPort: 0,
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
