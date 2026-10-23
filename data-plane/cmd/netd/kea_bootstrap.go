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

// First guest network on a factory-clean appliance. Kea can answer its control socket and report healthy while
// guests get no DHCP, so the check is whether Kea is listening on each guest gateway:
//  1. Bootstrap deadlock: the control socket exists only while Kea runs, but provisioning installs Kea stopped
//     and disabled (it binds the guest bridge), so the first apply must start it once the bridge exists.
//  2. Bind gap: Kea will not bind an interface that is not IFF_RUNNING, and a new bridge is not running until
//     a member settles.
//  3. config-set does not reliably rebind a recreated bridge (new ifindex); status-get and config-get still look healthy.

// keaBootstrapState is the pre-apply Kea state, captured before the first apply so a rollback can restore a
// factory-clean appliance (else Kea stays enabled and serving a removed network). It is on disk because the
// confirmation watchdog, which runs the rollback, also runs at boot.
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

func (a *applier) keaSocketPresent() bool {
	c, err := net.DialTimeout("unix", a.keaSocket, 2*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// bootstrapKeaIfStopped breaks deadlock (1): it writes the rendered config to /etc/kea/kea-dhcp4.conf, enables
// the unit, starts it and waits for the control socket. It is a no-op whenever Kea already answers.
//
// It must call a.run, never a.runFn: runFn is the test seam and is nil in production, so a direct call panics
// on a real apply and leaves the revision stuck in "applying" (shell_seam_test.go greps for direct seam calls).
func (a *applier) bootstrapKeaIfStopped(ctx context.Context, revID string, dhcp4Full []byte) error {
	if a.dryRun || a.keaSocketPresent() {
		return nil
	}

	slog.Info("kea bootstrap: no control socket; Kea is installed stopped on a factory-clean appliance and " +
		"cannot be configured until it runs")

	// Capture what we are about to change before changing it.
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
		// Do not start Kea if we cannot record how to undo it.
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

	// Wait for the socket so a Kea that refuses the bridge reports that, not a misleading "no control socket" later.
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

// restoreKeaBootstrap undoes bootstrapKeaIfStopped, only if it ran. Confirming a revision discards the snapshot.
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

// waitBridgesRunning waits, bounded, for each bridge to be IFF_RUNNING. Timeout is non-fatal on purpose: a
// bridge with an unplugged NIC never comes up, and the network may be configured before the cable is in;
// keaListeningFor catches a silently unbound bridge.
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

// ifaceIsRunning reads IFF_RUNNING from sysfs; operstate reads "unknown" on some kernels while flags carry RUNNING.
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

// gatewaysNotListening returns gateways of enabled, locally-served networks with no DHCP socket (relayed
// networks legitimately have none). Empty is the only acceptable post-apply state.
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

// keaListeningFor reports whether something holds udp/67 on the gateway (or wildcard). It reads /proc/net/udp
// because Kea's own status cannot tell the difference.
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
		local := f[1]
		i := strings.IndexByte(local, ':')
		if i < 0 {
			continue
		}
		addr, port := strings.ToUpper(local[:i]), strings.ToUpper(local[i+1:])
		if port != "0043" { // port 67
			continue
		}
		if addr == want || addr == "00000000" {
			return true
		}
	}
	return false
}

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
