package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// No guest PII, card data, credentials, provider payloads or raw PMS material in metrics, logs or audit output: every
// field is a count, an age in seconds or a fixed enumerated word, so the forbidden data is unrepresentable.
// The redaction test seeds secrets into the rows and asserts they cannot be found in the serialised output.

type FinancialHealth struct {
	OutboxQueued     int `json:"outbox_queued"`
	OutboxInFlight   int `json:"outbox_in_flight"`
	OutboxHeld       int `json:"outbox_held_recovery"`
	OutboxOldestAgeS int `json:"outbox_oldest_age_seconds"`
	PostingsUnknown  int `json:"postings_unknown"`
	ReviewQueueOpen  int `json:"review_queue_open"`
	ReviewOldestAgeS int `json:"review_oldest_age_seconds"`

	PaymentsCreated    int `json:"payments_created"`
	PaymentsPending    int `json:"payments_pending"`
	PaymentsUnknown    int `json:"payments_unknown"`
	PaymentsOldestAgeS int `json:"payments_oldest_age_seconds"`

	SettlementsRequired     int `json:"settlements_required"`
	SettlementsInProgress   int `json:"settlements_in_progress"`
	SettlementsManualReview int `json:"settlements_manual_review"`
	SettlementsFailed       int `json:"settlements_failed"`

	RecoveryActive    bool  `json:"recovery_active"`
	RecoveryEpoch     int64 `json:"recovery_epoch"`
	RecoveryHoldsOpen int   `json:"recovery_holds_open"`

	PaymentAccountConfigured bool `json:"payment_account_configured"`
	ProviderEgressEnabled    bool `json:"provider_egress_enabled"`

	// Status is a fixed word (OK, DEGRADED, ATTENTION_REQUIRED, HELD), never a message.
	Status string `json:"status"`
	// Reasons are fixed codes from the closed set below; no free text.
	Reasons []string `json:"reasons"`
}

// The vocabulary is closed: AddReason refuses unknown codes.
const (
	ReasonRecoveryHeld       = "FINANCIAL_RECOVERY_MODE"
	ReasonUnknownOutcomes    = "UNKNOWN_OUTCOMES_AWAITING_REVIEW"
	ReasonReviewBacklog      = "MANUAL_REVIEW_BACKLOG"
	ReasonOutboxStalled      = "POSTING_OUTBOX_STALLED"
	ReasonPaymentsStuck      = "PAYMENTS_STUCK_PENDING"
	ReasonNoPaymentAccount   = "NO_ACTIVE_PAYMENT_ACCOUNT"
	ReasonSettlementsWaiting = "SETTLEMENTS_AWAITING_REVIEW"
)

var knownReasons = map[string]bool{
	ReasonRecoveryHeld: true, ReasonUnknownOutcomes: true, ReasonReviewBacklog: true,
	ReasonOutboxStalled: true, ReasonPaymentsStuck: true, ReasonNoPaymentAccount: true,
	ReasonSettlementsWaiting: true,
}

func (h *FinancialHealth) addReason(code string) {
	if !knownReasons[code] {
		return
	}
	for _, r := range h.Reasons {
		if r == code {
			return
		}
	}
	h.Reasons = append(h.Reasons, code)
}

// Thresholds are constants, not configuration: a tunable threshold is one somebody turns off.
const (
	outboxStalledSeconds  = 900
	reviewBacklogSeconds  = 3600
	paymentStuckSeconds   = 600
	reviewBacklogCountMax = 25
)

func (e *Engine) Health(ctx context.Context, tenantID, siteID string) (FinancialHealth, error) {
	var h FinancialHealth
	err := e.pool.QueryRow(ctx, `
WITH ob AS (
  SELECT count(*) FILTER (WHERE state = 'QUEUED')                            AS queued,
         count(*) FILTER (WHERE state = 'IN_FLIGHT')                        AS inflight,
         count(*) FILTER (WHERE state = 'HELD_RECOVERY')                     AS held,
         coalesce(max(EXTRACT(EPOCH FROM (now() - enqueued_at))
                      ) FILTER (WHERE state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY')), 0) AS oldest
    FROM iam_v2.posting_outbox WHERE tenant_id = $1 AND site_id = $2),
pa AS (
  SELECT count(*) FILTER (WHERE outcome = 'UNKNOWN') AS unknown
    FROM iam_v2.posting_attempts WHERE tenant_id = $1 AND site_id = $2),
rv AS (
  SELECT count(*) FILTER (WHERE terminal_action IS NULL)                     AS open,
         coalesce(max(EXTRACT(EPOCH FROM (now() - updated_at))
                      ) FILTER (WHERE terminal_action IS NULL), 0)           AS oldest
    FROM iam_v2.posting_review_state WHERE tenant_id = $1 AND site_id = $2),
ptx AS (
  SELECT count(*) FILTER (WHERE status = 'CREATED')  AS created,
         count(*) FILTER (WHERE status = 'PENDING')  AS pending,
         count(*) FILTER (WHERE status = 'UNKNOWN')  AS unknown,
         coalesce(max(EXTRACT(EPOCH FROM (now() - coalesce(intent_created_at, now())))
                      ) FILTER (WHERE status IN ('CREATED','PENDING')), 0)   AS oldest
    FROM iam_v2.payment_transactions WHERE tenant_id = $1 AND site_id = $2),
se AS (
  SELECT count(*) FILTER (WHERE status = 'REQUIRED')      AS required,
         count(*) FILTER (WHERE status = 'IN_PROGRESS')   AS inprogress,
         count(*) FILTER (WHERE status = 'MANUAL_REVIEW') AS review,
         count(*) FILTER (WHERE status = 'FAILED')        AS failed
    FROM iam_v2.settlements WHERE tenant_id = $1 AND site_id = $2),
rec AS (
  SELECT coalesce(bool_or(recovery_active), false) AS active,
         coalesce(max(epoch), 0)                   AS epoch,
         coalesce(sum(held_open) FILTER (WHERE released_at IS NULL), 0) AS holds
    FROM iam_v2.v_financial_recovery WHERE tenant_id = $1 AND site_id = $2),
acct AS (
  SELECT EXISTS (SELECT 1 FROM iam_v2.payment_provider_accounts
                  WHERE tenant_id = $1 AND site_id = $2 AND status='ACTIVE' AND is_default) AS configured)
SELECT ob.queued::int, ob.inflight::int, ob.held::int, ob.oldest::int,
       pa.unknown::int, rv.open::int, rv.oldest::int,
       ptx.created::int, ptx.pending::int, ptx.unknown::int, ptx.oldest::int,
       se.required::int, se.inprogress::int, se.review::int, se.failed::int,
       rec.active, rec.epoch::bigint, rec.holds::int, acct.configured
  FROM ob, pa, rv, ptx, se, rec, acct`, tenantID, siteID).
		Scan(&h.OutboxQueued, &h.OutboxInFlight, &h.OutboxHeld, &h.OutboxOldestAgeS,
			&h.PostingsUnknown, &h.ReviewQueueOpen, &h.ReviewOldestAgeS,
			&h.PaymentsCreated, &h.PaymentsPending, &h.PaymentsUnknown, &h.PaymentsOldestAgeS,
			&h.SettlementsRequired, &h.SettlementsInProgress, &h.SettlementsManualReview, &h.SettlementsFailed,
			&h.RecoveryActive, &h.RecoveryEpoch, &h.RecoveryHoldsOpen, &h.PaymentAccountConfigured)
	if err != nil {
		return FinancialHealth{}, classify(err)
	}
	h.ProviderEgressEnabled = e.cfg.ProviderOn()
	h.classify()
	return h, nil
}

// HELD outranks everything: a site in recovery is deliberately stopped, not degraded.
func (h *FinancialHealth) classify() {
	h.Status = "OK"
	worst := 0
	raise := func(level int, status, reason string) {
		h.addReason(reason)
		if level > worst {
			worst, h.Status = level, status
		}
	}
	if h.RecoveryActive {
		raise(3, "HELD", ReasonRecoveryHeld)
	}
	if h.PostingsUnknown > 0 || h.PaymentsUnknown > 0 {
		raise(2, "ATTENTION_REQUIRED", ReasonUnknownOutcomes)
	}
	if h.SettlementsManualReview > 0 {
		raise(2, "ATTENTION_REQUIRED", ReasonSettlementsWaiting)
	}
	if h.ReviewQueueOpen > reviewBacklogCountMax || h.ReviewOldestAgeS > reviewBacklogSeconds {
		raise(2, "ATTENTION_REQUIRED", ReasonReviewBacklog)
	}
	if h.OutboxOldestAgeS > outboxStalledSeconds {
		raise(1, "DEGRADED", ReasonOutboxStalled)
	}
	if h.PaymentsOldestAgeS > paymentStuckSeconds && h.PaymentsPending > 0 {
		raise(1, "DEGRADED", ReasonPaymentsStuck)
	}
	if !h.PaymentAccountConfigured {
		raise(1, "DEGRADED", ReasonNoPaymentAccount)
	}
	if h.Reasons == nil {
		h.Reasons = []string{}
	}
}

// No labels carry identifiers: they would reach every scrape, alert and dashboard, and metrics stores are less
// protected than a financial database.
func (h FinancialHealth) PrometheusText(prefix string) string {
	var b strings.Builder
	m := func(name string, v int) {
		fmt.Fprintf(&b, "%s_%s %d\n", prefix, name, v)
	}
	m("outbox_queued", h.OutboxQueued)
	m("outbox_in_flight", h.OutboxInFlight)
	m("outbox_held_recovery", h.OutboxHeld)
	m("outbox_oldest_age_seconds", h.OutboxOldestAgeS)
	m("postings_unknown", h.PostingsUnknown)
	m("review_queue_open", h.ReviewQueueOpen)
	m("review_oldest_age_seconds", h.ReviewOldestAgeS)
	m("payments_created", h.PaymentsCreated)
	m("payments_pending", h.PaymentsPending)
	m("payments_unknown", h.PaymentsUnknown)
	m("payments_oldest_age_seconds", h.PaymentsOldestAgeS)
	m("settlements_required", h.SettlementsRequired)
	m("settlements_in_progress", h.SettlementsInProgress)
	m("settlements_manual_review", h.SettlementsManualReview)
	m("settlements_failed", h.SettlementsFailed)
	m("recovery_holds_open", h.RecoveryHoldsOpen)
	m("recovery_active", boolInt(h.RecoveryActive))
	m("payment_account_configured", boolInt(h.PaymentAccountConfigured))
	m("provider_egress_enabled", boolInt(h.ProviderEgressEnabled))
	for _, r := range h.Reasons {
		fmt.Fprintf(&b, "%s_condition{code=\"%s\"} 1\n", prefix, r)
	}
	return b.String()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (h FinancialHealth) JSON() ([]byte, error) { return json.Marshal(h) }

// identifierLike exists for the redaction test, which asserts none of these shapes appear in output.
var identifierLike = regexp.MustCompile(
	`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}` +
		`|sc_[0-9a-f]{32}` +
		`|\bacct_[A-Za-z0-9]{6,}`)

// ContainsIdentifier: the operator surface and metrics endpoint assert this on their own output before returning it.
func ContainsIdentifier(s string) bool { return identifierLike.MatchString(s) }
