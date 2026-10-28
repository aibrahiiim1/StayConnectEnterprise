package netcfg

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Audit Phase 5 M1 step 3 / Phase 8 M1: one guest address cannot hold more than PortalConnLimitPerSource connections
// open to the portal listeners. The cap counts new connections only, covers every portal port guests reach at the
// input hook, and sits ahead of every portal accept.
func TestPortalConnectionsAreCappedPerSource(t *testing.T) {
	for _, vip := range []string{"", "10.255.255.1"} {
		topo := topoT()
		topo.PortalVIP = vip
		in := inputChain(t, string(RenderNftables(twoNetworks(), topo)))
		rule := "ct state new add @portal_conn_per_src { ip saddr ct count over 256 } counter name \"portal_conn_limited\" drop"
		if strings.Count(in, rule) != 1 {
			t.Fatalf("vip=%q: the portal connection cap is missing or repeated:\n%s", vip, in)
		}
		capAt := strings.Index(in, "portal_conn_per_src")
		for _, accept := range []string{`accept comment "portal (own gateway)"`, `accept comment "portal address (TLS and HTTP portal)"`} {
			if i := strings.Index(in, accept); i >= 0 && i < capAt {
				t.Errorf("vip=%q: %q precedes the cap", vip, accept)
			}
		}
		line := in[strings.LastIndex(in[:capAt], "\n")+1:]
		line = line[:strings.Index(line, "\n")]
		want := []string{"8380"} // the TLS listener is open (and so capped) only with a portal address or TLS port
		if vip != "" {
			want = append(want, "8343")
		}
		for _, port := range want {
			if !strings.Contains(line, port) {
				t.Errorf("vip=%q: portal port %s is not capped: %s", vip, port, line)
			}
		}
		if !strings.Contains(line, "iifname @guest_interfaces") {
			t.Errorf("vip=%q: the cap must apply to guest networks only: %s", vip, line)
		}
	}
}

// Optional real-parser check: NETCFG_NFT_CHECK=1 with nft installed and root, run inside a disposable network
// namespace (`unshare -n`). Proves the kernel accepts the rendered set and rule.
func TestPortalConnectionCapLoadsInAKernel(t *testing.T) {
	if os.Getenv("NETCFG_NFT_CHECK") != "1" {
		t.Skip("set NETCFG_NFT_CHECK=1 inside a throwaway network namespace to load the ruleset")
	}
	topo := topoT()
	topo.PortalVIP = "10.255.255.1"
	f := t.TempDir() + "/r.nft"
	if err := os.WriteFile(f, RenderNftables(twoNetworks(), topo), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runNft(f); err != nil {
		t.Fatalf("nft refused the ruleset: %v\n%s", err, out)
	}
}

func runNft(file string) ([]byte, error) { return exec.Command("nft", "-f", file).CombinedOutput() }
