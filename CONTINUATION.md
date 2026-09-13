# Continuation checkpoint

Branch: `delivery/central-licensing-removal-and-pms-departures` (off master `8b76113a`).
Nothing merged. Nothing deployed. Appliance `172.21.60.25` and Central `150.0.0.252` unchanged.

## Commits so far

| sha | what |
|---|---|
| `eb1ae07b` | resync GO without `G#` is no longer admitted as a departure event (stops backlog growth) |
| `d768f719` | reconciliation by reservation: settings, run ledger, disposition ledger, reconcile fn |
| `b2259ed6` | completeness proven against room inventory; 6 refusals; post-snapshot protection |
| `dd0a3efd` | correction: there are no live room-only departures; the 547 were RESYNC-by-room |

## Evidence established (live, read-only)

* LIVE departures 565/565 carry `G#`; RESYNC departures 14 125, none carry `G#`.
* A resync enumerates the whole building: GI/GC = occupied, GO = vacant. Totals 588/589/589/589 over
  eleven generations while occupancy moved 445/467/479/454. This is the completeness test.
* Mirror 821 IN_HOUSE vs 445 rostered at generation 246. 6 stays are CHECKED_OUT while the latest roster
  still lists them -> wrongly closed by room-keyed resync inference.
* Appliance: one real interface `ddff5d07-f588-4f1f-8133-a0f393524476` (protel-fias, ACTIVE), 1 568 stays,
  zero test-pattern records. There are no obsolete test records in PMS data.
* Central: `fleet_telemetry` 251 166 rows / 13 appliance identities / 2026-07-12..2026-09-13;
  `fleet_telemetry_dedupe` 251 286; `usage_counters`, `appliance_commands`,
  `appliance_update_assignments` all 0. Two registered appliances.

## Build environment

Designated workstation. Docker 29.5.2 healthy, data disk on C: (F: is NOT achievable -- Docker Desktop
4.76 on the WSL2 backend ignores `DataFolder` and reverted a supported WSL distro move). C: ~61 GB free.
Constraint: system commit charge is near its ceiling, so Go builds need `-p 2` / `GOMAXPROCS=2` and Node
needs `--no-file-parallelism`. Use `GOCACHE=/d/tmp/gocache GOTMPDIR=/d/tmp/gotmp GOMODCACHE=/d/tmp/gomodcache`.

Fixture recipe that works: `timescale/timescaledb:2.16.1-pg16`, wait for 3 consecutive successful
`psql -c 'select 1'` (the image restarts after initdb), create the 8 service roles, apply
`migrations/baseline/0000_production_baseline.sql`, reassign `iam_v2` to `iam_v2_owner`, then apply 0072.

## DONE: PMS ingestion fix deployed and live-verified (2026-09-13 15:14 UTC)

`pmsd` + `edged` rebuilt in `golang:1.25.12` on the designated workstation and installed on
`172.21.60.25`. Rollback point: `/opt/stayconnect/rollback/pms-dd0a3efd/{pmsd,edged}.bak`.

Live evidence after deployment:

* `resync_go_since_deploy = 0` -- not one roster snapshot admitted as a departure.
* `roster_gi_since_deploy = 302` -- the roster still ingests normally; the link is unharmed.
* resync GO total frozen at 14 274 (the pre-deploy baseline); review backlog frozen at 13 717.
* journal shows `skipping roster-snapshot departure with no reservation` carrying only record type,
  interface id, field code and a counter -- no guest value.

## DONE: audit of the 547 snapshot-based closures -- NOTHING NEEDS REPAIR

547 applied snapshot GO events resolve to 328 distinct stays. Against the current authoritative
generation 247:

| snapshot-closed stays | in latest roster | verdict |
|---|---|---|
| 311 CHECKED_OUT | no | closure agrees with current evidence -- correct |
| 10 IN_HOUSE | yes | already self-healed by a later GI (lifecycle_version up to 22) |
| 7 IN_HOUSE | no | stale IN_HOUSE; ordinary reconciliation candidates, not repairs |

**Zero stays are in a proven-incorrect state.** The harm was transient: subsequent GI records reinstated
every wrongly-closed stay, which is why lifecycle_version reached 22-26 on these rows.

The 13 stays that ARE checked out while generation 247 lists them were closed by LIVE GO events carrying
reservations, between 14:51 and 15:07 -- all AFTER the 14:50:33 roster boundary. The mirror is more
current than the roster; they are correct. Reinstating them would have resurrected 13 departed guests'
access. This is exactly the "do not blindly restore" case, and the earlier "6 wrongly closed" claim was
the same measurement error made against generation 246.

Access/entitlement consequence: the whole appliance holds 21 entitlements. The 10 self-healed stays hold
NONE (those guests never signed in), so no guest lost or regained access through any of this. The 10
entitlements attached to CHECKED_OUT stays are all TERMINATED, which is correct.

## DONE: Central non-licensing removal (code + data)

`ebeca837` removes ~4 000 lines: internal/{fleet,commands,updates,configpush,heartbeat,transport}; the
fleet/usage/sessions/pms_admin/commands/updates APIs; the NATS transport selection, heartbeat consumer,
fleet consumer and command/update result consumers in ctrlapi; cmd/nats-authz and cmd/nats-acctest; and the
cloud-admin fleet page. `github.com/nats-io/*` is no longer a control-plane dependency at all.

KEPT on purpose: `internal/metrics` (payments, router, licensing paths), `internal/stripe` (commercial
billing, not appliance telemetry), and `PMSProvider`/`strDeref`/`newUUIDv4`, moved to
`internal/api/shared_helpers.go` because they merely happened to live inside deleted files.

Central migration `0045` applied LIVE to 150.0.0.252: dropped fleet_telemetry (251 166 rows),
fleet_telemetry_dedupe (251 286), usage_counters, appliance_commands, appliance_update_assignments.
Zero inbound foreign keys were verified first. Telemetry tables remaining: 0.

Verified after: ctrlapi restarted and healthz=200; appliance licence Active, cloud_stale=false, evaluated
every minute; scd/pmsd/edged/portald all active.

## Remaining

1. **Deploy the new ctrlapi binary to Central.** The telemetry code is removed in source and the DATA is
   gone, but 150.0.0.252 still runs the previous ctrlapi build. Harmless (nothing sends to it, and the
   tables are gone) but the removal is not complete on the host until the binary ships.
2. Wire reconciliation into pmsd/edged + a Hotel Admin screen, and apply migration 0072 to the appliance.
   0072 is written, tested and committed but NOT yet applied anywhere.
3. Gates, protected merge, governance sync.
