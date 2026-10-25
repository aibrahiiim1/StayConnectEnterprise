package netstatus

import (
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

func net90(enabled bool) netcfg.GuestNetwork {
	return netcfg.GuestNetwork{
		ID: "n90", Name: "Guests", Enabled: enabled, NetworkType: "vlan", ParentInterface: "ens192", VLANID: 90, BridgeName: "br-g90",
		GatewayIP: "10.90.0.1", SubnetCIDR: "10.90.0.0/24", DHCPMode: netcfg.DHCPLocal, DNSMode: "appliance", DomainName: "guest.local",
		LeaseDefault: 3600, LeaseMin: 900, LeaseMax: 7200, CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
		Pools: []netcfg.Pool{{StartIP: "10.90.0.100", EndIP: "10.90.0.200"}},
	}
}

func ptr(n netcfg.GuestNetwork) *netcfg.GuestNetwork { return &n }

func TestNeverActivatedStaysNotActivatedWhateverIsSaved(t *testing.T) {
	r := Derive(Input{Saved: net90(false)})
	if r.Status != NotActivated {
		t.Fatalf("%+v", r)
	}
}

func TestSwitchedOffAfterBeingConfirmedIsDisabledAndNeedsNoNewActivation(t *testing.T) {
	r := Derive(Input{Saved: net90(false), EverActivated: true})
	if r.Status != Disabled || !strings.Contains(r.Reason, "needs no new activation") {
		t.Fatalf("%+v", r)
	}
}

// Saving a network as enabled, or an API call returning success, does not make it operational.
func TestSavedAsEnabledIsNotActiveUntilItIsRunning(t *testing.T) {
	r := Derive(Input{Saved: net90(true)})
	if r.Status != ChangePending {
		t.Fatalf("%+v", r)
	}
	if len(r.Changes) != 1 || r.Changes[0].Field != "enabled" {
		t.Fatalf("%+v", r.Changes)
	}
}

func TestBeingAppliedIsActivatingNotActive(t *testing.T) {
	r := Derive(Input{Saved: net90(true), InFlight: &Revision{Intent: []netcfg.GuestNetwork{net90(true)}}})
	if r.Status != Activating {
		t.Fatalf("%+v", r)
	}
}

func TestRunningAndSavedAlikeIsActive(t *testing.T) {
	r := Derive(Input{Saved: net90(true), Running: ptr(net90(true)), EverActivated: true, Health: Health{Known: true}})
	if r.Status != Active || len(r.Changes) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestAFaultTheApplianceObservesMakesItDegradedNotActive(t *testing.T) {
	r := Derive(Input{Saved: net90(true), Running: ptr(net90(true)), EverActivated: true,
		Health: Health{Known: true, Failing: []string{"dhcp_bound"}}})
	if r.Status != ActiveDegraded || !strings.Contains(r.Reason, "dhcp_bound") {
		t.Fatalf("%+v", r)
	}
}

func TestUnknownHealthIsNotReportedAsAFault(t *testing.T) {
	r := Derive(Input{Saved: net90(true), Running: ptr(net90(true)), Health: Health{Known: false, Failing: []string{"x"}}})
	if r.Status != Active {
		t.Fatalf("unreadable health was read as a fault: %+v", r)
	}
}

func TestSavedSettingsThatDifferFromWhatRunsAreAPendingChange(t *testing.T) {
	saved := net90(true)
	saved.GatewayIP, saved.LeaseDefault = "10.90.0.254", 1800
	r := Derive(Input{Saved: saved, Running: ptr(net90(true)), EverActivated: true})
	if r.Status != ChangePending {
		t.Fatalf("%+v", r)
	}
	if got := fields(r.Changes); got != "gateway_ip, lease_default_seconds" {
		t.Fatalf("changes: %s", got)
	}
	if !strings.Contains(r.Reason, "gateway_ip") {
		t.Fatalf("%q", r.Reason)
	}
}

func TestSavedAsDisabledWhileRunningIsAPendingChangeNotDisabled(t *testing.T) {
	r := Derive(Input{Saved: net90(false), Running: ptr(net90(true)), EverActivated: true})
	if r.Status != ChangePending || r.Changes[0].Field != "enabled" || r.Changes[0].Running != "on" {
		t.Fatalf("%+v", r)
	}
}

func TestAChangeBeingAppliedToARunningNetworkIsPending(t *testing.T) {
	saved := net90(true)
	saved.Name = "Renamed"
	r := Derive(Input{Saved: saved, Running: ptr(net90(true)), InFlight: &Revision{Intent: []netcfg.GuestNetwork{saved}}})
	if r.Status != ChangePending || !strings.Contains(r.Reason, "being applied") {
		t.Fatalf("%+v", r)
	}
}

func TestARevisionThatDoesNotTouchTheNetworkLeavesItAlone(t *testing.T) {
	other := net90(true)
	other.ID = "other"
	r := Derive(Input{Saved: net90(true), Running: ptr(net90(true)), InFlight: &Revision{Intent: []netcfg.GuestNetwork{other, net90(true)}}})
	if r.Status != Active {
		t.Fatalf("%+v", r)
	}
}

func TestAFailedChangeIsFailedWithItsReasonWhileTheSameSettingsAreStillSaved(t *testing.T) {
	r := Derive(Input{Saved: net90(true), EverActivated: false,
		LastFailed: &Revision{Intent: []netcfg.GuestNetwork{net90(true)}, Reason: "health check failed: gateway_up"}})
	if r.Status != Failed || !strings.Contains(r.Reason, "gateway_up") {
		t.Fatalf("%+v", r)
	}

	saved := net90(true)
	saved.Name = "Renamed"
	r = Derive(Input{Saved: saved, Running: ptr(net90(true)),
		LastFailed: &Revision{Intent: []netcfg.GuestNetwork{saved}, Reason: "netplan generate failed"}})
	if r.Status != Failed || !strings.Contains(r.Reason, "netplan generate failed") {
		t.Fatalf("%+v", r)
	}
}

// An old failure about different settings does not label a newer, unrelated edit as failed.
func TestAnOlderFailureOfDifferentSettingsIsNotBlamed(t *testing.T) {
	older := net90(true)
	older.Name = "Old attempt"
	saved := net90(true)
	saved.Name = "New attempt"
	r := Derive(Input{Saved: saved, Running: ptr(net90(true)), LastFailed: &Revision{Intent: []netcfg.GuestNetwork{older}, Reason: "x"}})
	if r.Status != ChangePending {
		t.Fatalf("%+v", r)
	}
}

func TestAFailureWithNoRecordedReasonSaysSo(t *testing.T) {
	r := Derive(Input{Saved: net90(true), LastFailed: &Revision{Intent: []netcfg.GuestNetwork{net90(true)}}})
	if r.Status != Failed || !strings.Contains(r.Reason, "no reason was recorded") {
		t.Fatalf("%+v", r)
	}
}

func TestDiffIgnoresPoolOrderAndReportsRealPoolChanges(t *testing.T) {
	a, b := net90(true), net90(true)
	a.Pools = []netcfg.Pool{{StartIP: "10.90.0.10", EndIP: "10.90.0.20"}, {StartIP: "10.90.0.100", EndIP: "10.90.0.200"}}
	b.Pools = []netcfg.Pool{{StartIP: "10.90.0.100", EndIP: "10.90.0.200"}, {StartIP: "10.90.0.10", EndIP: "10.90.0.20"}}
	if d := Diff(a, b); len(d) != 0 {
		t.Fatalf("%+v", d)
	}
	b.Pools = b.Pools[:1]
	if d := Diff(a, b); len(d) != 1 || d[0].Field != "pools" {
		t.Fatalf("%+v", d)
	}
}
