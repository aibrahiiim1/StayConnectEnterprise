package shape

// /19 client networks: class-id allocation is collision-free over the whole subnet and across bridges, with the network, broadcast and
// gateway addresses excluded from the session set, and the legacy u32 fallback is bounded and unambiguous.

import (
	"fmt"
	"net"
	"testing"
)

func u32(ip4 net.IP) uint32 {
	return uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
}
func fromU32(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).To4()
}

// sessionAddrs is every address a client can hold in cidr: not the network address, not the broadcast, not the gateway (gateway = first host).
func sessionAddrs(t *testing.T, cidr string) (addrs []net.IP, gw net.IP) {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	ones, bits := n.Mask.Size()
	size := uint32(1) << uint(bits-ones)
	base := u32(n.IP.To4())
	gw = fromU32(base + 1)
	for h := uint32(1); h < size-1; h++ { // 0 = network, size-1 = broadcast
		if h == 1 {
			continue // the gateway never holds a session
		}
		addrs = append(addrs, fromU32(base+h))
	}
	return addrs, gw
}

// Exhaustive over a /19: all 8,189 session addresses get distinct minors in the guest bands, none in the SHARED band or the root's own
// classes, the gateway/network/broadcast are never part of the set, and the first and last of each band are as documented.
func TestSlash19EveryAddressHasItsOwnClass(t *testing.T) {
	withNets(t, map[string][]string{"br": {"10.62.0.0/19"}})
	addrs, gw := sessionAddrs(t, "10.62.0.0/19")
	if len(addrs) != 8189 {
		t.Fatalf("a /19 has 8190 hosts, one is the gateway: %d session addresses", len(addrs))
	}
	seen := make(map[int]string, len(addrs))
	var lo, hi = 1 << 30, 0
	for _, ip := range addrs {
		m, ok := MinorFor("br", ip)
		if !ok {
			t.Fatalf("no class for %s", ip)
		}
		if !IsGuestMinor(m) || (m >= GroupMinorBase && m <= GroupMinorMax) || m < 0x1000 || m > 0xffff {
			t.Fatalf("%s: minor %#x is not a per-session class", ip, m)
		}
		if prev, dup := seen[m]; dup {
			t.Fatalf("%s and %s share class minor %#x", prev, ip, m)
		}
		seen[m] = ip.String()
		if m < lo {
			lo = m
		}
		if m > hi {
			hi = m
		}
	}
	// the gateway's own minor (index 1) is never given to a session address
	if gm, _ := MinorFor("br", gw); seen[gm] != "" {
		t.Errorf("the gateway's class %#x is used by session %s", gm, seen[gm])
	}
	// first client is host index 2 (the original band starts at 0x1000), last is index 8190 -> 0x3000 + 8190 - 4096
	if lo != 0x1002 || hi != 0x3000+8190-4096 {
		t.Errorf("class range %#x..%#x, want 0x1002..%#x", lo, hi, 0x3000+8190-4096)
	}
	// network and broadcast would also be distinct, so a stray packet to them never lands in a client's class
	for _, ip := range []string{"10.62.0.0", "10.62.31.255"} {
		m, ok := MinorFor("br", net.ParseIP(ip))
		if !ok || seen[m] != "" {
			t.Errorf("%s: class %#x ok=%v collides with %s", ip, m, ok, seen[m])
		}
	}
}

// Several bridges at once: /19s, a /20 and a /24, one per bridge. Within each bridge the classes are distinct; the (device, minor) pair is
// unique appliance-wide because every bridge has its own HTB root and IFB, and each bridge answers only for its own subnet.
func TestClassesAreCollisionFreeAcrossBridges(t *testing.T) {
	nets := map[string][]string{
		"br-g10": {"10.62.0.0/19"},
		"br-g11": {"10.62.32.0/19"},
		"br-g12": {"10.62.64.0/20"},
		"br-g13": {"10.62.80.0/24"},
		"br-g14": {"10.63.0.0/19"},
	}
	withNets(t, nets)
	global := map[string]string{}
	for br, cs := range nets {
		addrs, _ := sessionAddrs(t, cs[0])
		per := map[int]string{}
		for _, ip := range addrs {
			m, ok := MinorFor(br, ip)
			if !ok {
				t.Fatalf("%s %s: no class", br, ip)
			}
			if prev, dup := per[m]; dup {
				t.Fatalf("%s: %s and %s share %#x", br, prev, ip, m)
			}
			per[m] = ip.String()
			key := fmt.Sprintf("%s/%#x", br, m)
			if _, dup := global[key]; dup {
				t.Fatalf("%s repeated", key)
			}
			global[key] = ip.String()
		}
	}
	// the same host index on two /19 bridges is the same minor on different devices, which is the design: classes belong to a device
	m1, _ := MinorFor("br-g10", net.ParseIP("10.62.31.254"))
	m2, _ := MinorFor("br-g11", net.ParseIP("10.62.63.254"))
	if m1 != m2 {
		t.Errorf("the last host of two /19 bridges differs: %#x vs %#x", m1, m2)
	}
	// an address that belongs to another bridge is not mapped by this bridge's geometry (original rule; a bridge never sees it)
	if m, ok := MinorFor("br-g13", net.ParseIP("10.62.31.254")); !ok || m != 0x1000+int(31*256+254)&0xfff {
		t.Errorf("a foreign address on a /24 bridge: %#x ok=%v", m, ok)
	}
}

// A /19 held on a bridge whose OTHER address is a /24 inside it (an overlapping secondary) takes the narrower subnet for that /24 only.
func TestSlash19ClassesSurviveASecondaryAddress(t *testing.T) {
	withNets(t, map[string][]string{"br": {"10.62.0.0/19", "10.62.20.0/24"}})
	m, _ := MinorFor("br", net.ParseIP("10.62.31.7"))
	if want := 0x3000 + (31*256 + 7 - 4096); m != want {
		t.Errorf("host outside the secondary: %#x, want %#x", m, want)
	}
}

// The legacy u32 fallback (NETD_SHAPE_CLASSIFIER=u32, or a kernel without cls_flower) names every filter by pref = class minor. On a /19 that
// is unambiguous (distinct, and inside the 16-bit priority range) but bounded by the kernel at 2,047 simultaneously classified sessions per
// device, which the classifier reports as a failed activation (fail closed); the /19 is accepted, its concurrent capacity is not.
func TestSlash19U32FallbackPrefsAreDistinctAndInRange(t *testing.T) {
	withNets(t, map[string][]string{"br": {"10.62.0.0/19"}})
	addrs, _ := sessionAddrs(t, "10.62.0.0/19")
	prefs := map[string]string{}
	for _, ip := range addrs {
		m, _ := MinorFor("br", ip)
		a := u32AddArgs("br", classidOf(m), "dst", ip)
		var pref string
		for i, x := range a {
			if x == "pref" {
				pref = a[i+1]
			}
		}
		if pref == "" {
			t.Fatalf("no pref in %v", a)
		}
		if prev, dup := prefs[pref]; dup {
			t.Fatalf("u32 pref %s used by %s and %s", pref, prev, ip)
		}
		prefs[pref] = ip.String()
		var p int
		fmt.Sscanf(pref, "%d", &p)
		if p < 1 || p > 0xffff {
			t.Fatalf("pref %d outside the 16-bit priority range", p)
		}
	}
	if len(prefs) != 8189 {
		t.Fatalf("%d prefs", len(prefs))
	}
}
