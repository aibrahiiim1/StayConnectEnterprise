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

Latest merged deliveries: #256–#261 (fresh-VM installer, enrollment F1, installer acceptance, Central `:9443`
verified from outside, modules follow the licence, card payment and room charge configurable but never executable;
records T0259–T0262) and #262 (repository cleanup, T0263). This branch (`claude/vigilant-keller-sag14r`, T0264, D78)
records the Product Owner's environment record, removes retired-server dependencies from configuration, scripts and
tests, adds the single retired-target safeguard, and removes superseded documents.

## Environments (Product-Owner record, 2026-10-28, D78)

| Role | Host | State |
|---|---|---|
| **Production Central** | `central.onegate.getsemantics.com`, public `41.128.240.133`, private `172.21.96.96` | release `onegate-central-05481cb57334`; its own trust universe. Production: never a default for tests or drills |
| **Test appliance** | `172.21.96.97` | release `onegate-appliance-d25999781b1a` (master code of #261), activated on the production Central; Go-Live not authorised |

Every other host is retired and is never contacted: see `current_state_facts.retired_hosts`, enforced by
`tools/validate-standing-records.py`.

## Open, and known

* **Deployment target.** The standing per-change PRE-LIVE deployment decision named the retired appliance, so it has
  no target. Deploying to the test appliance needs a mission that names it, or a new standing decision.
* Card payment and room charge execute only with their execution switches (provider calls, LIVE, PMS transmit),
  each a separate Product-Owner authorisation (manual C.12).
* A blank-VM install of the current release was not repeated after #256 (it needs the test VM reset).
* Rebuilding an appliance with the same identity (manual E.4 Case 2) is self-tested but not run on a VM.
* The backend audit (`docs/audit/`) and the performance audit (`docs/performance-audit/`) are open backlogs of findings.
* High availability and white label are licence keys with no implementation.
* Product-Owner decisions still open (system handbook §24.2): the off-host copy destination for Central dumps; custody
  and shredding of the former trust universe's Root-CA backup (on the stopped first Central); whether Ubuntu 24.04 is
  supported; Go-Live, LIVE card payments and further PMS postings. High availability is a product decision (§16.5).
* A local backup ref `refs/original/refs/heads/feat/operator-journeys-consolidation` (tip `c6905b3f`) holds the
  pre-redaction form of the T0164 commits, whose redacted form is on master. It was never on GitHub and was left in
  place; dropping it is the Product Owner's call.

## Build environment

Docker Desktop on the workstation; releases build in pinned containers (`golang:1.25.12`, `node:20.18.0`) from a fresh
clone of one commit, so the working tree must be clean for `central-build.sh`. A build needs about 500 MB free on D:.
Run the Go suites in the `golang:1.25.12` container (`MSYS_NO_PATHCONV=1` for `docker run -v` from Git Bash; do NOT set
it for `clean-install-reconstruction.sh` or `generate-production-baseline.sh`, which manage path conversion themselves).

`iam_v2_scratch/run.sh` refuses `stayconnect_site`, `stayconnect` and `stayconnect_site_b` as scratch database names — a
live-database guard. The `timescale/timescaledb` image initialises, shuts down and restarts, so wait on `pg_isready`
rather than on a first successful query.
