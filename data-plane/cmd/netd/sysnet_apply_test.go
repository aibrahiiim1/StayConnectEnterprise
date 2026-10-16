package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/sysnet"
)

// a system-network manager over a temporary directory, with no real netplan, no database and a staged state.
func testSysnet(t *testing.T) (*sysNetMgr, sysnetPaths, *[]string) {
	t.Helper()
	dir := t.TempDir()
	ps := sysnetPaths{
		wan:     filepath.Join(dir, "netplan", "01-static.yaml"),
		lan:     filepath.Join(dir, "netplan", "02-lan-bridge.yaml"),
		backups: filepath.Join(dir, "backups"),
		pending: filepath.Join(dir, "lib", "sysnet-pending.json"),
		commit:  filepath.Join(dir, "run", "committed"),
	}
	_ = os.MkdirAll(filepath.Dir(ps.wan), 0o700)
	_ = os.MkdirAll(filepath.Dir(ps.commit), 0o700)
	var calls []string
	m := &sysNetMgr{
		wanIface: "ens160", lanPhys: "ens192", lanBr: "br-lan", confirmWindow: 120 * time.Second,
		files: &ps,
		currentFn: func(context.Context) (sysnet.WANConfig, sysnet.LANConfig) {
			return sysnet.WANConfig{Interface: "ens160", Mode: "static", IP: "192.0.2.10", PrefixLen: 24, Gateway: "192.0.2.1"},
				sysnet.LANConfig{PhysicalInterface: "ens192", Bridge: "br-lan"} // base LAN carries no address
		},
		verifyFn: func(sysnet.WANConfig, sysnet.LANConfig) map[string]bool {
			return map[string]bool{"wan_ip": true, "gateway": true, "management_reachable": true}
		},
	}
	m.runFn = func(name string, args ...string) error {
		_, pendingErr := os.Stat(ps.pending)
		calls = append(calls, name+" "+args[0]+map[bool]string{true: " [pending]", false: ""}[pendingErr == nil])
		return nil
	}
	return m, ps, &calls
}

func wanTo(ip string) sysnet.Proposal {
	return sysnet.Proposal{WAN: &sysnet.WANConfig{Interface: "ens160", Mode: "static", IP: ip, PrefixLen: 24, Gateway: "192.0.2.1"}}
}

// ROLLBACK WITH NOTHING PENDING REFUSES. It used to restore the newest backup directory -- a change confirmed
// long ago -- silently reverting everything applied since.
func TestSysnetRollbackWithNothingPendingRefuses(t *testing.T) {
	m, ps, calls := testSysnet(t)
	_ = os.WriteFile(ps.wan, []byte("current"), 0o600)
	old := filepath.Join(ps.backups, "20260101-000000")
	_ = os.MkdirAll(old, 0o700)
	_ = os.WriteFile(filepath.Join(old, "01-static.yaml"), []byte("months old"), 0o600)

	err := m.rollback(context.Background(), "op", "", "")
	if !errors.Is(err, errNothingPending) {
		t.Fatalf("rollback with nothing pending: %v", err)
	}
	if b, _ := os.ReadFile(ps.wan); string(b) != "current" {
		t.Fatalf("a refused rollback rewrote the WAN file: %q", b)
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused rollback ran netplan: %v", *calls)
	}
}

// THE PENDING MARKER EXISTS BEFORE THE CHANGE TAKES EFFECT, so a crash between `netplan apply` and the end of
// the apply still leaves the watchdog and the boot reconcile something to roll back.
func TestSysnetPendingMarkerIsWrittenBeforeNetplanApply(t *testing.T) {
	m, ps, calls := testSysnet(t)
	res, err := m.apply(context.Background(), wanTo("192.0.2.20"), "op", "", "")
	if err != nil || !res.OK || res.State != "pending_confirmation" {
		t.Fatalf("apply: %+v %v", res, err)
	}
	want := []string{"netplan generate", "netplan apply [pending]"}
	if len(*calls) != 2 || (*calls)[0] != want[0] || (*calls)[1] != want[1] {
		t.Fatalf("netplan calls %v, want %v (the marker must exist when apply runs)", *calls, want)
	}
	pc, ok := m.readPending()
	if !ok || pc.Backup == "" || pc.Deadline <= time.Now().Unix() {
		t.Fatalf("pending marker after apply: %+v ok=%v", pc, ok)
	}
	if _, err := os.Stat(ps.wan); err != nil {
		t.Fatal("the WAN file was not written")
	}
}

// A ROLLBACK REMOVES WHAT THE CHANGE CREATED. The backup records that the WAN file did not exist; restoring
// must delete it rather than leave the new configuration to take effect at the next netplan apply or reboot.
func TestSysnetRollbackRemovesFilesTheChangeCreated(t *testing.T) {
	m, ps, _ := testSysnet(t)
	if _, err := os.Stat(ps.wan); !os.IsNotExist(err) {
		t.Fatal("precondition: no WAN file")
	}
	if res, err := m.apply(context.Background(), wanTo("192.0.2.20"), "op", "", ""); err != nil || !res.OK {
		t.Fatalf("apply: %+v %v", res, err)
	}
	if err := m.rollback(context.Background(), "op", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ps.wan); !os.IsNotExist(err) {
		t.Fatalf("the file the change created survived its rollback: %v", err)
	}
	if _, ok := m.readPending(); ok {
		t.Fatal("the pending marker survived the rollback")
	}
}

// A failed verification rolls back immediately, restoring the previous content.
func TestSysnetFailedVerificationRestoresThePreviousFile(t *testing.T) {
	m, ps, _ := testSysnet(t)
	_ = os.WriteFile(ps.wan, []byte("previous"), 0o600)
	m.verifyFn = func(sysnet.WANConfig, sysnet.LANConfig) map[string]bool {
		return map[string]bool{"wan_ip": true, "gateway": true, "management_reachable": false}
	}
	res, err := m.apply(context.Background(), wanTo("192.0.2.20"), "op", "", "")
	if err != nil || res.State != "rolled_back" {
		t.Fatalf("an unreachable console after the change must roll back: %+v %v", res, err)
	}
	if b, _ := os.ReadFile(ps.wan); string(b) != "previous" {
		t.Fatalf("restore did not put the previous file back: %q", b)
	}
}

// THE LOCKOUT CHECK: a literal management pin refuses a WAN move away from it; "auto" follows.
func TestSysnetLiteralManagementPinRefusesAWANMove(t *testing.T) {
	m, _, calls := testSysnet(t)
	m.mgmtAddr = "192.0.2.10"
	res, err := m.apply(context.Background(), wanTo("192.0.2.20"), "op", "", "")
	if err != nil || res.OK || res.State != "failed" {
		t.Fatalf("a move away from the pinned console address was not refused: %+v %v", res, err)
	}
	found := false
	for _, is := range res.Validation.Issues {
		if is.Code == "management_pin_lockout" {
			found = true
		}
	}
	if !found || len(*calls) != 0 {
		t.Fatalf("issues %+v calls %v", res.Validation.Issues, *calls)
	}
	m.mgmtAddr = mgmtAddrAuto
	if res, _ := m.apply(context.Background(), wanTo("192.0.2.20"), "op", "", ""); !res.OK {
		t.Fatalf("NETD_MGMT_ADDR=auto must not refuse a WAN move: %+v", res)
	}
}

// netplan apply also re-asserts the firewall (the management pin follows the WAN), on apply and on restore.
func TestSysnetReassertsTheFirewallAfterEveryNetplanApply(t *testing.T) {
	m, _, _ := testSysnet(t)
	n := 0
	m.afterNetplan = func(context.Context) { n++ }
	if res, _ := m.apply(context.Background(), wanTo("192.0.2.20"), "op", "", ""); !res.OK {
		t.Fatal("apply failed")
	}
	if n != 1 {
		t.Fatalf("after apply: %d re-assertions", n)
	}
	_ = m.rollback(context.Background(), "op", "", "")
	if n != 2 {
		t.Fatalf("after rollback: %d re-assertions", n)
	}
}

// NETD_MGMT_ADDR=auto renders the management interface's current address; an interface with none renders no
// pin at all (interface-wide accept), never a pin to nothing.
func TestManagementPinAutoFollowsTheInterface(t *testing.T) {
	a := &applier{}
	a.topo.MgmtInterface = "ens160"
	addr := "192.0.2.10"
	a.mgmtAddrFn = func(iface string) string {
		if iface != "ens160" {
			t.Fatalf("asked about %s", iface)
		}
		return addr
	}
	if got := a.resolveMgmtAddr("auto"); got != "192.0.2.10" {
		t.Fatalf("auto -> %q", got)
	}
	addr = "198.51.100.4"
	if got := a.resolveMgmtAddr("auto"); got != "198.51.100.4" {
		t.Fatalf("auto did not follow the address: %q", got)
	}
	addr = ""
	if got := a.resolveMgmtAddr("auto"); got != "" {
		t.Fatalf("auto with no address must render no pin: %q", got)
	}
	if got := a.resolveMgmtAddr("203.0.113.9"); got != "203.0.113.9" {
		t.Fatalf("a literal pin was changed: %q", got)
	}
}
