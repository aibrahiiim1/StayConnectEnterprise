# Phase 8: Networking (netd)

**Audited:** master `b00410b5`, on `delivery/backend-audit`. Code only: nothing was run against PRE-LIVE.

**Scope:**
- `data-plane/cmd/netd`
- `internal/netcfg`, `nft`, `nftconverge`, `nftfoundation`, `conntrack`, `sysnet`, `kerneltest`, `healthdir`
- `deploy/sysctl/99-stayconnect.conf` and `deploy/systemd/stayconnect-netd.service`
- The netd half of the `internal/shape` re-rate

**Also checked, as earlier phases asked:**
- Per-source connection limits for the portal (Phase 5 M1)
- IPv6 and client isolation on guest networks (Phase 5 L1)
- The shared-allowance re-rate (Phase 6 M4)
- Shaping input validation on the netd side (Phase 6 L12)

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 1 | 10 | 14 | 4 |

**netd's core design is careful:**
- The firewall is replaced in one atomic step, and live authorizations are carried across the replace.
- Each guest-network change has a confirm window and a rollback.
- Callers on the socket are identified by kernel peer credentials, and the gate fails closed.
- Phase-3 grace times use boot-relative time, and the activation journal is written durably.
- The gateway-only boundary for guest input is stated as explicit rules.

**The main problems are:**
1. **The weakest point is input.** One free-text field, the network name, is written straight into the firewall script. An admin can use it to add firewall rules. A real kernel accepted such a rule.
2. **Several recovery paths report success when they failed.**
3. **What a guest can reach is wider than it looks:**
   - hotel 10.x networks are not blocked;
   - access is tied to the IP address, not the device;
   - open connections outlive an expired authorization.

## Earlier phases' questions, answered

| Item | Answer |
|---|---|
| **Portal per-source limits (Phase 5 M1)** | **None at the network layer. Confirmed.** The portal accepts (`render_nft.go:154-158`, `:164-167`) have no `ct count` and no rate limit. One device can open as many connections to portald as it likes. The only per-device limit is the pre-sign-in DNS limiter (`:96-100`, `:117`), which is the pattern to copy. See M1. |
| **IPv6 on guest networks (Phase 5 L1)** | **Sound.** IPv6 from a guest bridge is dropped in the input chain, and IPv6 to or from a guest interface is dropped in the forward chain, both before the established accept (`render_nft.go:142`, `:176-177`). Router advertisements and neighbour discovery from guests never reach the appliance. Unbound runs with `do-ip6: no`, and the portal publishes no AAAA record. Two small gaps are in L2. |
| **Client isolation (Phase 5 L1)** | **Guests cannot reach each other through the appliance, whatever the setting.** `ip daddr @guest_subnets drop` applies to every guest network (`:209`). For routed traffic the "client isolation" switch changes nothing; it only adds a rule that matches the same packets earlier (`:191-195`). Guests on the same VLAN reach each other at layer 2 through the switch or access point, which the appliance never sees. Isolation there depends entirely on the access point. See L1. |
| **Shared-allowance re-rate (Phase 6 M4)** | **Confirmed on a real kernel. Now M4 of this phase.** The kernel accepts the re-rate without error. The session class stays under its group, but its guaranteed rate is raised from its floor to the whole group allowance: 800 Kbit becomes 8 Mbit under an 8 Mbit group. In HTB a class sending within its own rate never borrows from its parent, so every device on a shared allowance can use the full allowance at once. The Phase 6 alternative, that the kernel refuses the change, does not happen. |
| **Shaping validation (Phase 6 L12)** | Still open on the netd side. One more defect is in L6: on a class clash, a third session with the same class number gets through. Fix with Phase 6 batch B5. |

## High

### H1: A network name can inject rules into the firewall (Confirmed)

**Where.**
- `internal/netcfg/render_nft.go:211-214` writes `comment "internet disabled for %s"` using `n.Name`. This is emitted for every network with internet access off.
- `internal/netcfg/validate.go:37-39` checks only that the name is not empty.
- edged stores the name as given: `cmd/edged/resources_network.go:233-259` and `resources_network_replace.go:76-92`. Any role with the `network` permission can set it.

**Problem.** The name is not escaped. A name containing `"` ends the comment, and `;` begins a new statement. The pre-apply check `nft -c` (`cmd/netd/apply.go:240`) only checks syntax, so a well-formed injected rule passes it.

**Evidence.**
- In a throwaway Linux container, a network named `Lobby"; iifname @guest_interfaces accept comment "x` rendered and passed `nft -c`.
- It also loaded into a real kernel, which listed `iifname @guest_interfaces accept` as a rule of the forward chain.
- The injected rule sits after the inter-guest and management drops but before the sign-in accepts. With it, every guest has internet without signing in or paying.
- `}` can presumably close the chain and open a new one, for example an input chain that admits guests to SSH. That was not tried.

**Impact.**
- **Deliberate use.** An admin can bypass the captive portal and payments for every guest, or open appliance services to guests. The firewall is the one boundary edged does not otherwise let an admin change.
- **Accidental use.** A name such as `Lobby "VIP"` makes every apply of every network fail at `nft -c`. The operator sees a syntax error that names no field.

**Recommended fix.**
1. Never write free text into the ruleset. Use the bridge name, which is generated, in comments.
2. Validate the name in `netcfg.ValidateOne` and in edged: a length cap, and no control characters, quotes or backslashes.
3. Validate `BridgeName` against the form `BridgeNameFor` produces (`^br-g[0-9]{1,4}$|^br-g-[0-9a-f]{8}$`). The same goes for every other value the renderers write without escaping (M9).
4. Add a render test: for a hostile value in each free-text field, the rendered ruleset has the same rule count as for a plain value.

**CONTROLLED** (networking). The change itself is code only.

## Medium

### M1: The portal has no per-device connection or rate limit at the firewall (Confirmed)

**Where.** `internal/netcfg/render_nft.go:154-158` and `:164-167`.

**Problem.** One device can open unlimited connections to portald's HTTP and TLS listeners, and portald itself has no body or idle limits (Phase 5 M1). A single guest can exhaust portald's file descriptors or memory, and then nobody can sign in.

**Recommended fix.**
- Add a per-source limit to each portal accept, following the DNS limiter: a dynamic set keyed on `ip saddr` with `ct count over N` for open connections, plus `limit rate over R/second burst B` on new connections. Count both refusals.
- Under §0C these are operational limits, so they become settings with defaults (for example 64 connections and 20 new per second), a unit, validation and an audit record.

**Test.**
- A render test.
- A kernel test in which the guest namespace opens N+1 connections and the last is refused.

**CONTROLLED** (networking). Do it with Phase 5 batch B1.

### M2: Guests can reach hotel networks in 10.0.0.0/8 and other private ranges (Possible, depends on the site)

**Where.**
- `internal/netcfg/model.go:111-118`: `MgmtCIDRs` is fixed at `172.16.0.0/12` and `192.168.0.0/16`, and `main.go` cannot change it.
- `render_nft.go:206-208`.

**Problem.** Guests are blocked only from those two ranges, from their own subnets and from other guest subnets. The WAN accept for a signed-in guest (`:216`, `:218`) allows any other destination. That includes 10.0.0.0/8, 100.64.0.0/10 (carrier NAT), and 169.254.0.0/16 (link-local, including cloud metadata addresses). Before sign-in, walled-garden addresses in those ranges are also reachable.

**Impact.**
- On a site whose hotel network uses 10.x, a signed-in guest reaches that network (PMS, POS, printers, management) unless the upstream firewall blocks it.
- Phase 7 M1 depends on this: an unencrypted FIAS link can be attacked by anyone who can reach it.
- PRE-LIVE's addresses fall inside 172.16.0.0/12 and are blocked.

**Recommended fix.**
- Block all of RFC 1918, 100.64.0.0/10 and 169.254.0.0/16 by default.
- Make exceptions an explicit setting: an allow list of addresses or ranges, with audit, under §0C.
- Keep the walled-garden and custom-DNS accepts ahead of the block, as now.

**Test.** A render test that each range is dropped, plus a kernel test.

**DECISION + CONTROLLED.** Which internal destinations guests may reach is a product choice. The recommended default is deny.

### M3: Authorization is tied to the IP address, not the device (Confirmed by reading)

**Where.**
- Both authorization sets are `ifname . ipv4_addr` (`render_nft.go:84`, `:89`), and so is the forward accept (`:216`, `:218`).
- The Kea ownership check (`cmd/netd/phase3_address_owner.go`) runs only when a session is renewed. It never runs per packet.

**Problem.** A device on the same guest network that sets a signed-in guest's IP address by hand gets that guest's internet access. Its MAC address is never checked. The legitimate guest keeps the DHCP lease, so the ownership check sees nothing wrong. Access isolation at the access point does not stop this, because the attacker only needs the address.

**Impact.** Free or paid access can be taken without signing in, for as long as the victim's session lasts. Both devices then suffer from the address conflict, so this is noisy, but it works.

**Recommended fix.**
- Key the sets as `ifname . ipv4_addr . ether_addr`, with the MAC from the Kea lease that the ownership check already reads.
- Match `ether saddr` in the forward accept.
- scd's legacy set needs the same change, or it should be retired first.
- A device without a known MAC is not authorized.

**Test.** A kernel test: authorize (IP, MAC A); a guest namespace using that IP with MAC B is refused, and MAC A is allowed.

**CONTROLLED** (enforcement core).

### M4: The shared-allowance re-rate gives every device the whole allowance (Confirmed on a real kernel; Phase 6 M4)

**Where.**
- `internal/shape/shape.go:306-326`: `ReRateSession`, and `changeClass` at `:445-451`.
- Called on every plan for every active session, which is the lease-renewal path: `cmd/netd/phase3_provision.go:148-160`.

**Evidence.** A throwaway kernel test against the real `shape` client in the netns harness. The test was not committed.
- Group `1:2890`: rate 8 Mbit, ceil 8 Mbit.
- Session `1:1002`: before the re-rate, rate 800 Kbit and ceil 8 Mbit. After the re-rate, rate 8 Mbit and ceil 8 Mbit.
- `ReRateSession` returned no error. The class kept its parent.

**Impact.** From the first plan after a shared session goes active, each device on the allowance has the whole allowance guaranteed. A two-device 8 Mbit package behaves like two 8 Mbit packages. The bill is unaffected, but the product's speed promise to the hotel is not kept.

**Recommended fix.**
- Pass `ReRateSession` the session's parent and floor: for SHARED, the group class and `sharedFloorKbps(rate)`, as `PrepareSessionIn` does.
- Re-rate the group class itself when the package rate changes.
- Turn the scratch test into a committed `kerneltest` case.

**Test.** Two SHARED sessions under one group are re-rated. Afterwards each keeps its floor, and the group's rate and ceiling are unchanged.

**CONTROLLED** (enforcement). Do it with Phase 6 batch B4.

### M5: Open connections outlive the end of a guest's authorization (Confirmed by reading)

**Where.**
- `render_nft.go:196`: `ct state established,related accept` comes before both sign-in accepts.
- conntrack is flushed only in `Revoke` (`cmd/netd/phase3_gate.go:404-449`).

**Problem.** When an authorization ends because its lease timed out, nobody calls `Revoke`. This happens when the producer stops, or when Kea is unreadable and renewal is withheld (`phase3_provision.go:62-69`). The stray sweep works from a read of the set that no longer contains the element. A guest's open TCP connections, and UDP or QUIC flows they keep refreshing, go on being forwarded indefinitely. Only new connections are refused.

**Impact.** The documented "fails closed within a bounded interval" holds only for new connections. A guest streaming video keeps internet after their entitlement ended, until the stream stops. The same applies to scd's legacy set.

**Recommended fix.** Accept established traffic towards the WAN only for an authorized source: split the established accept so guest-to-WAN traffic still has to match one of the sets, and return traffic stays as it is. This takes no conntrack bookkeeping and also covers the legacy set. Keep the flush in `Revoke` for a prompt cut.

**Test.** A kernel test: authorize with a 2 s lease and open a long-lived TCP connection. Once the lease expires, the connection stops passing traffic.

**CONTROLLED** (enforcement core).

### M6: A network change that dies mid-apply blocks every later change (Confirmed)

**Where.**
- `cmd/netd/store.go:367-380`: `PendingRevision` selects only `state='pending_confirmation'`. The watchdog at `main.go:461-475` uses it.
- `store.go:215` and `apply.go:244-246` both say the watchdog reclaims `applying` revisions.
- The one-in-flight unique index: `migrations/0002_edge_networking.up.sql:167-170`.

**Problem.** If netd crashes or is killed inside `applyBundle`, which can take minutes, the revision stays `applying` forever. Its `confirm_deadline` is set but never read. Every later apply fails on the unique index. Boot reconciliation re-asserts the active revision, but the half-applied revision is never marked failed or rolled back.

**Impact.** Network configuration is stuck until someone edits the database by hand.

**Recommended fix.**
- Have the watchdog and the boot path also take `applying` rows whose deadline has passed, and roll them back through the existing rollback.
- On boot, treat any `applying` row as failed whatever its deadline, since no apply survives a restart.

**Test.** An integration test: a revision left `applying` with a past deadline is rolled back, and the next apply succeeds.

### M7: Rollbacks report success when they failed (Confirmed)

**Where.**
- **Guest networks:** `cmd/netd/apply_ops.go:564-571` discards the Kea push errors, then records `rolled_back`.
- **System network (WAN and management):**
  - `cmd/netd/sysnet.go:483-490`: `restore` discards the `netplan apply` error.
  - `:470-476`: `restoreFiles` discards `copyFile` errors.
  - The callers then clear the pending marker and record `network.rollback.automatic.succeeded` (`:331-335`, and the watchdog and boot paths).

**Impact.**
- **Guest networks.** The database and the admin console say "rolled back" while Kea goes on serving the failed revision's subnets.
- **System network.** This is the safety net for a change that cut the appliance off. If the revert fails, the marker is gone, so nothing retries. The appliance can stay unreachable while the audit record says the revert succeeded.

**Recommended fix.**
- Check every step.
- On a failed system-network revert, keep the marker so the watchdog retries, and record `rollback.automatic.failed`.
- On a failed Kea push, record a `rollback_kea` failure event and mark the revision `rollback_failed`, or whatever state the UI already shows as needing attention.

**Test.**
- A netplan double that fails `apply` once: the marker stays and the retry succeeds.
- A Kea double that fails: the revision is not recorded as cleanly rolled back.

**CONTROLLED** (networking).

### M8: The accepted-plan generation is not saved durably, so a replay guard can be lost (Confirmed; netd half of Phase 6 M2)

**Where.**
- `cmd/netd/phase3_control.go:80-95`: `planStore.save` ignores errors, does no fsync of the file or its directory, and uses a fixed temp name.
- Called at `phase3_shaping.go:186-188`.
- The stale-generation check runs only `if hasLast` (`internal/shapeplan/plan.go:157-166`).

**Problem.** After a power cut or a failed write, the stored generation is old or missing. A missing or unreadable file means `hasLast` is false, so the next plan is accepted whatever its generation. An older plan that has not expired can then be accepted again and reinstate access that was revoked. `ExpiresAt` bounds the window, and `Validate` puts no cap on it.

**Impact.** Only an authorized producer (acctd) can submit plans, so this needs a crash plus a replay. Combined with Phase 6 M2, where acctd's own counter is also not durable, generations can move in either direction after a power cut.

**Recommended fix.**
- Write the file the way the activation journal does: temp file, fsync, rename, fsync of the directory. Return the error, and refuse to report the plan converged if the save failed.
- Cap `ExpiresAt` at a few plan intervals.
- Treat an unreadable store as "unknown": refuse a plan whose generation is not higher than the highest one netd can prove, rather than accepting anything.

**Test.** A unit test with a store that fails to write: the plan is reported degraded. A load of a corrupt file refuses the stale generation.

**Batch.** Do it with Phase 6 batch B2.

### M9: Other config values are written without validation or escaping (Confirmed for the render; Possible for the reach)

**Where.**
- **Guest networks:**
  - `BridgeName` reaches nft (`render_nft.go:141` and others), netplan, Kea and `ip link add name` argv (`apply_ops.go:200`, `:285`).
  - `ParentInterface` goes into netplan YAML unquoted (`render_netplan.go:46`).
  - `DomainName` and reservation hostnames go to Kea.
  - None of these is checked in `validate.go`.
- **System network:**
  - `internal/sysnet/sysnet.go:60-62` and `:72-77` take the WAN and LAN interface names from the request.
  - `ValidateFull` (`:155-170`) checks only conflicts and empty values.
  - `RenderWANNetplan` prints the name as a YAML key (`:295`), as does the LAN render.

**Problem.**
- A system-network interface name containing a newline adds YAML keys, for example extra addresses or routes.
- A bridge name starting with `-` becomes an `ip` option.
- A bridge name that does not start with `br-g` is never cleaned up or rolled back (`apply_ops.go:728`).

Today edged generates the bridge name and the parent comes from a list, so most of this needs a buggy or hostile writer. The system-network interface names do come straight from the request.

**Recommended fix.** Validate every rendered value at the boundary:
- interface names against `^[a-zA-Z0-9._-]{1,15}$` and the discovered inventory;
- bridge names against the generated form;
- domain and host names against RFC 1123.

Quote the YAML scalars as well.

**Test.** Hostile values for each field are refused by validation.

**CONTROLLED** (networking). Do it with H1.

### M10: A rejected apply can still reach the live firewall (Confirmed by reading)

**Where.** `cmd/netd/apply.go:224-252`. `setInForce(intent, vip)` runs before `MarkApplying`. If `MarkApplying` fails, the function returns without `restoreInForce(before)`, which the two earlier failure paths do call (`:231`, `:241`).

**Problem.** Nothing refuses an apply while another revision is waiting for confirmation, so the unique index refuses it at `MarkApplying`. By then the new intent is "in force". `nftStructureLoop` re-renders the firewall from the in-force intent every 10 s (`nft_watch.go:40-55`). The rejected intent is installed without a confirm window or a rollback, while the earlier revision still waits for its own confirmation. A database error at that moment has the same effect.

**Impact.** The live firewall describes a configuration the database records as never applied. A later rollback of the pending revision restores the wrong "previous" state.

**Recommended fix.**
- Restore the in-force intent on every return after `setInForce`.
- Refuse an apply up front while a revision is `applying` or `pending_confirmation`.

**Test.** An apply while another is pending is refused, and the in-force intent is unchanged.

## Low

| # | Problem | Where | Recommended fix |
|---|---|---|---|
| L1 | **The client-isolation switch has no effect.** Routed traffic between guests is always dropped by the inter-guest rule, and layer-2 traffic never reaches the appliance. The admin console implies the appliance enforces isolation. | `render_nft.go:188-195`, `:209`; `model.go:57` | Either relabel the setting to say it depends on the access point, or enforce it on bridges with several member ports (`bridge link set dev <port> isolated on`). DECISION (product wording). |
| L2 | **IPv6 leftovers.** `net.ipv6.conf.all.forwarding=1` is set although no IPv6 is routed, and it stops the WAN accepting router advertisements unless `accept_ra=2`. Guest bridges keep IPv6 link-local addresses. The firewall drops the traffic, but nothing disables IPv6 on them. | `deploy/sysctl/99-stayconnect.conf:3`; netplan render | Set IPv6 forwarding to 0, and set `disable_ipv6=1` on guest bridges, as defence in depth. CONTROLLED (networking). |
| L3 | **One failed class-state save denies every new guest until restart.** `unprovenUnknown` is set and never cleared. | `phase3_shaping.go:418-426`; `phase3_journal.go:329` | Clear it after the next successful save. |
| L4 | **Journal bookkeeping.** When a save fails, the rollback deletes the strike history, so the backoff resets. A record with strikes but no backoff can be written, and the journal's own validator then rejects the whole file after a restart. | `phase3_journal.go:350-356`; `phase3_provision.go:145`; journal validation | Restore the previous record instead of deleting it. Do not persist a record the validator would refuse. |
| L5 | **A plan without a MAC skips the ownership check.** `Owns` returns "unknown" for an empty MAC, and provisioning continues. | `phase3_address_owner.go:59`; `phase3_provision.go:62` | Refuse a session without a MAC (this follows from M3). |
| L6 | **A class clash lets a third session through.** On a clash the first session is removed from `desired`, so a third with the same class number finds no clash and is installed. | `phase3_shaping.go:258-269` | Keep a "clashed" mark per class and refuse every later claimant. With Phase 6 L12. |
| L7 | **The stray sweep matches by class number, not IP.** A stale element for another IP with the same low 12 bits is treated as wanted. Possible: subnets are capped at /20. | `phase3_shaping.go:340-345` | Compare the IP. |
| L8 | **Locking details.** `endFlows` takes `p.mu` without `defer`, so a panic would deadlock enforcement. `/epochs` reads `mode.Active`, which the scope watcher replaces, without the lock. | `flows.go:486-506`; `phase3_shaping.go:620-626`; `phase3_scope_watch.go:45` | Use `defer`, and read the mode under the lock. |
| L9 | **A plan pass runs under the request context, holding the lock.** A cancelled request leaves a revocation half done until the next pass. A hung nft, tc or Kea call blocks status and staff disconnect for up to 180 s. | `phase3_shaping.go:153-156`; `main.go:264` | Run the pass under `context.WithoutCancel` with its own timeout, as the other handlers do. Give `Leases()` a context. |
| L10 | **conntrack dump.** The whole table is held in memory on every revoke. `NLM_F_DUMP_INTR` is not checked. The delete loop ignores the context. | `internal/conntrack/netlink_linux.go:41-98` | Filter while reading, retry on an interrupted dump, check the context per entry. |
| L11 | **Commands without timeouts.** `ifaceAddrs`, `ifaceHasIP` and `ifaceHasAnyIP` use `exec.Command` with no context. | `apply_ops.go:741-771` | Use the existing `run` helpers, with a timeout. |
| L12 | **Config files are not written atomically.** netplan, the Unbound fragment and the Kea config are truncated in place, and the system-network files the same way. A power cut can leave a truncated file for the next boot. | `apply_ops.go:81`, `:151`, `:401`, `:577`; `kea_bootstrap.go:86`; `sysnet.go:283`, `:289`, `:672-677` | Temp file, fsync, rename, as `portal.go` and `healthdir` already do. |
| L13 | **nft watch failures are only logged.** No backoff and no event. A permanent failure, such as H1's bad render, silently stops the firewall being re-asserted. | `nft_watch.go` | Record an event and report it in health after N failures; back off. |
| L14 | **Smaller system-network checks.** WAN verification dials the appliance's own new address, so it proves little about the operator's path. In DHCP mode the old static IP is still checked, which can cause a needless rollback. Any mode other than `dhcp` is treated as static. Loopback, multicast and link-local addresses are not refused. `confirm()` takes no lock and is not tied to a revision. The netd unit has no hardening, which its comment says is deliberate. | `sysnet.go:326-336`, `:354-364`, `:500-515`; `internal/sysnet/sysnet.go:95`; `deploy/systemd/stayconnect-netd.service` | Check the gateway and DNS through the new path; skip the address check in DHCP mode; validate the mode and address classes; bind confirm to the revision under the lock. Unit hardening (a capability bounding set, `ProtectSystem=full` with write paths) is a separate decision. |

## Clean code

| # | Problem | Recommended fix |
|---|---|---|
| CC1 | **Dead code (staticcheck U1000).** `keaListeningFor`, `unprovenRecordHorizon`, seven `stage*` constants in `phase3_shaping.go`, `writePendingLocked`, `audit`, `cfgJSON` in `sysnet.go`, `joinNonEmpty`, `nft.probeConcat`. `sanitize()` in `sysnet.go:822-824` does nothing. | Delete them. Make `sanitize` real or remove it. |
| CC2 | **The kernel gate does not test the real ruleset.** `scripts/ci/kernel-netns-suite.sh` loads a hand-written subset (the forward chain and captive DNAT), and no kernel test calls `RenderNftables`. The IPv6 drops, the input boundary, the management drop and the inter-guest drop are proven only as strings. | Load `RenderNftables` output for the harness topology into the router namespace. Add packet tests for IPv6, the management ranges (M2), the inter-guest drop and portal limits (M1). |
| CC3 | **Ignored errors and inconsistent patterns.** `_ =` on store and push calls (M7, M8, `apply.go:214`). Each file-writing path has its own pattern, some atomic and some not (L12). | Check errors. Use one atomic-write helper (`healthdir` has one). |
| CC4 | **Comment volume.** As in earlier phases: long narrative comments, some restating history. | Trim when a batch touches a file. |

## Not findings

- **No shell is involved.** Every command uses `exec.CommandContext` or `exec.Command` with an argument list. gosec's G204 and G702 reports are command-with-variable-arguments, and none crosses a shell. The values are covered by H1 and M9.
- **gosec G703, G304, G301, G302 and G306 on file paths and permissions.** The paths come from configuration, not requests. No written file holds a secret. The Kea config, at 0644, carries reservation MAC addresses and hostnames: 0640 is better, and is noted with L12.
- **gosec G115 integer conversions** in `phase3_origin.go` and `conntrack`. Values are bounded by their sources (epochs, netlink lengths that are already checked).
- **govulncheck: 15 reachable vulnerabilities.** These are the same Go 1.25.12 standard-library, `x/text` and `pgx` v5.7.1 advisories found in earlier phases. They are tracked once, in Phase 10.
- **The default `NETD_DB_URL` with a password.** Already Phase 6 L14, tracked in Phase 10.

## Fix batches

A batch is done when its tests pass, and the package tests, the netd integration tests on a throwaway full-schema database, and the kernel netns suite on Linux still pass.

| Batch | Findings | Files | Area and notes |
|---|---|---|---|
| B1 | H1, M9 | `internal/netcfg/render_nft.go`, `validate.go`, `render_netplan.go`; `internal/sysnet/sysnet.go`; `cmd/edged/resources_network*.go` | Input validation and escaping. CONTROLLED. Do first. |
| B2 | M6, M10, M7 (guest-network part), L13 | `cmd/netd/apply.go`, `apply_ops.go`, `store.go`, `main.go`, `nft_watch.go` | The apply lifecycle. Shares `apply_ops.go` with B6. |
| B3 | M7 (system-network part), L14 | `cmd/netd/sysnet.go`, `internal/sysnet/sysnet.go` | System-network rollback. CONTROLLED. Shares `sysnet.go` with B1: do it after B1. |
| B4 | M4, L6, L7 | `internal/shape/shape.go`, `cmd/netd/phase3_provision.go`, `phase3_shaping.go`, `internal/kerneltest` | Shaping. With Phase 6 B4 and B5. Needs Linux. CONTROLLED. |
| B5 | M8, L3, L4, L8, L9 | `cmd/netd/phase3_control.go`, `phase3_shaping.go`, `phase3_journal.go`, `flows.go`, `internal/shapeplan/plan.go` | Phase-3 durability and locking. With Phase 6 B2. Shares `phase3_shaping.go` with B4: do it after B4. |
| B6 | M1, M2, M5, L2, L12 (netd files), CC2 | `internal/netcfg/render_nft.go`, `model.go`; `deploy/sysctl`; `scripts/ci/kernel-netns-suite.sh`; `internal/kerneltest` | Ruleset changes, each with a real-kernel test. M2 needs the decision. New settings under §0C for M1 and M2. CONTROLLED. Shares `render_nft.go` with B1. |
| B7 | M3, L5 | `internal/netcfg/render_nft.go`, `internal/nft`, `cmd/netd/phase3_gate.go`, `phase3_provision.go`; scd's legacy set | MAC binding. CONTROLLED. After B6. Needs a plan for scd's legacy set. |
| B8 | L10, L11, L1, CC1, CC3, CC4 | `internal/conntrack`, `cmd/netd/apply_ops.go`, `sysnet.go` | Clean-up. L1 needs the wording decision. |

## Decisions for the Product Owner

1. **M2.** Which internal networks guests may reach. Recommended: deny all private, carrier-NAT and link-local ranges by default, with an allow-list setting.
2. **L1.** Whether "client isolation" is relabelled as depending on the access point, or enforced on multi-port bridges.

## Tests run

All runs used throwaway infrastructure and were removed afterwards.

- **Unit tests, Linux container** (Go 1.25.12; netd and the eight library packages): 513 passed, 0 failed, 1 skipped. The skipped test needs an unreadable `/sys/class/net`.
- **netd integration tests, throwaway full-schema PostgreSQL** (all migrations through 0150): 8 passed, 0 failed.
  - 4 ran on Windows.
  - The 4 enforcement-cycle tests ran in the Linux container, because they need `SO_PEERCRED`.
- **Kernel netns suite** (`scripts/ci/kernel-netns-suite.sh`, WSL2 kernel 6.6, nftables 1.0.6, iproute2 6.1, ifb available): 33 passed, 0 failed, 0 skipped. The host ruleset was unchanged.
- **Throwaway checks, not committed:**
  - the SHARED re-rate kernel test (M4);
  - the network-name render with `nft -c` and a real load in a separate namespace (H1).
- **Scanners:** go vet clean, staticcheck (U1000 only, CC1), gosec, govulncheck.
