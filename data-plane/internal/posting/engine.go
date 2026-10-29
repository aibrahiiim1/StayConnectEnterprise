package posting

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/postinghandoff"
)

// Engine creates postings under the fail-closed gate and executes them through per-interface outbox lanes.
//
// Construction boundary: no exported function accepts a Config or a Transport. NewProductionEngine(repo) is the
// whole exported financial surface; config comes from the environment and the transport from the internal
// factory. The test seam lives in export_test.go, so production code cannot reference it.
type Engine struct {
	cfg       Config
	repo      *Repo
	transport Transport
	gate      Gate
}

// newEngine is the single constructor and always wraps the transport in the DARK guard.
func newEngine(cfg Config, repo *Repo, inner Transport) *Engine {
	return &Engine{cfg: cfg, repo: repo, transport: newDarkGuard(cfg, inner)}
}

// NewProductionEngine reads flags from the real process environment, never an injected reader (that would let a
// caller choose its own posture), so enabling transmission needs both a deployment change and a factory change.
func NewProductionEngine(repo *Repo) (*Engine, error) {
	cfg, err := LoadConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	inner, err := productionTransport(cfg)
	if err != nil {
		return nil, err
	}
	return newEngine(cfg, repo, inner), nil
}

// productionTransport is the hand-off to pmsd, which owns the single FIAS connection (handoff_transport.go,
// internal/pmsd/financial_relay.go). It exists only when transmission is ON; while DARK it is nil and DarkGuard refuses first.
func productionTransport(cfg Config) (Transport, error) {
	if !cfg.TransmitOn() {
		return nil, nil
	}
	return newHandoffTransport(), nil
}

func (e *Engine) Config() Config { return e.cfg }

// TransportRefusals lets a test assert the worker tried and was stopped, rather than inferring darkness from empty tables.
func (e *Engine) TransportRefusals() int {
	if g, ok := e.transport.(*DarkGuard); ok {
		return g.Refusals()
	}
	return 0
}

func (e *Engine) LastRefusedBody() string {
	if g, ok := e.transport.(*DarkGuard); ok {
		return g.LastRefusedBody()
	}
	return ""
}

// CreatePosting runs the fail-closed gate first. A refusal rolls back the posting and outbox rows, consumes no
// P# (allocated at transmission time) and produces no wire bytes.
func (e *Engine) CreatePosting(ctx context.Context, p Pinned) (string, error) {
	if !e.cfg.PostingOn() {
		return "", fail(ErrConfig, "the phase-4 posting domain is disabled")
	}
	tx, err := e.repo.pool.Begin(ctx)
	if err != nil {
		return "", fail(ErrRepo, "could not begin the financial transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	snap, err := LoadSnapshot(ctx, tx, p)
	if err != nil {
		return "", err
	}
	if err := e.gate.Check(p, snap); err != nil {
		return "", err
	}
	id, err := e.repo.InsertPosting(ctx, tx, p)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fail(ErrRepo, "could not commit the financial transaction")
	}
	return id, nil
}

type Outcome struct {
	Claimed   bool
	PostingID string
	AttemptNo int
	PNumber   int64
	Result    string // POSTED | REJECTED | NOT_SENT | UNKNOWN | DECLINED | ABORTED
	// NotSentReason is pmsd's bounded reason when it proved nothing was written (e.g. ROOM_CHANGED).
	NotSentReason string
	ASStatus      string
	RefusedFor    Code
	// Parked is true when this refusal was the one that moved the row to HELD_RECOVERY (AD-41).
	Parked bool
}

// RunOnce executes at most one queued posting for one PMS interface. A lane is (tenant, site, interface): it
// claims only its own rows and takes no lock another lane wants.
//
// The DARK check runs after the claim and evidence re-verification but before the P# is allocated, so a DARK
// worker consumes no P#, writes no attempt, produces no bytes, and releases the claim.
func (e *Engine) RunOnce(ctx context.Context, tenantID, siteID, interfaceID string) (Outcome, error) {
	var out Outcome
	if !e.cfg.OutboxOn() {
		return out, fail(ErrConfig, "the phase-4 outbox worker is disabled")
	}
	tx, err := e.repo.pool.Begin(ctx)
	if err != nil {
		return out, fail(ErrRepo, "could not begin the lane transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	claim, err := e.repo.ClaimNext(ctx, tx, tenantID, siteID, interfaceID)
	if err != nil {
		return out, err
	}
	if claim == nil {
		return out, tx.Commit(ctx) // nothing queued for this lane
	}
	out.Claimed, out.PostingID, out.AttemptNo = true, claim.PostingID, claim.AttemptNo

	// Lock the stay so a room move cannot land between reading the room and committing the attempt (pmsd re-checks at write time).
	if _, err := tx.Exec(ctx, `SELECT iam_v2.p4_lock_posting_stay($1)`, claim.PostingID); err != nil {
		return e.decline(ctx, tx, &out, claim, classify(err))
	}
	// Re-verify the pinned evidence (not re-resolution): a stay that checked out since authorization stops here.
	snap, err := LoadSnapshot(ctx, tx, claim.Pinned)
	if err != nil {
		return e.decline(ctx, tx, &out, claim, err)
	}
	// G# is stable, RN is mutable: the reservation is the pin, the room is read now. A room move re-targets the room
	// and does not fail the purchase; earlier attempts keep, immutably, the room they carried.
	claim.Pinned.RN = snap.StayRoomNumber
	if err := e.gate.CheckFor(PurposeExecute, claim.Pinned, snap); err != nil {
		if reason := presendAbortReason(err); reason != "" {
			return e.abortBeforeSend(ctx, tx, &out, claim, reason, err)
		}
		return e.decline(ctx, tx, &out, claim, err)
	}

	if e.cfg.Dark() {
		// Offer a complete PS to the guard so the refusal proves it was stopped at the wire; the placeholder P# is local.
		body, _ := BuildPS(PSRequest{RN: claim.Pinned.RN, GNumber: claim.Pinned.GNumber,
			AmountMinor: claim.Pinned.AmountMinor, PostingCode: claim.Pinned.PostingCode, PNumber: 1})
		_, terr := e.transport.SendPS(ctx, interfaceID, 1, body)
		if terr == nil {
			// A send accepted while DARK is a defect; refuse loudly.
			return out, fail(ErrDarkNoEgress, "the financial transport transmitted while DARK")
		}
		if err := e.repo.ReleaseClaim(ctx, tx, claim.OutboxID); err != nil {
			return out, err
		}
		out.Result, out.RefusedFor = "DECLINED", ErrDarkNoEgress
		if err := tx.Commit(ctx); err != nil {
			return out, fail(ErrRepo, "could not commit the DARK decline")
		}
		return out, nil
	}

	pn, err := e.repo.AllocatePNumber(ctx, tx, tenantID, siteID, interfaceID)
	if err != nil {
		return out, err
	}
	out.PNumber = pn
	body, err := BuildPS(PSRequest{RN: claim.Pinned.RN, GNumber: claim.Pinned.GNumber,
		AmountMinor: claim.Pinned.AmountMinor, PostingCode: claim.Pinned.PostingCode, PNumber: pn})
	if err != nil {
		return e.decline(ctx, tx, &out, claim, err)
	}
	// The attempt records the SHA-256 of the exact bytes; pmsd carries only a command the database confirms
	// (p4_posting_command_authorised), so no un-authorised byte sequence can reach the PMS.
	attemptID, err := e.repo.InsertAttempt(ctx, tx, claim.Pinned, claim.PostingID, claim.AttemptNo, pn, psHash(body))
	if err != nil {
		return out, err
	}
	// Commit before the bytes go out so a crash mid-send leaves evidence for recovery.
	if err := tx.Commit(ctx); err != nil {
		return out, fail(ErrRepo, "could not commit the attempt before transmission")
	}

	pa, sendErr := e.transport.SendPS(ctx, interfaceID, pn, body)
	// Verify correlation here rather than trust the transport: a PA with another P# never ACKs this attempt, and
	// since the PS was transmitted the outcome is UNKNOWN.
	if sendErr == nil && pa != nil && pa.PNumber != pn {
		pa, sendErr = nil, fail(ErrPAWrongPNumber,
			"the PA answers a different protocol-attempt reference; this attempt is unresolved")
	}
	return e.settle(ctx, claim, attemptID, out, pa, sendErr)
}

// RecoverOrphanedAttempts turns attempts left SENDING by a stopped process into UNKNOWN (outbox HELD_RECOVERY,
// settlement MANUAL_REVIEW); nothing resends them. olderThan is at least AnswerDeadline plus a minute so an
// in-progress send is never judged.
func (e *Engine) RecoverOrphanedAttempts(ctx context.Context, tenantID, siteID string, olderThan time.Duration) (int, error) {
	if olderThan < AnswerDeadline+time.Minute {
		olderThan = AnswerDeadline + time.Minute
	}
	rows, err := e.repo.pool.Query(ctx, `
SELECT a.id::text, a.internal_posting_id::text, a.pms_interface_id::text, o.id::text
  FROM iam_v2.posting_attempts a
  JOIN iam_v2.posting_outbox o ON o.posting_id = a.internal_posting_id AND o.state = 'IN_FLIGHT'
 WHERE a.tenant_id = $1 AND a.site_id = $2 AND a.outcome = 'SENDING'
   AND a.sent_at < now() - make_interval(secs => $3)`, tenantID, siteID, olderThan.Seconds())
	if err != nil {
		return 0, classify(err)
	}
	type orphan struct{ attempt, posting, iface, outbox string }
	var list []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.attempt, &o.posting, &o.iface, &o.outbox); err != nil {
			rows.Close()
			return 0, classify(err)
		}
		list = append(list, o)
	}
	rows.Close()
	n := 0
	for _, o := range list {
		tx, err := e.repo.pool.Begin(ctx)
		if err != nil {
			return n, fail(ErrRepo, "could not begin orphan recovery")
		}
		p := Pinned{TenantID: tenantID, SiteID: siteID, PMSInterfaceID: o.iface}
		ok := e.repo.SettleAttempt(ctx, tx, o.attempt, "UNKNOWN", "") == nil &&
			e.repo.AppendAttemptEvent(ctx, tx, p, o.attempt, "UNKNOWN_NO_CONCLUSIVE_PA",
				`{"record":"PA","classification":"UNKNOWN_WORKER_STOPPED_BEFORE_OUTCOME"}`) == nil &&
			e.repo.FinishClaim(ctx, tx, o.outbox, "HELD_RECOVERY") == nil
		if !ok || tx.Commit(ctx) != nil {
			_ = tx.Rollback(ctx)
			return n, fail(ErrRepo, "could not record an orphaned attempt as UNKNOWN")
		}
		if _, err := e.repo.pool.Exec(ctx, `SELECT iam_v2.p4_posting_settlement_outcome($1)`, o.posting); err != nil {
			return n, classify(err)
		}
		n++
	}
	return n, nil
}

// psHash is the hand-off's own command identity, shared rather than copied (audit P6 CC2): pmsd checks the
// attempt's recorded hash against postinghandoff.BodyHash.
func psHash(body string) string { return postinghandoff.BodyHash(body) }

// presendAbortReason names the pre-send failures that end the charge as definitely not posted; anything else
// (interface lifecycle, onboarding, currency) only declines and leaves the work queued.
func presendAbortReason(err error) string {
	switch CodeOf(err) {
	case ErrStayNotInHouse:
		return "STAY_NOT_IN_HOUSE"
	case ErrPostingNotAllowed:
		return "STAY_BLOCKED"
	case ErrReservationMismatch:
		return "RESERVATION_MISMATCH"
	case ErrRNMissing, ErrRNNotWireSafe:
		return "ROOM_UNRESOLVED"
	case ErrInterfaceNotFresh:
		return "DATA_STALE"
	}
	return ""
}

// abortBeforeSend ends a charge that provably never reached the PMS (settlement fails, nothing is granted). A
// charge with any possibly-transmitted attempt goes back to manual review instead.
func (e *Engine) abortBeforeSend(ctx context.Context, tx pgx.Tx, out *Outcome, claim *Claim, reason string, cause error) (Outcome, error) {
	var mayHaveSent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.posting_attempts
	                             WHERE internal_posting_id = $1 AND outcome <> 'NOT_SENT'`, claim.PostingID).Scan(&mayHaveSent); err != nil {
		return *out, classify(err)
	}
	if mayHaveSent > 0 {
		if err := e.repo.FinishClaim(ctx, tx, claim.OutboxID, "HELD_RECOVERY"); err != nil {
			return *out, err
		}
		out.Result, out.RefusedFor = "DECLINED", CodeOf(cause)
		if err := tx.Commit(ctx); err != nil {
			return *out, fail(ErrRepo, "could not commit the return to review")
		}
		return *out, cause
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.p4_posting_abort_before_send($1, $2)`, claim.PostingID, reason); err != nil {
		return *out, classify(err)
	}
	out.Result, out.RefusedFor = "ABORTED", CodeOf(cause)
	if err := tx.Commit(ctx); err != nil {
		return *out, fail(ErrRepo, "could not commit the pre-send abort")
	}
	return *out, nil
}

func (e *Engine) decline(ctx context.Context, tx pgx.Tx, out *Outcome, claim *Claim, cause error) (Outcome, error) {
	// A refusal backs the row off (2 s doubling to 5 min) and, at the site's N-th refusal of the same row, parks it so the
	// lane moves on (audit AD-41). Nothing was sent, so no P# was consumed for it.
	state, rerr := e.repo.Defer(ctx, tx, claim.OutboxID, "DECLINED", string(CodeOf(cause)))
	if rerr != nil {
		return *out, rerr
	}
	out.Parked = state == "PARKED"
	out.Result, out.RefusedFor = "DECLINED", CodeOf(cause)
	if cerr := tx.Commit(ctx); cerr != nil {
		return *out, fail(ErrRepo, "could not commit the decline")
	}
	return *out, cause
}

// settle applies the attempt's terminal outcome (the UNKNOWN contract):
//
//	answered, confirmed     -> ACKED; a non-OK status means "not posted" only if the vendor confirmed that code
//	                           for this interface (iam_v2.p4_answer_effect), otherwise UNKNOWN
//	provably not sent       -> NOT_SENT; outbox QUEUED, a new attempt is fine
//	sent, no matched PA     -> UNKNOWN; HELD_RECOVERY and nothing retries it (no timer, backoff or second P#)
//	                           until an audited CONFIRM_NOT_POSTED_RETRY, which the database requires for attempt 2
func (e *Engine) settle(ctx context.Context, claim *Claim, attemptID string, out Outcome, pa *PA, sendErr error) (Outcome, error) {
	// Audit P6 L8: the outcome of bytes already on the wire is recorded even when the worker is stopping; a
	// cancelled context here would leave an acknowledged charge SENDING, and recovery would make it UNKNOWN.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	tx, err := e.repo.pool.Begin(ctx)
	if err != nil {
		return out, fail(ErrRepo, "could not begin the settlement transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var outcome, as, outboxState, event, reason string
	switch {
	case sendErr == nil && pa != nil && e.answerEffect(ctx, claim.Pinned.PMSInterfaceID, pa.AS) == "UNPROVEN":
		// Matched PA whose status is not vendor-confirmed: effect unproven, so UNKNOWN (manual review, never retry).
		outcome, as = "UNKNOWN", pa.AS
		outboxState, event = "HELD_RECOVERY", "UNKNOWN_UNCONFIRMED_ANSWER"
		out.Result, out.ASStatus = "UNKNOWN", pa.AS
	case sendErr == nil && pa != nil:
		outcome, as = "ACKED", pa.AS
		outboxState, event = "DONE", "PA_MATCHED"
		if pa.Posted() {
			out.Result = "POSTED"
		} else {
			out.Result = "REJECTED"
		}
		out.ASStatus = pa.AS
	case NotTransmitted(sendErr):
		outcome, outboxState, event = "NOT_SENT", "QUEUED", "NOT_SENT"
		reason = notSentReason(sendErr)
		out.Result, out.RefusedFor, out.NotSentReason = "NOT_SENT", CodeOf(sendErr), reason
	default:
		// Timeout, torn connection after the write, unparseable or unmatched answer: the PS may have been applied.
		outcome, outboxState, event = "UNKNOWN", "HELD_RECOVERY", "UNKNOWN_NO_CONCLUSIVE_PA"
		out.Result = "UNKNOWN"
		if sendErr != nil {
			out.RefusedFor = CodeOf(sendErr)
		}
	}
	if err := e.repo.SettleAttempt(ctx, tx, attemptID, outcome, as); err != nil {
		return out, err
	}
	fields := map[string]string{"record": RecordPA, "as": as, "classification": event}
	if reason != "" {
		fields["record"], fields["reason"] = RecordPS, reason
	}
	detail, _ := json.Marshal(fields)
	if err := e.repo.AppendAttemptEvent(ctx, tx, claim.Pinned, attemptID, event, string(detail)); err != nil {
		return out, err
	}
	if outcome == "NOT_SENT" && !strings.HasPrefix(reason, "ROOM_CHANGED") {
		// Provably not sent: back off instead of re-claiming on the next tick (it would burn a P# and an attempt row
		// every two seconds for as long as the link is down). Never parks: the link is not this row's fault.
		// A room move is the exception: the stale command was refused because the guest really moved, and the very next
		// attempt carries the new room, so it is not retried against an unchanged condition.
		if _, err := e.repo.Defer(ctx, tx, claim.OutboxID, "NOT_SENT", reason); err != nil {
			return out, err
		}
	} else if err := e.repo.FinishClaim(ctx, tx, claim.OutboxID, outboxState); err != nil {
		return out, err
	}
	if err := tx.Commit(ctx); err != nil {
		return out, fail(ErrRepo, "could not commit the settlement")
	}
	// Settlement follows the PMS: PA=OK grants, any other PA fails, UNKNOWN goes to manual review. Idempotent, so a
	// crash before this is repaired by ApplySettlementOutcomes.
	if outcome == "ACKED" || outcome == "UNKNOWN" {
		if _, err := e.repo.pool.Exec(ctx, `SELECT iam_v2.p4_posting_settlement_outcome($1)`, claim.PostingID); err != nil {
			return out, classify(err)
		}
	}
	if outcome == "UNKNOWN" {
		return out, fail(ErrUnknownTerminal,
			"the attempt is UNKNOWN and will not be retried without an audited review decision")
	}
	return out, nil
}

// answerEffect returns POSTED, a NOT_POSTED_* effect or UNPROVEN; any doubt, including a failed lookup, is UNPROVEN (treated as UNKNOWN).
func (e *Engine) answerEffect(ctx context.Context, interfaceID, as string) string {
	var eff string
	if err := e.repo.pool.QueryRow(ctx, `SELECT iam_v2.p4_answer_effect($1, $2)`, interfaceID, as).Scan(&eff); err != nil {
		return "UNPROVEN"
	}
	return eff
}

func notSentReason(err error) string {
	var r interface{ NotSentReason() string }
	if errors.As(err, &r) {
		return r.NotSentReason()
	}
	return ""
}

// Requeue is the only path out of UNKNOWN and decides nothing: it needs an audited CONFIRM_NOT_POSTED_RETRY
// already recorded via iam_v2.record_posting_review_action, and the database refuses an attempt that decision
// did not authorize.
func (e *Engine) Requeue(ctx context.Context, postingID string) error {
	if !e.cfg.ReviewOn() {
		return fail(ErrConfig, "the phase-4 financial review surface is disabled")
	}
	ct, err := e.repo.pool.Exec(ctx, `
UPDATE iam_v2.posting_outbox o
   SET state='QUEUED'
 WHERE o.posting_id = $1 AND o.state = 'HELD_RECOVERY'
   AND EXISTS (SELECT 1 FROM iam_v2.posting_review_state rs
                WHERE rs.posting_id = o.posting_id
                  AND rs.terminal_action = 'CONFIRM_NOT_POSTED_RETRY'
                  AND rs.retry_authorized_attempt_no IS NOT NULL)`, postingID)
	if err != nil {
		return classify(err)
	}
	if ct.RowsAffected() == 0 {
		return fail(ErrRetryNotAuthed,
			"no audited CONFIRM_NOT_POSTED_RETRY authorizes requeueing this posting")
	}
	return nil
}

// ApplySettlementOutcomes repairs a crash between recording the PA and moving the settlement. Idempotent.
func (e *Engine) ApplySettlementOutcomes(ctx context.Context, tenantID, siteID string) error {
	rows, err := e.repo.pool.Query(ctx, `
SELECT p.id::text FROM iam_v2.pms_postings p
  JOIN iam_v2.settlements se ON se.id = p.settlement_id
 WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.posting_type='CHARGE' AND se.method='PMS_POSTING'
   AND se.status = 'IN_PROGRESS'
   AND EXISTS (SELECT 1 FROM iam_v2.posting_attempts a WHERE a.internal_posting_id = p.id
                AND a.outcome IN ('ACKED','UNKNOWN'))
 LIMIT 100`, tenantID, siteID)
	if err != nil {
		return classify(err)
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
		if _, err := e.repo.pool.Exec(ctx, `SELECT iam_v2.p4_posting_settlement_outcome($1)`, id); err != nil {
			return classify(err)
		}
	}
	return nil
}
