//go:build integration && phase5

package poststay

// Demonstrations for docs/reviews/AD36_NEW_DEVICE_IDENTITY_REVIEW.md that are still true after the closure
// (docs/reviews/AD36_POST_STAY_CLOSURE.md). They use no PMS traffic.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A device that has history on one eligible stay has no new-device route: the right room, reservation and last name of
// ANOTHER stay are refused from it.
func TestIntegration_AD36Review_DeviceWithOtherHistoryHasNoNewDeviceRoute(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f) // f.device has history on stay R1
	other := addStay(t, f, f.iface, "R2", "CHECKED_OUT", "202", "TURING")
	_ = other
	s := storeAtFixedTime(t, p, time.Now().Add(-500*time.Minute))
	if _, err := s.Verify(context.Background(), req(f, f.device, "202", "R2", "TURING")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("got %v", err)
	}
	fresh := newDevice(t, p, f, "02:00:00:00:00:a1")
	if _, err := s.Verify(context.Background(), req(f, fresh, "202", "R2", "TURING")); err != nil {
		t.Fatalf("the same answers from a device with no history: %v", err)
	}
}

// The surname alone, or with only one of room/reservation, never finds a stay.
func TestIntegration_AD36Review_SurnameAloneNeverSucceeds(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	fresh := newDevice(t, p, f, "02:00:00:00:d0:01")
	s := storeAtFixedTime(t, p, time.Now().Add(-540*time.Minute))
	for name, r := range map[string]VerifyRequest{
		"name only":        req(f, fresh, "", "", "LOVELACE"),
		"name + room":      req(f, fresh, "101", "", "LOVELACE"),
		"name + res":       req(f, fresh, "", "R1", "LOVELACE"),
		"name + wrong res": req(f, fresh, "101", "R2", "LOVELACE"),
	} {
		if _, err := s.Verify(context.Background(), r); !errors.Is(err, ErrNotAuthenticable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The two demonstrations that used to follow (a rotating device identity multiplies the guesses; a second device verifies
// but cannot convert) pinned the gaps AD-36 closed. Their replacements are in ad36_closure_integration_test.go.
