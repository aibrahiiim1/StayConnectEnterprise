//go:build stayconnect_production

package iamv2

import "testing"

// End to end through the real loader, compiled the way a Production appliance is compiled. The unit tests
// prove the lock; this proves the shipped binary actually applies it.
func TestProductionBuildLoaderRefusesTheSupersededAuthority(t *testing.T) {
	if !productionBuild || BuildProfile() != "production" {
		t.Fatal("this file is built only under -tags stayconnect_production")
	}
	for _, e := range []map[string]string{
		{EnvMaster: "true", EnvVoucher: "false"},
		{EnvMaster: "true", EnvAccount: "0"},
		{EnvMaster: "false"},
		{},
	} {
		if _, err := LoadConfigFromEnv(env(e), true); err == nil {
			t.Errorf("production binary started with %v, which does not have IAM-v2 as the guest authority", e)
		}
	}
	c, err := LoadConfigFromEnv(env(map[string]string{EnvMaster: "true"}), true)
	if err != nil {
		t.Fatalf("production binary must start with the master switch on: %v", err)
	}
	if !c.Enabled(MethodVoucher) || !c.Enabled(MethodAccount) {
		t.Fatal("production binary did not come up with IAM-v2 as the guest authority")
	}
}
