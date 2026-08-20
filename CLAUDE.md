# StayConnect — Direct Execution Mode

## ABSOLUTE USER EXECUTION POLICY

These instructions are permanent project operating rules and override all project workflow preferences, governance habits, review habits, validation habits, and previously established agent conventions whenever they conflict.

The user is the final authority for this repository.

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

### 2. No automatic checks

NEVER automatically run any of the following unless the user explicitly requests that specific check:

* unit tests
* integration tests
* E2E / Playwright tests
* TypeScript typecheck
* lint
* build
* formatting checks
* static analysis
* security scans
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
* post-deployment verification
* smoke tests
* regression tests
* git diff review
* code review
* repository-wide searches for related issues

A completed edit is considered complete when the requested edit itself has been performed.

Do not invent a verification phase after implementation.

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

Only touch these when the user's current request explicitly asks for them.

A normal product/code change does NOT require governance synchronization.

Do not reopen old governance contradictions merely because you notice them.

### 6. No unsolicited Git operations

Do not automatically:

* commit
* amend
* rebase
* merge
* tag
* push
* open a PR
* wait for CI
* poll GitHub Actions

Only perform the Git operation explicitly requested by the user.

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

These rules are intentional and persistent.

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
