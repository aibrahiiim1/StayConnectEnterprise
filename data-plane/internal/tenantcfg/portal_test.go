package tenantcfg

import "testing"

func TestRememberDeviceDaysIsDefaultedAndBounded(t *testing.T) {
	var none *AuthMethods
	if none.RememberDeviceDays() != DefaultRememberDeviceDays {
		t.Fatal("nil config must default")
	}
	if (&AuthMethods{}).RememberDeviceDays() != 30 {
		t.Fatal("absent portal block must default to 30")
	}
	for in, want := range map[int]int{0: 0, 7: 7, 365: 365, 400: 365, -3: 0} {
		d := in
		if got := (&AuthMethods{Portal: &PortalConfig{RememberDeviceDays: &d}}).RememberDeviceDays(); got != want {
			t.Errorf("%d -> %d, want %d", in, got, want)
		}
	}
}

// A switch stored before the owner ran the 0117 conversion still answers: "email" reads as "email_account" with
// the same enabled state, and only when the new key is absent.
func TestALeftoverEmailSwitchReadsAsEmailAccounts(t *testing.T) {
	cases := []struct {
		raw     string
		present bool
		enabled bool
		primary string
	}{
		{`{"email":{"enabled":true,"temporary_access":{"minutes":10}},"portal":{"primary_method":"email"}}`, true, true, "email_account"},
		{`{"email":{"enabled":false}}`, true, false, ""},
		{`{"email":{"enabled":true},"email_account":{"enabled":false}}`, true, false, ""},
		{`{"email_account":{"enabled":true},"portal":{"primary_method":"sms"}}`, true, true, "sms"},
		{`{"voucher":{"enabled":true}}`, false, false, ""},
	}
	for _, c := range cases {
		cfg, err := Parse([]byte(c.raw))
		if err != nil {
			t.Fatal(err)
		}
		if (cfg.EmailAccount != nil) != c.present || (cfg.EmailAccount != nil && cfg.EmailAccount.Enabled != c.enabled) {
			t.Errorf("%s: email_account = %+v", c.raw, cfg.EmailAccount)
		}
		if c.primary != "" && cfg.Portal.PrimaryMethod != c.primary {
			t.Errorf("%s: primary = %q", c.raw, cfg.Portal.PrimaryMethod)
		}
	}
}
