package main

// The admin bundle is built with every capability while edged mounts a subset at runtime, so the menu offered
// destinations that 404ed. mountResource is the single place a surface becomes reachable and records its name;
// this endpoint reports that set, so there is no second list to keep in step.

import (
	"net/http"
	"sort"
	"sync"
)

// mountedSurfaces is written during router construction and read by handlers afterwards; the mutex lets the
// race detector prove the hand-off.
type mountedSurfaces struct {
	mu    sync.RWMutex
	names map[string]bool
}

func newMountedSurfaces() *mountedSurfaces {
	return &mountedSurfaces{names: map[string]bool{}}
}

// add and list tolerate a nil receiver: servers built without a registry (focused tests) must not panic in route construction.
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

// capabilities reports mounted surfaces, not features. It is deliberately not permission-filtered: which
// operator may read a surface is enforced per request by resourcePermission, and a second authorisation model
// here could disagree with it.
//
// Module-owned surfaces appear only while served (modules.go surfaceServed); if module state is unreadable they
// are all omitted so navigation fails closed to the core. "modules" is null when unreadable.
func (s *server) capabilities(w http.ResponseWriter, r *http.Request) {
	surfaces, rep := s.filterSurfaces(r.Context(), s.surfaces.list())
	var mods any
	if rep != nil {
		mods = rep.Modules
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"surfaces": surfaces,
		"modules":  mods,
	})
}
