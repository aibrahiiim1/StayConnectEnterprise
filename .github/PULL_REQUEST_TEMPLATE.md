<!--
DELIVERY MODEL: D42 (docs/PO_LED_DELIVERY_MODEL.md).

NO PRODUCT OWNER ACCEPTANCE = NO MERGE. The only required check is `po-merge-authorization`. It passes only
while this PR carries the label `po-merge-approved` on its current head, applied after the Product Owner's
explicit approval ("Approved", "Merge it", ...) or when the mission authorized the merge in advance. Any
later push removes the label.

The comprehensive gates run only for "FULL CHECK THE WHOLE CODE" (python tools/full-check.py --ref <branch>).
When they do, the `governance` gate reads THIS BODY live via tools/validate-pr-metadata.sh, which requires the
Status block to state the current phase status verbatim and to cite the decision and receipt that granted it.
Fill it from the canonical state:  python tools/project-state.py validate
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
| FULL CHECK (only if requested) | not requested |

## Scope held

<!-- Name explicitly what this PR did NOT touch, especially anything CONTROLLED: schema migration, PMS
     traffic or configuration, financial traffic, appliance deployment, networking topology, Root-CA, Go-Live. -->

## Guest data

<!-- Delivery evidence is published. If this PR quotes anything observed against a live or PRE-LIVE appliance,
     confirm no real guest name, room number or reservation identifier appears in the body, the commits or the
     tracked evidence. Describe the observation instead of reproducing the record. -->
