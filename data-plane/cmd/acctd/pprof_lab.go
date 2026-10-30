//go:build oglabdiag

package main

// Lab diagnostics only: a build with `-tags oglabdiag` (never part of a release, which is built with the stayconnect_production tag alone)
// serves Go's profiler and goroutine dump on 127.0.0.1:6063 so a stall can be read from a live process without stopping it.
// Used by tools/oglab for the capacity acceptance; see docs/reviews/SIGNIN_CAPACITY_CLOSURE.md.

import (
	"net/http"
	_ "net/http/pprof"
)

func init() {
	go func() { _ = http.ListenAndServe("127.0.0.1:6063", nil) }()
}
