# Per-session shaping beyond 2,047 sessions per bridge

Status: implemented on branch `shaping/scale-beyond-2047`, verified on a real kernel (privileged container, kernel 6.6.87.2-microsoft-standard-WSL2) and against the simulated kernel.
Not pushed, not deployed, not merged. No schema, migration, API or plan-contract change. The two-NIC WAN/LAN architecture, client isolation and the IFB-per-bridge upload design are unchanged.

## 1. Root cause of the limit

Finding in `docs/reviews/NETD_ENFORCEMENT_SCALING.md` section 6: a bridge cannot shape more than 2,047 sessions. What is and is not the limit:

* **Not the HTB class id.** A classid is `major:minor` with a 16-bit minor. netd used `0x1000 + low 12 bits of the address` (4,096 hosts, i.e. a /20, which `internal/netcfg` enforced as the widest client network). That is a separate, real ceiling of 4,094 clients per network, but it is not 2,047.
* **The classifier.** Each session installed one `u32` filter per direction **at its own priority** (`pref = class minor`). Measured on the real kernel: the 2,048th `filter add` on a device parent is refused with `Error: Filter already exists` (batch line 2048; exactly 2,047 installed), identically for a plain loop and for `tc -batch`. Each distinct priority is a separate classifier instance with its own `u32` hash table; the table ids come from one small space per device (0x800..0xFFF), which is what runs out. I measured the failure; I did not trace the kernel source for the exact allocator.
* **A second, previously unrecorded cost of the same design.** Every priority is another classifier the packet walks in turn. Measured (400,000 UDP packets sprayed over the addresses, classification only): **263,000 packets/s with no filters, 16,979 packets/s with 1,900 per-priority u32 filters.** The per-packet cost was linear in the number of shaped sessions, so even below the limit a large bridge lost most of its forwarding rate to classification.

## 2. Options considered, with measurements

All on one device with an HTB root, N sessions, `tc -batch`, real kernel, 40-core host (so absolute packet rates are noisy; the shape of the result is what matters; each row is one run, the flower row was repeated and is shown as a range).

| Option | Add N=8,000 filters | Packets/s at N=1,900 / 8,000 (no filters: 263,000) | Verdict |
|---|---|---|---|
| u32, one filter per pref (the old design) | **refused at the 2,048th** | 16,979 / n.a. | The defect. |
| u32, one hash table keyed on the last address byte | 0.11 s | not measured | Works, but chains grow as N/256 (31 entries per bucket at 8,000), so it is not O(1). Not pursued. |
| u32, two-level hash (third byte, then last byte) | 0.12 s | 116,695 / 151,749 | O(1) and unlimited, but needs a table per third octet, link filters, deterministic nested handles, and tables to create before the entries in the same batch. More moving parts than flower for the same result. Kept in mind as the fallback if a kernel has no flower. |
| **flower, one filter per session at one priority, handle = class minor** | **0.15 s** | **181,833 and 134,380 / 160,129 and 126,565** | **Chosen.** One hash table per match mask: one lookup per packet whatever N; addressed by handle for add, delete and presence; no table id to size. |
| nft `meta mark` + `tc ... fw` | not built | n.a. | Rejected on design, not measured: upload classification must happen at the bridge's tc **ingress** (before the redirect to the IFB, because the WAN only sees the SNAT address); that hook runs before netfilter prerouting, so a mark set by nft is not yet there. Download could use it, upload could not, and a mark would also collide with other users of `meta mark`. |
| Several HTB parents / more class ids / IFB sharding | not built | n.a. | Not the cause (the classid space is 16 bits; the limit is in the filters), and sharding the IFB would multiply devices and change the redirect topology. |

Class and leaf creation is the dominant admission cost and is independent of the classifier: 2,000 classes+leaves in 0.33 s, 8,000 in 5.4 s per device on that run (super-linear in the kernel's class hash). The default `fq_codel` leaf is also the dominant memory cost: **73 KB per class and device** with the kernel default of 1,024 flows against **13 KB** with `flows 128` (4,000 classes+leaves on a dummy device, run twice: 73.3/73.5 KB and 13.3/12.5 KB).

## 3. Chosen design

### 3.1 Classification (`internal/shape/classifier.go`)

* Download: on the bridge's HTB root, `filter add dev <bridge> protocol ip parent 1: pref 1 handle 0x<minor> flower dst_ip <ip>/32 classid 1:<minor> skip_hw`. Upload: the same on the IFB with `src_ip`. One priority (`FlowerPref = 1`), the class minor is the handle, so a session's filter is created, deleted and tested by its class number alone. The old pref/handle bookkeeping is gone.
* Presence (`SessionForwarding`, `ForwardingBatch`) and removal read **both** forms. A dump of `tc filter show` is parsed for `flower ... handle 0x<minor>` and for the legacy `pref <minor> u32`. Removal tries the current form first and the other second, in both modes.
* **Kernel capability probe.** The first filter operation on a client adds and removes a probe flower filter (handle `0xfff`, below every guest minor). If the kernel answers "kind not found / unknown filter / unable to load classifier / not supported" the client uses the previous one-u32-per-pref scheme (limit 2,047, fail closed, as before) and logs a warning. Any other failure (device or qdisc not ready) is inconclusive: the safe u32 scheme is used for that call and the probe is repeated on the next one, so an early error never pins the fallback. `NETD_SHAPE_CLASSIFIER=u32` forces the fallback (rollback switch, section 5).
* Leaf qdisc: `fq_codel flows 128` for new classes (`LeafFlows`).

### 3.2 Class ids (`internal/shape/minor.go`)

Stateless as before (netd, acctd and a restarted process derive the same id from the address). Per bridge subnet:

| Host index in the subnet | Minor |
|---|---|
| 0..4095 | `0x1000 + index` (**identical to the previous rule** for every subnet of /20 or narrower) |
| 4096..32767 | `0x3000 + (index - 4096)` (new extension band; SHARED group aggregates keep `0x2000..0x2fff`) |

The host index needs the bridge's subnet. It is read from the **bridge's own IPv4 addresses** (`net.InterfaceByName`, cached 5 s, the narrowest configured subnet containing the address wins), so no plan, contract or database field is added and netd and acctd agree without talking. A bridge with no address, an address outside /17../19, or a session outside every configured subnet falls back to the previous rule. `MinorFor(bridge, ip)` replaces `MinorForIP(ip)` at the ten production call sites in `cmd/netd` and `cmd/acctd`; `IsGuestMinor` replaces the `GuestMinorBase..GuestMinorMax` range checks (stray sweeps, class-state restore, `ReadClasses`, delete refusal), so strays in the extension band are found and removed like any other.

`internal/netcfg` `MinSubnetPrefix` was NOT raised by this work (it stayed /20 pending a Product Owner decision). **Update 2026-10-30: the Product Owner approved /19** (8,190 usable addresses, an address-capacity limit and not a concurrency claim); it is implemented in the /19 closure work, see [`../CLIENT_NETWORKING.md`](../CLIENT_NETWORKING.md) section 1a.

### 3.3 What did not change

Accounting still reads the per-class counters (`ReadClasses`, keyed by minor); acctd now derives the minor with the bridge-aware function and nothing else about attribution changed. Stray detection and cleanup, the write-ahead journal, MAC binding, element timeouts, fail-closed behaviour, the classes-before-filters admission order (a class carries no packet until its filter is installed) and `class change` re-rating are unchanged code paths.

## 4. Tests and results

Everything below was run in this work. "Real kernel" is a privileged `oglab-base:1` container (a lab image, kernel 6.6.87.2-microsoft-standard-WSL2, 40 host CPUs; runs marked "2 CPUs" were pinned with `taskset -c 0,1`). Containers I created were removed afterwards.

### 4.1 Unit and simulated-kernel tests (Linux container and Windows)

* New: `internal/shape/minor_test.go` (every narrow subnet keeps the original minor over its hosts; every host of a /19, /18, /17 gets a distinct minor inside the guest bands and clear of the SHARED band; boundaries 4095/4096 and the /17 last host; unknown geometry and overlapping subnets; `IsGuestMinor`; a batch on a wide bridge names both bands), `internal/shape/classifier_test.go` (probe selects flower and cleans up once; kernel without flower falls back to u32; an inconclusive probe does not pin the fallback; the rollback switch; removal releases both forms; presence reads both forms from one dump per device; batch delete covers both forms and the extension band; `ReadClasses` reads the extension band and not the SHARED band), `cmd/netd/phase3_scale_beyond_test.go` (4,200 sessions on one simulated /17 bridge all active with one flower filter per direction each and the right number of extension-band classes; a kernel without flower fails closed at exactly 2,047; sessions classified by the previous netd are recognised as forwarding and released when they end), `internal/netcfg` (/19 accepted, /18 refused).
* The simulated kernel (`cmd/netd/phase3_fakekernel_test.go`) now models flower filters by handle and refuses the 2,048th u32 priority, as the real kernel does.
* Existing tests updated only where they asserted the old filter text.
* Results: see section 4.4 (the Linux run of the affected packages).

### 4.2 Real kernel, shape package (`SHAPE_REAL=1 SHAPE_REAL_N=<n> go test ./internal/shape -run TestRealKernel -v`)

A bridge with `10.60.0.1/17`, a veth port into a network namespace holding the sampled guest addresses, `shape.New()` (real `tc`), sessions installed in chunks of 32 with `PrepareBatch` + `ActivateBatch`, TCP traffic in both directions through Python sockets.

| Run | Sessions on ONE bridge | Admit all | Admit 300 more | Kernel memory | Teardown |
|---|---|---|---|---|---|
| N=4,000 (all cores) | 4,000 | 12.5 s (3.1 ms/session) | 1.32 s | +127 MB | 4,300 in 5.2 s |
| N=4,000 (2 CPUs) | 4,000 | 11.2 s | **1.20 s** | +122 MB | 4,300 in 4.8 s |
| N=8,000 (all cores) | 8,000 | 38.6 s (4.8 ms/session) | **2.32 s** | +255 MB | 8,300 in 22.1 s |

Per-session rate and accounting (N=8,000, six sampled sessions, 4 s of TCP each way; the sampled set covers the first, middle and last of the original band, the **boundary hosts 4095 (minor 0x1fff) and 4096 (minor 0x3000)** and the last host 8001 (minor 0x3f41)):

| Host index | Minor | Configured | Measured download / upload (payload) | Class counter vs payload |
|---|---|---|---|---|
| 10 | 0x100a | 3,000 kbit/s | 2,877 / 2,872 | within [1.00, 1.12] of payload, both directions |
| 2000 | 0x17d0 | 1,000 | 960 / 960 | same |
| 4001 | 0x1fa1 | 2,000 | 1,915 / 1,915 | same |
| 4095 | 0x1fff | 4,000 | 3,830 / 3,828 | same |
| 4096 | 0x3000 | 1,000 | 960 / 960 | same |
| 8001 | 0x3f41 | 2,000 | 1,915 / 1,918 | same |

Measured goodput is 4-5 % under the configured rate (HTB shapes link-layer bytes, the application counts payload); the same shortfall appears at every position, so classification is not the cause. The test asserts the class counter is at least the payload read and at most 12 % above it (headers), per direction, and that **no unsampled class counted a single byte** while the samples ran (a mis-hashed or shared match would show here). It passed for both runs above. It also requires every one of the N sessions to read as forwarding in both directions from one dump per device, and that after teardown no class (either device) and no filter is left.

`TestRealKernelLegacyU32SessionIsReleased` (real kernel): a session whose two u32 filters were installed by hand in the pre-upgrade form reads as forwarding, `RemoveClassification` releases it, it can be re-activated in the current form and aborted cleanly. Passed.

### 4.3 Real kernel, full netd pass (`NETD_SCALE_REAL=1 NETD_SCALE_N=4000,8000 NETD_SCALE_BRIDGES=1 go test ./cmd/netd -run TestScaleTable -v`, 2 CPUs)

The real `reconcileLocked`/`provisionSession`, real `shape` and `nft` clients, a real bridge with a /17 address, simulated database latency (the harness's, unchanged). One bridge, no failures, every session active:

| N | Scenario | execs | wall | Result |
|---|---|---|---|---|
| 4,000 | steady | 36 | 2.8 s | 4,000 shaped, 0 failed |
| 4,000 | renew_due / reprove_due | 68 / 68 | 5.2 s / 5.2 s | 0 failed |
| 4,000 | arrive300 | 144 | 9.8 s | 300 of 300 promoted in the pass, 0 failed |
| 8,000 | steady | 28 | 4.3 s | 8,000 shaped, 0 failed |
| 8,000 | renew_due / reprove_due | 132 / 132 | 18.9 s / 19.4 s | 0 failed |
| 8,000 | arrive300 | 180 | 25.5 s | 256 of 300 in the first pass, the rest in the following passes; 0 failed |

(The admit rows stop at the harness's first-pass budget; the harness then drives to all-active, and it did: `TestScaleTable` passed for both sizes.) At 8,000 the renew/reprove cost is nft (130 of 132 processes), not tc: the nft set rewrite is unchanged by this work and is the next cost to look at if a bridge is run to 8,000 (section 6). The comparison harness at N=200 on the **base commit** and on this change gave the same admit/steady/renew wall times (1.3 s / 37 ms / 114 ms against 1.2 s / 46 ms / 128 ms) and the same fork counts (110 vs 112 for admit).

### 4.4 Linux run of the affected packages

`golang:1.26.10`, `go test ./cmd/netd ./cmd/acctd ./internal/shape ./internal/netcfg ./internal/nft ./internal/shapeplan ./internal/shapeproducer`: all seven packages `ok` (`cmd/netd` 127.6 s including the new 4,200-session simulated tests). On the Windows development host the same packages fail only the pre-existing directory-`fsync` tests in `cmd/netd` and `cmd/acctd` (`Access is denied`), which pass on Linux.

## 5. Compatibility, upgrade and rollback

* **Class ids do not change** for any network of /20 or narrower (asserted over the subnet's hosts for six subnet shapes in `TestNarrowSubnetsKeepTheOriginalMinors`). Existing classes, counters, the class-state journal and acctd's attribution survive the upgrade untouched.
* **Existing filters are not rewritten.** A session classified by the previous netd keeps its two u32 filters until it ends; it reads as forwarding (presence parses both forms), steady passes do nothing to it, and it is released by the normal teardown (flower delete fails with "not found", the u32 delete follows). New sessions get flower filters. During that period both kinds exist at different priorities (flower at 1, u32 at the class minors), which is harmless: a legacy session and a new one never share an address. Verified on the real kernel (`TestRealKernelLegacyU32SessionIsReleased`) and in the simulated pass (`TestSessionsFromThePreviousClassifierAreReleasedAndReplaced`).
* **fq_codel flows**: new classes get `flows 128`; existing classes keep their 1,024 until they end. Re-rating (`class change`) never touches the leaf.
* **Rollback, same binary:** set `NETD_SHAPE_CLASSIFIER=u32` in netd's environment and restart. New sessions use u32 (limit 2,047, fail closed); sessions already classified by flower keep working and are released at their end (removal tries both forms). Nothing is lost.
* **Rollback to the previous binary:** the previous netd does not know flower filters. It would read flower-classified sessions as not forwarding, re-activate them (adding u32 filters that classify to the same class: harmless), and at teardown fail to delete their class because a flower filter still points at it (`EBUSY`), leaving strays. **Before downgrading, remove the flower filters on every managed bridge and IFB:** `tc filter del dev <bridge> parent 1: pref 1` and `tc filter del dev ifb-<name> parent 1: pref 1`. Guests then pass unshaped and uncounted through the default class until the old netd re-activates them in its next pass. This downgrade path was reasoned about, **not run** with the old binary.
* **Reconcile and boot recovery:** unchanged mechanism. Strays are classes on a managed bridge that the plan does not claim (`IsGuestMinor` covers both bands); a stray class is removed with its filter in the same batch (filters first); presence is read from the kernel, never from memory, on every pass and after every restart; a class lost behind netd's back is a new series, not a renewal.
* **Subnet change:** if a bridge's address (hence its subnet geometry) changes, the host-index rule changes with it after at most 5 s per process. Classes already installed for hosts whose minor moved are then strays and are re-created, which is the same path as any network re-addressing today.

## 6. Limits that remain

1. **Widest subnet:** the shaper names classes up to /17 (32,766 hosts); the product rule is /19 (8,190 clients). Anything wider than /17 keeps the original 12-bit rule and aliases, so `internal/netcfg` must keep refusing it.
2. **Cold admission is super-linear in the kernel's class count:** 4,000 sessions 11-12 s, 8,000 sessions 38.6 s (class and leaf creation on both devices; independent of the classifier). Steady state is unaffected. Arrival of 300 on top of 8,000 took 2.3 s in the shape test.
3. **Teardown of the whole population is slow at 8,000** (22 s for 8,300). It is a bulk operation (appliance reset, network removal), not a per-session cost.
4. **Memory:** about 32 KB of kernel memory per session (both devices, `flows 128`), 255 MB at 8,000; with the old leaf default it would be about 1.2 GB.
5. **Class dumps still grow with N** (`tc -s class show` per device per pass; unchanged by this work). At 8,000 the netd pass is dominated by nft (130 of 132 processes in renew/reprove), not tc. A single bridge at 8,000 is therefore fine for tc and a separate nft-scaling question (set rewrite per 256 leases).
6. A packet-rate figure for the classifier is a noisy lab measurement (ranges above); it shows the absence of growth with N, not a forwarding capacity for the appliance.

## 7. What could not be verified

* **The appliance's kernel.** I had no access to the test appliance (172.21.96.97) and did not touch any lab container. `cls_flower` is present in the Ubuntu kernel packages the appliance uses as far as I know, but I did not confirm it there. The fallback to u32 on an unsupported kernel is exercised against a simulated refusal only; the exact wording of a real "no cls_flower" refusal is a pattern guess (`kind not found | unknown filter | unable to load classifier | not supported`), and an unrecognised wording would make the probe inconclusive every time (safe: u32 for that call, nothing pinned, but also never flower). Run `tc filter add ... flower` once on the appliance before deploying.
* The downgrade to the previous binary (section 5), by test.
* Several bridges at once at 4,000-8,000 each; a real NIC or VLAN-tagged port (the lab used a veth into a namespace); the Kea pool and DHCP behaviour of a /19 (nothing in the DHCP code was changed or exercised for this).
* The comparison with the previous classifier is on a dummy device and on the harness; I did not run the old binary at 4,000 (it cannot pass 2,047).

## 8. Decision required

`MinSubnetPrefix` /20 to /19 (commit "netcfg: allow client networks up to /19"). Without it nothing above 4,094 clients per network can be configured, so the 8,000-session case is reachable only in the lab. The shaper work does not depend on it and works without it for 2,048-4,094 sessions per network. It is a product limit on broadcast-domain size (ARP/broadcast load of an 8,190-host segment), so it was the Product Owner's call. **Decided 2026-10-30: /19 approved.**

## 9. Files

* `data-plane/internal/shape/classifier.go`, `minor.go` (new); `shape.go`, `batch.go` (bridge-aware ids, classifier selection, `flows 128`).
* `data-plane/cmd/netd/phase3_batch.go`, `phase3_shaping.go`, `phase3_classstate.go`, `data-plane/cmd/acctd/phase3_accounting.go`: only the `MinorForIP` to `MinorFor(bridge, ...)` and range-check call sites (ten lines).
* `data-plane/internal/netcfg/validate.go` (separate commit).
* Tests: `internal/shape/minor_test.go`, `classifier_test.go`, `realkernel_test.go`, `cmd/netd/phase3_scale_beyond_test.go`; `phase3_fakekernel_test.go` and `phase3_scale_test.go` (flower in the simulated kernel; the real-kernel bridge gets a /17 address).
