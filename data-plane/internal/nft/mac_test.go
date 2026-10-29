package nft

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func macClient(listJSON string) (*Client, *[]string) {
	var cmds []string
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmds = append(cmds, strings.Join(args, " "))
		if len(args) > 0 && args[0] == "-j" {
			return []byte(listJSON), nil
		}
		return nil, nil
	}
	return &Client{NftPath: "nft", exec: exec, concatBySet: map[string]bool{}}, &cmds
}

const macListJSON = `{"nftables":[{"set":{"name":"phase3_auth_mac_ipv4","elem":[
 {"elem":{"val":{"concat":["br-g20","10.20.0.5","aa:bb:cc:dd:ee:01"]},"timeout":90,"expires":40}},
 {"elem":{"val":{"concat":["br-g20","10.20.0.6","aa:bb:cc:dd:ee:02"]},"timeout":90,"expires":10}}]}}]}`

func TestParseReadsTheMACOfAThreePartKey(t *testing.T) {
	els, err := ParseSetJSON([]byte(macListJSON))
	if err != nil || len(els) != 2 {
		t.Fatalf("err=%v els=%v", err, els)
	}
	if els[0].Iface != "br-g20" || els[0].MAC != "aa:bb:cc:dd:ee:01" || !els[0].IP.Equal(net.ParseIP("10.20.0.5")) || els[0].Expires != 40*time.Second {
		t.Fatalf("bad element: %+v", els[0])
	}
}

func TestLeaseMACWritesTheThreePartKeyWithABoundedTimeout(t *testing.T) {
	c, cmds := macClient(`{"nftables":[]}`)
	if err := c.LeaseMAC(context.Background(), "br-g20", net.ParseIP("10.20.0.5"), "AA:BB:CC:DD:EE:01", 90*time.Second); err != nil {
		t.Fatal(err)
	}
	last := (*cmds)[len(*cmds)-1]
	want := `add element inet stayconnect phase3_auth_mac_ipv4 { "br-g20" . 10.20.0.5 . aa:bb:cc:dd:ee:01 timeout 90s }`
	if last != want {
		t.Fatalf("got  %s\nwant %s", last, want)
	}
}

func TestLeaseMACReplacesTheMACAnAddressHeldBeforeInOneTransaction(t *testing.T) {
	c, cmds := macClient(macListJSON)
	if err := c.LeaseMAC(context.Background(), "br-g20", net.ParseIP("10.20.0.5"), "aa:bb:cc:dd:ee:99", 90*time.Second); err != nil {
		t.Fatal(err)
	}
	last := (*cmds)[len(*cmds)-1]
	if !strings.Contains(last, `delete element inet stayconnect phase3_auth_mac_ipv4 { "br-g20" . 10.20.0.5 . aa:bb:cc:dd:ee:01 } ; add element`) ||
		!strings.Contains(last, "aa:bb:cc:dd:ee:99 timeout 90s") {
		t.Fatalf("a reassigned address must drop the previous holder's element in the same command buffer: %s", last)
	}
	if strings.Contains(last, "10.20.0.6") {
		t.Fatalf("another address was touched: %s", last)
	}
}

func TestLeaseMACRefusesWhatCannotBeBoundedOrBound(t *testing.T) {
	c, cmds := macClient(`{"nftables":[]}`)
	ip := net.ParseIP("10.20.0.5")
	if err := c.LeaseMAC(context.Background(), "br", ip, "aa:bb:cc:dd:ee:01", 0); err != ErrUnboundedLease {
		t.Fatalf("permanent lease: %v", err)
	}
	if err := c.LeaseMAC(context.Background(), "br", ip, "aa:bb:cc:dd:ee:01", 300*time.Millisecond); err != ErrLeaseTooShort {
		t.Fatalf("sub-second lease: %v", err)
	}
	for _, bad := range []string{"", "not-a-mac", "aa:bb:cc:dd:ee", "00:00:5e:00:53:01:02:03"} {
		if err := c.LeaseMAC(context.Background(), "br", ip, bad, time.Minute); err == nil {
			t.Fatalf("%q was accepted as a device address", bad)
		}
	}
	if len(*cmds) != 0 {
		t.Fatalf("nft was invoked for a refused lease: %v", *cmds)
	}
}

func TestDenyMACRemovesEveryMACOfTheAddressAndNothingElse(t *testing.T) {
	c, cmds := macClient(macListJSON)
	if err := c.DenyMAC(context.Background(), "br-g20", net.ParseIP("10.20.0.6")); err != nil {
		t.Fatal(err)
	}
	last := (*cmds)[len(*cmds)-1]
	if last != `delete element inet stayconnect phase3_auth_mac_ipv4 { "br-g20" . 10.20.0.6 . aa:bb:cc:dd:ee:02 }` {
		t.Fatalf("got %s", last)
	}
	before := len(*cmds)
	if err := c.DenyMAC(context.Background(), "br-g20", net.ParseIP("10.20.0.99")); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range (*cmds)[before:] {
		if strings.Contains(cmd, "delete") {
			t.Fatalf("absent address must be a no-op, got %s", cmd)
		}
	}
}
