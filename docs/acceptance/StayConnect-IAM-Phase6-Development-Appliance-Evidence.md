# Phase 6 — DEVELOPMENT-appliance evidence (LIVE-DARK, in progress)

**Appliance:** the development appliance at `172.21.60.23` (`radius`). **Not Production**, which was not contacted.
**Decision:** D25 / T0057; **accepted** by D26 / T0061 (2026-08-16). **Status:** Phase 6 is `ACCEPTED_AND_CLOSED` at verified LIVE-DARK maturity.

Everything below was executed on 2026-08-15 and read back from the appliance itself. Sections 1-6 describe the
appliance before any capability was enabled. Section 7 is the **controlled validation**, which did enable the
Phase-6 capabilities for the duration of one supervised run, under D25, and restored them; the appliance is
verified dark afterwards and across a reboot (section 9). No real guest, PMS, provider or financial traffic
was involved at any point, and no paid access was created: the only entitlement is a zero-price
`ADMIN_GRANT`.

## 1. Flag coherence, before anything was changed

`tools/phase6-flag-coherence.sh`, run on the appliance against its own systemd units:

```
STAYCONNECT_PHASE6_MASTER                      agrees across scd/acctd/edged: off
STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME       agrees across scd/acctd/edged: off
STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST    agrees across scd/acctd/edged: off
STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_ADMIN    agrees across scd/acctd/edged: off
no child flag is set without its master
aggregate is OFF everywhere; no accounting prerequisite applies
PHASE6_FLAG_COHERENCE pass=6 fail=0
```

The three services read the same state, which is the property that matters: a half-configured runtime is what
would let an appliance offer a capability it cannot account for.

## 2. Backup, taken before the schema was touched

`pg_dump -Fc` of `stayconnect_site` to `/var/backups/stayconnect/phase6/pre-phase6-20260815T192610Z.dump`
(6,466,001 bytes, sha256 begins `308015a2fd418118a179e065bf4d1e46`). Verified readable by listing its table
of contents: **1187 entries**. A backup nobody has read is a hope, not a backup.

## 3. The Phase-6 schema, applied DARK

Migrations `0030` → `0043` applied in order, each under `ON_ERROR_STOP`, all fourteen succeeding. `0044`
through `0047` followed later (sections 5 and 6); the readings below are from this first pass.

| Reading | Before | After |
|---|---|---|
| `iam_v2` tables | 75 | **81** |
| `p6_*` functions | 0 | **16** |
| **rows in `iam_v2`** | **0** | **0** |
| session-binding guard (`0032`) | absent | **present** |
| `guest_device_self_service` default | — | **false** |
| `PUBLIC` may execute the expiry writer | — | **no** |
| `svc_acctd` writes anywhere in `iam_v2` | — | **none** |

`iam_v2` still holds **zero rows**: the schema is present and nothing is routed to it. This is additive and
reversible — the same down sequence rehearsed 50/50 against the scratch database, including the `0032`
boundary.

All four services stayed `active` through the migration, and `journalctl -p err` recorded no entries.

## 4. Reboot verification

The appliance was rebooted and came back. Read after the reboot:

* `stayconnect-scd`, `-acctd`, `-edged`, `-portald`, `-netd` — all **active**;
* flag coherence **6/6 again**: every Phase-6 flag still **OFF**, and still agreeing across the three
  services, so the dark state is a property of the deployment rather than of the last thing someone typed;
* schema unchanged: 81 tables, 16 functions, **0 rows**, guard present;
* `appliance_product_settings`: **0 rows**, and none with the capability ON;
* no service errors since boot.

## 5. The Phase-6 runtime, deployed and inert

Migration `0044` (the lower-bound correction) was applied first; `p6_exhaustion_instant` is present and
`iam_v2` still holds **zero rows**.

The four binaries were built from this branch (`GOOS=linux`, `-trimpath`, `-tags production`), checksummed
locally, transferred, and **verified by `sha256sum -c` on the appliance** — all four `OK`. The previous
binaries were copied to `*.bak-prep6` first, so the runtime is one `install` away from being reverted. The new
`scd` reports build profile `production`, matching the binaries it replaced.

After restarting scd, acctd, edged and portald:

* all five services **active**, and `journalctl -p err` recorded **no entries**;
* `scd`: *"phase6 guest device surface is DARK (routes absent)"* — the routes are absent, not refusing;
* `edged`: *"phase6 dark guest-device surface"*, every flag `false`;
* `acctd`: `phase6_fallback_accounting: true` — the accounting owner of last resort was constructed, because
  the Phase-3 arm is off, and it logged the safety line saying so. With `iam_v2` empty it finds nothing to do,
  which is the data-driven behaviour the design depends on;
* flag coherence **6/6** with the new binaries;
* `iam_v2` rows: **0**.

So the runtime is deployed and **inert**: the code is present, every capability is off, and nothing has been
written.

## 6. The corrected runtime, the Hotel Admin bundle, and the safety harness

**Migration `0045`** (over-budget fail-closed), **`0046`** (suspension evidence that does not claim a
termination) and **`0047`** (the grants the guest surface needs to resolve a device) were applied, and the
binaries rebuilt from the corrected source and installed with `*.bak-*` rollback copies beside them. The
appliance now carries `0030` -> `0047`: **81 `iam_v2` tables, 18 `p6_*` functions**, and zero Phase-6 rows in
ordinary operation.

**The Hotel Admin bundle IS deployed**, through the existing `deploy/scripts/deploy-hotel-admin.sh`
package/install path -- an atomic release symlink with `hotel-admin.previous` one `ln` away. The release in
place is `/opt/stayconnect/releases/hotel-admin/20260815-204927`, built **without** `NEXT_PUBLIC_PHASE6_ADMIN`.
That flag is **build-time state**, so the Phase-6 operator screens are compiled out of the deployed artifact:
the dark state of the admin surface is a property of the bundle, not of a runtime check. Validating those
screens would need a bundle built with the flag set, and the appliance finishes on this dark one -- which the
harness enforces by restoring the exact release, verified by path *and* by content hash.

**The fail-safe harness** (`deploy/scripts/phase6-controlled-validation.sh`) was proven before it was trusted.
Its restoration is a **trap** -- success, failure, error, INT and TERM -- and it restores a captured baseline
rather than tidying towards an assumption. Three fault injections were run on the appliance, each enabling
capabilities and then abandoning the run in a different way:

| injection | what it leaves behind | outcome |
|---|---|---|
| `body-failure` | capabilities on, body fails | restored, 14/14, appliance dark |
| `signal` | capabilities on, run signalled | restored twice (idempotent), 26/26 |
| `partial` | every flag on, setting on, fixture seeded | restored, 14/14, appliance dark |

Running it also found three defects in the harness itself that reading it would not have: `curl ... || echo
000` printing *both* values, so the caller compared `000000` against `404`; a probe fired into the gap while a
restarted service is still binding its socket, answering `000`, which was being read as "the route is absent"
-- silence is not darkness; and a flag list wrapped with a backslash **inside double quotes**, where
backslash-newline is two literal characters rather than a continuation, producing a line systemd refused and
`scd` would not start on.

## 7. The controlled validation, run end to end

**42 proofs, no failures**, followed by verified restoration. The capability was exercised against the real
runtime -- the real `scd` socket, the real device resolution, the real accounting daemon -- not a fixture:

| what was proven | result |
|---|---|
| master flag alone mounts no guest route | PASS |
| guest surface without its Phase-3 prerequisite refuses to start at all | PASS |
| prerequisite satisfied, Phase-6 child off -> route still absent | PASS |
| both -> route mounted | PASS |
| setting OFF -> guest told nothing is available; ON -> sees own devices | PASS |
| setting change takes effect with no restart | PASS |
| guest releases their own idle device; refused an id that is not theirs | PASS |
| every outcome, including refusals, in the durable audit | PASS |
| Central (`150.0.0.252`) blackholed -> surface answers unchanged | PASS |
| the running `acctd` charges online time | PASS |
| **capability DISABLED -> the durable budget is still accounted** | PASS |
| budget exhausted -> terminated for `TIME`, evidence names the cause | PASS |
| ended entitlement -> surface stops answering, session closed | PASS |
| nothing outside the reserved stay changed state; no priced purchase exists | PASS |

Three appliance-side findings came out of it, all the same class -- code paths no test could reach, because
every test connects as a role that owns the schema:

1. `svc_scd` held `SELECT` on `iam_v2.devices` and nothing more, so the surface failed on its first line
   (`permission denied for table devices`). Device resolution is an **upsert**: a device row is created the
   first time the appliance sees it. Fixed by `0047`, with the refusals asserted as well as the grants.
2. The same for `iam_v2.entitlements` and `iam_v2.service_plan_revisions`, read-only.
3. Neither `scd` nor `acctd` could open a controlled operation, so both refused to serve the Phase-3 arm the
   Phase-6 guest surface depends on. That grant is a **Phase-3 provisioning** step, not a Phase-6 one: the
   harness makes it for the duration of the run and revokes it afterwards, and both roles were confirmed back
   at `false` at the end.

Two proofs were also being obtained dishonestly and are now obtained properly. The local-first check
blackholed `127.0.0.1` and passed -- routing loopback into a black hole proves nothing -- and now excludes
loopback and the appliance's own addresses. And exhaustion was being forced by winding the accounting
watermark twenty minutes back, which the product rightly refuses to bill: a gap longer than the per-tick
charge bound means the service was not watching, so those seconds become a recorded skipped interval rather
than a charge. The fixture is now placed just short of its budget and the running daemon crosses the boundary
itself.

**Restoration is verified against the runtime**, not against the files the script wrote: the authoritative
flag-coherence gate, a real request to the `scd` socket, service health, the accounting owner, the Hotel Admin
release by path and content hash, and the absence of live synthetic state.

One restoration defect was found and fixed here, and it is worth naming because every individual run was
correct: an earlier run ended with `guest_device_self_service = true`; the next captured `true` as its
baseline and faithfully restored it; and the row-for-row baseline check passed the whole time, because enabled
was what had been captured. While the phase is dark that setting cannot legitimately be on -- nothing reads
it, so a `true` is residue, and the dangerous kind, because the capability would go live the moment the gate
opened without anyone choosing it. The final state is now **pinned** through the audited writer rather than
merely restored, and reported as a correction.

## 8. Backup, and what is deliberately not claimed

A fresh backup was taken with the sanctioned `stayconnect-site-backup.sh`
(`/var/backups/stayconnect/site-20260815-234920.dump`) and **proven restorable**: restored into a scratch
database on the appliance, showing **81 `iam_v2` tables, 18 `p6_*` functions** and the settings row, then
dropped.

A full in-place restore of `stayconnect_site` is **not** claimed. It requires a restore manifest signed
off-appliance with the registry root key and verified against the appliance's own pinned anchor. That is key
custody, and working around it to make a test pass is exactly the shortcut this phase has refused elsewhere.

## 9. The second reboot

The appliance was rebooted (`2026-08-15 23:55:51`) -- the second reboot of the phase, and the first with the
Phase-6 runtime, the dark Hotel Admin bundle and migrations `0030`-`0047` all present. The earlier reboot in
section 4 was of the *schema-dark* state and does not stand in for this one.

| after the reboot | reading |
|---|---|
| `scd`, `acctd`, `edged`, `portald`, `netd`, `hotel-admin` | all **active** |
| flag coherence | **pass=6 fail=0** |
| guest device route in the running `scd` | **absent (404)** |
| Hotel Admin release | `20260815-204927`, matching by path and content hash |
| live synthetic entitlements, sessions, device bindings | **0** |
| appliances with the capability enabled | **0** |
| `svc_scd` / `svc_acctd` writer-boundary grant | **false**, back to baseline |

## 10. Standing state

Phase 6 is `ACCEPTED_AND_CLOSED` at verified LIVE-DARK maturity (D26/T0061). This evidence is what that
acceptance rests on, and it authorises no cutover and no enablement of any kind. Production was not contacted.

The appliance is left as a **validated LIVE-DARK candidate**: every capability off and coherent across the
three services, the guest routes absent from the running process, the operator screens compiled out of the
deployed bundle, no synthetic business state live, a read-back backup, the previous binaries one `install`
away, and a rollback sequence rehearsed **65/65** across `0030`-`0047` and back, including the `0032`
boundary -- with that runner mutation-proven to fail hard (`pass=2 fail=5`) when pointed at a container that
does not exist, which the previous text-matching runner would have reported as green.
