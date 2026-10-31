package netcfg

import (
	"os"
	"testing"
)

// Writes the Kea file the product renders for a /19 client network, for the container lab that runs a real kea-dhcp4 against it
// (docs/runbooks/NETWORK_19_LAB_PLAN.md). Skipped unless KEA_FIXTURE_OUT names the file.
func TestWriteKeaSlash19Fixture(t *testing.T) {
	out := os.Getenv("KEA_FIXTURE_OUT")
	if out == "" {
		t.Skip("set KEA_FIXTURE_OUT to write the fixture")
	}
	n := GuestNetwork{ID: "11111111-2222-3333-4444-555555555555", Name: "Lab /19", Enabled: true, NetworkType: "untagged", ParentInterface: "kc1",
		BridgeName: "br-k19", GatewayIP: "10.65.0.1", SubnetCIDR: "10.65.0.0/19", PrefixLen: 19, DHCPMode: DHCPLocal, DNSMode: "appliance",
		DomainName: "guest.local", LeaseDefault: 3600, LeaseMin: 900, LeaseMax: 7200, CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
		Pools:        []Pool{{StartIP: "10.65.0.10", EndIP: "10.65.31.250"}},
		Reservations: []Reservation{{MAC: "02:00:00:00:03:e8", ReservedIP: "10.65.31.254", Hostname: "top-of-range", Enabled: true}}}
	if iss := ValidateOne(n, DefaultTopology(), nil, nil, ""); len(iss) != 0 {
		t.Fatalf("the fixture network is not valid: %+v", iss)
	}
	raw, err := RenderKeaFile([]GuestNetwork{n}, DefaultTopology(), "/var/lib/kea/kea-leases4.csv", "/run/kea/kea4-ctrl.sock")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
