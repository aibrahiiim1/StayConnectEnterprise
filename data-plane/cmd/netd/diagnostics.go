package main

// Read-only diagnostics of the confirmed client networks, from what this appliance can observe. Every check names where it was observed; what the appliance
// cannot see (a switch trunk, a hypervisor port group, an access point's VLAN mapping) is listed as something to check outside it and is never reported as
// verified or as the cause. Nothing here changes any state, and nothing is invented: a value that cannot be read is "unknown".

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

const (
	checkOK      = "ok"
	checkFail    = "fail"
	checkUnknown = "unknown"
	checkInfo    = "info"

	whereAppliance = "appliance"
)

type diagCheck struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	Where  string `json:"where"`
}

type networkDiag struct {
	NetworkID string      `json:"network_id"`
	Name      string      `json:"name"`
	Bridge    string      `json:"bridge"`
	Parent    string      `json:"parent_interface"`
	VLAN      int         `json:"vlan_id,omitempty"`
	Checks    []diagCheck `json:"checks"`
	// Findings are plain statements derived only from the observed checks.
	Findings []string `json:"findings"`
	// Outside lists what cannot be observed from this appliance and may need checking where it is.
	Outside []string `json:"outside"`
}

type diagReport struct {
	GeneratedAt    time.Time      `json:"generated_at"`
	Revision       string         `json:"revision,omitempty"`
	Networks       []networkDiag  `json:"networks"`
	Server         []diagCheck    `json:"server"`
	DHCPCounters   map[string]int `json:"dhcp_counters,omitempty"`
	SampleMillis   int            `json:"sample_ms,omitempty"`
	NoConfirmedCfg bool           `json:"no_confirmed_configuration,omitempty"`
}

// diagSources are the machine reads diagnostics makes, replaceable by a test.
type diagSources struct {
	sys          func(iface, file string) (string, bool)
	addrs        func(iface string) []string
	procUDP      func() string
	leases       func() ([]KeaLease, error)
	keaRunning   func() (map[string]any, error)
	keaCounters  func() (map[string]int, error)
	tcpListening func(port int) bool
	sleep        func(time.Duration)
}

func (a *applier) diagnosticSources() diagSources {
	if a.diag != nil {
		return *a.diag
	}
	d := diagSources{
		sys: func(iface, file string) (string, bool) {
			raw, err := os.ReadFile("/sys/class/net/" + iface + "/" + file)
			if err != nil {
				return "", false
			}
			return strings.TrimSpace(string(raw)), true
		},
		addrs:        ifaceAddrs,
		procUDP:      readProcNetUDP,
		tcpListening: tcpListening,
		sleep:        time.Sleep,
	}
	if a.kea != nil {
		d.leases = a.kea.Leases
		d.keaRunning = a.kea.RunningDhcp4
		d.keaCounters = a.kea.Counters
	}
	return d
}

// diagnose reports on the confirmed configuration. sample, when positive, also measures frames received over that long (bounded by the caller).
func (a *applier) diagnose(ctx context.Context, sample time.Duration) diagReport {
	src := a.diagnosticSources()
	rep := diagReport{GeneratedAt: time.Now().UTC(), Networks: []networkDiag{}, Server: []diagCheck{}}
	id, _, intent, err := a.currentActiveIntent(ctx)
	if err != nil || id == "" {
		rep.NoConfirmedCfg = true
		return rep
	}
	rep.Revision = id

	var leases []KeaLease
	var leaseErr error
	if src.leases != nil {
		leases, leaseErr = src.leases()
	}
	var running map[string]any
	var runErr error
	if src.keaRunning != nil {
		running, runErr = src.keaRunning()
	}
	proc := ""
	if src.procUDP != nil {
		proc = src.procUDP()
	}

	// Frames received are read twice when a sample is asked for, so "none yet" can be told from "none while watching".
	type counters struct{ rx, tx int64 }
	read := func(iface string) (counters, bool) {
		rx, ok1 := src.sys(iface, "statistics/rx_packets")
		tx, ok2 := src.sys(iface, "statistics/tx_packets")
		if !ok1 || !ok2 {
			return counters{}, false
		}
		r, e1 := strconv.ParseInt(rx, 10, 64)
		t, e2 := strconv.ParseInt(tx, 10, 64)
		if e1 != nil || e2 != nil {
			return counters{}, false
		}
		return counters{r, t}, true
	}
	before := map[string]counters{}
	if sample > 0 {
		for _, n := range a.netdManaged(intent) {
			if n.Enabled {
				if c, ok := read(memberOf(n)); ok {
					before[memberOf(n)] = c
				}
			}
		}
		src.sleep(sample)
		rep.SampleMillis = int(sample / time.Millisecond)
	}

	for _, n := range a.netdManaged(intent) {
		if !n.Enabled {
			continue
		}
		nd := networkDiag{NetworkID: n.ID, Name: n.Name, Bridge: n.BridgeName, Parent: n.ParentInterface, VLAN: n.VLANID,
			Checks: []diagCheck{}, Findings: []string{}, Outside: []string{}}
		add := func(id, label, state, detail string) {
			nd.Checks = append(nd.Checks, diagCheck{ID: id, Label: label, State: state, Detail: detail, Where: whereAppliance})
		}

		// 1. The physical port.
		carrier, okC := src.sys(n.ParentInterface, "carrier")
		oper, _ := src.sys(n.ParentInterface, "operstate")
		switch {
		case !okC:
			add("port_link", "Port link", checkUnknown, n.ParentInterface+" could not be read")
		case carrier == "1":
			add("port_link", "Port link", checkOK, n.ParentInterface+" has link ("+oper+")")
		default:
			add("port_link", "Port link", checkFail, n.ParentInterface+" has no link")
			nd.Findings = append(nd.Findings, "The port "+n.ParentInterface+" reports no link.")
		}

		// 2. The VLAN device the frames must reach, and how many have.
		member := memberOf(n)
		if n.NetworkType == "vlan" {
			if _, ok := src.sys(member, "operstate"); !ok {
				add("vlan_device", "VLAN interface", checkFail, member+" does not exist")
				nd.Findings = append(nd.Findings, "The VLAN interface "+member+" does not exist on this appliance.")
			} else {
				st, _ := src.sys(member, "operstate")
				add("vlan_device", "VLAN interface", checkOK, member+" exists ("+st+")")
			}
		}
		if now, ok := read(member); ok {
			detail := fmt.Sprintf("%d frames received and %d sent on %s since it was created", now.rx, now.tx, member)
			state := checkInfo
			if b, had := before[member]; had {
				gained := now.rx - b.rx
				detail += fmt.Sprintf("; %d received in the last %s", gained, sample)
				if gained == 0 {
					state = checkFail
				}
			} else if now.rx == 0 {
				state = checkFail
			}
			add("frames", "Frames delivered to the interface", state, detail)
			if state == checkFail && carrier == "1" {
				nd.Findings = append(nd.Findings, "No frames were delivered to "+member+" by this appliance's network stack. That does not show where they were lost.")
				nd.Outside = append(nd.Outside, outsideHints(n)...)
			}
		} else {
			add("frames", "Frames delivered to the interface", checkUnknown, "the counters of "+member+" could not be read")
		}

		// 3. The bridge and its gateway address.
		if st, ok := src.sys(n.BridgeName, "operstate"); !ok {
			add("bridge", "Bridge", checkFail, n.BridgeName+" does not exist")
			nd.Findings = append(nd.Findings, "The bridge "+n.BridgeName+" does not exist.")
		} else if !hasAddr(src.addrs(n.BridgeName), n.GatewayIP) {
			add("bridge", "Bridge", checkFail, n.BridgeName+" ("+st+") does not carry "+n.GatewayIP)
			nd.Findings = append(nd.Findings, "The gateway address "+n.GatewayIP+" is not on "+n.BridgeName+".")
		} else {
			add("bridge", "Bridge and gateway address", checkOK, n.BridgeName+" carries "+n.GatewayIP)
		}

		// 4. DHCP: served at all, bound to this gateway, configured for this subnet, and how full.
		a.diagDHCP(&nd, add, n, proc, running, runErr, leases, leaseErr)

		// 5. DNS and the portal listener on this gateway.
		if udpBound(proc, n.GatewayIP, 53) {
			add("dns", "DNS resolver", checkOK, "answering on "+n.GatewayIP+":53")
		} else if proc == "" {
			add("dns", "DNS resolver", checkUnknown, "the socket table could not be read")
		} else {
			add("dns", "DNS resolver", checkFail, "nothing is listening on "+n.GatewayIP+":53")
			nd.Findings = append(nd.Findings, "Nothing is answering DNS on "+n.GatewayIP+".")
		}
		if src.tcpListening != nil {
			if src.tcpListening(a.topo.PortalHTTPPort) {
				add("portal", "Client Portal listener", checkOK, "portald answers on port "+strconv.Itoa(a.topo.PortalHTTPPort))
			} else {
				add("portal", "Client Portal listener", checkFail, "portald does not answer on port "+strconv.Itoa(a.topo.PortalHTTPPort))
			}
		}
		rep.Networks = append(rep.Networks, nd)
	}

	// Appliance-wide: the file Kea starts from, and whether changes are being refused.
	ok, detail := a.keaDiskState()
	switch {
	case !ok:
		rep.Server = append(rep.Server, diagCheck{ID: "kea_file", Label: "DHCP configuration saved for a restart", State: checkFail, Detail: detail, Where: whereAppliance})
	case a.keaFileMatchesConfirmed(ctx):
		rep.Server = append(rep.Server, diagCheck{ID: "kea_file", Label: "DHCP configuration saved for a restart", State: checkOK, Detail: "the file Kea starts from holds the confirmed configuration", Where: whereAppliance})
	default:
		rep.Server = append(rep.Server, diagCheck{ID: "kea_file", Label: "DHCP configuration saved for a restart", State: checkFail,
			Detail: "the file Kea starts from differs from the confirmed configuration; a Kea restart would not serve it", Where: whereAppliance})
	}
	if m, blocked := a.rollbackIncomplete(); blocked {
		rep.Server = append(rep.Server, diagCheck{ID: "changes_blocked", Label: "Changes", State: checkFail, Detail: m.describe(), Where: whereAppliance})
	}
	if src.keaCounters != nil {
		if c, err := src.keaCounters(); err == nil {
			rep.DHCPCounters = c
		}
	}
	return rep
}

func (a *applier) diagDHCP(nd *networkDiag, add func(id, label, state, detail string), n netcfg.GuestNetwork, proc string, running map[string]any, runErr error, leases []KeaLease, leaseErr error) {
	if n.DHCPMode != netcfg.DHCPLocal {
		add("dhcp", "DHCP", checkInfo, "this network does not use the appliance's DHCP server ("+string(n.DHCPMode)+")")
		return
	}
	switch {
	case proc == "":
		add("dhcp_bound", "DHCP server bound to the gateway", checkUnknown, "the socket table could not be read")
	case keaListeningIn(proc, n.GatewayIP):
		add("dhcp_bound", "DHCP server bound to the gateway", checkOK, "Kea holds a DHCP socket for "+n.GatewayIP)
	default:
		add("dhcp_bound", "DHCP server bound to the gateway", checkFail, "Kea holds no DHCP socket for "+n.GatewayIP)
		nd.Findings = append(nd.Findings, "The DHCP server is not bound to "+n.GatewayIP+", so clients on this network get no address.")
	}
	if runErr != nil {
		add("dhcp_config", "DHCP configuration in force", checkUnknown, "Kea's running configuration could not be read: "+runErr.Error())
	} else if running != nil {
		if subnetConfigured(running, n.SubnetCIDR) {
			add("dhcp_config", "DHCP configuration in force", checkOK, "Kea serves "+n.SubnetCIDR)
		} else {
			add("dhcp_config", "DHCP configuration in force", checkFail, "Kea's running configuration has no subnet "+n.SubnetCIDR)
			nd.Findings = append(nd.Findings, "Kea is not configured to serve "+n.SubnetCIDR+".")
		}
	}
	total := poolSize(n.Pools)
	if leaseErr != nil {
		add("dhcp_pool", "Address pool", checkUnknown, "the leases could not be read: "+leaseErr.Error())
		return
	}
	used := leasesIn(leases, n.SubnetCIDR)
	switch {
	case total == 0:
		add("dhcp_pool", "Address pool", checkFail, "the network has no address pool")
	case used >= total:
		add("dhcp_pool", "Address pool", checkFail, fmt.Sprintf("%d of %d addresses are leased: the pool is exhausted", used, total))
		nd.Findings = append(nd.Findings, "The address pool is exhausted; new clients get no address.")
	default:
		add("dhcp_pool", "Address pool", checkInfo, fmt.Sprintf("%d of %d addresses are leased", used, total))
	}
}

// memberOf is the interface frames for the network arrive on: the VLAN sub-interface, or the port itself for an untagged network.
func memberOf(n netcfg.GuestNetwork) string {
	if n.NetworkType == "vlan" {
		return netcfg.VLANIfaceName(n.ParentInterface, n.VLANID)
	}
	return n.ParentInterface
}

// outsideHints are the places a missing frame can be lost that this appliance cannot see.
func outsideHints(n netcfg.GuestNetwork) []string {
	if n.NetworkType == "vlan" {
		v := strconv.Itoa(n.VLANID)
		return []string{
			"The switch port facing this appliance allows VLAN " + v + " (trunk allowed list).",
			"The virtual switch or hypervisor port group passes VLAN " + v + " to this appliance.",
			"The access point or controller maps the Wi-Fi network to VLAN " + v + ".",
		}
	}
	return []string{
		"The switch port and the hypervisor port group deliver the untagged (native) VLAN to this appliance.",
		"The access point or controller puts the Wi-Fi network on that untagged VLAN.",
	}
}

func hasAddr(cidrs []string, ip string) bool {
	for _, c := range cidrs {
		if strings.HasPrefix(c, ip+"/") {
			return true
		}
	}
	return false
}

// udpBound reports whether anything holds the UDP port on the address or the wildcard.
func udpBound(procNetUDP, ip string, port int) bool {
	want := hexLE(ip)
	if want == "" {
		return false
	}
	hexPort := strings.ToUpper(fmt.Sprintf("%04x", port))
	for _, line := range strings.Split(procNetUDP, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		local := f[1]
		i := strings.IndexByte(local, ':')
		if i < 0 {
			continue
		}
		addr, p := strings.ToUpper(local[:i]), strings.ToUpper(local[i+1:])
		if p == hexPort && (addr == want || addr == "00000000") {
			return true
		}
	}
	return false
}

func subnetConfigured(dhcp4 map[string]any, cidr string) bool {
	list, _ := dhcp4["subnet4"].([]any)
	_, want, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	for _, s := range list {
		m, _ := s.(map[string]any)
		c, _ := m["subnet"].(string)
		if _, got, perr := net.ParseCIDR(c); perr == nil && got.String() == want.String() {
			return true
		}
	}
	return false
}

func poolSize(pools []netcfg.Pool) int {
	total := 0
	for _, p := range pools {
		s, e := net.ParseIP(p.StartIP).To4(), net.ParseIP(p.EndIP).To4()
		if s == nil || e == nil {
			continue
		}
		lo, _ := ipToU32(s)
		hi, _ := ipToU32(e)
		if hi >= lo {
			total += int(hi-lo) + 1
		}
	}
	return total
}

func ipToU32(ip net.IP) (uint32, bool) {
	v := ip.To4()
	if v == nil {
		return 0, false
	}
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3]), true
}

// leasesIn counts current leases (not released or lapsed) inside the subnet.
func leasesIn(leases []KeaLease, cidr string) int {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0
	}
	now := time.Now().Unix()
	c := 0
	for _, l := range leases {
		ip := net.ParseIP(l.IPAddress)
		if ip == nil || !n.Contains(ip) {
			continue
		}
		if l.State == keaLeaseStateExpired || l.ValidLft <= 0 || l.CLTT+int64(l.ValidLft) <= now {
			continue
		}
		c++
	}
	return c
}

// keaFileMatchesConfirmed reports whether the file Kea starts from is byte-for-byte what netd would write for the confirmed configuration.
func (a *applier) keaFileMatchesConfirmed(ctx context.Context) bool {
	_, _, intent, err := a.currentActiveIntent(ctx)
	if err != nil {
		return false
	}
	want, err := a.confirmedKeaFile(intent)
	if err != nil {
		return false
	}
	have, err := os.ReadFile(a.keaConfPath())
	return err == nil && string(have) == string(want)
}
