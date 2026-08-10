package main

// TRUSTED BOOT IDENTITY, AND A SECURITY JOURNAL THAT MEANS WHAT IT SAYS.
//
// The cross-reboot guarantee rests on two readings, not one: a monotonic uptime AND the identity of the boot
// that uptime belongs to. A monotonic number without a trustworthy boot is not a bound — it is a number that
// happens to be small again.
//
// Two failure shapes are covered here, and both used to pass silently:
//
//	AN UNREADABLE OR EMPTY BOOT ID. Represented as "", it made crossBoot() compare "" against "" and answer
//	"same boot", so a previous boot's deadline could be measured against the new boot's uptime.
//
//	A JOURNAL THAT PARSES BUT DOES NOT COHERE. The file is security authority; valid JSON carrying a deadline
//	far in the future, a negative reading, a missing boot identity or two records for one key must be treated
//	exactly like an unreadable file, and never normalised into a fresh grace.
//
// FAKE-KERNEL tests. nft and tc are modelled; the disposable-namespace suite covers the kernel contract.

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// ---- 1. boot identity is part of the security clock --------------------------

// AN UNREADABLE BOOT ID DENIES ADMISSION. The monotonic reading is fine; the identity it would be anchored to
// is not, so the bound cannot be trusted after a reboot and no provisional access is granted.
func TestBootID_UnreadableBootIdentityDeniesAdmission(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.clk.bootErr = errors.New("/proc/sys/kernel/random/boot_id: permission denied")

	res := h.pass(t, nil)
	if !res.Degraded {
		t.Fatal("an untrustworthy boot identity was reported as a clean convergence")
	}
	if h.authorized() {
		t.Fatal("a guest was authorized while reboot detection was not working")
	}
	if len(h.attempts(t)) != 0 {
		t.Fatal("an attempt was journalled with no boot to anchor it to")
	}
}

// AN EMPTY OR WHITESPACE BOOT ID is the same failure wearing a different mask: it parses, it is a string, and
// it cannot distinguish two boots.
func TestBootID_EmptyOrWhitespaceBootIdentityDeniesAdmission(t *testing.T) {
	for _, id := range []string{"", "   ", "\n", "\t \n"} {
		h := newDurHarness(t)
		brokenDB(h)
		h.clk.bootID = id
		h.pass(t, nil)
		if h.authorized() {
			t.Fatalf("a guest was authorized with boot identity %q", id)
		}
	}
}

// A SHORT OR MALFORMED identity is rejected by the clock itself: a value too short to distinguish two boots,
// or one carrying whitespace or control characters, is far more likely to be a truncated read or an error
// message than a kernel boot id.
func TestBootID_MalformedBootIdentityIsRefusedByTheClock(t *testing.T) {
	for _, id := range []string{"abc", "1234567", "has space here", "x\x00y", "line\nbreak"} {
		if plausibleBootID(id) {
			t.Fatalf("%q was accepted as a boot identity", id)
		}
	}
	if !plausibleBootID("b00a1de-0000-4000-8000-00000000000a") {
		t.Fatal("a realistic boot identity was rejected")
	}
}

// THE CENTRAL CASE: a reboot followed by an unreadable boot id must not be mistaken for the same boot. This is
// the exact shape that let a prior boot's large deadline be compared against a new boot's small uptime.
func TestBootID_ARebootHiddenByAnUnreadableBootIDCannotRestoreAccess(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("setup: the guest was never admitted")
	}

	// Reboot: the kernel is empty and uptime restarts small — but the boot id cannot be read, so nothing can
	// prove which boot the journalled deadline belongs to.
	h.reboot(t, bootIDFor("B"))
	h.clk.bootErr = errors.New("boot_id unreadable")
	h.clk.ms = 1_000 // a fresh, small uptime: the prior deadline would look far in the future

	for i := 0; i < 5; i++ {
		h.advance(20 * time.Second)
		h.pass(t, nil)
		if h.authorized() {
			t.Fatalf("pass %d: a reboot hidden by an unreadable boot id restored provisional access", i+1)
		}
	}
}

// AN OLD JOURNAL WITH A LARGE PRIOR-BOOT DEADLINE plus a new small uptime must not be honoured, even when the
// boot id IS readable — it simply differs, which is the ordinary reboot path.
func TestBootID_APriorBootDeadlineIsNeverComparedWithANewUptime(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.clk.ms = 5_000_000 // a long-running previous boot
	h.pass(t, nil)
	at := h.attempts(t)
	if len(at) != 1 || at[0].DeadlineBootMs < 5_000_000 {
		t.Fatalf("setup: expected a large prior-boot deadline, got %+v", at)
	}

	h.reboot(t, bootIDFor("C")) // uptime restarts small; the old deadline is now "in the future"
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("a prior boot's deadline was compared against the new boot's uptime and read as unexpired")
	}
}

// RECOVERY. Once the boot identity is readable again, an ordinary session is admitted again.
func TestBootID_AccessResumesOnceBootIdentityIsReadableAgain(t *testing.T) {
	h := newDurHarness(t)
	h.clk.bootErr = errors.New("boot_id unreadable")
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("setup: the guest should not be authorized while the identity is untrustworthy")
	}

	h.clk.bootErr = nil
	h.advance(time.Second)
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("a healthy session was not admitted after boot identity became readable")
	}
}

// AND A PROVEN-ACTIVE SESSION IS NOT DISCONNECTED unnecessarily: durable state proving ACTIVE resolves the
// session regardless of the journal, so an identity problem must not turn into an outage for it.
func TestBootID_AProvenActiveSessionSurvivesAnUnreadableBootIdentity(t *testing.T) {
	h := newDurHarness(t)
	h.pass(t, nil) // healthy: promoted to active, no attempt outstanding
	if !h.authorized() || h.enf.sessionState("live-1") != "active" {
		t.Fatal("setup: the session did not converge to active")
	}
	if len(h.attempts(t)) != 0 {
		t.Fatal("setup: a proven session left an attempt record")
	}

	h.clk.bootErr = errors.New("boot_id unreadable")
	h.advance(5 * time.Second)
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("a session already proven ACTIVE was disconnected because the boot id became unreadable")
	}
}

// ---- 2. the journal must cohere, not merely parse ----------------------------

// writeJournal puts an arbitrary — valid JSON — document in the journal's place.
func writeJournal(t *testing.T, h *durHarness, attempts []activationAttempt) {
	t.Helper()
	raw, err := json.Marshal(journalState{
		TenantID: h.p.mode.TenantID, SiteID: h.p.mode.SiteID, ApplianceID: h.p.mode.ApplianceID,
		Attempts: attempts})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.journalPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// THE POISON MATRIX. Every one of these is valid JSON, and every one would widen or erase the bound if it were
// believed. Each must land in exactly the same place as an unreadable file: UNKNOWN, fail-closed.
func TestJournal_SemanticallyIncoherentRecordsAreTreatedAsUnknown(t *testing.T) {
	good := activationAttempt{
		Key: "br-guest|live-1", SessionID: "live-1", BootID: bootIDFor("A"),
		BeganBootMs: 60_000, DeadlineBootMs: 90_000, Strikes: 0,
	}
	mutate := func(f func(a *activationAttempt)) []activationAttempt {
		a := good
		f(&a)
		return []activationAttempt{a}
	}
	cases := []struct {
		name     string
		attempts []activationAttempt
	}{
		{"no activation key", mutate(func(a *activationAttempt) { a.Key = "" })},
		{"key is not a bridge|session identity", mutate(func(a *activationAttempt) { a.Key = "live-1" })},
		{"no session identity", mutate(func(a *activationAttempt) { a.SessionID = "  " })},
		{"boot-relative readings with no boot identity", mutate(func(a *activationAttempt) { a.BootID = "" })},
		{"boot identity too short to distinguish boots", mutate(func(a *activationAttempt) { a.BootID = "abc" })},
		{"negative start", mutate(func(a *activationAttempt) { a.BeganBootMs = -1 })},
		{"negative deadline", mutate(func(a *activationAttempt) { a.DeadlineBootMs = -5 })},
		{"negative backoff", mutate(func(a *activationAttempt) { a.BackoffUntilBootMs = -1; a.Strikes = 1 })},
		{"deadline before start", mutate(func(a *activationAttempt) { a.DeadlineBootMs = a.BeganBootMs - 1 })},
		{"deadline equal to start", mutate(func(a *activationAttempt) { a.DeadlineBootMs = a.BeganBootMs })},
		{"WIDENED GRACE: an hour instead of the policy", mutate(func(a *activationAttempt) {
			a.DeadlineBootMs = a.BeganBootMs + int64(time.Hour/time.Millisecond)
		})},
		{"overflowed deadline", mutate(func(a *activationAttempt) { a.DeadlineBootMs = 1 << 62 })},
		{"implausible start", mutate(func(a *activationAttempt) {
			a.BeganBootMs = maxPlausibleBootMs + 1
			a.DeadlineBootMs = maxPlausibleBootMs + 2
		})},
		{"negative strikes", mutate(func(a *activationAttempt) { a.Strikes = -3 })},
		{"backoff with no strike behind it", mutate(func(a *activationAttempt) {
			a.BeganBootMs, a.DeadlineBootMs = 0, 0
			a.BackoffUntilBootMs = 120_000
			a.Strikes = 0
		})},
		{"strikes with neither backoff nor attempt", mutate(func(a *activationAttempt) {
			a.BeganBootMs, a.DeadlineBootMs, a.BackoffUntilBootMs = 0, 0, 0
			a.Strikes = 4
		})},
		{"duplicate records for one key", []activationAttempt{good, good}},
	}

	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			h := newDurHarness(t)
			brokenDB(h)
			writeJournal(t, h, tc.attempts)
			h.restart(t)

			if !h.p.unprovenUnknown {
				t.Fatal("an incoherent security record was accepted as trustworthy")
			}
			h.pass(t, nil)
			if h.authorized() {
				t.Fatal("an incoherent security record was normalised into a fresh grace period")
			}
		})
	}
}

// A COHERENT record is still honoured — otherwise the check above would be indistinguishable from refusing
// everything, and the whole journal would be dead weight.
func TestJournal_ACoherentRecordIsStillHonoured(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	writeJournal(t, h, []activationAttempt{{
		Key: "br-guest|live-1", SessionID: "live-1", BootID: bootIDFor("A"),
		BeganBootMs: h.clk.ms, DeadlineBootMs: h.clk.ms + phase3ActivationGrace.Milliseconds(),
	}})
	h.restart(t)
	if h.p.unprovenUnknown {
		t.Fatal("a coherent record was rejected")
	}
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("a session inside its recorded grace was denied")
	}
	// and the countdown continues from the RECORDED start rather than restarting. One millisecond past the
	// recorded deadline, not exactly on it: the grace is inclusive of its final instant by design.
	h.advance(phase3ActivationGrace + time.Millisecond)
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("the restored record did not enforce its own deadline")
	}
}
