# Phase 4: Enforcement path (netd)

**Audited:** master `653363d7`. Code and repository only: nothing was run against PRE-LIVE.

**Covers:**
- how a guest's access is put into the kernel, kept there and taken out:
  - acctd's shaping plan;
  - netd's pass (`cmd/netd/phase3_shaping.go`, `phase3_provision.go`, `phase3_gate.go`);
  - the nftables sets (`internal/nft`, `internal/netcfg/render_nft.go`, `internal/nftconverge`);
  - the tc shaping (`internal/shape`);
  - connection tracking (`internal/conntrack`).
- The items earlier phases handed on:
  - from Phase 2, the cadence that sets how long a sign-in waits for enforcement (D2-L4);
  - from Phase 3, the redirect of port 443.

**How:**
- **Code:** the code was traced.
- **Kernel measurements:** a throwaway privileged container (Debian bookworm, nftables 1.0.6, iproute2 6.1, iperf3 3.12), limited to 2 CPUs and 4 GB, on the workstation's kernel (WSL2 6.6). Network namespaces reproduced netd's layout exactly:
  - one HTB class per device and one u32 filter per device, each filter in its own priority (`internal/shape/shape.go:412, 458`);
  - the authorised set `phase3_auth_ipv4` as `ifname . ipv4_addr` with timeouts (`render_nft.go:89`).
- **What was measured:**
  - `tc` and `nft` command costs at 500, 2,000 and 5,000 devices;
  - the packet rate through the filters;
  - connection-tracking dumps with 20,000 generated flows;
  - the kernel's limit on the number of filters.
- **Caveat:** process-start costs on WSL2 are not the appliance's. Phase 11 repeats the key numbers on a reference VM.
- **Compliance:** checked against the [README](README.md#compliance) rules.

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 3 | 3 | 3 | 1 |

**Main result.** Enforcement is correct and fails closed, but it does not scale to the design load. Three of its structures grow with the number of devices.

1. **A hard limit on one guest network.** The kernel accepts only **2,047** per-device filters in netd's layout, but netd's addressing expects up to 4,096. From the 2,048th device, a guest cannot be enforced, so it cannot get online (D4-H1).
2. **The 1-second pass takes about a minute at 2,000 devices.** Every second, netd starts two `tc` processes per online device, makes a database call per device, and dumps the whole authorised set for every lease it renews. An estimated pass, built from measured parts, takes about **75 seconds** at 2,000 devices. That is close to the 90-second lease that keeps guests online, and far beyond the 8 seconds a new sign-in waits before giving up (D4-H2).
3. **Every packet walks the filter list.** At 2,000 devices, the device at the end of the list gets **14,700 packets a second against 454,000** with one filter. A hashed filter table measured **516,000** at 2,000 devices and 476,000 at 4,000 (D4-H3).

**All three have well-known fixes**, measured here or standard in the kernel tools:
- a hashed filter table;
- work only on what changed;
- one batched kernel transaction per pass instead of thousands of processes.

**Already right:**
- authorisation is an O(1) set lookup, not per-device rules;
- every lease expires on its own, so a stopped netd fails closed;
- ended sessions are torn down once and then skipped;
- signed-in devices bypass the portal redirect entirely.

## Measured costs

| Operation (2 CPUs) | 500 devices | 2,000 devices | 5,000 devices |
|---|---|---|---|
| One `tc class change` process | 3.7 ms | 5.4 ms | |
| One pass of re-rates, as netd does it (2 per device) | 3.7 s | **21.5 s** | |
| `tc -batch` of all classes and filters, one process | 51 ms | 801 ms | |
| `tc -s class show` (one interface) | 5 ms, 113 KB | 21 ms, 450 KB | |
| `nft -j list set phase3_auth_ipv4` | 7 ms | 21 ms | 50 ms |
| `nft -j list table inet stayconnect` | 8 ms | 21 ms | 50 ms |
| `nft add element` (one, own process) | 4.0 ms | 3.8 ms | 3.8 ms |
| Packet rate, last device, netd's linear filters | 57,000/s | **14,700/s** | not installable (D4-H1) |
| Packet rate, last device, hashed u32 table | | **516,000/s** | 476,000/s (4,000) |
| Packet rate with one filter (reference) | 454,000/s | | |
| Connection-tracking full dump, 20,000 flows | 103–112 ms | | |

## Findings

### D4-H1: One guest network cannot enforce more than 2,047 devices (High, Measured)

**Where:**
- **The filters:** `internal/shape/shape.go:458`, `addFilter`, installs `u32 match ip dst|src <ip>/32` with `pref` equal to the device's class minor. So each device's filter is its own u32 instance, with its own hash table.
- **The addresses:** `MinorForIP` (`shape.go:58`) gives minors 0x1000–0x1FFF, room for 4,096 devices per bridge.

**Measured.** On a single interface with netd's layout, filters 1 to 2,047 install. The 2,048th fails with `Error: Filter already exists`. The same holds for the upload interface (IFB).

This is the kernel's u32 classifier: each new u32 instance takes a hash-table id from a range of 0x7FF per qdisc, and that range is exhausted at 2,047.

**Problem.**
- From the 2,048th concurrent device on one guest network, `ActivateSession` fails.
- The session is never proven active and is quarantined (`cmd/netd/phase3_enforcement.go:108-135`).
- The guest's sign-in waits and fails (Phase 2 D2-L4).

**Impact.**
- **A hotel with one guest network** and more than 2,047 devices online at once, which is below the 5,000 stress load, cannot admit any more devices.
- **PRE-LIVE's licence allows 2,000**, close to the limit.
- **Hotels with several guest networks** have the limit per network.

**Recommended fix.** Classify with one hashed table instead of one filter per device (D4-H3's fix). That has no per-device instance, so the limit disappears; it was measured with 4,000 devices.

**Proof.**
- On a reference VM, 4,096 devices install on one bridge.
- The 4,096-device activation test passes in both directions.

**Compliance.** None beyond availability. The fail-closed behaviour stays: a device that cannot be shaped is not admitted.

**Batch:** B1, with D4-H3. CONTROLLED (guest enforcement core).

### D4-H2: The enforcement pass does per-device process and database work every second (High, Measured parts, estimated total)

**Where:**
- **The plan:** acctd submits the full plan every tick, 1 s by default (`cmd/acctd/main.go:178, 201`). netd walks it all, under one mutex (`phase3_shaping.go:154, 202`).
- **For every active device, on every pass:**
  - two `tc class change` processes, one per direction, **even when nothing changed** (`phase3_provision.go:150-155`, `shape.go:306-325`);
  - one database call, `iam_v2.activate_session_enforcement`, through `proveActive` (`phase3_provision.go:162`, `phase3_enforcement.go:108`). It is idempotent, but it runs every second.
- **For every lease under 60 s** of its 90 s (`phase3_lease.go:16, 60`): `LeaseIn` (`internal/nft/nft.go:116-138`) **dumps the whole authorised set** to check membership, then starts another process for the delete-and-add. It ignores the set snapshot the pass already took.
- **For every new device:** about 25 processes and 3–4 database calls, including two more full set dumps (`phase3_provision.go`, `phase3_gate.go`).

**Estimated pass at 2,000 devices,** from the measured parts:

| Part | Time |
|---|---|
| 4,000 `tc class change` processes | about 21.5 s |
| 2,000 `activate_session_enforcement` calls, at about 3 ms (Phase 1 D1-H2's measured call cost) | about 6 s |
| Renewals. A pass longer than 30 s finds every lease under 60 s, so all 2,000 are renewed, each with a 21 ms dump plus a 4 ms command. | about 50 s |
| **Total** | **about 75 s for a pass meant to run every second** |

**Impact.**
- **New sign-ins fail at the design load.** A new device is activated only when the pass reaches it, up to a full pass later. scd's `awaitEnforced` gives up after 8 s (Phase 2 D2-L4), so the guest sees a failed sign-in, often with access arriving a minute later.
- **Guests are cut off in batches.** A guest stays online only if its 90 s lease is renewed before it runs out. When a pass takes longer than about 90 s, whether from more devices, a burst of revocations (D4-M1) or a slow disk, leases expire mid-pass and those guests lose access until the pass reaches them. That happens at about 2,400 devices by this estimate, and below that under any extra load.
- **Speed changes and expiries land a minute late.** acctd's 1 s timer and the 10 s HTTP timeout toward netd mean acctd's own loop also waits on netd (Phase 1 D1-H2 compounds it).
- **CPU:** process starts take most of a CPU continuously.

**Recommended fix.**
1. **Do only what changed.**
   - Keep the rate last applied per class, and run `class change` only when the plan's rate differs.
   - Call `activate_session_enforcement` only for sessions not yet proven.
2. **Use the pass's set snapshot for every membership question** (`snapshotGate` already takes one). That removes the per-renewal and per-activation dumps.
3. **Batch the kernel work per pass:**
   - one `nft -f -` transaction holding every add, renewal and delete of the pass;
   - one `tc -batch` for classes and filters, or netlink.

   The measured gap is 801 ms for all 2,000 classes and filters in one process, against 21.5 s for 4,000 separate changes.
4. **Give new activations their own fast path.** When acctd's plan contains a session not yet installed, install it first, before renewals and re-rates, so sign-in waits about 1 s and not a full pass.
5. **Skip a plan identical to the last one** (D4-M2), keeping a full reconcile every 30 s as the safety net.

**Proof.** On a reference VM with 2,000 generated sessions:
- a steady-state pass under 300 ms;
- a new session active within 1.5 s of its plan entry;
- no lease falling below 30 s remaining;
- with 5,000 sessions, a pass under 1 s.

**Compliance.** These are security controls (GDPR Art. 32): only entitled devices forward, and leases expire on their own. The fix keeps both:
- the lease length and the fail-closed expiry are unchanged;
- one nft transaction is still atomic, so no guest is briefly unauthorised during a renewal;
- skipping unchanged re-rates does not change who is allowed or at what rate.

**Batch:** B2. CONTROLLED (guest enforcement core).

### D4-H3: Every packet is matched against the device filters one by one (High, Measured)

**Where:** `internal/shape/shape.go:458`. Each device's u32 filter has its own priority, so the kernel tries them in order for every packet:
- on the bridge, for download;
- on the IFB, for upload (`shape.go:154-163`).

**Measured:** UDP 64-byte packets to the device whose filter is last, on 2 CPUs:

| Filters | Packets a second |
|---|---|
| 1 | 454,000 |
| 500 | 57,000 |
| 2,000 | 14,700 |
| 2,000, hashed table | 516,000 |
| 4,000, hashed table | 476,000 |

**Impact.**
- **The cost per packet grows with the number of devices online,** and is paid in the kernel's packet path on the same 2 CPUs everything else uses.
- **At 2,000 devices,** an average device's packets each pass about 1,000 filters, twice in a round trip (once each way). The two CPUs then carry a few tens of thousands of packets a second in total, roughly 200–400 Mbit/s for the whole hotel at typical packet sizes.
- **The CPU spent there is taken from** scd, acctd and Postgres.

**Recommended fix.** Classify with a hashed table, keyed on the address's last byte, with the per-device entries in its buckets. Two options:
- **A u32 hash table**, measured above: one `divisor 256` table, one linking filter, and the device entries placed by their last byte.
- **An nftables map from address to class**, so nftables, which already looks the address up in `phase3_auth_ipv4`, sets the packet's priority and HTB uses it directly.

Benchmark both on the reference VM and keep the faster.

**Proof.** On the reference VM, packet rate to the last of 4,096 devices is within 10% of the rate with one device.

**Compliance.** None beyond availability. Speed limits per device and per shared allowance stay exactly as configured.

**Batch:** B1. CONTROLLED (guest enforcement core).

### D4-M1: Revoking a device reads the whole connection-tracking table (Medium, Measured)

**Where:**
- `internal/conntrack/netlink_linux.go:16`, called from `flushingGate.Revoke` (`cmd/netd/phase3_gate.go:117`) for every revoked address.
- It dumps the **entire** conntrack table over netlink, filters it in Go, then deletes the matches.
- Its fallback runs `conntrack -D` three times per address (`conntrack.go:58-90`).

**Measured:**
- a full dump of 20,000 flows took 103–112 ms;
- a kernel-filtered delete of one source address took 49 ms, using the same tool.

**Impact.**
- **Estimate at the design load:** at about 50 flows per device, 2,000 devices hold about 100,000 flows, so each revoked device costs about 0.5 s of dumping.
- **At a check-out peak of 5 revocations a second,** that is 2.5 s of work per second, added to the pass of D4-H2.

**Recommended fix.**
1. **Collect a pass's revoked addresses and flush them with one dump**, matched against that set.
2. **Better:** filter in the kernel. Either mark each authorised device's flows at authorisation and flush by mark (ctnetlink filters by mark during the dump), or use the kernel's tuple filter.

**Proof.** With 100,000 generated flows, revoking 50 devices in one pass costs under 300 ms.

**Compliance.**
- **Revocation must still end existing connections.** The forward chain accepts established flows before checking the authorised sets (`render_nft.go`), so the flush is what stops a revoked or expired guest's open connections.
- **The fix keeps it complete:** every flow of a revoked address is removed (GDPR Art. 32; product correctness).

**Batch:** B3. CONTROLLED (guest enforcement core).

### D4-M2: acctd sends the whole plan every second, and netd never skips an unchanged one (Medium, Confirmed)

**Where:**
- **The plan's contents:** `internal/enforce/enforce.go:114-125` builds it from every live session plus every session ended in the last hour.
- **The submission:** acctd POSTs it every tick (`cmd/acctd/phase3.go`, 10 s client timeout).
- **What netd does with it** (`phase3_shaping.go:154-180`):
  - it gives each submission a new generation;
  - it never compares content with the last plan;
  - it walks every session;
  - it saves the accepted plan to disk.

**Impact.**
- **All of D4-H2's work repeats every second** whether anything changed or not.
- **The plan's size includes the last hour's ended sessions:** at a check-out peak, hundreds of entries netd only skips.
- **Each submission writes netd's plan file to disk once a second.**

**Recommended fix.**
1. **Hash the plan's enforcement-relevant content.** netd accepts an identical plan as a renewal of validity only, with no walk and no write.
2. **List ended sessions only until netd has acknowledged their teardown.**

**Batch:** B2.

### D4-M3: Shared-allowance groups collide by chance and are then refused (Medium, Confirmed)

**Where:** `internal/shape/shape.go:75-95`, `GroupMinorFor`.
- **How a group gets its class:** a SHARED allowance's group class is a 32-bit FNV hash of the entitlement id, reduced to 4,096 slots (0x2000–0x2FFF).
- **What happens on a collision:** the applier refuses the second group rather than pool two accounts.

**Problem.** Collisions follow the birthday bound. With *k* SHARED entitlements live on one bridge, the chance that at least one pair collides is about 1 − e^(−k²/8,192):

| SHARED entitlements live | Chance of at least one collision | Expected refused |
|---|---|---|
| 50 | 26% | 0.3 |
| 100 | 70% | 1.2 |
| 500 | > 99.99% | 30 |

**Impact.**
- **Opt-in only:** SHARED is off by default (`migrations/0059_speed_allocation.up.sql:26` defaults to `PER_DEVICE`).
- **But common in practice:** a hotel that sells shared room packages (one allowance for a room's devices) will, at 500 occupied rooms, see about 30 rooms whose devices cannot be enforced, and so cannot get online.

**Recommended fix.** Allocate group classes from a table: a durable map from entitlement to minor, kept by netd, freed on teardown. It is collision-free up to 4,096 groups and can still be named after a restart.

**Batch:** B4, with the backend audit's Phase 8 B4 (the shared-allowance re-rate). CONTROLLED.

### D4-L1: Class counters are dumped four times a second per guest network (Low, Measured)

**Where:**
- acctd reads `tc -s class show` for the bridge and its IFB every tick (`internal/shape/shape.go:494`, `cmd/acctd/phase3_accounting.go:107`).
- netd's stray scan reads the same two each pass, and `readClassesForPass` reads them separately again.

**Measured:** 21 ms and 450 KB per dump at 2,000 classes, then parsed with regular expressions.

**Impact.** About 0.1 s of CPU per second per guest network for the same data. It grows with devices.

**Recommended fix.**
- Read once per pass and share the result within netd.
- Use netlink class statistics instead of parsing text.

**Batch:** B2.

### D4-L2: The structure check dumps the whole nftables table every 10 seconds (Low, Measured)

**Where:** `internal/nftconverge/converge.go:152`, `ReadLive`, from `cmd/netd/nft_watch.go:59`. It runs `nft -j list table inet stayconnect` with every authorised element, then compares a fingerprint.

**Measured:** 21 ms at 2,000 devices, 50 ms at 5,000.

**Recommended fix.** Read the marker set's comment (one small `list set`) and the chain and set declarations without their elements (`nft -t`, terse).

**Batch:** B2.

### D4-L3: Guest networks wider than /20 alias device classes (Low, Confirmed)

**Where:** `internal/shape/shape.go:58`, `MinorForIP`. It uses the address's low 12 bits, so on a guest subnet wider than /20 two devices can map to one class. Both are then refused with "class conflict" (`phase3_shaping.go:262`).

**Impact.** Only for subnets wider than 4,094 hosts. Such a network would hit D4-H1 first today.

**Recommended fix.**
- Refuse a guest subnet wider than /20 when it is configured, with an explanation. That also belongs to the backend audit's network-config validation (Phase 8 M9).
- Or allocate minors from a table, as in D4-M3.

**Batch:** B4.

### D4-CC1: Every kernel operation is a separate process (Clean code, Confirmed)

**Where:**
- `internal/shape/shape.go:45` (`realExec`) and `internal/nft/nft.go:38, 243` (`run`): one `tc` or `nft` process per command.
- There is no batch interface and no netlink client, except the conntrack one.

**Recommended fix.** One small kernel-operations layer per tool, with:
- a batch for nft (`nft -f -`);
- a batch or netlink for tc;
- a per-pass snapshot.

That turns D4-H2's fixes into one change, and makes the cost of a pass measurable in one place.

**Batch:** B2.

## Items from earlier phases

| Item | Outcome |
|---|---|
| Phase 2 D2-L4: how long a sign-in waits for enforcement | There is no 1 s producer cadence at the design load. Activation waits for netd's pass, about 75 s at 2,000 devices (D4-H2), so `awaitEnforced`'s 8 s limit fails the sign-in. D4-H2's fast path fixes it; D2-L4's NOTIFY then removes the polling. |
| Phase 3: should 443 be redirected? | While the portal's HTTPS is on, unsigned devices' 443 is redirected to portald (`render_nft.go:262-265`); with HTTPS off it is dropped (`:232`). The redirect is what lets HTTPS-only captive checks reach a valid certificate, so it stays. The cost is the refused handshakes measured in Phase 3 (D3-L3). **No change recommended.** Phase 3's reference has been corrected to the rendered ruleset. |
| Backend audit Phase 8 M4: shared re-rate | Not re-audited. D4-M3 is a different shared-allowance defect, in the same batch. |

## Handed on

| To | What |
|---|---|
| Phase 5 | acctd's side of D4-M2: the plan build and the 10 s blocking call inside acctd's tick. |
| Phase 10 | CPU of process starts and of packet classification in the appliance budget. |
| Phase 11 | Repeat D4-H1, D4-H2's parts, D4-H3 and D4-M1 on a reference VM with the appliance's kernel. |

## Batches

| Batch | Findings | Notes |
|---|---|---|
| **B1** | D4-H1, D4-H3 | Hashed classification. **Do first.** It lifts the 2,047 limit and the per-packet cost together. CONTROLLED. |
| **B2** | D4-H2, D4-M2, D4-L1, D4-L2, D4-CC1 | Work only on change, batch the kernel work, add the activation fast path. CONTROLLED. |
| **B3** | D4-M1 | Batched or kernel-filtered conntrack flush. CONTROLLED. |
| **B4** | D4-M3, D4-L3 | Table-allocated classes, with the backend audit's Phase 8 B4. CONTROLLED. |

All four are networking and enforcement changes, so each needs the Product Owner's authorisation (CLAUDE.md §0) and a real-kernel test run before deployment.

## Compliance

| Finding | Rules | What the fix must respect |
|---|---|---|
| D4-H2, D4-M2 | GDPR Art. 32; the product's fail-closed lease contract | Leases keep their length and expire on their own. Renewals stay atomic (one transaction). Skipping work never extends a guest past their entitlement. |
| D4-M1 | GDPR Art. 32 | A revoked or expired guest's open connections are still ended, completely. |
| D4-H1, D4-H3, D4-M3, D4-L3 | Availability (Art. 32(1)(b)); the speed contract the hotel sold | Each device and shared group keeps exactly its configured rate. A device that cannot be shaped is still refused, never admitted unshaped. |
| D4-L1 | ePrivacy Art. 6 | The class counters are traffic data. Reading them less often changes nothing about what is stored (Phase 1 D1-H3 governs that). |
| All | GDPR Art. 5(1)(c) | No fix adds logging of guest addresses beyond what netd logs today. Kernel test data was generated. |

## Tests run

| Test | Result |
|---|---|
| `tc` setup in one batch, 500 and 2,000 devices; 500 and 2,000 separate `class change` processes; `tc -s class show` | Measured costs table |
| Filter limit: netd's layout, 4,096 attempted on one interface | 2,047 installed; the 2,048th refused |
| Packet rate, UDP 64 bytes, 8 s, last device: 1, 500, 2,000 linear filters; 2,000 and 4,000 hashed | D4-H3 |
| `nft -j list set`, `nft -j list table`, single `add element` at 500, 2,000, 5,000 elements | Measured costs table |
| Connection tracking: 20,000 generated flows; three full dumps; one source-filtered delete | D4-M1 |
| Code trace of acctd's plan, netd's pass, provision, gate, lease, conntrack and structure loop | D4-H2, D4-M2, D4-M3, D4-L1 to D4-L3 |

**Cleanup.**
- **Docker:** the privileged test container was removed with its volume (`docker rm -f -v`). Docker is back to its state before the phase: no audit containers and 16 volumes.
- **Image:** the Debian image was already present and was kept.
- **Tools:** the network namespaces and installed tools existed only inside the removed container.
