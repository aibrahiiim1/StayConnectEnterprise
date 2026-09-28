package payment

// CARD PAYMENT CHECKOUT ENGINE.
//
// The flow, and who holds which authority at each step:
//
//	purchase AWAITING_SETTLEMENT + settlement ONLINE_PAYMENT/REQUIRED     (commerce, in scd, as svc_scd)
//	-> intent: payment_transactions CHARGE CREATED, client ref sc_...      (runtime role: svc_payment)
//	-> begin_payment_execution: txn PENDING, settlement IN_PROGRESS        (runtime role)
//	-> provider hosted checkout, idempotency key = client ref              (outbound HTTPS, no money moves)
//	-> the client pays ON THE PROVIDER'S PAGE
//	-> QueryStatus, outbound and authenticated, repeated safely             (return URL and reconciler)
//	-> p4_apply_provider_outcome                                            (outcome role: svc_payment_outcome)
//	-> settlement SETTLED -> p4_grant_paid_entitlement                      (runtime role)
//
// Nothing the browser says is proof. A checkout is created at most once per settlement (the unique live-charge
// index), and an ambiguous creation is resolved by querying the client reference, never by creating again. A
// still-unresolved outcome after the checkout expiry plus the reconciliation grace becomes UNKNOWN, which the
// ledger routes to manual review; it is never retried.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Checkout errors a caller can act on.
var (
	ErrLiveNotAllowed      = errors.New("payment: LIVE-mode provider accounts are not authorised on this appliance")
	ErrNoAdapter           = errors.New("payment: no adapter for the configured provider")
	ErrCredentials         = errors.New("payment: provider credentials are missing or unreadable")
	ErrCheckoutUnavailable = errors.New("payment: the provider refused to create a checkout")
	ErrCheckoutAmbiguous   = errors.New("payment: the checkout could not be confirmed; it is being reconciled")
	ErrNotConfigured       = errors.New("payment: card payment is not configured on this appliance")
)

// CheckoutSettings are the site's operational card-payment settings (card_payment_settings_get).
type CheckoutSettings struct {
	Expiry         time.Duration
	ReconcileGrace time.Duration
}

// CheckoutEngine runs hosted checkouts. Runtime and Outcome are separate credentials by design.
type CheckoutEngine struct {
	Runtime     *pgxpool.Pool // svc_payment (sc_payment_runtime)
	Outcome     *pgxpool.Pool // svc_payment_outcome (sc_payment_outcome)
	Adapters    map[string]HostedCheckoutProvider
	Key         PaymentKey
	LiveAllowed bool
	Now         func() time.Time
	Health      *ProviderHealth
}

func (e *CheckoutEngine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Configured reports whether the engine has both credentials and a key.
func (e *CheckoutEngine) Configured() bool {
	return e != nil && e.Runtime != nil && e.Outcome != nil && e.Key.Present()
}

// ResolvedAccount is the site's ACTIVE default account with its opened credentials.
type ResolvedAccount struct {
	ID          string
	Provider    string
	MerchantRef string
	Currency    string
	Mode        Mode
	Credentials Credentials
}

// ResolveAccount reads the default account and opens its current credential generation.
func (e *CheckoutEngine) ResolveAccount(ctx context.Context, tenantID, siteID string) (ResolvedAccount, error) {
	if !e.Configured() {
		return ResolvedAccount{}, ErrNotConfigured
	}
	var a ResolvedAccount
	var mode string
	var cur *string
	err := e.Runtime.QueryRow(ctx,
		`SELECT account_id::text, provider, merchant_account_ref, currency, mode FROM iam_v2.p4_resolve_payment_account_v2($1,$2)`,
		tenantID, siteID).Scan(&a.ID, &a.Provider, &a.MerchantRef, &cur, &mode)
	if err != nil {
		if strings.Contains(err.Error(), "PAYMENT_NO_CONFIGURED_ACCOUNT") || errors.Is(err, pgx.ErrNoRows) {
			return ResolvedAccount{}, fail(ErrNoAccount, "no ACTIVE default payment account")
		}
		return ResolvedAccount{}, classify(err)
	}
	if cur != nil {
		a.Currency = strings.TrimSpace(*cur)
	}
	a.Mode = Mode(mode)
	c, err := e.openCredentials(ctx, tenantID, siteID, a.ID)
	if err != nil {
		return ResolvedAccount{}, err
	}
	a.Credentials = c
	return a, nil
}

func (e *CheckoutEngine) openCredentials(ctx context.Context, tenantID, siteID, accountID string) (Credentials, error) {
	var gen, keyID string
	var nonce, ct []byte
	err := e.Runtime.QueryRow(ctx, `
		SELECT id::text, key_id, nonce, ciphertext FROM iam_v2.payment_provider_secret_generations
		 WHERE tenant_id=$1 AND site_id=$2 AND account_id=$3 AND superseded_at IS NULL`,
		tenantID, siteID, accountID).Scan(&gen, &keyID, &nonce, &ct)
	if err != nil {
		return nil, ErrCredentials
	}
	c, err := OpenCredentials(e.Key, tenantID, siteID, accountID, gen, keyID, nonce, ct)
	if err != nil {
		return nil, ErrCredentials
	}
	return c, nil
}

// Settings reads the site's card payment settings (defaults when unset).
func (e *CheckoutEngine) Settings(ctx context.Context, tenantID, siteID string) CheckoutSettings {
	s := CheckoutSettings{Expiry: 30 * time.Minute, ReconcileGrace: 60 * time.Minute}
	if e.Runtime == nil {
		return s
	}
	var exp, grace int
	if err := e.Runtime.QueryRow(ctx,
		`SELECT checkout_expiry_minutes, reconcile_grace_minutes FROM iam_v2.card_payment_settings_get($1,$2)`,
		tenantID, siteID).Scan(&exp, &grace); err == nil {
		s.Expiry, s.ReconcileGrace = time.Duration(exp)*time.Minute, time.Duration(grace)*time.Minute
	}
	return s
}

// adapterFor returns the adapter for an account, refusing LIVE mode unless authorised and refusing a key whose
// declared mode disagrees (the adapters enforce key prefixes too).
func (e *CheckoutEngine) adapterFor(a ResolvedAccount) (HostedCheckoutProvider, error) {
	if a.Mode == ModeLive && !e.LiveAllowed {
		return nil, ErrLiveNotAllowed
	}
	if a.Mode != ModeLive && a.Mode != ModeTest {
		return nil, ErrCredentials
	}
	ad, ok := e.Adapters[AdapterKey(a.Provider, a.Credentials)]
	if !ok || ad == nil {
		return nil, ErrNoAdapter
	}
	for _, k := range ad.CredentialKeys() {
		if k.Required && strings.TrimSpace(a.Credentials[k.Key]) == "" {
			return nil, ErrCredentials
		}
	}
	return ad, nil
}

// CheckoutStart is what the portal needs to send the client to the provider.
type CheckoutStart struct {
	TransactionID string
	ClientRef     string
	RedirectURL   string
}

// StartCheckout creates the intent, begins execution and creates the provider checkout for a REQUIRED
// ONLINE_PAYMENT settlement. The returnURL must carry nothing the portal will trust.
func (e *CheckoutEngine) StartCheckout(ctx context.Context, tenantID, siteID, settlementID, description, returnURL, cancelURL string) (CheckoutStart, error) {
	acct, err := e.ResolveAccount(ctx, tenantID, siteID)
	if err != nil {
		return CheckoutStart{}, err
	}
	ad, err := e.adapterFor(acct)
	if err != nil {
		return CheckoutStart{}, err
	}
	settings := e.Settings(ctx, tenantID, siteID)
	ref, err := newClientRef()
	if err != nil {
		return CheckoutStart{}, err
	}
	now := e.now()
	expires := now.Add(settings.Expiry)

	var out CheckoutStart
	var amount int64
	var currency string
	var exponent int16
	// Intent + execution start + checkout row: one transaction on the runtime credential.
	err = pgx.BeginFunc(ctx, e.Runtime, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
INSERT INTO iam_v2.payment_transactions
  (tenant_id, site_id, settlement_id, merchant_account_id, transaction_type, provider, provider_ref,
   idempotency_key, amount_minor, currency, currency_exponent, status)
SELECT $1, $2, se.id, $4, 'CHARGE', $5, $6, $7, pu.amount_minor, pu.currency, pu.currency_exponent, 'CREATED'
  FROM iam_v2.settlements se
  JOIN iam_v2.purchases pu ON pu.tenant_id = se.tenant_id AND pu.site_id = se.site_id AND pu.id = se.purchase_id
 WHERE se.tenant_id = $1 AND se.site_id = $2 AND se.id = $3
   AND se.method = 'ONLINE_PAYMENT' AND se.status = 'REQUIRED'
RETURNING id::text, amount_minor, currency, currency_exponent`,
			tenantID, siteID, settlementID, acct.ID, acct.Provider, ref, "checkout:"+settlementID).
			Scan(&out.TransactionID, &amount, &currency, &exponent); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fail(ErrNotExecutable, "the settlement is not a REQUIRED card payment")
			}
			return classify(err)
		}
		var began string
		if err := tx.QueryRow(ctx, `SELECT iam_v2.begin_payment_execution($1)`, out.TransactionID).Scan(&began); err != nil {
			return classify(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.payment_checkouts (transaction_id, tenant_id, site_id, expires_at)
			VALUES ($1,$2,$3,$4)`, out.TransactionID, tenantID, siteID, expires); err != nil {
			return classify(err)
		}
		return nil
	})
	if err != nil {
		return CheckoutStart{}, err
	}
	out.ClientRef = ref

	co, cerr := ad.CreateCheckout(ctx, CheckoutRequest{
		ClientRef: ref, MerchantAccount: acct.MerchantRef, Mode: acct.Mode, AmountMinor: amount,
		Currency: strings.TrimSpace(currency), Exponent: exponent, Description: description,
		ReturnURL: returnURL, CancelURL: cancelURL, ExpiresAt: expires, Credentials: acct.Credentials,
	})
	switch {
	case cerr == nil:
		e.Health.Record(acct.ID, true)
		if _, err := e.Runtime.Exec(ctx, `UPDATE iam_v2.payment_checkouts
			   SET provider_session_ref=$2, redirect_url=$3, creation_outcome='CREATED'
			 WHERE transaction_id=$1`, out.TransactionID, co.ProviderSessionRef, co.RedirectURL); err != nil {
			// The checkout exists at the provider; the reconciler still finds it by client reference.
			return out, ErrCheckoutAmbiguous
		}
		out.RedirectURL = co.RedirectURL
		return out, nil
	case errors.Is(cerr, ErrCheckoutNotCreated):
		// PROVABLY nothing exists at the provider: no money can move. The outcome authority records FAILED.
		e.Health.Record(acct.ID, false)
		_, _ = e.Runtime.Exec(ctx, `UPDATE iam_v2.payment_checkouts SET creation_outcome='NOT_CREATED' WHERE transaction_id=$1`, out.TransactionID)
		_ = e.applyOutcome(ctx, ref, "create:"+ref, "CHECKOUT_NOT_CREATED", "FAILED", "", "checkout_not_created")
		return out, ErrCheckoutUnavailable
	default:
		// Ambiguous: the checkout may exist. Never created again; the reconciler asks by client reference.
		e.Health.Record(acct.ID, false)
		_, _ = e.Runtime.Exec(ctx, `UPDATE iam_v2.payment_checkouts SET creation_outcome='AMBIGUOUS' WHERE transaction_id=$1`, out.TransactionID)
		return out, ErrCheckoutAmbiguous
	}
}

// applyOutcome records a provider-verified outcome through the OUTCOME credential only.
func (e *CheckoutEngine) applyOutcome(ctx context.Context, clientRef, eventID, eventType, status, providerTxnRef, reason string) error {
	// Only the ledger's allowed evidence keys (p4_callback_evidence_safe); how the outcome was learned goes in
	// provider_message.
	evidence, _ := json.Marshal(map[string]string{"provider_status": status, "provider_reason_code": reason,
		"provider_message": "outbound status query"})
	var ptr any
	if providerTxnRef != "" {
		ptr = providerTxnRef
	}
	var applied string
	err := e.Outcome.QueryRow(ctx, `SELECT iam_v2.p4_apply_provider_outcome($1,$2,$3,$4,$5,$6::jsonb)`,
		clientRef, eventID, eventType, status, ptr, string(evidence)).Scan(&applied)
	if err != nil {
		return classify(err)
	}
	return nil
}

// ReconcileResult reports where one checkout stands.
type ReconcileResult struct {
	TransactionStatus string // PENDING, CAPTURED, FAILED, UNKNOWN, ...
	SettlementStatus  string
	PurchaseState     string
	EntitlementID     string
	RedirectURL       string
}

type txnRow struct {
	id, tenant, site, settlement, purchase, clientRef, provider, status, currency string
	amount                                                                        int64
	sessionRef, redirect                                                          *string
	expires, created                                                              time.Time
	creation                                                                      string
}

func (e *CheckoutEngine) loadTxn(ctx context.Context, tenantID, siteID, txnID string) (txnRow, error) {
	var t txnRow
	err := e.Runtime.QueryRow(ctx, `
		SELECT pt.id::text, pt.tenant_id::text, pt.site_id::text, pt.settlement_id::text, se.purchase_id::text,
		       pt.provider_ref, pt.provider, pt.status, pt.currency, pt.amount_minor,
		       pc.provider_session_ref, pc.redirect_url, pc.expires_at, pc.created_at, pc.creation_outcome
		  FROM iam_v2.payment_transactions pt
		  JOIN iam_v2.settlements se ON se.id = pt.settlement_id
		  JOIN iam_v2.payment_checkouts pc ON pc.transaction_id = pt.id
		 WHERE pt.tenant_id=$1 AND pt.site_id=$2 AND pt.id=$3 AND pt.transaction_type='CHARGE'`,
		tenantID, siteID, txnID).Scan(&t.id, &t.tenant, &t.site, &t.settlement, &t.purchase, &t.clientRef,
		&t.provider, &t.status, &t.currency, &t.amount, &t.sessionRef, &t.redirect, &t.expires, &t.created, &t.creation)
	if err != nil {
		return txnRow{}, classify(err)
	}
	t.currency = strings.TrimSpace(t.currency)
	return t, nil
}

// Reconcile asks the provider what happened to one checkout and applies a conclusive answer. It is safe to
// call any number of times, from the return URL and from the background reconciler concurrently: status
// queries are read-only, outcome application is deduplicated by event id, and the grant is idempotent.
func (e *CheckoutEngine) Reconcile(ctx context.Context, tenantID, siteID, txnID string) (ReconcileResult, error) {
	t, err := e.loadTxn(ctx, tenantID, siteID, txnID)
	if err != nil {
		return ReconcileResult{}, err
	}
	var qerr error
	if t.status == "PENDING" {
		if err := e.queryAndApply(ctx, t); err != nil && !errors.Is(err, errStillOpen) {
			// A failed query changes nothing; the next pass asks again. The error is reported with the state.
			qerr = err
		}
	}
	// A captured charge whose grant did not happen yet (a crash between the two) is granted here.
	var st string
	if err := e.Runtime.QueryRow(ctx, `SELECT status FROM iam_v2.payment_transactions WHERE id=$1`, t.id).Scan(&st); err == nil && st == "CAPTURED" {
		if _, gerr := e.grant(ctx, t.tenant, t.site, t.settlement); gerr != nil && qerr == nil {
			qerr = gerr
		}
	}
	r, serr := e.state(ctx, t)
	if serr != nil {
		return r, serr
	}
	return r, qerr
}

var errStillOpen = errors.New("still open")

func (e *CheckoutEngine) queryAndApply(ctx context.Context, t txnRow) error {
	acct, err := e.accountForTxn(ctx, t)
	if err != nil {
		return err
	}
	ad, ok := e.Adapters[AdapterKey(t.provider, acct.Credentials)]
	if !ok || ad == nil {
		return ErrNoAdapter
	}
	settings := e.Settings(ctx, t.tenant, t.site)
	now := e.now()
	deadline := t.expires.Add(settings.ReconcileGrace)
	session := ""
	if t.sessionRef != nil {
		session = *t.sessionRef
	}
	res, qerr := ad.QueryStatus(ctx, StatusQuery{ClientRef: t.clientRef, ProviderSessionRef: session,
		MerchantAccount: acct.MerchantRef, Mode: acct.Mode, Credentials: acct.Credentials})
	e.bookkeep(ctx, t.id, res.State, qerr == nil)
	e.Health.Record(acct.ID, qerr == nil)
	if qerr != nil {
		if now.After(deadline) {
			return e.applyOutcome(ctx, t.clientRef, "status:"+t.clientRef+":unresolved", "STATUS_UNRESOLVED", "UNKNOWN", "", "unresolved_after_grace")
		}
		return qerr
	}
	switch res.State {
	case CheckoutCaptured:
		if res.AmountMinor != t.amount || !strings.EqualFold(res.Currency, t.currency) || res.ProviderTxnRef == "" {
			// The provider says paid, but not what was pinned. Never grant on a mismatch; review it.
			return e.applyOutcome(ctx, t.clientRef, "status:"+t.clientRef+":mismatch", "AMOUNT_MISMATCH", "UNKNOWN",
				res.ProviderTxnRef, "captured_amount_mismatch")
		}
		if err := e.applyOutcome(ctx, t.clientRef, "status:"+t.clientRef+":captured", "CAPTURED", "CAPTURED",
			res.ProviderTxnRef, res.ReasonCode); err != nil {
			return err
		}
		_, gerr := e.grant(ctx, t.tenant, t.site, t.settlement)
		e.recordReversals(ctx, t, res.Events)
		return gerr
	case CheckoutDeclined, CheckoutExpired:
		return e.applyOutcome(ctx, t.clientRef, "status:"+t.clientRef+":"+strings.ToLower(string(res.State)),
			string(res.State), "FAILED", res.ProviderTxnRef, res.ReasonCode)
	case CheckoutNotFound:
		// The provider has no checkout for this reference. After the grace, that proves no money moved.
		if now.After(deadline) || (t.creation == "AMBIGUOUS" && now.After(t.created.Add(settings.ReconcileGrace))) {
			return e.applyOutcome(ctx, t.clientRef, "status:"+t.clientRef+":not_found", "NOT_FOUND", "FAILED", "", "not_found")
		}
		return errStillOpen
	default: // OPEN
		if now.After(deadline) {
			return e.applyOutcome(ctx, t.clientRef, "status:"+t.clientRef+":open_after_grace", "OPEN_AFTER_GRACE",
				"UNKNOWN", "", "open_after_grace")
		}
		return errStillOpen
	}
}

func (e *CheckoutEngine) accountForTxn(ctx context.Context, t txnRow) (ResolvedAccount, error) {
	var a ResolvedAccount
	var mode string
	var cur *string
	err := e.Runtime.QueryRow(ctx, `
		SELECT a.id::text, a.provider, a.merchant_account_ref, a.currency, a.mode
		  FROM iam_v2.payment_transactions pt JOIN iam_v2.payment_provider_accounts a ON a.id = pt.merchant_account_id
		 WHERE pt.id=$1`, t.id).Scan(&a.ID, &a.Provider, &a.MerchantRef, &cur, &mode)
	if err != nil {
		return ResolvedAccount{}, classify(err)
	}
	if cur != nil {
		a.Currency = strings.TrimSpace(*cur)
	}
	a.Mode = Mode(mode)
	c, err := e.openCredentials(ctx, t.tenant, t.site, a.ID)
	if err != nil {
		return ResolvedAccount{}, err
	}
	a.Credentials = c
	if a.Mode == ModeLive && !e.LiveAllowed {
		return ResolvedAccount{}, ErrLiveNotAllowed
	}
	return a, nil
}

func (e *CheckoutEngine) bookkeep(ctx context.Context, txnID string, state CheckoutState, ok bool) {
	if ok {
		_, _ = e.Runtime.Exec(ctx, `UPDATE iam_v2.payment_checkouts SET last_status_at=now(), last_state=$2,
			status_checks=status_checks+1, consecutive_failures=0 WHERE transaction_id=$1`, txnID, string(state))
		return
	}
	_, _ = e.Runtime.Exec(ctx, `UPDATE iam_v2.payment_checkouts SET last_status_at=now(),
		status_checks=status_checks+1, consecutive_failures=consecutive_failures+1 WHERE transaction_id=$1`, txnID)
}

func (e *CheckoutEngine) grant(ctx context.Context, tenantID, siteID, settlementID string) (string, error) {
	var eid string
	var already bool
	var superseded *string
	err := e.Runtime.QueryRow(ctx, `SELECT entitlement_id::text, already_granted, superseded::text
		FROM iam_v2.p4_grant_paid_entitlement($1,$2,$3)`, tenantID, siteID, settlementID).Scan(&eid, &already, &superseded)
	if err != nil {
		return "", classify(err)
	}
	return eid, nil
}

// recordReversals records provider-originated refunds and chargebacks on a captured charge. OneGate never
// initiates one; it records what the provider reports so the ledger is true.
func (e *CheckoutEngine) recordReversals(ctx context.Context, t txnRow, events []ProviderEvent) {
	for _, ev := range events {
		if ev.Kind != "REFUND" && ev.Kind != "CHARGEBACK" || ev.EventID == "" || ev.AmountMinor <= 0 {
			continue
		}
		amount := ev.AmountMinor
		if ev.Cumulative {
			var recorded int64
			_ = e.Runtime.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor),0) FROM iam_v2.payment_transactions
				WHERE parent_transaction_id=$1 AND transaction_type=$2 AND status='CAPTURED'`, t.id, ev.Kind).Scan(&recorded)
			amount = ev.AmountMinor - recorded
			if amount <= 0 {
				continue
			}
		}
		var r string
		_ = e.Outcome.QueryRow(ctx, `SELECT iam_v2.p4_record_provider_reversal($1,$2,$3,$4,$5,$6,$7)`,
			t.tenant, t.site, t.id, ev.Kind, amount, ev.EventID, nullIfEmpty(ev.ProviderRef)).Scan(&r)
	}
}

// SweepReversals asks the provider about recently captured charges and records any refund or chargeback it
// reports. Read-only towards the provider.
func (e *CheckoutEngine) SweepReversals(ctx context.Context, tenantID, siteID string, since time.Duration, limit int) {
	rows, err := e.Runtime.Query(ctx, `
		SELECT pt.id::text FROM iam_v2.payment_transactions pt
		  JOIN iam_v2.payment_checkouts pc ON pc.transaction_id = pt.id
		 WHERE pt.tenant_id=$1 AND pt.site_id=$2 AND pt.transaction_type='CHARGE' AND pt.status='CAPTURED'
		   AND pc.created_at > now() - make_interval(secs => $3)
		 ORDER BY pc.last_status_at NULLS FIRST LIMIT $4`, tenantID, siteID, since.Seconds(), limit)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		t, err := e.loadTxn(ctx, tenantID, siteID, id)
		if err != nil {
			continue
		}
		acct, err := e.accountForTxn(ctx, t)
		if err != nil {
			continue
		}
		ad := e.Adapters[AdapterKey(t.provider, acct.Credentials)]
		if ad == nil {
			continue
		}
		session := ""
		if t.sessionRef != nil {
			session = *t.sessionRef
		}
		res, qerr := ad.QueryStatus(ctx, StatusQuery{ClientRef: t.clientRef, ProviderSessionRef: session,
			MerchantAccount: acct.MerchantRef, Mode: acct.Mode, Credentials: acct.Credentials})
		e.bookkeep(ctx, t.id, res.State, qerr == nil)
		if qerr == nil {
			e.recordReversals(ctx, t, res.Events)
		}
	}
}

// PendingTransactions lists open checkouts for the reconciler, least recently checked first.
func (e *CheckoutEngine) PendingTransactions(ctx context.Context, tenantID, siteID string, limit int) ([]string, error) {
	rows, err := e.Runtime.Query(ctx, `
		SELECT pt.id::text FROM iam_v2.payment_transactions pt
		  JOIN iam_v2.payment_checkouts pc ON pc.transaction_id = pt.id
		 WHERE pt.tenant_id=$1 AND pt.site_id=$2 AND pt.transaction_type='CHARGE' AND pt.status='PENDING'
		 ORDER BY pc.last_status_at NULLS FIRST, pc.created_at LIMIT $3`, tenantID, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CapturedStillUngranted lists captured charges whose grant has not happened (crash recovery).
func (e *CheckoutEngine) CapturedStillUngranted(ctx context.Context, tenantID, siteID string) []string {
	rows, err := e.Runtime.Query(ctx, `
		SELECT pt.id::text FROM iam_v2.payment_transactions pt
		  JOIN iam_v2.settlements se ON se.id = pt.settlement_id
		 WHERE pt.tenant_id=$1 AND pt.site_id=$2 AND pt.transaction_type='CHARGE' AND pt.status='CAPTURED'
		   AND se.status='SETTLED'
		   AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e WHERE e.purchase_id = se.purchase_id)
		 LIMIT 50`, tenantID, siteID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

func (e *CheckoutEngine) state(ctx context.Context, t txnRow) (ReconcileResult, error) {
	var r ReconcileResult
	var eid, redirect *string
	err := e.Runtime.QueryRow(ctx, `
		SELECT pt.status, se.status, pu.state, e.id::text, pc.redirect_url
		  FROM iam_v2.payment_transactions pt
		  JOIN iam_v2.settlements se ON se.id = pt.settlement_id
		  JOIN iam_v2.purchases pu ON pu.id = se.purchase_id
		  JOIN iam_v2.payment_checkouts pc ON pc.transaction_id = pt.id
		  LEFT JOIN iam_v2.entitlements e ON e.purchase_id = pu.id
		 WHERE pt.id=$1`, t.id).Scan(&r.TransactionStatus, &r.SettlementStatus, &r.PurchaseState, &eid, &redirect)
	if err != nil {
		return ReconcileResult{}, classify(err)
	}
	if eid != nil {
		r.EntitlementID = *eid
	}
	if redirect != nil {
		r.RedirectURL = *redirect
	}
	return r, nil
}

// TransactionForPurchase returns the charge of a purchase, if one was started.
func (e *CheckoutEngine) TransactionForPurchase(ctx context.Context, tenantID, siteID, purchaseID string) (string, error) {
	var id string
	err := e.Runtime.QueryRow(ctx, `
		SELECT pt.id::text FROM iam_v2.payment_transactions pt
		  JOIN iam_v2.settlements se ON se.id = pt.settlement_id
		 WHERE pt.tenant_id=$1 AND pt.site_id=$2 AND se.purchase_id=$3 AND pt.transaction_type='CHARGE'
		 ORDER BY pt.intent_created_at DESC LIMIT 1`, tenantID, siteID, purchaseID).Scan(&id)
	if err != nil {
		return "", classify(err)
	}
	return id, nil
}

// ---- provider health (readiness) ------------------------------------------------------------------------

// ProviderHealth tracks recent provider call outcomes per account. Card payment is not offered for NEW
// purchases while an account's provider keeps failing; configuration, history and reconciliation are
// unaffected.
type ProviderHealth struct {
	mu   sync.Mutex
	fail map[string]int
	last map[string]time.Time
}

// NewProviderHealth builds an empty tracker.
func NewProviderHealth() *ProviderHealth {
	return &ProviderHealth{fail: map[string]int{}, last: map[string]time.Time{}}
}

// Record notes one call outcome.
func (h *ProviderHealth) Record(account string, ok bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if ok {
		h.fail[account] = 0
		return
	}
	h.fail[account]++
	h.last[account] = time.Now()
}

// Healthy is false after three consecutive failures, for five minutes after the last one.
func (h *ProviderHealth) Healthy(account string) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fail[account] < 3 || time.Since(h.last[account]) > 5*time.Minute
}

// ---- account management (called by scd for the Admin Console) -------------------------------------------

// SaveAccount creates or updates an account through the audited definer function, and seals new credentials
// when any are supplied. Secrets are write-only: blank values keep the stored generation.
func SaveAccount(ctx context.Context, db *pgxpool.Pool, k PaymentKey, tenantID, siteID, id, provider, merchantRef,
	displayName, currency string, mode Mode, status string, isDefault bool, creds Credentials, operator, reason string) (string, error) {
	var newID string
	var idArg any
	if id != "" {
		idArg = id
	}
	err := db.QueryRow(ctx, `SELECT iam_v2.payment_account_save($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)::text`,
		tenantID, siteID, idArg, provider, merchantRef, displayName, currency, string(mode), status, isDefault,
		operator, reason).Scan(&newID)
	if err != nil {
		return "", err
	}
	if len(creds) == 0 {
		return newID, nil
	}
	gen, err := newUUID()
	if err != nil {
		return newID, err
	}
	sealed, err := SealCredentials(k, tenantID, siteID, newID, gen, creds)
	if err != nil {
		return newID, err
	}
	if _, err := db.Exec(ctx, `SELECT iam_v2.payment_account_set_secret($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		tenantID, siteID, newID, gen, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, int16(sealed.CipherVersion), operator); err != nil {
		return newID, err
	}
	return newID, nil
}

// MergeCredentials returns the stored credentials with every non-blank supplied value replacing its key, so an
// operator can rotate one secret without retyping the others.
func MergeCredentials(stored, supplied Credentials) Credentials {
	out := Credentials{}
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range supplied {
		if strings.TrimSpace(v) != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// OpenStoredCredentials opens an account's current generation with the scd pool (svc_scd holds SELECT).
func OpenStoredCredentials(ctx context.Context, db *pgxpool.Pool, k PaymentKey, tenantID, siteID, accountID string) (Credentials, bool, error) {
	var gen, keyID string
	var nonce, ct []byte
	err := db.QueryRow(ctx, `SELECT id::text, key_id, nonce, ciphertext FROM iam_v2.payment_provider_secret_generations
		 WHERE tenant_id=$1 AND site_id=$2 AND account_id=$3 AND superseded_at IS NULL`,
		tenantID, siteID, accountID).Scan(&gen, &keyID, &nonce, &ct)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credentials{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	c, err := OpenCredentials(k, tenantID, siteID, accountID, gen, keyID, nonce, ct)
	if err != nil {
		return nil, true, fmt.Errorf("stored credentials cannot be opened with this appliance's payment key")
	}
	return c, true, nil
}

// newUUID returns a random (version 4) UUID string.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// AdapterKey selects the adapter instance for a provider account. Paymob adapters are bound to one region, so
// a Paymob account whose "region" credential names another region uses that region's instance.
func AdapterKey(provider string, c Credentials) string {
	if provider == "paymob" {
		if r := strings.ToLower(strings.TrimSpace(c["region"])); r != "" && r != "egypt" {
			return "paymob:" + r
		}
	}
	return provider
}
