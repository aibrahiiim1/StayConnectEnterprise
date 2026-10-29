package main

import (
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
)

// AD-32: a production build never runs without the durable throttle, whatever the environment says.
func TestDurableThrottleIsAlwaysOnInProductionBuilds(t *testing.T) {
	for _, env := range []string{"", "false", "true", "0"} {
		got := durableThrottleEnabled(env)
		want := buildprofile.Production || env == "true"
		if got != want {
			t.Errorf("durableThrottleEnabled(%q) = %v, want %v", env, got, want)
		}
		if buildprofile.Production && !got {
			t.Errorf("production build left the durable throttle off for %q", env)
		}
	}
}
