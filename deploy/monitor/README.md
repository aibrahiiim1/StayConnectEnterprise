# OneGate appliance monitor

Passive, read-only operational monitoring for a OneGate appliance. It changes nothing: no configuration, network, database,
PMS, licence or service state. It needs no Admin Console account.

| Run | Timer | Covers | Typical cost (test appliance, 450 guests online) |
|---|---|---|---|
| `quick` | every 5 min | services, resources, database, enforcement, DHCP, network, portal, Admin Console, PMS, posting, licence, Central, certificates, sign-in health, session integrity, errors | about 3.6 s elapsed, under 1 s CPU |
| `deep` | every 30 min | quick, plus backups, install parity, client activity, per-device diagnostics, trends and the report | about 9 s elapsed, about 2.2 s CPU |

Both run at low CPU and I/O priority under `CPUQuota=25%`. A quick run that finds another run still going skips itself;
a deep run waits up to a minute for it, so the report is not skipped.

## Output

- `/var/log/ogcheck.log`: one summary line per run, then one `ISSUE [WARN|CRITICAL] …` line per active issue and one
  `RECOVERED …` line when an issue clears. This is the file and format the operators already read.
- `/var/lib/onegate-monitor/report.txt` (deep): executive summary, every check by area, client activity, per-network,
  hourly and daily trends, device diagnostics, deadlocks, recently resolved issues. `onegate_monitor.py report` prints it.
- `/var/lib/onegate-monitor/devices.tsv` (deep): every device finding.
- `/var/lib/onegate-monitor/results.json` (deep): every check result and metric, for tooling.
- `/var/lib/onegate-monitor/state.json`: incident and trend state.

All files under `/var/lib/onegate-monitor` are root-only (0600). MAC addresses are masked to their last two octets;
no names, rooms or reservations are written.

- `/var/lib/onegate-monitor-console/health.json` (every run): what the Admin Console's **System health** page shows (verdict,
  checks, active and resolved issues, and the last deep run's client activity and device findings). Root-owned, readable only by
  the `stayconnect` group (0640, directory 0750), and it carries **no client IP address**. edged serves it read-only at
  `GET /edge/v1/diagnostics/monitor` (diagnostics read permission), redacts every text again, and reports it stale after 15 minutes;
  a missing or unreadable file is shown as such, never as a healthy appliance. Manual `--stdout` runs do not write it.

## Results

- **PASS**: the evidence was read and is within the threshold.
- **WARN** / **CRITICAL**: the evidence was read and is outside the threshold.
- **NOT VERIFIED**: the evidence could not be read (a service, file, log or the database did not answer). It is never
  shown as PASS.

An issue is raised after a check has failed for its persistence count of consecutive runs (1 unless listed below). It is
then repeated on each run with its first-seen time and run count, not re-announced, and reported once as RECOVERED when it
clears. A check that did not run (deep-only checks during a quick run) keeps its incident.

## Thresholds

| Area | Check | WARN | CRITICAL |
|---|---|---|---|
| services | unit not active | | any |
| services | crash restart by systemd since the last run | any | |
| services | failed units | any | |
| services | `stayconnect-pg` / `stayconnect-redis` not running | | any |
| resources | CPU busy (2 s sample) | > 85 % for 2 runs | > 95 % for 3 runs |
| resources | memory available | < 500 MB for 2 runs | < 200 MB for 2 runs |
| resources | disk `/`, `/var/lib/docker` | > 85 % | > 95 % |
| database | query latency | > 3 s for 2 runs | |
| database | connections | > 80 % of max | |
| database | transaction open > 5 min | any, 2 runs | |
| database | lock waits | ≥ 5, 2 runs | |
| database | deadlocks (database counter) | > 5 in the last hour | |
| database | lock waits over 1 s logged since the last run (`log_lock_waits`), counted once and named by table | > 30 | |
| database | `log_lock_waits` off | any | |
| database | the database log could carry guest data (`log_error_verbosity` not `terse` or `log_min_error_statement` not `panic`, GDPR audit C5) | any | |
| enforcement | netd pass | > 15 s for 2 runs | no pass in 3 min |
| enforcement | DB online vs netd enforced | gap > max(10, 5 %) for 2 runs | |
| enforcement | authorizations withdrawn (address moved) | > 20 in 15 min | |
| background jobs | package-expiry pass failures | ≥ 3 in 15 min | |
| background jobs | newest database backup | > 30 h | > 54 h |
| admin console / portal | edged health, console, portal HTTP | | not answering / unhealthy |
| network | default route, WAN link, client bridges and their uplinks, client DNS | | missing or down |
| dhcp | Kea control socket | no answer | |
| dhcp | client subnets not authoritative | any | |
| dhcp | pool utilisation | > 85 % | > 95 % |
| dhcp | requests Kea could not serve | any in 15 min | |
| pms | link not connected | 2 runs | disconnected > 30 min |
| pms | last complete roster | > 20 min, 2 runs | > 60 min |
| posting | room-charge items pending > 30 min or parked | any | |
| licensing | days left | < 30 | < 7, expired or not active |
| licensing | online guests vs licence | > 85 % | ≥ 100 % |
| licensing | sign-ins refused by the licence | any in 15 min | |
| central | last licence validation | > 24 h | > 80 % of the offline grace |
| certificates | portal, Admin Console, Central client | < 21 days | < 7 days |
| authentication | refused as service unavailable | ≥ 3 in 15 min | ≥ 10 |
| authentication | refused for PMS routing | ≥ 3 in 15 min | |
| authentication | sign-ins from an address leased to another device | ≥ 5 in 15 min | |
| authentication | devices refused 5+ times in 60 min for a **system** reason | any | |
| authentication | automatic reconnection failed on a server error | any in 15 min | |
| automatic reconnection (deep) | eligible devices back on the network > 10 min, not reconnected | any, 2 runs | |
| sessions | disconnected by a network removal or replacement | any in 15 min | |
| sessions | ended by the system while the device was passing traffic | ≥ 3 in 15 min | |
| session integrity | online on an ended package; device not authorized; reconnection after a sign-out | | any |
| session integrity | past session expiry; authorization record mismatch; over device limit; > 100 MB over quota; two live sessions on one device | any | |
| errors | ERROR lines from one service | > 20 in 15 min | |
| installation (deep) | binaries differ from the installed release; shipped migration not applied | | any |
| installation (deep) | service units differ from the release (ignoring comments and descriptions) | any | |

Devices stuck at the portal on wrong details or package policy are counted in the summary (`stuck_60m=N(M system)`) but
not raised: they are guest errors or the policy working as set, not faults.

## Client activity and device diagnostics

Devices, sessions and room sign-in attempts are counted separately, for 15 min, 1 hour, 24 hours and 7 days, each beside
the equal period before it. New devices are those whose first session ever falls in the period. Unnecessary repeat
sign-ins are verified manual sign-ins whose device's previous session on the same network ended on an address change
while its package was still valid.

Device findings (24 h) name the device (first 8 characters of its OneGate id), the masked MAC, the IP, the network, the
issue, the cause and whether it is confirmed or suspected, severity, first and last occurrence, count, ONGOING or
RESOLVED, and the action. Wrong details and policy refusals are LOW; failed reconnections, IP conflicts and repeated
cut-offs are MEDIUM; a disconnect while active or an online device without authorization is HIGH.

## Install, roll back, remove

From a release or checkout on the appliance, as root:

```
bash deploy/monitor/install-monitor.sh             # install or upgrade; disables the old ogcheck timer
bash deploy/monitor/install-monitor.sh --rollback  # stop the new timers, re-enable the old ogcheck timer
bash deploy/monitor/install-monitor.sh --remove    # remove the new monitor
```

`deploy/scripts/appliance-install.sh` installs it on every fresh install and every upgrade (step 7c), from the release's own
`/opt/stayconnect/deploy/monitor`; a monitoring failure warns and never fails the install. Run the script above by hand only to roll back
or remove it.

## Tests

```
python3 -m unittest deploy/monitor/test_onegate_monitor.py
OG_TEST_PSQL='psql -X -q -v ON_ERROR_STOP=1 postgresql://…/stayconnect_site' python3 -m unittest deploy/monitor/test_onegate_monitor.py
```

The second form also runs every query the monitor issues against a database at the current schema.
