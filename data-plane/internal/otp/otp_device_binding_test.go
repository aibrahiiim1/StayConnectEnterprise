package otp

import (
	"context"
	"errors"
	"testing"
)

// Needs OTP_TEST_DSN on a database with iam_v2.otp_device_bindings.
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
	if _, err := VerifyFrom(ctx, db, ring, iss.ChallengeID, iss.Code, "aa:bb:cc:dd:ee:02"); !errors.Is(err, ErrOtherDevice) {
		t.Fatalf("another device must be refused with ErrOtherDevice, got %v", err)
	}
	var attempts int
	if err := db.QueryRow(ctx, `SELECT attempts FROM auth_otps WHERE id = $1`, iss.ChallengeID).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("a refusal by device must count no attempt: attempts=%d err=%v", attempts, err)
	}
	if _, err := VerifyFrom(ctx, db, ring, iss.ChallengeID, iss.Code, "AA:BB:CC:DD:EE:01"); err != nil {
		t.Fatalf("the issuing device must verify: %v", err)
	}
	if _, err := VerifyFrom(ctx, db, ring, iss.ChallengeID, iss.Code, "aa:bb:cc:dd:ee:01"); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatalf("single-use: %v", err)
	}
	// A challenge with no binding, or a verification naming no device, is refused from every device and counts no
	// attempt.
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
