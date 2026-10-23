package main

import (
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// applyAggregateTimeCapability is the one place the aggregate flag reaches the commerce admin. It takes the
// config as an argument, not from the server, so it cannot read s.phase6 before it is assigned (which pinned the capability off).
func applyAggregateTimeCapability(admin *iamv2.CommerceAdmin, p6 iamv2.Phase6Config) {
	if admin == nil {
		return
	}
	admin.AllowAggregateOnlineTime(p6.AggregateTimeOn())
}
