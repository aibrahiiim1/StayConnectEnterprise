# Phase-0 Contract — Amendment A2: Room sign-in routing is an explicit policy of each client network

**Status: APPLIED — Product-Owner decision D74 (2026-10-26), mission "ONEGATE — COMPLETE ENTERPRISE CLIENT NETWORKING & MULTI-PMS DELIVERY".**
Its wording is applied to [`StayConnect-IAM-Phase0-Contract.md`](StayConnect-IAM-Phase0-Contract.md) §8, which carries the normative text; this document is the record of
what changed and why.

**Amends:** §8 "Multi-PMS Automatic Resolution", item 1 (the candidate set) and the way it is configured. **Does not change:** STRICT resolution itself
(items 2–5 and 7), the one-time Auth Context, interface-scoped Stay identity, independent PMS ownership, the uniform non-success envelope, the throttles, posting, or
anything about commerce and entitlements.

---

## A2.0 Why

The contract had the right resolver (parallel fan-out over the mapped interfaces, a complete outcome vector, STRICT decision) and the right fail-closed rule (an
unmapped network offers nothing). What it left open was how an operator says *which* interfaces a network checks, and the product only ever offered one:

- the write path replaced a network's route with exactly one interface, so the resolver's multi-interface vector could never be configured;
- "no row" meant both "deliberately PMS-free" and "nobody decided", and the screens could not tell the two apart;
- the contract text still offered `ALL_ACTIVE_INTERFACES` ("all ACTIVE interfaces ... under explicit configuration"), which no resolver ever implemented and which, if
  implemented, would let a network silently check every PMS the site happens to have.

## A2.1 The rule

Each client network carries **one explicit room sign-in policy**:

| Policy | Meaning | Interfaces |
|---|---|---|
| `NONE` | The network deliberately offers no room sign-in. Other methods are unaffected. It is never reported as a fault. | none |
| `DEDICATED` | The room is checked against exactly one selected PMS Interface. | exactly 1 |
| `SHARED_STRICT` | The room is checked against an explicitly selected set of independent PMS Interfaces under the STRICT contract. | 2 to 3 |

A network with **no policy is undecided** and offers room sign-in to nobody (the existing fail-closed rule). A route stored before policies existed (exactly one mapped
interface) is read as `DEDICATED`; more than one mapped interface without a policy is refused rather than interpreted.

- **The candidate set is the selected set, and only that.** `ALL_ACTIVE_INTERFACES` is removed from the contract: it is not offered, not implemented and not a
  fallback. Nothing ever means "every active PMS". Several networks may use the same interface; one network may use several, only under `SHARED_STRICT`.
- **Bounded.** A shared set has at most **three** interfaces (the contract's default cap; the hard ceiling of five needs a Product-Owner decision to raise). The cap is
  enforced when a policy is saved (the API refuses it and a deferred database constraint makes a larger set unstorable) and again at run time (the resolver returns
  INDETERMINATE for an over-cap vector).
- **A named candidate that is not in service is not dropped.** An interface the policy names that is `AUTH_DISABLED`, `DRAINING` or `DECOMMISSIONED` stays in the vector as
  UNAVAILABLE and is never probed. Dropping it would let a strict resolution quietly ask one PMS fewer than the operator chose and admit a guest the missing PMS
  might have contradicted. The result is INDETERMINATE (`CANDIDATE_NOT_ACTIVE`), reported to the operator as a routing fact and to the guest as the usual uniform
  non-success.
- **Each candidate answers for its own mirror.** A candidate whose feed cannot authorise (`iam_v2.p3_interface_can_authorise`, the feed half of the same predicate as
  `p3_feed_authorizes`) is STALE, never NO_MATCH. "This PMS has no such guest" is only worth something from a PMS that was in a position to say so.
- **No selector, no inference.** The guest is never shown which PMS they belong to; an interface is never inferred from a room number alone; independent PMS
  namespaces are never merged (a room number is only ever looked up inside one interface's Stay table); the first responding PMS is never chosen.
- **A policy that disagrees with its mapping is refused**, not guessed at, at the moment a guest tries (`NONE` with mapped interfaces, `DEDICATED` with other than one,
  `SHARED_STRICT` with fewer than two or more than three).

## A2.2 Changing a policy

There is **no other write path**: a request without a mode, the interfaces and the proposal of its preview is refused, and so is the earlier single-interface request;
returning a network to *not decided* is previewed and confirmed with its proposal too. A proposal is server-held, single-use and short-lived, bound to the network, the previewing
operator, the exact content and the network's change counter, and never exposed by a read, so an approval cannot be reused by another operator, for other content, or after any
intervening change (including a change away and back). No administrative call can replace a shared set with a dedicated route, or remove it, without it.

A change is **previewed, then confirmed against that preview**. The preview names the interfaces added and removed, whether each can answer now, how many room numbers
exist in more than one selected PMS (a guest in one is asked for the reservation number, never matched by room alone), the clients and room-based entitlements online on
the network, and what is *not* touched: no PMS traffic is started or stopped, nothing is posted, package eligibility is not changed, no route is copied from another
network, and guests who are online keep their access. A change is applied only if the network's routing is still the state that was previewed. Preview and apply are
`pms-routing` writes (site administrator); reading is as before. Every change is audited with the previous and new policy.

## A2.3 Topology changes and recovery

A network replaced under another topology (Access to Trunk, a VLAN or port change) keeps **its own** routing and policy; nothing is merged with another network's. A
recovery (Restore Last Working, Restore Factory Defaults, Full Reset) never changes PMS routing: a difference from the last confirmation pauses room sign-in on that network
only, until reviewed (unchanged from D72), and the policy is part of what is compared. After a **confirmed** reset the operator is offered an optional, separate "Configure
room sign-in" step for the resulting network, with the same preview and confirmation; a staged, cancelled, lapsed or rolled-back reset never offers it and never changes routing.

## A2.4 One readiness assessment

Whether room sign-in works is a fact about a network and the interfaces its policy names, assessed once on the appliance (`GET /pms-routing/readiness`) and read by the
dashboard's Needs attention, PMS connections, PMS routing, the Default Network and Client networks. `NONE` is never a fault; an undecided network is a decision to make
only where a PMS in service could serve it; a PMS that also keeps a route to an archived network is not a problem on its own; a shared network is blocked when *any*
selected interface cannot answer (and says so); PMS transport health stays a separate fact (a disconnected transport with an intact mirror still answers); unknown evidence
stays unknown. A warning names the network and interface, links to the page that fixes it, and clears by itself when the cause is corrected.

## A2.5 Rollback

Nothing may be rolled back to a release that predates the policy while a Shared STRICT policy exists: the binary rollback tool and the down migration both refuse (section 16 of
docs/CLIENT_NETWORKING.md). Otherwise, software first, then schema. Rolling the schema back keeps every mapped interface and saves the policies so that applying it again restores those that still agree with the
mapping; the rest fail closed until chosen. A release that predates the policy cannot downgrade a shared set (the database refuses its replace) but resolves it without the cap,
the per-candidate freshness check or the out-of-service guard. Detail and the drill: docs/CLIENT_NETWORKING.md section 16.

## A2.6 Not decided here

Raising the three-interface cap; offering one network a *preference order* between interfaces (which STRICT forbids); routing by anything other than the client network
(for example by SSID or by guest attribute).
