package shapeproducer

// The producer half of the shaping contract (acctd builds the envelope, netd applies it), kept outside
// acctd's main so end-to-end tests can drive it.

import (
	"sort"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// Scope is checked field by field by netd against its own resolved scope, so a producer cannot widen its reach.
type Scope struct {
	TenantID      string
	SiteID        string
	ApplianceID   string
	AssignmentID  string
	AssignmentGen int64
}

func BuildEnvelope(plan enforce.Plan, scope Scope, planGen, runtimeGen int64,
	managedBridges []string, fallbackBridge string, now time.Time, validity time.Duration) shapeplan.Envelope {
	bridgeOf := func(b string) string {
		if b != "" {
			return b
		}
		return fallbackBridge
	}
	sessions := make([]shapeplan.Session, 0, len(plan.Tear)+len(plan.Shape))
	for _, s := range plan.Tear {
		sessions = append(sessions, shapeplan.Session{
			SessionID: s.SessionID, DeviceID: s.DeviceID, IP: s.IP, Bridge: bridgeOf(s.Bridge), Entitled: false,
			EndReason: s.EndReason, MAC: s.MAC, EntitlementID: s.EntitlementID})
	}
	for _, s := range plan.Shape {
		// The hard boundary is passed through unchanged; the producer must not soften a deadline it did not set.
		sessions = append(sessions, shapeplan.Session{
			SessionID: s.SessionID, DeviceID: s.DeviceID, IP: s.IP, Bridge: bridgeOf(s.Bridge),
			DownKbps: s.DownKbps, UpKbps: s.UpKbps, Entitled: true, AccessEndsAt: s.WindowEndsAt,
			MAC: s.MAC, EntitlementID: s.EntitlementID, SpeedAllocation: s.SpeedAllocation})
	}
	// Declare every session bridge plus every guest bridge, including empty ones, which can quietly keep forwarding for ended access.
	declared := map[string]bool{}
	for _, b := range managedBridges {
		if b != "" {
			declared[b] = true
		}
	}
	for _, s := range sessions {
		if s.Bridge != "" {
			declared[s.Bridge] = true
		}
	}
	if fallbackBridge != "" {
		declared[fallbackBridge] = true
	}
	bridges := make([]string, 0, len(declared))
	for b := range declared {
		bridges = append(bridges, b)
	}
	sort.Strings(bridges)

	env := shapeplan.Envelope{
		ContractVersion:    shapeplan.ContractVersion,
		TenantID:           scope.TenantID,
		SiteID:             scope.SiteID,
		ApplianceID:        scope.ApplianceID,
		AssignmentID:       scope.AssignmentID,
		AssignmentGen:      scope.AssignmentGen,
		ProducerRuntimeGen: runtimeGen,
		PlanGeneration:     planGen,
		GeneratedAt:        now.UTC(),
		ExpiresAt:          now.UTC().Add(validity),
		ManagedBridges:     bridges,
		Sessions:           sessions,
	}
	env.DesiredStateHash = shapeplan.HashDesiredState(bridges, sessions)
	return env
}
