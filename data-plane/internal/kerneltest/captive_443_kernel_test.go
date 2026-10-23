//go:build kernelgate

package kerneltest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func wanListener(t *testing.T) {
	t.Helper()
	wan := wanNS()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatal("python3 is required for the :443 listener (scripts/ci/kernel-netns-suite.sh installs it)")
	}
	const srv = "import socket\n" +
		"s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)\n" +
		"s.bind(('0.0.0.0', 443)); s.listen(16)\n" +
		"while True:\n    c,_=s.accept(); c.close()\n"
	cmd := exec.Command("ip", "netns", "exec", wan, "python3", "-c", srv)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the WAN listener: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 50; i++ {
		if exec.Command("ip", "netns", "exec", wan, "timeout", "1", "bash", "-c", "exec 3<>/dev/tcp/127.0.0.1/443").Run() == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the WAN :443 listener never came up")
}

var counterPacketsRe = regexp.MustCompile(`packets (\d+)`)

// wanSYNCounter counts every TCP SYN to :443 that ARRIVES in the WAN namespace -- i.e. that left the appliance.
func wanSYNCounter(t *testing.T) func() int {
	t.Helper()
	wan := wanNS()
	file := filepath.Join(t.TempDir(), "wan-counter.nft")
	body := "table inet kgwan\ndelete table inet kgwan\ntable inet kgwan {\n" +
		"\tcounter syn443 {}\n" +
		"\tchain in {\n\t\ttype filter hook prerouting priority -300; policy accept;\n" +
		"\t\ttcp dport 443 tcp flags & (syn | ack) == syn counter name \"syn443\"\n\t}\n}\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ip", "netns", "exec", wan, "nft", "-f", file).CombinedOutput(); err != nil {
		t.Fatalf("install the WAN-side counter: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "exec", wan, "nft", "delete", "table", "inet", "kgwan").Run() })
	return func() int {
		out, err := exec.Command("ip", "netns", "exec", wan, "nft", "list", "counter", "inet", "kgwan", "syn443").CombinedOutput()
		if err != nil {
			t.Fatalf("read the WAN-side counter: %v %s", err, out)
		}
		m := counterPacketsRe.FindStringSubmatch(string(out))
		if m == nil {
			t.Fatalf("unparseable counter: %s", out)
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
}

func TestKernel_UnauthenticatedGuest443GetsNoResetAndNeverLeavesTheWAN(t *testing.T) {
	e := newEngine(t)
	if _, err := e.Ensure(context.Background(), convergeIntent(), "install"); err != nil {
		t.Fatalf("the real kernel refused the rendered ruleset: %v", err)
	}
	if !strings.Contains(run(t, nftWrapper, "list", "chain", "inet", "stayconnect", "forward"), "tcp dport 443 drop") {
		t.Fatal("precondition: the rendered forward chain has no unauthenticated 443 drop")
	}
	wanListener(t)
	synsAtWAN := wanSYNCounter(t)

	for i := 0; i < 2; i++ {
		if got := tcpProbe(t, guestNS, wanIP, "443"); got != "timeout" {
			t.Fatalf("unauthenticated guest -> %s:443 = %s; want an unanswered SYN (timeout). "+
				"refused means a reset was sent, which suppresses Chrome's captive-portal check; "+
				"connected means the guest reached the Internet before signing in", wanIP, got)
		}
	}
	if n := synsAtWAN(); n != 0 {
		t.Fatalf("%d SYN(s) to :443 from an unauthenticated guest arrived on the WAN side", n)
	}

	run(t, nftWrapper, "add", "element", "inet", "stayconnect", "auth_ipv4", "{ \""+guestIface+"\" . "+guestIP+" }")
	if got := tcpProbe(t, guestNS, wanIP, "443"); got != "connected" {
		t.Fatalf("authenticated guest -> %s:443 = %s; a signed-in guest must reach HTTPS", wanIP, got)
	}
	if n := synsAtWAN(); n == 0 {
		t.Fatal("the authenticated SYN connected but the WAN counter saw nothing; the instrument is broken")
	}

	run(t, nftWrapper, "delete", "element", "inet", "stayconnect", "auth_ipv4", "{ \""+guestIface+"\" . "+guestIP+" }")
	before := synsAtWAN()
	if got := tcpProbe(t, guestNS, wanIP, "443"); got != "timeout" {
		t.Fatalf("after sign-out, guest -> %s:443 = %s; want timeout", wanIP, got)
	}
	if n := synsAtWAN(); n != before {
		t.Fatalf("after sign-out, %d more SYN(s) to :443 reached the WAN", n-before)
	}
}

// An upgrade from verification-access software must converge away verify_ipv4, its quota/rate maps and mark
// rules, take that device offline and carry signed-in guests' authorization across.
func TestKernel_UpgradeFromAVerificationAccessTableRemovesItCleanly(t *testing.T) {
	e := newEngine(t)
	c := context.Background()
	wan := convergeTopo().WANInterface
	const otherGuest = "10.77.0.9"
	old := `table inet stayconnect
delete table inet stayconnect
table inet stayconnect {
	quota vq_0123456789ab { over 50000000 bytes }
	limit vu_0123456789ab { rate over 1250000 bytes/second burst 625000 bytes }
	limit vd_0123456789ab { rate over 1250000 bytes/second burst 625000 bytes }
	set auth_ipv4 {
		type ifname . ipv4_addr
		flags timeout
		comment "Authenticated guests: (ingress bridge, IP)"
	}
	set walled_garden_ip {
		type ipv4_addr
		flags interval
	}
	set verify_ipv4 {
		type ifname . ipv4_addr
		flags timeout
		comment "temporary verification access: (ingress bridge, IP); scd-owned"
	}
	map verify_quota {
		type ipv4_addr : quota
	}
	map verify_up {
		type ipv4_addr : limit
	}
	map verify_down {
		type ipv4_addr : limit
	}
	chain forward {
		type filter hook forward priority filter; policy drop;
		oifname "` + guestIface + `" ip daddr @verify_quota quota name ip daddr map @verify_quota drop
		oifname "` + guestIface + `" ip daddr @verify_down limit name ip daddr map @verify_down drop
		iifname "` + guestIface + `" ip saddr @verify_quota quota name ip saddr map @verify_quota drop
		iifname "` + guestIface + `" ip saddr @verify_up limit name ip saddr map @verify_up drop
		ct mark 0x56455249 iifname "` + guestIface + `" ip saddr != @verify_quota drop
		ct mark 0x56455249 oifname "` + guestIface + `" ip daddr != @verify_quota drop
		ct state established,related accept
		ct state invalid drop
		oifname "` + wan + `" iifname . ip saddr @auth_ipv4 accept comment "authenticated guests"
		oifname "` + wan + `" iifname . ip saddr @verify_ipv4 ct mark set 0x56455249 accept comment "temporary verification access"
	}
}
add element inet stayconnect verify_quota { ` + guestIP + ` : "vq_0123456789ab" }
add element inet stayconnect verify_up { ` + guestIP + ` : "vu_0123456789ab" }
add element inet stayconnect verify_down { ` + guestIP + ` : "vd_0123456789ab" }
add element inet stayconnect verify_ipv4 { "` + guestIface + `" . ` + guestIP + ` timeout 600s }
add element inet stayconnect auth_ipv4 { "` + guestIface + `" . ` + otherGuest + ` timeout 600s }
`
	p := filepath.Join(t.TempDir(), "verify-era.nft")
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(nftWrapper, "-f", p).CombinedOutput(); err != nil {
		t.Fatalf("loading the verification-era ruleset: %v — %s", err, strings.TrimSpace(string(out)))
	}
	// Probed by TCP to an unlistened port in the WAN namespace: forwarded is "refused" (reset), dropped is "timeout".
	// Each probe is a new flow so conntrack cannot carry results between probes.
	if got := tcpProbe(t, guestNS, wanIP, "22"); got != "refused" {
		t.Fatalf("precondition: the device holding a verification window does not reach the WAN through it (%s)", got)
	}

	res, err := e.Ensure(c, convergeIntent(), "boot_reconcile")
	if err != nil {
		t.Fatalf("converge from the verification-era table: %v", err)
	}
	if !res.Changed || res.Carried != 1 {
		t.Fatalf("changed=%v carried=%d; want a converge carrying exactly the one signed-in guest", res.Changed, res.Carried)
	}
	table := run(t, nftWrapper, "list", "table", "inet", "stayconnect")
	for _, gone := range []string{"verify", "vq_", "vu_", "vd_", "quota", "0x56455249", "ct mark"} {
		if strings.Contains(table, gone) {
			t.Fatalf("the converged table still carries %q:\n%s", gone, table)
		}
	}
	if auth := run(t, nftWrapper, "list", "set", "inet", "stayconnect", "auth_ipv4"); !strings.Contains(auth, otherGuest) {
		t.Fatalf("the signed-in guest was not carried across: %s", auth)
	}
	if got := tcpProbe(t, guestNS, wanIP, "22"); got != "timeout" {
		t.Fatalf("the device that held only a verification window still reaches the WAN after the converge (%s)", got)
	}
}
