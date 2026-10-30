# Capacity and first-time offline activation acceptance (2026-10-29 / 2026-10-30)

Closure round of the AD-1..AD-68 audit remediation mission. Tools: [`tools/oglab`](../../tools/oglab/README.md) (the lab) and
[`tools/ogload`](../../tools/ogload) (the load generator). Related evidence: [`reviews/NETD_ENFORCEMENT_SCALING.md`](../reviews/NETD_ENFORCEMENT_SCALING.md),
[`reviews/PR272_ROLLUP_REVIEW.md`](../reviews/PR272_ROLLUP_REVIEW.md), [`reviews/USAGE_AFTER_RAW_PURGE_FIX.md`](../reviews/USAGE_AFTER_RAW_PURGE_FIX.md),
[`reviews/OFFLINE_ACTIVATION_REGISTRY_FIX.md`](../reviews/OFFLINE_ACTIVATION_REGISTRY_FIX.md), [`reviews/AD36_NEW_DEVICE_IDENTITY_REVIEW.md`](../reviews/AD36_NEW_DEVICE_IDENTITY_REVIEW.md).

## 1. What this is, and what it is not

**Simulated acceptance in an isolated lab, not field acceptance.** The lab is a set of privileged systemd containers on one Docker Desktop
host (no hypervisor rights were available), installed with the product's own installers, with the appliance capped at 2 CPUs and 4 GB by cgroup
(the approved reference). 2,000 *simulated* clients (one network namespace, MAC and DHCP lease each, real portal sign-ins and real HTTP transfers) stand in
for devices. Nothing was registered on the production Central; the lab Central is new (new vendor key, assignment keys, registry root, appliance CA)
and the production name is blackholed. A container shares the host kernel and CPUs, so absolute numbers are indicative, not a hardware benchmark.

**Not performed (needs equipment or authority this mission does not have):** 2,000 physical devices and a real Wi-Fi/switch fabric; the approved
hardware or a VM at 2 vCPU / 4 GB; real PMS traffic (post-stay, FIAS, posting); payment providers.

## 2. Capacity results

Release under test: `f20faa171ea7` (appliance), 1,700 long sessions plus up to 300 cycling clients on two client networks (one untagged, one VLAN),
Kea leases for 2,000 clients, accounting, shaping and the kernel authorization set all live.

| Item | Result |
|---|---|
| Ramp: 1,700 sign-ins at 300/min (release `885b6ac9`, before the churn fixes) | 98% succeeded; 1,700 sessions shaped and authorized |
| Idle-load admission with 1,700 sessions live (release `f20faa17`) | a fresh sign-in is online in 3.5 to 6.6 s |
| 1,700 sessions held for the whole run | 1,700 shaped; the kernel authorization set stayed full except during an acctd outage (below) |
| Memory | cgroup peak 3,039 MB of 4,096 MB; resident: scd 37-108 MB, acctd 29-136 MB, netd 36-42 MB, edged up to 153 MB, portald up to 142 MB; all PostgreSQL processes together 2.7-4.8 GB resident (shared buffers counted per process) |
| CPU | 10-48% average of 2 CPUs by phase, 77% max |
| Database | 33-34 connections max (scd 20, the per-role limit); accounting rows grew +8,377 (10 min steady) and +1,343 (5 min stress) |
| **300 successful sign-ins/minute sustained with 60-second sessions (steady phase)** | **NOT MET**: 19 of 3,000 attempts succeeded; p50 8.3 s, p95 16.8 s; the refusals are "could not bring your device online" (enforcement not confirmed in time). |
| Traffic phase success | not a valid forwarding measure at this concurrency (see 2.2) |

### 2.1 Why the churn target is not met (measured, not guessed)

* The first netd defect (a pass that grew with the square of the sessions) was fixed earlier; the ramp then reached 98%.
* Under sustained churn the netd pass budget was consumed by queueing, lease renewal waited behind other work, ended sessions were re-recorded every pass and the plan
  listed them for an hour. Fixed and measured (`NETD_ENFORCEMENT_SCALING.md` section 10): in the simulator 268 of 300 long sessions lost authorization before and 0 after, longest pass 127.8 s to 4.3 s, database calls 40,209 to 3,114.
* **What remains:** new sign-ins queue on the appliance-wide licence-slot advisory lock and a saturated `svc_scd` connection limit, and every admission waits for a netd pass whose database calls
  (`activate_session_enforcement` hit its 3 s bound repeatedly under churn) are slowed by the same load. In this run the lock is now taken only for the count and the insert
  (`iamv2_session_activate.go`), which is correct but did not change the outcome: p50 sign-in latency under churn is 8 s with most attempts finishing after the 10 s target.
  Closing the gap needs further change in scd (connection limit, admission path), acctd (per-session commits, long expiry transaction) and possibly a schema or role decision, which is a controlled change and is **not** made here.
* An earlier voucher-exhaustion artefact (spent 60-second voucher codes) was found and removed from the measurement by issuing fresh codes; the numbers above are with fresh codes.

### 2.2 Honest limits of the traffic numbers

`ogload traffic` success was 914 of 6,972 (steady) and 9,953 of 33,320 (stress): the failed connections never reached the bridge (a burst of 850 clients fetching from the appliance's own portal failed 43% while 100 clients at once
succeeded 100%), so these figures describe the generator and the container network, not forwarding health. They are reported, not claimed as a pass.

### 2.3 Failure behaviour (single clean run, 1,700 sessions live)

| Test | Result |
|---|---|
| netd stop/start | authorization set refilled within 5 s; 1,700 sessions still active |
| PostgreSQL restart | listening again in 2 s; authorization set never emptied; 1,700 sessions active. (An earlier attempt left the lab's published database port unprogrammed after a Docker daemon restart in the container; recreating the container through the product's compose file restored it. Lab plumbing, not a product change.) |
| acctd stopped for 4 minutes | the authorization set emptied (a lease lives only as long as the plan that renews it, 90 s), so guests go offline after about 90 s without a plan; sessions stayed active in the database; the set refilled within 6 s of acctd starting. **Whether that bound should be longer is a product decision.** |
| roll-up catch-up after the outage | raw, minute and hour byte totals equal (7,161,476,120) after catch-up: nothing lost, nothing counted twice |
| Central disconnect | **not run in this lab.** By construction no Central call is on the sign-in path (licence from disk), but this was not exercised here. |
| Retention (migration 0180) | proven with a real PostgreSQL in `usage_after_raw_purge_integration_test.go` (usage unchanged after the raw purge); not repeated at lab scale |

## 3. First-time offline activation acceptance

Fresh factory-clean appliance container installed with `--offline-activation-only` and only the lab Central's public trust files; the lab Central is isolated.

* **Defect found and fixed:** a genuine, correctly signed package was refused (`no assignment trust registry on this appliance`) because the signed registry only arrived online. The package now carries the signed registry; it is verified against the pinned root and applied all-or-nothing with undo ([`OFFLINE_ACTIVATION_REGISTRY_FIX.md`](../reviews/OFFLINE_ACTIVATION_REGISTRY_FIX.md)). Central must run this release before an appliance on it can take a package; an appliance on the older code fails closed.
* Request generated on the appliance (signed by its own identity key), imported on the lab Central, activated, package minted, uploaded to the appliance byte for byte: **activated**, assigned to its customer and site, licence bound (30 days, 50 concurrent guests), Central connected afterwards.
* **Single use:** re-applying the consumed package -> 403 "appliance is already assigned".
* **Invalid packages (all refused, nothing applied):** modified signature, serial, appliance id or expiry -> 403 "signature invalid"; truncated file -> 400.
* **Recovery:** interrupted-apply recovery and undo of the registry are covered by the scd tests (journal, single-use ledger, identity adopted last).
* The identity of the existing appliance was never cloned or altered; no appliance was registered on the production Central.

## 4. Roll-up review (PR #272)

Reviewed in [`PR272_ROLLUP_REVIEW.md`](../reviews/PR272_ROLLUP_REVIEW.md): a poisoned two-hour window stalled catch-up forever; fixed with an adaptive window. Usage that depended on raw rows before the purge lost data; fixed by migration 0180 (`accounting_usage_entitlement`). Late-commit loss analysed, not reachable at current settings, documented.

## 5. Decisions and limits left to the Product Owner

1. The churn target (300 sign-ins/min sustained) is not met on this lab; the fix needs scd/acctd/database work as in 2.1.
2. Lease/plan bound: 90 s without a plan takes every guest offline (fail-closed). Longer?
3. Kernel limit: at most 2,047 shaped sessions per bridge or IFB; a site above that needs a second client network.
4. AD-36: the entitlement continuity PO clarification is not met by the new conversion (a new Entitlement is created); refusals after check-out are logged but not audited. See the AD-36 review. Not changed.
5. Field acceptance still required: physical devices at scale, reference hardware, real PMS post-stay traffic.
