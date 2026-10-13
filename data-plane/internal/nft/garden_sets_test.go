package nft

import (
	"strings"
	"testing"
)

// THREE SETS, ONE TRANSACTION, and a port-restricted element that survives a read-back.
func TestGardenScriptFlushesAndAddsEverySetInOneTransaction(t *testing.T) {
	plan := GardenPlan{IP: []string{"198.51.100.7"}, TCP: []string{"8.8.8.8 . 53", "203.0.113.9 . 443"}, UDP: []string{"8.8.8.8 . 53"}}
	all := map[string]bool{GardenV4: true, GardenTCP: true, GardenUDP: true}
	got := gardenScript(plan, all)
	want := "flush set inet stayconnect walled_garden_ip\n" +
		"add element inet stayconnect walled_garden_ip { 198.51.100.7 }\n" +
		"flush set inet stayconnect walled_garden_tcp\n" +
		"add element inet stayconnect walled_garden_tcp { 8.8.8.8 . 53, 203.0.113.9 . 443 }\n" +
		"flush set inet stayconnect walled_garden_udp\n" +
		"add element inet stayconnect walled_garden_udp { 8.8.8.8 . 53 }\n"
	if got != want {
		t.Fatalf("script:\n%s\nwant:\n%s", got, want)
	}
	// An empty set is flushed and left empty, never skipped (a stale element would otherwise survive).
	if s := gardenScript(GardenPlan{}, all); strings.Count(s, "flush set") != 3 || strings.Contains(s, "add element") {
		t.Fatalf("empty plan: %q", s)
	}
	// A set the live table does not declare yet is simply not named.
	if s := gardenScript(plan, map[string]bool{GardenV4: true}); strings.Contains(s, "walled_garden_tcp") {
		t.Fatalf("undeclared set named: %q", s)
	}
}

func TestGardenPortElemRefusesWhatTheKernelWould(t *testing.T) {
	for _, c := range []struct {
		addr string
		port int
	}{{"1.2.3.4", 0}, {"1.2.3.4 ", 443}, {"::1", 443}, {"example.com", 443}, {"1.2.3.4", 70000}} {
		if e := GardenPortElem(c.addr, c.port); e != "" {
			t.Errorf("%q:%d accepted as %q", c.addr, c.port, e)
		}
	}
	if e := GardenPortElem("10.0.0.0/8", 8443); e != "10.0.0.0/8 . 8443" {
		t.Errorf("cidr pair: %q", e)
	}
}

// The carry-over reads `address . port` back as the pair it is (converge re-adds it inside the replace).
func TestParseReadsAPortRestrictedElement(t *testing.T) {
	raw := []byte(`{"nftables":[{"set":{"family":"inet","name":"walled_garden_tcp","table":"stayconnect","type":["ipv4_addr","inet_service"],
	  "elem":[{"concat":["203.0.113.9",443]},{"elem":{"val":{"concat":["8.8.8.8",53]},"timeout":60,"expires":30}}]}}]}`)
	els, err := parseJSON(raw)
	if err != nil || len(els) != 2 {
		t.Fatalf("parse: %v %+v", err, els)
	}
	if els[0].IP.String() != "203.0.113.9" || els[0].Port != 443 || els[0].Iface != "" {
		t.Fatalf("permanent pair: %+v", els[0])
	}
	if els[1].IP.String() != "8.8.8.8" || els[1].Port != 53 || els[1].Expires == 0 {
		t.Fatalf("timed pair: %+v", els[1])
	}
}
