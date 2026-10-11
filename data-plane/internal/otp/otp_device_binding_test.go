package otp

import (
	"context"
	"errors"
	"testing"
)

// A ONE-TIME CODE IS BOUND TO THE DEVICE THAT ASKED FOR IT (migration 0110, VerifyFrom). Needs OTP_TEST_DSN on
// a database that carries iam_v2.otp_device_bindings.
func TestACodeVerifiesOnlyFromTheDeviceItWasIssuedTo(t *testing.T) {
	db := otpDB(t)
	ring := testRing(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `DELETE FROM iam_v2.otp_device_bindings WHERE tenant_id = $1::uuid`, otpTenant); err != nil {
		t.Skipf("no device-binding table (apply 0110): %v", err)
	}
	iss, err := Issue(ctx, db, ring, IssueParams{TenantID: otpTenant, Channel: "email", Destination: "bound@example.com", DeviceMAC: "aa:bb:cc:dd:ee:01"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Another device, right code: refused before the code is compared, and no attempt is counted.
	if _, err := VerifyFrom(ctx, db, ring, iss.ChallengeID, iss.Code, "aa:bb:cc:dd:ee:02"); !errors.Is(err, ErrOtherDevice) {
		t.Fatalf("another device must be refused with ErrOtherDevice, got %v", err)
	}
	var attempts int
	if err := db.QueryRow(ctx, `SELECT attempts FROM auth_otps WHERE id = $1`, iss.ChallengeID).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("a refusal by device must count no attempt: attempts=%d err=%v", attempts, err)
	}
	// The issuing device, case-insensitively: verified, single-use.
	if _, err := VerifyFrom(ctx, db, ring, iss.ChallengeID, iss.Code, "AA:BB:CC:DD:EE:01"); err != nil {
		t.Fatalf("the issuing device must verify: %v", err)
	}
	if _, err := VerifyFrom(ctx, db, ring, iss.ChallengeID, iss.Code, "aa:bb:cc:dd:ee:01"); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatalf("single-use: %v", err)
	}
	// FAIL CLOSED: a challenge with no binding (issued before 0110, or by a caller with no device) is refused
	// from every device, right code or not, and counts no attempt; so is a verification that names no device.
	old, err := Issue(ctx, db, ring, IssueParams{TenantID: otpTenant, Channel: "email", Destination: "unbound@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyFrom(ctx, db, ring, old.ChallengeID, old.Code, "aa:bb:cc:dd:ee:09"); !errors.Is(err, ErrOtherDevice) {
		t.Fatalf("an unbound challenge must be refused: %v", err)
	}
	if _, err := VerifyFrom(ctx, db, ring, iss.ChallengeID, iss.Code, ""); !errors.Is(err, ErrOtherDevice) {
		t.Fatalf("a verification without a device must be refused: %v", err)
	}
}
