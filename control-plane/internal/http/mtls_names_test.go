package http

import (
	"strings"
	"testing"
)

// The mTLS certificate's names used to be the literal "150.0.0.252". That made the appliance-facing
// endpoint un-moveable in the way that actually matters: configuration could name any FQDN, and TLS would
// still reject it. These pin the derivation, because getting it wrong breaks every appliance's transport at
// once and looks like a network fault rather than a configuration one.
func TestMTLSServerNamesComeFromConfiguration(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		extra   string
		want    []string
		wantNot []string
	}{
		{
			name: "the configured appliance endpoint, plus loopback",
			base: "https://sc-central.echofusion.com",
			want: []string{"sc-central.echofusion.com", "127.0.0.1"},
			// Nothing from any particular lab may survive in the certificate by default.
			wantNot: []string{"150.0.0.252"},
		},
		{
			name:    "a port on the endpoint is not part of the name",
			base:    "https://sc-central.echofusion.com:8443",
			want:    []string{"sc-central.echofusion.com"},
			wantNot: []string{"sc-central.echofusion.com:8443"},
		},
		{
			name:  "a transition keeps the old address valid alongside the new name",
			base:  "https://sc-central.echofusion.com",
			extra: "150.0.0.252, old-central.example.com",
			want:  []string{"sc-central.echofusion.com", "150.0.0.252", "old-central.example.com", "127.0.0.1"},
		},
		{
			// Unconfigured, the listener still has to serve its own health check.
			name: "no endpoint configured at all still yields loopback",
			base: "",
			want: []string{"127.0.0.1"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CTRLAPI_APPLIANCE_BASE", c.base)
			t.Setenv("CTRLAPI_MTLS_SANS", c.extra)
			got := mtlsServerNames()
			joined := strings.Join(got, ",")
			for _, w := range c.want {
				if !contains(got, w) {
					t.Fatalf("certificate must be valid for %q; got [%s]", w, joined)
				}
			}
			for _, w := range c.wantNot {
				if contains(got, w) {
					t.Fatalf("certificate must NOT be issued for %q; got [%s]", w, joined)
				}
			}
			// A duplicated SAN is not fatal but signals the dedupe broke.
			seen := map[string]bool{}
			for _, n := range got {
				if seen[n] {
					t.Fatalf("duplicate SAN %q in [%s]", n, joined)
				}
				seen[n] = true
			}
		})
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
