package shape

import (
	"context"
	"net"
	"strings"
	"testing"
)

func withNets(t *testing.T, m map[string][]string) {
	t.Helper()
	old := bridgeNets
	bridgeNets = func(b string) []*net.IPNet {
		var out []*net.IPNet
		for _, c := range m[b] {
			_, n, err := net.ParseCIDR(c)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}
	t.Cleanup(func() { bridgeNets = old })
}

// Every subnet of /20 or narrower keeps exactly the class ids of the pre-extension scheme, so a deployed appliance's classes survive an upgrade.
func TestNarrowSubnetsKeepTheOriginalMinors(t *testing.T) {
	for _, cidr := range []string{"10.20.0.0/20", "10.20.16.0/20", "10.20.240.0/20", "192.168.7.0/24", "10.0.48.0/22", "172.16.0.0/12"} {
		withNets(t, map[string][]string{"br": {cidr}})
		_, n, _ := net.ParseCIDR(cidr)
		ones, _ := n.Mask.Size()
		step := 1
		if ones < 20 {
			step = 997
		}
		base := n.IP.To4()
		for h := 1; h < 1<<(32-ones) && h < 6000; h += step {
			ip := net.IPv4(base[0], base[1], base[2], base[3]).To4()
			v := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
			v += uint32(h)
			ip = net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
			got, ok1 := MinorFor("br", ip)
			want, ok2 := MinorForIP(ip)
			if !ok1 || !ok2 || got != want {
				t.Fatalf("%s host %d (%s): minor %x, original rule %x", cidr, h, ip, got, want)
			}
		}
	}
}

// In a /17../19 every host of the subnet gets its own minor, inside the guest range, clear of the SHARED band and the root's own classes.
func TestWideSubnetMinorsAreUniqueAndInRange(t *testing.T) {
	for _, tc := range []struct {
		cidr  string
		hosts int
	}{{"10.30.0.0/19", 8192}, {"10.30.64.0/18", 16384}, {"10.30.128.0/17", 32768}} {
		withNets(t, map[string][]string{"br": {tc.cidr}})
		_, n, _ := net.ParseCIDR(tc.cidr)
		seen := map[int]string{}
		v := uint32(n.IP[0])<<24 | uint32(n.IP[1])<<16 | uint32(n.IP[2])<<8 | uint32(n.IP[3])
		for h := 1; h < tc.hosts-1; h++ {
			x := v + uint32(h)
			ip := net.IPv4(byte(x>>24), byte(x>>16), byte(x>>8), byte(x))
			m, ok := MinorFor("br", ip)
			if !ok {
				t.Fatalf("%s: no minor for %s", tc.cidr, ip)
			}
			if !IsGuestMinor(m) {
				t.Fatalf("%s: minor %x for %s is outside the guest bands", tc.cidr, m, ip)
			}
			if m >= GroupMinorBase && m <= GroupMinorMax {
				t.Fatalf("%s: minor %x for %s collides with the SHARED group band", tc.cidr, m, ip)
			}
			if prev, dup := seen[m]; dup {
				t.Fatalf("%s: %s and %s share minor %x", tc.cidr, prev, ip, m)
			}
			seen[m] = ip.String()
		}
	}
}

func TestMinorBoundaries(t *testing.T) {
	withNets(t, map[string][]string{"br": {"10.30.0.0/19"}})
	for ip, want := range map[string]int{
		"10.30.0.1":    0x1001,
		"10.30.15.255": 0x1fff, // host index 4095: last of the original band
		"10.30.16.0":   0x3000, // host index 4096: first of the extension band
		"10.30.31.254": 0x3000 + 8190 - 4096,
	} {
		got, ok := MinorFor("br", net.ParseIP(ip))
		if !ok || got != want {
			t.Errorf("%s: minor %x ok=%v, want %x", ip, got, ok, want)
		}
	}
	// the widest supported subnet ends below 0xffff
	withNets(t, map[string][]string{"br": {"10.30.128.0/17"}})
	if m, ok := MinorFor("br", net.ParseIP("10.30.255.254")); !ok || m > ExtMinorMax || m != 0x3000+32766-4096 {
		t.Errorf("/17 last host: minor %x ok=%v", m, ok)
	}
}

// A bridge with no address (or an address outside /17../19 for the session) applies the original rule: nothing changes for it.
func TestUnknownGeometryFallsBackToTheOriginalRule(t *testing.T) {
	withNets(t, map[string][]string{"br-none": nil, "br-wide": {"10.0.0.0/8"}})
	ip := net.ParseIP("10.20.33.77")
	want, _ := MinorForIP(ip)
	for _, b := range []string{"br-none", "br-wide", "br-unknown"} {
		if got, ok := MinorFor(b, ip); !ok || got != want {
			t.Errorf("%s: %x ok=%v want %x", b, got, ok, want)
		}
	}
	if _, ok := MinorFor("br-none", net.ParseIP("::1")); ok {
		t.Error("an IPv6 address must not be shapeable")
	}
}

func TestIsGuestMinor(t *testing.T) {
	for m, want := range map[int]bool{0x1: false, 0xfff: false, 0x1000: true, 0x1fff: true, 0x2000: false, 0x2fff: false, 0x3000: true, 0xa7ff: true, 0xffff: true, 0x10000: false} {
		if IsGuestMinor(m) != want {
			t.Errorf("IsGuestMinor(%x) = %v", m, !want)
		}
	}
}

// An address in two subnets of the bridge takes the narrower, as routing does.
func TestOverlappingBridgeSubnetsPickTheNarrower(t *testing.T) {
	withNets(t, map[string][]string{"br": {"10.30.0.0/19", "10.30.20.0/24"}})
	got, _ := MinorFor("br", net.ParseIP("10.30.20.9"))
	want, _ := MinorForIP(net.ParseIP("10.30.20.9"))
	if got != want {
		t.Errorf("minor %x, want the /24 rule %x", got, want)
	}
}

// A batch for a wide bridge names classes in both bands and names every filter by the same minor.
func TestWideBridgeBatchUsesBothBands(t *testing.T) {
	withNets(t, map[string][]string{"br-g301": {"10.30.0.0/19"}})
	r := &tcRecorder{}
	c := r.client()
	ips := []net.IP{net.ParseIP("10.30.0.5"), net.ParseIP("10.30.20.5")}
	var items []PrepItem
	for _, ip := range ips {
		items = append(items, PrepItem{IP: ip, DownKbps: 1000, UpKbps: 1000})
	}
	if err := c.PrepareBatch(context.Background(), "br-g301", items); err != nil {
		t.Fatal(err)
	}
	if err := c.ActivateBatch(context.Background(), "br-g301", ips); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.scripts, "\n")
	// 10.30.20.5 is host index 0x1405 = 5125 -> 0x3000 + (5125 - 4096) = 0x3405
	for _, want := range []string{"classid 1:1005 htb", "classid 1:3405 htb", "handle 0x3405 flower dst_ip 10.30.20.5/32 classid 1:3405", "handle 0x3405 flower src_ip 10.30.20.5/32 classid 1:3405"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
}
