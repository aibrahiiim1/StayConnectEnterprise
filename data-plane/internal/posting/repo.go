package posting

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo is the durable financial store; policy lives in Gate and the invariants in the database.
type Repo struct{ pool *pgxpool.Pool }

func NewRepo(p *pgxpool.Pool) *Repo { return &Repo{pool: p} }

func (r *Repo) Pool() *pgxpool.Pool { return r.pool }

// nullable renders "" as NULL so an unpinned optional identifier does not fail a composite foreign key.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// InsertPosting writes the posting and its QUEUED outbox row atomically: a posting without an outbox row
// would never be sent, and the reverse would loop on nothing.
func (r *Repo) InsertPosting(ctx context.Context, tx pgx.Tx, p Pinned) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `
INSERT INTO iam_v2.pms_postings
  (tenant_id, site_id, pms_interface_id, settlement_id, purchase_id, stay_id, g_number,
   posting_interface_revision_id, secret_generation_id, posting_type,
   amount_minor, currency, currency_exponent, idempotency_key)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'CHARGE',$10,$11,$12,$13)
RETURNING id::text`,
		p.TenantID, p.SiteID, p.PMSInterfaceID, p.SettlementID, p.PurchaseID, p.StayID, p.GNumber,
		p.PostingInterfaceRevisionID, nullable(p.SecretGenerationID),
		p.AmountMinor, p.Currency, p.CurrencyExponent, p.IdempotencyKey).Scan(&id); err != nil {
		return "", classify(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO iam_v2.posting_outbox (tenant_id, site_id, pms_interface_id, posting_id, state)
VALUES ($1,$2,$3,$4,'QUEUED')`, p.TenantID, p.SiteID, p.PMSInterfaceID, id); err != nil {
		return "", classify(err)
	}
	return id, nil
}

type Claim struct {
	OutboxID  string
	PostingID string
	Pinned    Pinned
	AttemptNo int
}

// ClaimNext claims at most one QUEUED posting for one interface and marks it IN_FLIGHT, or returns
// (nil, nil). SKIP LOCKED keeps lanes independent; the outbox_one_active unique index is what stops one
// posting from being executed twice by two workers.
func (r *Repo) ClaimNext(ctx context.Context, tx pgx.Tx, tenantID, siteID, interfaceID string) (*Claim, error) {
	var c Claim
	err := tx.QueryRow(ctx, `
WITH claimed AS (
  SELECT o.id
    FROM iam_v2.posting_outbox o
   WHERE o.tenant_id = $1 AND o.site_id = $2 AND o.pms_interface_id = $3 AND o.state = 'QUEUED'
   ORDER BY o.id
     FOR UPDATE SKIP LOCKED
   LIMIT 1)
UPDATE iam_v2.posting_outbox o
   SET state = 'IN_FLIGHT'
  FROM claimed, iam_v2.pms_postings p
 WHERE o.id = claimed.id AND p.id = o.posting_id
RETURNING o.id::text, p.id::text, p.settlement_id::text, p.purchase_id::text, p.stay_id::text,
          p.g_number, p.posting_interface_revision_id::text,
          coalesce(p.secret_generation_id::text,''), p.amount_minor, p.currency, p.currency_exponent,
          p.idempotency_key`,
		tenantID, siteID, interfaceID).Scan(
		&c.OutboxID, &c.PostingID, &c.Pinned.SettlementID, &c.Pinned.PurchaseID, &c.Pinned.StayID,
		&c.Pinned.GNumber, &c.Pinned.PostingInterfaceRevisionID, &c.Pinned.SecretGenerationID,
		&c.Pinned.AmountMinor, &c.Pinned.Currency, &c.Pinned.CurrencyExponent, &c.Pinned.IdempotencyKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, classify(err)
	}
	c.Pinned.TenantID, c.Pinned.SiteID, c.Pinned.PMSInterfaceID = tenantID, siteID, interfaceID
	c.Pinned.PostingID = c.PostingID

	// Reads the pin from the objects the posting already points at; it does not re-resolve it.
	if err := tx.QueryRow(ctx, `
SELECT pu.package_revision_id::text, coalesce(pu.settlement_mapping_id::text,''),
       coalesce(sm.posting_code,''), pu.state
  FROM iam_v2.purchases pu
  LEFT JOIN iam_v2.package_settlement_mappings sm ON sm.id = pu.settlement_mapping_id
 WHERE pu.tenant_id = $1 AND pu.site_id = $2 AND pu.id = $3`,
		tenantID, siteID, c.Pinned.PurchaseID).Scan(
		&c.Pinned.PackageRevisionID, &c.Pinned.SettlementMappingID,
		&c.Pinned.PostingCode, &c.Pinned.ExpectPurchaseState); err != nil {
		return nil, classify(err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(max(attempt_no),0)+1 FROM iam_v2.posting_attempts WHERE internal_posting_id = $1`,
		c.PostingID).Scan(&c.AttemptNo); err != nil {
		return nil, classify(err)
	}
	return &c, nil
}

// ReleaseClaim puts a claimed row back to QUEUED. Used when the lane declines to execute (DARK, or an
// evidence refusal that a later operator action could legitimately resolve).
func (r *Repo) ReleaseClaim(ctx context.Context, tx pgx.Tx, outboxID string) error {
	_, err := tx.Exec(ctx, `UPDATE iam_v2.posting_outbox SET state='QUEUED' WHERE id=$1`, outboxID)
	return classify(err)
}

// FinishClaim marks an outbox row terminal (DONE) or parks it for recovery (HELD_RECOVERY).
func (r *Repo) FinishClaim(ctx context.Context, tx pgx.Tx, outboxID, state string) error {
	_, err := tx.Exec(ctx, `UPDATE iam_v2.posting_outbox SET state=$2 WHERE id=$1`, outboxID, state)
	return classify(err)
}

func (r *Repo) AllocatePNumber(ctx context.Context, tx pgx.Tx, tenantID, siteID, interfaceID string) (int64, error) {
	var p int64
	if err := tx.QueryRow(ctx, `SELECT iam_v2.allocate_p_number($1,$2,$3)`,
		tenantID, siteID, interfaceID).Scan(&p); err != nil {
		return 0, classify(err)
	}
	return p, nil
}

func (r *Repo) InsertAttempt(ctx context.Context, tx pgx.Tx, p Pinned, postingID string, attemptNo int, pNumber int64, psSHA256 string) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `
INSERT INTO iam_v2.posting_attempts
  (tenant_id, site_id, internal_posting_id, pms_interface_id, attempt_no, p_number, rn, g_number, sent_at, ps_sha256)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now(), NULLIF($9,''))
RETURNING id::text`,
		p.TenantID, p.SiteID, postingID, p.PMSInterfaceID, attemptNo,
		// p_number is a text column; render it here so the stored value is byte-for-byte the wire value.
		strconv.FormatInt(pNumber, 10), p.RN, p.GNumber, psSHA256).Scan(&id); err != nil {
		return "", classify(err)
	}
	return id, nil
}

func (r *Repo) SettleAttempt(ctx context.Context, tx pgx.Tx, attemptID, outcome, asStatus string) error {
	_, err := tx.Exec(ctx, `
UPDATE iam_v2.posting_attempts
   SET outcome = $2, pa_as_status = $3, response_at = now()
 WHERE id = $1`, attemptID, outcome, nullable(asStatus))
	return classify(err)
}

// AppendAttemptEvent appends to the attempt-event ledger; detail must never carry a secret, card number or guest PII.
func (r *Repo) AppendAttemptEvent(ctx context.Context, tx pgx.Tx, p Pinned, attemptID, eventType, detailJSON string) error {
	if detailJSON == "" {
		detailJSON = "{}"
	}
	_, err := tx.Exec(ctx, `
INSERT INTO iam_v2.posting_attempt_events (tenant_id, site_id, posting_attempt_id, event_type, detail)
VALUES ($1,$2,$3,$4,$5::jsonb)`, p.TenantID, p.SiteID, attemptID, eventType, detailJSON)
	return classify(err)
}

// RecordReview routes a manual-review decision through the single controlled writer. expectedVersion
// makes a concurrent second reviewer fail. reversalAmount applies only to CREATE_REVERSAL (nil reverses
// the whole charge); every other action must pass nil.
func (r *Repo) RecordReview(ctx context.Context, postingID, action, actor, reason, evidenceJSON string, expectedVersion *int, reversalAmount *int64) (string, error) {
	if evidenceJSON == "" {
		evidenceJSON = "{}"
	}
	var id string
	err := r.pool.QueryRow(ctx,
		`SELECT iam_v2.record_posting_review_action($1,$2,$3,$4,$5::jsonb,$6,$7)::text`,
		postingID, action, actor, reason, evidenceJSON, expectedVersion, reversalAmount).Scan(&id)
	if err != nil {
		return "", classify(err)
	}
	return id, nil
}

// State is one row of the derived read model; nothing here is stored.
type State struct {
	PostingID                  string
	ExecutionState             string
	LatestAttemptNo            *int
	LatestPNumber              *string
	OutboxState                *string
	HasUnknownHistory          bool
	AwaitingManualReview       bool
	TerminalReviewAction       *string
	ReviewVersion              *int
	AttemptCount               int64
	UnknownAttemptCount        int64
	LatestPAStatusValue        *string
	RetryAuthorizationConsumed bool
	FreshnessBlock             *string
}

// LatestPAStatus returns the latest AS, or "" for no answer, so "no answer" is never mistaken for one.
func (s State) LatestPAStatus() string {
	if s.LatestPAStatusValue == nil {
		return ""
	}
	return *s.LatestPAStatusValue
}

func (r *Repo) ReadState(ctx context.Context, postingID string) (State, error) {
	var s State
	err := r.pool.QueryRow(ctx, `
SELECT posting_id::text, execution_state, latest_attempt_no, latest_p_number, outbox_state,
       coalesce(has_unknown_history,false), coalesce(awaiting_manual_review,false),
       terminal_review_action, review_version, coalesce(attempt_count,0), coalesce(unknown_attempt_count,0),
       latest_pa_as_status, coalesce(retry_authorization_consumed,false), freshness_block
  FROM iam_v2.posting_execution_state WHERE posting_id = $1`, postingID).Scan(
		&s.PostingID, &s.ExecutionState, &s.LatestAttemptNo, &s.LatestPNumber, &s.OutboxState,
		&s.HasUnknownHistory, &s.AwaitingManualReview, &s.TerminalReviewAction, &s.ReviewVersion,
		&s.AttemptCount, &s.UnknownAttemptCount,
		&s.LatestPAStatusValue, &s.RetryAuthorizationConsumed, &s.FreshnessBlock)
	if err != nil {
		return State{}, classify(err)
	}
	return s, nil
}

// classify maps a database refusal to the typed financial code, matching what Gate would return.
func classify(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, m := range []struct {
		needle string
		code   Code
	}{
		{"POSTING_TARGET_UNSET", ErrPostingTargetUnset},
		{"POSTING_RESERVATION_MISMATCH", ErrReservationMismatch},
		{"ATTEMPT_RESERVATION_MISMATCH", ErrReservationMismatch},
		{"ROOM_CHARGE_UNRESOLVED", ErrStayChargeOpen},
		{"POSTING_NO_STAY", ErrReservationMismatch},
		{"POSTING_NOT_ALLOWED", ErrPostingNotAllowed},
		{"INTERFACE_CURRENCY_NOT_ONBOARDED", ErrInterfaceNoCurrency},
		{"POSTING_CURRENCY_MISMATCH", ErrCurrencyMismatch},
		{"PURCHASE_CURRENCY_MISMATCH", ErrCurrencyMismatch},
		{"PACKAGE_CURRENCY_MISMATCH", ErrCurrencyMismatch},
		{"POSTING_CURRENCY_UNSET", ErrCurrencyMismatch},
		{"PURCHASE_CURRENCY_UNSET", ErrCurrencyMismatch},
		{"PACKAGE_CURRENCY_UNSET", ErrCurrencyMismatch},
		{"attempt_rn_verified", ErrRNMissing},
		{"attempt_gnumber_verified", ErrGNumberMissing},
		{"attempt_rn_wire_safe", ErrRNNotWireSafe},
		{"attempt_gnumber_wire_safe", ErrGNumberNotWireSafe},
		{"attempt_pnumber_wire_safe", ErrWireFieldInvalid},
		{"ATTEMPT_IN_FLIGHT", ErrAlreadyInFlight},
		{"RETRY_REQUIRES_REVIEW", ErrRetryNotAuthed},
		{"ATTEMPT_SEQUENCE", ErrRetryNotAuthed},
		{"REVIEW_CONFLICT", ErrReviewConflict},
		{"REVIEW_ALREADY_DECIDED", ErrReviewConflict},
		{"REVIEW_VERSION_STALE", ErrReviewStale},
		{"REVIEW_ACTION_UNKNOWN", ErrReviewConflict},
		{"REVIEW_WRITER_ONLY", ErrReviewConflict},
		{"REVIEW_REVERSAL_REFUSED", ErrReviewConflict},
		{"REVERSAL_NOT_EXECUTABLE", ErrReversalDisabled},
		{"REVERSAL_WRITER_ONLY", ErrReversalDisabled},
		{"REVERSAL_EXCEEDS_CHARGE", ErrReviewConflict},
	} {
		if contains(msg, m.needle) {
			return &Error{Code: m.code, Msg: m.needle}
		}
	}
	// Unrecognised: keep the database's first line (constraint or SQLSTATE); the ledgers hold no guest PII.
	if i := indexByte(msg, 10); i >= 0 {
		msg = msg[:i]
	}
	return &Error{Code: ErrRepo, Msg: msg}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
