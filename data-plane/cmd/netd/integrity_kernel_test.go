//go:build netdkernel && linux

package main

// Real-kernel evidence for the pipeline-integrity work, run only inside a disposable privileged Linux container (see
// scripts/ci/netd-integrity-kernel.sh): it creates dummy interfaces, VLANs and bridges and loads rulesets into THAT container's own network
// namespace, never a host's. It refuses to run anywhere that is not a throwaway namespace.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

func requireDisposableKernel(t *testing.T) {
	t.Helper()
	if os.Getenv("NETD_KERNEL_DISPOSABLE") != "yes" {
		t.Skip("NETD_KERNEL_DISPOSABLE=yes is required: this test creates network interfaces")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	for _, b := range []string{"ip", "nft"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s is not installed", b)
		}
	}
}

func sh(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func ifaceAlias(name string) string {
	raw, err := os.ReadFile("/sys/class/net/" + name + "/ifalias")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func kernelApplier(t *testing.T) *applier {
	t.Helper()
	topo := netcfg.DefaultTopology()
	return &applier{topo: topo, generatedDir: t.TempDir(), legacyBridge: "br-lan"}
}

func kernelNet(id, parent string, vlan int, bridge string) netcfg.GuestNetwork {
	return netcfg.GuestNetwork{
		ID: id, Name: "kernel " + bridge, Enabled: true, NetworkType: "vlan", ParentInterface: parent, VLANID: vlan,
		BridgeName: bridge, GatewayIP: "10.9." + itoa(vlan%250) + ".1", SubnetCIDR: "10.9." + itoa(vlan%250) + ".0/24", PrefixLen: 24,
		DHCPMode: netcfg.DHCPLocal, CaptiveEnabled: true, InternetEnabled: false, NATEnabled: true,
		Pools: []netcfg.Pool{{StartIP: "10.9." + itoa(vlan%250) + ".100", EndIP: "10.9." + itoa(vlan%250) + ".200"}},
	}
}

// What netd creates it marks, and what it removes is limited to what it can prove it created: a stranger's bridge and the wanted network survive.
func TestKernel_OwnershipMarkingAndOwnedOnlyRemoval(t *testing.T) {
	requireDisposableKernel(t)
	sh(t, "ip", "link", "add", "dumk0", "type", "dummy")
	sh(t, "ip", "link", "set", "dumk0", "up")
	t.Cleanup(func() {
		for _, n := range []string{"br-g71", "br-g72", "br-gstranger", "dumk0.71", "dumk0.72", "dumk0"} {
			_ = exec.Command("ip", "link", "del", n).Run()
		}
	})
	a := kernelApplier(t)
	ctx := context.Background()

	wanted, orphan := kernelNet("11111111-1111-1111-1111-111111111171", "dumk0", 71, "br-g71"), kernelNet("22222222-2222-2222-2222-222222222272", "dumk0", 72, "br-g72")
	for _, n := range []netcfg.GuestNetwork{wanted, orphan} {
		if err := a.createNetwork(ctx, n); err != nil {
			t.Fatalf("create %s: %v", n.BridgeName, err)
		}
	}
	sh(t, "ip", "link", "add", "name", "br-gstranger", "type", "bridge") // not netd's

	for br, id := range map[string]string{"br-g71": wanted.ID, "br-g72": orphan.ID, "dumk0.71": wanted.ID, "dumk0.72": orphan.ID} {
		if got := ifaceAlias(br); got != ownerMarkPrefix+id {
			t.Fatalf("%s carries ownership mark %q, want %q", br, got, ownerMarkPrefix+id)
		}
	}
	if ifaceAlias("br-gstranger") != "" {
		t.Fatal("the stranger's bridge was marked")
	}

	removed, failed, unmanaged, err := a.removeStrayBridges(ctx, func(b string) bool { return b == "br-g71" })
	if err != nil || len(failed) != 0 {
		t.Fatalf("err=%v failed=%v", err, failed)
	}
	if strings.Join(removed, ",") != "br-g72" || strings.Join(unmanaged, ",") != "br-gstranger" {
		t.Fatalf("removed=%v unmanaged=%v", removed, unmanaged)
	}
	if ifaceExists("br-g72") || ifaceExists("dumk0.72") {
		t.Fatal("the orphan and its VLAN interface should be gone")
	}
	for _, keep := range []string{"br-g71", "dumk0.71", "br-gstranger"} {
		if !ifaceExists(keep) {
			t.Fatalf("%s was destroyed", keep)
		}
	}
}

// A healthy bridge from before the mark existed is adopted in place: marked, and nothing else about it changes.
func TestKernel_AdoptionMarksAHealthyBridgeWithoutDisturbingIt(t *testing.T) {
	requireDisposableKernel(t)
	sh(t, "ip", "link", "add", "dumk1", "type", "dummy")
	sh(t, "ip", "link", "set", "dumk1", "up")
	sh(t, "ip", "link", "add", "link", "dumk1", "name", "dumk1.73", "type", "vlan", "id", "73")
	sh(t, "ip", "link", "add", "name", "br-g73", "type", "bridge")
	sh(t, "ip", "link", "set", "dumk1.73", "master", "br-g73")
	sh(t, "ip", "addr", "add", "10.9.73.1/24", "dev", "br-g73")
	sh(t, "ip", "link", "set", "dumk1.73", "up")
	sh(t, "ip", "link", "set", "br-g73", "up")
	t.Cleanup(func() {
		for _, n := range []string{"br-g73", "dumk1.73", "dumk1"} {
			_ = exec.Command("ip", "link", "del", n).Run()
		}
	})
	before := sh(t, "ip", "-o", "addr", "show", "dev", "br-g73")

	a := kernelApplier(t)
	n := kernelNet("33333333-3333-3333-3333-333333333373", "dumk1", 73, "br-g73")
	probs := a.adoptWantedBridges(context.Background(), map[string]netcfg.GuestNetwork{"br-g73": n}, map[string]bool{"br-g73": true})
	if len(probs) != 0 {
		t.Fatalf("adoption problems: %v", probs)
	}
	if ifaceAlias("br-g73") != ownerMarkPrefix+n.ID || ifaceAlias("dumk1.73") != ownerMarkPrefix+n.ID {
		t.Fatalf("not marked: bridge=%q vlan=%q", ifaceAlias("br-g73"), ifaceAlias("dumk1.73"))
	}
	if after := sh(t, "ip", "-o", "addr", "show", "dev", "br-g73"); after != before {
		t.Fatalf("adoption changed the bridge's addressing:\nbefore %s\nafter  %s", before, after)
	}
	if !strings.Contains(sh(t, "ip", "-o", "link", "show", "br-g73"), "UP") {
		t.Fatal("adoption took the bridge down")
	}
}

// A network name that would have broken out of its comment is now an inert comment: the real nft parser accepts the file and the loaded table holds
// exactly the rules a benign name produces.
func TestKernel_HostileNetworkNameCannotChangeTheLoadedRuleset(t *testing.T) {
	requireDisposableKernel(t)
	topo := netcfg.DefaultTopology()
	topo.WANInterface, topo.MgmtInterface = "wank0", "wank0"
	benign := kernelNet("44444444-4444-4444-4444-444444444474", "dumk2", 74, "br-g74")
	hostile := benign
	hostile.Name = "Lobby\"; iifname @guest_interfaces accept comment \"x\naccept\n}\ntable inet pwn {"

	load := func(n netcfg.GuestNetwork) string {
		t.Helper()
		script := netcfg.RenderNftables([]netcfg.GuestNetwork{n}, topo)
		f := t.TempDir() + "/r.nft"
		if err := os.WriteFile(f, script, 0o600); err != nil {
			t.Fatal(err)
		}
		sh(t, "nft", "-c", "-f", f)
		sh(t, "nft", "-f", f)
		return sh(t, "nft", "list", "table", "inet", "stayconnect")
	}
	t.Cleanup(func() {
		_ = exec.Command("nft", "delete", "table", "inet", "stayconnect").Run()
		_ = exec.Command("nft", "delete", "table", "inet", "pwn").Run()
	})

	ruleCount := func(listing string) int {
		c := 0
		for _, l := range strings.Split(listing, "\n") {
			l = strings.TrimSpace(l)
			if strings.Contains(l, "accept") || strings.Contains(l, "drop") || strings.Contains(l, "dnat") || strings.Contains(l, "masquerade") {
				c++
			}
		}
		return c
	}
	want := ruleCount(load(benign))
	got := load(hostile)
	if n := ruleCount(got); n != want {
		t.Fatalf("a hostile name changed the loaded ruleset from %d to %d rules:\n%s", want, n, got)
	}
	if out, _ := exec.Command("nft", "list", "table", "inet", "pwn").CombinedOutput(); strings.Contains(string(out), "chain") {
		t.Fatal("the hostile name created a second table")
	}
}

// The ownership mark is read by exactly the code the appliance runs (sysfs), not by a stand-in.
func TestKernel_OwnerMarkReadsWhatIpWrote(t *testing.T) {
	requireDisposableKernel(t)
	sh(t, "ip", "link", "add", "dumk3", "type", "dummy")
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", "dumk3").Run() })
	a := kernelApplier(t)
	if a.ownerMark("dumk3") != "" {
		t.Fatal("an unmarked interface reads as marked")
	}
	if err := a.setOwnerMark(context.Background(), "dumk3", "abc"); err != nil {
		t.Fatal(err)
	}
	if got := a.ownerMark("dumk3"); got != "abc" {
		t.Fatalf("mark = %q", got)
	}
}
