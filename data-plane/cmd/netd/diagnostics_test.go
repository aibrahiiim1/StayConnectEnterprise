package main

// Diagnostics report what the appliance observes and never more. The reported VLAN case (a VLAN interface that has received no frames while its bridge,
// gateway and DHCP server are all in place) must say exactly that, and must not name a cause the appliance cannot see.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

type diagWorld struct {
	sys      map[string]string // "iface/file" -> value
	addrs    map[string][]string
	proc     string
	leases   []KeaLease
	leaseErr error
	running  map[string]any
	runErr   error
	counters map[string]int
	listen   bool
	sleeps   []time.Duration
	onSleep  func()
}

func (w *diagWorld) sources() *diagSources {
	return &diagSources{
		sys: func(iface, file string) (string, bool) {
			v, ok := w.sys[iface+"/"+file]
			return v, ok
		},
		addrs:        func(i string) []string { return w.addrs[i] },
		procUDP:      func() string { return w.proc },
		leases:       func() ([]KeaLease, error) { return w.leases, w.leaseErr },
		keaRunning:   func() (map[string]any, error) { return w.running, w.runErr },
		keaCounters:  func() (map[string]int, error) { return w.counters, nil },
		tcpListening: func(int) bool { return w.listen },
		sleep: func(d time.Duration) {
			w.sleeps = append(w.sleeps, d)
			if w.onSleep != nil {
				w.onSleep()
			}
		},
	}
}

func vlan60() netcfg.GuestNetwork {
	return netcfg.GuestNetwork{
		ID: "net-60", Name: "VLAN 60", Enabled: true, NetworkType: "vlan", ParentInterface: "ens192", VLANID: 60, BridgeName: "br-g60",
		GatewayIP: "10.60.0.1", SubnetCIDR: "10.60.0.0/24", PrefixLen: 24, DHCPMode: netcfg.DHCPLocal,
		Pools: []netcfg.Pool{{StartIP: "10.60.0.100", EndIP: "10.60.0.250"}},
	}
}

// healthyWorld is the VLAN 60 case as it was observed: everything on the appliance in place, no frames delivered to the VLAN interface.
func healthyWorld(rx string) *diagWorld {
	return &diagWorld{
		sys: map[string]string{
			"ens192/carrier": "1", "ens192/operstate": "up",
			"ens192.60/operstate": "up", "ens192.60/statistics/rx_packets": rx, "ens192.60/statistics/tx_packets": "18",
			"br-g60/operstate": "up",
		},
		addrs:    map[string][]string{"br-g60": {"10.60.0.1/24"}},
		proc:     "  sl  local_address rem_address st\n   1: " + hexLE("10.60.0.1") + ":0043 00000000:0000 07\n   2: " + hexLE("10.60.0.1") + ":0035 00000000:0000 07\n",
		running:  map[string]any{"subnet4": []any{map[string]any{"subnet": "10.60.0.0/24"}}},
		counters: map[string]int{"pkt4-received": 20562, "pkt4-discover-received": 8370},
		listen:   true,
	}
}

func diagApplier(t *testing.T, w *diagWorld, nets ...netcfg.GuestNetwork) *applier {
	t.Helper()
	k := newFakeKernel(t)
	a := applyFixture(t, k, newFakeStore(nets))
	a.diag = w.sources()
	a.activeIntentFn = func(context.Context) (string, string, []netcfg.GuestNetwork, error) { return "rev-9", "", nets, nil }
	return a
}

func findCheck(nd networkDiag, id string) diagCheck {
	for _, c := range nd.Checks {
		if c.ID == id {
			return c
		}
	}
	return diagCheck{ID: "missing:" + id}
}

func TestAVlanThatReceivesNoFramesIsReportedAsExactlyThat(t *testing.T) {
	a := diagApplier(t, healthyWorld("0"), vlan60())
	rep := a.diagnose(context.Background(), 0)
	if len(rep.Networks) != 1 {
		t.Fatalf("networks: %+v", rep.Networks)
	}
	nd := rep.Networks[0]
	if c := findCheck(nd, "frames"); c.State != checkFail || !strings.Contains(c.Detail, "0 frames received") {
		t.Fatalf("frames check: %+v", c)
	}
	for _, id := range []string{"port_link", "vlan_device", "bridge", "dhcp_bound", "dhcp_config", "dns", "portal"} {
		if c := findCheck(nd, id); c.State != checkOK {
			t.Errorf("%s should be ok on this appliance: %+v", id, c)
		}
	}
	joined := strings.Join(nd.Findings, " ")
	if !strings.Contains(joined, "No frames were delivered to ens192.60") || !strings.Contains(joined, "does not show where they were lost") {
		t.Fatalf("findings do not say what was observed and what it does not show: %q", joined)
	}
	// It must not assert a cause it cannot see.
	for _, bad := range []string{"switch is", "trunk is misconfigured", "port group is", "caused by", "because"} {
		if strings.Contains(strings.ToLower(joined), bad) {
			t.Fatalf("the finding names a cause (%q): %q", bad, joined)
		}
	}
	// The places it cannot see are listed to be checked, and say VLAN 60.
	out := strings.Join(nd.Outside, " | ")
	for _, want := range []string{"switch port", "port group", "access point", "VLAN 60"} {
		if !strings.Contains(out, want) {
			t.Fatalf("outside list is missing %q: %q", want, out)
		}
	}
	for _, c := range nd.Checks {
		if c.Where != whereAppliance {
			t.Fatalf("a check claims a place other than the appliance: %+v", c)
		}
	}
}

func TestANetworkReceivingFramesHasNothingToCheckOutside(t *testing.T) {
	a := diagApplier(t, healthyWorld("4252015"), vlan60())
	nd := a.diagnose(context.Background(), 0).Networks[0]
	if len(nd.Findings) != 0 || len(nd.Outside) != 0 {
		t.Fatalf("a working network produced findings %v / outside %v", nd.Findings, nd.Outside)
	}
	if c := findCheck(nd, "frames"); c.State != checkInfo {
		t.Fatalf("frames: %+v", c)
	}
}

func TestSamplingTellsNoneYetFromNoneWhileWatching(t *testing.T) {
	w := healthyWorld("500")
	a := diagApplier(t, w, vlan60())
	nd := a.diagnose(context.Background(), 2*time.Second).Networks[0]
	if c := findCheck(nd, "frames"); c.State != checkFail || !strings.Contains(c.Detail, "0 received in the last 2s") {
		t.Fatalf("a quiet interface during the sample: %+v", c)
	}
	if len(w.sleeps) != 1 || w.sleeps[0] != 2*time.Second {
		t.Fatalf("sleeps: %v", w.sleeps)
	}

	w2 := healthyWorld("500")
	w2.onSleep = func() { w2.sys["ens192.60/statistics/rx_packets"] = "530" }
	a2 := diagApplier(t, w2, vlan60())
	nd2 := a2.diagnose(context.Background(), time.Second).Networks[0]
	if c := findCheck(nd2, "frames"); c.State != checkInfo || !strings.Contains(c.Detail, "30 received in the last 1s") {
		t.Fatalf("an active interface during the sample: %+v", c)
	}
}

func TestDiagnosticsNeverInventAValueTheyCannotRead(t *testing.T) {
	w := healthyWorld("0")
	delete(w.sys, "ens192.60/statistics/rx_packets")
	delete(w.sys, "ens192/carrier")
	w.proc = ""
	w.leaseErr = errors.New("kea down")
	w.runErr = errors.New("kea down")
	a := diagApplier(t, w, vlan60())
	nd := a.diagnose(context.Background(), 0).Networks[0]
	for _, id := range []string{"port_link", "frames", "dhcp_bound", "dhcp_config", "dhcp_pool", "dns"} {
		if c := findCheck(nd, id); c.State != checkUnknown {
			t.Errorf("%s should be unknown when it cannot be read: %+v", id, c)
		}
	}
	if len(nd.Outside) != 0 {
		t.Fatalf("outside hints were offered although nothing was observed: %v", nd.Outside)
	}
}

func TestDhcpNotBoundOrNotConfiguredOrExhaustedIsFoundOnTheAppliance(t *testing.T) {
	w := healthyWorld("100")
	w.proc = "  sl  local_address rem_address st\n"
	w.running = map[string]any{"subnet4": []any{map[string]any{"subnet": "10.99.0.0/24"}}}
	n := vlan60()
	n.Pools = []netcfg.Pool{{StartIP: "10.60.0.100", EndIP: "10.60.0.101"}}
	now := time.Now().Unix()
	w.leases = []KeaLease{
		{IPAddress: "10.60.0.100", HWAddr: "aa:bb:cc:00:00:01", ValidLft: 600, CLTT: now},
		{IPAddress: "10.60.0.101", HWAddr: "aa:bb:cc:00:00:02", ValidLft: 600, CLTT: now},
		{IPAddress: "10.60.0.50", HWAddr: "aa:bb:cc:00:00:03", ValidLft: 600, CLTT: now, State: keaLeaseStateExpired},
	}
	nd := diagApplier(t, w, n).diagnose(context.Background(), 0).Networks[0]
	for id, state := range map[string]string{"dhcp_bound": checkFail, "dhcp_config": checkFail, "dhcp_pool": checkFail, "dns": checkFail} {
		if c := findCheck(nd, id); c.State != state {
			t.Errorf("%s = %+v, want %s", id, c, state)
		}
	}
	joined := strings.Join(nd.Findings, " | ")
	for _, want := range []string{"not bound to 10.60.0.1", "not configured to serve 10.60.0.0/24", "pool is exhausted"} {
		if !strings.Contains(joined, want) {
			t.Errorf("findings lack %q: %s", want, joined)
		}
	}
	if c := findCheck(nd, "dhcp_pool"); !strings.Contains(c.Detail, "2 of 2") {
		t.Fatalf("an expired lease was counted: %+v", c)
	}
}

func TestExternalDhcpIsNotCheckedAgainstTheLocalServer(t *testing.T) {
	n := vlan60()
	n.DHCPMode = netcfg.DHCPExternal
	nd := diagApplier(t, healthyWorld("100"), n).diagnose(context.Background(), 0).Networks[0]
	if c := findCheck(nd, "dhcp"); c.State != checkInfo || findCheck(nd, "dhcp_bound").State == checkFail {
		t.Fatalf("an external DHCP network was judged by the local server: %+v", nd.Checks)
	}
}

func TestDisabledNetworksAreNotReportedOn(t *testing.T) {
	n := vlan60()
	n.Enabled = false
	if rep := diagApplier(t, healthyWorld("0"), n).diagnose(context.Background(), 0); len(rep.Networks) != 0 {
		t.Fatalf("%+v", rep.Networks)
	}
}

func TestNoConfirmedConfigurationIsSaidOutright(t *testing.T) {
	a := diagApplier(t, healthyWorld("0"), vlan60())
	a.activeIntentFn = func(context.Context) (string, string, []netcfg.GuestNetwork, error) {
		return "", "", nil, ErrNoConfirmedActiveRevision
	}
	if rep := a.diagnose(context.Background(), 0); !rep.NoConfirmedCfg || len(rep.Networks) != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestTheKeaFileCheckComparesTheFileToTheConfirmedConfiguration(t *testing.T) {
	a := diagApplier(t, healthyWorld("100"), vlan60())
	rep := a.diagnose(context.Background(), 0)
	if c := rep.Server[0]; c.ID != "kea_file" || c.State != checkFail {
		t.Fatalf("a missing file passed: %+v", c)
	}
	if err := a.persistConfirmedKea(context.Background(), "rev-9"); err != nil {
		t.Fatal(err)
	}
	if c := a.diagnose(context.Background(), 0).Server[0]; c.State != checkOK {
		t.Fatalf("the saved file was not recognised: %+v", c)
	}
	if err := os.WriteFile(a.keaConfPath(), []byte(`{"Dhcp4":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := a.diagnose(context.Background(), 0).Server[0]; c.State != checkFail || !strings.Contains(c.Detail, "differs") {
		t.Fatalf("a stale file passed: %+v", c)
	}
}

func TestBlockedChangesAreVisibleInDiagnostics(t *testing.T) {
	a := diagApplier(t, healthyWorld("100"), vlan60())
	if err := a.markRollbackIncomplete("rev-8", []string{"kea not restored"}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range a.diagnose(context.Background(), 0).Server {
		if c.ID == "changes_blocked" && c.State == checkFail && strings.Contains(c.Detail, "kea not restored") {
			found = true
		}
	}
	if !found {
		t.Fatal("a refusal to apply changes is not shown")
	}
}

func TestDhcpCountersAreCarriedThrough(t *testing.T) {
	rep := diagApplier(t, healthyWorld("100"), vlan60()).diagnose(context.Background(), 0)
	if rep.DHCPCounters["pkt4-discover-received"] != 8370 {
		t.Fatalf("%v", rep.DHCPCounters)
	}
	_ = fmt.Sprint
}

func TestUntaggedNetworkOutsideHintsAskForTheNativeVlan(t *testing.T) {
	n := vlan60()
	n.NetworkType, n.VLANID, n.BridgeName = "untagged", 0, "br-g-00d1fa1a"
	w := healthyWorld("0")
	w.sys["ens192/statistics/rx_packets"] = "0"
	w.sys["ens192/statistics/tx_packets"] = "3"
	w.sys["br-g-00d1fa1a/operstate"] = "up"
	w.addrs["br-g-00d1fa1a"] = []string{"10.60.0.1/24"}
	nd := diagApplier(t, w, n).diagnose(context.Background(), 0).Networks[0]
	if out := strings.Join(nd.Outside, " "); !strings.Contains(out, "untagged (native) VLAN") {
		t.Fatalf("%q", out)
	}
}
