# oglab: an isolated OneGate test lab on one Docker host (capacity and offline-activation acceptance)

These scripts built and drove the lab used for the capacity acceptance (2,000 simulated clients on a 2 vCPU / 4 GB appliance) and the
first-time offline activation acceptance of the audit-remediation mission (2026-10-29/30). Results are in
`docs/runbooks/CAPACITY_AND_OFFLINE_ACCEPTANCE.md`. They are TEST tools: nothing here is installed on an appliance.

## What the lab is, and what it is not

A lab of **systemd containers**, not virtual machines (the workstation had no hypervisor rights and little disk): every host is a privileged
Ubuntu 22.04 container on Docker Desktop (WSL2 kernel 6.6) running systemd and its own Docker, installed by the product's own installers
(`central-install.sh --mode new`, `appliance-install.sh`). Appliances are capped with `--cpus 2 --memory 4g --cpuset-cpus` (the approved
2 vCPU / 4 GB reference). It is **isolated**: its own Central with a brand-new vendor key, assignment keys, registry root and appliance CA;
the production Central's name is blackholed in the appliances' `/etc/hosts`; nothing is registered on any real Central.

| Container | Role | Network |
|---|---|---|
| `oglab-central` | a new Central (`--mode new`, name `central.oglab.test`) | `oglab-wan` 172.30.10.10 |
| `oglab-appl` | appliance for the capacity run (2 vCPU / 4 GB) | WAN 172.30.10.20, LAN = a veth to the generator |
| `oglab-applb` | a second, factory-clean appliance for the offline activation (`--offline-activation-only`) | WAN 172.30.10.21 |
| `oglab-gen` | the load generator (`ogload`, `tools/ogload`) | LAN side: veth to the appliance |
| `oglab-sink` | the "internet": an HTTP server the clients download from | `oglab-wan` 172.30.10.50 |

## Lab adaptations (each one is a property of the container host, not of the product)

* Package mirrors: `archive.ubuntu.com` answered HTTP 403 for some files; the base image (`bake.sh`) uses an HTTPS mirror and retries.
* The nested Docker uses `overlay2` (the containerd snapshotter cannot create whiteouts in a nested container).
* `/etc/resolv.conf` points at Docker Desktop's resolver (the appliance's own nftables replaces the rules Docker's 127.0.0.11 needs).
* `net.bridge.bridge-nf-call-*=0`: the host kernel has `br_netfilter` loaded globally; with it on, the first packet of a new connection
  reached the appliance's INPUT chain as `ct state invalid`. A VM has no such shared module.
* The generator's clients are macvlan interfaces in `private` mode (bridge mode copies every broadcast to every sibling, an N-squared storm);
  the appliance LAN is a veth pair created by a `--net=host` helper because Docker's `--internal` networks drop IP packets outside their own subnet.
* `netplan.io` and the unbound root key are installed in the container image the installer expects on Ubuntu Server; `eth1` is enslaved to the
  client bridge by hand (no udev in a container, so networkd marks it unmanaged); each appliance gets its own `/etc/machine-id` (the serial is derived from it).
* One appliance container shares the Docker host's kernel and CPUs: the 2 vCPU limit is a cgroup quota plus a cpuset, not a virtual CPU.

## Order of use

`bake.sh` (base image) -> `docker run` the containers -> `central-install.sh --mode new` / `appliance-install.sh` -> activate
(`central_api_lab.py activate <id> <code>`) -> `lab_networks.py`, `lab_policy.py`, `lab_setup.py` (networks, the sink exception, vouchers) ->
`ogload setup / signin / traffic` through `scenario.sh`, with `monitor.sh` on the appliance -> `analyze.py monitor.csv scenario.log`.
Offline activation: `off_flow.py` on Central (import request, mint package) and `apply_pkg.py` on the appliance (uploads the package exactly as the Admin Console does,
with tampering options for the refusal tests).

## Sign-in capacity round (2026-10-30): what changed in the lab, and the measuring tools

The lab was rebuilt for the sign-in capacity closure ([`docs/reviews/SIGNIN_CAPACITY_CLOSURE.md`](../../docs/reviews/SIGNIN_CAPACITY_CLOSURE.md)).
Differences from the first lab, each a property of the lab host that decided what the earlier numbers meant:

* **The host kernel's neighbour (ARP) table.** Docker Desktop's VM keeps one table for every container; its hard limit (`gc_thresh3`, 1,024) is below the
  2,000+ simulated clients plus the appliance's own entries, and the kernel logged `neighbour: arp_cache: neighbor table overflow!`. That, not the generator's
  dialling pattern, is what dropped SYNs in the earlier runs ("traffic success 7 percent"). Raise it from the VM:
  `wsl -d docker-desktop -e sh -c "sysctl -w net.ipv4.neigh.default.gc_thresh1=8192 net.ipv4.neigh.default.gc_thresh2=32768 net.ipv4.neigh.default.gc_thresh3=65536"`
  (it does not survive a Docker Desktop restart). The product carries the same setting for the appliance itself (`deploy/sysctl/99-stayconnect.conf`).
* **Database memory.** The TimescaleDB image tunes itself from the host's memory (15 GB here: `shared_buffers` 3,958 MB); the lab sets what it would pick on the
  4 GB VM (`pgtune.sh`: 1 GB, 3 GB effective cache, 5 MB work_mem) and adds `pg_stat_statements`, `track_io_timing`, `track_wal_io_timing`, `log_lock_waits`.
  Postgres is restarted by recreating its container through the product's compose file
  (`docker compose -f /opt/stayconnect/deploy/compose/infra.yml up -d --force-recreate postgres`), never by restarting the nested Docker daemon.
* **Churn clients.** A client that signs in exactly when its 60-second session ends is a hand-over (the new session takes the old class's address and goes down
  netd's slow per-session path, 0.5 to 1 s each). The churn therefore cycles 400 clients (indices 2000 to 2399) at 300 sign-ins a minute, so each is idle for
  about 20 s between the end of one session and its next sign-in.
* **Measuring.** `sampler.sh` (every 5 s: cgroup CPU and memory, PostgreSQL connections, locks and waits, authorisation-set size, duplicate live sessions per
  device, licence count), `gen-churn.sh` (300 a minute plus 10 percent of the long sessions moving), `ogload signin -log` (one line per attempt),
  `attempts.py`, `csvsum.py`, `cpu.py` (analysis), `pgsample.sh` + `pgagg.py` (who waits for whom), `pgstat.sh` (pg_stat_statements), `passes.py`/`passfull.py`
  (netd pass lines), `routes.py` (scd route latencies), `poolshow.py` (scd pool metrics), `collect.sh`, `cpuproc.sh` (CPU per service), `prof.sh` (pprof),
  `probe.sh` (commit latency), `lab_quota.py` (data-quota vouchers). The scripts assume the lab's layout (`/rel` is the shared directory, `/root` the working directory).
  Diagnostic binaries: `build-lab-binaries.sh stayconnect_production,oglabdiag <svc>` serves pprof on loopback (scd 6061, netd 6062, acctd 6063); a release is
  never built with that tag.

## Final mission round (2026-10-31): outage continuity, /19 and capacity tools (`finalmission/`)

Host-side orchestration and analysis for `docs/runbooks/OUTAGE_CONTINUITY_LAB_PLAN.md`, `NETWORK_19_LAB_PLAN.md` and the capacity rerun (paths are the lab workstation paths, edit before reuse): `orch.py` (one continuity run: population, outage, timed events, collection), `setwatch.py` (the kernel set and netd governor every 2 s), `analyze_cont.py`, `analyze_acct.py`, `cap_run.sh` / `cap_remote.sh` / `cap_analyze.py` / `cap_stats.py` (offered rate from `ogload signin -log` column 5, enforced rate from the activation timestamps the lab trigger `cap_trigger.sql` records), `s19_*` and `u32_*` (the /19 plan), `ac_api.py`. `ogload` now logs the OFFERED (scheduled) time of every sign-in attempt next to its start and end, schedules offers on a fixed grid, and `traffic` takes `-perclient` and `-up`. **`sampler.sh` and `cpuproc.sh` read the wrong cgroup in a container with a private cgroup namespace before this round** (they used /init.scope); they now use the namespace root and `sampler.sh` also records CPU/IO/memory pressure.
