# /19 client network: lab plan (TEST appliance and container lab)

Status: **plan for a lab run that has not happened.** Everything in the code and unit tests is done (see [`../CLIENT_NETWORKING.md`](../CLIENT_NETWORKING.md) section 1a and
[`../AUDIT_DECISION_DISPOSITIONS.md`](../AUDIT_DECISION_DISPOSITIONS.md)); this document says exactly what the lab must still show, with commands and pass criteria.
Product Owner decision of 2026-10-30: the widest client network is `/19` (8,190 usable addresses). **That is address capacity, not a claim that 8,190 clients are served
at once**; no step below should be reported as proving 8,190 concurrent clients unless that many were actually signed in and measured.

Scope and rules (CLAUDE.md): the TEST appliance `172.21.96.97` (standing deployment authorization, D79) and the container lab only. **Never** the production Central
(`41.128.240.133` / `172.21.96.96`) and never a retired host. Two physical NICs only: WAN (also management) and LAN (client networks and VLANs). No Go-Live, no PMS or
financial traffic, no Root-CA or trust change, no destructive data operation. Record the exact deployed commit read back from the installed artifact.

What already holds, so the lab does not repeat it: size rule at every prefix `/8`..`/32` (netcfg, API, hotel-admin); overlap with client networks and with the WAN/management
subnet; class-id uniqueness over a whole `/19` and across bridges; Kea/netplan/nft rendering of a `/19`; **real kernel (container, kernel 6.6.87.2-microsoft-standard-WSL2)**:
6,300 sessions at the top of a `/19`, per-session rate within 4-5 % of configured both ways, class counters equal to payload plus headers, no counting on other classes or the
other bridge, 300 more admitted in 2.1 s on top, 8,189-address neighbour sweep without overflow; **real Kea 2.2** serving the pool across the whole `/19`
(results recorded in the evidence section at the end of this file once the lead has rerun them).

## 0. Variables and helpers

```sh
# on the lead workstation
APPL=172.21.96.97                    # TEST appliance (management = WAN address)
NET19=10.66.0.0/19                   # a /19 that must not overlap anything: check `ip -4 route` on the appliance and the WAN subnet first
GW19=10.66.0.1
POOL19_FROM=10.66.0.10 ; POOL19_TO=10.66.31.250
# Admin API helper: tools/oglab/labapi.py logs in and calls the Admin API; set BASE/HOST/EMAIL/PW for the TEST appliance
# (BASE = https://$APPL/api/edge/v1, a test operator, never a real person's account).
```

Management reachability probe (leave running in its own terminal for the whole run; it is the pass/fail witness for "management never moves"):

```sh
# one line per second: time, ssh banner, admin 443 on the MANAGEMENT address, ping. Any gap during an apply is a FAIL.
while true; do
  t=$(date +%T); p=$(ping -n 1 -w 800 $APPL >/dev/null 2>&1 && echo up || echo DOWN)
  h=$(curl -sk -o /dev/null -m 2 -w '%{http_code}' https://$APPL/ || echo 000)
  s=$(timeout 2 bash -c "exec 3<>/dev/tcp/$APPL/22 && head -c 4 <&3" 2>/dev/null || echo none)
  echo "$t ping=$p https=$h ssh=$s"; sleep 1
done | tee mgmt-probe-$(date +%Y%m%d-%H%M).log
```

Baseline snapshot on the appliance before anything (save the files; they are the rollback comparison):

```sh
ssh $APPL 'set -x; git -C /opt/stayconnect rev-parse HEAD 2>/dev/null; go version -m /usr/local/bin/stayconnect-netd 2>/dev/null | grep vcs.revision;
  ip -4 -br addr; ip -4 route; nft list ruleset | sha256sum; nft list table inet stayconnect > /tmp/before.nft;
  sysctl net.ipv4.neigh.default.gc_thresh1 net.ipv4.neigh.default.gc_thresh2 net.ipv4.neigh.default.gc_thresh3;
  systemctl is-active stayconnect-netd stayconnect-acctd stayconnect-edged stayconnect-scd caddy;
  for d in $(ip -br link | awk "/^br-g/{print \$1}"); do tc -s class show dev $d | grep -c "^class htb"; done'
```

Pass: the deployed netd/edged commit is the one under test (a build that contains `netcfg.CheckClientSubnet`), `gc_thresh1/2/3 = 8192/32768/65536` (the
sysctl file is installed by `--upgrade`; if it still shows `4096/8192/16384` the file was not applied: `sysctl --system` and record it as a finding).

## 1. Refusals first (nothing is created or changed)

Each call must be answered with the stated code; none may create a row. (`labapi.py METHOD PATH JSON`.)

| # | Call | Expected |
|---|---|---|
| 1.1 | `POST /network/guest-networks` with `subnet_cidr 10.66.0.0/18` | HTTP 400, code `subnet_too_large`, message names `/19` and 8190 |
| 1.2 | the same with `/17`, `/8` | HTTP 400 `subnet_too_large` |
| 1.3 | `/31` | HTTP 400 `subnet_too_small` |
| 1.4 | `/19` over an existing enabled `/20` or `/24` (create a `/24` first on a free range, then a `/19` that contains it) | HTTP 409 `subnet_overlap` |
| 1.5 | a `/19` that contains the appliance's WAN/management address (`172.21.96.0/19` if the WAN is `172.21.96.97/24`) | HTTP 400 `subnet_overlaps_host` (API, when netd can say what the WAN is) and, if forced into the database, a failed apply with `subnet_overlaps_host` |
| 1.6 | `PUT /network/guest-networks/{id}` on an **enabled** `/20` with `subnet_cidr` the enclosing `/19` | HTTP 409 `subnet_widen_refused` (the existing `/20` is untouched) |
| 1.7 | Network -> System: set the LAN prefix to 18, then to 19 beside the WAN | 18: `invalid_prefix`; 19: accepted by validate; a LAN `/19` containing the WAN subnet: `subnet_overlap` |
| 1.8 | Hotel Admin -> Client networks -> New: type `10.66.0.0/18` | the step is blocked with "A client network can be at most /19 (8,190 addresses)" before any request is sent |

Pass: all eight. Evidence: the response bodies.

## 2. Staged apply of a /19 with management reachability, health checks and rollback

Do this on the TEST appliance with the management probe running. Use a test operator.

```sh
# create (switched ON by default; the apply is what makes it live)
labapi.py POST /network/guest-networks '{"name":"Lab /19","network_type":"untagged","parent_interface":"<LAN NIC, e.g. ens192>","gateway_ip":"10.66.0.1","subnet_cidr":"10.66.0.0/19",
  "dhcp_mode":"local","pools":[{"start_ip":"10.66.0.10","end_ip":"10.66.31.250"}],"lease_default_seconds":3600,"lease_min_seconds":900,"lease_max_seconds":7200,
  "captive_portal_enabled":true,"internet_access_enabled":true,"nat_enabled":true}'
labapi.py POST /network/validate '{}'          # expect ok:true and no issues
labapi.py POST /network/apply '{"reason":"/19 lab","your_password":"@PW","password":"@PW"}'   # expect state pending_confirmation, every health check ok
```

While the revision is `pending_confirmation` (120 s watchdog), on the appliance:

```sh
ip -4 -br addr show | grep -E 'br-g.* 10.66.0.1/19'                 # bridge carries the /19
nft list table inet stayconnect | grep -E '10.66.0.0/19'            # guest_subnets element + masquerade rule
nft list table inet stayconnect | grep -E 'ip daddr 172\.|dport 443 accept'   # admin 443 is still pinned to the management address only
curl -skI --resolve x:443:<WAN address> https://x/ -m 3 ; echo "443 on the WAN address (must be refused or timed out when it differs from MgmtAddr)"
kea-shell --host ... || echo '{"command":"config-get"}' | socat - UNIX-CONNECT:<kea control socket> | grep -o '"subnet": *"10.66.0.0/19"'   # live Kea has the /19
journalctl -u stayconnect-netd --since -5min | grep -iE 'warn|error' ; true
```

Pass criteria (all): the apply response is `pending_confirmation` with **all** health checks `ok`; the management probe shows **no DOWN, no 000, no gap** from before the
apply to after the confirm; `ip addr` shows the `/19` on the new bridge and the previous client bridges unchanged; Kea's live config has the subnet and the pool;
`nft` shows the `/19` in `guest_subnets`; admin `443` accept still names only the management address. Then:

* **Confirm path:** `POST /network/revisions/{id}/confirm` -> revision `active`; re-run the baseline snapshot and diff `before.nft` (only the new network's rules differ).
* **Rollback path A (watchdog):** repeat with a second `/19` (or after deleting the first) and **do not confirm**: after 120 s the revision is `rolled_back`, the bridge address
  is gone, Kea has no such subnet, `nft` equals `before.nft` for the old networks, the probe shows no gap.
* **Rollback path B (operator):** apply, then `POST /network/revisions/{id}/rollback` while pending: same end state.
* **Failed health check:** make the apply fail on purpose (stop Kea control socket access or point the pool outside the subnet, which validation refuses first; the practical way is
  `systemctl stop kea-dhcp4-server` right before apply). Expect `failed`/auto-rolled-back and the previous configuration intact.
* **Staged replacement of a /20 by a /19** (the supported "widening"): `POST /network/guest-networks/{id}/replace` with `subnet_cidr` the `/19` and the old network's pool
  transposed; apply, confirm. Pass: the old `/20` network is switched off and archived with its history, clients renew into the `/19`, the probe shows no gap.

## 3. DHCP across the whole /19, including the top of the range (container lab and TEST)

Container lab (already scripted; rerun it on the lab host whenever Kea changes): `tools/oglab/slash19/kea_slash19_lab.sh` inside `c19-kea:1` (README in that directory).
It config-tests the file the product renders, leases the whole pool to distinct MACs through a real DHCP exchange, checks the reservation at `10.65.31.254`, the pool
exhaustion behaviour, Kea's `lease4-get-all` against the memfile, a Kea restart and INIT-REBOOT renewals. Pass: every line `PASS`; `highest index == pool top`; clients beyond the
pool get no offer and never a wrong address; after the restart every lease renews with the same address.

TEST appliance (real Kea 2.0.x from Ubuntu 22.04, which the container lab does not have): from a client host on the LAN port (the oglab generator, or any Linux box with a
macvlan on the LAN NIC), run `tools/oglab/slash19/dhcpclient.py <iface> 8200 100000 dora` (Python 3, root) with the network applied and confirmed. Pass: the same checks;
additionally on the appliance `wc -l <kea lease csv>` equals the leases plus the header and `ls -la` of the file shows its size (record it), Kea RSS (`ps -o rss= -C kea-dhcp4`),
and `journalctl -u kea-dhcp4-server` shows no `DHCP4_LEASE_ALLOC_FAIL`/`DB` errors. Record the DORA rate (the client prints it).

## 4. Shaping, accounting and the classifier at the high end

With the `/19` active and 2,000+ sessions signed in on it (oglab `ogload` supports setting the client address range; if sessions must be created by hand, use vouchers from
`lab_setup.py`), at least 200 of them in the upper half (`10.66.16.0` - `10.66.31.254`):

```sh
tc -s class show dev <bridge> | grep -c '^class htb 1:3'        # upper-half classes (minor 0x3000+) exist
tc filter show dev <bridge> parent 1: | grep -c flower           # one flower filter per session, pref 1
tc filter show dev ifb-<name> parent 1: | grep -c flower         # and per upload direction
tc filter show dev <bridge> parent 1: | grep -E 'handle 0x3ffe|dst_ip 10.66.31.254' # the last host, if signed in
```

Pass: class count equals the number of shaped sessions per device; no stray class after sign-outs (`netd` logs no `stray`); acctd usage for an upper-half session grows with real
traffic and is equal to the class counter within header overhead; per-session rate within the configured value minus 4-5 % (the real-kernel container run measured 2,873
of 3,000 kbit/s, 960 of 1,000). Measure admission time of a 300-session burst on top of the population (container: 2.1 s on 6,000).

**u32 fallback on a /19** (`NETD_SHAPE_CLASSIFIER=u32` in netd's environment, `systemctl restart stayconnect-netd` using the stop / reset-failed / start --no-block sequence):
the `/19` stays configurable and works; at most **2,047** sessions are classified per bridge, further sessions are not admitted (fail closed, reported by netd); existing
flower-classified sessions keep working and are released at their end. Pass: 2,047 shaped and the rest rejected with an error in the netd log, not silently unshaped. Then remove
the variable, restart, and verify new sessions use flower again (`tc filter show ... | grep -c flower` grows).

## 5. Large-network ARP / neighbour behaviour

```sh
sysctl net.ipv4.neigh.default.gc_thresh1 net.ipv4.neigh.default.gc_thresh2 net.ipv4.neigh.default.gc_thresh3     # 8192 32768 65536
dmesg -T | grep -c 'neighbor table overflow'                                   # note the count BEFORE
ip -4 neigh show dev <bridge> | wc -l                                          # entries now
# sweep every address of the /19 from the appliance (creates an entry per address):
python3 - <<'PY'
import socket
for i in range(2, 8191):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.setblocking(False)
    try: s.sendto(b'x', ('10.66.%d.%d' % (i >> 8, i & 255), 9))
    except OSError: pass
PY
sleep 3; ip -4 neigh show dev <bridge> | wc -l ; dmesg -T | grep -c 'neighbor table overflow'
```

Pass: about 8,189 entries on the bridge, **no new** `neighbor table overflow`, portal connections and DNS from a signed-in client keep working during the sweep (run a download
in parallel; the previous capacity round showed that an overflowing table made downloads fail at random), the portal answers, `neigh` entries age out afterwards.
Also check broadcast load: `ip -s link show <bridge>` multicast/broadcast RX counters over 60 s with 2,000 clients (record the rate; no pass/fail threshold is set by the
decision, but a figure must exist before anyone quotes a larger client count).

## 6. Client isolation, management access and the WAN

* Two clients, one in the lower half and one in the upper half of the `/19`: neither can reach the other (L3 through the gateway is dropped by `client isolation` and
  `inter-guest isolation`); both reach the portal on the gateway address; DNS to the gateway works; the bridge's own address is the only appliance address that answers on the client side.
* From a client: `curl -k https://<management address>/` and `https://<WAN address>/` must fail (the portal gateway/VIP answers only its own ports); admin `443` answers on the management address
  from the management side only. `nft list chain inet stayconnect input` shows the accept pinned to the management address.
* The WAN configuration (`ip -4 addr show <WAN NIC>`, default route) is identical before and after every step of section 2.

## 7. Restart and boot recovery

With the `/19` active and sessions present:

1. `systemctl stop/reset-failed/start --no-block` each of `stayconnect-netd`, `stayconnect-acctd`, `kea-dhcp4-server` in turn: sessions keep their classes (no `class change`), leases are
   kept (Kea memfile), `nft` fingerprint unchanged (`sc_render_fp`), the probe shows no gap.
2. Reboot the TEST appliance (and only it): the boot reconcile re-asserts the `/19` bridge address, Kea subnet, nft table, tc classes and filters from the stored revision;
   the `/19` is serving within the normal boot time; sessions that were authorised are re-asserted from the roster/entitlement state without re-sign-in where that is the product behaviour.
   Pass: `ip -4 -br addr` shows the `/19`; `tc filter show` flower filters for the active sessions; DHCP renewals succeed with the same addresses.
3. Hard stop during a pending-confirmation window (power the service off, do not confirm): after restart the revision is rolled back by the watchdog logic, not left half applied.

## 8. Things this plan deliberately does not claim

* Not 8,190 concurrent clients. The capacity records of the 2 vCPU / 4 GB reference (conntrack at 262,144, one fq_codel leaf per session, memory per session of about 32 KB
  kernel memory measured in the container, nft set rewrite cost per 256 leases at 8,000 sessions) are what bound concurrency; measure and quote them separately.
* No behaviour of a real switch/AP at `/19` scale (broadcast domain size, ARP proxy, DHCP relay on the access layer): ask the site's network team for their broadcast-domain limit.
* The legacy `u32` fallback is not a way to serve more than 2,047 sessions.

## 9. Cleanup

Delete or switch off the lab `/19` network (Network -> Client networks), apply and confirm; verify the baseline snapshot again (`nft`, `ip addr`, `tc` class counts equal the
pre-run values); remove `/tmp/before.nft` and any lab voucher/accounts created; stop the probe. The container lab: `docker rm -f` the containers created (unique names `c19-*`),
and leave no neighbour-table setting changed on a shared Docker host.

## Evidence recorded by the implementation round (container, 2026-10-30)

Real kernel, `c19-lab:1` (Go 1.26.10 + iproute2 6.15), privileged, kernel 6.6.87.2-microsoft-standard-WSL2: `SHAPE_REAL=1 SHAPE_REAL_N=6000 go test ./internal/shape -run TestRealKernelSlash19HighEndOfRange`
- 6,000 sessions at host index 8190..2191 of one bridge admitted in 21.9 s (3.6 ms/session), +185 MB kernel memory; 600 on a second bridge in 2.2 s; 300 more on top in 2.1 s.
- Sampled sessions (the last host `10.62.31.254` minor `0x3ffe`, `10.62.31.253`, host 7000, the band boundary 4096 and 4095, and the lowest): measured download/upload 2,873/2,874 of 3,000;
  1,917 of 2,000; 960 of 1,000 (three times); 3,829/3,831 and 3,833/3,831 of 4,000 kbit/s. Class counters at or above the payload read and within 12 % of it in both directions.
- No unsampled class on the bridge and no class on the second bridge counted a byte while only the sampled sessions carried traffic.
- Deleting 6,300 sessions took 16.5 s; no class or filter was left on either bridge or IFB.
- Real Kea (Debian 12 package, **Kea 2.2.0**; the appliance runs 2.0.x, so section 3 must be repeated there) on the file the product renders for `10.65.0.0/19` (`kea_slash19_lab.sh`):
  `kea-dhcp4 -t` accepts it; 8,201 distinct clients ran the full DISCOVER/OFFER/REQUEST/ACK exchange in 16.0 s (511 clients/s); 8,178 leased (the 8,177-address pool `10.65.0.10 - 10.65.31.250`
  plus the one reservation `10.65.31.254` at the very top), all unique, lowest `10.65.0.10`, highest in the pool `10.65.31.250`; the 23 clients beyond the pool got no offer and no wrong address;
  option 3 (router) `10.65.0.1`, option 51 `3600`, option 114 `http://10.65.0.1:8380/`; Kea's `lease4-get-all` and the memfile CSV (8,178 rows, 697 KB) agree; Kea RSS 20 MB empty, 27 MB with the
  whole `/19` leased; after stopping and starting Kea all 8,178 clients renewed (INIT-REBOOT) with the same address, 0 refused. Test client and namespace trap: the client must live in its own network
  namespace, otherwise the server's own address arrives as a martian source and no offer is ever received.
- `TestRealKernelSlash19NeighbourTable`: a sweep of 8,189 addresses produced 8,189 neighbour entries on the bridge and zero new `neighbor table overflow` messages (the Docker host's table limits are 8192/32768/65536).
