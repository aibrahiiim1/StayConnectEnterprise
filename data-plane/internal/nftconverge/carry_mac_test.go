package nftconverge

import (
	"net"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
)

// A structural replace carries a MAC-bound authorization with its device address and remaining lease; dropping the MAC
// would make the re-added element a two-part key that the three-part set refuses, failing the whole atomic `nft -f`.
func TestCarryKeepsTheMACOfABoundAuthorization(t *testing.T) {
	cmds := CarryOverCommands(nft.Phase3MACV4, []nft.Element{
		{Iface: "br-g20", IP: net.ParseIP("10.20.0.5"), MAC: "aa:bb:cc:dd:ee:01", Timeout: 90 * time.Second, Expires: 40 * time.Second},
		{Iface: "br-g20", IP: net.ParseIP("10.20.0.6"), MAC: "aa:bb:cc:dd:ee:02", Timeout: 90 * time.Second, Expires: 0},
	})
	want := `add element inet stayconnect phase3_auth_mac_ipv4 { "br-g20" . 10.20.0.5 . aa:bb:cc:dd:ee:01 timeout 40s }`
	if len(cmds) != 1 || cmds[0] != want {
		t.Fatalf("got %v want [%s] (the expired element must not be resurrected)", cmds, want)
	}
}

func TestTheMACBoundSetIsCarriedAcrossAReplace(t *testing.T) {
	for _, s := range AuthSets {
		if s == nft.Phase3MACV4 {
			return
		}
	}
	t.Fatal("phase3_auth_mac_ipv4 is not in AuthSets: enabling binding then re-asserting the ruleset would deauthorize everyone")
}
