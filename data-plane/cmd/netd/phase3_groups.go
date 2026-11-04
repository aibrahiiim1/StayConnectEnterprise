package main

// SHARED group slots. Each SHARED entitlement gets its own group class minor, starting at its hash and probing to the next free
// slot, so two entitlements never pool their bandwidth in one class (a bare hash into 4,096 slots collided often at a few hundred
// guests). A slot is kept while its entitlement is in the plan; after a restart it is read back from the parent the sessions'
// installed classes already hang under, so nothing is renumbered.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/onegate/onegate/data-plane/internal/shape"
	"github.com/onegate/onegate/data-plane/internal/shapeplan"
)

const groupSlotSpan = shape.GroupMinorMax - shape.GroupMinorBase + 1

// assignGroups runs once per walk, before any session is shaped.
func (p *phase3Shaping) assignGroups(ctx context.Context, desired map[string]map[int]shapeplan.Session) {
	type place struct {
		bridge string
		minor  int
	}
	byEnt := map[string][]place{}
	for bridge, byMinor := range desired {
		for minor, s := range byMinor {
			if id := strings.TrimSpace(s.EntitlementID); s.SpeedAllocation == speedAllocationShared && id != "" {
				byEnt[id] = append(byEnt[id], place{bridge, minor})
			}
		}
	}
	if p.groupSlots == nil {
		p.groupSlots = map[string]int{}
	}
	for id := range p.groupSlots {
		if _, ok := byEnt[id]; !ok {
			delete(p.groupSlots, id)
		}
	}
	used := map[int]string{}
	for id, m := range p.groupSlots {
		used[m] = id
	}

	// An entitlement whose sessions already hang under a free group keeps it; where two share one (the old hash collision), the one
	// with more sessions keeps it and the other is given its own, which regroups its sessions.
	type claim struct {
		id    string
		minor int
		n     int
	}
	var claims []claim
	for id, places := range byEnt {
		if _, ok := p.groupSlots[id]; ok {
			continue
		}
		count := map[int]int{}
		for _, pl := range places {
			classes, err := p.readClassesForPass(ctx, pl.bridge)
			if err != nil {
				continue
			}
			if m, ok := groupMinorOf(classes[pl.minor].Parent); ok {
				count[m]++
			}
		}
		for m, n := range count {
			claims = append(claims, claim{id, m, n})
		}
	}
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].n != claims[j].n {
			return claims[i].n > claims[j].n
		}
		if claims[i].id != claims[j].id {
			return claims[i].id < claims[j].id
		}
		return claims[i].minor < claims[j].minor
	})
	for _, c := range claims {
		if _, ok := p.groupSlots[c.id]; ok || used[c.minor] != "" {
			continue
		}
		p.groupSlots[c.id], used[c.minor] = c.minor, c.id
	}

	ids := make([]string, 0, len(byEnt))
	for id := range byEnt {
		if _, ok := p.groupSlots[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		start, _ := shape.GroupMinorFor(id)
		for i := 0; i < groupSlotSpan; i++ {
			m := shape.GroupMinorBase + (start-shape.GroupMinorBase+i)%groupSlotSpan
			if used[m] == "" {
				p.groupSlots[id], used[m] = m, id
				break
			}
		}
	}
}

// groupClassFor is the entitlement's group classid for this walk, or "" when it has none (no entitlement, or every slot taken).
func (p *phase3Shaping) groupClassFor(entitlementID string) string {
	m, ok := p.groupSlots[strings.TrimSpace(entitlementID)]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s:%x", shape.RootMajor, m)
}

// groupMinorOf parses a SHARED group parent ("1:2abc") to its minor.
func groupMinorOf(parent string) (int, bool) {
	rest, ok := strings.CutPrefix(parent, shape.RootMajor+":")
	if !ok || rest == "" {
		return 0, false
	}
	m, err := strconv.ParseInt(rest, 16, 32)
	if err != nil || m < shape.GroupMinorBase || m > shape.GroupMinorMax {
		return 0, false
	}
	return int(m), true
}
