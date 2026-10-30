#!/bin/bash
# build.sh <tags> <svc...>  : build lab binaries into D:/tmp/oglab3/rel/bin
TAGS=$1; shift
for s in "$@"; do
  MSYS_NO_PATHCONV=1 docker run --rm -v 'D:\WebProjects\StayConnectEnterprise\.claude\worktrees\agent-a545bfb7990bd42d5:/repo' -v 'D:\tmp\oglab3\gomod:/gomod' -v 'D:\tmp\oglab3\gobuild:/gobuild' -v 'D:\tmp\oglab3\rel\bin:/out' -e GOMODCACHE=/gomod -e GOCACHE=/gobuild -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 -w /repo/data-plane golang:1.26.10 go build -trimpath -tags "$TAGS" -o /out/$s ./cmd/$s 2>&1 | tail -5
done
ls -la /d/tmp/oglab3/rel/bin
