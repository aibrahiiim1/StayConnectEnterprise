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

## 6. What is NOT yet done, and is required before acceptance

* the **controlled flag-on validation** of the authorized gate/setting combinations, across both product
  slices, using controlled synthetic state only. **No synthetic state has been created**, and no Phase-6 flag
  has been enabled;
* the appliance-side **local-first proof** with Central unavailable;
* the appliance-side **backup/restore and rollback** exercises, against this appliance's own backup rather
  than only the scratch database;
* restoring every flag OFF afterwards, cleaning the controlled state, and **re-verifying across a second
  reboot**. The reboot proof in section 4 is of the *schema-dark* state and does not stand in for it.

This is a **deployed but unvalidated** LIVE-DARK candidate, and it is deliberately safe to hold in exactly
this state: every capability off and coherent across the three services, `iam_v2` empty, no synthetic state,
a read-back backup from before the schema changed, the previous binaries one `install` away at `*.bak-prep6`,
and a rollback sequence rehearsed 54/54 including the 0032 boundary.
