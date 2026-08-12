package posting

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Engine is the Posting domain core: creation under the fail-closed gate, and execution through the
// per-interface outbox lanes.
type Engine struct {
	Cfg       Config
	Repo      *Repo
	Transport Transport
	Gate      Gate
}

// NewEngine builds an engine. The transport is always wrapped in a DarkGuard, so an engine constructed
// anywhere in this codebase is DARK unless the flags say otherwise — there is no constructor that produces
// an unguarded financial sender.
func NewEngine(cfg Config, repo *Repo, inner Transport) *Engine {
	return &Engine{Cfg: cfg, Repo: repo, Transport: NewDarkGuard(cfg, inner)}
}

// CreatePosting runs the fail-closed gate and, only if it passes, durably creates the posting and queues it.
//
// The ordering is the guarantee this whole milestone rests on, so it is worth being explicit about what a
// refusal costs. When the gate refuses:
//
//	no posting row exists          — the transaction is rolled back
//	no outbox row exists           — same transaction
//	NO P# was consumed             — P# is allocated at TRANSMISSION time, not here, so a refused
//	                                 creation cannot burn a protocol reference even in principle
//	no financial wire bytes exist  — nothing has reached a Transport
//
// It returns the new posting id.
func (e *Engine) CreatePosting(ctx context.Context, p Pinned) (string, error) {
	if !e.Cfg.PostingOn() {
		return "", fail(ErrConfig, "the phase-4 posting domain is disabled")
	}
	tx, err := e.Repo.pool.Begin(ctx)
	if err != nil {
		return "", fail(ErrRepo, "could not begin the financial transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	snap, err := LoadSnapshot(ctx, tx, p)
	if err != nil {
		return "", err
	}
	if err := e.Gate.Check(p, snap); err != nil {
		return "", err
	}
	id, err := e.Repo.InsertPosting(ctx, tx, p)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fail(ErrRepo, "could not commit the financial transaction")
	}
	return id, nil
}

// Outcome is what one lane step did.
type Outcome struct {
	Claimed    bool
	PostingID  string
	AttemptNo  int
	PNumber    int64
	Result     string // POSTED | REJECTED | NOT_SENT | UNKNOWN | DECLINED
	ASStatus   string
	RefusedFor Code
}

// RunOnce executes at most ONE queued posting for ONE PMS interface.
//
// Lane discipline: a lane is (tenant, site, interface). It claims only its own rows, it takes no lock any
// other lane wants, and a lane that is failing or slow has no way to hold up another interface's money.
//
// DARK ordering, stated precisely because it is the no-egress proof: the DARK check happens AFTER the row
// is claimed and the evidence re-verified, and BEFORE the P# is allocated. So a DARK worker that is
// genuinely running, genuinely finds queued work and genuinely re-validates it still consumes no protocol
// reference, writes no attempt and produces no bytes — and the claim is released so the work is not lost.
func (e *Engine) RunOnce(ctx context.Context, tenantID, siteID, interfaceID string) (Outcome, error) {
	var out Outcome
	if !e.Cfg.OutboxOn() {
		return out, fail(ErrConfig, "the phase-4 outbox worker is disabled")
	}
	tx, err := e.Repo.pool.Begin(ctx)
	if err != nil {
		return out, fail(ErrRepo, "could not begin the lane transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	claim, err := e.Repo.ClaimNext(ctx, tx, tenantID, siteID, interfaceID)
	if err != nil {
		return out, err
	}
	if claim == nil {
		return out, tx.Commit(ctx) // nothing queued for this lane
	}
	out.Claimed, out.PostingID, out.AttemptNo = true, claim.PostingID, claim.AttemptNo

	// Re-verify the PINNED evidence. This is not re-resolution: every identifier comes from the durable
	// posting row, and the check is that those exact objects are still in a state that authorizes the
	// charge. A stay that checked out between authorization and transmission stops the charge here.
	snap, err := LoadSnapshot(ctx, tx, claim.Pinned)
	if err != nil {
		return e.decline(ctx, tx, &out, claim, err)
	}
	// RN and G# are read from the pinned stay and folio and then verified by the gate against those same
	// objects, so an attempt can never carry a room number the pinned stay does not have.
	claim.Pinned.RN, claim.Pinned.GNumber = snap.StayRoomNumber, snap.FolioExternalID
	if claim.AttemptNo > 1 {
		// A retry MUST reuse the targeting evidence of the attempt it is retrying. If the PMS has since
		// moved the guest, the correct action is to stop, not to re-target the money.
		prevRN, prevG, err := e.previousTargeting(ctx, tx, claim.PostingID, claim.AttemptNo-1)
		if err != nil {
			return e.decline(ctx, tx, &out, claim, err)
		}
		if prevRN != claim.Pinned.RN || prevG != claim.Pinned.GNumber {
			return e.decline(ctx, tx, &out, claim,
				fail(ErrEvidenceStale, "targeting evidence changed since the attempt being retried"))
		}
	}
	if err := e.Gate.Check(claim.Pinned, snap); err != nil {
		return e.decline(ctx, tx, &out, claim, err)
	}

	// ---- the DARK boundary --------------------------------------------------------------------------
	if e.Cfg.Dark() {
		// Build a complete, valid PS anyway and offer it to the guard, so the refusal is evidence that a
		// real financial record was constructed and stopped AT THE WIRE rather than never reached. The
		// placeholder P# below is a local value: no P# is allocated, so nothing durable is consumed.
		body, _ := BuildPS(PSRequest{RN: claim.Pinned.RN, GNumber: claim.Pinned.GNumber,
			AmountMinor: claim.Pinned.AmountMinor, PostingCode: claim.Pinned.PostingCode, PNumber: 1})
		_, terr := e.Transport.SendPS(ctx, interfaceID, body)
		if terr == nil {
			// A transport that accepted a send while DARK is a defect, not a success. Refuse loudly.
			return out, fail(ErrDarkNoEgress, "the financial transport transmitted while DARK")
		}
		if err := e.Repo.ReleaseClaim(ctx, tx, claim.OutboxID); err != nil {
			return out, err
		}
		out.Result, out.RefusedFor = "DECLINED", ErrDarkNoEgress
		if err := tx.Commit(ctx); err != nil {
			return out, fail(ErrRepo, "could not commit the DARK decline")
		}
		return out, nil
	}

	// ---- transmission -------------------------------------------------------------------------------
	pn, err := e.Repo.AllocatePNumber(ctx, tx, tenantID, siteID, interfaceID)
	if err != nil {
		return out, err
	}
	out.PNumber = pn
	body, err := BuildPS(PSRequest{RN: claim.Pinned.RN, GNumber: claim.Pinned.GNumber,
		AmountMinor: claim.Pinned.AmountMinor, PostingCode: claim.Pinned.PostingCode, PNumber: pn})
	if err != nil {
		return e.decline(ctx, tx, &out, claim, err)
	}
	attemptID, err := e.Repo.InsertAttempt(ctx, tx, claim.Pinned, claim.PostingID, claim.AttemptNo, pn)
	if err != nil {
		return out, err
	}
	// The attempt must be durable BEFORE the bytes go out. If this process dies mid-send, the recovering
	// process has to find an attempt it cannot explain rather than no evidence that anything happened.
	if err := tx.Commit(ctx); err != nil {
		return out, fail(ErrRepo, "could not commit the attempt before transmission")
	}

	pa, sendErr := e.Transport.SendPS(ctx, interfaceID, body)
	return e.settle(ctx, claim, attemptID, out, pa, sendErr)
}

// decline releases the claim and records why, without consuming a P# or writing an attempt.
func (e *Engine) decline(ctx context.Context, tx pgx.Tx, out *Outcome, claim *Claim, cause error) (Outcome, error) {
	if rerr := e.Repo.ReleaseClaim(ctx, tx, claim.OutboxID); rerr != nil {
		return *out, rerr
	}
	out.Result, out.RefusedFor = "DECLINED", CodeOf(cause)
	if cerr := tx.Commit(ctx); cerr != nil {
		return *out, fail(ErrRepo, "could not commit the decline")
	}
	return *out, cause
}

// settle applies the one terminal outcome the attempt reached, in its own transaction.
//
// The three-way split IS the UNKNOWN contract:
//
//	answered conclusively        -> ACKED, with the AS the PMS gave; outbox DONE
//	provably not transmitted     -> FAILED; the outbox goes back to QUEUED and an automatic retry is fine,
//	                                because nothing was sent
//	transmitted, no matched PA   -> UNKNOWN; the outbox is parked in HELD_RECOVERY and NOTHING retries it.
//	                                No timer, no backoff, no second P#, no restart path. It leaves that
//	                                state only through an audited CONFIRM_NOT_POSTED_RETRY, which the
//	                                database itself requires before an attempt 2 may exist at all.
func (e *Engine) settle(ctx context.Context, claim *Claim, attemptID string, out Outcome, pa *PA, sendErr error) (Outcome, error) {
	tx, err := e.Repo.pool.Begin(ctx)
	if err != nil {
		return out, fail(ErrRepo, "could not begin the settlement transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var outcome, as, outboxState, event string
	switch {
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
		outcome, outboxState, event = "FAILED", "QUEUED", "NOT_TRANSMITTED"
		out.Result, out.RefusedFor = "NOT_SENT", CodeOf(sendErr)
	default:
		// Everything else — a timeout, a torn connection after the write, an unparseable or unmatched
		// answer. The PS may have been applied. Assuming either way would be inventing a financial fact.
		outcome, outboxState, event = "UNKNOWN", "HELD_RECOVERY", "UNKNOWN_NO_CONCLUSIVE_PA"
		out.Result = "UNKNOWN"
		if sendErr != nil {
			out.RefusedFor = CodeOf(sendErr)
		}
	}
	if err := e.Repo.SettleAttempt(ctx, tx, attemptID, outcome, as); err != nil {
		return out, err
	}
	detail, _ := json.Marshal(map[string]string{"record": RecordPA, "as": as, "classification": event})
	if err := e.Repo.AppendAttemptEvent(ctx, tx, claim.Pinned, attemptID, event, string(detail)); err != nil {
		return out, err
	}
	if err := e.Repo.FinishClaim(ctx, tx, claim.OutboxID, outboxState); err != nil {
		return out, err
	}
	if err := tx.Commit(ctx); err != nil {
		return out, fail(ErrRepo, "could not commit the settlement")
	}
	if outcome == "UNKNOWN" {
		return out, fail(ErrUnknownTerminal,
			"the attempt is UNKNOWN and will not be retried without an audited review decision")
	}
	return out, nil
}

func (e *Engine) previousTargeting(ctx context.Context, tx pgx.Tx, postingID string, attemptNo int) (string, string, error) {
	var rn, g string
	err := tx.QueryRow(ctx,
		`SELECT rn, g_number FROM iam_v2.posting_attempts WHERE internal_posting_id=$1 AND attempt_no=$2`,
		postingID, attemptNo).Scan(&rn, &g)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", fail(ErrEvidenceStale, "the attempt being retried does not exist")
	}
	if err != nil {
		return "", "", fail(ErrRepo, "could not read the previous attempt's targeting evidence")
	}
	return rn, g, nil
}

// Requeue is the ONLY path out of UNKNOWN, and it is not a retry mechanism.
//
// It does not decide anything: it requires that an audited CONFIRM_NOT_POSTED_RETRY has ALREADY been
// recorded through iam_v2.record_posting_review_action, and the database refuses the resulting attempt
// unless that decision authorized exactly that attempt number. If a caller invokes this without a decision,
// the requeued work simply fails the retry gate and lands back in HELD_RECOVERY.
func (e *Engine) Requeue(ctx context.Context, postingID string) error {
	if !e.Cfg.ReviewOn() {
		return fail(ErrConfig, "the phase-4 financial review surface is disabled")
	}
	ct, err := e.Repo.pool.Exec(ctx, `
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
