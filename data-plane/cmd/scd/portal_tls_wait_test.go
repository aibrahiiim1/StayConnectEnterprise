package main

import (
	"testing"
	"time"
)

// After a reboot scd decides before netd has brought the portal address up; it must look again within seconds, not after five minutes.
func TestPortalTLSRecheckSoonWhileOnlyTheAddressIsMissing(t *testing.T) {
	cases := []struct {
		name    string
		st      PortalTLSState
		pending bool
		want    time.Duration
	}{
		{"certificate ready, address not up yet", PortalTLSState{FallbackReason: ptReasonAddressNotActive, Canonical: "portal.example", PortalIP: "10.255.255.1"}, false, portalTLSAddressEvery},
		{"no portal address configured at all", PortalTLSState{FallbackReason: ptReasonAddressNotActive, Canonical: "portal.example"}, false, portalTLSEvery},
		{"an order is open", PortalTLSState{FallbackReason: ptReasonNoCertificate}, true, portalTLSPendingEvery},
		{"on HTTPS", PortalTLSState{HTTPSActive: true, Canonical: "portal.example", PortalIP: "10.255.255.1"}, false, portalTLSEvery},
	}
	for _, c := range cases {
		a := &portalTLSAgent{}
		a.snap, a.pendingAny = c.st, c.pending
		if got := a.nextWait(); got != c.want {
			t.Errorf("%s: wait %v, want %v", c.name, got, c.want)
		}
	}
}
