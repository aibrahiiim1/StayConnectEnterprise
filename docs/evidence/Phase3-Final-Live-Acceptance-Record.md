# Phase 3 — Final Live-Acceptance Record

**This document is the post-CI record.** It exists because the Software CI evidence artifact cannot be it.

The artifact for a run is written *during* that run and is immutable afterwards. Its snapshot of the world is
therefore true as of the moment the gate finished and no later — it cannot know what happened on the appliance
twenty minutes after it was uploaded, and it must not be repacked to pretend otherwise. So the artifact remains
the **software-gate** evidence, and this file is the **live-closure and acceptance** evidence. Read together
they are the complete picture; read separately, each is honest about what it covers.

| | Software CI artifact `9098688283` | This record |
|---|---|---|
| covers | the software gate on HEAD `7c8b8cf0…` | the live closure performed *after* that gate, and the Product-Owner acceptance |
| written | during the CI run, then sealed | after the live closure |
| snapshot fields | historical to the run's own moment | current as of acceptance |

---

## 1. Accepted runtime candidate

**`7c8b8cf019c5af3dd2294ee268e8f7137e6ef5d4`** — the build the appliance runs.

Governance/closure work recorded after acceptance lives on a later HEAD; the runtime tree was byte-for-byte
identical between them at the time of acceptance, and that is proven in §6. *Later* post-closure remediation
(T0026, T0027) changed Caddy deployment material and operational tooling under `deploy/` and `scripts/` —
the accepted application/runtime binaries remain unchanged.

## 2. The order things actually happened

Taken from GitHub Actions run metadata and the preserved live-closure evidence, not from recollection.

| # | Stage | Evidence |
|---|---|---|
| 1 | Repository correction (rollback boundary + pmsd contract) | T0023 written `10:23:22Z`; inventory `11411b9f…`, delivery `88c6deba…` |
| 2 | Same-HEAD gates on `88c6deb` | both started `10:34:52Z`; Software **31482837021** SUCCESS `10:40:44Z`; Governance **31482837046** SUCCESS `10:41:24Z` |
| 3 | Authorized live closure, **after** those gates | DARK pmsd first installed/started `10:43:57Z`; rollback boundary exercised with the live legacy set EMPTY; reboot `10:45:08Z`; required-state proof `10:47:44Z` |
| 4 | Second repository correction | the dark pmsd env mentioned flag **names** in comments, tripping the appliance darkness grep; corrected. inventory `b9cf8330…`, delivery `7c8b8cf0…` |
| 5 | Same-HEAD gates on `7c8b8cf` | both started `10:56:59Z`; Software **31484446685** SUCCESS `11:03:15Z`; Governance **31484446661** SUCCESS `11:03:26Z` |
| 6 | Final authorized live closure, **after** those gates | final candidate deployed and verified on disk and in `/proc/<pid>/exe`; pmsd reinstalled from the corrected env; darkness grep clean; reboot `11:04:00Z`; final required-state proof `11:06:43Z` |
| 7 | Product-Owner acceptance | decision **D16**, receipt **T0024** |
| 8 | Product-Owner repository closure and merge, **after** a full re-validation on the merged head | gates **31496059979** / **31496060002** SUCCESS and artifact **9103191800** verified on head `fb25fd43…`, zero runtime diff vs the accepted candidate; merged SHA-pinned `13:35:59Z` as `8a7230a7…`; post-merge gates **31497023194** / **31497023118** SUCCESS on the merge commit; decision **D17**, receipt **T0025** |

**On T0023.** Its verdict said the two closure findings were "closed in software and verified live". At
`10:23:22Z` the live half had not happened and the gates had not run. The software claim was true; the live
claim was premature. T0023 is preserved unaltered as the record of stage 1 — T0024 supersedes only that
wording. The work itself was performed in the correct order, as the table above shows.

## 3. Final live state at acceptance

Observed on the appliance after the final controlled reboot (boot `c825ae9f…`, `11:06:43Z`).

**Runtime identity — on disk *and* in the running process:**

| binary | sha256 |
|---|---|
| scd | `c2caa6690a52583cfbacd561d8927c1da9fdb461dbfd05c2044bf881d99ca4f4` |
| netd | `ce74f7f69255d8faa00f7a07d036036d710de6e9ead155ae2c306bde73f244b0` |
| acctd | `5ccc2a206d75e373ddeeb96ae23acdf74cbce8a809d866990da56f35b29820bd` |
| portald | `a00a9e8500081ebd3b721ea95410245b47946c37408b08d9ffb53983b351d42e` |
| edged | `b6e5b301e659046cae87ad0868b4842eb42bfca520e8b24516aab569c19002d3` |
| pmsd (installed, DARK) | `eda8fb8d927d9cbe6c34e4962bfeada84db0e0dedec55a6606601eb0da27dca7` |

**DARK safety:** all six Phase-3 flags absent from every env file and unit and `0` in every running process;
`netd phase3 shaping writer active=false`; `phase3_auth_ipv4` **present and empty**; render marker
`aa3d99d18590b220689cfceab197da0c`; legacy `auth_ipv4` present and empty; 6 captive DNAT rules; portal
listening; kea active; confirmed active revision `0cb0028c…` reconstructed on boot.

**Schema:** migration 0010 applied; iam_v2 **63 tables / 0 rows**; 4 controlled functions; **zero** `iam_v2`
grants to any `svc_*` role; `pms_postings`, `posting_outbox` and `payment_transactions` all **0**.

**pmsd DARK contract, after the reboot:** `enabled`, `result=success`, `ExecMainStatus=0`, `NRestarts=0`, runs
as `stayconnect-pmsd`; logs `connector and ingest flags OFF; no assignment, DB, secret, worker or PMS socket`;
**0** processes, **0** sockets, **0** connections to the PMS port, **0** database connections.

**Health:** 7 services running, **0 failed**.

## 4. Accepted limitation — NOT PROVEN

> **Legacy live-session continuity = NOT PROVEN — no real legacy guest was available during the authorized
> live windows.**

`auth_ipv4` held zero elements at every checkpoint of every authorized window. No guest, session or
authorization was fabricated to turn this into a PASS, and it is **not** promoted to one here.

It is accepted at DARK maturity because the populated-session behaviour it would exercise is covered by the
disposable real-kernel suite — a real populated authorization set, real packets, and the assertion that the
guest stays online across convergence and across a refused rollback. It may be observed opportunistically when
a genuine legacy guest is naturally online. That observation does not by itself reopen Phase 3 unless it
reveals a real defect.

## 5. What acceptance does *not* authorize

DARK maturity only. No IAM-v2 cutover, no Phase-3 feature enablement, no PMS financial posting, no paid access,
no `PS`/`PA`, no implicit FX, no programmatic reversal, no Gate-P runtime grants, no Phase 4. Legacy
public-schema IAM remains the sole production authority.

**PR #6 was subsequently MERGED into `master`** on 2026-08-11, as merge commit `8a7230a7220e4c773bfb6399ce7774f31f20c906`, under the separate
Product-Owner merge authorization recorded as decision **D17** / transition **T0025**. That merge is a
repository event: it deployed nothing, enabled nothing and contacted nothing, and it did not widen what
acceptance authorizes. Everything this section withholds is still withheld.

## 6. No live action in the acceptance or merge rounds

The acceptance/closure round that produced this record changed governance, documentation and governance
validators only. **No appliance action was required or performed**, no database was contacted, and no PMS
traffic occurred. The runtime tree is byte-for-byte identical to the accepted candidate — proven by
`git diff <accepted> <closure> -- data-plane/ hotel-admin/ deploy/ */migrations/*` being empty, which the
Software CI on the closure HEAD re-confirms by rebuilding and re-testing that unchanged tree.

The same is true of the merge round that followed. Merging PR #6 into `master` was a repository event: the
runtime diff against the accepted candidate was re-proven zero across `data-plane/`, `hotel-admin/`,
`control-plane/`, `deploy/`, `scripts/` and every migration path *before* the merge was issued, the merge
commit introduced no content, and no appliance, Production database or PMS was contacted at any point in it.
