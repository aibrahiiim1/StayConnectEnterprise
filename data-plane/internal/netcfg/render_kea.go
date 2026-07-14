package netcfg

import (
	"encoding/json"
	"fmt"
	"os"
)

// leaseCmdsHook is the Kea lease_cmds hook library path. Overridable via
// NETD_KEA_LEASE_HOOK for distros that install it elsewhere; the default is the
// Debian/Ubuntu multiarch location.
var leaseCmdsHook = func() string {
	if p := os.Getenv("NETD_KEA_LEASE_HOOK"); p != "" {
		return p
	}
	return "/usr/lib/x86_64-linux-gnu/kea/hooks/libdhcp_lease_cmds.so"
}()

// RenderKeaDhcp4 builds the complete Kea "Dhcp4" configuration object from the
// enabled *local* guest networks. It is designed to be handed to the Kea
// control socket via config-test (validate) and config-set (apply), so it is
// the authoritative runtime DHCP config — no file templating/restart needed.
//
// external/relay/disabled networks are intentionally NOT served here (Kea must
// not answer for a subnet the hotel runs itself). Their listen interfaces are
// still excluded so Kea doesn't bind them.
func RenderKeaDhcp4(nets []GuestNetwork, topo Topology, leaseCSVPath, ctrlSocket string) map[string]any {
	local := make([]GuestNetwork, 0)
	for _, n := range sortEnabled(nets) {
		if n.DHCPMode == DHCPLocal {
			local = append(local, n)
		}
	}

	ifaceList := make([]string, 0, len(local))
	subnets := make([]map[string]any, 0, len(local))
	for i, n := range local {
		// Kea binds the network's bridge interface.
		ifaceList = append(ifaceList, fmt.Sprintf("%s/%s", n.BridgeName, n.GatewayIP))

		dns := n.GatewayIP
		if n.DNSMode == "custom" && len(n.DNSServers) > 0 {
			dns = joinComma(n.DNSServers)
		}
		optionData := []map[string]any{
			{"name": "routers", "data": n.GatewayIP},
			{"name": "domain-name-servers", "data": dns},
			{"name": "domain-name", "data": n.DomainName},
		}
		// RFC 8910 option 114 — plain-HTTP portal on this network's gateway.
		if n.CaptiveEnabled {
			optionData = append(optionData, map[string]any{
				"name": "v4-captive-portal",
				"data": PortalURLFor(n.GatewayIP, topo.PortalHTTPPort),
			})
		}

		pools := make([]map[string]any, 0, len(n.Pools))
		for _, p := range n.Pools {
			pools = append(pools, map[string]any{"pool": fmt.Sprintf("%s - %s", p.StartIP, p.EndIP)})
		}

		reservations := make([]map[string]any, 0)
		for _, r := range n.Reservations {
			if !r.Enabled {
				continue
			}
			res := map[string]any{"hw-address": r.MAC, "ip-address": r.ReservedIP}
			if r.Hostname != "" {
				res["hostname"] = r.Hostname
			}
			reservations = append(reservations, res)
		}

		sub := map[string]any{
			"id":                 i + 1,
			"subnet":             n.SubnetCIDR,
			"interface":          n.BridgeName,
			"pools":              pools,
			"option-data":        optionData,
			"valid-lifetime":     n.LeaseDefault,
			"min-valid-lifetime": n.LeaseMin,
			"max-valid-lifetime": n.LeaseMax,
			"reservations":       reservations,
			// user-context carries our network id so lease queries can be
			// attributed back to a guest network.
			"user-context": map[string]any{
				"guest_network_id": n.ID,
				"vlan_id":          n.VLANID,
			},
		}
		subnets = append(subnets, sub)
	}

	dhcp4 := map[string]any{
		"interfaces-config": map[string]any{
			"interfaces":       ifaceList,
			"dhcp-socket-type": "raw",
		},
		"control-socket": map[string]any{
			"socket-type": "unix",
			"socket-name": ctrlSocket,
		},
		// lease_cmds enables lease4-get-all / lease4-get-by-* over the control
		// socket so the Hotel Admin leases page reads structured lease data
		// (never parses the memfile CSV). Path is the standard Debian/Ubuntu
		// location; override via NETD if a distro differs.
		"hooks-libraries": []map[string]any{
			{"library": leaseCmdsHook},
		},
		"lease-database": map[string]any{
			"type":         "memfile",
			"persist":      true,
			"name":         leaseCSVPath,
			"lfc-interval": 3600,
		},
		"expired-leases-processing": map[string]any{
			"reclaim-timer-wait-time":         10,
			"flush-reclaimed-timer-wait-time": 25,
			"hold-reclaimed-time":             3600,
			"max-reclaim-leases":              100,
			"max-reclaim-time":                250,
		},
		"subnet4": subnets,
		"loggers": []map[string]any{{
			"name":           "kea-dhcp4",
			"output_options": []map[string]any{{"output": "/var/log/kea/kea-dhcp4.log", "flush": true}},
			"severity":       "INFO",
		}},
	}
	return dhcp4
}

// RenderKeaFile wraps the Dhcp4 object as a full { "Dhcp4": {...} } file the
// kea-dhcp4 service reads on cold start (kept in sync with what we config-set).
func RenderKeaFile(nets []GuestNetwork, topo Topology, leaseCSVPath, ctrlSocket string) ([]byte, error) {
	full := map[string]any{"Dhcp4": RenderKeaDhcp4(nets, topo, leaseCSVPath, ctrlSocket)}
	return json.MarshalIndent(full, "", "  ")
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
