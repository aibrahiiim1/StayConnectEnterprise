package posting

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/jackc/pgx/v5"
)

// Engine is the Posting domain core: creation under the fail-closed gate, and execution through the
// per-interface outbox lanes.
//
// THE CONSTRUCTION BOUNDARY, in its final form. Three attempts were needed, and the first two were each
// narrower than the hole:
//
//	private fields          stopped a caller MUTATING an engine it was handed — but not building its own
//	NewProductionEngine     removed the Config/Transport arguments — but still took an injectable getenv,
//	                        and NewDarkGuard was still exported with both
//	this                    NO exported function in this package accepts a Config or a Transport at all
//
// The package's exported financial surface is now: NewProductionEngine(repo). That is the whole of it.
//
//	config     comes from the environment, through the same fail-closed loader everything else uses
//	transport  comes from the internal factory below, which in this milestone returns nothing at all
//
// The deterministic seam the tests need lives in export_test.go, which the Go toolchain compiles ONLY when
// building this package's own tests. Production code cannot reference it — not by discipline, not by
// review, but because it does not exist in a production build.
type Engine struct {
	cfg       Config
	repo      *Repo
	transport Transport
	gate      Gate
}

// newEngine is the single internal constructor. Every path into an Engine goes through it, and it always
// wraps the transport in the DARK guard.
func newEngine(cfg Config, repo *Repo, inner Transport) *Engine {
	return &Engine{cfg: cfg, repo: repo, transport: newDarkGuard(cfg, inner)}
}

// NewProductionEngine is the ONLY exported way to obtain a financial engine, and it takes NOTHING that
// could change its financial posture.
//
// It reads the flag posture from the real process environment — not from an injected reader — and obtains
// its transport from productionTransport. A service that wants a transmitting engine therefore has to
// change the DEPLOYMENT's environment and this package's factory: two separate, reviewable acts in two
// different places, neither of which is reachable by writing Go at a call site.
//
// The getenv seam that used to be here was a hole, not a convenience: a production caller could hand the
// fail-closed loader any environment it liked. Tests drive the loader through export_test.go instead.
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

// productionTransport returns the real financial transport for this build.
//
// In this milestone it returns nil, and that is the honest answer rather than a placeholder: no real FIAS
// posting transport has been built, and none is authorized. A nil inner transport is safe by construction
// because DarkGuard refuses before it would ever be reached — and if the flags were somehow ON, the guard
// refuses with "no financial transport is configured" instead of inventing one.
//
// When the real transport is built it goes HERE, behind the same guard, and nowhere else.
func productionTransport(cfg Config) (Transport, error) {
	if cfg.TransmitOn() {
		// Fail closed and loudly. A deployment that enabled transmission against a build that has no
		// transport is a misconfiguration, and starting anyway would leave an operator believing money was
		// flowing when it was not.
		return nil, fail(ErrConfig,
			"phase-4 transmission is enabled but this build has no financial transport; refusing to start")
	}
	return nil, nil
}

// Config returns a COPY of the flag state. Config is a value type, so a caller can read the posture and
// cannot mutate the engine's.
func (e *Engine) Config() Config { return e.cfg }

// TransportRefusals reports how many financial transmissions the guard has refused. It exists so a test
// can assert the worker really tried and was stopped, rather than inferring darkness from empty tables.
func (e *Engine) TransportRefusals() int {
	if g, ok := e.transport.(*DarkGuard); ok {
		return g.Refusals()
	}
	return 0
}

// LastRefusedBody returns the last PS body the guard refused, so a test can assert a COMPLETE financial
// record was built and stopped at the wire.
func (e *Engine) LastRefusedBody() string {
	if g, ok := e.transport.(*DarkGuard); ok {
		return g.LastRefusedBody()
	}
	return ""
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
	if err := e.gate.CheckFor(PurposeExecute, claim.Pinned, snap); err != nil {
		return e.decline(ctx, tx, &out, claim, err)
	}

	// ---- the DARK boundary --------------------------------------------------------------------------
	if e.cfg.Dark() {
		// Build a complete, valid PS anyway and offer it to the guard, so the refusal is evidence that a
		// real financial record was constructed and stopped AT THE WIRE rather than never reached. The
		// placeholder P# below is a local value: no P# is allocated, so nothing durable is consumed.
		body, _ := BuildPS(PSRequest{RN: claim.Pinned.RN, GNumber: claim.Pinned.GNumber,
			AmountMinor: claim.Pinned.AmountMinor, PostingCode: claim.Pinned.PostingCode, PNumber: 1})
		_, terr := e.transport.SendPS(ctx, interfaceID, 1, body)
		if terr == nil {
			// A transport that accepted a send while DARK is a defect, not a success. Refuse loudly.
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

	// ---- transmission -------------------------------------------------------------------------------
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
	attemptID, err := e.repo.InsertAttempt(ctx, tx, claim.Pinned, claim.PostingID, claim.AttemptNo, pn)
	if err != nil {
		return out, err
	}
	// The attempt must be durable BEFORE the bytes go out. If this process dies mid-send, the recovering
	// process has to find an attempt it cannot explain rather than no evidence that anything happened.
	if err := tx.Commit(ctx); err != nil {
		return out, fail(ErrRepo, "could not commit the attempt before transmission")
	}

	pa, sendErr := e.transport.SendPS(ctx, interfaceID, pn, body)
	// The core verifies correlation itself rather than trusting the transport to have done it. An answer
	// carrying another P# is not this attempt's answer -- it must never ACK it, and because the PS was
	// genuinely transmitted the honest outcome is UNKNOWN, not a failure and certainly not a success.
	if sendErr == nil && pa != nil && pa.PNumber != pn {
		pa, sendErr = nil, fail(ErrPAWrongPNumber,
			"the PA answers a different protocol-attempt reference; this attempt is unresolved")
	}
	return e.settle(ctx, claim, attemptID, out, pa, sendErr)
}

// decline releases the claim and records why, without consuming a P# or writing an attempt.
func (e *Engine) decline(ctx context.Context, tx pgx.Tx, out *Outcome, claim *Claim, cause error) (Outcome, error) {
	if rerr := e.repo.ReleaseClaim(ctx, tx, claim.OutboxID); rerr != nil {
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
	tx, err := e.repo.pool.Begin(ctx)
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
	if err := e.repo.SettleAttempt(ctx, tx, attemptID, outcome, as); err != nil {
		return out, err
	}
	detail, _ := json.Marshal(map[string]string{"record": RecordPA, "as": as, "classification": event})
	if err := e.repo.AppendAttemptEvent(ctx, tx, claim.Pinned, attemptID, event, string(detail)); err != nil {
		return out, err
	}
	if err := e.repo.FinishClaim(ctx, tx, claim.OutboxID, outboxState); err != nil {
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
