package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// Interface is an observed appliance interface (from `ip -j addr`).
type Interface struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac"`
	LinkState string   `json:"link_state"`
	MTU       int      `json:"mtu"`
	IPs       []string `json:"ips"`
	Kind      string   `json:"kind"` // device|vlan|bridge|bond|veth
	Parent    string   `json:"parent,omitempty"`
	// Role/Protected are the operator-assigned classification from the
	// network_interfaces inventory (guest_access|guest_trunk|ha_sync|unused|
	// management|wan). Discover() leaves these empty; the /v1/interfaces handler
	// fills them from the store so the UI can tell which interfaces may parent a
	// guest network.
	Role      string `json:"role"`
	Protected bool   `json:"is_protected"`
}

type ipAddrEntry struct {
	IfName    string `json:"ifname"`
	Link      string `json:"link,omitempty"`
	Address   string `json:"address"`
	OperState string `json:"operstate"`
	MTU       int    `json:"mtu"`
	LinkType  string `json:"link_type"`
	AddrInfo  []struct {
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
		Family    string `json:"family"`
	} `json:"addr_info"`
	LinkInfo struct {
		InfoKind string `json:"info_kind"`
	} `json:"linkinfo"`
}

// Discover enumerates real appliance interfaces, skipping loopback, docker,
// veth and stayconnect-generated guest bridges (those are described by the DB,
// not offered as parent candidates). Guest bridges start with "br-g".
func Discover(ctx context.Context) ([]Interface, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ip", "-j", "addr").Output()
	if err != nil {
		return nil, err
	}
	var entries []ipAddrEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, err
	}
	var ifaces []Interface
	for _, e := range entries {
		if skipIface(e.IfName) {
			continue
		}
		var ips []string
		for _, a := range e.AddrInfo {
			if a.Family == "inet" {
				ips = append(ips, a.Local+"/"+itoa(a.PrefixLen))
			}
		}
		kind := "device"
		switch e.LinkInfo.InfoKind {
		case "vlan":
			kind = "vlan"
		case "bridge":
			kind = "bridge"
		case "bond":
			kind = "bond"
		case "veth":
			kind = "veth"
		}
		ifaces = append(ifaces, Interface{
			Name:      e.IfName,
			MAC:       e.Address,
			LinkState: strings.ToLower(e.OperState),
			MTU:       e.MTU,
			IPs:       ips,
			Kind:      kind,
			Parent:    e.Link,
		})
	}
	return ifaces, nil
}

func skipIface(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range []string{"docker", "veth", "br-", "vcli", "veth", "kube", "cni", "flannel"} {
		if strings.HasPrefix(name, p) {
			// br-lan is the legacy guest bridge — keep it visible as it maps to
			// the legacy guest network; hide docker bridges (br-<12hex>) and
			// generated guest bridges (br-g*).
			if name == "br-lan" {
				return false
			}
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
