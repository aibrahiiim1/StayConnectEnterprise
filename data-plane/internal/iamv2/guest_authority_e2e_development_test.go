//go:build !stayconnect_production

package iamv2

import "testing"

// The development build keeps the all-off dark default (counterpart of the production e2e test).
func TestDevelopmentBuildLoaderKeepsTheDarkDefault(t *testing.T) {
	if productionBuild || BuildProfile() != "development" {
		t.Fatal("this file is built only WITHOUT -tags stayconnect_production")
	}
	c, err := LoadConfigFromEnv(env(nil), true)
	if err != nil {
		t.Fatalf("development build must accept the all-off default: %v", err)
	}
	for _, m := range []Method{MethodVoucher, MethodAccount, MethodOTP, MethodSocial} {
		if c.Enabled(m) {
			t.Errorf("%s must default OFF on a development build", m)
		}
	}
}
