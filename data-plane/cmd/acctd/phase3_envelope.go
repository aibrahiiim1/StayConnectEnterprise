package main

// The plan generation is durable: in memory only, a restarted acctd would start at 1 and netd would ignore
// every plan as stale, freezing access at the pre-restart state with nothing looking broken.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
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
	// now seeds the generation floor; a test may replace it.
	now func() time.Time
}

type counterState struct {
	Generation int64 `json:"plan_generation"`
	Runtime    int64 `json:"producer_runtime_generation"`
}

func newPlanCounter(path string) *planCounter { return &planCounter{path: path, now: time.Now} }

// start loads the counter and claims a new runtime generation. The generation is seeded with
// max(saved, current Unix time in milliseconds) (audit P6 M2): a counter file lost to a power cut, a full disk
// or corruption must never make acctd re-issue generations netd has already accepted, which netd would refuse
// as stale and so freeze enforcement. At one generation per tick the saved value does not overtake the clock,
// so the floor matters only when the file is missing or behind.
func (c *planCounter) start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = true
	if c.now != nil {
		if floor := c.now().UnixMilli(); floor > c.gen {
			c.gen = floor
		}
	}
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
// A generation that could not be saved durably comes back with an error and must not be submitted.
func (c *planCounter) next() (int64, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		c.loaded = true
	}
	c.gen++
	if c.path != "" {
		raw, err := json.Marshal(counterState{Generation: c.gen, Runtime: c.runtime})
		if err != nil {
			return c.gen, c.runtime, fmt.Errorf("plan generation not saved: %w", err)
		}
		if err := writeFileDurable(c.path, raw); err != nil {
			return c.gen, c.runtime, fmt.Errorf("plan generation not saved: %w", err)
		}
	}
	return c.gen, c.runtime, nil
}

// writeFileDurable writes and fsyncs a temporary file, renames it into place and fsyncs the directory, so a
// power cut leaves the old or the new content, never a lost rename.
func writeFileDurable(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
