//go:build integration

package main

// GUEST SIGN-IN PROTECTION AT THE AUTHENTICATION PATH ITSELF.
//
// The policy's arithmetic is proved next door, against the SQL that performs it. What can only be proved
// here is what scd does with the answer: that a restricted device is refused BEFORE its evidence is looked
// at, that the refusal tells the guest the server's own remaining time, that the refusal is recorded, and
// that a technical failure never feeds the counter.
//
// THE ORDERING IS THE SECURITY PROPERTY. A gate asked after the evidence is evaluated is not a rate limit:
// the work is already done, the timing already differs, and the mirror has already been asked about a room.
// So these tests submit CORRECT details from a restricted device and require the refusal anyway.
//
// NO REAL GUEST DATA APPEARS ANYWHERE IN THIS FILE.

import (
	"context"
	"testing"
)

// restrictThisDevice drives the device to the threshold through the REAL handler, with genuinely wrong
// details — no row is written by hand, so what is under test includes the wiring that reports a failure.
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

// newRequestUUID gives each deliberate submission its own id, which is what the portal now does and what
// Delivery A2 exists to guarantee. Reusing one id here would test the replay path instead of this one.
func newRequestUUID(i int) string {
	const base = "00000068-0000-4000-8000-0000000000"
	return base + string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// A RESTRICTED DEVICE IS REFUSED EVEN WITH CORRECT DETAILS, AND IS TOLD HOW LONG TO WAIT.
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
	// The refusal happened before the evidence was evaluated, so the record says RATE_LIMITED and carries no
	// comparison: nothing matched because nothing was compared, and no stay was even counted as a candidate.
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

// EVERY BLOCKED SUBMISSION IS STILL RECORDED. An operator asking "why can this guest not get online" must
// find the refusals, not a silence — and RATE_LIMITED is the answer that stops them looking at spellings.
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

// A BLOCKED SUBMISSION DOES NOT FEED THE COUNTER. RATE_LIMITED is not a wrong credential, so a guest tapping
// Connect during their wait cannot extend it — which is the behaviour a waiting guest actually exhibits.
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

// A SUCCESSFUL SIGN-IN CLEARS THE COUNTER, through the real handler and the real grant path.
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

	// Four more wrong submissions would restrict a device whose counter had NOT been cleared.
	for i := 0; i < 4; i++ {
		_, r := post(t, f.p3.resolveHandler, f.resolveBody("412", "WrongAgain", "", newRequestUUID(50+i)))
		if r.FailureClass == "RATE_LIMITED" {
			t.Fatalf("submission %d was refused for waiting; the success did not clear the counter", i+1)
		}
	}
}

// THE DEVICE, NOT THE ROOM. A restricted device stays restricted whatever room it types next, and its
// restriction reaches nobody else.
func TestIntegration_Protection_TheRestrictionFollowsTheDeviceNotTheRoom(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)

	restrictThisDevice(t, f)

	// A different room from the same device: still refused, and refused for waiting rather than for the room.
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("777", "Okonkwo", "", newRequestUUID(60)))
	if res.FailureClass != "RATE_LIMITED" {
		t.Fatalf("changing the room escaped the restriction: failure_class=%q", res.FailureClass)
	}

	// A different device on the same guest network is unaffected, so the guest who lives in 412 can sign in.
	body := f.resolveBody("412", "Okonkwo", "", newRequestUUID(61))
	body["device"] = map[string]string{"ip": f.net.otherIP, "mac": "02:00:00:cc:dd:ee"}
	_, other := post(t, f.p3.resolveHandler, body)
	if other.FailureClass == "RATE_LIMITED" {
		t.Fatal("a second device was refused for the first device's behaviour")
	}
}
