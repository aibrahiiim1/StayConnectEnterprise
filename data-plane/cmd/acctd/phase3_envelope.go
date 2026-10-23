package main

// The plan generation is durable: in memory only, a restarted acctd would start at 1 and netd would ignore
// every plan as stale, freezing access at the pre-restart state with nothing looking broken.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type planScope struct {
	TenantID     string
	SiteID       string
	ApplianceID  string
	AssignmentID string
	AssignmentGe int64
}

type planCounter struct {
	mu   sync.Mutex
	path string
	gen  int64
	// runtime tells operators one acctd process from the next; netd does not gate on it.
	runtime int64
	loaded  bool
}

type counterState struct {
	Generation int64 `json:"plan_generation"`
	Runtime    int64 `json:"producer_runtime_generation"`
}

func newPlanCounter(path string) *planCounter { return &planCounter{path: path} }

// start loads the counter and claims a new runtime generation. A missing or unreadable file means "never
// produced"; for a corrupt one, netd's stale-generation refusal surfaces the problem.
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

// next persists the generation BEFORE it is used: persisting after submission would let netd accept a
// generation the producer does not know it issued, and a restart would re-issue it with different contents.
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
