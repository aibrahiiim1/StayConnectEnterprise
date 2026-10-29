package payment

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type GrantOutcome struct {
	EntitlementID  string
	AlreadyGranted bool
	Superseded     string
}

// Granter is the only way this package creates guest access; there is no SQL here touching iam_v2.entitlements.
type Granter interface {
	// The parameter is a settlement, not a purchase: the database decides which purchase the money paid for.
	GrantSettledSettlement(ctx context.Context, tenantID, siteID, settlementID string) (GrantOutcome, error)
}

// Engine is the payment runtime's read and recovery surface (health, recovery, reversals). It cannot start a provider
// request or apply a provider outcome: the dormant execute and notification path was removed (audit AD-44). Card
// money enters only through CheckoutEngine and the narrowed database functions it calls.
type Engine struct {
	cfg     Config
	pool    *pgxpool.Pool
	granter Granter
}

// NewProductionEngine is the only exported constructor; posture comes from the environment.
func NewProductionEngine(pool *pgxpool.Pool, granter Granter) (*Engine, error) {
	cfg, err := LoadConfigFromEnv(osGetenv)
	if err != nil {
		return nil, err
	}
	// Provider egress is configured per site account for card checkout, never through this engine: a posture that
	// claims it is refused rather than half-honoured.
	if cfg.ProviderOn() {
		return nil, fail(ErrConfig,
			"provider execution is enabled but this build has no payment provider adapter; refusing to start")
	}
	return newEngine(cfg, pool, granter), nil
}

func newEngine(cfg Config, pool *pgxpool.Pool, g Granter) *Engine {
	return &Engine{cfg: cfg, pool: pool, granter: g}
}

func (e *Engine) Config() Config { return e.cfg }

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	m := err.Error()
	for _, c := range []struct {
		needle string
		code   Code
	}{
		{"CALLBACK_UNCORRELATED", ErrUncorrelated},
		{"PAYMENT_EXTERNAL_REF_CONFLICT", ErrRefConflict},
		{"PAYMENT_AMOUNT_NOT_SERVER_PINNED", ErrUntrustedInput},
		{"PAYMENT_SETTLEMENT_CLOSED", ErrNotExecutable},
		{"PAYMENT_NOT_EXECUTABLE", ErrNotExecutable},
		{"SETTLEMENT_NOT_EXECUTABLE", ErrNotExecutable},
		{"PAYMENT_STATUS_TRANSITION", ErrNotExecutable},
		{"PAYMENT_STATUS_TERMINAL", ErrNotExecutable},
		{"PAYMENT_REFUND_EXCEEDS_CHARGE", ErrNotExecutable},
		{"ptx_one_live_charge_per_settlement", ErrNotExecutable},
		// Audit P6 L5: a concurrent second checkout start for one settlement collides on the idempotency key; it
		// is "already executing", the same as the live-charge index, not a repository failure (a 500).
		{"payment_transactions_idempotency_key_key", ErrNotExecutable},
		{"CALLBACK_EVIDENCE_UNSAFE", ErrUntrustedInput},
		{"GRANT_NOT_SETTLED", ErrNotExecutable},
		{"GRANT_WRONG_RAIL", ErrNotExecutable},
		{"GRANT_PURCHASE_STATE", ErrNotExecutable},
		{"GRANT_SETTLEMENT_UNKNOWN", ErrNotExecutable},
		{"GRANT_EVIDENCE_MISSING", ErrNotExecutable},
		{"GRANT_SUBJECT_UNRESOLVED", ErrNotExecutable},
		{"PAYMENT_NOT_EXECUTING", ErrNotExecutable},
		{"PAYMENT_OUTCOME_INVALID", ErrUntrustedInput},
		{"FINANCIAL_ACTOR_UNKNOWN", ErrUntrustedInput},
		{"FINANCIAL_ACTOR_REQUIRED", ErrUntrustedInput},
		{"FINANCIAL_RECOVERY_MODE", ErrRecoveryHeld},
		{"RECOVERY_HOLDS_UNRESOLVED", ErrRecoveryHeld},
		{"RECOVERY_STATE_UNSAFE", ErrRecoveryHeld},
		{"RESTORE_GENERATION_NOT_ADVANCED", ErrNotExecutable},
		{"RESTORE_MANIFEST_REQUIRED", ErrUntrustedInput},
		{"RECOVERY_HOLD_ALREADY_RESOLVED", ErrNotExecutable},
		{"RECOVERY_NOTE_REQUIRED", ErrUntrustedInput},
		{"RECOVERY_REASON_REQUIRED", ErrUntrustedInput},
		{"RECOVERY_RESOLUTION_INVALID", ErrUntrustedInput},
		{"RECOVERY_ACTOR_REQUIRED", ErrUntrustedInput},
		// Operator-actionable refusals are returned in the database's own words (edged answers 400).
		{"RECOVERY_NOT_ACTIVE", ErrUntrustedInput},
		{"RECOVERY_HOLD_UNKNOWN", ErrUntrustedInput},
		{"PAYMENT_NO_CONFIGURED_ACCOUNT", ErrNoAccount},
		{"PAYMENT_ACCOUNT_NOT_ACTIVE", ErrNoAccount},
		{"PAYMENT_ACCOUNT_UNKNOWN", ErrNoAccount},
		{"PAYMENT_PROVIDER_MISMATCH", ErrConfig},
	} {
		if strings.Contains(m, c.needle) {
			return &Error{Code: c.code, Msg: c.needle}
		}
	}
	if i := strings.Index(m, "\n"); i >= 0 {
		m = m[:i]
	}
	return &Error{Code: ErrRepo, Msg: m}
}
