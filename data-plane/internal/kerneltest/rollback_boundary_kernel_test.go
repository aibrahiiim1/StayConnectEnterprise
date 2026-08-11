//go:build kernelgate

package kerneltest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// THE PRE-nftconverge ROLLBACK BOUNDARY, AGAINST A REAL KERNEL AND REAL PACKETS.
//
// Live Increment 9's rollback rehearsal started a netd that predates ADR-0003. That binary does not reconcile;
// it re-asserts the stored bundle, and the bundle begins with `delete table inet stayconnect` — so starting it
// recreates the authorization sets EMPTY. On the pilot appliance that was harmless, because nobody was
// authorized. On a property with guests online it is a simultaneous, total deauthorization performed by a
// command whose whole purpose is to make things safer.
//
// The modelled suite in scripts/ci proves the decision with stubs. This proves it where it matters: a REAL
// populated authorization set in a real kernel, a REAL packet that is passing because of it, the REAL operator
// script, and the assertion that after the refusal the ruleset is untouched and the packet still passes.
//
// These tests never restart anything real. install/restart/exe/state are stubbed, so the only live thing the
// script touches is the namespace's nft — which is exactly the input the decision is made from.

func rollbackTool(t *testing.T) string {
	t.Helper()
	p := os.Getenv("KG_ROLLBACK_TOOL")
	if p == "" {
		t.Skip("KG_ROLLBACK_TOOL not set by the harness")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("rollback tool not found at %s: %v", p, err)
	}
	return p
}

// rollbackWorld builds a disposable bin dir whose "previous release" netd either carries the render marker or
// does not, plus stubs for everything except nft.
type rollbackWorld struct {
	dir      string
	binDir   string
	env      []string
	restarts string // file the restart stub appends to; empty means it never ran
}

func newRollbackWorld(t *testing.T, targetConverges bool) *rollbackWorld {
	t.Helper()
	d := t.TempDir()
	binDir := filepath.Join(d, "bin")
	runDir := filepath.Join(d, "run")
	for _, p := range []string{binDir, runDir} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The rollback TARGET. "netd-render-fp=" is the string a convergence-capable netd carries; its absence is
	// what marks a pre-ADR-0003 binary. This asks the artifact itself rather than trusting a version string.
	target := "OLD-NETD-without-the-marker"
	if targetConverges {
		target = "NEW-NETD carrying netd-render-fp= in its binary"
	}
	write := func(p, s string, mode os.FileMode) {
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(binDir, "netd.bak"), target, 0o755)
	write(filepath.Join(binDir, "netd"), "CURRENT-NETD carrying netd-render-fp= in its binary", 0o755)
	write(filepath.Join(runDir, "netd.exe"), "CURRENT-NETD carrying netd-render-fp= in its binary", 0o755)

	restarts := filepath.Join(d, "restarts.log")
	write(filepath.Join(d, "restart.sh"), "#!/usr/bin/env bash\necho \"$1\" >> "+restarts+"\ncp "+
		filepath.Join(binDir, "netd")+" "+filepath.Join(runDir, "netd.exe")+"\n", 0o755)
	write(filepath.Join(d, "runexe.sh"), "#!/usr/bin/env bash\necho "+filepath.Join(runDir, "netd.exe")+"\n", 0o755)
	write(filepath.Join(d, "state.sh"), "#!/usr/bin/env bash\necho active\n", 0o755)

	return &rollbackWorld{
		dir: d, binDir: binDir, restarts: restarts,
		env: append(os.Environ(),
			"SC_ROLLBACK_NFT="+nftWrapper, // the REAL namespace nft — the live state it reads is real
			"SC_ROLLBACK_RESTART_CMD="+filepath.Join(d, "restart.sh"),
			"SC_ROLLBACK_RUNNING_EXE="+filepath.Join(d, "runexe.sh"),
			"SC_ROLLBACK_UNIT_STATE="+filepath.Join(d, "state.sh"),
		),
	}
}

func (w *rollbackWorld) run(t *testing.T) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", rollbackTool(t),
		"--bin-dir", w.binDir, "--source-suffix", ".bak", "--unit", "netd=stayconnect-netd")
	cmd.Env = w.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (w *rollbackWorld) restarted() bool {
	_, err := os.Stat(w.restarts)
	return err == nil
}

// authorize the guest for real and confirm packets actually flow because of it.
func authorizeGuest(t *testing.T) {
	t.Helper()
	run(t, nftWrapper, "add", "element", "inet", "stayconnect", "auth_ipv4",
		`{ "`+guestIface+`" . `+guestIP+` }`)
	if !reaches(t) {
		t.Fatal("precondition: the authorized guest cannot reach the WAN")
	}
}

func deauthorizeGuest(t *testing.T) {
	t.Helper()
	_ = exec.Command(nftWrapper, "delete", "element", "inet", "stayconnect", "auth_ipv4",
		`{ "`+guestIface+`" . `+guestIP+` }`).Run()
}

// THE CENTRAL CASE: a real guest is online, the rollback target predates convergence, and the command must
// refuse BEFORE anything is replaced or restarted — with the guest still online afterwards.
func TestKernel_RollbackToPreConvergeTargetIsRefusedWhileAGuestIsAuthorized(t *testing.T) {
	t.Cleanup(func() { deauthorizeGuest(t) })
	authorizeGuest(t)
	before := run(t, nftWrapper, "list", "table", "inet", "stayconnect")

	w := newRollbackWorld(t, false)
	out, err := w.run(t)

	if err == nil {
		t.Fatalf("the rollback proceeded into a property-wide deauthorization:\n%s", out)
	}
	if !strings.Contains(out, "NOTHING HAS BEEN CHANGED") {
		t.Fatalf("the refusal does not state that nothing changed:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "operator action") {
		t.Fatalf("the refusal does not name the operator action:\n%s", out)
	}
	if w.restarted() {
		t.Fatal("a service was restarted despite the refusal")
	}
	if strings.Contains(out, "== replace ==") {
		t.Fatalf("it reached the replace phase:\n%s", out)
	}

	// the live ruleset is untouched...
	after := run(t, nftWrapper, "list", "table", "inet", "stayconnect")
	if stableRuleset(before) != stableRuleset(after) {
		t.Fatalf("the live ruleset changed after the refusal:\n--- before ---\n%s\n--- after ---\n%s",
			stableRuleset(before), stableRuleset(after))
	}
	// ...and the guest is still online, proven with packets.
	if !reaches(t) {
		t.Fatal("the authorized guest lost access even though the rollback was refused")
	}
}

// THE SUPPORTED CASE: with the authorization set EMPTY there is nothing to lose, so the same rollback proceeds
// and is verified exactly as before. This is the rehearsal Increment 9 actually ran.
func TestKernel_RollbackToPreConvergeTargetIsAllowedWhenAuthorizationIsEmpty(t *testing.T) {
	deauthorizeGuest(t)
	if reaches(t) {
		t.Fatal("precondition: the guest still reaches the WAN with an empty authorization set")
	}

	w := newRollbackWorld(t, false)
	out, err := w.run(t)

	if err != nil {
		t.Fatalf("an empty-set rollback was blocked:\n%s", out)
	}
	if !strings.Contains(out, "EMPTY (0 elements)") {
		t.Fatalf("the decision did not state why it was allowed:\n%s", out)
	}
	if !strings.Contains(out, "BINARY_ROLLBACK = PASS") {
		t.Fatalf("the rollback did not verify:\n%s", out)
	}
	if !w.restarted() {
		t.Fatal("the rollback claimed success without restarting the service")
	}
}

// A CONVERGENCE-CAPABLE TARGET is unaffected by the boundary: it preserves authorization across the transition
// by design, so it stays supported even with a guest online.
func TestKernel_RollbackToConvergenceCapableTargetStaysSupportedWithGuestsOnline(t *testing.T) {
	t.Cleanup(func() { deauthorizeGuest(t) })
	authorizeGuest(t)

	w := newRollbackWorld(t, true)
	out, err := w.run(t)

	if err != nil {
		t.Fatalf("a safe rollback target was blocked while a guest was online:\n%s", out)
	}
	if !strings.Contains(out, "no compatibility boundary applies") {
		t.Fatalf("the decision did not explain why the boundary did not apply:\n%s", out)
	}
	if !reaches(t) {
		t.Fatal("the guest lost access during a rollback that was supposed to be safe")
	}
}

// "CANNOT PROVE EMPTY" IS NOT "IS EMPTY". If the live set cannot be read at all, the command must refuse rather
// than assume the safe case — the assumption is what turns an unreadable kernel into an outage.
func TestKernel_RollbackRefusesWhenTheLiveSetCannotBeRead(t *testing.T) {
	t.Cleanup(func() { deauthorizeGuest(t) })
	authorizeGuest(t)

	w := newRollbackWorld(t, false)
	broken := filepath.Join(w.dir, "nft-broken")
	if err := os.WriteFile(broken, []byte("#!/usr/bin/env bash\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i, e := range w.env {
		if strings.HasPrefix(e, "SC_ROLLBACK_NFT=") {
			w.env[i] = "SC_ROLLBACK_NFT=" + broken
		}
	}

	out, err := w.run(t)
	if err == nil {
		t.Fatalf("it proceeded without being able to read the live authorization set:\n%s", out)
	}
	if w.restarted() {
		t.Fatal("a service was restarted despite an unreadable live set")
	}
	if !reaches(t) {
		t.Fatal("the authorized guest lost access after a refusal")
	}
}
