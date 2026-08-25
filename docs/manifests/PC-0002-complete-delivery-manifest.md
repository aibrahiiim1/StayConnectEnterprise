# PC-0002 — complete delivery manifest

The final report for the PC-0002 portald forwarding correction carried a **partial** file manifest:
it listed the six files that were interesting to read and silently dropped every generated pack,
export artefact and checksum file the delivery also changed. A manifest that lists only the
human-authored paths is not a manifest — it is a summary that reads like one, and it understates
what actually entered master.

This document is the reconstruction, taken from the GitHub compare API for each merged pull
request rather than from memory. Counts here match what GitHub reports for each PR.

| PR | Title | Head | Merge commit | Paths | Status |
|---|---|---|---|---|---|
| #42 | D38 preparation runbook | `92dd151` | `92dd151` (own head) | **3** | MERGED — unintentionally, see below |
| #43 | portald forwarding fix (PC-0002) | `006a743` | `1482890` | **28** | MERGED |
| #44 | PC-0002 deployment evidence | `5e4cdf7` | `82254f9` | **13** | MERGED |

**Unique paths across PR #43 and PR #44: 29.** Twelve paths are touched by both PRs and are
counted once in that total; they are listed explicitly further down, because "28 + 13" invites the
reader to assume 41 and there is no way to tell from the two numbers alone that it is not.

---

## PR #43 — 28 paths

The portald fix itself is two files. The other twenty-six are the D38 runbook that rode along on
the same branch (see the PR #42 note) and the generated packs and checksums that any records
change regenerates.

### Source

| Status | Path |
|---|---|
| added | `data-plane/cmd/portald/pms_phase3_forwarding_test.go` |
| modified | `data-plane/cmd/portald/pms_phase3_handlers.go` |

### Governance

| Status | Path |
|---|---|
| modified | `governance/project-state.json` |
| added | `governance/transitions/T0088.json` |

### Documentation

| Status | Path |
|---|---|
| added | `docs/runbooks/Guest-Access-End-To-End-Acceptance.md` |
| modified | `docs/manifests/PostClosure-change-manifest.md` |
| modified | `docs/architecture/StayConnect-IAM-Phase0-Contract.md` |
| modified | `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` |
| modified | `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` |
| modified | `docs/context/StayConnect-IAM-Handoff.md` |

### Generated exports and packs

| Status | Path |
|---|---|
| modified | `exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip` |
| modified | `exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip` |
| modified | `exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip` |
| removed | `exports/chatgpt/phase-evidence/GIT_STAT_2724417.txt` |
| added | `exports/chatgpt/phase-evidence/GIT_STAT_a66c005.txt` |
| modified | `exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt` |
| modified | `exports/chatgpt/phase-evidence/REPOSITORY_ARTIFACT_SHA256SUMS.txt` |
| modified | `exports/chatgpt/phase1b-planning/MANIFEST.md` |
| modified | `exports/chatgpt/phase1b-planning/PACK_SHA256SUMS.txt` |
| modified | `exports/chatgpt/phase1b-planning/REPOSITORY_ARTIFACT_SHA256SUMS.txt` |
| modified | `exports/chatgpt/phase1b-planning/StayConnect-IAM-Phase1B-Plan.md` |
| modified | `exports/chatgpt/stayconnectenterprise/00-START-HERE.md` |
| modified | `exports/chatgpt/stayconnectenterprise/MANIFEST.md` |
| modified | `exports/chatgpt/stayconnectenterprise/PROJECT-INSTRUCTIONS.md` |
| modified | `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Handoff.md` |
| modified | `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase0-Contract.md` |
| modified | `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase1A-Plan.md` |
| modified | `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase1B-Plan.md` |

---

## PR #44 — 13 paths

Records only. No source file appears here, which is the point: the runtime was already deployed and
this PR only wrote down what the deployment proved.

| Status | Path |
|---|---|
| modified | `governance/project-state.json` |
| modified | `docs/manifests/PostClosure-change-manifest.md` |
| modified | `exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip` |
| modified | `exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip` |
| modified | `exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip` |
| removed | `exports/chatgpt/phase-evidence/GIT_STAT_a66c005.txt` |
| added | `exports/chatgpt/phase-evidence/GIT_STAT_fb360ab.txt` |
| modified | `exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt` |
| modified | `exports/chatgpt/phase-evidence/REPOSITORY_ARTIFACT_SHA256SUMS.txt` |
| modified | `exports/chatgpt/phase1b-planning/MANIFEST.md` |
| modified | `exports/chatgpt/phase1b-planning/PACK_SHA256SUMS.txt` |
| modified | `exports/chatgpt/phase1b-planning/REPOSITORY_ARTIFACT_SHA256SUMS.txt` |
| modified | `exports/chatgpt/stayconnectenterprise/MANIFEST.md` |

---

## The twelve paths touched by both PRs

Counted once in the 29. Note the third row: `GIT_STAT_a66c005.txt` was **added** by PR #43 and
**removed** by PR #44 — the same path, opposite operations, because each delivery stamps the pack
with its own head and retires the previous stamp.

| Path | in #43 | in #44 |
|---|---|---|
| `governance/project-state.json` | modified | modified |
| `docs/manifests/PostClosure-change-manifest.md` | modified | modified |
| `exports/chatgpt/phase-evidence/GIT_STAT_a66c005.txt` | added | removed |
| `exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip` | modified | modified |
| `exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip` | modified | modified |
| `exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip` | modified | modified |
| `exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt` | modified | modified |
| `exports/chatgpt/phase-evidence/REPOSITORY_ARTIFACT_SHA256SUMS.txt` | modified | modified |
| `exports/chatgpt/phase1b-planning/MANIFEST.md` | modified | modified |
| `exports/chatgpt/phase1b-planning/PACK_SHA256SUMS.txt` | modified | modified |
| `exports/chatgpt/phase1b-planning/REPOSITORY_ARTIFACT_SHA256SUMS.txt` | modified | modified |
| `exports/chatgpt/stayconnectenterprise/MANIFEST.md` | modified | modified |

---

## PR #42 — 3 paths, and why they are inside PR #43's diff

| Status | Path |
|---|---|
| added | `docs/runbooks/Guest-Access-End-To-End-Acceptance.md` |
| modified | `governance/project-state.json` |
| modified | `docs/manifests/PostClosure-change-manifest.md` |

All three appear in PR #43's file list as well. That is not a coincidence and it is the whole
explanation of what went wrong: **the PR #43 branch was cut from the PR #42 branch, not from
master.** Every commit of PR #42 was therefore an ancestor of PR #43's head, and merging PR #43
carried them into master.

GitHub then did what it always does with a pull request whose commits have become reachable from
the base branch: it marked PR #42 merged and closed it. The fingerprints are unambiguous —

* `merge_commit_sha` for PR #42 equals **its own head** `92dd151`, not a merge commit. A deliberate
  merge produces a distinct merge commit; this one has none because no merge was performed.
* `merged_at` for PR #42 is `2026-08-24T23:03:34Z`, **two seconds after** PR #43's
  `2026-08-24T23:03:32Z`.
* No merge API call was ever issued against PR #42.

Verified by ancestry, not inference:

```
git merge-base --is-ancestor 92dd151 006a743   ->  true
git merge-base --is-ancestor 92dd151 master    ->  true
```

**This happened against the standing instruction to leave PR #42 unmerged.** It was a consequence
of branching carelessly, and it is recorded here as such. It is not being reinterpreted as prior
authorization, and nothing about the D38 runbook's status changes because of it: the runbook is
**preparation only**, it grants no execution authority and no Go-Live authority, and its being in
master does not alter that by one word.
