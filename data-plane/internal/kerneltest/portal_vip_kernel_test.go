//go:build kernelgate

package kerneltest

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// THE PORTAL ADDRESS, AGAINST A REAL KERNEL, WITH REAL PACKETS (contract ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN §2.1).
//
// The renderer's own tests prove the rule text and its order. These prove what only the kernel decides: that the
// rendered ruleset LOADS, that a guest's 443 and 80 to the portal address are translated to a LOCAL listener port
// and admitted, that nothing else on the address is, that the WAN/management side cannot reach it even on the
// ports the management accepts are open for, and that the address is never forwarded.
//
// No listener runs in the router namespace, and that is the instrument: a TCP SYN that reaches the local stack on
// a port nobody listens on is answered with a RESET ("connection refused", immediately); a SYN the firewall drops
// gets nothing ("timed out"). So refused == admitted to the appliance, timed out == dropped.

const kgPortalVIP = "10.255.255.1"

func wanNS() string {
	if v := os.Getenv("KG_WAN_NS"); v != "" {
		return v
	}
	return "p3-wan" // the harness's WAN namespace (scripts/ci/kernel-netns-suite.sh)
}

// tcpProbe connects from a namespace and reports "refused", "timeout" or "connected".
func tcpProbe(t *testing.T, ns, ip, port string) string {
	t.Helper()
	cmd := exec.Command("ip", "netns", "exec", ns, "timeout", "3", "bash", "-c", "exec 3<>/dev/tcp/"+ip+"/"+port)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	switch {
	case err == nil:
		return "connected"
	case strings.Contains(string(out), "refused"):
		return "refused"
	case time.Since(start) >= 2900*time.Millisecond:
		return "timeout"
	default:
		t.Fatalf("probe %s:%s from %s failed unexpectedly: %v %s", ip, port, ns, err, out)
		return ""
	}
}

func portalSetup(t *testing.T) {
	t.Helper()
	e := newEngine(t)
	e.Topo.PortalVIP = kgPortalVIP
	t.Cleanup(func() { _ = exec.Command(ipWrapper, "link", "del", "ogportal0").Run() })
	run(t, ipWrapper, "link", "add", "ogportal0", "type", "dummy")
	run(t, ipWrapper, "addr", "replace", kgPortalVIP+"/32", "dev", "ogportal0")
	run(t, ipWrapper, "link", "set", "ogportal0", "up")
	if _, err := e.Ensure(context.Background(), convergeIntent(), "install"); err != nil {
		t.Fatalf("the real kernel refused the ruleset with a portal address: %v", err)
	}
}

func TestKernel_PortalAddressAnswersGuestsOnTheTwoPortalPortsOnly(t *testing.T) {
	portalSetup(t)
	// An UNAUTHENTICATED guest (no auth element): 443 and 80 are translated to 8343/8380 on the portal address and
	// admitted, so the local stack answers.
	for _, port := range []string{"443", "80"} {
		if got := tcpProbe(t, guestNS, kgPortalVIP, port); got != "refused" {
			t.Fatalf("guest -> portal address :%s = %s; want it to reach the appliance (refused, no listener)", port, got)
		}
	}
	// Anything else on the portal address is dropped.
	for _, port := range []string{"22", "53", "5432"} {
		if got := tcpProbe(t, guestNS, kgPortalVIP, port); got != "timeout" {
			t.Fatalf("guest -> portal address :%s = %s; only 443 and 80 may reach it", port, got)
		}
	}
	// And an AUTHENTICATED guest reaches it the same way (the redirect is for any auth state).
	run(t, nftWrapper, "add", "element", "inet", "stayconnect", "auth_ipv4", "{ \""+guestIface+"\" . "+guestIP+" }")
	if got := tcpProbe(t, guestNS, kgPortalVIP, "443"); got != "refused" {
		t.Fatalf("an authenticated guest -> portal address :443 = %s", got)
	}
}

func TestKernel_PortalAddressIsUnreachableFromTheWANAndManagementSide(t *testing.T) {
	portalSetup(t)
	// Give the WAN namespace a route to the portal address through the appliance -- the worst case: somebody
	// upstream points it at us. In this harness the WAN interface is ALSO the management interface, so SSH (22)
	// and the admin UI (443) are accepted on it by interface; only the explicit portal-address drop stops them.
	wan := wanNS()
	// 10.88.0.1 is the router's WAN-side address in the harness (WAN_GW in kernel-netns-suite.sh).
	if out, err := exec.Command("ip", "-n", wan, "route", "add", kgPortalVIP+"/32", "via", "10.88.0.1").CombinedOutput(); err != nil {
		t.Fatalf("route the portal address from the WAN namespace: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "-n", wan, "route", "del", kgPortalVIP+"/32").Run() })

	for _, port := range []string{"443", "22", "80", "8343", "8380"} {
		if got := tcpProbe(t, wan, kgPortalVIP, port); got != "timeout" {
			t.Fatalf("WAN/management -> portal address :%s = %s; it must be dropped", port, got)
		}
	}
	// Control: the management accept itself still works on the router's own WAN address (22 is accepted and
	// refused for lack of a listener), so the drop above is specific to the portal address.
	if got := tcpProbe(t, wan, "10.88.0.1", "22"); got != "refused" {
		t.Fatalf("control: WAN -> router :22 = %s; the management accept should still admit it", got)
	}
}

func TestKernel_PortalAddressIsNeverForwarded(t *testing.T) {
	portalSetup(t)
	// Remove the portal interface (a failed apply, a hand-deleted link) while the ruleset still names the address,
	// and put the address on the WAN side, where a forwarded-and-masqueraded packet WOULD be answered. An
	// authorized guest's packet must still go nowhere.
	run(t, ipWrapper, "link", "del", "ogportal0")
	// The harness router has no default route, so without one the kernel would answer "network unreachable"
	// before the forward hook ever ran -- which proves nothing about the ruleset. A real appliance routes
	// everything unknown out of the WAN, so the router is given that default route for this test.
	run(t, ipWrapper, "route", "add", "default", "via", "10.88.0.2")
	t.Cleanup(func() { _ = exec.Command(ipWrapper, "route", "del", "default").Run() })
	wan := wanNS()
	if out, err := exec.Command("ip", "-n", wan, "addr", "add", kgPortalVIP+"/32", "dev", "lo").CombinedOutput(); err != nil {
		t.Fatalf("place the address upstream: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "-n", wan, "addr", "del", kgPortalVIP+"/32", "dev", "lo").Run() })
	run(t, nftWrapper, "add", "element", "inet", "stayconnect", "auth_ipv4", "{ \""+guestIface+"\" . "+guestIP+" }")
	if !reaches(t) {
		t.Fatal("precondition: the authorized guest cannot reach the WAN at all")
	}
	for _, port := range []string{"443", "80", "22"} {
		if got := tcpProbe(t, guestNS, kgPortalVIP, port); got != "timeout" {
			t.Fatalf("authorized guest -> portal address :%s with no portal interface = %s; it was FORWARDED", port, got)
		}
	}
}
