package portaltls

import "testing"

// Audit Phase 1 CC2: the test DNS server is refused on a production Central, allowed elsewhere.
func TestChalltestsrvRefusedInProduction(t *testing.T) {
	o := Options{DNSProvider: ProviderChalltestsrv, ChalltestsrvURL: "http://127.0.0.1:8055"}
	if err := o.CheckEnv(true); err == nil {
		t.Fatal("challtestsrv must be refused in production")
	}
	if err := o.CheckEnv(false); err != nil {
		t.Fatalf("challtestsrv is allowed outside production: %v", err)
	}
	if err := (Options{DNSProvider: ProviderCloudflare}).CheckEnv(true); err != nil {
		t.Fatalf("cloudflare in production: %v", err)
	}
}
