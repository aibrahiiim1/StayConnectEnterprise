package payment

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

// Audit AD-43 (P6 L1): a capture that happens after a checkout was marked FAILED or UNKNOWN used to go unnoticed.
//
// RecheckLateCaptures asks the provider, READ-ONLY (QueryStatus, the same call the reconciler makes), about the charges of the
// last N days (a per-site setting, default 7) that OneGate recorded as failed or unknown. When the provider reports a capture,
// it opens ONE review case for finance and does nothing else: it does not apply an outcome, does not change the charge, the
// settlement or the purchase, and grants nothing. Finance refunds at the provider or grants by hand, then resolves the case.
//
// It is dormant: RecheckEnabled is false unless the deployer switched STAYCONNECT_PHASE4_PAYMENT_RECHECK on (and the provider is
// on), so while it is off this method returns before any database read or provider call.

// ErrRecheckOff: the late-capture re-query is not deployed.
var ErrRecheckOff = errors.New("payment: the late-capture re-query is not enabled")

// RecheckWindow is the site's setting: how many days back a failed or unknown charge is still asked about.
func (e *CheckoutEngine) RecheckWindow(ctx context.Context, tenantID, siteID string) int {
	days := 7
	if e.Runtime == nil {
		return days
	}
	var d int
	var version int64
	var isDefault bool
	if err := e.Runtime.QueryRow(ctx, `SELECT window_days, config_version, is_default FROM iam_v2.card_recheck_settings_get($1,$2)`,
		tenantID, siteID).Scan(&d, &version, &isDefault); err == nil && d >= 1 && d <= 30 {
		days = d
	}
	return days
}

// RecheckLateCaptures returns how many review cases it opened.
func (e *CheckoutEngine) RecheckLateCaptures(ctx context.Context, tenantID, siteID string, limit int) (int, error) {
	if e == nil || !e.RecheckEnabled {
		return 0, ErrRecheckOff
	}
	if !e.Configured() {
		return 0, ErrNoAdapter
	}
	rows, err := e.Runtime.Query(ctx, `SELECT transaction_id::text FROM iam_v2.p4_late_capture_candidates($1,$2,$3,$4)`,
		tenantID, siteID, e.RecheckWindow(ctx, tenantID, siteID), limit)
	if err != nil {
		return 0, classify(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("late-capture re-query list ended early", "err", err)
	}
	opened := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		t, err := e.loadTxn(ctx, tenantID, siteID, id)
		if err != nil {
			continue
		}
		acct, err := e.accountForTxn(ctx, t)
		if err != nil {
			continue
		}
		ad := e.Adapters[AdapterKey(t.provider, acct.Credentials)]
		if ad == nil || (acct.Mode == ModeLive && !e.LiveAllowed) {
			continue
		}
		session := ""
		if t.sessionRef != nil {
			session = *t.sessionRef
		}
		res, qerr := ad.QueryStatus(ctx, StatusQuery{ClientRef: t.clientRef, ProviderSessionRef: session,
			MerchantAccount: acct.MerchantRef, Mode: acct.Mode, Credentials: acct.Credentials, CreatedAt: t.created})
		e.Health.Record(acct.ID, qerr == nil)
		// Asked, whatever the answer: the next pass spaces itself six hours later.
		if _, merr := e.Runtime.Exec(ctx, `SELECT iam_v2.p4_mark_late_checked($1,$2,$3)`, tenantID, siteID, id); merr != nil {
			slog.Warn("late-capture re-query could not record that it asked", "err", merr)
		}
		if qerr != nil || res.State != CheckoutCaptured {
			continue
		}
		ref := res.ProviderTxnRef
		if strings.TrimSpace(ref) == "" {
			ref = t.clientRef
		}
		var r string
		if err := e.Outcome.QueryRow(ctx, `SELECT iam_v2.p4_open_late_capture_review($1,$2,$3,$4,$5,$6)`,
			tenantID, siteID, id, ref, res.AmountMinor, strings.ToUpper(strings.TrimSpace(res.Currency))).Scan(&r); err != nil {
			slog.Error("a late capture was reported by the provider but its review case was NOT opened; finance review required",
				"transaction", id, "err", err)
			continue
		}
		if r == "OPENED" {
			opened++
			slog.Error("card checkout recorded as failed was captured by the provider: a review case was opened for finance",
				"transaction", id)
		}
	}
	return opened, nil
}

// LateRecheckEvery is how often the background loop asks; the per-charge spacing (6 hours) is in the database.
const LateRecheckEvery = 6 * time.Hour
