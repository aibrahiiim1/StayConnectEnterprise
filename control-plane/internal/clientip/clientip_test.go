package clientip

import "testing"

func TestResolve(t *testing.T) {
	cases := []struct {
		name, remote, xri, xff, want string
	}{
		{"direct client, no headers", "203.0.113.7:5555", "", "", "203.0.113.7"},
		{"direct client spoofing X-Real-IP is ignored", "203.0.113.7:5555", "1.2.3.4", "", "203.0.113.7"},
		{"direct client spoofing X-Forwarded-For is ignored", "203.0.113.7:5555", "", "1.2.3.4", "203.0.113.7"},
		{"caddy on loopback sets X-Real-IP", "127.0.0.1:40000", "198.51.100.9", "", "198.51.100.9"},
		{"caddy on IPv6 loopback", "[::1]:40000", "198.51.100.9", "", "198.51.100.9"},
		{"loopback: X-Real-IP wins over a spoofed first XFF hop", "127.0.0.1:1", "198.51.100.9", "6.6.6.6, 198.51.100.9", "198.51.100.9"},
		{"loopback without X-Real-IP uses the LAST XFF hop", "127.0.0.1:1", "", "6.6.6.6, 198.51.100.9", "198.51.100.9"},
		{"loopback with garbage headers falls back to the peer", "127.0.0.1:1", "not-an-ip", "also-not", "127.0.0.1"},
		{"loopback with nothing is local", "127.0.0.1:1", "", "", "127.0.0.1"},
		{"remote without port", "203.0.113.7", "1.1.1.1", "", "203.0.113.7"},
	}
	for _, c := range cases {
		if got := Resolve(c.remote, c.xri, c.xff); got != c.want {
			t.Errorf("%s: Resolve(%q,%q,%q)=%q want %q", c.name, c.remote, c.xri, c.xff, got, c.want)
		}
	}
}
