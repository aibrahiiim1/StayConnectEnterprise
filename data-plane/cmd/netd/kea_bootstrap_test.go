package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// status-get can answer "healthy" from a Kea holding no socket; these assert the /proc/net/udp check instead.

func TestKeaListeningExactBind(t *testing.T) {
	// /proc/net/udp is little-endian: 192.168.77.1 -> 014DA8C0, port 67 -> 0043.
	proc := "  sl  local_address rem_address   st tx_queue rx_queue\n" +
		"  123: 014DA8C0:0043 00000000:0000 07 00000000:00000000 00:00000000 00000000   116\n"
	if !keaListeningIn(proc, "192.168.77.1") {
		t.Fatal("an exact bind on the gateway must count as listening")
	}
	if keaListeningIn(proc, "192.168.78.1") {
		t.Fatal("a bind on a DIFFERENT gateway must not count as listening for this one")
	}
}

func TestKeaListeningWildcardBind(t *testing.T) {
	proc := "  sl  local_address rem_address\n" +
		"  9: 00000000:0043 00000000:0000 07\n"
	if !keaListeningIn(proc, "10.30.0.1") {
		t.Fatal("a wildcard 0.0.0.0:67 bind serves every gateway and must count")
	}
}

// Kea running, answering and configured but holding nothing on udp/67.
func TestKeaAcceptedConfigButServesNothing(t *testing.T) {
	proc := "  sl  local_address rem_address\n" +
		"  7: 0100A8C0:0035 00000000:0000 07\n" + // :53, not :67 — DNS, not DHCP
		"  8: 0100007F:0043 00000000:0000 07\n" // 127.0.0.1:67 — loopback, not the gateway
	if keaListeningIn(proc, "192.168.77.1") {
		t.Fatal("neither a DNS socket nor a loopback DHCP socket serves the guest gateway; " +
			"reporting healthy here is the defect this check replaced")
	}
}

func TestGatewaysNotListeningSkipsRelayedAndDisabled(t *testing.T) {
	a := &applier{}
	managed := []netcfg.GuestNetwork{
		{Name: "local-on", Enabled: true, DHCPMode: netcfg.DHCPLocal, GatewayIP: "192.168.77.1"},
		{Name: "local-off", Enabled: false, DHCPMode: netcfg.DHCPLocal, GatewayIP: "192.168.78.1"},
		{Name: "relayed", Enabled: true, DHCPMode: netcfg.DHCPRelay, GatewayIP: "192.168.79.1"},
	}
	got := a.gatewaysNotListening(managed)
	if len(got) != 1 || got[0] != "192.168.77.1" {
		t.Fatalf("only the enabled, locally-served network is expected to hold a socket; got %v", got)
	}
}

// newBootstrapApplier records shell calls and redirects Kea's config and snapshot into a temp dir.
func newBootstrapApplier(t *testing.T) (*applier, *[]string) {
	t.Helper()
	dir := t.TempDir()
	var calls []string
	a := &applier{
		generatedDir: dir,
		keaConfFile:  filepath.Join(dir, "kea-dhcp4.conf"),
		keaSocket:    filepath.Join(dir, "no-such-socket"),
		runFn: func(_ context.Context, name string, args ...string) error {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil
		},
		outFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			return []byte("disabled\n"), nil
		},
	}
	return a, &calls
}

// A factory-clean appliance comes back factory-clean: stopped, disabled, no config of ours.
func TestBootstrapRollbackRestoresFactoryClean(t *testing.T) {
	a, calls := newBootstrapApplier(t)

	err := a.bootstrapKeaIfStopped(context.Background(), "rev-1", []byte(`{"Dhcp4":{}}`))
	if err == nil {
		t.Fatal("expected the bootstrap to fail when no control socket ever appears")
	}
	if !strings.Contains(err.Error(), "no control socket appeared") {
		t.Fatalf("the error must name what it waited for, got: %v", err)
	}
	// The undo snapshot must be recorded before anything is started.
	if _, ok := a.readKeaSnapshot(); !ok {
		t.Fatal("no pre-apply snapshot was written; a failed bootstrap could never be rolled back")
	}
	if _, err := os.Stat(a.keaConfPath()); err != nil {
		t.Fatalf("bootstrap should have written the config it intends Kea to start with: %v", err)
	}

	*calls = nil
	a.restoreKeaBootstrap(context.Background())

	joined := strings.Join(*calls, " | ")
	for _, want := range []string{"systemctl stop kea-dhcp4-server", "systemctl disable kea-dhcp4-server"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rollback must %q — the appliance was stopped and disabled before we touched it; got %q", want, joined)
		}
	}
	if _, err := os.Stat(a.keaConfPath()); !os.IsNotExist(err) {
		t.Fatal("there was no Kea config before the bootstrap, so ours must not survive the rollback")
	}
	if _, ok := a.readKeaSnapshot(); ok {
		t.Fatal("the snapshot must be consumed by the rollback")
	}
}

// A rollback must not stop or disable a Kea that was already running, and restores its config byte for byte.
func TestBootstrapRollbackRestoresPreviousConfig(t *testing.T) {
	a, calls := newBootstrapApplier(t)
	a.outFn = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "is-enabled" {
			return []byte("enabled\n"), nil
		}
		return []byte("active\n"), nil
	}
	previous := []byte(`{"Dhcp4":{"note":"the operator's own"}}`)
	if err := os.WriteFile(a.keaConfPath(), previous, 0o644); err != nil {
		t.Fatal(err)
	}

	_ = a.bootstrapKeaIfStopped(context.Background(), "rev-2", []byte(`{"Dhcp4":{"note":"ours"}}`))
	*calls = nil
	a.restoreKeaBootstrap(context.Background())

	got, err := os.ReadFile(a.keaConfPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(previous) {
		t.Fatalf("previous config not restored:\n got %s\nwant %s", got, previous)
	}
	joined := strings.Join(*calls, " | ")
	if strings.Contains(joined, "disable") || strings.Contains(joined, "stop kea") {
		t.Fatalf("Kea was enabled and active before the bootstrap; a rollback must not stop or disable it: %q", joined)
	}
}

// Confirming keeps the bootstrap: a later rollback must not return a serving appliance to stopped-and-disabled.
func TestConfirmDiscardsBootstrapSnapshot(t *testing.T) {
	a, _ := newBootstrapApplier(t)
	if err := a.writeKeaSnapshot(keaBootstrapState{WasEnabled: false, WasActive: false}); err != nil {
		t.Fatal(err)
	}
	a.clearKeaSnapshot()
	if _, ok := a.readKeaSnapshot(); ok {
		t.Fatal("confirm must discard the snapshot")
	}
	var calls []string
	a.runFn = func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	a.restoreKeaBootstrap(context.Background())
	if len(calls) != 0 {
		t.Fatalf("with no snapshot, rollback must not touch Kea; issued %v", calls)
	}
}

// The bootstrap is a no-op once Kea answers (a real listening socket stands in).
func TestBootstrapIsNoOpWhenKeaAnswers(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "kea.sock")
	l, err := listenUnix(sock)
	if err != nil {
		t.Skipf("unix sockets unavailable here: %v", err)
	}
	defer l.Close()

	var calls []string
	a := &applier{
		generatedDir: dir,
		keaConfFile:  filepath.Join(dir, "kea-dhcp4.conf"),
		keaSocket:    sock,
		runFn: func(_ context.Context, name string, args ...string) error {
			calls = append(calls, name)
			return nil
		},
	}
	if err := a.bootstrapKeaIfStopped(context.Background(), "rev-3", []byte(`{"Dhcp4":{}}`)); err != nil {
		t.Fatalf("bootstrap must be a no-op when Kea is answering: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("no-op must issue no commands; issued %v", calls)
	}
	if _, err := os.Stat(a.keaConfPath()); !os.IsNotExist(err) {
		t.Fatal("a no-op must not overwrite the running appliance's Kea config")
	}
	if _, ok := a.readKeaSnapshot(); ok {
		t.Fatal("a no-op must not leave a rollback snapshot behind")
	}
}

func TestKeaSnapshotRoundTrips(t *testing.T) {
	a, _ := newBootstrapApplier(t)
	want := keaBootstrapState{WasEnabled: true, WasActive: false, HadConfig: true, PrevConfig: []byte("x"), BootstrapID: "rev-9"}
	if err := a.writeKeaSnapshot(want); err != nil {
		t.Fatal(err)
	}
	got, ok := a.readKeaSnapshot()
	if !ok {
		t.Fatal("snapshot did not read back")
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Fatalf("snapshot did not round-trip:\n got %s\nwant %s", gb, wb)
	}
}

func listenUnix(path string) (net.Listener, error) { return net.Listen("unix", path) }
