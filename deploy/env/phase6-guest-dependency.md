# Phase-6 GUEST device self-service: a dependency that must fail closed

`STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST=true` requires the Phase-3 auth arm
(`STAYCONNECT_PHASE3_PMS_AUTH=true`), which in turn requires a real PMS.

Enabled without it, `scd` **exits at startup** with:

    phase6 guest device surface is enabled but the Phase-3 auth arm is off

That is the fail-closed design working. On the DEVELOPMENT appliance it produced a
crash loop that ran to **580 restarts** while `systemctl is-active` still reported
`active`, because systemd kept restarting it — which is why restart counters, not
`is-active`, are the health signal.

## Rule for every environment

| Flag | Requires | If the requirement is absent |
|---|---|---|
| `STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST` | `STAYCONNECT_PHASE3_PMS_AUTH` (and a real PMS) | keep it **false** |
| `STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_ADMIN` | none | may be **true** |
| `STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME` | none | may be **true** |
| `STAYCONNECT_PHASE5_POSTSTAY_GUEST` | `STAYCONNECT_PHASE3_PMS_AUTH` (and a real PMS) | keep it **false** |

The independent admin surface and AGGREGATE_ONLINE_TIME are unaffected: only the
PMS-dependent **guest** surfaces are gated.

Do not satisfy the dependency by fabricating PMS state. Where no PMS exists, the
guest surfaces are **ENVIRONMENT-BLOCKED**, not failed and not passed.

Checked by `deploy/scripts/check-phase6-guest-dependency.sh`, which is safe to run
against any env directory and exits non-zero on a combination that would crash.
