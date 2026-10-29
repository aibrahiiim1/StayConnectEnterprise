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
