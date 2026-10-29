package main

// AD-57 (c): every landing page view asks scd whether this device's package just ended, and nearly every answer is
// "nothing to say". portald remembers that negative answer per client address for a few seconds, so a page that is
// reloaded, a captive-portal probe that retries, or several tabs do not each reach scd and its database.
//
// Only the negative answer is kept; a notice is never cached, so it is shown exactly as before. The cost of the
// cache is that a package which ends inside the window shows its notice at the next view after the window (at most
// accessStatusQuietFor later), not at the very next one.

import (
	"sync"
	"time"
)

const (
	accessStatusQuietFor = 15 * time.Second
	accessStatusQuietMax = 4096
)

type quietCache struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]time.Time
}

func newQuietCache() *quietCache { return &quietCache{now: time.Now, m: map[string]time.Time{}} }

// quiet reports whether key was answered "nothing to say" within the window.
func (c *quietCache) quiet(key string) bool {
	if c == nil || key == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.m[key]
	if !ok {
		return false
	}
	if !c.now().Before(until) {
		delete(c.m, key)
		return false
	}
	return true
}

func (c *quietCache) remember(key string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.m) >= accessStatusQuietMax {
		for k, until := range c.m {
			if !now.Before(until) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= accessStatusQuietMax { // still full of live entries: drop the cache rather than grow
			c.m = map[string]time.Time{}
		}
	}
	c.m[key] = now.Add(accessStatusQuietFor)
}
