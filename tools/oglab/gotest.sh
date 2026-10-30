#!/bin/bash
# gotest.sh <args to go test...>  (run inside data-plane of the worktree, in golang:1.26.10 on Linux)
MSYS_NO_PATHCONV=1 docker run --rm -v 'D:\WebProjects\StayConnectEnterprise\.claude\worktrees\agent-a545bfb7990bd42d5:/repo' -v 'D:\tmp\oglab3\gomod:/gomod' -v 'D:\tmp\oglab3\gobuild:/gobuild' -e GOMODCACHE=/gomod -e GOCACHE=/gobuild -e GOFLAGS=-buildvcs=false ${EXTRA_DOCKER} -w /repo/data-plane golang:1.26.10 go test "$@" 2>&1 | tail -${TAILN:-40}
