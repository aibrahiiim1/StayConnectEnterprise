//go:build kernelgate

package kerneltest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/nftconverge"
)

// THE RESTART/REBOOT BLOCKER, AGAINST A REAL KERNEL.
//
// The modelled suite in cmd/netd proves the decision logic: when to converge, when to do nothing, and what to
// carry across. It cannot prove the things only nftables decides — that a table carrying a fingerprint COMMENT
// can be read back and compared at all, that the real generated ruleset LOADS on a real kernel, and that a
// `delete table` + recreate + re-add submitted as one file really does leave a previously authorized guest
// still able to reach the internet.
//
// This drives the SAME engine netd runs, against real nft in the disposable router namespace, and checks the
// result with real packets.
//
// Each test restores the harness ruleset afterwards, so the rest of the kernel suite sees the topology it set
// up. Nothing here escapes the namespaces the harness created.

var harnessRuleset = mustEnv("KG_RULESET")

type kernelRunner struct{ t *testing.T }

func (r kernelRunner) Run(_ context.Context, name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		r.t.Logf("kernel converge: %s %s -> %v — %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return err
}

func (r kernelRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

type countingRunner struct {
	inner     nftconverge.Runner
	mutations int
}

func (c *countingRunner) Run(ctx context.Context, name string, args ...string) error {
	c.mutations++
	return c.inner.Run(ctx, name, args...)
}

func (c *countingRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return c.inner.Output(ctx, name, args...)
}

// convergeIntent describes the disposable namespace as a guest network, so the renderer produces a ruleset that
// actually governs THIS topology: guests arrive on the router-side guest interface and egress via the WAN veth.
// MgmtAddr is deliberately empty — there is no management address in the namespace, and rendering a drop rule
// for one would block traffic this suite is about to measure.
func convergeIntent() []netcfg.GuestNetwork {
	return []netcfg.GuestNetwork{{
		Name: "kg", Enabled: true, NetworkType: "untagged", BridgeName: guestIface,
		GatewayIP: "10.77.0.1", SubnetCIDR: "10.77.0.0/24", PrefixLen: 24,
		DHCPMode: "managed", CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
	}}
}

func convergeTopo() netcfg.Topology {
	wan := os.Getenv("KG_WAN_IF")
	if wan == "" {
		wan = "wan0"
	}
	return netcfg.Topology{WANInterface: wan, MgmtInterface: wan}
}

// newEngine also arranges for the harness ruleset to be restored, so one converge test cannot change what the
// next test in the suite is looking at.
func newEngine(t *testing.T) *nftconverge.Engine {
	t.Cleanup(func() {
		if err := exec.Command(nftWrapper, "-f", harnessRuleset).Run(); err != nil {
			t.Logf("could not restore the harness ruleset: %v", err)
		}
	})
	return &nftconverge.Engine{Topo: convergeTopo(), Dir: t.TempDir(), NftPath: nftWrapper, R: kernelRunner{t}}
}

// prePhase3Ruleset is the July shape: the authorization set and the accept rule, with NO phase3_auth_ipv4 and
// no fingerprint marker. It is what the pilot appliance's stored bundle reinstalled on every netd start.
func prePhase3Ruleset(t *testing.T) string {
	t.Helper()
	wan := convergeTopo().WANInterface
	body := `table inet stayconnect
delete table inet stayconnect
table inet stayconnect {
	set auth_ipv4 {
		type ifname . ipv4_addr
		flags timeout
		comment "Authenticated guests: (ingress bridge, IP)"
	}
	set walled_garden_ip {
		type ipv4_addr
		flags interval
	}
	chain forward {
		type filter hook forward priority filter; policy drop;
		ct state established,related accept
		ct state invalid drop
		oifname "` + wan + `" iifname . ip saddr @auth_ipv4 accept comment "authenticated guests"
	}
}
`
	p := filepath.Join(t.TempDir(), "pre-phase3.nft")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A REAL kernel must be able to hold the marker and give it back. If nft rejected a set comment, or normalised
// it away, skip-when-equal would silently never match and every restart would rewrite the ruleset.
func TestKernel_ConvergeWritesAndReadsBackItsFingerprint(t *testing.T) {
	e := newEngine(t)
	nets := convergeIntent()
	c := context.Background()

	res, err := e.Ensure(c, nets, "install")
	if err != nil {
		t.Fatalf("converge: %v", err)
	}
	if !res.Changed {
		t.Fatal("the first converge in this namespace changed nothing")
	}
	live, ok := e.LiveFingerprint(c)
	if !ok {
		t.Fatal("no stayconnect table after converge")
	}
	if want := netcfg.RenderFingerprint(nets, e.Topo); live != want {
		t.Fatalf("the kernel gave back fingerprint %q, the renderer says %q", live, want)
	}
}

// THE PRODUCTION INVARIANT, ON A REAL KERNEL: once converged, reconciling again issues no nft command at all.
func TestKernel_SteadyStateConvergeIssuesNothing(t *testing.T) {
	e := newEngine(t)
	nets := convergeIntent()
	c := context.Background()
	if _, err := e.Ensure(c, nets, "install"); err != nil {
		t.Fatalf("install: %v", err)
	}

	counting := &countingRunner{inner: kernelRunner{t}}
	e.R = counting
	res, err := e.Ensure(c, nets, "restart")
	if err != nil {
		t.Fatalf("restart converge: %v", err)
	}
	if res.Changed {
		t.Fatal("a steady-state converge rewrote a real ruleset")
	}
	if counting.mutations != 0 {
		t.Fatalf("a steady-state converge issued %d real nft mutation(s)", counting.mutations)
	}
}

// THE INCREMENT-9 SCENARIO END TO END, with packets: a July-shaped table carrying a live PERMANENT legacy
// authorization, converged by the current renderer. The Phase-3 set must appear empty, and the guest must never
// lose access.
func TestKernel_UpgradeFromPrePhase3TableKeepsTheGuestOnline(t *testing.T) {
	e := newEngine(t)
	nets := convergeIntent()
	c := context.Background()

	if out, err := exec.Command(nftWrapper, "-f", prePhase3Ruleset(t)).CombinedOutput(); err != nil {
		t.Fatalf("loading the pre-Phase-3 ruleset: %v — %s", err, strings.TrimSpace(string(out)))
	}
	run(t, nftWrapper, "add", "element", "inet", "stayconnect", "auth_ipv4",
		`{ "`+guestIface+`" . `+guestIP+` }`)
	if !reaches(t) {
		t.Fatal("precondition: the legacy-authorized guest cannot reach the WAN")
	}
	if fp, _ := e.LiveFingerprint(c); fp != "" {
		t.Fatalf("the pre-Phase-3 ruleset already carries a fingerprint %q", fp)
	}

	res, err := e.Ensure(c, nets, "boot_reconcile")
	if err != nil {
		t.Fatalf("converge: %v", err)
	}
	if !res.Changed {
		t.Fatal("the pre-Phase-3 table was not converged — this is the Increment-9 blocker")
	}
	if res.Carried != 1 {
		t.Fatalf("carried %d elements; the live legacy authorization was not preserved", res.Carried)
	}
	out := run(t, nftWrapper, "list", "set", "inet", "stayconnect", "phase3_auth_ipv4")
	if !strings.Contains(out, "phase3_auth_ipv4") {
		t.Fatal("phase3_auth_ipv4 is absent after converging a pre-Phase-3 table")
	}
	if strings.Contains(out, "elements = {") {
		t.Fatalf("phase3_auth_ipv4 is not empty while Phase 3 is dark: %s", out)
	}
	if !reaches(t) {
		t.Fatal("the upgrade deauthorized a live legacy guest")
	}
}

// REBOOT RECONSTRUCTION, REPEATEDLY. Deleting the table is what a reboot leaves behind before netd starts.
func TestKernel_RebootReconstructionIsIdempotent(t *testing.T) {
	e := newEngine(t)
	nets := convergeIntent()
	c := context.Background()

	for cycle := 0; cycle < 2; cycle++ {
		_ = exec.Command(nftWrapper, "delete", "table", "inet", "stayconnect").Run()
		res, err := e.Ensure(c, nets, "boot_reconcile")
		if err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		if !res.Changed {
			t.Fatalf("cycle %d: a missing table was not reconstructed", cycle)
		}
		if out := run(t, nftWrapper, "list", "set", "inet", "stayconnect", "phase3_auth_ipv4"); !strings.Contains(out, "phase3_auth_ipv4") {
			t.Fatalf("cycle %d: phase3_auth_ipv4 missing after reconstruction", cycle)
		}
		counting := &countingRunner{inner: kernelRunner{t}}
		e.R = counting
		r2, err := e.Ensure(c, nets, "settle")
		if err != nil || r2.Changed || counting.mutations != 0 {
			t.Fatalf("cycle %d: did not settle (changed=%v mutations=%d err=%v)", cycle, r2.Changed, counting.mutations, err)
		}
		e.R = kernelRunner{t}
	}
}

// A TIMED authorization must be carried with the time it has LEFT, and must still work afterwards.
func TestKernel_CarryOverPreservesATimedAuthorization(t *testing.T) {
	e := newEngine(t)
	nets := convergeIntent()
	c := context.Background()
	if _, err := e.Ensure(c, nets, "install"); err != nil {
		t.Fatalf("install: %v", err)
	}
	run(t, nftWrapper, "add", "element", "inet", "stayconnect", "auth_ipv4",
		`{ "`+guestIface+`" . `+guestIP+` timeout 600s }`)
	if !reaches(t) {
		t.Fatal("precondition: the timed-authorized guest cannot reach the WAN")
	}

	// Force a converge by changing the rendered structure. A SECOND network is used rather than editing this
	// one: it moves the fingerprint (new bridge, subnet, captive DNAT and masquerade rules) while leaving every
	// rule that governs the authorized guest exactly as it was, so the packet assertion below is measuring
	// carry-over and not some unrelated rule change.
	//
	// An earlier version toggled ClientIsolation, which does not appear in the render at all — inter-guest
	// isolation is emitted unconditionally from guest_subnets — so the fingerprint never moved, no converge
	// happened, and the test failed for the right reason.
	changed := convergeIntent()
	second := changed[0]
	second.Name, second.BridgeName = "kg2", "kg-br2"
	second.GatewayIP, second.SubnetCIDR = "10.79.0.1", "10.79.0.0/24"
	changed = append(changed, second)
	res, err := e.Ensure(c, changed, "upgrade")
	if err != nil {
		t.Fatalf("converge: %v", err)
	}
	if !res.Changed || res.Carried != 1 {
		t.Fatalf("expected a converge carrying 1 element, got changed=%v carried=%d", res.Changed, res.Carried)
	}
	if !reaches(t) {
		t.Fatal("a timed authorization did not survive the structural replace")
	}
	if out := run(t, nftWrapper, "list", "set", "inet", "stayconnect", "auth_ipv4"); !strings.Contains(out, "expires") {
		t.Fatalf("the carried element lost its lease: %s", out)
	}
}
