# Phase 6 — DEVELOPMENT-appliance evidence (LIVE-DARK, in progress)

**Appliance:** the development appliance at `172.21.60.23` (`radius`). **Not Production**, which was not contacted.
**Decision:** D25 / T0057. **Status:** Phase 6 is `IN_PROGRESS` — not accepted, not closed, not merged.

Everything below was executed on 2026-08-15 and read back from the appliance itself. Nothing here enabled a
Phase-6 flag, and no real guest, PMS, provider or financial traffic was involved.

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

Migrations `0030` → `0043` applied in order, each under `ON_ERROR_STOP`, all fourteen succeeding.

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

## 5. What is NOT yet done, and is required before acceptance

* the Phase-6 **binaries** are not deployed — the appliance runs the pre-Phase-6 build, which is why the
  schema is inert rather than merely unused;
* the **controlled flag-on validation** of the authorized combinations has therefore not been performed;
* restoring every flag OFF afterwards and re-verifying across a reboot follows that validation;
* the appliance-side restore and rollback exercises, which should be performed against this appliance's own
  backup rather than only against the scratch database.

Until those are done this is a **partial** LIVE-DARK candidate: the schema half, verified and reboot-proven,
with the runtime half outstanding. It is deliberately safe to leave in this state — additive schema, zero
rows, every flag off, and a verified backup taken beforehand.
