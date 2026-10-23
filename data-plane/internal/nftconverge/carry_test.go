package nftconverge

import (
	"net"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/nft"
)

// The converge carries runtime state only: baseline resolver pairs are declared by the render and are not carried; a
// scd garden pair on the same set, or the same resolver on another port, is runtime state and is carried.
func TestTheRenderedResolverPairsAreNotCarried(t *testing.T) {
	var els []nft.Element
	for _, ip := range netcfg.BaselineResolvers {
		els = append(els, nft.Element{IP: net.ParseIP(ip), Port: netcfg.BaselineResolverPort})
	}
	els = append(els,
		nft.Element{IP: net.ParseIP("142.250.1.1"), Port: 443}, // an scd garden pair
		nft.Element{IP: net.ParseIP("1.1.1.1"), Port: 443},     // a baseline address on another port: runtime
	)
	for _, set := range []string{nft.GardenTCP, nft.GardenUDP} {
		got := withoutRendered(set, els)
		if len(got) != 2 || got[0].Port != 443 || got[1].Port != 443 {
			t.Fatalf("%s: carried %+v, want only the two runtime pairs", set, got)
		}
	}
	auth := []nft.Element{{Iface: "br-g", IP: net.ParseIP("10.0.0.5")}}
	if got := withoutRendered(nft.AuthV4, auth); len(got) != 1 {
		t.Fatalf("auth set changed: %+v", got)
	}
}
