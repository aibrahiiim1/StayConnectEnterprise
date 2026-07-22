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
- `netd` reconciles the kernel to the submitted plan: tear down first, then remove strays, then shape,
  idempotently, every time.

### The submission is a controlled interface, not a shared socket

Choosing one writer was necessary but not sufficient. A single writer that applies whatever arrives on a
group-readable socket has simply moved the problem: the writer is now the thing that can be talked into
enforcing someone else's policy. Three further decisions close that:

**1. The producer is authenticated by the kernel, not by a header.** `netd`'s socket is group `stayconnect`
and reachable by `scd`, `edged`, `portald` and `pmsd`, none of which own enforcement. Every accepted
connection carries its `SO_PEERCRED` identity, and only the one configured `NETD_PHASE3_PRODUCER_UID` may
submit. A header would be a claim the caller writes about itself; the peer uid is the kernel's statement about
the caller. With no producer uid configured, submission fails closed — and if the Phase-3 flags are on, `netd`
refuses to start at all, because live enforcement with no authenticable producer is not a degraded mode, it is
an unenforceable one.

**2. `netd` decides for itself whether Phase 3 is live.** It resolves the flags, the enrollment identity and
the signed assignment from the same sources every other daemon uses, and refuses every submission while dark —
including the class-generation read. If it instead trusted "`acctd` would never submit while dark", the kill
switch would depend on a *different* process staying correct. If the flags are on but the appliance cannot
establish its own tenant/site/appliance, `netd` stays inactive rather than taking the scope from the plan:
otherwise the envelope would be both the claim and its own authorization.

**3. The plan is a scoped, versioned, expiring, hashed envelope** (`internal/shapeplan`, contract
`phase3-shaping/1`), carrying tenant, site, appliance, assignment generation, a durable monotonic plan
generation, a validity window and a hash of the desired state. `netd` checks each field against its own scope
and against the last plan it accepted — persisted, so a restart cannot be talked into re-applying a plan it had
already superseded. The hash is what makes a truncated body refusable: without it, a body that lost half its
sessions is indistinguishable from a legitimate mass revocation. The producer's generation is durable for the
mirror-image reason: a restarted `acctd` that began again at 1 would have every plan correctly refused as
stale, freezing enforcement at the pre-restart state with nothing appearing broken.

### Reconciliation enumerates the kernel

A plan lists what *should* be in force. The dangerous state is what is in force that nothing will ever mention
again: a class belonging to a session that ended while `netd` was down, or that survived a crash. No delta
protocol can name it, and `netd`'s own memory does not contain it — that is exactly what was lost. So
reconciliation reads the installed classes on every bridge the plan touches and removes any managed-range
minor the desired state does not claim. Removals are logged, never silent: a stray means the kernel was
forwarding for a session durable state does not have.

An unreadable kernel therefore means UNKNOWN strays, and is reported as degraded rather than as a clean apply.

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
  assumption, and its producer is authenticated by peer credentials against one allowlisted uid.
- A dark appliance cannot be made to shape anything, by any local process, however misconfigured.
- Drift is corrected rather than accumulated: classes with no live session are removed on the next pass.
- Reconciliation is idempotent and complete, so a restart, a reboot or a manual `tc` change converges on the
  next submission with no remembered state on either side.
- Degraded state is visible: if `netd` cannot apply the plan, `acctd` reports it and the appliance's health
  shows it, rather than the plan silently not being in force.

**Costs**

- A plan submission can fail (socket down, `netd` restarting). It is retried with bounds on the next tick;
  the plan is derived fresh each time, so a missed submission is never stale — it is simply superseded.
- One extra hop of latency between deriving and applying, which is irrelevant at the tick cadence.
- Two pieces of durable state now exist purely to make refusals correct: the producer's plan generation and
  the applier's last-accepted record. Both are small, both are rewritten atomically, and losing either fails
  in the safe direction (a refused plan, retried on the next tick — never an applied stale one).
- **Cutover is exclusive.** Once Phase 3 is live, a bridge that appears in the desired state belongs entirely
  to Phase 3: any per-session class on it that the plan does not claim is removed, including one the legacy
  path installed before cutover. This is the intended consequence of single ownership — `acctd`'s legacy
  accounting/shaping loop stands down whenever the Phase-3 arm owns accounting — but it does mean enabling the
  flags on a site with live legacy sessions must be treated as a maintenance action, not a silent toggle.

## Invariants this decision must keep

1. Exactly one shaping writer (`netd`).
2. Teardown before shaping, always.
3. Full idempotent reconciliation — never incremental deltas the two sides could disagree about.
4. Restart/reboot convergence with no remembered policy state on either side.
5. Zero Phase-3 database queries and zero Phase-3 network mutations while the flags are OFF.
6. Bounded retry, and a truthful degraded status when the plan cannot be applied.
7. No ended session remains forwarded after its Entitlement terminates.
8. Only the one authenticated producer uid may submit; an unauthenticable caller is refused, and an
   unconfigured producer uid fails closed.
9. A plan is applied only if it matches this appliance's own tenant, site, appliance and assignment, speaks
   the supported contract version, is unexpired, hashes to its own contents, and is not older than the last
   plan accepted — across restarts.
10. Reconciliation removes managed classes the desired state does not claim, and reports when the installed
    state could not be read.
