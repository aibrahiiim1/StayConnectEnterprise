#!/usr/bin/env bash
# Real-kernel evidence for netd's pipeline-integrity work (bridge ownership, adoption, a hostile network name against the real nft parser).
#
# Runs ONLY inside a throwaway privileged Linux container, so every interface and ruleset it creates lives in that container's own network namespace
# and is gone when it exits. It touches no appliance, no Central, no PMS, and nothing on the machine that runs docker.
#
#   scripts/ci/netd-integrity-kernel.sh            # builds nothing on the host; needs docker and a local golang:1.25 image
#
# Exit codes: 0 pass, 1 assertion failure, 2 infrastructure (the container could not be started).
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/../.." && pwd -P)"
IMAGE="${NETD_KERNEL_IMAGE:-golang:1.26.10}"
MODCACHE="$(cd "$ROOT/data-plane" && go env GOMODCACHE 2>/dev/null || true)"

docker image inspect "$IMAGE" >/dev/null 2>&1 || { echo "INFRA: image $IMAGE is not available locally"; exit 2; }

mods=()
if [ -n "$MODCACHE" ] && [ -d "$MODCACHE" ]; then
  mods=(-v "$MODCACHE:/go/pkg/mod:ro")
fi

MSYS_NO_PATHCONV=1 docker run --rm --privileged \
  -v "$ROOT:/src:ro" "${mods[@]}" \
  -e NETD_KERNEL_DISPOSABLE=yes -e GOFLAGS=-mod=readonly -e GOCACHE=/tmp/gocache -e GOTMPDIR=/tmp \
  -w /src/data-plane "$IMAGE" bash -c '
    set -e
    apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq iproute2 nftables >/dev/null 2>&1 || { echo "INFRA: could not install iproute2/nftables"; exit 2; }
    go test -tags netdkernel -count=1 -v -run "TestKernel_" ./cmd/netd/
  '
rc=$?
[ "$rc" = 2 ] && echo "INFRA failure" >&2
exit "$rc"
