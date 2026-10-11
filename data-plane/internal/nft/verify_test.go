package nft

import (
	"net"
	"strings"
	"testing"
	"time"
)

// ONE GRANT, ONE TRANSACTION: the objects, their map entries and the timed admission, in an order the kernel
// accepts (objects before the map entries that name them).
func TestVerifyScriptInstallsAGrantInOneOrderedTransaction(t *testing.T) {
	s, err := VerifyScript(VerifyGrant{Key: "0123456789ab", Bridge: "br-g90", IP: net.ParseIP("192.168.77.20"),
		Remaining: 599*time.Second + 400*time.Millisecond, CapBytes: 25 << 20, UsedBytes: 1000, UpKbps: 1000, DownKbps: 2000})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`add quota inet stayconnect vq_0123456789ab { over 26214400 bytes used 1000 bytes }`,
		`add limit inet stayconnect vu_0123456789ab { rate over 125000 bytes/second burst 62500 bytes }`,
		`add limit inet stayconnect vd_0123456789ab { rate over 250000 bytes/second burst 125000 bytes }`,
		`add element inet stayconnect verify_quota { 192.168.77.20 : "vq_0123456789ab" }`,
		`add element inet stayconnect verify_up { 192.168.77.20 : "vu_0123456789ab" }`,
		`add element inet stayconnect verify_down { 192.168.77.20 : "vd_0123456789ab" }`,
		`add element inet stayconnect verify_ipv4 { "br-g90" . 192.168.77.20 timeout 599s }`,
	}
	if got := strings.Split(strings.TrimSpace(s), "\n"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("script:\n%s", s)
	}
}

func TestVerifyScriptRefusesWhatCouldInjectOrGrantNothing(t *testing.T) {
	ok := VerifyGrant{Key: "0123456789ab", Bridge: "br-g90", IP: net.ParseIP("10.0.0.2"), Remaining: time.Minute, CapBytes: 1, UpKbps: 1, DownKbps: 1}
	for name, mut := range map[string]func(*VerifyGrant){
		"bad key":      func(g *VerifyGrant) { g.Key = "x; flush ruleset" },
		"bad bridge":   func(g *VerifyGrant) { g.Bridge = `br" ; flush ruleset` },
		"ipv6":         func(g *VerifyGrant) { g.IP = net.ParseIP("::1") },
		"nothing left": func(g *VerifyGrant) { g.Remaining = 500 * time.Millisecond },
		"no cap":       func(g *VerifyGrant) { g.CapBytes = 0 },
		"no rate":      func(g *VerifyGrant) { g.UpKbps = 0 },
	} {
		g := ok
		mut(&g)
		if _, err := VerifyScript(g); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A small rate still gets a usable burst.
	if _, b := kbpsToRate(64); b != "16384 bytes" {
		t.Errorf("burst floor: %s", b)
	}
}

func TestVerifyQuotaReadOnlyClaimsItsOwnObjects(t *testing.T) {
	raw := []byte(`{"nftables":[{"metainfo":{}},{"quota":{"family":"inet","name":"vq_0123456789ab","table":"stayconnect","bytes":26214400,"used":5000,"inv":true}},
	 {"quota":{"family":"inet","name":"some_other","table":"stayconnect","used":7}},{"quota":{"name":"vq_short","used":9}}]}`)
	got, err := parseVerifyQuotas(raw)
	if err != nil || len(got) != 1 || got["0123456789ab"] != 5000 {
		t.Fatalf("got %v %v", got, err)
	}
}
