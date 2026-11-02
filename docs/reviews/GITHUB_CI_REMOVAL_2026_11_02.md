# GitHub CI removal — 2026-11-02 PO decision

**Authorization:** The Product Owner explicitly asked to remove GitHub CI entirely, while retaining relevant local testing, code review and protected PR-only merging, then requested immediate implementation.

## In scope

- Remove five GitHub Actions YAML workflows (four comprehensive gates and the required PO-merge check).
- The sole active ruleset `master-protected-delivery` must retain PR-only, merge commits, resolved review threads, zero bypass actors, no direct/force push or branch deletion, and remove only its required-status-checks rule.
- Explicit PO acceptance before merge still applies procedurally and to the exact approved PR head. With no CI, GitHub no longer checks it automatically: a residual governance risk, never misrepresented.
- Preserve test code and require local, authentic regression, integration, financial/security and browser evidence. `tools/full-check.py` now invokes the local preflight plus explicit real integration-suite commands and refuses to mark missing suites PASS.

## Out of scope

No appliance or Central deployment, no PMS financial operation, active PKI, destructive data change, networking change or Go-Live.

## Live GitHub settings boundary

The connected GitHub tool supports PR and repository file edits but has **no ruleset mutation or Actions settings function**. The repository administrator must edit the live [master-protected-delivery ruleset](https://github.com/aibrahiiim1/StayConnectEnterprise/rules/22779180): remove **only** the `required_status_checks` rule, leaving all other rules unchanged. For immediate repository-level disabling of all GitHub Actions independently of code state, use Settings → Actions → General → Actions permissions → Disable actions.

**Do not declare the no-CI mission completed until:** the live ruleset has no required status checks, five workflow YAML files are absent on merged master, local validation semantics are consistent, and PR-only/no-bypass/force-push/deletion protection is rechecked.

## Evidence / present limits

Before changing files, the live ruleset required `po-merge-authorization` from GitHub Actions app 15368, with PR-only protection and no bypass. Repository updates are staged on PR #291; they do not by themselves change the live ruleset. No local test suite or GitHub workflow has been run as part of this documentation change.
