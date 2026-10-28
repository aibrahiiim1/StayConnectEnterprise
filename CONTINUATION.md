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

On 2026-10-28 every merged branch was deleted (GitHub and local: only `master` remains), and finished working documents
were removed: 44 per-delivery change manifests and five superseded status reports (all in git history).

## Environments

| Role | Host | State |
|---|---|---|
| Production Central | `172.21.96.196` (`sc-central.echofusion.com`) | **not updated** by #256–#261 |
| PRE-LIVE appliance | `172.21.60.25` | **not updated** by #256–#261; Go-Live not authorised |
| Test Central (lab) | `central.onegate.getsemantics.com` — public `41.128.240.133`, private `172.21.96.96` | release `onegate-central-05481cb57334` |
| Test appliance (lab) | `172.21.96.97` | release `onegate-appliance-d25999781b1a` (master code of #261) |

The test pair has its own trust universe; never point it at PRE-LIVE or the production Central, or the reverse.

## Open, and known

* **The production Central will not come back from a reboot** until it applies the current `central-infra.yml`
  (finding R1; the procedure is the installation manual F.6). A Product-Owner decision.
* PRE-LIVE and the production Central do not yet run F1, the installer fixes or the module changes.
* Card payment and room charge execute only with their execution switches (provider calls, LIVE, PMS transmit),
  each a separate Product-Owner authorisation (manual C.12).
* A blank-VM install of the current release was not repeated after #256 (it needs the test VM reset).
* Rebuilding an appliance with the same identity (manual E.4 Case 2) is self-tested but not run on a VM.
* The backend audit (`docs/audit/`) and the performance audit (`docs/performance-audit/`) are open backlogs of findings.
* High availability and white label are licence keys with no implementation.
* Product-Owner decisions still open: the off-host backup destination, Root CA custody, HA, Ubuntu 24.04 support.

## Build environment

Docker Desktop on the workstation; releases build in pinned containers (`golang:1.25.12`, `node:20.18.0`) from a fresh
clone of one commit, so the working tree must be clean for `central-build.sh`. A build needs about 500 MB free on D:.
Run the Go suites in the `golang:1.25.12` container (`MSYS_NO_PATHCONV=1` for `docker run -v` from Git Bash; do NOT set
it for `clean-install-reconstruction.sh` or `generate-production-baseline.sh`, which manage path conversion themselves).

`iam_v2_scratch/run.sh` refuses `stayconnect_site`, `stayconnect` and `stayconnect_site_b` as scratch database names — a
live-database guard. The `timescale/timescaledb` image initialises, shuts down and restarts, so wait on `pg_isready`
rather than on a first successful query.
