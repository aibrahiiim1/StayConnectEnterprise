package main

// The provisional-access bound is a security bound, so it is measured in boot-relative monotonic time from
// /proc/uptime (CLOCK_BOOTTIME), never the wall clock: an NTP step, bad RTC, `date` or snapshot resume would
// stretch the grace. Wall-clock stamps are audit detail only.
//
// The monotonic timeline restarts at zero on reboot and cannot be bridged (the only estimate of downtime is
// the untrusted RTC), so an unproven activation surviving a reboot stays denied until durable state resolves
// it (see phase3_provision.go).

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type securityClock interface {
	// BootMillis is monotonic within a boot. An error means the bound cannot be measured and must not be
	// assumed unspent.
	BootMillis() (int64, error)
	// BootID changes when the monotonic timeline restarts. It returns an error, not "", because an empty id
	// made crossBoot() compare "" with "" and miss the reboot.
	BootID() (string, error)
}

// procSecurityClock reads /proc/uptime, not a process-start baseline, because the bound must survive a
// process restart within the same boot.
type procSecurityClock struct {
	uptimePath string
	bootIDPath string

	// A pass asks for the time and the boot identity once per session (about two thousand times a pass): each ask was an open, read and close,
	// and together they were a quarter of netd's CPU at 300 sign-ins a minute (capacity acceptance, 2026-10-30). The identity of a boot cannot change
	// within it, so it is read again at most once a second; the uptime is read again at most every 250 ms and carried forward in between by the
	// monotonic clock, which advances at the same rate, so a reading is never more than a few milliseconds from a fresh one.
	mu       sync.Mutex
	upBase   int64
	upAt     time.Time
	bootID   string
	bootIDAt time.Time
}

const (
	securityUptimeRefresh = 250 * time.Millisecond
	securityBootIDRefresh = time.Second
)

func newSecurityClock() *procSecurityClock {
	return &procSecurityClock{
		uptimePath: envOr("NETD_UPTIME_FILE", "/proc/uptime"),
		bootIDPath: envOr("NETD_BOOT_ID_FILE", "/proc/sys/kernel/random/boot_id"),
	}
}

func (c *procSecurityClock) BootMillis() (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.upAt.IsZero() {
		if age := time.Since(c.upAt); age >= 0 && age < securityUptimeRefresh {
			return c.upBase + age.Milliseconds(), nil
		}
	}
	ms, err := c.readUptime()
	if err != nil {
		c.upAt = time.Time{}
		return 0, err
	}
	c.upBase, c.upAt = ms, time.Now()
	return ms, nil
}

func (c *procSecurityClock) readUptime() (int64, error) {
	raw, err := os.ReadFile(c.uptimePath)
	if err != nil {
		return 0, fmt.Errorf("security clock unreadable: %w", err)
	}
	// The first field is seconds since boot.
	field := strings.Fields(string(raw))
	if len(field) == 0 {
		return 0, fmt.Errorf("security clock is empty")
	}
	secs, err := strconv.ParseFloat(field[0], 64)
	if err != nil {
		return 0, fmt.Errorf("security clock is unparseable: %w", err)
	}
	if secs < 0 {
		return 0, fmt.Errorf("security clock is negative (%v)", secs)
	}
	return int64(secs * 1000), nil
}

func (c *procSecurityClock) BootID() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bootID != "" && time.Since(c.bootIDAt) < securityBootIDRefresh && time.Since(c.bootIDAt) >= 0 {
		return c.bootID, nil
	}
	raw, err := os.ReadFile(c.bootIDPath)
	if err != nil {
		c.bootID = ""
		return "", fmt.Errorf("boot identity unreadable: %w", err)
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		c.bootID = ""
		return "", fmt.Errorf("boot identity is empty")
	}
	if !plausibleBootID(id) {
		c.bootID = ""
		return "", fmt.Errorf("boot identity is malformed: %q", trimForLog(id))
	}
	c.bootID, c.bootIDAt = id, time.Now()
	return id, nil
}

// plausibleBootID requires the exact canonical lowercase 8-4-4-4-12 UUID the kernel emits; a truncated or
// partial read could compare equal to another truncation and hide a real reboot.
func plausibleBootID(id string) bool {
	const canonical = "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
	if len(id) != len(canonical) {
		return false
	}
	for i := 0; i < len(canonical); i++ {
		c := id[i]
		if canonical[i] == '-' {
			if c != '-' {
				return false
			}
			continue
		}
		isDigit := c >= '0' && c <= '9'
		isLowerHex := c >= 'a' && c <= 'f'
		if !isDigit && !isLowerHex {
			return false
		}
	}
	return true
}

func trimForLog(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// fixedSecurityClock is the test seam.
type fixedSecurityClock struct {
	ms     int64
	bootID string
	err    error
	// bootErr fails the boot-identity read independently of the monotonic reading.
	bootErr error
}

func (c *fixedSecurityClock) BootMillis() (int64, error) {
	if c.err != nil {
		return 0, c.err
	}
	return c.ms, nil
}

func (c *fixedSecurityClock) BootID() (string, error) {
	if c.bootErr != nil {
		return "", c.bootErr
	}
	if strings.TrimSpace(c.bootID) == "" {
		return "", fmt.Errorf("boot identity is empty")
	}
	return c.bootID, nil
}

// Callers must treat an error as unmeasurable and fail closed, never as zero elapsed.
func (p *phase3Shaping) bootNow() (int64, error) {
	if p.secClock == nil {
		return 0, fmt.Errorf("no security clock configured")
	}
	return p.secClock.BootMillis()
}

// Never returns "" without an error.
func (p *phase3Shaping) currentBootID() (string, error) {
	if p.secClock == nil {
		return "", fmt.Errorf("no security clock configured")
	}
	return p.secClock.BootID()
}
