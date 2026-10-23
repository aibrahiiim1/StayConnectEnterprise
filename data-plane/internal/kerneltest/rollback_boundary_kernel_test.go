//go:build kernelgate

package kerneltest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Rollback boundary against a real kernel: a pre-nftconverge netd re-asserts the stored bundle, whose `delete table`
// empties the authorization sets and deauthorizes every online guest, so the rollback command must refuse and leave
// the ruleset untouched. install/restart/exe/state are stubbed; only the namespace's nft is real.

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

// rollbackWorld builds a disposable bin dir whose previous-release netd carries the render marker or not, with stubs
// for everything except nft.
type rollbackWorld struct {
	dir      string
	binDir   string
	env      []string
	restarts string
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
	// "netd-render-fp=" marks a convergence-capable netd; its absence marks a pre-ADR-0003 binary. Every fixture also
	// carries "stayconnect_production" because binary-rollback.sh refuses targets built without that tag.
	const prodTag = " stayconnect_production"
	target := "OLD-NETD-without-the-render-marker" + prodTag
	if targetConverges {
		target = "NEW-NETD carrying netd-render-fp= in its binary" + prodTag
	}
	write := func(p, s string, mode os.FileMode) {
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(binDir, "netd.bak"), target, 0o755)
	write(filepath.Join(binDir, "netd"), "CURRENT-NETD carrying netd-render-fp= in its binary"+prodTag, 0o755)
	write(filepath.Join(runDir, "netd.exe"), "CURRENT-NETD carrying netd-render-fp= in its binary"+prodTag, 0o755)

	restarts := filepath.Join(d, "restarts.log")
	write(filepath.Join(d, "restart.sh"), "#!/usr/bin/env bash\necho \"$1\" >> "+restarts+"\ncp "+
		filepath.Join(binDir, "netd")+" "+filepath.Join(runDir, "netd.exe")+"\n", 0o755)
	write(filepath.Join(d, "runexe.sh"), "#!/usr/bin/env bash\necho "+filepath.Join(runDir, "netd.exe")+"\n", 0o755)
	write(filepath.Join(d, "state.sh"), "#!/usr/bin/env bash\necho active\n", 0o755)

	return &rollbackWorld{
		dir: d, binDir: binDir, restarts: restarts,
		env: append(os.Environ(),
			"SC_ROLLBACK_NFT="+nftWrapper,
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

// A guest is online and the rollback target predates convergence: the command must refuse before replacing or
// restarting anything.
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

	after := run(t, nftWrapper, "list", "table", "inet", "stayconnect")
	if stableRuleset(before) != stableRuleset(after) {
		t.Fatalf("the live ruleset changed after the refusal:\n--- before ---\n%s\n--- after ---\n%s",
			stableRuleset(before), stableRuleset(after))
	}
	if !reaches(t) {
		t.Fatal("the authorized guest lost access even though the rollback was refused")
	}
}

// With the authorization set empty there is nothing to lose, so the rollback proceeds.
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

// A convergence-capable target preserves authorization by design, so it stays supported with a guest online.
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

// An unreadable live set is not an empty set: the command must refuse rather than assume.
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
