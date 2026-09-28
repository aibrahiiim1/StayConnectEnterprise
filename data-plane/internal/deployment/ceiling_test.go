package deployment

import (
	"testing"

	lic "github.com/stayconnect/enterprise/license"
)

// Identity add-ons have no optional code path: WhatsApp is deployed exactly when SMS is, even on a ceiling
// that allows nothing optional.
func TestWhatsAppIsDeployedExactlyWhenSMSIs(t *testing.T) {
	var zero Ceiling
	if zero.ModuleDeployed(lic.ModuleWhatsAppOTP) != zero.ModuleDeployed(lic.ModuleSMSOTP) || !zero.ModuleDeployed(lic.ModuleWhatsAppOTP) {
		t.Fatal("whatsapp_otp deployment differs from sms_otp")
	}
	if zero.ModuleDeployed("no_such_module") {
		t.Fatal("unknown module deployed")
	}
}
