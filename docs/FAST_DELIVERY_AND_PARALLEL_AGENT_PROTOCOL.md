# PERMANENT RULE — Fast Delivery and Parallel-Agent Protocol (project-wide, mandatory)

This is the single authoritative protocol for **how a delivery is executed quickly and safely**, and for **how
more than one agent may work at once without corrupting a delivery**. It sits beside the
[GitHub Execution and Delivery rule](GITHUB_EXECUTION_AND_DELIVERY_RULE.md), which governs *what* a delivery
must contain; this one governs *how it is run*. Where a habit conflicts with this file, this file wins.

Nothing here relaxes a gate, a protection setting, an authorization boundary or an evidence requirement. Every
rule below either removes waiting that proved nothing, or prevents a failure being discovered later than it
could have been.

---

## 0. The measurement this rule is built on

Not an opinion. The delivery of PRs #108 and #109 was measured from the GitHub API:

| | |
|---|---|
| Wall clock, first run created to last run finished | **3 h 12 m** |
| Gate execution billed across 34 attempts | **11 461 s (3 h 11 m)** |
| CI genuinely running (parallelism collapsed) | 6 049 s — **52.5 %** |
| Idle: authoring, fixing, waiting for a human or an agent | 5 472 s — **47.5 %** |
| GitHub queue time | **0 s on every run** |
| Failed attempts | **16 of 34** |

Failure categories, which is where the rules come from:

| Category | Count | Could a workstation have caught it? |
|---|---|---|
| PG16-FIXTURE — disposable-Postgres fixture behind the real migrations | **8** | Yes, in milliseconds |
| MANIFEST-OR-PACK — manifest not regenerated after the last commit | 3 | Yes, in seconds |
| E2E-PRODUCT — genuine assertions against redesigned markup | 2 | Yes, by running the suite |
| PR-METADATA — PR body missing its governance metadata | 2 | Yes, once the PR exists |
| LINUX-ONLY — Windows-pruned lockfile | 1 | Yes, with one container |
| E2E-INFRASTRUCTURE | **0** | — |

**Queue time was never the problem. Rework was.** Two structural faults account for most of the avoidable
compute: there was no `concurrency:` key in any gate workflow, so superseded runs were never cancelled
(869 s of provably obsolete compute), and every gate accepted `workflow_dispatch`, so a dispatched run
reported the same required context as the real one (1 274 s duplicated, and a merge blocked for ~20 minutes
while it finished).

**A footnote to that second fault, added 2026-09-24.** It was later measured that a dispatched run's checks
never satisfy a *ruleset*-required status check in the first place — so the 1 274 s bought a run that could
block a merge and could never permit one. The original rule against dispatching a required check was more
right than the reasoning given for it.

---

## 1. The order of work

1. **Change code, tests, fixtures and accessibility assertions together.** A change that updates markup and
   leaves the specs pinned to the old markup produces E2E failures that look like regressions. Two of the
   sixteen failures were exactly this.
2. **Run focused tests while implementing** — the file, the package, the one spec.
3. **Run `bash tools/preflight.sh`** before pushing. It refuses locally what the gates would refuse remotely.
   Stage 9 is the slow one and is the reason the list has nine rows rather than eight: the governance gate's
   mutation suite will not mutate anything until BOTH validators pass on the good state, and the keyword
   half ran nowhere local until T0177 paid a full governance cycle to learn that one regex alternative had
   lagged the truth.
4. **One stable full E2E pass**, once, when everything else is green.
5. **Only then** generate the manifest, rebuild the packs and make the delivery-only commit. Generating them
   earlier guarantees regenerating them again, and a stale manifest is its own gate failure.
6. **Validate the live PR body** (`tools/preflight.sh --stage 2`) *before* waiting on the expensive cycle.
7. Push, and let the gates run.

---

## 2. Local preflight is not optional

`tools/preflight.sh` runs the cheapest and most-likely-to-fail checks first:

| Stage | What it refuses | Category it closes |
|---|---|---|
| 1 | governance, generated blocks, delivery protocol, dirty working tree | MANIFEST-OR-PACK |
| 2 | a PR body missing its phase status, decision or receipt | PR-METADATA |
| 3 | a lockfile that does not install on Linux (`npm ci` in `node:20`) | LINUX-ONLY |
| 4 | a fixture missing a column or table its queries actually select | PG16-FIXTURE |
| 5 | Go build/vet/test **as CI runs them** — `-count=1`, and vet under build tags | — |
| 6 | typecheck, unit tests, production build | — |
| 7 | an E2E harness that cannot tell a dead server from a broken product | E2E-INFRASTRUCTURE |
| 8 | the full browser suite (`--full`) | E2E-PRODUCT |
| 9 | the Zero-Stale keyword validator — the governance gate's mutation suite refuses to run at all unless it passes on the good state | ZERO-STALE |

Two details that look like pedantry and are not:

- **`-count=1`.** Without it a locally green `go test` may be served entirely from the test cache. CI always
  passes it. A local pass that used the cache proves nothing about the runner.
- **`go vet` under build tags.** 61 files carry `//go:build integration`. A plain `go build ./...`,
  `go vet ./...` or `go test ./...` compiles **none** of them, so a tagged file that does not compile is
  invisible locally and fails inside a gate.

A stage that cannot run (no Docker, no token) reports **SKIP**, never PASS. An unverifiable check is not a
passing check.

---

## 3. CI hygiene

**Superseded in part by D42 (2026-09-26, T0196) — see [`PO_LED_DELIVERY_MODEL.md`](PO_LED_DELIVERY_MODEL.md).**
The four comprehensive gates no longer run on `pull_request` or on push, and there is no nightly run. They are
`workflow_dispatch` only and run solely for "FULL CHECK THE WHOLE CODE" (`tools/full-check.py`). The one
required status context is `po-merge-authorization`. The historical rules this section used to state — the
attempt-1 sentinel, the nightly re-run, `tools/nightly-status.py` — are retired; the measurement behind them
still stands: **a `workflow_dispatch` run's checks never satisfy a ruleset-required status check** (PR #181,
`HTTP 405 … 4 of 4 required status checks are expected`). That is why the merge requirement is a
`pull_request` check and the comprehensive gates are not required checks at all.

- **Concurrency.** Each gate declares one FULL CHECK per workflow and ref, never cancelled mid-flight:

  ```yaml
  concurrency:
    group: ${{ github.workflow }}-${{ github.ref }}
    cancel-in-progress: false
  ```
- **Prepare read-only delivery material while the gates run** — the report, the summary, the review notes.
  **Never merge or deploy before the required exact head is ALL_GREEN.**
- **Record, for every rerun:** queue time, execution time, failure category, and the reason.

---

## 4. Deployment order is dependency-aware

**Deploy a backward-compatible API before the UI that depends on it, or use a verified atomic staged switch.
Never expose a temporary 404 or an incompatible surface.**

This is written from a real incident. During the PC-0006 deployment the Hotel Admin bundle was installed
first, on its own. The rebuilt screens read a new endpoint and richer projections that live in `edged`, which
had not been deployed — so the dashboard answered **404** and the sessions list showed bare MAC addresses:
precisely the defects the delivery existed to remove. The window was short and PRE-LIVE, but on a live
appliance it would have been a visible outage caused entirely by deployment order.

A delivery whose halves depend on each other names that dependency **before** it starts, and deploys in the
order the dependency implies.

---

## 5. Guest data never enters delivery evidence

Delivery evidence is published: PR bodies, commit messages, governance records and export packs all leave the
machine. An appliance under acceptance may carry a **real PMS guest list**.

- **Describe the observation; never reproduce the record.** "Every row leads with the room number and the
  registered guest name" carries the same evidential weight as quoting one, and identifies nobody.
- Never place a real guest name, room number or reservation identifier in a PR body, a commit message, a
  governance record or an export pack.
- Commit messages are immutable. A name committed is a name that cannot be withdrawn — only the working tree
  can be redacted. **Check before committing, not after.**

---

## 6. The parallel-agent contract

Parallelism is for reducing elapsed time, never for sharing a working tree.

1. **Exactly one Delivery Owner per delivery branch.** That agent alone owns the branch, the commits, the
   pushes, the PR, the merge and the final report.
2. **No two agents may write, commit, push or run `git add -A` against the same branch or shared working tree
   concurrently.** `git add -A` is singled out because it stages whatever another agent happens to have
   written mid-edit, producing a commit nobody authored.
3. **Read-only investigation and review may run fully in parallel**, and should — a repository-wide search, a
   CI measurement and a gap analysis are independent and cost nothing to run at once.
4. **Code-producing subagents work in isolated worktrees or branches**, on non-overlapping scopes, and return
   their results to the Delivery Owner for controlled integration. They do not push.
5. **Parallel tests use isolated databases, artifacts and ports.** Never run competing full E2E servers
   against the same environment — the harness reuses whatever is already on the port.
6. **No concurrent deployment and no concurrent live mutation**, ever, regardless of environment.
7. **Independent CI jobs run in parallel where it is safe**; jobs that share a database, a port or an
   artifact name do not.
8. On detecting a concurrent writer to an active delivery branch, **establish a single owner and have the
   other stand down** before continuing.

---

## 7. What is machine-enforced

Prose is what allowed a missing `concurrency:` key to persist unnoticed across four workflows, so every
statement above that *can* be checked from the tree *is*:

- **`tools/validate-delivery-protocol.py`** — under D42: every comprehensive gate is `workflow_dispatch`
  only (no `pull_request`, `push`, `schedule` or `workflow_run`), carries no sentinel, executes fresh, and has
  a never-cancelled concurrency block keyed on workflow and ref; no workflow anywhere declares a schedule or
  performs a merge, and the nightly orchestrator is gone; `po-merge-authorization.yml` runs on the pull-request
  events that grant and revoke approval and actually runs `tools/po_merge_authorization.py`; the decision's
  negative tests and `tools/full-check.py` exist; this document, the preflight and the PR template exist; this
  document is registered; and the preflight still implements each late-failure check.
- **`tools/check-fixture-parity.py`** — the disposable-Postgres fixture carries every column its queries
  actually select, and every table joined into a fixture-backed statement.
- **`tools/preflight.sh`** — the aggregate a developer runs.
- **`.github/PULL_REQUEST_TEMPLATE.md`** — the metadata the governance gate validates from the live body.
- The adversarial mutation suite proves each of these assertions actually fires, so none can be silently
  removed.

---

## 8. What this rule does not relax

`master-protected-delivery` keeps `enforcement: active`, an **empty bypass-actor list**, merge-commit-only
delivery, no force push and no deletion. Under D42 its one required context is `po-merge-authorization`,
pinned to the GitHub Actions app; the four comprehensive gates are FULL CHECK only. Authorization boundaries (§12 of the delivery rule) are untouched. A skipped or
non-failing gate is not evidence, and speed is never a reason to accept one.
