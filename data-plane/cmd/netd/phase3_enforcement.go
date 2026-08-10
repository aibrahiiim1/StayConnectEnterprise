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

	"github.com/jackc/pgx/v5"
)

// enforcementRecorder converges durable Session state with the confirmed kernel result.
type enforcementRecorder interface {
	// Activate promotes a Session to network-active. It is idempotent and must refuse a Session that has
	// ended, so a late plan cannot resurrect access that was revoked.
	Activate(ctx context.Context, sessionID, bridge string, minor int, epoch int64) (string, error)
	// Ended records that enforcement for a Session is gone, so durable state stops claiming it is active.
	Ended(ctx context.Context, sessionID, reason string) error
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

// markActive promotes a Session to active after both enforcement halves are confirmed. Caller holds p.mu.
//
// A failure here is REPORTED, never swallowed, but it does not tear the guest down: the kernel state is
// correct and proven, and durable state is merely behind. The next reconciliation retries the promotion, and
// the guest's own retry sees ACTIVE as soon as it lands. Tearing down working, accountable enforcement
// because a status write failed would turn a bookkeeping problem into an outage.
func (p *phase3Shaping) markActive(ctx context.Context, sessionID, bridge string, minor int, epoch int64) string {
	if p.enforcement == nil {
		return "" // no recorder wired (unit tests drive the kernel halves directly)
	}
	outcome, err := p.enforcement.Activate(ctx, sessionID, bridge, minor, epoch)
	if err != nil {
		return "session " + sessionID + ": enforcement is in force but durable state was not promoted to active: " + err.Error()
	}
	slog.Info("phase3: session enforcement confirmed", "session", sessionID, "bridge", bridge,
		"minor", minor, "epoch", epoch, "outcome", outcome)
	return ""
}

// markEnded converges durable state after access has been denied and torn down. Caller holds p.mu.
func (p *phase3Shaping) markEnded(ctx context.Context, sessionID string) {
	if p.enforcement == nil {
		return
	}
	if err := p.enforcement.Ended(ctx, sessionID, "ENFORCEMENT_TORN_DOWN"); err != nil {
		// Access is already denied at the packet layer, so this is a bookkeeping lag, not continued access.
		slog.Warn("phase3: session enforcement ended but durable state not converged",
			"session", sessionID, "err", err)
	}
}
