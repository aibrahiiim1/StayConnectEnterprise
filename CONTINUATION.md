# Continuation checkpoint

> **This file is a CHECKPOINT, not a state store.** The authoritative current state is
> `governance/project-state.json`, and it is the only place current operational state is defined. This file
> exists to hand a session to the next one: what was just done, what is known to be open, and the
> environment facts that cost time to rediscover. If the two ever disagree, the register wins.
>
> It went stale once, badly: it announced PR #120 as the master head for nine days and 52 merges, and
> carried an "OPEN DEFECT" that four later PRs had closed. Nothing read it, because it was in no validator's
> surface list. It now is — `tools/validate-standing-records.py` checks any stated master head here against
> `current_state_facts.master_head_at_delivery_base` and live master. It names no master head; read
> `latest_transition_id` and `current_delivery` in the register.

## Where the work is (2026-10-28)

Merged: #256–#263 (installer, enrollment, modules, cleanups; T0259–T0265). Then the **audit remediation** (D80/T0266,
PR from `claude/vigilant-keller-sag14r`): every backend, performance and hardening finding re-checked and the
actionable ones fixed with tests; statuses in [`docs/AUDIT_BACKLOG.md`](docs/AUDIT_BACKLOG.md), open decisions in
[`docs/AUDIT_DECISIONS.md`](docs/AUDIT_DECISIONS.md) (AD-1…AD-68). **Not deployed** anywhere: read the upgrade notes
(installation manual E.5.1; DEPLOYMENT_CLOUD §7.1) before upgrading the test appliance.

## Environments (Product-Owner record, 2026-10-28)

| Role | Host | State |
|---|---|---|
| **Production Central** | `central.onegate.getsemantics.com`, public `41.128.240.133`, private `172.21.96.96` | release `onegate-central-05481cb57334`; its own trust universe. Never a default for tests or drills |
| **Test appliance** | `172.21.96.97` | release `onegate-appliance-d25999781b1a` (master code of #261); activated on the production Central; standing deployment authorization for routine software (D79); Go-Live not authorised |

Every other host is retired and never contacted (`current_state_facts.retired_hosts`, enforced by
`tools/validate-standing-records.py`). The cloud delivery environment cannot reach either current host (its egress
proxy refuses them), so deployments run from the workstation.

## Open, and known

* **Legacy Root CA custody** ([CA_CEREMONY_RUNBOOK.md](docs/CA_CEREMONY_RUNBOOK.md)): the workstation copy is no longer
  held and the former servers have been permanently removed (PO confirmations, 2026-10-28). Key erasure was never
  observed or verified, and none is claimed. No current system trusts those keys.
* Whether the production Central's own Root key was moved to cold storage (manual F, step 2) is not recorded.
* Card payment and room charge execute only with their execution switches, each a separate PO authorisation (manual C.12).
* A blank-VM install of the current release was not repeated after #256; rebuild with the same identity (manual E.4
  Case 2) is self-tested but not run on a VM.
* Audit backlog after T0266: backend 143 fixed, 48 partial, 40 waiting on a decision, 19 open; performance 27 fixed,
  12 partial, 24 waiting on a decision, 18 open. High findings still open: P1 H2 (AD-1) and the Go 1.26 toolchain (AD-60).
* The audit-remediation release is not on the test appliance yet (no route from the cloud delivery environment).
* High availability and white label are licence keys with no implementation.
* PO decisions still open (handbook §24.2): the off-host copy destination for Central dumps; whether Ubuntu 24.04 is
  supported; Go-Live, LIVE card payments and further PMS postings; high availability (§16.5).
* A local backup ref `refs/original/refs/heads/feat/operator-journeys-consolidation` (tip `c6905b3f`) on the
  workstation holds the pre-redaction form of the T0164 commits; never on GitHub; dropping it is the PO's call.

## Build environment

Docker Desktop on the workstation; releases build in pinned containers (`golang:1.25.12`, `node:20.18.0`) from a fresh
clone of one commit, so the working tree must be clean for `central-build.sh`. A build needs about 500 MB free on D:.
Run the Go suites in the `golang:1.25.12` container (`MSYS_NO_PATHCONV=1` for `docker run -v` from Git Bash; do NOT set
it for `clean-install-reconstruction.sh` or `generate-production-baseline.sh`, which manage path conversion themselves).

`iam_v2_scratch/run.sh` refuses `stayconnect_site`, `stayconnect` and `stayconnect_site_b` as scratch database names — a
live-database guard. The `timescale/timescaledb` image initialises, shuts down and restarts, so wait on `pg_isready`
rather than on a first successful query.
