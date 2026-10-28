package main

import (
	"context"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/api"
)

// Audit Phase 1 L19: a panicking housekeeping step is logged and the pass (and the loop) carries on.
func TestHousekeepingPassSurvivesAPanic(t *testing.T) {
	var last time.Time
	// A Base without a database makes every step panic on its first query.
	housekeepingPass(context.Background(), &api.Base{}, &last)
	if last.IsZero() {
		t.Fatal("the pass must continue past panicking steps to the hourly ones")
	}
}
