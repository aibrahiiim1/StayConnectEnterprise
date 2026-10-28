package enforce

import (
	"sync"
	"time"
)

// catalogProbeTTL bounds how long a catalog probe answer is reused (audit perf D5-L1). The probes exist so a
// migration applied or rolled back under a running process is noticed; a minute meets that, and an idle
// appliance stops paying a dozen catalog statements a second.
const catalogProbeTTL = time.Minute

// probeCache remembers catalog answers for catalogProbeTTL. A failed probe is never cached.
type probeCache struct {
	mu   sync.Mutex
	now  func() time.Time
	seen map[string]probeAnswer
}

type probeAnswer struct {
	value []bool
	at    time.Time
}

func (c *probeCache) get(key string, probe func() ([]bool, error)) ([]bool, error) {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	c.mu.Lock()
	if a, ok := c.seen[key]; ok && now().Sub(a.at) < catalogProbeTTL {
		c.mu.Unlock()
		return a.value, nil
	}
	c.mu.Unlock()
	v, err := probe()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.seen == nil {
		c.seen = map[string]probeAnswer{}
	}
	c.seen[key] = probeAnswer{value: v, at: now()}
	c.mu.Unlock()
	return v, nil
}
