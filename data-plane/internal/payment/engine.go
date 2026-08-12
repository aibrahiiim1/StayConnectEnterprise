package payment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GrantOutcome is what the paid-grant operation reports back.
type GrantOutcome struct {
	EntitlementID  string
	AlreadyGranted bool
	Superseded     string
}

// Granter is the ONLY way this package can create guest access.
//
// It is an interface so the dependency points one way and, more importantly, so this package CANNOT grow an
// entitlement writer of its own: there is no SQL here that touches iam_v2.entitlements, and the only
// implementation in the tree adapts the existing Phase-2 controlled path.
type Granter interface {
	// The parameter is a SETTLEMENT, deliberately. Passing a purchase would mean the caller had decided
	// which purchase this money paid for; passing the settlement means the database decides.
	GrantSettledSettlement(ctx context.Context, tenantID, siteID, settlementID string) (GrantOutcome, error)
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

	// outcomePool is a SEPARATE database credential holding sc_payment_outcome, and it is the only
	// connection that may assert what a provider said (migration 0024).
	//
	// WHY IT IS A DIFFERENT POOL. With one credential, stealing it is sufficient to fabricate money end
	// to end: create an intent, cross the execution boundary, declare it captured. Splitting the
	// authority means the execution credential can start payments and stop there, and the outcome
	// credential can only speak about payments some other credential already put in flight.
	//
	// It is NOT provider authentication -- that needs a signing secret the database never holds, and it
	// stays in AuthenticateNotification. This is the layer underneath it.
	//
	// nil is the DELIVERED state: with no adapter there is no authenticated notification to apply, so
	// the outcome path fails closed rather than quietly falling back to the execution credential.
	outcomePool *pgxpool.Pool
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
	e := newEngine(cfg, pool, p, granter)
	// The outcome credential is deliberately a separate DSN. Its absence is not an error at startup --
	// a build with no provider adapter has no notifications to apply -- but it makes every outcome
	// application fail closed, which is the correct posture for a DARK build.
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
	// The resolved identity, reported back so a caller can SEE what was chosen for it rather than assuming.
	Provider          string
	MerchantAccountID string
}

// CreateChargeIntent writes the durable intent for a settlement.
//
// The signature is the point. A caller supplies a settlement and an idempotency key and NOTHING ELSE:
// no amount, no currency, no exponent, no provider, no merchant account, no internal transaction id and no
// financial result. There is nothing here for an untrusted request to tamper with.
//
// Where each value actually comes from:
//
//	amount / currency / exponent   the pinned Purchase, read by the INSERT itself
//	provider / merchant account    p4_resolve_payment_account, the site's ACTIVE default configuration
//	client reference               minted locally, before the row exists
//
// Resolution happens FIRST and fails closed. A site with no ACTIVE default account cannot create a payment
// intent at all, which is the correct answer -- far better than persisting an invented identity and
// discovering at capture time that nobody knows whose money moved.
func (e *Engine) CreateChargeIntent(ctx context.Context, tenantID, siteID, settlementID, idempotencyKey string) (Intent, error) {
	if !e.cfg.DomainOn() {
		return Intent{}, fail(ErrConfig, "the phase-4 payment domain is disabled")
	}
	acct, err := e.resolveAccount(ctx, tenantID, siteID)
	if err != nil {
		return Intent{}, err
	}
	// The configured provider is the one this build must be able to reach. An adapter that is absent or
	// answers to a different name is a misconfiguration, and finding that out before writing a durable
	// intent is cheaper than finding it out afterwards.
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

// Account is the resolved, server-side financial identity for a site. It carries identifiers only; no
// credential of any kind passes through this type.
type Account struct {
	ID          string
	Provider    string
	MerchantRef string
}

// resolveAccount reads the site's ACTIVE default payment account. It is the ONLY way this package learns
// which provider and which merchant account to use, and there is deliberately no variant that accepts a
// preference from anywhere.
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

// CreateRefundIntent writes the durable intent for a refund of a captured charge.
//
// The amount is the only caller-supplied money value, because a partial refund is a genuine operator
// decision; the database enforces the cumulative bound against the captured parent under an advisory lock.
// Provider and merchant account are INHERITED from the parent row rather than resolved afresh: money must
// return by the way it arrived, even if the site's default configuration has since changed.
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

	// 0. PREFLIGHT, before anything durable moves.
	//
	// These checks read configuration and adapter capability only. They send nothing and change nothing, so
	// a refusal here is provably NOT_SENT and must leave NO trace: the intent stays CREATED and the
	// Settlement stays REQUIRED, both of which remain retriable once the configuration is fixed.
	//
	// Doing this after the durable transition -- as an earlier revision did -- meant a DARK build left every
	// intent permanently PENDING and its Settlement stuck IN_PROGRESS, describing an execution that had
	// never begun. A stuck IN_PROGRESS settlement is indistinguishable from one whose provider call is still
	// outstanding, so it silently becomes manual-review work that nobody created.
	if err := e.guard.preflight(); err != nil {
		out.Outcome = OutcomeNotSent
		return out, err
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
		// Preflight already passed, so reaching here means the posture changed between the two calls -- a
		// flag flipped, an adapter withdrawn -- or the durable row carried no client reference. Either way
		// the adapter was not entered and nothing was sent, so nothing is asserted and nothing is recorded.
		// An adapter that WAS entered and failed comes back as OutcomeUnknown, never as an error.
		out.Outcome = OutcomeNotSent
		return out, perr
	}

	// 3. apply, through the SAME trusted path an authenticated webhook uses.
	//
	// This is a synchronous answer to a request we made ourselves, so it is trusted for the one reason that
	// matters: we know which intent it belongs to because we are the ones who sent it. It still goes through
	// applyTrusted so that there is exactly one place where a provider outcome becomes financial fact.
	// 4. the settlement move and the grant both happen inside applyTrusted.
	return e.applyTrusted(ctx, trustedNotification{
		clientRef: r.ClientRef, eventID: "exec:" + r.ClientRef, eventType: "execution_result",
		outcome: res.Outcome, providerTxnRef: res.ProviderTxnRef, reasonCode: res.ReasonCode,
	})
}

// ---------------------------------------------------------------- the trusted notification boundary

// HandleProviderNotification is the ONLY way an out-of-band provider outcome enters the financial record.
//
// THE PROBLEM IT SOLVES. The previous entry point took an "asserted status" string from its caller. Any
// component that could reach it could therefore manufacture a CAPTURED for someone else's payment -- the
// database would dutifully settle the settlement and grant the entitlement, because from its point of view
// a trusted caller had said the money arrived. Correlating the client reference correctly does not help:
// the reference is not a secret, it appears in provider dashboards and logs, and knowing one is not
// evidence of anything.
//
// THE SHAPE OF THE FIX. A financial outcome is only trustworthy if it was AUTHENTICATED by the adapter that
// owns the relationship with the provider. So:
//
//   - the raw delivery (bytes plus transport headers) goes to the adapter, which verifies the provider's
//     signature with the secret only it holds;
//   - the adapter returns parsed, non-sensitive fields -- it cannot return "trusted", only "here is what
//     this authenticated delivery said";
//   - THIS function mints the trustedNotification. The type's zero value is useless and its fields are
//     unexported, so no caller anywhere can construct one;
//   - an adapter that does not implement NotificationAuthenticator cannot deliver an outcome at all.
//
// Tenant and site are NOT parameters. They are resolved from the durable row the client reference names, so
// a delivery cannot nominate whose money it is about.
//
// No real provider adapter exists in this build, so in practice this refuses. That is the correct DARK
// behaviour and the deterministic doubles exercise the same path a real adapter would.
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

// trustedNotification is an authenticated provider outcome. Its fields are unexported and it is minted in
// exactly two places in this package -- after an adapter authenticated a delivery, and by Execute for the
// synchronous response to a call we ourselves made. There is no exported constructor, and there is
// deliberately no way for any other package to produce one.
type trustedNotification struct {
	clientRef      string
	eventID        string
	eventType      string
	outcome        Outcome
	providerTxnRef string
	reasonCode     string
}

// assertedStatus maps a provider outcome onto the payment status machine. NOT_SENT deliberately has no
// mapping: nothing was sent, so there is nothing to assert.
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

// applyTrusted records an authenticated outcome and moves the settlement with it, in one transaction, then
// hands a settled charge to the Phase-2 grant.
func (e *Engine) applyTrusted(ctx context.Context, n trustedNotification) (ExecuteResult, error) {
	var out ExecuteResult
	if n.clientRef == "" || n.eventID == "" {
		return out, fail(ErrUncorrelated,
			"a notification without a client reference and a provider event id cannot be correlated or "+
				"deduplicated")
	}
	// Resolve ownership from the durable row rather than from the delivery. This is what makes tenant and
	// site unforgeable: they are read from the intent the client reference names, not claimed by a caller.
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
	// The NARROWED operation (0021), through the SEPARATE outcome credential (0024). The execution pool
	// is not used here and does not hold EXECUTE on this function: asserting what a provider said is a
	// different authority from executing a payment, so one stolen credential is not enough to fabricate
	// a financial outcome.
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

// grantForSettlement hands off to the EXISTING Phase-2 controlled path. It resolves the purchase from the
// settlement rather than accepting one, and it grants only when the settlement is durably SETTLED.
func (e *Engine) grantForSettlement(ctx context.Context, tenantID, siteID, settlementID string) (GrantOutcome, error) {
	if e.granter == nil {
		return GrantOutcome{}, fail(ErrGrant, "no entitlement granter is configured")
	}
	// No pre-check on the settlement status here. The operation re-reads and locks it, so a check performed
	// beforehand would be both redundant and racy -- and a caller that "verified" first would be tempted to
	// pass what it read.
	g, err := e.granter.GrantSettledSettlement(ctx, tenantID, siteID, settlementID)
	if err != nil {
		// A settlement that is legitimately not SETTLED -- already reversed, still in review -- is not an
		// error here: the capture was recorded, and granting would be the bug.
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
