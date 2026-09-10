<!--
READ THIS FIRST — it is three lines and it saves a full CI cycle.

The `governance` gate reads THIS BODY, live, as step 15 of ~21. If the Status block below is wrong or
missing, the gate fails after roughly twenty minutes of work that had already passed. That happened twice
during the PRs #108/#109 delivery, and the second time cost a complete 1,291-second re-run of a gate that had
nothing wrong with it.

Fill in the Status block from the canonical state, not from memory:

    python tools/project-state.py validate      # prints the current phase, status, decision and receipt
    bash tools/preflight.sh --stage 2           # validates THIS body against them, before the gates run

`tools/validate-pr-metadata.sh` requires two things of the Status block:
  1. it must state the recorded status of the current phase, verbatim (e.g. ACCEPTED_AND_CLOSED);
  2. if a decision and a receipt granted that status, it must cite BOTH (e.g. D40 and T0120).
-->

## Status

<!-- Replace every angle-bracket placeholder. Do not delete the block. -->

**Phase \<N\> is \<RECORDED_STATUS\>** (decision **\<D..\>**, receipt **\<T....\>**). This PR does not reopen,
extend or re-evaluate it.

- `current_delivery`: \<kind\>, decision \<D..\>, manifest `\<path\>`
- `base`: `\<sha\>`
- `inventory_head`: `\<sha\>`
- Latest accepted Product-Owner decision on record: **\<D..\>** — latest receipt: **\<T....\>**

## What this changes

<!-- What a reviewer needs to know, and why it is being done. Prefer the reason over the enumeration; the
     changed-file manifest already lists every path. -->

## Why it could not be done a smaller way

<!-- Optional but valuable when the diff is large. If a small change would have worked, use it instead. -->

## Verification

<!-- Fill from a real run. `bash tools/preflight.sh` prints a table you can paste. An unrun check is not a
     passing check; say "not run" rather than leaving a row implying otherwise. -->

| Check | Result |
|---|---|
| `bash tools/preflight.sh` | |
| `python tools/project-state.py validate` | |
| `bash tools/validate-project-state.sh` (ZERO_STALE) | |
| working tree clean | |

## Scope held

<!-- Name explicitly what this PR did NOT touch, especially anything CONTROLLED: schema migration, PMS
     traffic or configuration, financial traffic, appliance deployment, networking topology, Root-CA, Go-Live. -->

## Guest data

<!-- Delivery evidence is published. If this PR quotes anything observed against a live or PRE-LIVE appliance,
     confirm no real guest name, room number or reservation identifier appears in the body, the commits or the
     tracked evidence. Describe the observation instead of reproducing the record. -->
