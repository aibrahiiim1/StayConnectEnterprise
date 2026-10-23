package main

// netd promotes a PENDING_ENFORCEMENT session to active only once both the tc class and the nft gate are proven;
// the promotion is idempotent so retries and restarts converge on one active session.

import (
	"context"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// enforcementRecorder converges durable Session state with the confirmed kernel result.
type enforcementRecorder interface {
	// Activate is idempotent and must refuse an ended Session, so a late plan cannot resurrect revoked access.
	Activate(ctx context.Context, sessionID, bridge string, minor int, epoch int64) (string, error)
	Ended(ctx context.Context, sessionID, reason string) error
	// Confirm re-reads durable state; a failed Activate may have committed with only the acknowledgement lost.
	Confirm(ctx context.Context, sessionID string) (bool, error)
}

// pgEnforcement writes only through controlled writers, never raw UPDATE.
type pgEnforcement struct {
	pool interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	}
	tenant, site string
}

func (e *pgEnforcement) Activate(ctx context.Context, sessionID, bridge string, minor int, epoch int64) (string, error) {
	var outcome string
	err := e.pool.QueryRow(ctx,
		`SELECT iam_v2.activate_session_enforcement($1,$2,$3::uuid,$4,$5,$6)`,
		e.tenant, e.site, sessionID, bridge, minor, epoch).Scan(&outcome)
	return outcome, err
}

func (e *pgEnforcement) Ended(ctx context.Context, sessionID, reason string) error {
	var outcome string
	return e.pool.QueryRow(ctx,
		`SELECT iam_v2.end_session_enforcement($1,$2,$3::uuid,$4)`,
		e.tenant, e.site, sessionID, reason).Scan(&outcome)
}

// Confirm is a plain read, not a second promotion attempt.
func (e *pgEnforcement) Confirm(ctx context.Context, sessionID string) (bool, error) {
	var state string
	var ended *time.Time
	err := e.pool.QueryRow(ctx,
		`SELECT state, ended FROM iam_v2.sessions WHERE id=$1::uuid AND tenant_id=$2 AND site_id=$3`,
		sessionID, e.tenant, e.site).Scan(&state, &ended)
	if err != nil {
		return false, err
	}
	return ended == nil && state == "active", nil
}

type activationOutcome int

const (
	activationProven activationOutcome = iota
	activationUnproven
	activationFailed
)

// quarantineBackoff is how long a session stays denied after exhausting the activation grace. It doubles so a
// permanently unactivatable session is admitted a shrinking fraction of the time, capped so recovery stays fast.
func quarantineBackoff(strikes int) time.Duration {
	if strikes < 1 {
		strikes = 1
	}
	d := time.Minute
	for i := 1; i < strikes && d < 15*time.Minute; i++ {
		d *= 2
	}
	if d > 15*time.Minute {
		d = 15 * time.Minute
	}
	return d
}

// quarantineFor returns this class's record, creating it on demand. Caller holds p.mu.
func (p *phase3Shaping) quarantineFor(key string) *quarantineState {
	if p.quarantined == nil {
		p.quarantined = map[string]*quarantineState{}
	}
	q, ok := p.quarantined[key]
	if !ok {
		q = &quarantineState{}
		p.quarantined[key] = q
	}
	return q
}

// proveActive promotes a Session to active after both enforcement halves are confirmed, and reports whether
// durable ACTIVE is PROVEN. Caller holds p.mu.
//
// Outcomes:
//
//	PROVEN   — committed, or a re-read shows it had. Full lease.
//	UNPROVEN — unknown and inside the activation grace. Only the short provisional lease, which the kernel expires unaided.
//	FAILED   — grace spent. The caller revokes authorization, then tears the class down; the session is quarantined.
//
// The re-read matters: a lost acknowledgement on a committed transaction looks like a failure at the call site.
// Every duration is boot-relative monotonic time (/proc/uptime), so wall-clock changes cannot lengthen the grace.
func (p *phase3Shaping) proveActive(ctx context.Context, key, sessionID, bridge string, minor int, epoch int64, nowBoot int64) (activationOutcome, string) {
	if p.enforcement == nil {
		return activationProven, ""
	}
	outcome, err := p.enforcement.Activate(ctx, sessionID, bridge, minor, epoch)
	if err == nil {
		p.clearAttempt(key)
		slog.Info("phase3: session enforcement confirmed", "session", sessionID, "bridge", bridge,
			"minor", minor, "epoch", epoch, "outcome", outcome)
		return activationProven, ""
	}
	// Ambiguous: ask durable state whether the promotion landed.
	active, cerr := p.enforcement.Confirm(ctx, sessionID)
	if cerr == nil && active {
		p.clearAttempt(key)
		slog.Info("phase3: session enforcement confirmed by re-read after an unacknowledged promotion",
			"session", sessionID, "bridge", bridge, "minor", minor, "epoch", epoch)
		return activationProven, ""
	}

	q := p.quarantineFor(key)
	problem := problemFor(sessionID, err, cerr)

	bootID, bootErr := p.currentBootID()
	if bootErr != nil {
		// A bound not anchored to a boot cannot be interpreted later; treat it as an exhausted grace.
		q.Strikes++
		q.BackoffUntilBootMs = nowBoot + quarantineBackoff(q.Strikes).Milliseconds()
		return activationFailed, problem +
			" — and the boot identity is not trustworthy (" + bootErr.Error() + "); failing closed"
	}
	if q.BeganBootMs == 0 || q.crossBoot(bootID) {
		// First failure of this attempt: the write-ahead record exists; this records when the grace started.
		q.BootID = bootID
		q.BeganBootMs = nowBoot
		q.DeadlineBootMs = nowBoot + phase3ActivationGrace.Milliseconds()
		q.BeganWall = p.wallStamp()
		if perr := p.persistAttempts(); perr != nil {
			// A restart would not know this grace had started, so stop granting it.
			q.Strikes++
			q.BackoffUntilBootMs = nowBoot + quarantineBackoff(q.Strikes).Milliseconds()
			return activationFailed, problem +
				" — and the activation bound could not be made durable (" + perr.Error() + "); failing closed"
		}
	}

	if nowBoot <= q.DeadlineBootMs {
		return activationUnproven, problem + " — holding a provisional lease only"
	}
	// Grace spent: record the strike and clear the start so the next permitted attempt gets a full grace.
	q.Strikes++
	q.BackoffUntilBootMs = nowBoot + quarantineBackoff(q.Strikes).Milliseconds()
	q.BeganBootMs = 0
	q.DeadlineBootMs = 0
	if perr := p.persistAttempts(); perr != nil {
		// Safe: the in-memory record already denies and the older durable record still shows an outstanding attempt.
		problem += " (the strike could not be persisted: " + perr.Error() + ")"
	}
	return activationFailed, problem + " — activation grace exhausted; failing closed and quarantining"
}

// clearAttempt removes the durable activation record once the Session is proven active. Caller holds p.mu.
// A failed clear is not acted on: a stale fail-closed marker is harmless to a provable session and protective otherwise.
func (p *phase3Shaping) clearAttempt(key string) {
	if _, held := p.quarantined[key]; !held {
		return
	}
	delete(p.quarantined, key)
	if err := p.persistAttempts(); err != nil {
		slog.Warn("phase3: an activation record could not be cleared; it is left in place, which is the safe direction",
			"key", key, "err", err)
	}
}

// problemFor renders the one message both unproven paths report.
func problemFor(sessionID string, err, cerr error) string {
	problem := "session " + sessionID + ": enforcement is in force but durable ACTIVE could not be proven: " + err.Error()
	if cerr != nil {
		problem += " (state unreadable: " + cerr.Error() + ")"
	}
	return problem
}

// wallStamp is audit-only; never used for bounds.
func (p *phase3Shaping) wallStamp() string { return time.Now().UTC().Format(time.RFC3339) }

// markEnded converges durable state after access has been denied and torn down. Caller holds p.mu.
//
// The plan's reason is used when well-formed (the producer knows if it was a routine address handover); a
// malformed one falls back to the default because the writer would refuse the whole call.
// It reports whether durable state converged, so a teardown is repeated until the record says so.
func (p *phase3Shaping) markEnded(ctx context.Context, sessionID, reason string) bool {
	if p.enforcement == nil {
		return true
	}
	if !validEndReason(reason) {
		reason = "ENFORCEMENT_TORN_DOWN"
	}
	if err := p.enforcement.Ended(ctx, sessionID, reason); err != nil {
		// Access is already denied at the packet layer; this is only bookkeeping lag.
		slog.Warn("phase3: session enforcement ended but durable state not converged",
			"session", sessionID, "err", err)
		return false
	}
	return true
}

// endReasonAddressNotOwned is recorded when DHCP says the address belongs to somebody else; distinct from a failed teardown.
const endReasonAddressNotOwned = "ADDRESS_NO_LONGER_OWNED"

// endReasonPattern mirrors iam_v2.end_session_enforcement's own rule; a refused call would leave the session shown live.
var endReasonPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

func validEndReason(s string) bool { return endReasonPattern.MatchString(s) }
