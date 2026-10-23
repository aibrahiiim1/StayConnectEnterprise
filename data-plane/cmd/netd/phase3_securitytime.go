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
}

func newSecurityClock() *procSecurityClock {
	return &procSecurityClock{
		uptimePath: envOr("NETD_UPTIME_FILE", "/proc/uptime"),
		bootIDPath: envOr("NETD_BOOT_ID_FILE", "/proc/sys/kernel/random/boot_id"),
	}
}

func (c *procSecurityClock) BootMillis() (int64, error) {
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
	raw, err := os.ReadFile(c.bootIDPath)
	if err != nil {
		return "", fmt.Errorf("boot identity unreadable: %w", err)
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", fmt.Errorf("boot identity is empty")
	}
	if !plausibleBootID(id) {
		return "", fmt.Errorf("boot identity is malformed: %q", trimForLog(id))
	}
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
