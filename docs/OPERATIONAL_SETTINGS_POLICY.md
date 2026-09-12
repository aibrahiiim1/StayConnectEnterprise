# Operational settings policy

**Status: STANDING RULE. Product-Owner decision, 2026-09-12. It applies to all future work in this
repository until the Product Owner changes it.**

The authoritative short form also lives in [`CLAUDE.md`](../CLAUDE.md) §0C, because that is the file an agent
reads before it starts. This document carries the same rule with the reasoning and the practical checklist.

---

## The rule, verbatim

> Operational values that a hotel administrator may reasonably need to change—such as attempt thresholds,
> waiting periods, retention periods and operational limits—must be available as persisted settings in the
> administration UI. Each setting requires a default, unit, explanation, appropriate scope, permission checks,
> validation and change auditing. Changing such values must not normally require editing code or redeploying.
> Reuse the established configuration system and maintain one source of truth.

---

## Why it exists

The values this rule covers are the ones a property discovers it has wrong at the worst possible moment: a
conference floor of guests mistyping a surname on a full night, a resort whose guests need longer to read a
message than the timer allows, a retention period a group's policy changed. When such a value is a constant in
Go or a flag in a unit file, correcting it costs a code change, a review, a build, a deployment and a service
restart — to change a number. The property either waits, or somebody edits production by hand.

So the default answer to "how many / how long / how often" is **a setting**, not a constant.

## What the rule does NOT cover

This is not an instruction to expose the internals. Protocol constants, cryptographic parameters, wire
formats, timeouts that exist to bound a socket rather than to express a policy, and internal implementation
details stay where they are. The test is whether a **hotel administrator** could reasonably have an opinion
about the value and be right. Nobody at a front desk has an opinion about a GCM nonce length.

It is also not a licence to start a project-wide settings refactor. The rule governs work as it is done: when
you are implementing something that carries an operational number, that number becomes a setting. Existing
constants elsewhere are not a backlog this rule creates.

## What every such setting must have

| Requirement | What it means in practice |
|---|---|
| **A default** | The system works correctly with nobody having opened the screen. Absence of a saved value means the approved default — never "the feature is off". |
| **A unit** | In the label, not only in the help text. "60" has been read as minutes. |
| **An explanation** | What it does, in the words an operator uses, next to the field. |
| **Appropriate scope** | Usually per-site. Say so in the schema (the scope is part of the primary key), not only in a comment. |
| **Permission checks** | Server-enforced, on a resource key. Hiding a control in the browser is a courtesy, never the boundary. |
| **Validation** | Bounds in the domain type AND as a database CHECK, so an impossible value is unstorable even by a caller that skipped the handler. Send the bounds to the UI rather than duplicating them there. |
| **Change auditing** | Actor, timestamp, previous values and new values. Mandatory by privilege where the value is a security control: put the write and the change-log insert in one definer function and grant no role direct UPDATE, so there is no privilege that changes the value without recording who changed it. |
| **No restart** | The enforcement path reads the value from its store on each decision. Nothing caches it in a process. |
| **One source of truth** | Enforcement, the API and the UI all read the same function or table. If any two could disagree, the screen is showing a policy the property is not running. |

## Say what a change does and does not affect

A setting that changes future decisions but leaves existing state alone must say so **on the screen**.
An operator shortening a waiting period to help the guest in front of them will otherwise watch nothing
happen and conclude the setting is broken. Name the action that does affect the existing state, if there is
one.

## The worked example

Guest sign-in protection (Hotel Admin → **Sign-in methods** → *Guest sign-in protection*) is the reference
implementation of every row in the table above:

* three values — maximum failed attempts, observation window, restriction duration — with defaults 5 / 60s /
  60s, units in the labels and the allowed range in the help text;
* site-scoped: `iam_v2.site_guest_signin_protection` is keyed on `(tenant_id, site_id)`;
* bounds in `data-plane/internal/signinattempt/policy.go` and again as a table CHECK; the API returns the
  bounds so the form cannot drift from the server;
* two server-enforced resource keys (`guest-signin-protection` to change the policy,
  `guest-signin-restrictions` to release one restriction), neither implying the other;
* append-only `iam_v2.guest_signin_protection_changes`, written inside
  `iam_v2.guest_signin_protection_set` — no runtime role holds UPDATE on the settings table, so the change
  log is mandatory by privilege;
* `scd` reads `iam_v2.guest_signin_protection_get` on every decision, so a saved change applies to the next
  submission with no restart, build or deployment;
* the screen states that changed settings apply to what happens next and that a restriction already running
  keeps the expiry it was given, and points at the Release action for the case where that is not what the
  operator wants.

## Existing settings that already follow it

* Sign-in methods (`auth_methods`) — which methods the portal offers.
* Checkout grace (`iam_v2.site_checkout_grace_config`) — how long after checkout a guest may still sign in.
* Guest device self-service — whether guests may remove their own devices.
* Access plans — duration, data cap, device limits.
* Portal branding, walled garden, notification and social providers.

When you add the next one, add it to this list.
