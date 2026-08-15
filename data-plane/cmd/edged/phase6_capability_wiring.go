package main

import (
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// applyAggregateTimeCapability is the ONE place the Phase-6 aggregate flag reaches the commerce admin.
//
// It takes the config as an argument rather than reading it off the server, because the defect it exists to
// prevent was exactly that: a read of s.phase6 that happened before anything assigned it, which pinned the
// capability OFF regardless of the environment. A function that cannot see the server cannot make that
// mistake, and the composition test next to it drives this same path.
func applyAggregateTimeCapability(admin *iamv2.CommerceAdmin, p6 iamv2.Phase6Config) {
	if admin == nil {
		return
	}
	admin.AllowAggregateOnlineTime(p6.AggregateTimeOn())
}
