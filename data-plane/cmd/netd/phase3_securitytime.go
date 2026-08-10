package main

// SECURITY TIME.
//
// The maximum time a guest may hold provisional internet access while their durable ACTIVE state cannot be
// proven is a SECURITY bound. Measuring it against the wall clock makes it adjustable by anything that can
// move the wall clock:
//
//	an NTP correction after a long offline period;
//	an RTC that came back wrong after a power cut (appliances lose RTC batteries);
//	a manual `date` on a unit somebody is debugging;
//	a virtualised host resuming a snapshot.
//
// Any backwards jump makes `now - since` smaller — or negative — and a nominally 30-second grace becomes as
// long as the jump. The guest keeps a provisional authorization the whole time, and every log line looks
// normal. Nothing in the code has to be wrong for that to happen; it is what "compare two wall-clock
// timestamps" means on a machine whose clock can move.
//
// So the authority for this bound is BOOT-RELATIVE MONOTONIC TIME, read from /proc/uptime, which the kernel
// derives from CLOCK_BOOTTIME: it advances at one second per second, includes time spent suspended, and is
// unaffected by every clock adjustment above. Wall-clock timestamps are still recorded, but only as audit
// detail a human can read — never as the thing that decides whether access may continue.
//
// ACROSS A REBOOT the monotonic timeline restarts at zero, and no honest arithmetic can bridge it: the only
// available estimate of how long the unit was down comes from the very RTC that cannot be trusted. So a boot
// change is not translated into a fresh budget. An unproven activation that survives a reboot stays denied
// until durable state resolves it (see the cross-boot handling in phase3_provision.go).

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// securityClock is the monotonic time source and boot identity the activation bound is measured against.
type securityClock interface {
	// BootMillis is milliseconds since boot. It is monotonic within a boot and unaffected by RTC or NTP
	// adjustment. An error means the bound cannot be measured, and a bound that cannot be measured must not
	// be assumed to be unspent.
	BootMillis() (int64, error)
	// BootID identifies THIS boot. A change means the monotonic timeline restarted and prior readings from it
	// are meaningless — not merely old.
	//
	// It returns an ERROR rather than an empty string, because an empty identity is not "no reboot detected" —
	// it is "reboot detection is not working". Represented as a bare string, an unreadable boot id made
	// crossBoot() compare "" against "" and conclude the boot was unchanged, so a prior boot's deadline could
	// be measured against the new boot's uptime. The bound was silently gone and every log line looked normal.
	BootID() (string, error)
}

// procSecurityClock is the production clock: /proc/uptime for the monotonic reading, /proc/sys/kernel/random/
// boot_id for the boot identity.
//
// /proc/uptime is used rather than time.Now() for the reason this whole file exists, and rather than a
// process-start baseline because the bound has to survive a process RESTART within the same boot — a
// process-relative clock resets exactly when the attacker (or the crash loop) needs it to.
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
	// "12345.67 89012.34" — the first field is seconds since boot.
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

// plausibleBootID validates the ACTUAL Linux boot-identity contract.
//
// /proc/sys/kernel/random/boot_id is a canonical RFC-4122 UUID in lowercase hexadecimal, 8-4-4-4-12 with
// hyphens — the kernel formats it with %pUb and never produces anything else. Accepting "any opaque token of
// at least eight characters" was too generous by a wide margin: a truncated read ("f81d4fae-7dec-11d0"), a
// partially-written file, or an error string copied into place would all have passed, and each of them would
// then be compared for equality against a later, complete read and reported as a DIFFERENT boot — or, worse,
// two different truncations of the same id would compare equal to each other and hide a real reboot.
//
// The length is fixed and the alphabet is fixed, so the check can be exact. Being exact is the point: this
// value's only job is to be equal to itself across a restart and unequal across a reboot, and anything that is
// not the canonical form cannot be trusted to do either.
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

// fixedSecurityClock is the test seam. It is in the production file deliberately: a clock this important
// should have exactly one implementation to fake, and it should be obvious what faking it means.
type fixedSecurityClock struct {
	ms     int64
	bootID string
	err    error
	// bootErr fails the BOOT-IDENTITY read specifically, independently of the monotonic reading, because those
	// are two different files and two different failures.
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

// bootNow reads the security clock, or reports why it could not be read. Callers must treat an error as
// "this bound cannot be measured" and fail closed — never as zero elapsed.
func (p *phase3Shaping) bootNow() (int64, error) {
	if p.secClock == nil {
		return 0, fmt.Errorf("no security clock configured")
	}
	return p.secClock.BootMillis()
}

// currentBootID returns the trustworthy boot identity, or an error. There is no third answer: a caller that
// received "" and carried on would be doing reboot detection with a value that cannot detect a reboot.
func (p *phase3Shaping) currentBootID() (string, error) {
	if p.secClock == nil {
		return "", fmt.Errorf("no security clock configured")
	}
	return p.secClock.BootID()
}
