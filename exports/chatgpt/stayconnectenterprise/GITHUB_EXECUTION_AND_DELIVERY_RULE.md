# PERMANENT RULE — GitHub Execution, Reporting and Delivery (project-wide, mandatory)

**Authoritative, permanent Product-Owner operating rule for the remainder of the StayConnect Enterprise project.** It governs how every future Phase and milestone is executed, verified, reported and delivered through GitHub. It is a companion to, and does not replace, the [Zero-Stale-Leftovers rule](ZERO_STALE_LEFTOVERS_RULE.md) or the machine-readable project-state governance (`governance/project-state.json`, `tools/project-state.py`). Where this rule and the project-state governance overlap, both must pass.

<!-- MACHINE ASSERTION - validated by tools/project-state.py -->
`GIT_OPERATIONS_OWNER: AGENT`  (all Git and GitHub operations are performed by the authorized AI Agent; the Product Owner is never asked to run routine Git or `gh` commands — see §12)

## 0. Authoritative repository

- Repository: **`https://github.com/aibrahiiim1/StayConnectEnterprise.git`** (remote `aibrahiiim1/StayConnectEnterprise`).
- The GitHub repository is the **only** authoritative project source.
- Uploaded ZIP files (Project Pack / Evidence Pack / Planning Pack) are **exports and review artifacts only**. They must never silently override the repository state, Git history or verified execution evidence. When a ZIP and the repository disagree, the repository wins and the discrepancy is reported.

## 1. GitHub is the only authoritative working source

**Scope (Product-Owner decision).** The full preflight below is a **CONTROLLED-work** procedure — a migration, a deployment, an approved milestone or phase, an authoritative export, or an explicit release closure, per `CLAUDE.md` §0. Routine FAST DEVELOPMENT does not run it: for ordinary code, UI and bug work, use the current checkout, run only the relevant targeted tests, and commit locally. The repository is still the authoritative source and Git is still mandatory; what changes is that a routine edit no longer triggers a release preflight.

Before every CONTROLLED task, in order:

1. Fetch the remote repository (`git fetch origin`).
2. Verify the expected remote is `aibrahiiim1/StayConnectEnterprise`.
3. Verify the base branch and current HEAD (record both SHAs).
4. Ensure the full repository is clean (`git status --porcelain --untracked-files=all` is empty).
5. Run the project-state governance validator (`python tools/project-state.py validate` → `PROJECT_STATE_GOVERNANCE = PASS`) and the keyword layer (`tools/validate-project-state.sh` → `ZERO_STALE_LEFTOVERS = PASS`).
6. Read the authoritative project sources in their required precedence order (project-state governance → FINAL contract → synchronized handoff → current phase plan → verified evidence → system/ops docs).
7. Confirm the current Phase, authorization scope and the single next authorized action.

Do not work from an old ZIP, a copied folder or a stale local branch. Do not claim a commit exists until it is pushed and reachable from the GitHub remote.

## 2. One complete Phase per branch and PR

- Use **one implementation branch per approved Phase or major authorized milestone**.
- Use **one PR for the complete Phase**.
- Do not create unnecessary micro-branches, micro-PRs or extra Product-Owner prompts.
- Complete the full authorized Phase end-to-end inside that branch: implementation, migrations, tests, rollback, documentation, governance updates and exports in the **same** Phase delivery.
- **Never** continue automatically into the next Phase.

Recommended branch format: `phase/<phase-name>-<short-purpose>` — e.g. `phase/1b-dark-auth`, `phase/2-packages-quotes`, `phase/3-multipms-resolution`. Governance-only updates use `governance/<short-purpose>`.

Do not commit directly to the protected default branch unless the Product Owner explicitly authorizes that exact action.

## 3. No unnecessary stopping

A Phase authorization permits completing all work explicitly included in that Phase without stopping for minor internal decisions. **Do not stop** for: normal implementation choices already governed by the approved contract and Phase plan; routine file changes; tests and fixes inside the authorized Phase; documentation synchronization; export regeneration; ordinary refactoring required to complete acceptance.

**Stop only for a genuine hard blocker:** a contradiction with an authoritative Product-Owner decision; a missing prerequisite that cannot be safely created within the authorized scope; a security or financial risk; an action outside the authorized Phase; a production failure that cannot be safely rolled back; an acceptance failure requiring an architecture or contract decision; or a missing Product-Owner authorization for live traffic, cutover, financial posting or another separately-gated action.

When stopping, report **one precise blocker** and the recommended resolution. Do not restart the entire Phase.

## 4. Complete automatic changed-file manifest

Every final Agent report must include **every affected file without exception**, generated by `tools/generate-change-manifest.py` (never a hand-written list). The generator produces a deterministic manifest from the Phase base commit to HEAD using Git, and must include: base commit; branch name; HEAD commit; remote branch; every created, modified, deleted, renamed (old + new path), copied, generated and exported file; submodule changes if present; binary-file changes; and total diff statistics.

Required classifications:

```text
CREATED
MODIFIED
DELETED
RENAMED
COPIED
GENERATED
EXPORTED
UNCHANGED-BUT-VERIFIED
```

For every changed file, state: the exact path; classification; a concise purpose; the owning workstream or migration group; whether it affects runtime, database, configuration, documentation, tests, governance or export; and whether rollback removes or restores it. The generator emits exactly these columns per file — **Path · Classification · Git status · Domain · Workstream · Rollback · Purpose** — and no changed path may have an empty **Workstream** or **Purpose** value. Workstream is reproducible: it uses an explicit bracket prefix on the latest touching commit subject where present (e.g. `[W0]`, `[W1]`, `[MG-3]`, `[GOVERNANCE]`, `[EXPORT]`, `[CI]`) and otherwise falls back to a deterministic path-based classification.

The manifest is generated from commands equivalent to:

```bash
git diff --name-status --find-renames --find-copies <base>..HEAD
git diff --stat <base>..HEAD
git status --short --untracked-files=all
git log --oneline <base>..HEAD
```

The final report may add explanation, but it must not omit or contradict the generated Git manifest. **If the report's file list differs from Git, the delivery fails.**

### 4.1 Manifest self-reference protocol (`inventory_head` / `delivery_head`)

The complete-manifest rule requires that **every path changed between the Phase base and the final PR delivery HEAD is represented exactly once**. Because a committed manifest cannot contain the commit SHA that commits the manifest itself, use this deterministic, non-self-referential protocol:

- **`inventory_head`** — the committed HEAD immediately before the final delivery-only commit. It is the manifest's generation/provenance HEAD and is recorded in `governance/project-state.json` as `acceptance_candidate_head` (and `inventory_head`).
- **`delivery_head`** — the final PR HEAD (the delivery-only commit).
- The committed manifest must include the **complete `base..delivery_head`** path/status inventory. The manifest **records `inventory_head`** as its generation/provenance HEAD (its `HEAD commit:` line).
- The final delivery-only commit **may modify only paths already represented in the manifest** and must **introduce zero unlisted paths** (it (re)builds the manifest, export packs, checksums and the pointer/provenance only). Equivalently, every path that will exist in the final PR diff already exists in, or is enumerated by, the manifest generated for `delivery_head`.
- Governance CI **compares the manifest's complete path/status set against the actual** `git diff --name-status <base>..<delivery_head>`. **Any missing, extra, or status-mismatched path fails governance.** Export packs, checksums, manifests and provenance files are **not exempt** from the inventory.
- The Final Report and the PR body must record **both** HEADs (`inventory_head` and `delivery_head`) and the **actual final Git changed-file count**.

A substantive-only manifest (for example, one that lists the substantive source commit but omits the export packs/manifest/pointer paths that the final PR diff actually contains) **does not** satisfy this rule: it is not permissible for the committed manifest to represent fewer paths than `git diff --name-status base..delivery_head`.

## 5. Mandatory final report structure

Every Phase or milestone report must contain, in order: (1) simple Egyptian-Arabic explanation; (2) current Phase and authorized scope; (3) what was implemented; (4) practical effect; (5) risks and limitations; (6) acceptance tests with PASS/FAIL/DEFERRED/NOT-AUTHORIZED; (7) production and guest impact; (8) rollback status; (9) security and isolation results; (10) the complete generated changed-file manifest; (11) all commits created; (12) branch and PR information; (13) remote reachability of HEAD; (14) full working-tree status; (15) documentation and governance synchronization; (16) Project/Evidence Pack paths and checksums where applicable; (17) `PROJECT_STATE_GOVERNANCE` result; (18) `ZERO_STALE_LEFTOVERS` result; (19) remaining blockers; (20) exactly one next proposed action.

The canonical template is templates/PHASE_FINAL_REPORT_TEMPLATE.md.

**Forbidden summaries:** "docs updated"; "several files changed"; "related files modified"; "minor configuration changes"; any file list using ellipses; any report that excludes generated or deleted files.

## 6. Push and PR verification

Before reporting completion: commit all authorized changes intentionally; push the branch to GitHub; verify the pushed HEAD exists on the remote; open or update the single Phase PR; verify the PR base and head branches; verify all required CI checks; include the PR URL, the exact pushed HEAD SHA, and confirm the local and remote branch SHAs match. **A local commit is not a delivered result.** Do not claim GitHub delivery if the branch was not pushed.

## 7. Full-repository cleanliness

`git status --porcelain --untracked-files=all` must be clean before starting and before finishing (dirty only while the authorized task is actively being performed). **No permanent exception list for dirty files is allowed.** Generated build artifacts must be removed, restored, or explicitly untracked and ignored through a committed decision. Do not leave: local build outputs; temporary backups; secrets; logs containing PII; test databases; exported credentials; unexplained archives; or abandoned scripts.

## 8. Governance gates

**Before implementation:** `PROJECT_STATE_GOVERNANCE = PASS`; generated project-state blocks match the canonical state; transition history is valid; the current authorization permits the task; the full repository is clean.

**After implementation:** all acceptance tests pass or are truthfully classified; documentation is synchronized; the project-state transition is appended where authorized; the artifact registry is updated; the complete changed-file manifest is generated; repository and extracted-pack validation pass; `ZERO_STALE_LEFTOVERS = PASS`; the full repository is clean; the branch is pushed and the PR is available.

**Repository-side enforcement (GH-MANDATORY-CI).** GitHub Actions is the authoritative merge gate. The workflow `.github/workflows/project-governance.yml` (workflow name **Project Governance**, job **governance**) runs on every pull request targeting `master`, every push to `master`, and manual `workflow_dispatch`; it checks out full history (`fetch-depth: 0`), runs `python tools/project-state.py validate`, `python tools/project-state.py check-generated`, `python tools/tests/project_state_validator/run_mutations.py` and `bash tools/validate-project-state.sh`, and then asserts the working tree is still clean — failing on any non-zero step. It uses read-only repository permissions and performs no deployment, database access, migrations or production tests. **A PR is not merge-ready until this check is green** — local validator output alone is insufficient for a delivered PR. The default branch should be protected to require the `governance` check.

**What is actually enforced, and by what (WHO-ENFORCES-WHAT).** Verified against the GitHub API and
re-verified by `tools/validate-branch-protection.py` on every governance run.

*The mechanism is a repository RULESET named `master-protected-delivery`, not classic branch protection.*
Classic protection was **deleted** on 2026-09-10. It could not express what this project needs: its
`enforce_admins: false` let the sole administrator bypass every requirement, it permitted force pushes, and
it has no way to say "nobody may bypass". The ruleset has an **empty bypass-actor list**, so it applies to
everyone including the repository owner.

*Enforced by GitHub for every change to `master`, with no bypass actor:*

- **all four mandatory gates** — `governance`, `phase3-full-software-gate`, `phase4-financial-core-gate`,
  `phase5-post-stay-transfer-gate` — each **pinned to the GitHub Actions app (id 15368)**, so a check of the
  same name from any other app or token cannot satisfy it;
- **strict**: the branch must be up to date with `master` before merging;
- **a pull request is required** — direct pushes to `master` are refused for everyone;
- **conversation resolution** is required;
- **merge commits only** — squash and rebase are blocked, because they rewrite the tree and would break both
  the audit link between the validated pull-request head and what lands, and the CI evidence-reuse design
  that depends on that tree being identical;
- **force pushes are blocked** (`non_fast_forward`);
- **branch deletion is blocked**.

*Approving reviews are set to ZERO, and that is a measured constraint rather than a preference.* The
repository has exactly one collaborator, who authors every delivery, and GitHub refuses self-approval —
verified live: the API answers `Review Can not approve your own pull request`. A non-zero requirement is
therefore satisfiable only by granting an administrator bypass, which is the silent hole this model exists
to remove. Universal enforcement of everything that *can* be enforced was chosen over a nominal requirement
that only a bypass makes workable. **Raising this to one approving review requires a second reviewing
account and is an open Product-Owner decision**, recorded in `governance/branch-protection.json`.

*A reading trap worth knowing.* `GET /repos/{owner}/{repo}/branches/master/protection` now answers
**"Branch not protected"**. That endpoint reports classic protection only and knows nothing about rulesets.
The authoritative reads are `GET /repos/{owner}/{repo}/rules/branches/master` and
`GET /repos/{owner}/{repo}/rulesets`; both are readable without a token on this public repository, which is
what lets CI verify them every run.

*Residual, stated plainly.* An administrator cannot bypass these rules at merge time — proven by live test —
but can still edit or delete the ruleset itself. No control exists on a user-owned repository to prevent
that. The mitigation is that any **weakening that leaves the ruleset in place** fails the governance gate
immediately, and ruleset edits are recorded in its own history; outright deletion cannot be caught by a
check that the ruleset is what makes mandatory.

**How the gates spend their time, and what is never recomputed twice (CI-REUSE).** The four gates run in
parallel, so the delivery path is the slowest of them, twice: once on the pull-request head and once on the
merge commit. Two things were measured and changed.

*The adversarial mutation matrix is evaluated concurrently.* Each of its sixty cases costs a full double
validation — the structural validator plus the keyword validator over the whole tree, about sixteen seconds
on a runner — and they were evaluated one after another because they shared a single sandbox. Every worker
now gets its OWN sandbox and the cases run at once. Nothing about the checking changed: every case still
runs, both validators still run per case, `--require-full` still refuses a partial matrix, and the printed
`MUTATION_CASES_EXECUTED` count is still asserted against the total. `MUTATION_MAX_CASES` forces serial
execution, because that knob exists for the isolation regression's short overlapping child runs and giving
those an internal fan-out would change what that regression measures.

*Identical content is not validated twice — but only where content is the whole input.* Each gate first asks
whether it has already proven THIS EXACT TREE green, via `scripts/ci/evidence-reuse.sh`. This matters because
the merge commit's tree was byte-identical to the pull-request head's tree in thirteen of the last thirteen
merges — a merge introduces a commit, not content — so the post-merge run was re-deriving a verdict it
already held.

**A tree hash is not a complete evidence key, and the first version of this mechanism wrongly assumed it
was.** Tree equality proves two commits have identical CONTENT. It does not prove they have the same HISTORY,
were judged at the same MOMENT, or ran against the same EXTERNAL WORLD. Four checks in these gates depend on exactly
those things: `tools/validate-transition-times.sh` and its self-test read the COMMIT GRAPH (`git log
--diff-filter=A` for a receipt's introducing commit, `git log -1` for when a merge actually happened);
`tools/validate-project-state.sh` resolves the manifest's `SOURCE_COMMIT` against the object graph; and
`scripts/ci/phase4-dependency-gate.sh` queries the LIVE npm registry and compares acceptances against TODAY's
date, so the same bytes turn from PASS to FAIL when an advisory is published or an acceptance lapses. A
fifth case is not a check at all: the evidence artifacts are PRODUCED OUTPUT, and guarding their upload meant
the commit that actually landed on master carried no evidence artifact.

So reuse is governed by two separate mechanisms, both fail-closed.

**What may be skipped is declared, not assumed.** `governance/ci-reuse-policy.json` classifies EVERY step of
EVERY required gate as `tree-pure` (an earlier run over an identical tree already answers it), `always` (its
inputs are not fixed by the tree, or it produces output this run owes — with the reason recorded), or
`reuse-only`. `tools/validate-ci-reuse-policy.py` enforces the classification against the workflows in both
directions and **fails on any step nobody has classified**, so a step added later cannot inherit a skip by
having its guard copied from the step above it. That validator runs on every run, hit or not: a gate may
never reuse evidence without first proving the rules it is relying on are intact.

**What counts as equivalent evidence is five rules, not one.** A hit requires the SAME GATE (only successful
runs of the same workflow — a green Phase-4 run can never satisfy governance); an IDENTICAL TREE (and
`.github/workflows/**`, `tools/**`, `scripts/**` and `governance/**` are all tracked, so identical content is
also the same gate definition, the same validators and the same policy file — a changed check is a changed
tree, and a changed tree never matches); ANCESTRY, meaning the matched commit must be an ancestor of the one
being validated, which is precisely the delivery relationship and is what rules out cross-context evidence
from an unrelated branch or fork that happens to render the same tree; an IDENTICAL MEASURED EXECUTION
ENVIRONMENT (below); and RECENCY.

**The tree fixes the code, not the machine.** These gates ask for `ubuntu-latest`, Go `1.25`, Node `20`,
Python `3.12` and version-tagged actions. Every one of those is resolved at run time and every one can move
inside any recency window, so an identical tree does not establish that the toolchain which would run the
tests is the toolchain that did. `scripts/ci/env-fingerprint.sh` therefore MEASURES the resolved environment
— hosted image name and version, kernel release, resolved `go`/`node`/`npm`/`python` versions, and the
digests of the container images that gate actually pulls — and publishes a SHA-256 of it. The lookup reads
the candidate run's own fingerprint back out of **that run's job log** through the API and refuses to reuse
across any difference. The comparison is therefore against GitHub's record of what the run really executed
on, not a promise in a config file. Action versions are deliberately outside the fingerprint, and the reason
is checkable rather than asserted: **no reuse-eligible step is a GitHub action** — every `uses:` step is
classified `always`, and `tools/validate-ci-reuse-policy.py` fails the build if that ever stops being true —
so every action re-executes on every run and no action's behaviour is ever inherited. What a setup action
*leaves behind* is inherited, and that is exactly the resolved toolchain version the fingerprint captures.
The validator also enforces the ordering the measurement depends on: the fingerprint step must exist exactly
once, run unconditionally, precede the lookup, be handed to it, and have no toolchain set up after it.

**RECENCY IS A BOUND, NOT A PROOF, and is described that way on purpose.** The four rules above establish
equality of everything these gates can name and measure. The 24-hour limit exists to bound exposure to what
they cannot — an unnamed mutable input, a registry serving different bytes, a transitive dependency. It also
stops a verdict being RESURRECTED: a revert-of-a-revert restores old content and IS a descendant, so ancestry
alone would let a months-old judgement stand for content being accepted today. It is a safety margin on the
unknown; nothing in this mechanism treats it as evidence that two things are the same.

Every path that cannot establish all five — no token, an API error, no candidate, an object the checkout
lacks, an unreadable timestamp, an environment this run could not measure, a candidate whose log carries no
fingerprint — reports no hit and the full validation runs. Reuse can remove duplicated work and can never be
why something went unchecked. Both mechanisms are adversarially proven rather than asserted:
`tools/tests/ci_reuse_policy/run_negative.py` drives the policy validator against fixtures carrying each
specific defect (12 cases), and `scripts/ci/tests/evidence-reuse-selftest.sh` drives the real lookup against a
git repository with a known topology, making exactly one eligibility rule false at a time (12 cases).

No Agent may bypass the governed execution wrapper or validators.

## 9. No stale leftovers

A completed Phase must not leave stale or contradictory plans, status text, code paths, feature flags, schema references, environment keys, role assumptions, runbooks, deployment files, tests, comments, exports, manifests or acceptance records (see the [Zero-Stale-Leftovers rule](ZERO_STALE_LEFTOVERS_RULE.md)). Retained legacy behavior must be registered in `governance/artifact-registry.json` with a lifecycle classification, exact reason, runtime status, replacement, removal gate and owning future Phase. Do not delete a required rollback path early — classify and gate it instead.

## 10. Phase delivery speed

**HOW a delivery is executed quickly and safely is specified in the Fast Delivery and Parallel-Agent Protocol, which is authoritative for delivery execution, CI hygiene, dependency-aware deployment order, guest data in delivery evidence, and how more than one agent may work at once.** That rule is built on the measured delivery of PRs #108/#109 (3 h 12 m wall clock, 11 461 s of gate execution, 16 of 34 attempts failed, 0 s of GitHub queue time), and it is machine-enforced by `tools/validate-delivery-protocol.py`. Run `bash tools/preflight.sh` before pushing: it refuses locally what the gates would refuse remotely.

The objective is fast, complete delivery without sacrificing production safety. For each approved Phase: use the approved Phase plan as the complete work breakdown; execute all included workstreams in one controlled run; fix test failures within the same branch; synchronize all directly-affected documents once at the end and whenever a milestone materially changes the authoritative state; generate one consolidated final report; produce one Phase PR. Do not send the Product Owner through repeated documentation-only loops when structural governance can detect and resolve the issue automatically.

## 11. Enforcement

This rule is enforced by:

- `tools/project-state.py validate` — asserts `authoritative_remote` points to `aibrahiiim1/StayConnectEnterprise`, that `delivery_governance` names this rule, the manifest generator and the report template (all present on disk), and that the `GH-*` decisions are registered.
- `tools/validate-project-state.sh` — keyword safety layer (bundled with the export packs).
- `tools/generate-change-manifest.py` — the deterministic changed-file manifest every final report must embed (columns: Path · Classification · Git status · Domain · Workstream · Rollback · Purpose).
- `.github/workflows/project-governance.yml` — the mandatory **Project Governance** CI check (GH-MANDATORY-CI), enforced structurally by `tools/project-state.py validate` and adversarially by `run_mutations.py` (missing workflow, removed command, missing PR trigger, ignored failures).
- `tools/validate-delivery-protocol.py` — the **Fast Delivery and Parallel-Agent protocol**, asserted from the tree: every required gate reports on `pull_request`, declares **no `workflow_dispatch`** (a dispatched run reports the same required context, so it can block a merge and can be inherited as evidence), and carries a `concurrency:` block that cancels superseded pull-request runs while never cancelling a master run; and the preflight, its protocol document and the PR template all exist and still implement what they claim. Adversarially proven by `run_mutations.py` cases M61-M65.
- `tools/check-fixture-parity.py` — the disposable-Postgres fixture carries every column its queries actually select and every table joined into a fixture-backed statement. This was the largest single failure category in the PRs #108/#109 delivery (8 of 16 failed attempts).
- `tools/preflight.sh` — the local aggregate, ordered cheapest-and-most-likely-to-fail first.
- `.gitattributes` — cross-platform **LF consistency** (`GH-LF-CONSISTENCY`): all checksum-controlled text is pinned to `eol=lf` and ZIP/binary artifacts are marked binary, so a Windows checkout, a Linux checkout and CI produce byte-identical, checksum-stable pack files. Without it, `core.autocrlf` materializes tracked text as CRLF on Windows and the keyword validator reports false pack-checksum failures. Enforced by `tools/project-state.py validate` and adversarially by `run_mutations.py` (weakened/removed policy).
- The decision register entries `GH-SOURCE-OF-TRUTH`, `GH-BRANCH-PR`, `GH-COMPLETE-MANIFEST`, `GH-FINAL-REPORT`, `GH-MANDATORY-CI` (`governance/decision-register.json`).

Run `make governance-validate` before any CONTROLLED implement/migrate/deploy/export, and `python tools/generate-change-manifest.py <base>..HEAD` before writing the final report **of a CONTROLLED delivery or milestone closure** — not after routine FAST-mode work, which produces neither a manifest nor a governance run (`CLAUDE.md` §0). A delivered PR must additionally show the GitHub **Project Governance** check green before it is called merge-ready.

## 12. Agent-owned Git and GitHub operations (`GIT_OPERATIONS_OWNER: AGENT`)

All Git and GitHub operations are performed by the authorized AI Agent through the authenticated tools. **The Product Owner must not be asked to execute Git, GitHub CLI or repository-management commands as the normal workflow.** Agent-owned operations include: fetch and pull; branch creation and switching; commits; pushes; remote verification; PR creation and updates; CI monitoring; merge execution; post-merge synchronization; branch cleanup; release/tag operations when separately authorized; and changed-file-manifest generation.

The Agent may request Product-Owner intervention **only** for unavoidable account-level or repository-UI settings that cannot be performed through the authenticated tools — for example an unsupported branch-protection setting, billing, organization policy, or an authentication approval. The Agent must not hand the Product Owner Git or `gh` commands to run as the routine path. This is decision `GH-AGENT-ONLY-OPERATIONS`, enforced structurally by `tools/project-state.py validate` (which requires the `GIT_OPERATIONS_OWNER: AGENT` assertion and the decision) and adversarially by the mutation suite.
