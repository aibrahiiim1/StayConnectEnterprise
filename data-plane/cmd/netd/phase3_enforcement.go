package main

// RECORDING THE CONFIRMED KERNEL RESULT.
//
// A Session row that says `active` is a claim about the kernel: this guest is authorized and metered right
// now. The grant path cannot honestly make that claim, because when it commits, no kernel work has happened
// yet — the class does not exist and the guest is not authorized. So the grant commits the Session as
// PENDING_ENFORCEMENT, and netd — the process that actually performs the enforcement, and the only one that
// can see whether it took — promotes it to active through the controlled writer once BOTH halves are proven:
// the accountable tc class is classifying, and the nft gate is authorizing.
//
// The promotion is idempotent. A lost acknowledgement, a retried plan, or a restart mid-flight must converge
// on exactly one active Session rather than a second grant.

import (
	"context"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// enforcementRecorder converges durable Session state with the confirmed kernel result.
type enforcementRecorder interface {
	// Activate promotes a Session to network-active. It is idempotent and must refuse a Session that has
	// ended, so a late plan cannot resurrect access that was revoked.
	Activate(ctx context.Context, sessionID, bridge string, minor int, epoch int64) (string, error)
	// Ended records that enforcement for a Session is gone, so durable state stops claiming it is active.
	Ended(ctx context.Context, sessionID, reason string) error
	// Confirm re-reads AUTHORITATIVE durable state and answers whether the Session is active.
	//
	// It exists because a failed Activate is ambiguous in the one way that matters: the transaction may well
	// have committed and only the acknowledgement been lost. Treating that as failure would tear down a guest
	// whose state is already correct; treating it as success would leave an unproven claim standing. Asking
	// the database is the only way to tell, and the promotion being idempotent is what makes asking safe.
	Confirm(ctx context.Context, sessionID string) (bool, error)
}

// pgEnforcement is the database-backed recorder. Both operations go through controlled writers, so netd
// cannot move Session state by raw UPDATE any more than anything else can.
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

// Confirm reads the Session's own durable state. It is a plain read of the authoritative row — not a second
// attempt at the promotion — so it can answer "did the commit land?" without changing anything if it did not.
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

// proveActive promotes a Session to active after both enforcement halves are confirmed, and reports whether
// durable ACTIVE is PROVEN. Caller holds p.mu.
//
// The earlier policy left the kernel enforcing whenever this write failed, reasoning that the kernel was right
// and the bookkeeping merely behind. That reasoning has a hole with no floor under it: if the database stays
// unreachable, "merely behind" never ends, and the outcome is a guest permanently on the internet whose
// Session says PENDING_ENFORCEMENT — access that no durable record claims, that no audit can explain, and that
// no revocation acting on Session state can ever remove.
//
// So the outcome is one of exactly three, and none of them is "leave it and hope":
//
//	PROVEN     — the promotion committed, or a re-read shows it had already committed. Full lease.
//	UNPROVEN   — the outcome is genuinely unknown and still inside the activation grace. The guest keeps only
//	             the SHORT provisional lease, so if nothing ever proves the promotion the kernel expires the
//	             authorization on its own.
//	FAILED     — the grace is spent. The caller fails closed: authorization revoked and proven gone first,
//	             then the accountable class torn down.
//
// The re-read is what makes this safe rather than merely strict. A lost acknowledgement on a committed
// transaction is indistinguishable from a failed one at the call site, and tearing a correct guest down over
// it would be an outage manufactured out of a dropped packet.
type activationOutcome int

const (
	activationProven activationOutcome = iota
	activationUnproven
	activationFailed
)

// quarantineBackoff is how long a session stays denied after an attempt exhausts the activation grace.
//
// It doubles, and it is capped. The doubling is what makes the outcome converge: a session whose activation
// is permanently impossible (a mismatched accounting origin, a source that no longer describes it) spends a
// rapidly shrinking fraction of its life admitted, instead of flapping online and offline at a fixed period
// forever. The cap is so that a property whose database was down for an hour recovers within minutes of it
// coming back rather than hours.
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
// The outcome is one of exactly three, and none of them is "leave it and hope":
//
//	PROVEN     — the promotion committed, or a re-read shows it had already committed. Full lease.
//	UNPROVEN   — genuinely unknown and still inside the activation grace. The guest keeps only the SHORT
//	             provisional lease, so if nothing ever proves the promotion the kernel expires it unaided.
//	FAILED     — the grace is spent. The caller fails closed: authorization revoked and proven gone first,
//	             then the accountable class torn down, and the session quarantined with a doubling backoff.
//
// The re-read is what makes this safe rather than merely strict. A lost acknowledgement on a committed
// transaction is indistinguishable from a failed one at the call site, and tearing a correct guest down over
// it would be an outage manufactured out of a dropped packet.
//
// EVERY DURATION HERE IS BOOT-RELATIVE MONOTONIC TIME. nowBoot comes from /proc/uptime, not the wall clock,
// so an NTP correction or a wrong RTC cannot lengthen the grace (see phase3_securitytime.go).
func (p *phase3Shaping) proveActive(ctx context.Context, key, sessionID, bridge string, minor int, epoch int64, nowBoot int64) (activationOutcome, string) {
	if p.enforcement == nil {
		return activationProven, "" // no recorder wired (unit tests drive the kernel halves directly)
	}
	outcome, err := p.enforcement.Activate(ctx, sessionID, bridge, minor, epoch)
	if err == nil {
		p.clearAttempt(key)
		slog.Info("phase3: session enforcement confirmed", "session", sessionID, "bridge", bridge,
			"minor", minor, "epoch", epoch, "outcome", outcome)
		return activationProven, ""
	}
	// AMBIGUOUS. Ask authoritative state whether the promotion actually landed.
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
		// The bound cannot be anchored to a boot, so it cannot be trusted after one. Treat it exactly as an
		// exhausted grace rather than recording a deadline nothing can later interpret.
		q.Strikes++
		q.BackoffUntilBootMs = nowBoot + quarantineBackoff(q.Strikes).Milliseconds()
		return activationFailed, problem +
			" — and the boot identity is not trustworthy (" + bootErr.Error() + "); failing closed"
	}
	if q.BeganBootMs == 0 || q.crossBoot(bootID) {
		// The first failure of this attempt. The write-ahead record already exists (admission could not have
		// happened otherwise), so this only sharpens it with the moment the grace actually started.
		q.BootID = bootID
		q.BeganBootMs = nowBoot
		q.DeadlineBootMs = nowBoot + phase3ActivationGrace.Milliseconds()
		q.BeganWall = p.wallStamp()
		if perr := p.persistAttempts(); perr != nil {
			// The bound could not be made durable. A restart would not know this grace had started, so the
			// only safe answer is to stop granting it.
			q.Strikes++
			q.BackoffUntilBootMs = nowBoot + quarantineBackoff(q.Strikes).Milliseconds()
			return activationFailed, problem +
				" — and the activation bound could not be made durable (" + perr.Error() + "); failing closed"
		}
	}

	if nowBoot <= q.DeadlineBootMs {
		return activationUnproven, problem + " — holding a provisional lease only"
	}
	// The grace is spent. Record the strike, deny re-admission for the backoff, and clear the start so the
	// next permitted attempt gets its own full chance rather than an already-spent one.
	q.Strikes++
	q.BackoffUntilBootMs = nowBoot + quarantineBackoff(q.Strikes).Milliseconds()
	q.BeganBootMs = 0
	q.DeadlineBootMs = 0
	if perr := p.persistAttempts(); perr != nil {
		// Failing to persist a HARDER state is safe: the in-memory record already denies, and the older
		// durable record still says an attempt is outstanding. It is reported, never swallowed.
		problem += " (the strike could not be persisted: " + perr.Error() + ")"
	}
	return activationFailed, problem + " — activation grace exhausted; failing closed and quarantining"
}

// clearAttempt removes a durable activation record once the Session is PROVEN active. Caller holds p.mu.
//
// A failure to clear is deliberately NOT an error the caller acts on. The record is a fail-closed marker: a
// stale one is harmless to a session that can be proven active — the next pass proves it again and clears it
// again — and protective for one that cannot. Leaving it behind is always the safe direction, so this reports
// and moves on rather than turning a cleanup problem into a disconnection.
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

// wallStamp is the AUDIT-ONLY timestamp recorded beside the monotonic readings.
func (p *phase3Shaping) wallStamp() string { return time.Now().UTC().Format(time.RFC3339) }

// markEnded converges durable state after access has been denied and torn down. Caller holds p.mu.
//
// The reason comes from the plan when the producer supplied one, because it knows things the applier cannot
// see: a session torn down because a newer session took its address is a routine handover, not a teardown, and
// the durable record should not call it one. Anything malformed falls back to the default rather than being
// passed to the writer, which validates the code and would refuse the whole call.
func (p *phase3Shaping) markEnded(ctx context.Context, sessionID, reason string) {
	if p.enforcement == nil {
		return
	}
	if !validEndReason(reason) {
		reason = "ENFORCEMENT_TORN_DOWN"
	}
	if err := p.enforcement.Ended(ctx, sessionID, reason); err != nil {
		// Access is already denied at the packet layer, so this is a bookkeeping lag, not continued access.
		slog.Warn("phase3: session enforcement ended but durable state not converged",
			"session", sessionID, "err", err)
	}
}

// endReasonPattern is the writer's own rule, restated here so a malformed code is replaced rather than sent:
// iam_v2.end_session_enforcement refuses anything outside it, and a refused call would leave durable state
// claiming a session is still live after its access was already removed from the kernel.
// endReasonAddressNotOwned is recorded when DHCP says the session's address belongs to somebody else, or to
// nobody. It is deliberately distinct from an ordinary teardown: an operator reading it needs to know the
// address moved, not that enforcement failed.
const endReasonAddressNotOwned = "ADDRESS_NO_LONGER_OWNED"

var endReasonPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

func validEndReason(s string) bool { return endReasonPattern.MatchString(s) }
