# ADR-0003 — Ruleset structure is reconciled from the current render, never from a stored bundle

**Status:** Accepted (Phase 3, DARK)
**Supersedes:** nothing
**Context date:** 2026-08-11 (written after Live Increment 9 found the defect on a real appliance)

## The problem

`netd` re-asserts the guest-network ruleset when it starts, so that a reboot — where the kernel loads only the
static `/etc/nftables.conf` — does not leave the appliance forwarding by an older, IP-only ruleset. It did this
by replaying the file it had generated at the time of the last network apply:
`<bundle>/stayconnect.nft`, taken from the active row of `network_config_revisions`.

Live Increment 9 deployed the Phase-3 software to appliance `172.21.60.23` and installed the Phase-3 packet
authorization foundation into the running ruleset. The install was correct and surgical: legacy `auth_ipv4` was
byte-identical afterwards, chain and DNAT counts were unchanged, nothing was flushed.

The next `netd` start deleted all of it. So did the reboot. After the reboot the `inet stayconnect` table was
**structurally identical to the pre-install baseline — zero diff lines** — and `phase3_auth_ipv4` was gone.

The stored file was `/etc/stayconnect/generated/network/revision-000056/stayconnect.nft`, rendered
**2026-07-14 by the pre-Phase-3 binary**. It begins with `delete table inet stayconnect` and contains **zero**
occurrences of `phase3_auth_ipv4`.

Nothing was corrupt. The database was correct. The bundle was intact and exactly what it claimed to be. The
appliance was still wrong, every single time it started.

## The insight

Ruleset structure is a pure function of three inputs:

    structure = f(guest-network intent, appliance topology, RENDERER VERSION)

Two of those live in the database and are versioned there. **The third exists only in the running binary**, and
a stored artifact cannot represent it — a file rendered in July has no way to know that August's software needs
a set it has never heard of. Replaying it does not "restore" the ruleset; it *reverts* it, silently, to a
structure the running software has already moved past.

This is not specific to Phase 3. Any future change to `render_nft.go` would have been reverted the same way on
every appliance whose last network apply predated it.

## The decision

**Boot reconciliation renders the ruleset from the current binary and compares it against the kernel. The
stored bundle becomes a record of what was applied, not the instruction for what to apply next.**

To make that comparison possible, the render carries its own fingerprint into the kernel: an empty marker set
`sc_render_fp` whose comment holds the SHA-256 (first 16 bytes) of the rendered body. The fingerprint is
computed over the body *without* the marker, so it describes the structure rather than itself. The question
"does the live ruleset match what this binary would build?" is then answered by reading the live ruleset — the
only place the answer can actually be true.

Reconciliation has exactly two outcomes:

| | condition | what happens |
|---|---|---|
| **steady state** | live fingerprint == current render | **nothing is executed.** Not a narrower command, not an idempotent re-apply. Zero mutations. |
| **upgrade** | fingerprint differs, or the table is absent | the current render is applied as ONE atomic `nft -f` transaction, with the live authorization carried across inside that same transaction. |

### Why both halves are required

They defend against opposite failures, and either one alone leaves a real defect:

- **Skip-when-equal alone** would still make the one upgrade destructive — the render begins with
  `delete table`, so converging would deauthorize every guest then online.
- **Carry-over alone** would mean every routine restart rewrote the live ruleset. Even done perfectly that is a
  needless rewrite of the thing that decides guest access, on a path that runs unattended.

The production invariant is stated as the first row of that table and asserted on the **command log**, not on
the resulting ruleset: re-applying the same render leaves an identical structure while destroying every live
authorization, so a test that only inspected the ruleset could not tell the two apart. `SteadyStateRestartIssues
NoMutation` fails if a single `nft` mutation is issued.

### Carry-over distinguishes three cases, and collapsing any two is a bug

| live element | carried as |
|---|---|
| `Expires > 0` | its **remaining** lease. Never the original — an element created with a 90 s lease 89 seconds ago has one second left, and re-adding it with 90 s extends an authorization past the boundary that granted it. |
| `Timeout > 0, Expires <= 0` | **dropped.** The kernel was about to remove it; carrying it would resurrect an expired authorization. |
| `Timeout == 0, Expires == 0` | **permanent, with no timeout clause.** This is what legacy `scd` writes into `auth_ipv4`; treating it as expired would knock every legacy guest offline during the upgrade. |

## Consequences

- An appliance whose active revision predates the current renderer **converges by itself on the next start**.
  No operator step, no separate migration, no bundle rewrite is required for the structure to become correct.
- `phase3_auth_ipv4` is durable because it is part of the render, not because someone ran an install tool. The
  standalone `cmd/phase3-foundation` remains useful for inspecting a running appliance and for a surgical
  install/rollback with legacy-parity evidence, but the steady state no longer depends on it.
- Rollback of a failed network apply also renders — from the previous revision's stored **intent**, which every
  revision already carries as `jsonb` — rather than replaying that revision's file. Same defect, same fix.
- A converge that cannot be done safely **refuses**: an authorization set that cannot be read, a load that
  fails, or a post-apply fingerprint that does not match are all errors, and the live ruleset is left untouched.
- The marker is a real object in the ruleset. It is empty, matches nothing, and costs one set.

## What this ADR does not claim

The mechanism is proven by the modelled suite in `cmd/netd` (decision logic and the command log) and by the
real-kernel suite in `internal/kerneltest` (nftables really accepts and returns the comment; the generated
ruleset really loads; a legacy-authorized guest really stays online across the atomic replace, verified with
packets). **Both run on disposable machines.** As of this ADR the corrected software has *not* been deployed to
the appliance, and the live re-verification listed in the Phase-3 report §6b remains outstanding.
