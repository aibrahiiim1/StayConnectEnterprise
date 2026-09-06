package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// fakeSystemd records systemctl calls and answers is-enabled/is-active from a state it also updates, so a
// test can assert the appliance ended up in the state it started in — not merely that some commands ran.
type fakeSystemd struct {
	mu      sync.Mutex
	enabled bool
	active  bool
	calls   []string
}

func (f *fakeSystemd) run(ctx context.Context, name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name != "systemctl" || len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "enable":
		f.enabled = true
		for _, a := range args {
			if a == "--now" {
				f.active = true
			}
		}
	case "disable":
		f.enabled = false
	case "start":
		f.active = true
	case "stop":
		f.active = false
	}
	return nil
}

func (f *fakeSystemd) output(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name == "systemctl" && len(args) > 0 {
		switch args[0] {
		case "is-enabled":
			if f.enabled {
				return []byte("enabled\n"), nil
			}
			return []byte("disabled\n"), nil
		case "is-active":
			if f.active {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), nil
		}
	}
	return nil, nil
}

func (f *fakeSystemd) state() (enabled, active bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled, f.active
}

// keaTestApplier builds an applier whose Kea paths live in a temp dir. keaSocket points at a path that does
// not exist, so kea.Healthy() is false — a factory-clean appliance where Kea has never run.
func keaTestApplier(t *testing.T, sd *fakeSystemd) *applier {
	t.Helper()
	dir := t.TempDir()
	return &applier{
		topo:            testTopo(),
		generatedDir:    dir,
		netplanFile:     filepath.Join(dir, "50-stayconnect-guest.yaml"),
		unboundFrag:     filepath.Join(dir, "stayconnect-guest.conf"),
		keaLeaseCSV:     filepath.Join(dir, "leases.csv"),
		keaSocket:       filepath.Join(dir, "no-such-kea-socket"),
		keaConfFile:     filepath.Join(dir, "kea-dhcp4.conf"),
		keaUnit:         "kea-dhcp4-server.service",
		keaStartTimeout: 300 * time.Millisecond,
		kea:             newKeaClient(filepath.Join(dir, "no-such-kea-socket")),
		runFn:           sd.run,
		outFn:           sd.output,
	}
}

func keaIntent() []netcfg.GuestNetwork {
	return []netcfg.GuestNetwork{{
		ID: "g1", Name: "Guest", Enabled: true, NetworkType: "untagged",
		ParentInterface: "ens192", BridgeName: "br-g-f7d7d90c",
		GatewayIP: "10.20.0.1", SubnetCIDR: "10.20.0.0/22", PrefixLen: 22,
		DHCPMode: netcfg.DHCPLocal, DNSMode: "appliance", DomainName: "guest.local",
		LeaseDefault: 3600, LeaseMin: 900, LeaseMax: 7200,
		CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
		Pools: []netcfg.Pool{{StartIP: "10.20.0.100", EndIP: "10.20.3.250"}},
	}}
}

// THE DEFECT THIS CLOSES.
//
// The first guest-network apply starts Kea, because it cannot be configured any other way. If that apply
// then fails — or the operator never confirms and the watchdog rolls it back — there is no previous
// confirmed revision to fall back to, so rollback tore down the bridge, the netplan and the unbound
// fragment and left Kea running, enabled, and serving DHCP for a guest network that no longer exists.
//
// A factory-clean appliance has to come back factory-clean. Anything else means the next attempt starts
// from a state nobody chose, on a box that is quietly handing out addresses.
func TestFirstApplyRollbackRestoresFactoryCleanKea(t *testing.T) {
	sd := &fakeSystemd{enabled: false, active: false} // provisioning leaves Kea stopped and disabled
	a := keaTestApplier(t, sd)

	// Bootstrap, as the first apply does.
	if err := a.bootstrapKeaIfStopped(context.Background(), "rev-1", keaIntent()); err == nil {
		t.Fatal("expected the socket wait to fail (no real Kea in a unit test)")
	}
	// It got far enough to start the unit and write a config — which is the state rollback must undo.
	if en, act := sd.state(); !en || !act {
		t.Fatalf("bootstrap should have enabled and started Kea; enabled=%v active=%v", en, act)
	}
	if _, err := os.Stat(a.keaConfFile); err != nil {
		t.Fatalf("bootstrap should have written a Kea config: %v", err)
	}
	if _, err := os.Stat(a.keaPreBootstrapPath()); err != nil {
		t.Fatalf("bootstrap must record the pre-apply state before changing anything: %v", err)
	}

	// The apply fails, or confirmation times out. Either way: rollback with no previous revision.
	a.restoreKeaPreBootstrap(context.Background())

	en, act := sd.state()
	if en || act {
		t.Fatalf("Kea must return to stopped and disabled; enabled=%v active=%v (calls: %v)", en, act, sd.calls)
	}
	if _, err := os.Stat(a.keaConfFile); !os.IsNotExist(err) {
		t.Fatal("no guest-network DHCP configuration may be left behind on a factory-clean appliance")
	}
	if _, err := os.Stat(a.keaPreBootstrapPath()); !os.IsNotExist(err) {
		t.Fatal("the pre-bootstrap marker should be consumed by the restore")
	}
}

// An appliance that already had Kea running and configured — a second guest network, or a rebuild — must
// get THAT state back, not a factory-clean one.
func TestRollbackRestoresPreviousKeaConfigAndRunState(t *testing.T) {
	sd := &fakeSystemd{enabled: true, active: true}
	a := keaTestApplier(t, sd)

	const existing = `{"Dhcp4":{"note":"the configuration that was here first"}}`
	if err := os.WriteFile(a.keaConfFile, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := a.bootstrapKeaIfStopped(context.Background(), "rev-2", keaIntent()); err == nil {
		t.Fatal("expected the socket wait to fail")
	}
	if got, _ := os.ReadFile(a.keaConfFile); string(got) == existing {
		t.Fatal("bootstrap should have replaced the config (otherwise this test proves nothing)")
	}

	a.restoreKeaPreBootstrap(context.Background())

	if en, act := sd.state(); !en || !act {
		t.Fatalf("Kea was enabled and running before; it must stay that way. enabled=%v active=%v", en, act)
	}
	got, err := os.ReadFile(a.keaConfFile)
	if err != nil {
		t.Fatalf("the previous config must be restored, not removed: %v", err)
	}
	if string(got) != existing {
		t.Fatalf("the exact previous config must come back; got:\n%s", got)
	}
}

// Confirming the revision is the operator saying "keep this". The saved snapshot must be discarded, or a
// later unrelated rollback could resurrect a "factory-clean" state that stopped being true on confirm.
func TestConfirmDiscardsThePreBootstrapSnapshot(t *testing.T) {
	sd := &fakeSystemd{}
	a := keaTestApplier(t, sd)

	if err := a.bootstrapKeaIfStopped(context.Background(), "rev-3", keaIntent()); err == nil {
		t.Fatal("expected the socket wait to fail")
	}
	if _, err := os.Stat(a.keaPreBootstrapPath()); err != nil {
		t.Fatalf("marker should exist after bootstrap: %v", err)
	}

	a.clearKeaPreBootstrap()

	if _, err := os.Stat(a.keaPreBootstrapPath()); !os.IsNotExist(err) {
		t.Fatal("confirming must discard the pre-bootstrap marker")
	}
	// And a later rollback must then leave Kea alone — it is no longer ours to undo.
	sd.enabled, sd.active = true, true
	a.restoreKeaPreBootstrap(context.Background())
	if en, act := sd.state(); !en || !act {
		t.Fatalf("with no marker, rollback must not touch Kea; enabled=%v active=%v", en, act)
	}
}

// The marker is on disk because the rollback that needs it is often run by the confirmation watchdog,
// which also runs at boot — after the process that did the bootstrap is gone.
func TestPreBootstrapStateSurvivesProcessRestart(t *testing.T) {
	sd := &fakeSystemd{enabled: false, active: false}
	a := keaTestApplier(t, sd)

	if err := a.bootstrapKeaIfStopped(context.Background(), "rev-4", keaIntent()); err == nil {
		t.Fatal("expected the socket wait to fail")
	}

	// A NEW applier over the same generated directory: netd restarted, or the appliance rebooted, and the
	// watchdog is now the one rolling back.
	restarted := &applier{
		topo:         a.topo,
		generatedDir: a.generatedDir,
		keaConfFile:  a.keaConfFile,
		keaUnit:      a.keaUnit,
		keaSocket:    a.keaSocket,
		kea:          a.kea,
		runFn:        sd.run,
		outFn:        sd.output,
	}
	restarted.restoreKeaPreBootstrap(context.Background())

	if en, act := sd.state(); en || act {
		t.Fatalf("a restarted netd must still be able to restore Kea; enabled=%v active=%v", en, act)
	}
	if _, err := os.Stat(a.keaConfFile); !os.IsNotExist(err) {
		t.Fatal("the guest DHCP config must be removed by the restarted process too")
	}
}

// Nothing to undo when netd never started Kea: an apply that failed before the Kea step, or a dry run.
func TestRestoreIsNoOpWithoutBootstrap(t *testing.T) {
	sd := &fakeSystemd{enabled: true, active: true}
	a := keaTestApplier(t, sd)

	a.restoreKeaPreBootstrap(context.Background())

	if len(sd.calls) != 0 {
		t.Fatalf("without a marker nothing should be issued; got %v", sd.calls)
	}
	if en, act := sd.state(); !en || !act {
		t.Fatalf("Kea's state must be untouched; enabled=%v active=%v", en, act)
	}
}

// THE WIRING, not just the pieces.
//
// The two halves can both be correct and still leave Kea running if rollback never calls the restore. This
// drives the real rollback() down its no-previous-revision branch — the first-apply case — and asserts the
// appliance comes back factory-clean: netplan and unbound fragment gone, Kea stopped, disabled and
// unconfigured.
func TestRollbackWithNoPreviousRevisionRestoresKea(t *testing.T) {
	sd := &fakeSystemd{enabled: false, active: false}
	a := keaTestApplier(t, sd)

	// Files the first apply would have written.
	if err := os.WriteFile(a.netplanFile, []byte("netplan"), 0o600); err != nil {
		t.Fatalf("seed netplan: %v", err)
	}
	if err := os.WriteFile(a.unboundFrag, []byte("unbound"), 0o644); err != nil {
		t.Fatalf("seed unbound: %v", err)
	}
	if err := a.bootstrapKeaIfStopped(context.Background(), "rev-9", keaIntent()); err == nil {
		t.Fatal("expected the socket wait to fail")
	}

	// No previous confirmed revision — this is the first apply.
	a.prevBundleFn = func(ctx context.Context, exceptID string) (string, error) { return "", nil }
	a.markRolledFn = func(ctx context.Context, id, reason string) error { return nil }
	a.eventFn = func(ctx context.Context, revID, kind string, ok bool, detail map[string]any) {}

	a.rollback(context.Background(), "rev-9", "confirmation window expired")

	if en, act := sd.state(); en || act {
		t.Fatalf("rollback must restore Kea to stopped/disabled; enabled=%v active=%v", en, act)
	}
	if _, err := os.Stat(a.keaConfFile); !os.IsNotExist(err) {
		t.Fatal("rollback must not leave guest DHCP configuration behind")
	}
	if _, err := os.Stat(a.netplanFile); !os.IsNotExist(err) {
		t.Fatal("rollback must remove the guest netplan")
	}
	if _, err := os.Stat(a.unboundFrag); !os.IsNotExist(err) {
		t.Fatal("rollback must remove the guest unbound fragment")
	}
}
