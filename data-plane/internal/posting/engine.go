package posting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"

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
// THE TRANSPORT IS THE HAND-OFF TO pmsd (decision D45). The property's PMS accepts one FIAS connection and
// pmsd owns it, so a room charge is carried there as an immutable, authorised command and pmsd returns the
// matched PA (handoff_transport.go, internal/postinghandoff, internal/pmsd/financial_relay.go). It is
// constructed only when transmission is ON; while DARK the inner transport is nil, DarkGuard refuses before
// it would be reached, and no hand-off client exists at all. It stays behind the same guard, and nowhere else.
func productionTransport(cfg Config) (Transport, error) {
	if !cfg.TransmitOn() {
		return nil, nil
	}
	return newHandoffTransport(), nil
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
	Claimed   bool
	PostingID string
	AttemptNo int
	PNumber   int64
	Result    string // POSTED | REJECTED | NOT_SENT | UNKNOWN | DECLINED | ABORTED
	// NotSentReason is pmsd's bounded reason when it proved nothing was written (e.g. ROOM_CHANGED).
	NotSentReason string
	ASStatus      string
	RefusedFor    Code
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

	// Lock the pinned stay for the rest of this transaction, so a room move cannot land between reading the
	// reservation's room below and committing the attempt that carries it. (pmsd re-checks again at the
	// moment of writing; see Phase-0 Amendment A1, contract section 9a rule 7.)
	if _, err := tx.Exec(ctx, `SELECT iam_v2.p4_lock_posting_stay($1)`, claim.PostingID); err != nil {
		return e.decline(ctx, tx, &out, claim, classify(err))
	}
	// Re-verify the PINNED evidence. This is not re-resolution: every identifier comes from the durable
	// posting row, and the check is that those exact objects are still in a state that authorizes the
	// charge. A stay that checked out between authorization and transmission stops the charge here.
	snap, err := LoadSnapshot(ctx, tx, claim.Pinned)
	if err != nil {
		return e.decline(ctx, tx, &out, claim, err)
	}
	// G# IS STABLE, RN IS MUTABLE. The reservation is the posting's pin (claim.Pinned.GNumber, from
	// pms_postings.g_number) and is verified against the stay by the gate; the room is the reservation's
	// CURRENT room, read now. A room move before this point re-targets the room -- never the reservation -- and
	// does not fail the purchase. A reviewed retry is built the same way: its new attempt carries the room as
	// it is now, and every earlier attempt keeps, immutably, the room it actually carried.
	claim.Pinned.RN = snap.StayRoomNumber
	if err := e.gate.CheckFor(PurposeExecute, claim.Pinned, snap); err != nil {
		if reason := presendAbortReason(err); reason != "" {
			return e.abortBeforeSend(ctx, tx, &out, claim, reason, err)
		}
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
	// The attempt records the SHA-256 of the exact bytes it authorises. pmsd carries a command only when the
	// database confirms a SENDING attempt with this interface, P# and hash (p4_posting_command_authorised), so
	// no byte sequence the financial path did not durably authorise can ever be written to the PMS.
	attemptID, err := e.repo.InsertAttempt(ctx, tx, claim.Pinned, claim.PostingID, claim.AttemptNo, pn, psHash(body))
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

// RecoverOrphanedAttempts closes every attempt the worker left SENDING -- the process stopped after the
// attempt became durable and before its outcome was recorded. Such a PS may or may not have reached the PMS,
// so each becomes UNKNOWN: outbox HELD_RECOVERY, settlement MANUAL_REVIEW, and nothing resends it.
//
// It only touches attempts older than olderThan, which is never less than AnswerDeadline plus a minute --
// longer than any in-process send can last -- so a send still in progress is never judged.
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

// psHash is the command identity recorded on the attempt: lowercase hex SHA-256 of the exact PS body, the
// same value internal/postinghandoff.BodyHash computes on the wire.
func psHash(body string) string {
	s := sha256.Sum256([]byte(body))
	return hex.EncodeToString(s[:])
}

// presendAbortReason names the pre-send revalidation failures that END the charge as definitely not posted
// (contract section 9a rule 7): the reservation is no longer in house, the stay is blocked, the reservation
// does not match, its current room cannot be resolved, or the PMS data is stale. Anything else (interface
// lifecycle, onboarding, currency) only declines, leaving the work queued for an operator to resolve.
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

// abortBeforeSend ends a charge that provably never reached the PMS: its settlement FAILS (so the purchase
// fails and nothing is granted), the outbox row is DONE and the reason is recorded. A charge with any attempt
// that may have been transmitted is never aborted here: it goes back to manual review instead.
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
//	answered, meaning confirmed  -> ACKED, with the AS the PMS gave; outbox DONE. OK means posted; a non-OK
//	                                status means "not posted" ONLY when the vendor has confirmed that code for
//	                                this interface (iam_v2.p4_answer_effect); otherwise the answer is UNKNOWN
//	provably not transmitted     -> NOT_SENT, with pmsd's reason; the outbox goes back to QUEUED and a NEW
//	                                attempt (fresh room, same G#) is fine, because nothing was sent
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

	var outcome, as, outboxState, event, reason string
	switch {
	case sendErr == nil && pa != nil && e.answerEffect(ctx, claim.Pinned.PMSInterfaceID, pa.AS) == "UNPROVEN":
		// A PA was matched, but its status is not one the vendor has confirmed means "not posted" on this
		// interface. Its posting effect cannot be proven, so it is UNKNOWN: manual review, never a retry.
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
	fields := map[string]string{"record": RecordPA, "as": as, "classification": event}
	if reason != "" {
		fields["record"], fields["reason"] = RecordPS, reason
	}
	detail, _ := json.Marshal(fields)
	if err := e.repo.AppendAttemptEvent(ctx, tx, claim.Pinned, attemptID, event, string(detail)); err != nil {
		return out, err
	}
	if err := e.repo.FinishClaim(ctx, tx, claim.OutboxID, outboxState); err != nil {
		return out, err
	}
	if err := tx.Commit(ctx); err != nil {
		return out, fail(ErrRepo, "could not commit the settlement")
	}
	// THE SETTLEMENT FOLLOWS THE PMS (migration 0097): PA=OK settles the purchase's settlement and grants the
	// entitlement; any other PA fails it; UNKNOWN sends it to manual review. Applied after the attempt is
	// durable, and idempotent, so a crash between the two is repaired by the next pass (ApplySettlementOutcomes).
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

// answerEffect asks the database what a PA status means on this interface: POSTED, a NOT_POSTED_* effect, or
// UNPROVEN. Only a vendor-confirmed code can mean "not posted"; any doubt, including a failed lookup, is
// UNPROVEN, which the settlement treats as UNKNOWN.
func (e *Engine) answerEffect(ctx context.Context, interfaceID, as string) string {
	var eff string
	if err := e.repo.pool.QueryRow(ctx, `SELECT iam_v2.p4_answer_effect($1, $2)`, interfaceID, as).Scan(&eff); err != nil {
		return "UNPROVEN"
	}
	return eff
}

// notSentReason extracts pmsd's bounded reason from a not-transmitted refusal, or "" when there is none.
func notSentReason(err error) string {
	var r interface{ NotSentReason() string }
	if errors.As(err, &r) {
		return r.NotSentReason()
	}
	return ""
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

// ApplySettlementOutcomes re-applies the settlement effect of every concluded CHARGE posting whose settlement
// has not caught up (a crash between recording the PA and moving the settlement). Idempotent.
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
