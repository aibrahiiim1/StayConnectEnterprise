package payment

import (
	"errors"
	"testing"
)

// Audit P6 L5: a concurrent second checkout start hits the idempotency-key unique constraint; that is the
// same "already executing" refusal as the live-charge index, not ErrRepo.
func TestClassify_ConcurrentCheckoutStartIsNotExecutable(t *testing.T) {
	for _, msg := range []string{
		`ERROR: duplicate key value violates unique constraint "payment_transactions_idempotency_key_key" (SQLSTATE 23505)`,
		`ERROR: duplicate key value violates unique constraint "ptx_one_live_charge_per_settlement" (SQLSTATE 23505)`,
	} {
		if got := CodeOf(classify(errors.New(msg))); got != ErrNotExecutable {
			t.Fatalf("%s classified as %v, want ErrNotExecutable", msg, got)
		}
	}
}

// A refusal the operator can act on reaches the Recovery screen in the database's own words (edged answers 400
// for ErrUntrustedInput), not as an opaque "the operation failed". Both of these reached PRE-LIVE as a 500.
func TestClassify_RecoveryRefusalsAreActionable(t *testing.T) {
	for _, msg := range []string{
		"ERROR: RECOVERY_NOT_ACTIVE: this site is not in financial recovery (SQLSTATE P0001)",
		"ERROR: RECOVERY_HOLD_UNKNOWN: 00000000-0000-4000-8000-000000000001 (SQLSTATE P0002)",
	} {
		if got := CodeOf(classify(errors.New(msg))); got != ErrUntrustedInput {
			t.Fatalf("%s classified as %v, want ErrUntrustedInput", msg, got)
		}
	}
	if got := CodeOf(classify(errors.New("ERROR: RECOVERY_HOLDS_UNRESOLVED: 2 held item(s)"))); got != ErrRecoveryHeld {
		t.Fatalf("unresolved holds classified as %v", got)
	}
}
