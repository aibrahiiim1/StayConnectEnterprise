package payment

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Test-only driver (audit AD-44). The production Engine no longer starts provider requests or applies provider
// outcomes: that dormant path was removed. The database functions it called (begin_payment_execution,
// p4_apply_provider_outcome, p4_grant_paid_entitlement) still exist and still carry the money invariants, so these
// suites keep exercising them. This driver is compiled into this package's tests only; production cannot reach it.

type testIntent struct {
	ID          string
	ClientRef   string
	AmountMinor int64
	Currency    string
	Exponent    int16
	Provider    string

	MerchantAccountID string
}

type testExecResult struct {
	Outcome        Outcome
	Applied        string
	EntitlementID  string
	AlreadyGranted bool
}

type testEngine struct {
	*Engine
	provider    Provider
	outcomePool *pgxpool.Pool
}

// NewEngine defaults the outcome credential to the same pool; authority-split tests use NewEngineWithOutcome.
func NewEngine(cfg Config, pool *pgxpool.Pool, p Provider, g Granter) *testEngine {
	return &testEngine{Engine: newEngine(cfg, pool, g), provider: p, outcomePool: pool}
}

// NewEngineWithOutcome takes a separate outcome connection; nil models a missing outcome credential.
func NewEngineWithOutcome(cfg Config, pool, outcome *pgxpool.Pool, p Provider, g Granter) *testEngine {
	return &testEngine{Engine: newEngine(cfg, pool, g), provider: p, outcomePool: outcome}
}

func (e *testEngine) CreateChargeIntent(ctx context.Context, tenantID, siteID, settlementID, idempotencyKey string) (testIntent, error) {
	var acct, prov, merchantRef string
	if err := e.pool.QueryRow(ctx,
		`SELECT account_id::text, provider, merchant_account_ref FROM iam_v2.p4_resolve_payment_account($1,$2)`,
		tenantID, siteID).Scan(&acct, &prov, &merchantRef); err != nil {
		return testIntent{}, classify(err)
	}
	ref, err := newClientRef()
	if err != nil {
		return testIntent{}, err
	}
	in := testIntent{ClientRef: ref, Provider: prov, MerchantAccountID: acct}
	err = e.pool.QueryRow(ctx, `
INSERT INTO iam_v2.payment_transactions
  (tenant_id, site_id, settlement_id, merchant_account_id, transaction_type, provider, provider_ref,
   idempotency_key, amount_minor, currency, currency_exponent, status)
SELECT $1, $2, $3, $4, 'CHARGE', $5, $6, $7, pu.amount_minor, pu.currency, pu.currency_exponent, 'CREATED'
  FROM iam_v2.settlements se
  JOIN iam_v2.purchases pu ON pu.tenant_id = se.tenant_id AND pu.site_id = se.site_id AND pu.id = se.purchase_id
 WHERE se.tenant_id = $1 AND se.site_id = $2 AND se.id = $3
RETURNING id::text, amount_minor, currency, currency_exponent`,
		tenantID, siteID, settlementID, acct, prov, ref, idempotencyKey).
		Scan(&in.ID, &in.AmountMinor, &in.Currency, &in.Exponent)
	if errors.Is(err, pgx.ErrNoRows) {
		return testIntent{}, fail(ErrNotExecutable, "no such settlement in this tenant/site")
	}
	if err != nil {
		return testIntent{}, classify(err)
	}
	return in, nil
}

func (e *testEngine) CreateRefundIntent(ctx context.Context, tenantID, siteID, parentID string, amountMinor int64, idempotencyKey string) (testIntent, error) {
	ref, err := newClientRef()
	if err != nil {
		return testIntent{}, err
	}
	in := testIntent{ClientRef: ref}
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
		return testIntent{}, fail(ErrNotExecutable, "no such parent transaction in this tenant/site")
	}
	if err != nil {
		return testIntent{}, classify(err)
	}
	return in, nil
}

// Execute drives one scripted double: begin_payment_execution, the double, then the narrowed outcome function.
func (e *testEngine) Execute(ctx context.Context, tenantID, siteID, txnID string) (testExecResult, error) {
	var out testExecResult
	if e.provider == nil {
		return out, fail(ErrConfig, "no payment provider adapter is configured")
	}
	var began string
	if err := e.pool.QueryRow(ctx, `SELECT iam_v2.begin_payment_execution($1)`, txnID).Scan(&began); err != nil {
		return out, classify(err)
	}
	var r Request
	var merchant, kind string
	var parentRef *string
	if err := e.pool.QueryRow(ctx, `
SELECT t.merchant_account_id::text, t.transaction_type, t.provider_ref, t.amount_minor, t.currency, t.currency_exponent,
       (SELECT p.provider_ref FROM iam_v2.payment_transactions p WHERE p.id = t.parent_transaction_id)
  FROM iam_v2.payment_transactions t
 WHERE t.tenant_id = $1 AND t.site_id = $2 AND t.id = $3`, tenantID, siteID, txnID).
		Scan(&merchant, &kind, &r.ClientRef, &r.AmountMinor, &r.Currency, &r.Exponent, &parentRef); err != nil {
		return out, classify(err)
	}
	r.MerchantAccount, r.Kind = merchant, kind
	if parentRef != nil {
		r.ParentRef = *parentRef
	}
	res, perr := e.provider.Execute(ctx, r)
	if perr != nil {
		res = Result{Outcome: OutcomeUnknown, ReasonCode: "adapter_error"}
	}
	return e.apply(ctx, r.ClientRef, "exec:"+r.ClientRef, "execution_result", res.Outcome, res.ProviderTxnRef, res.ReasonCode)
}

// HandleProviderNotification authenticates through the double and applies the outcome.
func (e *testEngine) HandleProviderNotification(ctx context.Context, raw RawNotification) (testExecResult, error) {
	var out testExecResult
	auth, ok := e.provider.(NotificationAuthenticator)
	if !ok {
		return out, fail(ErrUntrusted, "the configured adapter cannot authenticate provider notifications")
	}
	parsed, err := auth.AuthenticateNotification(ctx, raw)
	if err != nil {
		return out, fail(ErrUntrusted, "the provider notification did not authenticate")
	}
	return e.apply(ctx, parsed.ClientRef, parsed.ProviderEventID, parsed.EventType, parsed.Outcome, parsed.ProviderTxnRef, parsed.ReasonCode)
}

func (e *testEngine) apply(ctx context.Context, clientRef, eventID, eventType string, outcome Outcome, providerTxnRef, reason string) (testExecResult, error) {
	var out testExecResult
	if clientRef == "" || eventID == "" {
		return out, fail(ErrUncorrelated, "a notification without a client reference and a provider event id cannot be correlated")
	}
	var tenantID, siteID, settlementID, kind string
	err := e.pool.QueryRow(ctx, `
SELECT tenant_id::text, site_id::text, settlement_id::text, transaction_type
  FROM iam_v2.payment_transactions WHERE provider_ref = $1`, clientRef).Scan(&tenantID, &siteID, &settlementID, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fail(ErrUncorrelated, "no payment intent matches this client reference")
	}
	if err != nil {
		return out, classify(err)
	}
	asserted := ""
	switch outcome {
	case OutcomeCaptured:
		asserted = "CAPTURED"
	case OutcomeDeclined:
		asserted = "FAILED"
	case OutcomeUnknown:
		asserted = "UNKNOWN"
	}
	evidence, _ := json.Marshal(map[string]string{"provider_status": string(outcome), "provider_reason_code": reason})
	if e.outcomePool == nil {
		return out, fail(ErrUntrusted, "no payment outcome credential is configured; a provider outcome cannot be applied")
	}
	var applied string
	if err := e.outcomePool.QueryRow(ctx, `SELECT iam_v2.p4_apply_provider_outcome($1,$2,$3,$4,$5,$6::jsonb)`,
		clientRef, eventID, eventType, asserted, providerTxnRef, string(evidence)).Scan(&applied); err != nil {
		return out, classify(err)
	}
	out.Applied, out.Outcome = applied, outcome
	if outcome == OutcomeUnknown {
		return out, fail(ErrProviderUnknown, "the provider outcome could not be determined; the settlement is in manual review")
	}
	if outcome != OutcomeCaptured || kind != "CHARGE" {
		return out, nil
	}
	if e.granter == nil {
		return out, fail(ErrGrant, "no entitlement granter is configured")
	}
	g, err := e.granter.GrantSettledSettlement(ctx, tenantID, siteID, settlementID)
	if err != nil {
		if CodeOf(err) == ErrNotExecutable {
			return out, nil
		}
		return out, fail(ErrGrant, "the paid grant refused: "+err.Error())
	}
	out.EntitlementID, out.AlreadyGranted = g.EntitlementID, g.AlreadyGranted
	return out, nil
}
