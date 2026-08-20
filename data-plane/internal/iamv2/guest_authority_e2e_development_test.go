//go:build !stayconnect_production

package iamv2

import "testing"

// The DEVELOPMENT build must still load the accepted all-off dark default. This is the counterpart of the
// production end-to-end test: whichever way the tree is compiled, one of the two runs.
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
