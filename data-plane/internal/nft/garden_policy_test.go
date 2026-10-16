package nft

import "testing"

// N-7: the enforcement floor -- no stored rule opens the internet.
func TestGardenEnforcementFloor(t *testing.T) {
	for _, bad := range []string{"0.0.0.0/0", "0.0.0.0", "1.0.0.0/7", "127.0.0.1", "224.0.0.1", "255.255.255.255", "::/0"} {
		if ValidGardenElem(bad) {
			t.Errorf("%s would be installed in the garden", bad)
		}
	}
	for _, good := range []string{"1.1.1.1", "34.0.0.0/8", "52.84.0.0/15", "93.184.216.34/32"} {
		if !ValidGardenElem(good) {
			t.Errorf("%s was refused by the enforcement floor", good)
		}
	}
}

// The operator-facing check: the site's widest network, and meaningless targets.
func TestGardenTargetRefusal(t *testing.T) {
	for v, refused := range map[string]bool{
		"0.0.0.0/0":      true,
		"52.84.0.0/15":   true, // wider than /24
		"52.84.12.0/24":  false,
		"52.84.12.7":     false,
		"10.1.2.3":       true, // private
		"192.168.1.0/24": true,
		"172.20.0.5":     true,
		"100.64.1.1":     true, // CGNAT
		"169.254.1.1":    true, // link-local
		"127.0.0.1":      true,
		"52.84.12.7/24":  true, // not a network address
		"not-an-address": true,
		"240.0.0.1":      true,
	} {
		why := GardenTargetRefusal(v, 24)
		if refused && why == "" {
			t.Errorf("%s was accepted", v)
		}
		if !refused && why != "" {
			t.Errorf("%s was refused: %s", v, why)
		}
	}
	if GardenTargetRefusal("52.84.0.0/16", 16) != "" {
		t.Error("a /16 was refused under a /16 policy")
	}
}
