#!/usr/bin/env bash
# Run a disposable-PostgreSQL gate under the Phase-3 CI retry policy.
#
# Usage:  scripts/ci/pg-gate.sh <gate-script>
#
# The gate scripts exit 2 for a genuine INFRASTRUCTURE failure (the disposable
# container would not build under runner load) and 1 for a failed ASSERTION. Exit 2
# may be retried ONCE; exit 1 is final and is never retried, because a second run that
# passed would report green while hiding an order- or timing-dependent defect. Every
# retry and its original classification is recorded for the evidence artifact.
set -uo pipefail

gate="${1:?gate script required}"
: "${EVID:?EVID must be set}"

bash "$gate"; rc=$?
if [ "$rc" -eq 2 ]; then
  echo "::warning::disposable infrastructure failed to build for $gate; retrying ONCE (assertions were never reached)"
  printf '%s\toriginal_rc=2 (infrastructure)\tretried_once\n' "$gate" >> "$EVID/infra-retries.tsv"
  bash "$gate"; rc=$?
fi
exit "$rc"
