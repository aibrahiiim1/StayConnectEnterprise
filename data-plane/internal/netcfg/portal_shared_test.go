package netcfg

import (
	"strings"
	"testing"
)

// Every appliance answers the shared Client Portal name locally with its OWN portal address, first in the list.
func TestTheSharedPortalNameIsAnsweredLocally(t *testing.T) {
	p := PortalTLS{SharedHostname: "portal.onegate.getsemantics.com", OneGateHostname: "k3j9x0ab.gw.onegate.work"}
	if got := PortalHostnames(p); len(got) != 2 || got[0] != "portal.onegate.getsemantics.com" || got[1] != "k3j9x0ab.gw.onegate.work" {
		t.Fatalf("names: %v", got)
	}
	out := string(RenderUnboundPortal("10.255.255.1", p))
	if !strings.Contains(out, `local-data: "portal.onegate.getsemantics.com. IN A 10.255.255.1"`) ||
		!strings.Contains(out, `local-zone: "portal.onegate.getsemantics.com." static`) {
		t.Fatalf("the shared name must resolve to this appliance's portal address:\n%s", out)
	}
	if got := PortalHostnames(PortalTLS{SharedHostname: "not a name"}); len(got) != 0 {
		t.Fatalf("an invalid name is never rendered: %v", got)
	}
}
