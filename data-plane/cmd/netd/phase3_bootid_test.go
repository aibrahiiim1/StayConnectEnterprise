package main

// The cross-reboot bound needs a monotonic uptime and a trustworthy boot id: an unreadable or empty id would
// compare "" with "" as the same boot, and a journal that parses but does not cohere (far-future deadline,
// negative reading, missing boot id, duplicate keys) must be treated like an unreadable file, never as a fresh
// grace. Fake-kernel tests; the disposable-namespace suite covers the kernel contract.

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// An unreadable boot id denies admission: the bound cannot be trusted after a reboot.
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

// An empty or whitespace boot id cannot distinguish two boots.
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

// /proc/sys/kernel/random/boot_id is a canonical lowercase RFC-4122 UUID (8-4-4-4-12); a looser check would let
// truncated reads compare equal and hide a real reboot.
func TestBootID_OnlyACanonicalLinuxBootIDIsAccepted(t *testing.T) {
	valid := []string{
		"f81d4fae-7dec-11d0-a765-00a0c91e6bf6",
		"00000000-0000-0000-0000-000000000000",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
	}
	for _, id := range valid {
		if !plausibleBootID(id) {
			t.Fatalf("a canonical Linux boot id was rejected: %q", id)
		}
	}

	invalid := []struct{ id, why string }{
		{"", "empty"},
		{"abc", "far too short"},
		{"1234567", "too short"},
		{"f81d4fae-7dec-11d0", "TRUNCATED at a group boundary"},
		{"f81d4fae-7dec-11d0-a765-00a0c91e6bf", "truncated by one character"},
		{"f81d4fae-7dec-11d0-a765-00a0c91e6bf67", "one character too long"},
		{"F81D4FAE-7DEC-11D0-A765-00A0C91E6BF6", "uppercase; the kernel emits lowercase"},
		{"f81d4fae7dec11d0a76500a0c91e6bf6", "no hyphens"},
		{"f81d4fae-7dec-11d0-a765_00a0c91e6bf6", "wrong separator"},
		{"g81d4fae-7dec-11d0-a765-00a0c91e6bf6", "non-hex character"},
		{"f81d4fae 7dec 11d0 a765 00a0c91e6bf6", "spaces instead of hyphens"},
		{"f81d4fae-7dec-11d0-a765-00a0c91e6bf6\n", "a trailing newline left in place"},
		{"cat: /proc/.../boot_id: No such file", "an error message copied into place"},
	}
	for _, tc := range invalid {
		if plausibleBootID(tc.id) {
			t.Fatalf("%q was accepted as a boot identity (%s)", tc.id, tc.why)
		}
	}
}

// A reboot followed by an unreadable boot id must not be mistaken for the same boot (a prior boot's large deadline would be compared with the new uptime).
func TestBootID_ARebootHiddenByAnUnreadableBootIDCannotRestoreAccess(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("setup: the guest was never admitted")
	}

	// Reboot with an unreadable boot id: nothing proves which boot the journalled deadline belongs to.
	h.reboot(t, bootIDFor("B"))
	h.clk.bootErr = errors.New("boot_id unreadable")
	h.clk.ms = 1_000 // fresh, small uptime

	for i := 0; i < 5; i++ {
		h.advance(20 * time.Second)
		h.pass(t, nil)
		if h.authorized() {
			t.Fatalf("pass %d: a reboot hidden by an unreadable boot id restored provisional access", i+1)
		}
	}
}

// A large prior-boot deadline plus a small new uptime must not be honoured even when the boot id is readable (it differs).
func TestBootID_APriorBootDeadlineIsNeverComparedWithANewUptime(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.clk.ms = 5_000_000
	h.pass(t, nil)
	at := h.attempts(t)
	if len(at) != 1 || at[0].DeadlineBootMs < 5_000_000 {
		t.Fatalf("setup: expected a large prior-boot deadline, got %+v", at)
	}

	h.reboot(t, bootIDFor("C"))
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("a prior boot's deadline was compared against the new boot's uptime and read as unexpired")
	}
}

// Once the boot identity is readable again, an ordinary session is admitted again.
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

// A proven-active session is not disconnected: durable state proving ACTIVE resolves it regardless of the journal.
func TestBootID_AProvenActiveSessionSurvivesAnUnreadableBootIdentity(t *testing.T) {
	h := newDurHarness(t)
	h.pass(t, nil)
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

// writeJournal puts an arbitrary valid-JSON document in the journal's place, under this appliance's scope.
func writeJournal(t *testing.T, h *durHarness, attempts []activationAttempt) {
	t.Helper()
	writeJournalScoped(t, h, h.p.mode.TenantID, h.p.mode.SiteID, h.p.mode.ApplianceID, attempts)
}

// writeJournalScoped writes the envelope under an arbitrary scope (another property's history).
func writeJournalScoped(t *testing.T, h *durHarness, tenant, site, appliance string, attempts []activationAttempt) {
	t.Helper()
	raw, err := json.Marshal(journalState{
		TenantID: tenant, SiteID: site, ApplianceID: appliance, Attempts: attempts})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.journalPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A journal from another tenant, site or appliance (restored image, cloned VM, copied /var/lib) is unknown, not
// empty: reading it as "no attempts outstanding" would award every guest a fresh grace.
func TestJournal_AForeignScopeIsUnknownNotAFreshStart(t *testing.T) {
	const otherID = "99999999-9999-4999-8999-999999999999"
	coherent := func(h *durHarness) []activationAttempt {
		return []activationAttempt{{
			Key: "br-guest|live-1", SessionID: "live-1", BootID: bootIDFor("A"),
			BeganBootMs: h.clk.ms, DeadlineBootMs: h.clk.ms + phase3ActivationGrace.Milliseconds(),
		}}
	}
	cases := []struct {
		name        string
		scope       func(h *durHarness) (string, string, string)
		withAttempt bool
	}{
		{"wrong tenant", func(h *durHarness) (string, string, string) {
			return otherID, h.p.mode.SiteID, h.p.mode.ApplianceID
		}, false},
		{"wrong site", func(h *durHarness) (string, string, string) {
			return h.p.mode.TenantID, otherID, h.p.mode.ApplianceID
		}, false},
		{"wrong appliance", func(h *durHarness) (string, string, string) {
			return h.p.mode.TenantID, h.p.mode.SiteID, otherID
		}, false},
		{"empty tenant", func(h *durHarness) (string, string, string) {
			return "", h.p.mode.SiteID, h.p.mode.ApplianceID
		}, false},
		{"empty site", func(h *durHarness) (string, string, string) {
			return h.p.mode.TenantID, "", h.p.mode.ApplianceID
		}, false},
		{"empty appliance", func(h *durHarness) (string, string, string) {
			return h.p.mode.TenantID, h.p.mode.SiteID, ""
		}, false},
		{"foreign scope with ZERO attempts", func(h *durHarness) (string, string, string) {
			return otherID, otherID, otherID
		}, false},
		{"foreign scope with an otherwise COHERENT attempt", func(h *durHarness) (string, string, string) {
			return otherID, otherID, otherID
		}, true},
	}

	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			h := newDurHarness(t)
			brokenDB(h)
			tenant, site, appliance := tc.scope(h)
			var attempts []activationAttempt
			if tc.withAttempt {
				attempts = coherent(h)
			}
			writeJournalScoped(t, h, tenant, site, appliance, attempts)
			h.restart(t)

			if !h.p.unprovenUnknown {
				t.Fatal("a journal from another scope was accepted as this appliance's history")
			}
			h.pass(t, nil)
			if h.authorized() {
				t.Fatal("a foreign-scope journal was read as a clean first run and awarded a fresh grace")
			}
			// It is never silently re-scoped into ours.
			after := h.p.journal.load(h.p.mode.TenantID, h.p.mode.SiteID, h.p.mode.ApplianceID)
			if !after.Unreadable {
				t.Fatal("the foreign journal was adopted into this appliance's scope")
			}
		})
	}
}

// The correct scope is still accepted.
func TestJournal_TheCorrectScopeIsAccepted(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	writeJournal(t, h, []activationAttempt{{
		Key: "br-guest|live-1", SessionID: "live-1", BootID: bootIDFor("A"),
		BeganBootMs: h.clk.ms, DeadlineBootMs: h.clk.ms + phase3ActivationGrace.Milliseconds(),
	}})
	h.restart(t)
	if h.p.unprovenUnknown {
		t.Fatal("this appliance's own journal was rejected")
	}
	if len(h.attempts(t)) != 1 {
		t.Fatalf("the record was not restored: %+v", h.attempts(t))
	}
}

// A truly absent file is the one legitimate clean first run.
func TestJournal_OnlyAnAbsentFileIsACleanFirstRun(t *testing.T) {
	h := newDurHarness(t)
	if err := os.Remove(h.journalPath()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	h.restart(t)
	if h.p.unprovenUnknown {
		t.Fatal("an absent journal was treated as unknown; every new appliance would fail closed")
	}
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("a healthy first run was denied")
	}
}

// parseClassKey is the only canonical reader: "contains a pipe" would let br-guest|a|b be read two ways and
// describe a different session than the one it was written for.
func TestJournal_CanonicalKeyParsing(t *testing.T) {
	bridge, session, ok := parseClassKey("br-guest|live-1")
	if !ok || bridge != "br-guest" || session != "live-1" {
		t.Fatalf("the canonical form did not parse: %q %q %v", bridge, session, ok)
	}
	for _, bad := range []string{
		"",
		"live-1",
		"|live-1",
		"br-guest|",
		"br-guest|a|b", // ambiguous: two separators, two readings
		"br-guest|live-1|",
		"|",
		"   |live-1",
		"br-guest|   ",
	} {
		if _, _, ok := parseClassKey(bad); ok {
			t.Fatalf("%q was accepted as a canonical class key", bad)
		}
	}
}

// Each case is valid JSON that would widen or erase the bound if believed; each must land as unknown, fail-closed.
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
		{"KEY AND SESSION NAME DIFFERENT SESSIONS", mutate(func(a *activationAttempt) {
			a.Key, a.SessionID = "br-guest|session-B", "session-A"
		})},
		{"key session differs by one character", mutate(func(a *activationAttempt) {
			a.SessionID = "live-2"
		})},
		{"empty bridge in the key", mutate(func(a *activationAttempt) {
			a.Key, a.SessionID = "|live-1", "live-1"
		})},
		{"no session suffix in the key", mutate(func(a *activationAttempt) {
			a.Key = "br-guest|"
		})},
		{"ambiguous key with two separators", mutate(func(a *activationAttempt) {
			a.Key, a.SessionID = "br-guest|live|1", "live|1"
		})},
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

// A coherent record is still honoured.
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
	// The countdown continues from the recorded start; one millisecond past the deadline, since the grace is inclusive of its final instant.
	h.advance(phase3ActivationGrace + time.Millisecond)
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("the restored record did not enforce its own deadline")
	}
}
