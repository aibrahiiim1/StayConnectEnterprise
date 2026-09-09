package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// THE FIRST GUEST NETWORK ON A FACTORY-CLEAN APPLIANCE, AND THE THREE WAYS KEA REFUSES TO SERVE IT.
//
// All three were found by running the guest-network workflow end to end on a real appliance. None of them
// surfaces as "DHCP is broken": each one leaves Kea answering its control socket and reporting healthy while
// guests send DHCPDISCOVER into silence, which from the guest side is indistinguishable from a dead cable.
//
//  1. THE BOOTSTRAP DEADLOCK. netd configures Kea through its control socket, and the socket exists only
//     while Kea runs. Provisioning installs Kea stopped and DISABLED on purpose, because it binds the guest
//     bridge and a new appliance has none. So Kea could not start without configuration and netd would not
//     configure it without the socket: the first guest network could never apply, and retrying could never
//     help. The bridge exists by the time the Kea step runs — l2l3 created it moments earlier — which is
//     precisely when Kea can legitimately start.
//
//  2. THE BIND GAP. Kea refuses to bind an interface that is not IFF_RUNNING:
//     DHCPSRV_OPEN_SOCKET_FAIL / DHCPSRV_NO_SOCKETS_OPEN. `ip link add` returns immediately, but a bridge is
//     not running until a member settles a moment later, and the Kea configuration used to land in that gap.
//
//  3. config-set DOES NOT RELIABLY REBIND. Rollback destroys a bridge and re-apply creates a new one with a
//     new ifindex; Kea accepts a configuration for an interface it never binds. status-get still reports
//     healthy and config-get still shows the right subnet. Only the socket tells the truth.
//
// The pattern in all three: ASSERT THE THING THAT MATTERS — is Kea LISTENING on each guest gateway — rather
// than asking it whether it feels well.

// keaBootstrapState is the pre-apply Kea state, captured before the first apply touches anything so a
// rollback can put the appliance back exactly as it found it.
//
// WHY IT IS ON DISK. The first apply STARTS Kea, because there is no other way to configure it. If that
// apply fails or its confirmation expires, there is no previous confirmed revision to fall back to — so a
// rollback that only tore down the bridge, the netplan and the unbound fragment would leave Kea running,
// enabled, and serving DHCP for a guest network that no longer exists. A factory-clean appliance has to come
// back factory-clean. The rollback that needs this is usually run by the confirmation watchdog, which also
// runs at boot, so the snapshot cannot live in memory.
type keaBootstrapState struct {
	WasEnabled  bool   `json:"was_enabled"`
	WasActive   bool   `json:"was_active"`
	PrevConfig  []byte `json:"prev_config,omitempty"`
	HadConfig   bool   `json:"had_config"`
	CapturedAt  string `json:"captured_at"`
	BootstrapID string `json:"bootstrap_id"`
}

func (a *applier) keaSnapshotPath() string {
	return filepath.Join(a.generatedDir, "kea-bootstrap-state.json")
}

// keaSocketPresent reports whether Kea is answering at all.
func (a *applier) keaSocketPresent() bool {
	c, err := net.DialTimeout("unix", a.keaSocket, 2*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// bootstrapKeaIfStopped breaks the deadlock in (1): it writes the rendered configuration straight to
// /etc/kea/kea-dhcp4.conf, enables the unit so the choice survives a reboot (provisioning left it disabled),
// starts it, and waits for the control socket.
//
// It is a NO-OP whenever Kea is already answering, so every later apply goes through the socket exactly as
// before. This runs only to break the one deadlock.
//
// It calls a.run, NEVER a.runFn. runFn is the test seam and is nil in production: calling it directly
// panicked on the first real apply, inside an HTTP handler, where the router's Recoverer turned it into a
// 500 — the apply never reached its own error path and the revision was left stuck in "applying" with no
// event and no rollback. Every test passed, because tests always set the seam. shell_seam_test.go now greps
// this package for direct seam calls, because nothing else we have can see that class of mistake.
func (a *applier) bootstrapKeaIfStopped(ctx context.Context, revID string, dhcp4Full []byte) error {
	if a.dryRun || a.keaSocketPresent() {
		return nil
	}

	slog.Info("kea bootstrap: no control socket; Kea is installed stopped on a factory-clean appliance and " +
		"cannot be configured until it runs")

	// Capture what we are about to change, BEFORE changing any of it.
	snap := keaBootstrapState{
		WasEnabled:  a.unitIsEnabled(ctx, "kea-dhcp4-server"),
		WasActive:   a.unitIsActive(ctx, "kea-dhcp4-server"),
		CapturedAt:  time.Now().UTC().Format(time.RFC3339),
		BootstrapID: revID,
	}
	if raw, err := os.ReadFile(a.keaConfPath()); err == nil {
		snap.PrevConfig, snap.HadConfig = raw, true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("kea bootstrap: read existing config: %w", err)
	}
	if err := a.writeKeaSnapshot(snap); err != nil {
		// Refuse to start Kea if we cannot record how to stop it again.
		return fmt.Errorf("kea bootstrap: record pre-apply state: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(a.keaConfPath()), 0o755); err != nil {
		return fmt.Errorf("kea bootstrap: %w", err)
	}
	if err := os.WriteFile(a.keaConfPath(), dhcp4Full, 0o644); err != nil {
		return fmt.Errorf("kea bootstrap: write config: %w", err)
	}
	if err := a.run(ctx, "systemctl", "enable", "kea-dhcp4-server"); err != nil {
		return fmt.Errorf("kea bootstrap: enable: %w", err)
	}
	if err := a.run(ctx, "systemctl", "restart", "kea-dhcp4-server"); err != nil {
		return fmt.Errorf("kea bootstrap: start: %w", err)
	}

	// WAIT FOR THE SOCKET rather than assuming it. A Kea that refuses the bridge then reports that directly,
	// instead of failing the following config-set with the same "no control socket" message that describes
	// the deadlock we just removed.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if a.keaSocketPresent() {
			slog.Info("kea bootstrap: control socket is up")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("kea bootstrap: started kea-dhcp4-server but no control socket appeared at %s within 20s", a.keaSocket)
}

// restoreKeaBootstrap undoes exactly what bootstrapKeaIfStopped did, and only if it ran. Confirming a
// revision discards the snapshot, because confirming is the operator saying to keep it.
func (a *applier) restoreKeaBootstrap(ctx context.Context) {
	snap, ok := a.readKeaSnapshot()
	if !ok || a.dryRun {
		return
	}
	slog.Warn("kea bootstrap: rolling back the state this appliance was in before its first guest network",
		"was_enabled", snap.WasEnabled, "was_active", snap.WasActive, "had_config", snap.HadConfig)

	if snap.HadConfig {
		if err := os.WriteFile(a.keaConfPath(), snap.PrevConfig, 0o644); err != nil {
			slog.Error("kea rollback: restore previous config", "err", err)
		}
	} else {
		// There was no config before us; ours must not survive.
		if err := os.Remove(a.keaConfPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Error("kea rollback: remove bootstrap config", "err", err)
		}
	}
	if !snap.WasActive {
		if err := a.run(ctx, "systemctl", "stop", "kea-dhcp4-server"); err != nil {
			slog.Error("kea rollback: stop", "err", err)
		}
	} else {
		if err := a.run(ctx, "systemctl", "restart", "kea-dhcp4-server"); err != nil {
			slog.Error("kea rollback: restart onto the restored config", "err", err)
		}
	}
	if !snap.WasEnabled {
		if err := a.run(ctx, "systemctl", "disable", "kea-dhcp4-server"); err != nil {
			slog.Error("kea rollback: disable", "err", err)
		}
	}
	a.clearKeaSnapshot()
}

func (a *applier) writeKeaSnapshot(s keaBootstrapState) error {
	if err := os.MkdirAll(a.generatedDir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(a.keaSnapshotPath(), raw, 0o600)
}

func (a *applier) readKeaSnapshot() (keaBootstrapState, bool) {
	var s keaBootstrapState
	raw, err := os.ReadFile(a.keaSnapshotPath())
	if err != nil {
		return s, false
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, false
	}
	return s, true
}

func (a *applier) clearKeaSnapshot() {
	if err := os.Remove(a.keaSnapshotPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Error("kea bootstrap: clear snapshot", "err", err)
	}
}

func (a *applier) keaConfPath() string {
	if a.keaConfFile != "" {
		return a.keaConfFile
	}
	return "/etc/kea/kea-dhcp4.conf"
}

func (a *applier) unitIsEnabled(ctx context.Context, unit string) bool {
	out, err := a.output(ctx, "systemctl", "is-enabled", unit)
	return err == nil && strings.TrimSpace(string(out)) == "enabled"
}

func (a *applier) unitIsActive(ctx context.Context, unit string) bool {
	out, err := a.output(ctx, "systemctl", "is-active", unit)
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

// --- (2) the bind gap -------------------------------------------------------------------------------

// waitBridgesRunning waits, bounded, for each managed bridge to be IFF_RUNNING before anything is told to
// use it.
//
// NON-FATAL ON TIMEOUT, deliberately: a bridge whose only member is an unplugged NIC never comes up, and an
// operator may legitimately configure the network before the cable is in. That must not fail an apply. What
// must not happen is configuring Kea against it SILENTLY — keaListeningFor below is what catches that.
func (a *applier) waitBridgesRunning(ctx context.Context, bridges []string) {
	if a.dryRun || len(bridges) == 0 {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, br := range bridges {
		for time.Now().Before(deadline) {
			if ifaceIsRunning(br) {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		if !ifaceIsRunning(br) {
			slog.Warn("guest bridge is not RUNNING yet; Kea may refuse to bind it. This is expected when the "+
				"guest NIC is unplugged, and the listening check will report it rather than assume success",
				"bridge", br)
		}
	}
}

// ifaceIsRunning reads IFF_RUNNING from sysfs. operstate alone is not enough for a bridge: it reads
// "unknown" on some kernels while flags already carry RUNNING.
func ifaceIsRunning(name string) bool {
	raw, err := os.ReadFile("/sys/class/net/" + name + "/flags")
	if err != nil {
		return false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(string(raw), "0x")), 16, 64)
	if err != nil {
		return false
	}
	const iffRunning = 0x40
	return v&iffRunning != 0
}

// --- (3) does Kea actually hold a DHCP socket? ------------------------------------------------------

// gatewaysNotListening returns the gateway addresses of locally-served, enabled networks that hold NO DHCP
// socket. An empty result is the only acceptable post-apply state.
//
// Only DHCPLocal networks are expected to hold a socket: a relayed network legitimately has none, and
// demanding one would fail an apply that is entirely correct.
func (a *applier) gatewaysNotListening(managed []netcfg.GuestNetwork) []string {
	proc := readProcNetUDP()
	var missing []string
	for _, n := range managed {
		if !n.Enabled || n.DHCPMode != netcfg.DHCPLocal || n.GatewayIP == "" {
			continue
		}
		if !keaListeningIn(proc, n.GatewayIP) {
			missing = append(missing, n.GatewayIP)
		}
	}
	return missing
}

// keaListeningFor reports whether something holds udp/67 bound to the given gateway address (or to the
// wildcard). It reads /proc/net/udp rather than asking Kea, because asking Kea is exactly what could not
// tell the difference.
func keaListeningFor(gatewayIP string) bool {
	return keaListeningIn(readProcNetUDP(), gatewayIP)
}

func readProcNetUDP() string {
	raw, err := os.ReadFile("/proc/net/udp")
	if err != nil {
		return ""
	}
	return string(raw)
}

// keaListeningIn is the pure half, so a test can state the /proc contents exactly.
func keaListeningIn(procNetUDP, gatewayIP string) bool {
	want := hexLE(gatewayIP)
	if want == "" {
		return false
	}
	for _, line := range strings.Split(procNetUDP, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		local := f[1] // "0100007F:0043"
		i := strings.IndexByte(local, ':')
		if i < 0 {
			continue
		}
		addr, port := strings.ToUpper(local[:i]), strings.ToUpper(local[i+1:])
		if port != "0043" { // 67
			continue
		}
		if addr == want || addr == "00000000" { // exact bind, or wildcard
			return true
		}
	}
	return false
}

// hexLE renders a dotted-quad the way /proc/net/udp does: little-endian, uppercase hex.
func hexLE(ip string) string {
	p := strings.Split(ip, ".")
	if len(p) != 4 {
		return ""
	}
	b := make([]byte, 4)
	for i, s := range p {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 || v > 255 {
			return ""
		}
		b[3-i] = byte(v)
	}
	return fmt.Sprintf("%02X%02X%02X%02X", b[0], b[1], b[2], b[3])
}
