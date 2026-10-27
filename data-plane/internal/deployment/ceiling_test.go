package deployment

import (
	"testing"

	lic "github.com/stayconnect/enterprise/license"
)

func TestIdentityModulesFollowTheirIAMv2Method(t *testing.T) {
	var zero Ceiling
	for _, m := range []string{lic.ModuleEmailOTP, lic.ModuleSMSOTP, lic.ModuleWhatsAppOTP, lic.ModuleSocialLogin} {
		if zero.ModuleDeployed(m) {
			t.Fatalf("%s deployed on a zero ceiling", m)
		}
	}
	otp := zero.WithIdentity(true, false)
	if !otp.ModuleDeployed(lic.ModuleWhatsAppOTP) || otp.ModuleDeployed(lic.ModuleWhatsAppOTP) != otp.ModuleDeployed(lic.ModuleSMSOTP) ||
		otp.ModuleDeployed(lic.ModuleEmailOTP) || otp.ModuleDeployed(lic.ModuleSocialLogin) {
		t.Fatal("OTP method did not deploy exactly the code modules (email sign-in is no longer a code)")
	}
	acc := zero.WithAccounts(true)
	if !acc.ModuleDeployed(lic.ModuleEmailOTP) || acc.ModuleDeployed(lic.ModuleSMSOTP) || acc.ModuleDeployed(lic.ModuleSocialLogin) {
		t.Fatal("the ACCOUNT method must deploy email sign-in and only it")
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

// Card payment is configurable with its payment domain; calling a provider is a separate capability that the domain alone
// never grants, and LIVE needs both plus its own switch.
func TestCardPaymentConfigurableWithoutProviderCalls(t *testing.T) {
	base := map[string]string{"STAYCONNECT_PHASE2_MASTER": "true", "STAYCONNECT_PHASE2_PORTAL": "true",
		"STAYCONNECT_PHASE4_MASTER": "true", "STAYCONNECT_PHASE4_PAYMENT": "true"}
	c, err := Load(func(k string) string { return base[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !c.ModuleDeployed(lic.ModuleCardPayment) || !c.Available(CapCardPayment) {
		t.Fatal("the payment domain must make card payment configurable")
	}
	if c.Available(CapCardProviderCalls) || c.Available(CapCardLive) {
		t.Fatal("the payment domain alone must never permit a provider call")
	}
	base["STAYCONNECT_PHASE4_PAYMENT_PROVIDER"] = "true"
	c, err = Load(func(k string) string { return base[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !c.Available(CapCardProviderCalls) || c.Available(CapCardLive) {
		t.Fatal("the provider flag permits test-mode calls only; LIVE needs its own switch")
	}
}

// Room charge is configurable with its posting domain; real posting (transmit) stays a separate capability.
func TestRoomChargeConfigurableWithoutTransmit(t *testing.T) {
	env := map[string]string{"STAYCONNECT_PHASE3_MASTER": "true", "STAYCONNECT_PHASE4_MASTER": "true", "STAYCONNECT_PHASE4_POSTING": "true"}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !c.ModuleDeployed(lic.ModuleRoomCharge) {
		t.Fatal("the posting domain must make room charge configurable")
	}
	if c.Available(CapRoomChargeTransmit) || c.Available(CapRoomChargeReview) {
		t.Fatal("the posting domain alone must never permit transmission or the review surface")
	}
}

// A licence key with no implementation in this release is never reported as available.
func TestUnimplementedModulesAreNeverDeployed(t *testing.T) {
	c, err := Load(func(string) string { return "true" })
	if err != nil {
		c = Ceiling{}
	}
	for _, m := range []string{lic.ModuleHA, lic.ModuleWhiteLabel} {
		if c.ModuleDeployed(m) {
			t.Fatalf("%s reported deployed", m)
		}
	}
}
