package payment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
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

// Engine is the payment runtime. Its single exported constructor prevents an independently configured money sender.
type Engine struct {
	cfg      Config
	pool     *pgxpool.Pool
	provider Provider
	granter  Granter
	guard    *providerGuard

	// outcomePool holds sc_payment_outcome, the only connection that may assert what a provider said, so one stolen
	// execution credential cannot fabricate money. nil fails closed: no outcome is applied. This is the layer under
	// provider authentication, which stays in AuthenticateNotification.
	outcomePool *pgxpool.Pool
}

// NewProductionEngine is the only exported constructor; posture comes from the environment, the provider from the
// internal factory.
func NewProductionEngine(pool *pgxpool.Pool, granter Granter) (*Engine, error) {
	cfg, err := LoadConfigFromEnv(osGetenv)
	if err != nil {
		return nil, err
	}
	p, err := productionProvider(cfg)
	if err != nil {
		return nil, err
	}
	e := newEngine(cfg, pool, p, granter)
	// A missing outcome DSN is not a startup error, but every outcome application then fails closed.
	if dsn := osGetenv(EnvOutcomeDSN); dsn != "" {
		op, oerr := pgxpool.New(context.Background(), dsn)
		if oerr != nil {
			return nil, fail(ErrConfig, "the payment outcome credential could not be opened")
		}
		e.outcomePool = op
	}
	return e, nil
}

func newEngine(cfg Config, pool *pgxpool.Pool, p Provider, g Granter) *Engine {
	return &Engine{cfg: cfg, pool: pool, provider: p, granter: g, guard: &providerGuard{cfg: cfg, inner: p}}
}

func (e *Engine) Config() Config { return e.cfg }

func (e *Engine) ProviderRefusals() int { return e.guard.refusals() }

// Intent is the durable local record of an intention to move money, written before any provider call.
type Intent struct {
	ID                string
	ClientRef         string
	AmountMinor       int64
	Currency          string
	Exponent          int16
	Provider          string
	MerchantAccountID string
}

// CreateChargeIntent takes only a settlement and an idempotency key: amount, currency and exponent come from the pinned
// Purchase, provider and merchant account from p4_resolve_payment_account. Resolution fails closed: a site without an
// ACTIVE default account cannot create an intent.
func (e *Engine) CreateChargeIntent(ctx context.Context, tenantID, siteID, settlementID, idempotencyKey string) (Intent, error) {
	if !e.cfg.DomainOn() {
		return Intent{}, fail(ErrConfig, "the phase-4 payment domain is disabled")
	}
	acct, err := e.resolveAccount(ctx, tenantID, siteID)
	if err != nil {
		return Intent{}, err
	}
	// The configured provider must be reachable by this build; check before writing a durable intent.
	if e.provider != nil && e.provider.Name() != acct.Provider {
		return Intent{}, fail(ErrConfig,
			"this build's payment adapter does not match the provider configured for this site")
	}
	ref, err := newClientRef()
	if err != nil {
		return Intent{}, err
	}
	var in Intent
	in.ClientRef = ref
	in.Provider = acct.Provider
	in.MerchantAccountID = acct.ID
	err = e.pool.QueryRow(ctx, `
INSERT INTO iam_v2.payment_transactions
  (tenant_id, site_id, settlement_id, merchant_account_id, transaction_type, provider, provider_ref,
   idempotency_key, amount_minor, currency, currency_exponent, status)
SELECT $1, $2, $3, $4, 'CHARGE', $5, $6, $7, pu.amount_minor, pu.currency, pu.currency_exponent, 'CREATED'
  FROM iam_v2.settlements se
  JOIN iam_v2.purchases pu ON pu.tenant_id = se.tenant_id AND pu.site_id = se.site_id AND pu.id = se.purchase_id
 WHERE se.tenant_id = $1 AND se.site_id = $2 AND se.id = $3
RETURNING id::text, amount_minor, currency, currency_exponent`,
		tenantID, siteID, settlementID, acct.ID, acct.Provider, ref, idempotencyKey).
		Scan(&in.ID, &in.AmountMinor, &in.Currency, &in.Exponent)
	if errors.Is(err, pgx.ErrNoRows) {
		return Intent{}, fail(ErrNotExecutable, "no such settlement in this tenant/site")
	}
	if err != nil {
		return Intent{}, classify(err)
	}
	return in, nil
}

// Account carries identifiers only; no credential passes through this type.
type Account struct {
	ID          string
	Provider    string
	MerchantRef string
}

// resolveAccount is the only way this package learns the provider and merchant account; it accepts no caller
// preference.
func (e *Engine) resolveAccount(ctx context.Context, tenantID, siteID string) (Account, error) {
	var a Account
	err := e.pool.QueryRow(ctx,
		`SELECT account_id::text, provider, merchant_account_ref FROM iam_v2.p4_resolve_payment_account($1,$2)`,
		tenantID, siteID).Scan(&a.ID, &a.Provider, &a.MerchantRef)
	if errors.Is(err, pgx.ErrNoRows) || err != nil {
		if errors.Is(err, pgx.ErrNoRows) || strings.Contains(err.Error(), "PAYMENT_NO_CONFIGURED_ACCOUNT") {
			return Account{}, fail(ErrNoAccount,
				"this site has no ACTIVE default payment account; online payment cannot be attempted")
		}
		return Account{}, classify(err)
	}
	return a, nil
}

// Only the amount is caller-supplied (partial refunds are operator decisions); the database bounds the cumulative total
// under an advisory lock. Provider and merchant account are inherited from the parent: money returns the way it
// arrived.
func (e *Engine) CreateRefundIntent(ctx context.Context, tenantID, siteID, parentID string, amountMinor int64, idempotencyKey string) (Intent, error) {
	if !e.cfg.DomainOn() {
		return Intent{}, fail(ErrConfig, "the phase-4 payment domain is disabled")
	}
	ref, err := newClientRef()
	if err != nil {
		return Intent{}, err
	}
	var in Intent
	in.ClientRef = ref
	err = e.pool.QueryRow(ctx, `
INSERT INTO iam_v2.payment_transactions
  (tenant_id, site_id, settlement_id, merchant_account_id, transaction_type, parent_transaction_id,
   provider, provider_ref, idempotency_key, amount_minor, currency, currency_exponent, status)
SELECT p.tenant_id, p.site_id, p.settlement_id, p.merchant_account_id, 'REFUND', p.id,
       p.provider, $4, $5, $6, p.currency, p.currency_exponent, 'CREATED'
  FROM iam_v2.payment_transactions p
 WHERE p.tenant_id = $1 AND p.site_id = $2 AND p.id = $3
RETURNING id::text, amount_minor, currency, currency_exponent`,
		tenantID, siteID, parentID, ref, idempotencyKey, amountMinor).
		Scan(&in.ID, &in.AmountMinor, &in.Currency, &in.Exponent)
	if errors.Is(err, pgx.ErrNoRows) {
		return Intent{}, fail(ErrNotExecutable, "no such parent transaction in this tenant/site")
	}
	if err != nil {
		return Intent{}, classify(err)
	}
	return in, nil
}

type ExecuteResult struct {
	Outcome        Outcome
	Applied        string // APPLIED | DUPLICATE | NOOP | "" when nothing was applied
	EntitlementID  string
	AlreadyGranted bool
}

// Order is the safety argument: (1) begin_payment_execution moves settlement REQUIRED->IN_PROGRESS and intent
// CREATED->PENDING in one tx before any provider is contacted; (2) provider.Execute behind the DARK guard; (3)
// apply_payment_callback_v2 applies the outcome with the settlement transition in one tx; (4) grant only on a settled
// charge. An UNKNOWN outcome stops at step 3 in MANUAL_REVIEW and is never retried.
func (e *Engine) Execute(ctx context.Context, tenantID, siteID, txnID string) (ExecuteResult, error) {
	var out ExecuteResult
	if !e.cfg.DomainOn() {
		return out, fail(ErrConfig, "the phase-4 payment domain is disabled")
	}

	// Preflight reads configuration only, so a refusal is provably NOT_SENT and leaves the intent CREATED and the
	// settlement REQUIRED, both retriable. Doing it after the durable transition left intents stuck PENDING,
	// indistinguishable from an outstanding provider call.
	if err := e.guard.preflight(); err != nil {
		out.Outcome = OutcomeNotSent
		return out, err
	}

	var began string
	if err := e.pool.QueryRow(ctx, `SELECT iam_v2.begin_payment_execution($1)`, txnID).Scan(&began); err != nil {
		return out, classify(err)
	}

	var r Request
	var provider, merchant, settlementID, kind string
	var parentRef *string
	if err := e.pool.QueryRow(ctx, `
SELECT t.provider, t.merchant_account_id::text, t.settlement_id::text, t.transaction_type,
       t.provider_ref, t.amount_minor, t.currency, t.currency_exponent,
       (SELECT p.provider_ref FROM iam_v2.payment_transactions p WHERE p.id = t.parent_transaction_id)
  FROM iam_v2.payment_transactions t
 WHERE t.tenant_id = $1 AND t.site_id = $2 AND t.id = $3`, tenantID, siteID, txnID).
		Scan(&provider, &merchant, &settlementID, &kind, &r.ClientRef, &r.AmountMinor, &r.Currency,
			&r.Exponent, &parentRef); err != nil {
		return out, classify(err)
	}
	r.MerchantAccount, r.Kind = merchant, kind
	if parentRef != nil {
		r.ParentRef = *parentRef
	}

	res, perr := e.guard.execute(ctx, r)
	if perr != nil {
		// Reaching here means the posture changed since preflight or the row has no client reference; nothing was sent,
		// so nothing is recorded. An adapter that was entered and failed returns OutcomeUnknown, never an error.
		out.Outcome = OutcomeNotSent
		return out, perr
	}

	// A synchronous answer to our own request is trusted because we know which intent it belongs to; it still goes
	// through applyTrusted so a provider outcome becomes financial fact in exactly one place.
	return e.applyTrusted(ctx, trustedNotification{
		clientRef: r.ClientRef, eventID: "exec:" + r.ClientRef, eventType: "execution_result",
		outcome: res.Outcome, providerTxnRef: res.ProviderTxnRef, reasonCode: res.ReasonCode,
	})
}

// HandleProviderNotification is the only way an out-of-band outcome enters the financial record. The raw delivery goes
// to the adapter, which verifies the signature with its own secret; only this function mints the unexported
// trustedNotification, so no caller can fabricate a CAPTURED (client references are not secrets). Tenant and site come
// from the durable row, never from the caller.
func (e *Engine) HandleProviderNotification(ctx context.Context, raw RawNotification) (ExecuteResult, error) {
	var out ExecuteResult
	if !e.cfg.DomainOn() {
		return out, fail(ErrConfig, "the phase-4 payment domain is disabled")
	}
	if e.provider == nil {
		return out, fail(ErrUntrusted,
			"no payment provider adapter is configured, so no notification can be authenticated")
	}
	auth, ok := e.provider.(NotificationAuthenticator)
	if !ok {
		return out, fail(ErrUntrusted,
			"the configured adapter cannot authenticate provider notifications; an unauthenticated "+
				"financial outcome is refused")
	}
	parsed, err := auth.AuthenticateNotification(ctx, raw)
	if err != nil {
		// Deliberately not the adapter's own words: a verification failure message is a probing oracle.
		return out, fail(ErrUntrusted, "the provider notification did not authenticate")
	}
	return e.applyTrusted(ctx, trustedNotification{
		clientRef: parsed.ClientRef, eventID: parsed.ProviderEventID, eventType: parsed.EventType,
		outcome: parsed.Outcome, providerTxnRef: parsed.ProviderTxnRef, reasonCode: parsed.ReasonCode,
	})
}

// trustedNotification is minted only after an adapter authenticated a delivery, or by Execute for its own synchronous
// call; it has no exported constructor.
type trustedNotification struct {
	clientRef      string
	eventID        string
	eventType      string
	outcome        Outcome
	providerTxnRef string
	reasonCode     string
}

// NOT_SENT has no mapping: nothing was sent, so nothing is asserted.
func (n trustedNotification) assertedStatus() string {
	switch n.outcome {
	case OutcomeCaptured:
		return "CAPTURED"
	case OutcomeDeclined:
		return "FAILED"
	case OutcomeUnknown:
		return "UNKNOWN"
	}
	return ""
}

func (e *Engine) applyTrusted(ctx context.Context, n trustedNotification) (ExecuteResult, error) {
	var out ExecuteResult
	if n.clientRef == "" || n.eventID == "" {
		return out, fail(ErrUncorrelated,
			"a notification without a client reference and a provider event id cannot be correlated or "+
				"deduplicated")
	}
	// Ownership is read from the durable row the client reference names, so tenant and site are unforgeable.
	var tenantID, siteID, settlementID, kind, provider, merchant string
	err := e.pool.QueryRow(ctx, `
SELECT tenant_id::text, site_id::text, settlement_id::text, transaction_type, provider,
       merchant_account_id::text
  FROM iam_v2.payment_transactions WHERE provider_ref = $1`, n.clientRef).
		Scan(&tenantID, &siteID, &settlementID, &kind, &provider, &merchant)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fail(ErrUncorrelated, "no payment intent matches this client reference")
	}
	if err != nil {
		return out, classify(err)
	}

	evidence, _ := json.Marshal(map[string]string{
		"provider_status": string(n.outcome), "provider_reason_code": n.reasonCode})
	// Uses the narrowed operation through the separate outcome credential: asserting what a provider said is a
	// different authority from executing a payment.
	_, _ = provider, merchant
	if e.outcomePool == nil {
		return out, fail(ErrUntrusted,
			"no payment outcome credential is configured; a provider outcome cannot be applied")
	}
	var applied string
	if err := e.outcomePool.QueryRow(ctx,
		`SELECT iam_v2.p4_apply_provider_outcome($1,$2,$3,$4,$5,$6::jsonb)`,
		n.clientRef, n.eventID, n.eventType, n.assertedStatus(),
		n.providerTxnRef, string(evidence)).Scan(&applied); err != nil {
		return out, classify(err)
	}
	out.Applied = applied
	out.Outcome = n.outcome

	if n.outcome == OutcomeUnknown {
		return out, fail(ErrProviderUnknown,
			"the provider outcome could not be determined; the settlement is in manual review and nothing "+
				"will be retried automatically")
	}
	if n.outcome != OutcomeCaptured || kind != "CHARGE" {
		return out, nil
	}
	g, err := e.grantForSettlement(ctx, tenantID, siteID, settlementID)
	if err != nil {
		return out, err
	}
	out.EntitlementID, out.AlreadyGranted = g.EntitlementID, g.AlreadyGranted
	return out, nil
}

// grantForSettlement grants only when the settlement is durably SETTLED.
func (e *Engine) grantForSettlement(ctx context.Context, tenantID, siteID, settlementID string) (GrantOutcome, error) {
	if e.granter == nil {
		return GrantOutcome{}, fail(ErrGrant, "no entitlement granter is configured")
	}
	// No status pre-check: the operation re-reads and locks the settlement, so a check here would be racy.
	g, err := e.granter.GrantSettledSettlement(ctx, tenantID, siteID, settlementID)
	if err != nil {
		// A settlement that is not SETTLED (reversed, in review) is not an error: granting would be the bug.
		if CodeOf(err) == ErrNotExecutable {
			return GrantOutcome{}, nil
		}
		return GrantOutcome{}, fail(ErrGrant, "the paid grant refused: "+err.Error())
	}
	return g, nil
}

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
