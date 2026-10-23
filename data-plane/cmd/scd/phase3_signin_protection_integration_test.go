//go:build integration

package main

// Sign-in protection at the authentication path. The gate must run before the evidence is looked at (afterwards
// the work and timing already differ), so these submit correct details from a restricted device and require
// the refusal. No real guest data.

import (
	"context"
	"testing"
)

// restrictThisDevice drives the device to the threshold through the real handler; no row is written by hand.
func restrictThisDevice(t *testing.T, f *authFixture) {
	t.Helper()
	for i := 0; i < 5; i++ {
		_, res := post(t, f.p3.resolveHandler,
			f.resolveBody("412", "NotTheGuestsName", "", newRequestUUID(i)))
		if res.Outcome == outcomeVerified {
			t.Fatalf("submission %d verified against a wrong surname", i+1)
		}
	}
}

// newRequestUUID gives each submission its own id; reusing one would test the replay path instead.
func newRequestUUID(i int) string {
	const base = "00000068-0000-4000-8000-0000000000"
	return base + string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// A restricted device is refused even with correct details and told how long to wait.
func TestIntegration_Protection_CorrectDetailsAreRefusedWhileRestricted(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)

	restrictThisDevice(t, f)

	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", newRequestUUID(9)))
	if res.Outcome == outcomeVerified {
		t.Fatal("a restricted device verified; the gate is not being asked, or is asked too late")
	}
	if res.FailureClass != "RATE_LIMITED" {
		t.Fatalf("failure_class = %q, want RATE_LIMITED — a restricted guest told to re-check their details "+
			"will re-check details that are already correct", res.FailureClass)
	}
	if res.RetryAfterSeconds <= 0 || res.RetryAfterSeconds > 60 {
		t.Fatalf("retry_after_seconds = %d; the guest's countdown comes from the server or it is fiction",
			res.RetryAfterSeconds)
	}
	// Refused before evidence evaluation: RATE_LIMITED, no comparison, no candidate stays.
	a := lastAttempt(t, f)
	if a.Result != "RATE_LIMITED" {
		t.Fatalf("the blocked submission was recorded as %q", a.Result)
	}
	if a.MatchedField != "" {
		t.Fatalf("a submission refused before its evidence recorded a matched field: %+v", a)
	}
	if a.Eligible == nil || *a.Eligible != 0 {
		t.Fatalf("the mirror was searched for a submission that was refused before its evidence: %+v", a)
	}
}

// Every blocked submission is still recorded, so operators find RATE_LIMITED rather than a silence.
func TestIntegration_Protection_BlockedSubmissionsAreRecorded(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)

	restrictThisDevice(t, f)
	before := attemptCount(t, f)

	for i := 0; i < 3; i++ {
		post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", newRequestUUID(20+i)))
	}
	if got := attemptCount(t, f); got != before+3 {
		t.Fatalf("%d records for 3 blocked submissions", got-before)
	}
	var rateLimited int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.sign_in_attempts
		  WHERE tenant_id=$1 AND site_id=$2 AND result='RATE_LIMITED'`,
		f.tenant, f.site).Scan(&rateLimited); err != nil {
		t.Fatal(err)
	}
	if rateLimited != 3 {
		t.Fatalf("%d RATE_LIMITED records, want 3", rateLimited)
	}
}

// A blocked submission does not feed the counter: tapping Connect during the wait cannot extend it.
func TestIntegration_Protection_ABlockedSubmissionDoesNotCountAgainstTheGuest(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)

	restrictThisDevice(t, f)

	var before, after string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT expires_at::text FROM iam_v2.guest_signin_restrictions
		  WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site).Scan(&before); err != nil {
		t.Fatalf("no restriction was created: %v", err)
	}
	for i := 0; i < 5; i++ {
		post(t, f.p3.resolveHandler, f.resolveBody("412", "StillWrong", "", newRequestUUID(30+i)))
	}
	if err := f.pool.QueryRow(context.Background(),
		`SELECT expires_at::text FROM iam_v2.guest_signin_restrictions
		  WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("the wait moved from %s to %s while the guest was already waiting", before, after)
	}
}

// A successful sign-in clears the counter.
func TestIntegration_Protection_ASuccessfulSignInClearsTheCounter(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)

	for i := 0; i < 4; i++ {
		post(t, f.p3.resolveHandler, f.resolveBody("412", "NotTheGuestsName", "", newRequestUUID(40+i)))
	}
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", newRequestUUID(49)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("the correct surname did not verify after four mistakes: %+v", res)
	}

	// Four more wrong submissions would restrict a device whose counter was not cleared.
	for i := 0; i < 4; i++ {
		_, r := post(t, f.p3.resolveHandler, f.resolveBody("412", "WrongAgain", "", newRequestUUID(50+i)))
		if r.FailureClass == "RATE_LIMITED" {
			t.Fatalf("submission %d was refused for waiting; the success did not clear the counter", i+1)
		}
	}
}

// The device, not the room, is restricted; the restriction reaches nobody else.
func TestIntegration_Protection_TheRestrictionFollowsTheDeviceNotTheRoom(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)

	restrictThisDevice(t, f)

	// A different room from the same device is still refused, for waiting.
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("777", "Okonkwo", "", newRequestUUID(60)))
	if res.FailureClass != "RATE_LIMITED" {
		t.Fatalf("changing the room escaped the restriction: failure_class=%q", res.FailureClass)
	}

	// A different device on the same network is unaffected.
	body := f.resolveBody("412", "Okonkwo", "", newRequestUUID(61))
	body["device"] = map[string]string{"ip": f.net.otherIP, "mac": "02:00:00:cc:dd:ee"}
	_, other := post(t, f.p3.resolveHandler, body)
	if other.FailureClass == "RATE_LIMITED" {
		t.Fatal("a second device was refused for the first device's behaviour")
	}
}

// Network pause: changing the MAC defeats the device rule, so many wrong sign-ins on one network pause room
// sign-in there for every device, before evidence is looked at. A staff-confirmed device gets through.
func TestIntegration_Protection_NetworkPauseRefusesUntilStaffConfirmTheDevice(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)
	ctx := context.Background()

	var pause string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_signin_network_pauses
		(tenant_id, site_id, guest_network_id, guest_network_name, expires_at, failure_count)
		VALUES ($1,$2,$3::uuid,'test',now() + interval '15 minutes',30) RETURNING id::text`,
		f.tenant, f.site, f.network).Scan(&pause); err != nil {
		t.Fatal(err)
	}

	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", newRequestUUID(20)))
	if res.Outcome == outcomeVerified || res.FailureClass != "RATE_LIMITED" || res.RetryAfterSeconds <= 0 {
		t.Fatalf("during a network pause correct details = %+v, want RATE_LIMITED with a wait", res)
	}
	if a := lastAttempt(t, f); a.Result != "RATE_LIMITED" || a.MatchedField != "" {
		t.Fatalf("the paused submission was recorded as %+v", a)
	}

	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.guest_signin_confirm_device($1::uuid,$2::uuid,$3::uuid,$4::macaddr,'staff@hotel.test','guest at reception')`,
		f.tenant, f.site, pause, f.net.mac); err != nil {
		t.Fatal(err)
	}
	_, res = post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", newRequestUUID(21)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("a confirmed guest with correct details was refused: %+v", res)
	}
}
