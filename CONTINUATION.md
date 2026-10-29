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

## Where the work is (2026-10-29)

Merged: #256–#264 (installer, enrollment, modules, cleanups, the first audit remediation; T0259–T0266). Then, by the
Product Owner's decisions of 2026-10-29 (D81): **PR #265** (audit decisions AD-1 to AD-68, T0267) and **PR #266**
(the upgrade-path fix found by deploying it, T0268). The disposition of every decision — implemented, accepted risk,
documentation only, or waiting for evidence — is in [`docs/AUDIT_DECISION_DISPOSITIONS.md`](docs/AUDIT_DECISION_DISPOSITIONS.md).
Both environments run the release (installation manual A3.10).

## Environments (Product-Owner record, 2026-10-28)

| Role | Host | State |
|---|---|---|
| **Production Central** | `central.onegate.getsemantics.com`, public `41.128.240.133`, private `172.21.96.96` | release `onegate-central-9613fb52679d` (2026-10-29, the closure round: ctrlapi runs as the least-privilege role `ctrlapi_rt`, migrations through 0063; the earlier audit-decision release `ee2faf881d89` had Central code equals master `1cd37145`); its own trust universe. Never a default for tests or drills |
| **Test appliance** | `172.21.96.97` | release `onegate-appliance-9613fb52679d` (2026-10-29, the closure round, migrations through 0179, appliance Root CA pinned against its fingerprint; the earlier audit-decision release was `1cd37145c468`); activated on the production Central; standing deployment authorization for routine software (D79); Go-Live not authorised |

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
* Audit decisions (D81, closed out by D82): **accepted risks**, not fixed: AD-25 (IPv6 forwarding kept), AD-29 (voucher format
  kept; guessable short digit vouchers behind a MAC-rotating device), AD-37 step 4 (hotel-wide feed rule), AD-8 (three parts);
  AD-65 (archive of a deleted appliance's history, not RESTRICT plus soft delete) was **accepted by the Product Owner**. AD-59
  was measured and decided (keep the Node hop). The per-item answer, with the live evidence, is
  [`docs/AUDIT_DECISION_DISPOSITIONS.md`](docs/AUDIT_DECISION_DISPOSITIONS.md). **Evidence that does not exist yet:** 2,000 real devices
  moving counters on a 2 vCPU / 4 GB machine (AD-50 S1; the database half of S10 was measured); a first activation *by package*
  that succeeds (needs a second waiting appliance); the Post-stay flow against a real PMS feed. **Central's database** is reached by ctrlapi as the least-privilege role `ctrlapi_rt` (migration 0063,
  [`docs/runbooks/CENTRAL_DB_LEAST_PRIVILEGE.md`](docs/runbooks/CENTRAL_DB_LEAST_PRIVILEGE.md)); a migration that adds an
  append-only Central table must list it in `central_apply_runtime_grants()`.
* Central silence only raises the `CloudStale` warning once the licence's `offline_grace_days` (default 30) pass; 48 hours is
  the clock-rollback tolerance. The Product Owner's wording was "more than 48 hours"; the contract in
  [`docs/LICENSING_AND_ENTITLEMENTS.md`](docs/LICENSING_AND_ENTITLEMENTS.md) and the code say 30 days, and no 48-hour Central warning exists.
  Nothing was changed; say if a 48-hour warning is wanted (a new licensing semantic).
* `appliance-install.sh` (fresh and `--upgrade`) pins the appliance Root CA (AD-2) only against a fingerprint: `--root-ca-sha256 <fp>` from
  Central's `FINGERPRINTS.txt`, or the trust directory's `FINGERPRINTS.txt`. It never adopts a certificate nothing vouches for and never
  replaces a pin.
* Migrations marked `-- edge-migrate: platform` change the platform schema and run as the platform role
  (`edge-migrate.sh --platform-migration`); every other migration runs as `iam_v2_owner`. A new migration that touches
  `public` needs the marker. Test it with `FULLSCHEMA_BASELINE_FILE`, `FULLSCHEMA_BASELINE_TOP` and `FULLSCHEMA_APPLY_ROLE=iam_v2_owner`.
* The former PRE-LIVE appliance `172.21.60.25` and the former Centrals are retired (Product-Owner records of 2026-10-28) and were not contacted.
* High availability and white label are licence keys with no implementation.
* PO decisions still open (handbook §24.2): the off-host copy destination for Central dumps; whether Ubuntu 24.04 is
  supported; Go-Live, LIVE card payments and further PMS postings; high availability (§16.5).
* A local backup ref `refs/original/refs/heads/feat/operator-journeys-consolidation` (tip `c6905b3f`) on the
  workstation holds the pre-redaction form of the T0164 commits; never on GitHub; dropping it is the PO's call.

## Build environment

Docker Desktop on the workstation; releases build in pinned containers (`golang:1.26.10`, `node:20.18.0`) from a fresh
clone of one commit, so the working tree must be clean for `central-build.sh`. A build needs about 500 MB free on D:.
Run the Go suites in the `golang:1.26.10` container (`MSYS_NO_PATHCONV=1` for `docker run -v` from Git Bash; do NOT set
it for `clean-install-reconstruction.sh` or `generate-production-baseline.sh`, which manage path conversion themselves).

`iam_v2_scratch/run.sh` refuses `stayconnect_site`, `stayconnect` and `stayconnect_site_b` as scratch database names — a
live-database guard. The `timescale/timescaledb` image initialises, shuts down and restarts, so wait on `pg_isready`
rather than on a first successful query.
