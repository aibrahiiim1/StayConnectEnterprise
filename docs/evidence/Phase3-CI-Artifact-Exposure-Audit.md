# Phase 3 - Historical CI Artifact Exposure Audit

**Read-only security/evidence audit. No artifact was deleted and no credential was rotated.**

This records the completed audit of retained GitHub Actions artifacts produced *before* the Playwright
metadata allowlist fix landed in **T0027's predecessor, T0026**. It is written so the conclusion survives
the artifacts themselves: every one of them expires within 90 days, and after that the only remaining
record of what they contained is this file.

## 1. Scope and population

Every artifact retained by the repository at the time of the audit: **36**, none expired. All are
`phase3-software-evidence-<head>` bundles produced by the Phase 3 Software CI workflow.

| | Count |
|---|---|
| Inspected | 36 |
| **AFFECTED** - carried embedded raw repository diff | **31** |
| **CLEAN** - no embedded diff | **5** |
| Expired / unreadable / download failed | 0 |

## 2. What the defect was

Playwright's JSON reporter captures git information on `pull_request` events and writes
`metadata.gitDiff` - the pull request's **entire diff, truncated at 100,000 characters** - into
`counts/playwright.json`. That file was copied into the evidence artifact verbatim, so the artifact
carried up to 100 KB of raw repository content while its own README promised "derived summaries only".

The reporter stores `metadata` in more than one place. Both were affected in all 31:

| Location inside `counts/playwright.json` | Artifacts |
|---|---|
| `config.metadata.gitDiff` | 31 |
| `config.projects[0].metadata.gitDiff` | 31 |

**The defect is already fixed** (T0026): the generator now reduces every `metadata` object in the
document to an allowlist of provenance fields, and `tools/tests/evidence_artifact/run_artifact_staleness.py`
fails the Governance gate if any copy of a diff survives anywhere in the report.

## 3. Methodology - deliberately bounded

Each artifact was downloaded, unpacked and searched **structurally**: every string at every path in every
JSON file, plus every non-JSON file, was tested for unified-diff markers. Anything found was then measured
rather than displayed:

- character count and SHA-256 of the embedded blob;
- the set of repository paths the diff names (path names only - never hunk content);
- whether the blob hit the 100,000-character truncation cap;
- pattern probes for material that would exceed ordinary repository-visible content: private-key blocks,
  credentialed DSNs, GitHub / AWS / Slack tokens, JWTs, assigned secret literals, non-project email
  addresses and PAN-like digit runs. Probes report **category and count only**.

**No discovered content, and no possible secret-like value, is reproduced in this record or was written to
any report.** That constraint was applied to the tooling, not just to the write-up.

## 4. What was proven

- **Sensitivity probes matched ZERO times across all 31 affected artifacts.** No secret, credential,
  key, token or personal-data category was detected.
- Every blob is exactly **100,000 characters** - the truncation cap - and truncation is in path order, so
  all 31 stop inside `.github`, `.gitignore` and `data-plane`: **9 distinct repository paths**, every one
  of them tracked or historical in this repository.
- A representative blob (artifact `9103191800`, PR #6 head `fb25fd43`, pre-fix) was checked
  line-by-line: **0 lines that are not unified-diff structure** - pure diff, no mixed or generated
  content. Blob SHA-256 `4b32ac89b01bc08ab5fd0e076d8153555e40db7e8683fc5bf055eb091214f3b9`.
- 10 distinct blob digests across 31 artifacts: repeated PR heads produce identical blobs.
- **Exposure is confined to `pull_request`-triggered runs.** Playwright only captures git info on PR
  events, which is why four pre-fix `push` artifacts are clean for a structural reason rather than by luck.

| Verdict | `pull_request` runs | `push` runs |
|---|---|---|
| AFFECTED | 31 | 0 |
| CLEAN | 1 (post-fix) | 4 |

## 5. What was NOT proven

- The probes are pattern-based. They establish that **no material matching known secret or personal-data
  shapes is present**; they cannot prove the absence of a secret that resembles ordinary source code.
- The audit covers artifacts **retained at the time it ran**. Artifacts that had already expired were
  outside it and cannot be retrieved.
- It does not assess GitHub-side access logs: who, if anyone, downloaded these artifacts is not knowable
  from the repository side.

## 6. Residual risk and why no containment was performed

The repository is **public**. Downloading an Actions artifact requires repository read access, which for a
public repository is the same audience that can already read the code. The embedded material is a
truncated diff of commits that are themselves published, contains no non-repository or generated data, and
matched no sensitivity probe.

**Residual risk: LOW. No additional exposure beyond what the repository already publishes.**

Consequently:

- **No artifact was deleted.** They are Phase-3 evidence; destroying evidence to remove content that is
  already public would cost more than it protects.
- **No credential was rotated.** Nothing credential-shaped was found, so rotation would be a reaction to
  an absence of evidence rather than to evidence.
- **Normal expiry is sufficient.** The affected population is closed - no artifact produced after the fix
  is affected - and shrinking. All 31 expire between **2026-10-21 and 2026-11-09**.

## 7. Affected artifacts (31)

| Artifact ID | Run ID | Created (UTC) | Expires | Head |
|---|---|---|---|---|
| `8560377228` | `29999254622` | 2026-07-23 10:30 | 2026-10-21 | `e65e254a2b47` |
| `8560614546` | `29999889669` | 2026-07-23 10:39 | 2026-10-21 | `1f407caadb20` |
| `8563830917` | `30007775670` | 2026-07-23 12:42 | 2026-10-21 | `c885f989c4e9` |
| `8564141812` | `30008496295` | 2026-07-23 12:53 | 2026-10-21 | `1b818f7976f2` |
| `8565016023` | `30010632154` | 2026-07-23 13:23 | 2026-10-21 | `5a2d527eead1` |
| `9057687110` | `31374936476` | 2026-08-10 09:36 | 2026-11-08 | `2742547617ef` |
| `9057917064` | `31375632025` | 2026-08-10 09:44 | 2026-11-08 | `9a1158af8ad6` |
| `9060838983` | `31383325189` | 2026-08-10 11:28 | 2026-11-08 | `4a37c4b10903` |
| `9060977983` | `31383683837` | 2026-08-10 11:33 | 2026-11-08 | `92391dbf40c6` |
| `9061197485` | `31384270613` | 2026-08-10 11:41 | 2026-11-08 | `7bd8acdde4ec` |
| `9061299139` | `31384560722` | 2026-08-10 11:45 | 2026-11-08 | `75fb848a2632` |
| `9064686190` | `31393261721` | 2026-08-10 13:35 | 2026-11-08 | `b830a2a518dc` |
| `9065105522` | `31394278227` | 2026-08-10 13:47 | 2026-11-08 | `33ef1ac255dd` |
| `9065258463` | `31394661769` | 2026-08-10 13:52 | 2026-11-08 | `5cb0ca904d76` |
| `9065382651` | `31394974992` | 2026-08-10 13:55 | 2026-11-08 | `ac07ed39f3c1` |
| `9068102905` | `31401754209` | 2026-08-10 15:11 | 2026-11-08 | `9c09cd63302b` |
| `9069620934` | `31405589261` | 2026-08-10 15:53 | 2026-11-08 | `b346e6d4c6f3` |
| `9071272133` | `31409875498` | 2026-08-10 16:42 | 2026-11-08 | `f1a998aadecd` |
| `9071397817` | `31410178597` | 2026-08-10 16:45 | 2026-11-08 | `83449200a8ac` |
| `9085376559` | `31448031619` | 2026-08-11 01:07 | 2026-11-09 | `5a245a394ddf` |
| `9085591262` | `31448670874` | 2026-08-11 01:19 | 2026-11-09 | `be135f214587` |
| `9092108465` | `31467301402` | 2026-08-11 07:07 | 2026-11-09 | `436d2cc8a3e4` |
| `9095117997` | `31475270609` | 2026-08-11 08:59 | 2026-11-09 | `3d106b805c56` |
| `9095351592` | `31475888895` | 2026-08-11 09:07 | 2026-11-09 | `7318ac239b5b` |
| `9098057154` | `31482837021` | 2026-08-11 10:40 | 2026-11-09 | `88c6deba7ed5` |
| `9098688283` | `31484446685` | 2026-08-11 11:03 | 2026-11-09 | `7c8b8cf019c5` |
| `9099953003` | `31487844807` | 2026-08-11 11:47 | 2026-11-09 | `ddbeccbd2e05` |
| `9100202731` | `31488465424` | 2026-08-11 11:56 | 2026-11-09 | `02d5567f8084` |
| `9100486638` | `31489214390` | 2026-08-11 12:06 | 2026-11-09 | `7f6bcb27af04` |
| `9102252178` | `31493696939` | 2026-08-11 13:03 | 2026-11-09 | `06e81b4baa78` |
| `9103191800` | `31496059979` | 2026-08-11 13:30 | 2026-11-09 | `fb25fd43bb83` |

## 8. Clean artifacts (5)

| Artifact ID | Run ID | Created (UTC) | Head | Why clean |
|---|---|---|---|---|
| `9103599358` | `31497023194` | 2026-08-11 13:42 | `8a7230a7220e` | pre-fix, push-triggered |
| `9104580294` | `31499550407` | 2026-08-11 14:09 | `a98338a6cb03` | pre-fix, push-triggered |
| `9104701678` | `31499875271` | 2026-08-11 14:12 | `ea8867395c27` | pre-fix, push-triggered |
| `9107023796` | `31505528899` | 2026-08-11 15:14 | `2e061ede9914` | post-fix |
| `9107796419` | `31507466909` | 2026-08-11 15:36 | `df55a093c124` | post-fix |
