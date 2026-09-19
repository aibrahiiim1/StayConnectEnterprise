package main

// WHAT THIS APPLIANCE ACTUALLY SERVES.
//
// THE DEFECT THIS EXISTS FOR. Hotel Admin decided which navigation destinations to show from
// NEXT_PUBLIC_PHASE*_ADMIN, which are substituted when the bundle is BUILT. edged decides which resources to
// mount at RUNTIME, from this appliance's own configuration. Those are two different facts, and on PRE-LIVE
// they disagreed about eight destinations: Guest devices, Online-time budgets, Post-stay access, Cross-PMS
// transfer, Charge health, Manual review, Settlements and Recovery were all offered in the menu and all
// answered 404, because the bundle was built with every capability on and this appliance mounts a subset.
//
// An operator clicking one of those got a blank page and a console full of 404s. Nothing told them the
// feature is not enabled here; it simply looked broken -- and a product that looks broken in eight places is
// judged by those eight.
//
// The fix is to stop guessing. mountResource is the single place a surface becomes reachable, so it records
// the name as it mounts, and this endpoint reports that set. There is no second list to keep in step: a
// resource that is mounted is reported, and one that is not, is not.

import (
	"net/http"
	"sort"
	"sync"
)

// mountedSurfaces is written during router construction (single-threaded) and read by request handlers
// afterwards. The mutex is not for contention, it is so the race detector can prove the hand-off.
type mountedSurfaces struct {
	mu    sync.RWMutex
	names map[string]bool
}

func newMountedSurfaces() *mountedSurfaces {
	return &mountedSurfaces{names: map[string]bool{}}
}

// add and list tolerate a nil receiver.
//
// mountResource is how EVERY operator surface becomes reachable, so it must not be the thing that brings the
// process down. A server assembled without a registry — several focused tests build one with two fields —
// records nothing and reports nothing, which is a truthful answer for a process that has no registry, and a
// far better one than a nil dereference inside route construction at startup.
func (m *mountedSurfaces) add(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.names[name] = true
}

func (m *mountedSurfaces) list() []string {
	if m == nil {
		return []string{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.names))
	for n := range m.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// capabilities serves GET /edge/v1/capabilities.
//
// It reports SURFACES, not features. "pms-stays is mounted" is a fact about this process that the admin can
// act on; "Phase 3 is enabled" is an implementation concept an operator should never meet. The admin uses it
// to hide destinations that would 404 and to explain, on any page reached directly by URL, that the feature
// is not enabled on this appliance.
//
// Deliberately NOT permission-filtered. Which surfaces exist is a property of the appliance; which of them a
// given operator may read is already enforced per request by resourcePermission, and the navigation applies
// the role matrix on top of this list. Returning the same answer to every authenticated operator keeps this
// endpoint a statement of fact rather than a second, parallel authorisation model that could disagree with
// the first one.
func (s *server) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"surfaces": s.surfaces.list(),
	})
}
