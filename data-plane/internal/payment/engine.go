package payment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GrantOutcome is what the Phase-2 grant path reports back.
type GrantOutcome struct {
	EntitlementID  string
	AlreadyGranted bool
}

// Granter is the ONLY way this package can create guest access.
//
// It is an interface so the dependency points one way and, more importantly, so this package CANNOT grow an
// entitlement writer of its own: there is no SQL here that touches iam_v2.entitlements, and the only
// implementation in the tree adapts the existing Phase-2 controlled path.
type Granter interface {
	GrantSettledPurchase(ctx context.Context, tenantID, siteID, purchaseID string) (GrantOutcome, error)
}

// Engine is the payment runtime.
//
// Its fields are unexported and there is one exported production constructor, for the same reason the
// posting engine has one: an exported constructor taking a Config and a Provider is an independently
// configured money sender.
type Engine struct {
	cfg      Config
	pool     *pgxpool.Pool
	provider Provider
	granter  Granter
	guard    *providerGuard
}

// NewProductionEngine is the ONLY exported constructor. It takes no config and no provider: the posture
// comes from the real environment and the provider from the internal factory below.
func NewProductionEngine(pool *pgxpool.Pool, granter Granter) (*Engine, error) {
	cfg, err := LoadConfigFromEnv(osGetenv)
	if err != nil {
		return nil, err
	}
	p, err := productionProvider(cfg)
	if err != nil {
		return nil, err
	}
	return newEngine(cfg, pool, p, granter), nil
}

func newEngine(cfg Config, pool *pgxpool.Pool, p Provider, g Granter) *Engine {
	return &Engine{cfg: cfg, pool: pool, provider: p, granter: g, guard: &providerGuard{cfg: cfg, inner: p}}
}

// Config returns a copy of the flag posture.
func (e *Engine) Config() Config { return e.cfg }

// ProviderRefusals reports how many provider calls the guard refused, so a DARK test can assert the
// runtime really tried and was stopped rather than never having reached the boundary.
func (e *Engine) ProviderRefusals() int { return e.guard.refusals() }

// ---------------------------------------------------------------- durable intent

// Intent is the durable local record of an intention to move money. It exists BEFORE any provider call.
type Intent struct {
	ID          string
	ClientRef   string
	AmountMinor int64
	Currency    string
	Exponent    int16
}

// CreateChargeIntent writes the durable intent for a settlement, resolving every money field from the
// pinned server-side Purchase.
//
// The signature is the point: a caller supplies a settlement and an idempotency key and NOTHING ELSE. There
// is no parameter for an amount, a currency, a merchant account, an internal transaction id or a provider
// result, so an untrusted request has nothing to tamper with. The database re-derives and re-checks all of
// it anyway (PAYMENT_AMOUNT_NOT_SERVER_PINNED), so this is defence in depth rather than the only defence.
func (e *Engine) CreateChargeIntent(ctx context.Context, tenantID, siteID, settlementID, merchantAccountID, idempotencyKey string) (Intent, error) {
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
  (tenant_id, site_id, settlement_id, merchant_account_id, transaction_type, provider, provider_ref,
   idempotency_key, amount_minor, currency, currency_exponent, status)
SELECT $1, $2, $3, $4, 'CHARGE', $5, $6, $7, pu.amount_minor, pu.currency, pu.currency_exponent, 'CREATED'
  FROM iam_v2.settlements se
  JOIN iam_v2.purchases pu ON pu.tenant_id = se.tenant_id AND pu.site_id = se.site_id AND pu.id = se.purchase_id
 WHERE se.tenant_id = $1 AND se.site_id = $2 AND se.id = $3
RETURNING id::text, amount_minor, currency, currency_exponent`,
		tenantID, siteID, settlementID, merchantAccountID, e.provider.Name(), ref, idempotencyKey).
		Scan(&in.ID, &in.AmountMinor, &in.Currency, &in.Exponent)
	if errors.Is(err, pgx.ErrNoRows) {
		return Intent{}, fail(ErrNotExecutable, "no such settlement in this tenant/site")
	}
	if err != nil {
		return Intent{}, classify(err)
	}
	return in, nil
}

// CreateRefundIntent writes the durable intent for a refund of a captured charge. The amount is the only
// caller-supplied money value, because a partial refund is a genuine operator decision; the database
// enforces the cumulative bound against the captured parent under an advisory lock.
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

// ---------------------------------------------------------------- execution

// ExecuteResult is what one execution attempt did.
type ExecuteResult struct {
	Outcome        Outcome
	Applied        string // APPLIED | DUPLICATE | NOOP | "" when nothing was applied
	EntitlementID  string
	AlreadyGranted bool
}

// Execute crosses the durable execution boundary and then contacts the provider.
//
// ORDER, which is the entire safety argument:
//
//  1. begin_payment_execution   settlement REQUIRED->IN_PROGRESS and intent CREATED->PENDING, in ONE
//     database transaction, BEFORE any provider is contacted. A crash after this
//     leaves a durable record saying we asked.
//  2. provider.Execute          behind the DARK guard. While dark it never runs.
//  3. apply_payment_callback_v2 the outcome, correlated by the provider's own identity, applied together
//     with the settlement transition in ONE transaction.
//  4. grant                     only on a settled charge, only through the Phase-2 path.
//
// An UNKNOWN outcome stops at step 3 with the settlement in MANUAL_REVIEW. Nothing retries it.
func (e *Engine) Execute(ctx context.Context, tenantID, siteID, txnID string) (ExecuteResult, error) {
	var out ExecuteResult
	if !e.cfg.DomainOn() {
		return out, fail(ErrConfig, "the phase-4 payment domain is disabled")
	}

	// 1. durable execution boundary
	var began string
	if err := e.pool.QueryRow(ctx, `SELECT iam_v2.begin_payment_execution($1)`, txnID).Scan(&began); err != nil {
		return out, classify(err)
	}

	// load the request from the DURABLE row; nothing is taken from a caller
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

	// 2. the provider, behind the guard
	res, perr := e.guard.execute(ctx, r)
	if perr != nil {
		// The guard refuses BEFORE the adapter is entered, so a guard error proves nothing was sent: DARK, a
		// missing adapter, an adapter that cannot correlate. Nothing is asserted about the money and nothing
		// is recorded. The intent stays PENDING and the settlement stays IN_PROGRESS, which is the truthful
		// state -- we began and did not proceed. An adapter that was entered and failed comes back as
		// OutcomeUnknown instead, never as an error.
		out.Outcome = OutcomeNotSent
		return out, perr
	}

	asserted := ""
	switch res.Outcome {
	case OutcomeCaptured:
		asserted = "CAPTURED"
	case OutcomeDeclined:
		asserted = "FAILED"
	case OutcomeNotSent:
		asserted = "FAILED"
	case OutcomeUnknown:
		asserted = "UNKNOWN"
	}
	out.Outcome = res.Outcome

	// 3. apply, correlated by the provider's identity
	evidence, _ := json.Marshal(map[string]string{"provider_status": string(res.Outcome),
		"provider_reason_code": res.ReasonCode})
	var applied string
	if err := e.pool.QueryRow(ctx,
		`SELECT iam_v2.apply_payment_callback_v2($1,$2,$3::uuid,$4,$5,$6,$7,$8,$9::jsonb)`,
		tenantID, provider, merchant, r.ClientRef,
		"exec:"+r.ClientRef, "execution_result", asserted,
		nullIfEmpty(res.ProviderTxnRef), string(evidence)).Scan(&applied); err != nil {
		return out, classify(err)
	}
	out.Applied = applied

	if res.Outcome == OutcomeUnknown {
		return out, fail(ErrProviderUnknown,
			"the provider outcome could not be determined; the settlement is in manual review and nothing "+
				"will be retried automatically")
	}

	// 4. the grant, only for a settled charge, only through the Phase-2 path
	if res.Outcome == OutcomeCaptured && kind == "CHARGE" {
		g, err := e.grantForSettlement(ctx, tenantID, siteID, settlementID)
		if err != nil {
			return out, err
		}
		out.EntitlementID, out.AlreadyGranted = g.EntitlementID, g.AlreadyGranted
	}
	return out, nil
}

// ApplyCallback applies an out-of-band provider notification. It takes what the PROVIDER supplied and
// nothing that identifies an internal row, so a forged callback cannot nominate someone else's money.
func (e *Engine) ApplyCallback(ctx context.Context, tenantID, provider, merchantAccountID, clientRef,
	providerEventID, eventType, assertedStatus, providerTxnRef string, evidence map[string]string) (ExecuteResult, error) {
	var out ExecuteResult
	if !e.cfg.DomainOn() {
		return out, fail(ErrConfig, "the phase-4 payment domain is disabled")
	}
	if evidence == nil {
		// An absent evidence map is an EMPTY object, never a JSON null: the append-only ledger rejects a
		// null, and a callback with nothing to record must still be recorded.
		evidence = map[string]string{}
	}
	raw, _ := json.Marshal(evidence)
	var applied string
	if err := e.pool.QueryRow(ctx,
		`SELECT iam_v2.apply_payment_callback_v2($1,$2,$3::uuid,$4,$5,$6,$7,$8,$9::jsonb)`,
		tenantID, provider, merchantAccountID, clientRef, providerEventID, eventType,
		nullIfEmpty(assertedStatus), nullIfEmpty(providerTxnRef), string(raw)).Scan(&applied); err != nil {
		return out, classify(err)
	}
	out.Applied = applied
	if applied != "APPLIED" || assertedStatus != "CAPTURED" {
		return out, nil
	}
	// A capture arriving out of band still settles and still grants -- through the same one path.
	var siteID, settlementID, kind string
	if err := e.pool.QueryRow(ctx, `
SELECT t.site_id::text, t.settlement_id::text, t.transaction_type FROM iam_v2.payment_transactions t
 WHERE t.tenant_id=$1 AND t.provider=$2 AND t.merchant_account_id=$3::uuid AND t.provider_ref=$4`,
		tenantID, provider, merchantAccountID, clientRef).Scan(&siteID, &settlementID, &kind); err != nil {
		return out, classify(err)
	}
	if kind != "CHARGE" {
		return out, nil
	}
	g, err := e.grantForSettlement(ctx, tenantID, siteID, settlementID)
	if err != nil {
		return out, err
	}
	out.EntitlementID, out.AlreadyGranted = g.EntitlementID, g.AlreadyGranted
	return out, nil
}

// grantForSettlement hands off to the EXISTING Phase-2 controlled path. It resolves the purchase from the
// settlement rather than accepting one, and it grants only when the settlement is durably SETTLED.
func (e *Engine) grantForSettlement(ctx context.Context, tenantID, siteID, settlementID string) (GrantOutcome, error) {
	var purchaseID, status string
	if err := e.pool.QueryRow(ctx,
		`SELECT purchase_id::text, status FROM iam_v2.settlements WHERE tenant_id=$1 AND site_id=$2 AND id=$3`,
		tenantID, siteID, settlementID).Scan(&purchaseID, &status); err != nil {
		return GrantOutcome{}, classify(err)
	}
	if status != "SETTLED" {
		// Not an error: a charge can be captured while the settlement is legitimately elsewhere (already
		// reversed, for instance). Granting would be the bug.
		return GrantOutcome{}, nil
	}
	if e.granter == nil {
		return GrantOutcome{}, fail(ErrGrant, "no entitlement granter is configured")
	}
	g, err := e.granter.GrantSettledPurchase(ctx, tenantID, siteID, purchaseID)
	if err != nil {
		// Carry the Phase-2 refusal's own words. It is a deterministic domain code, never guest data, and
		// swallowing it turns every grant problem into the same unactionable sentence.
		return GrantOutcome{}, fail(ErrGrant, "the phase-2 grant refused: "+err.Error())
	}
	return g, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// classify maps the database's own financial refusals onto this package's vocabulary.
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
