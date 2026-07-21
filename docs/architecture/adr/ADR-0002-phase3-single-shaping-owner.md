# ADR-0002 — One shaping owner for Phase-3 enforcement

**Status:** Accepted (Phase 3, DARK)
**Supersedes:** nothing
**Context date:** 2026-07-22

## The problem

Phase 3 introduced a derived shaping plan: given durable Entitlement state, the edge should be rate-limiting
exactly this set of sessions at exactly these rates, and forwarding nothing for anyone else. Something has to
apply that plan to `tc`.

The obvious shortcut was to let `acctd` apply it directly — it already holds a `tc` client for legacy
per-session shaping, and it already computes the plan. That shortcut is what this ADR rejects.

`netd` is the appliance's privileged networking owner. It runs as root, owns the netlink/`tc`/`nftables`
surface, and serves a protected local Unix socket. If `acctd` also wrote `tc` classes, **two daemons could
mutate the same kernel state on their own schedules**. The failure is not theoretical:

- both reconcile at the same moment and interleave a delete with an add for the same class;
- `acctd` re-applies a rate `netd` just removed during a network re-apply, resurrecting shaping for a
  session that is no longer entitled;
- a `netd` full re-apply wipes classes `acctd` believes it installed, and `acctd` has no way to know;
- and afterwards there is no single place to ask "why is this guest shaped like this?".

Two writers to one piece of kernel state is a race with no owner, and it degrades exactly when the appliance
is busiest.

## Decision

**`netd` is the only process that mutates Phase-3 shaping state.**

- `acctd` owns MEASUREMENT and DERIVATION: it reads counters, ingests accounting through the controlled
  Phase-3 operation, enforces true-time expiry, and derives the desired shaping plan from durable state.
- `acctd` then SUBMITS that plan to `netd` over the existing protected Unix socket
  (`POST /v1/phase3/shaping`). It performs no `tc` mutation of its own for Phase-3 sessions.
- `netd` reconciles the kernel to the submitted plan: tear down first, then shape, idempotently, every time.

The legacy per-session shaping path in `acctd` is untouched and keeps running while Phase-3 flags are OFF, so
this decision changes nothing about today's appliance.

## Why not the alternative

Keeping `acctd` as the writer (with `netd` unaware) would have been less code. It was rejected because the
only thing preventing a race would have been "the two daemons happen not to touch the same classes at the
same time" — an assumption nothing enforces and no test can hold. Making the privileged owner the sole writer
turns that assumption into a structural property: `acctd` cannot race `netd` because `acctd` cannot write.

## Consequences

**Good**

- Exactly one writer, provable by inspection: the Phase-3 shaping call sites live in `netd` only.
- The plan crossing a process boundary is a bounded, versioned contract rather than a shared mutable
  assumption, and it is authenticated by the socket's existing peer restriction.
- Reconciliation is idempotent and complete, so a restart, a reboot or a manual `tc` change converges on the
  next submission with no remembered state on either side.
- Degraded state is visible: if `netd` cannot apply the plan, `acctd` reports it and the appliance's health
  shows it, rather than the plan silently not being in force.

**Costs**

- A plan submission can fail (socket down, `netd` restarting). It is retried with bounds on the next tick;
  the plan is derived fresh each time, so a missed submission is never stale — it is simply superseded.
- One extra hop of latency between deriving and applying, which is irrelevant at the tick cadence.

## Invariants this decision must keep

1. Exactly one shaping writer (`netd`).
2. Teardown before shaping, always.
3. Full idempotent reconciliation — never incremental deltas the two sides could disagree about.
4. Restart/reboot convergence with no remembered policy state on either side.
5. Zero Phase-3 database queries and zero Phase-3 network mutations while the flags are OFF.
6. Bounded retry, and a truthful degraded status when the plan cannot be applied.
7. No ended session remains forwarded after its Entitlement terminates.
