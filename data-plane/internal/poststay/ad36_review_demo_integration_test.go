//go:build integration && phase5

package poststay

// Demonstrations for docs/reviews/AD36_NEW_DEVICE_IDENTITY_REVIEW.md. They pin CURRENT behaviour (including limits);
// they change no semantics and use no PMS traffic.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
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

// Wrong answers are bounded per device (5 then 15-minute block) and per IP (20), but each NEW device identity starts a
// fresh budget: rotating device identities from different addresses multiplies the guesses.
func TestIntegration_AD36Review_BudgetIsPerDeviceAndIP(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	s := storeAtFixedTime(t, p, time.Now().Add(-520*time.Minute))
	ctx := context.Background()
	evaluated, throttled := 0, 0
	for d := 0; d < 6; d++ {
		dev := newDevice(t, p, f, "02:00:00:00:b0:0"+string(rune('0'+d)))
		ip := fmt.Sprintf("10.%d.1.%d", time.Now().UnixNano()%250, d+1)
		for i := 0; i < 7; i++ {
			r := VerifyRequest{Tenant: f.tenant, Site: f.site, Device: dev, IP: ip, Room: "101", Reservation: "G" + string(rune('a'+i)), LastName: "LOVELACE"}
			_, err := s.Verify(ctx, r)
			switch {
			case errors.Is(err, ErrThrottled):
				throttled++
			case errors.Is(err, ErrNotAuthenticable):
				evaluated++
			default:
				t.Fatalf("unexpected %v", err)
			}
		}
	}
	t.Logf("6 device identities on 6 addresses, 7 tries each: evaluated=%d throttled=%d", evaluated, throttled)
	if evaluated != 30 {
		t.Fatalf("evaluated=%d, want 6 devices x 5", evaluated)
	}
	// One address, many device identities: the IP bucket (20) bounds them together.
	ev2 := 0
	shared := fmt.Sprintf("10.%d.2.9", time.Now().UnixNano()%250) // buckets are keyed by address only: a fresh one per run
	for d := 0; d < 6; d++ {
		dev := newDevice(t, p, f, "02:00:00:00:c0:0"+string(rune('0'+d)))
		for i := 0; i < 5; i++ {
			_, err := s.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Device: dev, IP: shared, Room: "101", Reservation: "X" + string(rune('a'+i)), LastName: "LOVELACE"})
			if errors.Is(err, ErrNotAuthenticable) {
				ev2++
			}
		}
	}
	t.Logf("6 device identities, one address, 5 tries each: evaluated=%d", ev2)
	if ev2 != 20 {
		t.Fatalf("evaluated=%d, want the IP budget of 20", ev2)
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

// After the first device converts, the stay is POST_STAY_ACTIVE: a second (new) device still VERIFIES, but the
// conversion is refused (one conversion per episode), so it gets no access and no device slot.
func TestIntegration_AD36Review_SecondDeviceVerifiesButCannotConvert(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	s := storeAtFixedTime(t, p, time.Now().Add(-560*time.Minute))
	ac := authctx.NewStore(p)
	ctx := context.Background()
	// The original entitlement has ended by check-out (a live one per stay is unique); its lineage rows remain.
	if _, err := p.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition(id,'TERMINATED',now(),NULL) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay); err != nil {
		t.Fatalf("expire original: %v", err)
	}

	issue := func(dev string, r VerifyRequest) string {
		profile, err := s.Verify(ctx, r)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		id, err := ac.IssuePostStay(ctx, authctx.PostStayGrant{Tenant: f.tenant, Site: f.site, Profile: profile, Device: dev, GuestNetwork: f.network, TTLSeconds: 600})
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		return id
	}
	c1 := issue(f.device, req(f, f.device, "101", "R1", ""))
	out, err := s.Convert(ctx, ConvertRequest{Tenant: f.tenant, Site: f.site, Context: c1,
		Presenter: authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: f.device, GuestNetwork: f.network}, PackageRevision: f.pkg}, ac)
	if err != nil {
		t.Fatalf("first conversion: %v", err)
	}
	t.Logf("first conversion made a NEW entitlement %s (not the stay's original one)", out.Entitlement)

	second := newDevice(t, p, f, "02:00:00:00:e0:01")
	c2 := issue(second, req(f, second, "101", "R1", "LOVELACE"))
	_, err = s.Convert(ctx, ConvertRequest{Tenant: f.tenant, Site: f.site, Context: c2,
		Presenter: authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: second, GuestNetwork: f.network}, PackageRevision: f.pkg}, ac)
	if !errors.Is(err, ErrAlreadyConverted) {
		t.Fatalf("second device conversion: %v, want ErrAlreadyConverted", err)
	}
	var n int
	_ = p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE device_id=$1`, second).Scan(&n)
	if n != 0 {
		t.Fatalf("second device holds %d bindings", n)
	}
}
