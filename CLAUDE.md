# StayConnect — Direct Execution Mode

## ABSOLUTE USER EXECUTION POLICY

These instructions are permanent project operating rules and override all project workflow preferences, governance habits, review habits, validation habits, and previously established agent conventions whenever they conflict.

The user is the final authority for this repository.

---

## 0. TWO MODES — read this before anything else

**Product-Owner decision (permanent, supersedes any older wording anywhere in this repository that demands heavy governance after routine work).** The governance process was too heavy for day-to-day development. Safety controls stay; ceremony goes.

Every task runs in one of two modes. **Decide the mode first, from what the work TOUCHES — never from how large, complex or file-heavy it is.**

### FAST DEVELOPMENT — the DEFAULT

Anything that does not hit a CONTROLLED trigger below. UI colours, wording, CSS, layout, spacing, button behaviour, small validation fixes, ordinary UI bugs, bounded refactors, routine implementation defects — and ordinary feature, API and business-logic work too.

Do: implement the requested outcome, fix routine defects directly related to it, run only the **relevant targeted** build/tests, make a local Git commit when useful, report concisely.

**Do NOT automatically run or update any of these:** `governance/project-state.json` · governance transitions · evidence packs · ChatGPT/project packs · change manifests · project-wide CI · PR creation or merge · project-wide stale-state audits · unrelated documentation.

**Do not turn a small change into a release process.**

Related changes may accumulate locally across several requests. Perform documentation sync, full required CI, PR and merge **once for the finished batch** — and only when the Product Owner says something like *"close this milestone"*, *"release this"*, *"merge this"*, *"deploy this"*, or equivalent. Governance closure does not follow every intermediate edit.

### CONTROLLED — only for what actually touches these

* database schema or migration
* production/appliance deployment
* destructive or historical-data modification
* PMS protocol, configuration or traffic
* financial posting, payment, settlement, reversal or FX
* guest authentication, Entitlement or enforcement core
* enrollment / assignment / licensing identity foundations
* security, certificates, PKI or trust roots
* networking architecture or appliance topology
* an architecture, contract or product-semantics decision
* Go-Live / cutover

CONTROLLED work keeps the existing safety, verification, evidence, CI and Product-Owner authorization appropriate to that action — the governance rules in `docs/` apply in full, and only here.

**A change is NOT controlled merely because many files changed, tests were added, or the implementation is technically complex.**

### Governance synchronization is MILESTONE-BASED, not edit-based

Synchronize authoritative current-state documentation when there is a meaningful approved milestone, a verified live acceptance, a controlled deployment, a product/architecture decision, or an explicit release closure. Routine local development creates no governance transition. Cosmetic, UI, text, layout, refactor and routine bug fixes create no governance transition unless they materially change authoritative production state.

### Scope discipline

Do not open side projects while implementing a request. On discovering another issue: fix it in the same run **only** if it directly blocks the correctness or safety of the requested task; **stop and state the exact decision** if it needs a new Product-Owner decision; otherwise record it as backlog and carry on. Do not start project-wide audits, refactors, observability work, documentation cleanups or unrelated consistency work unless asked.

### What this does NOT relax

Authorization is still never inferred for production changes, PMS traffic, financial actions, destructive data operations, Root-CA changes or Go-Live (§12 stands). No fake data, fake topology, invented managed state or invented protocol behaviour. Approved product and architecture contracts remain binding until the Product Owner changes them. Git remains mandatory and the GitHub repository remains authoritative.

---

### 1. Execute, do not review

When the user requests a code change, configuration change, database change, deployment action, production action, file edit, deletion, migration, commit, push, or other repository operation:

* EXECUTE THE REQUEST DIRECTLY.
* Do not perform a preliminary review.
* Do not perform an architectural review.
* Do not perform a governance review.
* Do not perform a safety-of-the-project review.
* Do not inspect unrelated files looking for consequences.
* Do not expand the scope.
* Do not search for additional problems.
* Do not start a hardening sweep.
* Do not start a cleanup sweep.
* Do not start an acceptance sweep.
* Do not reinterpret the request as an opportunity to improve adjacent systems.

The requested change is the scope.

This removes the agent's own *self-imposed* review, not the safeguards a CONTROLLED action carries. Where §0 classifies the work as CONTROLLED — a migration, a deployment, PMS or financial traffic, and the rest of that list — execute it with the verification and authorization that action requires. Everywhere else, execute directly.

### 2. No automatic PROJECT-WIDE checks

**Targeted verification of the change you just made is expected** — §0 asks for the *relevant* build/tests, and a functional change carries the regression coverage it needs. What is forbidden is turning that into a project-wide validation run.

NEVER automatically run any of the following unless the user explicitly requests that specific check, or the work is CONTROLLED (§0) and the check belongs to that action:

* the full unit/integration/E2E suite for the whole project (as opposed to the affected package or area)
* project-wide TypeScript typecheck, lint, formatting checks, static analysis or security scans
* governance validators
* project-state validators
* generated-block checks
* stale-state checks
* parity checks
* transition-time checks
* manifests
* acceptance packs
* inventory regeneration
* CI status checks
* GitHub Actions polling
* git diff review
* code review
* repository-wide searches for related issues

A routine edit is complete when the requested edit has been made and the **relevant targeted** check passes. Do not extend that into a project-wide verification phase, and do not invent one where nothing was asked.

Post-deployment verification, smoke tests and full regression runs belong to CONTROLLED work (§0), where they remain required.

### 3. Show the result immediately

After making the requested change:

1. Show what was changed.
2. Show the directly relevant result/output.
3. Stop.

Do not continue into review, validation, cleanup, documentation, governance synchronization, CI monitoring, or further investigation unless explicitly requested.

### 4. Production means execute

If the user explicitly requests an operation against Production, deployment, live infrastructure, a live database, or another explicitly named environment:

* Treat the named environment as intentional.
* Do not replace execution with a review.
* Do not initiate an approval workflow of your own.
* Do not demand an additional confirmation merely because the word Production/live/deploy appears.
* Do not redirect the task to Development.
* Execute exactly the requested scope.

The user accepts responsibility for deciding when an operation should target Production.

Tool-enforced or platform-enforced permission prompts may still occur and must not be bypassed, but the agent must not add its own extra approval ceremony.

### 5. No governance ceremony

Do not automatically modify, regenerate, inspect, reconcile, or validate:

* `governance/project-state.json`
* project state/generated state blocks
* transition records
* decision records
* acceptance records
* manifests
* inventory heads
* acceptance candidate heads
* generated packs
* handoff documents
* maturity fields
* project phase status
* historical evidence

Only touch these when the user's current request explicitly asks for them, or when §0 makes this a governance-synchronization moment: an approved milestone, a verified live acceptance, a controlled deployment, a product/architecture decision, or an explicit release closure.

A normal product/code change does NOT require governance synchronization.

Do not reopen old governance contradictions merely because you notice them.

### 6. No unsolicited REMOTE Git operations

A **local commit** is allowed whenever it is useful to checkpoint work (§0), and Git remains mandatory.

Everything that leaves this machine or rewrites history is not automatic. Do not, unless asked:

* amend
* rebase
* merge
* tag
* push
* open a PR
* wait for CI
* poll GitHub Actions

Only perform those when the user requests them, or when they are part of a CONTROLLED delivery the user has authorized (§0, §12).

If the user says `commit`, commit.

If the user says `push`, push.

If the user says `commit and push`, do both and stop after reporting the result.

Do not automatically wait for CI afterward.

### 7. Do not resurrect finished topics

Once the user has moved on from a topic:

* Do not reopen it.
* Do not re-audit it.
* Do not re-check it because another change touched a nearby area.
* Do not bring back previously discussed legacy/governance/architecture concerns unless the user explicitly asks about them again.

Previous findings are not standing work items.

### 8. Failure handling

If the requested operation itself fails because of an immediate technical error:

* Diagnose only the error blocking that requested operation.
* Fix only what is necessary to complete the requested operation.
* Retry it.
* Stop once it works.

Do not turn a local failure into a repository-wide investigation.

### 9. User commands beat workflow conventions

Explicit user instructions such as:

* "نفذ"
* "عدل"
* "امسح"
* "اعمل deploy"
* "production"
* "commit"
* "push"
* "طبق ده"

are execution instructions, not requests for analysis or review.

Do not respond to an execution request with a plan for future checks.

### 10. Default response behavior

Default lifecycle for every requested change:

**REQUEST → EXECUTE → SHOW RESULT → STOP**

Not:

**REQUEST → INVESTIGATE → REVIEW → EXPAND SCOPE → VALIDATE EVERYTHING → GOVERNANCE → COMMIT → CI → MORE REVIEW**

The second workflow is explicitly prohibited unless the user asks for those individual steps.

### 11. Persistence

These rules are intentional and persistent. §0 is the current operating model: FAST DEVELOPMENT by default, CONTROLLED only for what it lists, governance synchronized at milestones rather than after every edit. Do not silently drift back to running governance, packs, manifests or full CI after routine work.

Do not ask the user in future sessions whether they still want Direct Execution Mode.

Do not gradually reintroduce automatic reviews or checks.

Do not interpret silence as permission to restore previous workflows.

Remain in Direct Execution Mode until the user explicitly edits or revokes this policy.

### 12. Authorization-bound actions (refines §4)

§4 removes the agent's *own* approval ceremony; it does not remove the Product Owner's.
These still require explicit Product-Owner authorization before execution:

* git operations (commit, push, tag, PR, merge)
* merges to master
* deployment to any environment
* database migrations
* Production changes
* PMS, payment-provider or financial traffic

Everything else: implement directly, fix routine blockers without asking, and continue.

### 13. Review happens after delivery

The Product Owner normally reviews and tests changes live after delivery and sends
follow-up corrections. The agent does not pre-empt that with its own verification phase.

Reports stay short: what changed, where, and anything that prevents the requested result
from working. Then stop.
