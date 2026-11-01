# The golden OneGate lab and the five-minute accelerated tests (2026-10-31)

Status: **prepared, not executed.** Test execution was suspended by the Product Owner for the mission that wrote this; nothing here has run yet.

## Why

The last full mission spent about 15 hours in one sequential lab, and part of that was rebuilding: base image, two installers, activation,
2,400 client leases, before the first measurement. The golden lab builds that state **once**, freezes it, and resets to it in about a minute.
The tests run on it are **accelerated**: each proves a behaviour in at most 300 seconds of real time.

## The golden lab

| Command | What it does |
|---|---|
| `golden.sh fresh` | Describes the fresh-install path (`../README.md`, "Order of use"). It is kept separate and is never replaced. Run it for a release that changes an installer, the migration chain, Gate-P or the base image. |
| `golden.sh freeze <tag>` | Stops the lab containers cleanly (systemd shutdown, so PostgreSQL stops as on power-off) and records them: `docker commit` images `oglab/golden-<container>:<tag>`, a tar and SHA-256 of every volume, and the `docker inspect` of each container. It writes `manifest.json` with the **release SHA read back from the installed binary**, the schema version (`max(schema_migrations)`) and a hash of the fixture (networks, packages, vouchers). A tag is immutable. |
| `golden.sh reset <tag>` | Removes the lab containers and recreates them from the golden images and volume snapshots. Each snapshot's checksum is verified first. It then waits for the database and runs `check`. |
| `golden.sh check <tag>` | Refuses to go on if the running lab is not `<tag>`: the release SHA, schema version or fixture hash differ. |
| `golden.sh upgrade <release-dir>` | Installs the release under test with the product's own `appliance-install.sh --upgrade`. That is the upgrade path, so it is itself exercised. It refuses a schema downgrade. |
| `golden.sh run <scenario>` | Runs `scenarios/<scenario>.sh` under `timeout 300`. The result goes to `results/<time>-<scenario>.json`. Exceeding the bound is reported **NOT VERIFIED**, never PASS. |

The lab is isolated, exactly as `../README.md` describes: its own Central with a new vendor key, the production Central's name blackholed,
and nothing registered on any real Central.

### Compatibility rule

A golden lab tag is valid for a release under test when **both** of these hold:

- its schema version is at or below the release's last migration;
- the fixture hash matches.

`golden.sh upgrade` then moves it forward. A release whose migrations cannot apply to the golden schema, or that changes an installer or Gate-P, needs `fresh` and then a new `freeze`.

## Accelerated tests: the rules

1. **At most 300 seconds of real time per scenario or process.** The runner enforces this. A unit or integration package is expected to be far below it.
2. **Simulate time; never change production constants.**
   - Go code under test takes injected clocks: the netd continuity harness `contH.step` advances monotonic, kernel-lease and wall time together, so 120 minutes of outage run in milliseconds.
   - Database tests pre-date their records (for example, "activated an hour ago") instead of waiting.
   - Lab scenarios use the smallest real value of an operator setting. The continuity window is set to 1 minute, which is a legitimate setting, not a test hook.
3. **No test control reachable in the deployed product.** No flag, build tag or endpoint is added to a release for the lab. (The pprof lab binaries built with `oglabdiag` remain lab-only and are never released.)
4. **Bounded faults:**
   - stop or start a unit;
   - stop the database container;
   - flush a kernel set (what a reboot leaves);
   - make a remote folder read-only.

   Every scenario restores what it changed, in a `trap`.
5. **What five minutes cannot prove stays NOT VERIFIED.** This includes:
   - sustained real-time performance;
   - 60- or 120-minute real-time outages;
   - long-run stability;
   - physical devices and RF.

   A run proving them needs its own authorization and is reported separately. A short functional run is never presented as one.

## Scenarios (prepared, not executed)

| Scenario | Proves | Real-time proof it does NOT give |
|---|---|---|
| `continuity-1min` | Guests are held through an acctd outage until the window ends, cut within a tick of it, and re-admitted when acctd returns. | The 60- and 120-minute windows (same code path, proven by the simulated-clock unit tests) |
| `reboot-db-down` | With the database down after a restart, no authorization appears (fail closed, decision D-1). | A real host reboot (boot id change; covered by the netd unit tests) |
| `package-end-reset` | End withdraws only the ended package's kernel authorization. Reset is a zero-price ADMIN_GRANT cycle the guest can use again, with no posting. | Behaviour under load |
| `ledger-sftp` | Save answers OK only when the ledger is in the folder the SFTP server itself shows, by SHA-256. A relative folder in a chroot works. An unwritable folder is "not saved". Remove keeps the remote copy. | Other SFTP server software (Windows OpenSSH, NAS vendors) |
| `backup-five-sets` | Rotation keeps whole sets, at most five at rest, and never touches earlier backups. | Backups of production-sized databases |

## Before the first run

1. Build the lab by the fresh path once, then `golden.sh freeze g1-<release-short-sha>`.
2. Set `EDGED_ROLE_TEST_DSN` for the integration suites: a DSN connecting as `svc_edged` to the lab-built integration database after Gate-P has run. **The Admin Console role tests are skipped without it**, and that is how a missing privilege reached the test appliance on 2026-10-31.
3. Raise the Docker Desktop VM's neighbour table (see `../README.md`) before any scenario with more than about 1,000 clients.
