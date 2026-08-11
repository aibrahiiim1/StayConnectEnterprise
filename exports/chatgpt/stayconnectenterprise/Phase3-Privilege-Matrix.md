# StayConnect IAM — Phase 3 Privilege Matrix (PMS Stay Domain, DARK)

> Machine assertions (governance):
> `PRODUCTION_IAM_V2_DML: NONE`
> `PHASE_3_PRODUCTION_RUNTIME: DARK`
> `PHASE_3_PMS_FINANCIAL_POSTING: NONE`
> `PHASE_3_PMS_LIVE: READ_ONLY`

**Authorization:** D14 / T0015. Base master HEAD `ffb68e1`. This matrix specifies the runtime DB-privilege posture for Phase 3 and is derived from the accepted Phase-1B Gate-P least-privilege baseline (`docs/architecture/Phase1B-Privilege-Matrix.md`), which it preserves unchanged. Phase 3 adds **zero** new production runtime privileges while dark.

## 1. Production runtime service roles vs iam_v2 (dark)

No production runtime service role holds any `iam_v2` INSERT/UPDATE/DELETE/SELECT/EXECUTE grant (`PRODUCTION_IAM_V2_DML: NONE`). This is unchanged from the Phase-1B Gate-P deployment: the four site-DB daemons connect under least-privilege `svc_*` roles (`svc_scd`, `svc_edged`, `svc_acctd`, `svc_netd`; NOSUPERUSER, SCRAM), and none is granted any `iam_v2` object privilege. Phase 3 introduces the new `pmsd` daemon; while `STAYCONNECT_PHASE3_*` flags are OFF (the delivered/deployed state), `pmsd` opens no PMS connection and performs zero `iam_v2` DML, and its role is granted **no** `iam_v2` runtime privilege.

| Runtime role | iam_v2 DML | iam_v2 SELECT | iam_v2 EXECUTE | Notes |
|---|---|---|---|---|
| `svc_scd` | NONE | NONE | NONE | legacy public-schema auth only; Gate-P baseline preserved |
| `svc_edged` | NONE | NONE | NONE | Gate-P baseline preserved |
| `svc_acctd` | NONE | NONE | NONE | Gate-P baseline preserved |
| `svc_netd` | NONE | NONE | NONE | Gate-P baseline preserved |
| `svc_pmsd` (new, Phase-3) | NONE | NONE | NONE | dedicated read-only PMS connector; **no iam_v2 grant while dark**; opens no socket while flags OFF |
| `PUBLIC` | NONE | NONE | NONE | denied |

## 2. iam_v2 object ownership (unchanged)

All Phase-3 iam_v2 objects — the existing canonical tables (mg1–mg9) and the additive migration `0010_phase3_stay_resolution` (new `pms_interface_runtime` table, one-way status / monotonic-version triggers, checkout-episode/effective-checkout columns, grace-window scalars, optional dedup key + stay_events append-only guard) — are owned by `iam_v2_owner`. Runtime service roles are never granted DML on them. Migration application uses the `iam_v2_owner` role only, out of the guest runtime path.

## 3. Live PMS access posture

Phase-3 live PMS interaction is **READ-ONLY** (`PHASE_3_PMS_LIVE: READ_ONLY`). The connector sends only verified read-only FIAS records (`LS/LD/LR/LA`, `GI/GC/GO`, read-only `DR` resync). There is **no `PS` sender** and **no financial posting** in the Phase-3 deliverable (`PHASE_3_PMS_FINANCIAL_POSTING: NONE`). Live-verification persistence targets an isolated disposable test DB — never Production iam_v2. A single-owner DB advisory lock per `(tenant,site,pms_interface_id)` guarantees exactly one connector owns an Interface socket.

## 4. Darkness invariant

With all `STAYCONNECT_PHASE3_*` flags OFF (default; the deployed state): no PMS connection is opened, no Phase-3 repository is constructed, zero Phase-3 SQL executes, no PMS event is ingested, no Phase-3 auth route or UI surface is served, no Grace executes, and no runtime iam_v2 grant exists. The legacy public-schema pipeline remains the sole production authority; iam_v2 remains 49 tables / 0 rows in Production plus the additive 0010 objects (structural only, 0 Production Phase-3 rows).
