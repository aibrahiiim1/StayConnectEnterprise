package shapeproducer

// THE PRODUCER HALF OF THE SHAPING CONTRACT, in one place both sides can reach.
//
// acctd derives the plan and submits it; netd applies it. The construction of the ENVELOPE between them —
// which sessions are named, which bridges are declared, what is hashed — used to live inside acctd's process
// main, where the only way to exercise it end to end was to run the daemon. That mattered once the cycle
// itself became the thing that could be broken: the enforcement plane failed live not because either half was
// wrong, but because the two were never proven to converge against a real database and a real socket.
//
// Moving it here changes no behaviour. It makes the producer callable by the end-to-end regression that now
// drives a real plan from durable state, through the real applier, to a Session the kernel has confirmed.

import (
	"sort"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// Scope is the appliance identity a submitted plan is scoped to. netd checks every field of it against its own
// independently resolved scope, so a producer cannot widen its reach by asserting a different one.
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
			// Carried through unchanged: the producer knows WHY this session must stop being enforced, and the
			// applier is the only one that can write it down.
			EndReason: s.EndReason, MAC: s.MAC, EntitlementID: s.EntitlementID})
	}
	for _, s := range plan.Shape {
		// The entitlement's hard boundary travels with the session so the applier can bound its kernel lease by
		// it. It is passed through unchanged — the producer does not get to soften a deadline it did not set.
		sessions = append(sessions, shapeplan.Session{
			SessionID: s.SessionID, DeviceID: s.DeviceID, IP: s.IP, Bridge: bridgeOf(s.Bridge),
			DownKbps: s.DownKbps, UpKbps: s.UpKbps, Entitled: true, AccessEndsAt: s.WindowEndsAt,
			// The MAC is the applier's only means of asking DHCP whether this address is still this device's;
			// the entitlement and the allocation mode are what let it build a SHARED ceiling.
			MAC: s.MAC, EntitlementID: s.EntitlementID, SpeedAllocation: s.SpeedAllocation})
	}
	// Every bridge a session is on must be declared, plus every guest bridge the site has — including ones
	// with no sessions at all. Those are the ones that can quietly keep forwarding for access that ended.
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
