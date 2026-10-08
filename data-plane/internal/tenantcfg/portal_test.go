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
