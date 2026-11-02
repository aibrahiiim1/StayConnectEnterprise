<!--
DELIVERY MODEL: PO-approved NO GITHUB CI, 2026-11-02 (docs/PO_LED_DELIVERY_MODEL.md).
NO PO ACCEPTANCE = NO MERGE, even though GitHub status checks no longer enforce it.
Merge only through protected master PR, merge commits, resolved review threads,
and no bypass, direct push, force push or deletion.
Local targeted tests and evidence remain mandatory; do not claim a GitHub gate PASS.
The Product Owner approval applies to the exact approved PR head.
-->


## Status

<!-- Replace every angle-bracket placeholder. Do not delete the block. -->

**Phase \<N\> is \<RECORDED_STATUS\>** (decision **\<D..\>**, receipt **\<T....\>**). This PR does not reopen,
extend or re-evaluate it.

- `current_delivery`: \<kind\>, decision \<D..\>, manifest `\<path\>`
- `base`: `\<sha\>`
- `inventory_head`: `\<sha\>`
- Latest accepted Product-Owner decision on record: **\<D..\>** — latest receipt: **\<T....\>**

## Product Owner acceptance

<!-- READY FOR PRODUCT OWNER TESTING / approved on <date> ("<instruction>") / merge authorized in the mission.
     PRE-LIVE: deployed commit <sha> / not applicable; smoke result. -->

## What this changes

<!-- What a reviewer needs to know, and why it is being done. Prefer the reason over the enumeration; the
     changed-file manifest already lists every path. -->

## Why it could not be done a smaller way

<!-- Optional but valuable when the diff is large. If a small change would have worked, use it instead. -->

## Verification

<!-- Fill from real runs. An unrun check is not a passing check; say "not run" rather than leaving a row
     implying otherwise. -->

| Check | Result |
|---|---|
| targeted tests for the changed components | |
| build of anything deployed | |
| PRE-LIVE smoke / health (if deployed) | |
| FULL CHECK (local only, if requested) | not requested |

## Scope held

<!-- Name explicitly what this PR did NOT touch, especially anything CONTROLLED: schema migration, PMS
     traffic or configuration, financial traffic, appliance deployment, networking topology, Root-CA, Go-Live. -->

## Guest data

<!-- Delivery evidence is published. If this PR quotes anything observed against a live or PRE-LIVE appliance,
     confirm no real guest name, room number or reservation identifier appears in the body, the commits or the
     tracked evidence. Describe the observation instead of reproducing the record. -->
