package otp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// AD-30: the cooldown and the hourly cap are counted per destination and device, with a higher destination-wide
// ceiling; the lifetime and the wrong-guess limit are the site's settings. Needs OTP_TEST_DSN.
func TestIssueLimitsAreCountedPerDestinationAndDevice(t *testing.T) {
	db := otpDB(t)
	ring := testRing(t)
	ctx := context.Background()
	const dest = "ad30@example.com"
	lim := Limits{DeviceHourlyCap: 2, DestinationHourlyCap: 3, Lifetime: 4 * time.Minute, MaxAttempts: 7}
	issue := func(mac string) (*Issued, error) {
		return Issue(ctx, db, ring, IssueParams{TenantID: otpTenant, Channel: "email", Destination: dest,
			IP: "10.0.9.1", DeviceMAC: mac, Limits: lim})
	}
	forgetCooldown := func() {
		if _, err := db.Exec(ctx, `UPDATE auth_otps SET issued_at = issued_at - interval '2 minutes' WHERE destination = $1`, dest); err != nil {
			t.Fatal(err)
		}
	}
	const devA, devB, devC = "aa:bb:cc:00:09:0a", "aa:bb:cc:00:09:0b", "aa:bb:cc:00:09:0c"

	first, err := issue(devA)
	if err != nil {
		t.Fatalf("first code: %v", err)
	}
	if got := time.Until(first.ExpiresAt); got > 4*time.Minute || got < 3*time.Minute {
		t.Fatalf("lifetime setting ignored: code expires in %v, want about 4m", got)
	}
	var max int
	if err := db.QueryRow(ctx, `SELECT max_attempts FROM auth_otps WHERE id = $1`, first.ChallengeID).Scan(&max); err != nil || max != 7 {
		t.Fatalf("max_attempts = %d (%v), want the setting 7", max, err)
	}
	// Another device is not held by device A's cooldown.
	if _, err := issue(devB); err != nil {
		t.Fatalf("device B right after device A: %v (a cooldown must not be shared across devices)", err)
	}
	if _, err := issue(devA); !errors.Is(err, ErrCooldown) {
		t.Fatalf("device A again at once: %v, want ErrCooldown", err)
	}
	forgetCooldown()
	if _, err := issue(devA); err != nil { // A's second
		t.Fatalf("device A second code: %v", err)
	}
	forgetCooldown()
	if _, err := issue(devA); !errors.Is(err, ErrHourlyCap) {
		t.Fatalf("device A third code: %v, want ErrHourlyCap (its cap is 2)", err)
	}
	// Device B has used 1 of its 2, but the destination has received 3 in all: the ceiling.
	if _, err := issue(devC); !errors.Is(err, ErrDestinationCap) {
		t.Fatalf("device C: %v, want ErrDestinationCap (ceiling 3)", err)
	}
}

func TestLimitsDefaultsAndOrdering(t *testing.T) {
	d := Limits{}.WithDefaults()
	if d.Lifetime != DefaultTTL || d.MaxAttempts != DefaultMaxAttempts || d.DeviceHourlyCap != IssueHourlyCap ||
		d.DestinationHourlyCap != DestinationHourlyCap || d.SourceHourlyCap != IPHourlyCap {
		t.Fatalf("defaults changed: %+v", d)
	}
	if d.DestinationHourlyCap < d.DeviceHourlyCap {
		t.Fatal("the destination ceiling must not be below the per-device cap")
	}
	if l := (Limits{DeviceHourlyCap: 9, DestinationHourlyCap: 4}).WithDefaults(); l.DestinationHourlyCap != 9 {
		t.Fatalf("ceiling below the device cap was not raised: %+v", l)
	}
}
