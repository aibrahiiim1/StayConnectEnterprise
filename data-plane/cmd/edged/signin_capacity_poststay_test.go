package main

import (
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/poststay"
)

// The console lists, validates and explains the post-stay throttle settings from signinCapacityFields; their bounds and
// defaults must be the ones the sign-in code enforces (internal/poststay/limits.go) and the database CHECKs.
func TestPostStayThrottleSettingsAreListedWithTheEnforcedBounds(t *testing.T) {
	want := map[string][3]int{
		"post_stay_device_attempts_per_minute":          {poststay.BoundDeviceAttempts.Min(), poststay.BoundDeviceAttempts.Max(), poststay.BoundDeviceAttempts.Default()},
		"post_stay_ip_attempts_per_minute":              {poststay.BoundIPAttempts.Min(), poststay.BoundIPAttempts.Max(), poststay.BoundIPAttempts.Default()},
		"post_stay_wrong_answers_before_lockout":        {poststay.BoundWrongAnswers.Min(), poststay.BoundWrongAnswers.Max(), poststay.BoundWrongAnswers.Default()},
		"post_stay_lockout_minutes":                     {poststay.BoundLockoutMinutes.Min(), poststay.BoundLockoutMinutes.Max(), poststay.BoundLockoutMinutes.Default()},
		"post_stay_new_device_room_attempts_per_minute": {poststay.BoundNewDeviceRoom.Min(), poststay.BoundNewDeviceRoom.Max(), poststay.BoundNewDeviceRoom.Default()},
		"post_stay_appliance_attempts_per_minute":       {poststay.BoundApplianceAttempts.Min(), poststay.BoundApplianceAttempts.Max(), poststay.BoundApplianceAttempts.Default()},
	}
	seen := 0
	for _, f := range signinCapacityFields {
		w, ok := want[f.Name]
		if !ok {
			continue
		}
		seen++
		if f.Min != w[0] || f.Max != w[1] || f.Default != w[2] {
			t.Errorf("%s: console %d..%d default %v, enforced %d..%d default %d", f.Name, f.Min, f.Max, f.Default, w[0], w[1], w[2])
		}
		if f.Unit == "" || len(f.Help) < 20 {
			t.Errorf("%s: a setting needs a unit and an explanation", f.Name)
		}
	}
	if seen != len(want) {
		t.Fatalf("%d of %d post-stay throttle settings are listed", seen, len(want))
	}
}
