package netcfg

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"sort"
)

// leaseCmdsHook is the Kea lease_cmds hook library path (Debian/Ubuntu multiarch default); override via NETD_KEA_LEASE_HOOK.
var leaseCmdsHook = func() string {
	if p := os.Getenv("NETD_KEA_LEASE_HOOK"); p != "" {
		return p
	}
	return "/usr/lib/x86_64-linux-gnu/kea/hooks/libdhcp_lease_cmds.so"
}()

// RenderKeaDhcp4 builds the complete Kea "Dhcp4" object for enabled local guest networks, handed to the control
// socket via config-test and config-set as the authoritative runtime config. External, relay and disabled networks
// are not served (Kea must not answer for a subnet the hotel runs itself) and their interfaces are excluded so Kea
// does not bind them.
func RenderKeaDhcp4(nets []GuestNetwork, topo Topology, leaseCSVPath, ctrlSocket string) map[string]any {
	local := make([]GuestNetwork, 0)
	for _, n := range sortEnabled(nets) {
		if n.DHCPMode == DHCPLocal {
			local = append(local, n)
		}
	}

	ifaceList := make([]string, 0, len(local))
	subnets := make([]map[string]any, 0, len(local))
	ids := StableSubnetIDs(local)
	for _, n := range local {
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
		// RFC 8910 option 114: only while HTTPS is active, carrying the RFC 8908 captive API on the canonical hostname;
		// on the HTTP fallback the option is left out and devices use their own probes (see CaptivePortalURL for the
		// capture that showed a plain-HTTP value silences them). CaptivePortalURL alone decides, and netd's runtime
		// refresh renders through this function so apply and refresh cannot disagree.
		// Emitted by code, never by name: Kea 2.0 maps "v4-captive-portal" to code 160 (the obsolete RFC 7710 assignment);
		// only Kea 2.2+ maps it to 114.
		if url := CaptivePortalURL(n.GatewayIP, topo); n.CaptiveEnabled && url != "" {
			optionData = append(optionData, map[string]any{
				"code":  114,
				"space": "dhcp4",
				"data":  url,
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
			"id":                 ids[n.ID],
			"subnet":             n.SubnetCIDR,
			"interface":          n.BridgeName,
			"pools":              pools,
			"option-data":        optionData,
			"valid-lifetime":     n.LeaseDefault,
			"min-valid-lifetime": n.LeaseMin,
			"max-valid-lifetime": n.LeaseMax,
			"reservations":       reservations,
			// user-context carries our network id so lease queries map back to a guest network.
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
		// lease_cmds exposes lease4-get-* over the control socket so the leases page reads structured data instead of
		// parsing the memfile CSV.
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

// StableSubnetIDs derives each Kea subnet id from the network's own id, not its list position (Kea ties each lease
// to its subnet id). FNV-1a of the UUID folded into 1..2^31-1; on collision the later network in id order takes the
// next free value, so the result is a pure function of the set of networks.
func StableSubnetIDs(nets []GuestNetwork) map[string]int {
	sorted := make([]string, 0, len(nets))
	for _, n := range nets {
		sorted = append(sorted, n.ID)
	}
	sort.Strings(sorted)
	out := make(map[string]int, len(nets))
	used := map[int]bool{}
	for _, id := range sorted {
		h := fnv.New32a()
		_, _ = h.Write([]byte(id))
		v := int(h.Sum32()&0x7fffffff) % 0x7ffffffe
		v++
		for used[v] {
			v = v%0x7ffffffe + 1
		}
		used[v] = true
		out[id] = v
	}
	return out
}

// RenderKeaFile wraps the Dhcp4 object as the full file kea-dhcp4 reads on cold start.
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
