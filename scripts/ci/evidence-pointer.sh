#!/usr/bin/env bash
# THE EVIDENCE ARTIFACT MUST EXIST FOR THE COMMIT THAT WAS ACCEPTED.
#
# Each phase gate uploads an evidence artifact named for the delivery HEAD, and governance refers to it that
# way. When evidence reuse was first introduced it guarded the assemble/upload steps along with everything
# else, and the result was measurable: the three master-leg runs on 15624cbf produced ZERO artifacts, where
# every master run before it had produced one. That is not a weakened check -- it is a missing record, and a
# reviewer looking for the evidence of the commit that actually landed on master would have found nothing.
#
# Assembling a full artifact on a reuse hit is not the answer either: the staging directory is empty because
# the steps that fill it were legitimately skipped, so anything written would be a fabrication. Phase 5's
# publisher exists precisely to refuse an incomplete run.
#
# So a hit writes a POINTER: an artifact under the accepted commit's own name that says, in plain terms, that
# this gate's tree-pure verdict came from a named earlier run over an identical tree, and where the full
# evidence for that tree lives. It claims nothing it did not do.
#
# Usage: evidence-pointer.sh <staging-dir> <gate label>
set -euo pipefail

DIR="${1:?usage: evidence-pointer.sh <staging-dir> <gate label>}"
GATE="${2:?usage: evidence-pointer.sh <staging-dir> <gate label>}"
mkdir -p "$DIR"

SHA="${GITHUB_SHA:-unknown}"
TREE="$(git rev-parse "$SHA^{tree}" 2>/dev/null || echo unknown)"

{
  echo "# $GATE — evidence reused, not recomputed"
  echo
  echo "This run did not re-derive the tree-pure part of this gate. It found an earlier successful run of"
  echo "the SAME workflow over an IDENTICAL git tree, on an ANCESTOR commit, within the evidence-age limit,"
  echo "and reused that verdict. The full evidence for this exact content is the artifact of that run."
  echo
  echo "- commit accepted here: \`$SHA\`"
  echo "- tree (the evidence key): \`$TREE\`"
  echo "- reused from run: \`${REUSE_MATCHED_RUN:-unknown}\`"
  echo "- which validated commit: \`${REUSE_MATCHED_SHA:-unknown}\`"
  echo "- that run started: \`${REUSE_MATCHED_AT:-unknown}\`"
  echo "- this run: \`${GITHUB_RUN_ID:-unknown}\`"
  echo "- full evidence: https://github.com/${GITHUB_REPOSITORY:-aibrahiiim1/StayConnectEnterprise}/actions/runs/${REUSE_MATCHED_RUN:-}"
  echo
  echo "## What still ran in THIS run"
  echo
  echo "Every step governance/ci-reuse-policy.json classifies \`always\` — the checks whose inputs are not the"
  echo "tree. For this repository that is git-history validation, the live dependency-advisory gate, the live"
  echo "pull-request metadata check, and the artifact publication this file is part of. Only steps classified"
  echo "\`tree-pure\` were satisfied by the reused verdict."
} > "$DIR/EVIDENCE-REUSED.md"

echo "  wrote $DIR/EVIDENCE-REUSED.md (pointer to run ${REUSE_MATCHED_RUN:-unknown})"
