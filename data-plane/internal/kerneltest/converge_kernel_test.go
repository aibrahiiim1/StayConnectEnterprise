//go:build kernelgate

package kerneltest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/nftconverge"
)

// Drives the engine netd runs against real nft in the disposable router namespace; each test restores the
// harness ruleset.

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

// convergeIntent describes the namespace as a guest network. MgmtAddr is empty: a drop rule for one would block
// traffic under measurement.
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

// newEngine also restores the harness ruleset afterwards so converge tests do not affect each other.
func newEngine(t *testing.T) *nftconverge.Engine {
	t.Cleanup(func() {
		if err := exec.Command(nftWrapper, "-f", harnessRuleset).Run(); err != nil {
			t.Logf("could not restore the harness ruleset: %v", err)
		}
	})
	return &nftconverge.Engine{Topo: convergeTopo(), Dir: t.TempDir(), NftPath: nftWrapper, R: kernelRunner{t}}
}

// prePhase3Ruleset is the July shape: authorization set and accept rule, no phase3_auth_ipv4, no fingerprint marker.
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

// The kernel must keep and return the fingerprint set comment; otherwise skip-when-equal never matches.
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

// Once converged, reconciling again issues no nft command.
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

// A July-shaped table with a live permanent legacy authorization converges to an empty Phase-3 set without the
// guest losing access.
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

// Reboot reconstruction: deleting the table is what a reboot leaves before netd starts.
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

	// Converge via a SECOND network: it moves the fingerprint but leaves the authorized guest's rules untouched.
	// (ClientIsolation is not in the render, so toggling it moves nothing.)
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

// An unreadable live state must abort before any mutation. nft is wrapped so one read fails; treating that as an
// empty set would delete the table and cost the guest access.
func TestKernel_AnUnreadableSetAbortsWithoutTouchingTheRuleset(t *testing.T) {
	e := newEngine(t)
	nets := convergeIntent()
	c := context.Background()

	if _, err := e.Ensure(c, nets, "install"); err != nil {
		t.Fatalf("install: %v", err)
	}
	run(t, nftWrapper, "add", "element", "inet", "stayconnect", "auth_ipv4",
		`{ "`+guestIface+`" . `+guestIP+` timeout 600s }`)
	if !reaches(t) {
		t.Fatal("precondition: the authorized guest cannot reach the WAN")
	}
	before := run(t, nftWrapper, "list", "table", "inet", "stayconnect")

	broken := filepath.Join(t.TempDir(), "nft-broken")
	script := "#!/usr/bin/env bash\n" +
		"for a in \"$@\"; do if [ \"$a\" = \"set\" ]; then echo 'nft: netlink: Device or resource busy' >&2; exit 1; fi; done\n" +
		"exec " + nftWrapper + " \"$@\"\n"
	if err := os.WriteFile(broken, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e.NftPath = broken

	changed := convergeIntent()
	second := changed[0]
	second.Name, second.BridgeName = "kg3", "kg-br3"
	second.GatewayIP, second.SubnetCIDR = "10.81.0.1", "10.81.0.0/24"
	changed = append(changed, second)

	if _, err := e.Ensure(c, changed, "upgrade"); err == nil {
		t.Fatal("converged against a real ruleset it could not read")
	}

	e.NftPath = nftWrapper
	after := run(t, nftWrapper, "list", "table", "inet", "stayconnect")
	if stableRuleset(before) != stableRuleset(after) {
		t.Fatalf("the live ruleset changed after refusing to converge:\n--- before ---\n%s\n--- after ---\n%s",
			stableRuleset(before), stableRuleset(after))
	}
	if !reaches(t) {
		t.Fatal("the authorized guest lost access even though the converge was refused")
	}
}

// stableRuleset strips fields that change on their own (expires, counters) so listings can be compared for
// identical structure and authorization.
func stableRuleset(s string) string {
	s = expiresRe.ReplaceAllString(s, "expires <ticking>")
	s = counterRe.ReplaceAllString(s, "counter packets N bytes N")
	return s
}

var (
	expiresRe = regexp.MustCompile(`expires [0-9hmsd]+`)
	counterRe = regexp.MustCompile(`counter packets \d+ bytes \d+`)
)
