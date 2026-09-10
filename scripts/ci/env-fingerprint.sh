#!/usr/bin/env bash
# WHAT EXECUTION ENVIRONMENT IS THIS GATE ACTUALLY RUNNING IN?
#
# Evidence reuse skips a step on the claim that an earlier run already answered it. Tree equality proves the
# INPUTS AND THE CODE are identical. It does not prove the MACHINE is. These gates ask for `ubuntu-latest`,
# Go `1.25`, Node `20`, Python `3.12` and version-tagged actions -- every one of those is a moving target
# resolved at run time, and all of them can move inside any recency window. A run that sets up today's
# environment and then skips the build and tests on the strength of a verdict produced under a different
# toolchain is asserting something nobody measured.
#
# So the environment is not assumed equal, it is MEASURED and made part of the evidence key. This prints a
# canonical block of everything that determines how the skippable work would execute, and a SHA-256 over it:
#
#     EVIDENCE_ENV_FINGERPRINT=<64 hex>
#
# scripts/ci/evidence-reuse.sh reads that token back out of the CANDIDATE RUN'S OWN LOG through the API and
# refuses to reuse anything whose fingerprint differs. The comparison is therefore against GitHub's own
# record of what that run really ran on -- not against a promise in a config file.
#
# WHAT IS IN THE FINGERPRINT, and why each one is there:
#
#   runner os / arch          the platform every skipped command would run on.
#   hosted image + version    `ubuntu-latest` is a pointer. The image behind it is rebuilt continuously, and
#                             its version (e.g. 20260907.300.1) is the only honest name for "this machine".
#                             It also fixes the preinstalled software the gates rely on -- docker, psql
#                             clients, build tools -- none of which the tree pins.
#   kernel release            the Phase-3 REAL-KERNEL contract suite runs against real network namespaces.
#                             Its verdict is a function of the kernel, which the tree does not fix.
#   resolved toolchains       `go-version: '1.25'` resolves to whatever patch is current. The resolved
#                             `go version`, `node -v`, `npm -v` and `python3 -V` are what actually compiled
#                             and ran the tests. A tool absent at this point is recorded as absent, so a
#                             gate that gains or loses a toolchain changes fingerprint.
#   container image digests   the disposable-PG gates pull mutable tags like `postgres:16-alpine`. A re-push
#                             of that tag is exactly the same class of drift as a runner-image rebuild, so
#                             the digest is resolved (without pulling) and included. Only the images a given
#                             gate actually uses are passed in; governance passes none and stays off the
#                             network.
#
# WHAT IS DELIBERATELY NOT IN IT, with the reason:
#
#   action versions           `actions/checkout@v4`, `actions/setup-*` and `upload-artifact` are the ONLY
#                             `uses:` steps in these gates, and every one of them is classified `always` --
#                             tools/validate-ci-reuse-policy.py FAILS the build if a reuse-eligible step is
#                             ever an action. So no action's behaviour is ever inherited from an earlier run:
#                             they all re-execute. What a setup action leaves behind IS inherited, and that
#                             is precisely the resolved toolchain version captured above.
#   runner agent version      it orchestrates steps; the skipped work is shell, Python and Go invoked inside
#                             the image, whose behaviour the image and toolchain versions fix.
#
# FAIL CLOSED. If the image identity cannot be determined, or a required image digest cannot be resolved,
# this prints an EMPTY fingerprint. An empty fingerprint never matches, so the caller runs the full
# validation. The one thing it must never do is emit a stable placeholder: two runs that both failed to
# measure their environment would then "agree", which is the silent weakening this whole file exists to stop.
#
# Usage:  env-fingerprint.sh [container-image ...]
# Emits to $GITHUB_OUTPUT:  fingerprint=<64 hex or empty>
set -uo pipefail

OUT="${GITHUB_OUTPUT:-/dev/stdout}"
emit() { printf '%s\n' "$*" >> "$OUT"; }

PY=python3; python3 --version >/dev/null 2>&1 || PY=python
if ! "$PY" --version >/dev/null 2>&1; then
  echo "  no usable python: the environment cannot be fingerprinted"
  echo "EVIDENCE_ENV_FINGERPRINT="
  emit "fingerprint="
  exit 0
fi

echo "== execution-environment fingerprint =="

fail_open_guard=0
note_missing() { echo "  CANNOT MEASURE: $*"; fail_open_guard=1; }

# ---------------------------------------------------------------------------------------------------------
# The hosted image identity. ImageOS/ImageVersion are set on GitHub-hosted runners; imagedata.json is the
# same information written into the image itself, kept as a second source rather than a guess.
IMAGE_OS="${ImageOS:-}"
IMAGE_VERSION="${ImageVersion:-}"
if [ -z "$IMAGE_VERSION" ] && [ -r /imagegeneration/imagedata.json ]; then
  IMAGE_VERSION="$("$PY" -c '
import json, io, sys
try:
    d = json.load(io.open("/imagegeneration/imagedata.json", encoding="utf-8"))
except Exception:
    sys.exit(0)
if isinstance(d, list):
    for e in d:
        g = (e or {}).get("group") or ""
        if "Image" in g:
            for k in ("Version", "version"):
                if (e or {}).get(k):
                    print(e[k]); sys.exit(0)
' 2>/dev/null)"
fi
[ -n "$IMAGE_OS" ] || IMAGE_OS="${RUNNER_ENVIRONMENT:-unknown-image-os}"
[ -n "$IMAGE_VERSION" ] || note_missing "the hosted runner image version (ImageOS/ImageVersion unset and /imagegeneration/imagedata.json unreadable)"

# ---------------------------------------------------------------------------------------------------------
# Resolved toolchains. `absent` is a real, distinguishing value: a gate that starts installing Node must not
# share a fingerprint with the version of itself that did not.
tool() { local out; out="$("$@" 2>/dev/null | head -1 | tr -d '\r')"; [ -n "$out" ] && printf '%s' "$out" || printf 'absent'; }

GO_V="$(tool go version)"
NODE_V="$(tool node -v)"
NPM_V="$(tool npm -v)"
PY_V="$(tool "$PY" -V)"
KERNEL="$(uname -sr 2>/dev/null || echo unknown)"

# ---------------------------------------------------------------------------------------------------------
# Container image digests, resolved WITHOUT pulling. Retried, because a transient registry blip must not be
# confused with a changed image -- and if it never resolves, the fingerprint goes empty and the full gate
# runs, which costs time and never correctness.
IMAGE_LINES=""
for img in "$@"; do
    dig=""
    for attempt in 1 2 3; do
        dig="$(docker manifest inspect "$img" 2>/dev/null | "$PY" -c '
import hashlib, sys
data = sys.stdin.buffer.read()
if not data.strip():
    sys.exit(1)
print(hashlib.sha256(data).hexdigest())
' 2>/dev/null)"
        [ -n "$dig" ] && break
        sleep "$attempt"
    done
    if [ -z "$dig" ]; then
        note_missing "the digest of container image $img"
        dig="UNRESOLVED"
    fi
    IMAGE_LINES="${IMAGE_LINES}image[$img]=$dig
"
done

BLOCK="$(printf '%s\n' \
  "runner_os=${RUNNER_OS:-unknown}" \
  "runner_arch=${RUNNER_ARCH:-unknown}" \
  "image_os=$IMAGE_OS" \
  "image_version=$IMAGE_VERSION" \
  "kernel=$KERNEL" \
  "go=$GO_V" \
  "node=$NODE_V" \
  "npm=$NPM_V" \
  "python=$PY_V"
printf '%s' "$IMAGE_LINES")"

echo "$BLOCK" | sed 's/^/  /'

if [ "$fail_open_guard" -ne 0 ]; then
  echo "  -> the environment could not be fully measured, so this run publishes NO fingerprint."
  echo "     Reuse will not match it and the full validation will run. That is the intended direction."
  echo "EVIDENCE_ENV_FINGERPRINT="
  emit "fingerprint="
  exit 0
fi

FP="$(printf '%s' "$BLOCK" | "$PY" -c '
import hashlib, sys
print(hashlib.sha256(sys.stdin.buffer.read()).hexdigest())
')"
if [ -z "$FP" ]; then
  echo "EVIDENCE_ENV_FINGERPRINT="
  emit "fingerprint="
  exit 0
fi

# The token below is what a LATER run greps out of this run's log. It is printed unindented and alone on its
# line so the extractor can anchor on it; every other mention of the value in these scripts is indented or
# labelled differently on purpose.
echo "EVIDENCE_ENV_FINGERPRINT=$FP"
emit "fingerprint=$FP"
