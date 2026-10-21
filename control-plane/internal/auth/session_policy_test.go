package auth

import (
	"errors"
	"testing"
	"time"
)

func TestValidateEndsSessionsWhenAccessEnds(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	sess := &Session{OperatorID: "op", Roles: []string{"platform_admin"}, IsSuperAdmin: true,
		CreatedAt: now.Add(-2 * time.Hour), LastSeenAt: now.Add(-time.Minute), ExpiresAt: now.Add(10 * time.Hour)}
	active := OperatorState{Found: true, Status: "active", Roles: []string{"platform_admin"}, MaxHours: 24, IdleMinutes: 30}
	reset := now.Add(-time.Hour)

	for _, c := range []struct {
		name string
		st   OperatorState
		at   time.Time
	}{
		{"removed", OperatorState{}, now},
		{"disabled", OperatorState{Found: true, Status: "disabled", MaxHours: 24}, now},
		{"access changed after sign-in", OperatorState{Found: true, Status: "active", SessionsNotBefore: &reset, MaxHours: 24}, now},
		{"absolute lifetime reached", OperatorState{Found: true, Status: "active", MaxHours: 1}, now},
		{"absolute lifetime reached at default cap", active, now.Add(23 * time.Hour)},
		{"idle past the limit", OperatorState{Found: true, Status: "active", MaxHours: 24, IdleMinutes: 30}, now.Add(31 * time.Minute)},
		{"idle past the default when the stored limit is out of range", OperatorState{Found: true, Status: "active", MaxHours: 24}, now.Add(31 * time.Minute)},
	} {
		if _, _, err := Validate(sess, c.st, c.at); !errors.Is(err, ErrSessionEnded) {
			t.Errorf("%s: session must end, got %v", c.name, err)
		}
	}
	if _, _, err := Validate(sess, active, now); err != nil {
		t.Fatalf("an active operator's young session must continue: %v", err)
	}
	// A sign-in AFTER the change is not caught by it.
	early := now.Add(-3 * time.Hour)
	if _, _, err := Validate(sess, OperatorState{Found: true, Status: "active", SessionsNotBefore: &early, MaxHours: 24}, now); err != nil {
		t.Fatalf("a session opened after sessions_not_before must continue: %v", err)
	}
}

// Roles are re-read: a demoted operator is authorised with the new role on the very next request.
func TestValidateUsesCurrentRoles(t *testing.T) {
	now := time.Now()
	sess := &Session{OperatorID: "op", Roles: []string{"platform_admin"}, IsSuperAdmin: true,
		CreatedAt: now.Add(-time.Minute), LastSeenAt: now.Add(-time.Minute), ExpiresAt: now.Add(12 * time.Hour)}
	fresh, _, err := Validate(sess, OperatorState{Found: true, Status: "active", Roles: []string{"platform_support"}, MaxHours: 24}, now)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.IsSuperAdmin || fresh.HasPermission(PermAppliancesManage) || !fresh.HasPermission(PermFleetView) {
		t.Fatalf("demoted session kept its old authority: %+v", fresh)
	}
	if sess.IsSuperAdmin != true || sess.Roles[0] != "platform_admin" {
		t.Fatal("Validate must not modify the stored session in place")
	}
	if !fresh.ExpiresAt.Equal(sess.CreatedAt.Add(12*time.Hour)) && fresh.ExpiresAt.After(sess.CreatedAt.Add(24*time.Hour)) {
		t.Fatal("expiry must never pass the absolute cap")
	}
}

// An out-of-range stored cap falls back to the default instead of disabling the cap.
func TestValidateOutOfRangeCapUsesDefault(t *testing.T) {
	now := time.Now()
	sess := &Session{OperatorID: "op", CreatedAt: now.Add(-13 * time.Hour), LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	if _, _, err := Validate(sess, OperatorState{Found: true, Status: "active", MaxHours: 0}, now); !errors.Is(err, ErrSessionEnded) {
		t.Fatal("a cap of 0 must mean the default (12 h), not unlimited")
	}
}
