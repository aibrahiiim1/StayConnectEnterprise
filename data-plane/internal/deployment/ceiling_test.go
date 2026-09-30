package deployment

import (
	"testing"

	lic "github.com/stayconnect/enterprise/license"
)

// The identity add-ons are deployed exactly when IAM-v2 accepts their kind of identity: WhatsApp with SMS and
// Email (a one-time code), Social on its own. A ceiling that allows nothing optional deploys none of them.
func TestIdentityModulesFollowTheirIAMv2Method(t *testing.T) {
	var zero Ceiling
	for _, m := range []string{lic.ModuleEmailOTP, lic.ModuleSMSOTP, lic.ModuleWhatsAppOTP, lic.ModuleSocialLogin} {
		if zero.ModuleDeployed(m) {
			t.Fatalf("%s deployed on a zero ceiling", m)
		}
	}
	otp := zero.WithIdentity(true, false)
	if !otp.ModuleDeployed(lic.ModuleWhatsAppOTP) || otp.ModuleDeployed(lic.ModuleWhatsAppOTP) != otp.ModuleDeployed(lic.ModuleSMSOTP) ||
		!otp.ModuleDeployed(lic.ModuleEmailOTP) || otp.ModuleDeployed(lic.ModuleSocialLogin) {
		t.Fatal("OTP method did not deploy exactly the code modules")
	}
	if !zero.WithIdentity(false, true).ModuleDeployed(lic.ModuleSocialLogin) {
		t.Fatal("social method did not deploy social_login")
	}
	if zero.ModuleDeployed("no_such_module") {
		t.Fatal("unknown module deployed")
	}
}

func TestIdentityFlagsNeedTheMaster(t *testing.T) {
	env := map[string]string{"STAYCONNECT_IAMV2_OTP": "true", "STAYCONNECT_IAMV2_SOCIAL": "true"}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.ModuleDeployed(lic.ModuleSMSOTP) || c.ModuleDeployed(lic.ModuleSocialLogin) {
		t.Fatal("identity deployed with the IAM-v2 master off")
	}
	env["STAYCONNECT_IAMV2_MASTER"] = "true"
	if c, err = Load(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if !c.ModuleDeployed(lic.ModuleSMSOTP) || !c.ModuleDeployed(lic.ModuleSocialLogin) {
		t.Fatal("identity not deployed with master and methods on")
	}
	env["STAYCONNECT_IAMV2_OTP"] = "yes-please"
	if _, err = Load(func(k string) string { return env[k] }); err == nil {
		t.Fatal("malformed flag accepted")
	}
}
