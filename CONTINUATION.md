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

The latest deliveries, all merged to master under the Product Owner's approval:

| PR | What | Record |
|---|---|---|
| #256 | the fresh-VM installer: one release, one command (install, re-run, resume, `--upgrade`) | manual Appendix 3 |
| #257 | enrollment F1: a reinstall before activation can no longer bind an obsolete certificate | D75 / T0259 |
| #258 | installer acceptance: Central survives a reboot (Redis auth file out of `/tmp`), a refused first administrator stops the install, the manual executed end to end | T0260 |
| #259 | Central's `:9443` verified from outside as the mutual-TLS listener | manual A3.8 |
| #260 | modules follow the licence: the Hotel module installed; room sign-in only where it is in use | D76 / T0261 |
| #261 | card payment and room charge configurable, never executable; high availability and white label never shown available | D77 / T0262 |

On 2026-10-28 every merged branch was deleted, on GitHub (28) and locally (217), with 100 stale local copies of old
pull-request heads (T0263). Besides `master`, the one branch left is `docs/cleanup`, the branch of the still-open
PR #262 that records this cleanup; it is deleted when that PR is merged. The 45 historical
documents a first pass deleted (39 change manifests, six superseded reports) were restored in the same PR: each is
a delivery record or is cited by a record, so they stay, marked historical where they were already marked.

## Environments

| Role | Host | State |
|---|---|---|
| PRE-LIVE appliance | `172.21.60.25` | #256–#261 were **not deployed** to it; Go-Live not authorised |
| **Production Central** (Product-Owner record, 2026-10-28) | `central.onegate.getsemantics.com` — public `41.128.240.133`, private `172.21.96.96` | release `onegate-central-05481cb57334`; its own trust universe |
| Former Central | `172.21.96.196` (`sc-central.echofusion.com`) | **retired 2026-10-28**; not an operational target |
| Test appliance | `172.21.96.97` | release `onegate-appliance-d25999781b1a` (master code of #261), activated on the current Central |

The current Central and the test appliance share one trust universe; PRE-LIVE belongs to the former Central's.

## Open, and known

* The current Central carries the R1 fix (Redis auth file out of `/tmp`; installation manual A3.7).
* PRE-LIVE was provisioned against the retired Central and does not run F1, the installer fixes or the module changes.
  Serving it from the current Central needs it re-provisioned in that trust universe: a Product-Owner decision.
* Card payment and room charge execute only with their execution switches (provider calls, LIVE, PMS transmit),
  each a separate Product-Owner authorisation (manual C.12).
* A blank-VM install of the current release was not repeated after #256 (it needs the test VM reset).
* Rebuilding an appliance with the same identity (manual E.4 Case 2) is self-tested but not run on a VM.
* The backend audit (`docs/audit/`) and the performance audit (`docs/performance-audit/`) are open backlogs of findings.
* High availability and white label are licence keys with no implementation.
* Product-Owner decisions still open (system handbook §24.2): the off-host copy destination for Central dumps, and
  shredding the Root-CA backup once it is confirmed on offline media; whether Ubuntu 24.04 is supported; Go-Live, LIVE
  card payments and further PMS postings; retiring the obsolete documents listed in handbook §23. High availability is
  also a product decision (§16.5).
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
