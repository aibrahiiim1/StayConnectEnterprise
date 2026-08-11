package main

// THE PRODUCER SIDE OF THE SHAPING CONTRACT.
//
// acctd states a COMPLETE desired state, scoped to exactly one tenant/site/appliance, stamped with a
// monotonically increasing generation and a short expiry, and hashed so a truncated body cannot be mistaken
// for a smaller desired state (which would read as "revoke everyone who is missing").
//
// The generation is durable. If it lived only in memory a restarted acctd would start again at 1, and netd —
// correctly refusing anything older than what it already applied — would ignore every plan the new process
// produced. Access would then be frozen at whatever the pre-restart plan said, indefinitely, and nothing would
// look broken. The counter therefore survives the process that produces it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// planScope is the appliance's authoritative identity for shaping submissions.
type planScope struct {
	TenantID     string
	SiteID       string
	ApplianceID  string
	AssignmentID string
	AssignmentGe int64
}

// planCounter is the durable monotonic generation source.
type planCounter struct {
	mu   sync.Mutex
	path string
	gen  int64
	// runtime distinguishes one acctd process from the next. netd does not gate on it; it exists so an
	// operator reading two plans can tell "the same process re-derived" from "a new process took over".
	runtime int64
	loaded  bool
}

type counterState struct {
	Generation int64 `json:"plan_generation"`
	Runtime    int64 `json:"producer_runtime_generation"`
}

func newPlanCounter(path string) *planCounter { return &planCounter{path: path} }

// start loads the persisted counter and claims a new runtime generation. A missing or unreadable file is
// treated as "never produced": starting at 1 is correct for a fresh appliance, and for a corrupted file netd's
// stale-generation refusal is what surfaces the problem instead of it being silently overwritten.
func (c *planCounter) start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = true
	if c.path == "" {
		return
	}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var st counterState
	if json.Unmarshal(raw, &st) != nil {
		return
	}
	if st.Generation > c.gen {
		c.gen = st.Generation
	}
	c.runtime = st.Runtime + 1
}

// next allocates the generation for the plan about to be submitted, persisting it BEFORE it is used. Writing
// after a successful submission would leave a window in which netd has accepted a generation the producer does
// not know it issued, and the next restart would re-issue it with different contents.
func (c *planCounter) next() (int64, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		c.loaded = true
	}
	c.gen++
	if c.path != "" {
		if raw, err := json.Marshal(counterState{Generation: c.gen, Runtime: c.runtime}); err == nil {
			_ = os.MkdirAll(filepath.Dir(c.path), 0o750)
			tmp := c.path + ".tmp"
			if os.WriteFile(tmp, raw, 0o600) == nil {
				_ = os.Rename(tmp, c.path)
			}
		}
	}
	return c.gen, c.runtime
}
