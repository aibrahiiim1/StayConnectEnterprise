package main

// Write-ahead durability and monotonic security time of the activation bound, against the real journal and
// fsync path with a fake nft/tc kernel.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// bootIDFor builds a distinct, canonical boot identity per tag.
func bootIDFor(tag string) string {
	// Must be a canonical Linux boot id (lowercase hex, 8-4-4-4-12); the security clock validates the real format.
	hex := "0123456789abcdef"
	var b byte
	if tag != "" {
		b = tag[0]
	}
	return "f81d4fae-7dec-11d0-a765-00a0c91e6b" + string(hex[(b>>4)&0x0f]) + string(hex[b&0x0f])
}

// durHarness is one appliance with separate monotonic and wall clocks, so tests can make them disagree.
type durHarness struct {
	dir     string
	tc      *fakeTC
	g       *fakeGate
	gens    *fakeGenerations
	origins *fakeOrigins
	enf     *fakeEnforcement
	clk     *fixedSecurityClock
	p       *phase3Shaping
	wall    time.Time
	gen     int64
}

func newDurHarness(t *testing.T) *durHarness {
	t.Helper()
	h := &durHarness{
		dir: t.TempDir(), tc: newFakeTC(), g: newFakeGate(),
		gens: &fakeGenerations{}, origins: &fakeOrigins{}, enf: newFakeEnforcement(),
		clk:  &fixedSecurityClock{ms: 60_000, bootID: bootIDFor("A")},
		wall: time.Now(), gen: 0,
	}
	h.p = h.build()
	return h
}

func (h *durHarness) classPath() string   { return filepath.Join(h.dir, "classes.json") }
func (h *durHarness) journalPath() string { return filepath.Join(h.dir, "activation-journal.json") }

// build makes a fresh process object over the SAME durable files and the same kernel.
func (h *durHarness) build() *phase3Shaping {
	p := sysWriter(h.tc, h.g, h.gens, h.origins, h.enf)
	p.classStore = &classStore{path: h.classPath()}
	p.journal = &activationJournal{path: h.journalPath()}
	p.secClock = h.clk
	return p
}

// restart models a netd restart through the real load/restore path: same files, kernel and boot.
func (h *durHarness) restart(t *testing.T) {
	t.Helper()
	p := h.build()
	st, _, _ := p.classStore.load()
	inv, verified := kernelInventory(t.Context(), p.shp, bridgesIn(st))
	p.restore(st, h.clk.bootID, inv, verified)
	p.restoreAttempts(p.journal.load(p.mode.TenantID, p.mode.SiteID, p.mode.ApplianceID))
	h.p = p
}

// reboot models a new boot id, an empty kernel and the same files.
func (h *durHarness) reboot(t *testing.T, bootID string) {
	t.Helper()
	h.tc, h.g = newFakeTC(), newFakeGate()
	h.clk.bootID = bootID
	h.clk.ms = 45_000
	h.restart(t)
}

// advance moves monotonic, kernel lease and wall time together.
func (h *durHarness) advance(d time.Duration) {
	h.clk.ms += d.Milliseconds()
	h.g.advance(d)
	h.wall = h.wall.Add(d)
}

// moveWall moves only the wall clock, like an NTP correction or a wrong RTC.
func (h *durHarness) moveWall(d time.Duration) { h.wall = h.wall.Add(d) }

// pass runs one reconciliation for a single entitled session.
func (h *durHarness) pass(t *testing.T, endsAt *time.Time) shapingPlanResponse {
	t.Helper()
	h.gen++
	res, err := h.p.submit(t.Context(), leasePlanAt(h.gen, endsAt, h.wall), h.wall)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	return res
}

func (h *durHarness) authorized() bool { return h.g.isAuthorized("br-guest", provIP) }

func (h *durHarness) attempts(t *testing.T) []activationAttempt {
	t.Helper()
	return h.p.journal.load(h.p.mode.TenantID, h.p.mode.SiteID, h.p.mode.ApplianceID).Attempts
}

func brokenDB(h *durHarness) {
	h.enf.err = errors.New("database unreachable")
	h.enf.confirmErr = errors.New("database unreachable")
}

// The attempt is fsynced before the guest is authorized.
func TestDurable_TheAttemptIsRecordedBeforeTheGuestIsAuthorized(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)

	if !h.authorized() {
		t.Fatal("the provisional admission did not take, so this proves nothing about ordering")
	}
	at := h.attempts(t)
	if len(at) != 1 {
		t.Fatalf("the attempt was not journalled: %+v", at)
	}
	if at[0].BeganBootMs == 0 || at[0].DeadlineBootMs <= at[0].BeganBootMs {
		t.Fatalf("the journalled attempt carries no usable bound: %+v", at[0])
	}
	if at[0].BootID != bootIDFor("A") {
		t.Fatalf("the attempt does not name the boot its readings belong to: %+v", at[0])
	}
}

// A failed write-ahead authorizes nothing and leaves nothing to recover.
func TestDurable_AFailedWriteAheadDeniesAdmissionEntirely(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.p.journal.writeErr = errors.New("no space left on device")

	res := h.pass(t, nil)
	if !res.Degraded {
		t.Fatal("an undurable activation bound was reported as a clean convergence")
	}
	if h.authorized() {
		t.Fatal("the guest was authorized although the bound could not be made durable")
	}
	if h.tc.countForwarding() != 0 {
		t.Fatal("a class was left classifying after the write-ahead failure")
	}
	if len(h.attempts(t)) != 0 {
		t.Fatal("a failed write left an attempt record behind")
	}

	h.p.journal.writeErr = nil
	h.restart(t)
	if h.p.unprovenUnknown {
		t.Fatal("a never-written journal was treated as corrupt")
	}
}

// A write that reached the page cache but failed fsync is a durability failure like any other.
func TestDurable_AnFsyncFailureDeniesAdmission(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.p.journal.syncErr = errors.New("fsync: input/output error")

	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("the guest was authorized although the bound was never fsynced")
	}
	if len(h.attempts(t)) != 0 {
		t.Fatal("an unsynced write left a visible attempt record")
	}
}

// A write failure with an older valid file on disk must not look like a clean appliance; the earlier bound continues.
func TestDurable_AWriteFailureWithAnOlderValidFileStillHonoursTheEarlierBound(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil) // attempt 1 is durably recorded and the guest admitted
	first := h.attempts(t)
	if len(first) != 1 {
		t.Fatalf("setup: expected one attempt, got %+v", first)
	}

	// Now every later write fails, and the process restarts.
	h.p.journal.writeErr = errors.New("read-only file system")
	h.advance(10 * time.Second)
	h.pass(t, nil)
	h.restart(t)

	got := h.attempts(t)
	if len(got) != 1 || got[0].BeganBootMs != first[0].BeganBootMs {
		t.Fatalf("the earlier bound was lost or restarted: before=%+v after=%+v", first[0], got)
	}
	h.advance(phase3ActivationGrace)
	h.pass(t, nil)
	if h.authorized() {
		t.Fatalf("the guest is still authorized %v after the original attempt began",
			time.Duration(h.clk.ms-first[0].BeganBootMs)*time.Millisecond)
	}
}

// A process that dies just after authorizing must not hand the next one a clean slate.
func TestDurable_RepeatedCrashesJustAfterAdmissionCannotManufactureFreshGrace(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)
	began := h.attempts(t)[0].BeganBootMs

	authorizedFor := time.Duration(0)
	const step = 5 * time.Second
	for i := 0; i < 24; i++ {
		h.advance(step)
		h.restart(t)
		h.pass(t, nil)
		if h.authorized() {
			authorizedFor += step
		}
		if got := h.attempts(t); len(got) > 0 && got[0].BeganBootMs != began && got[0].BeganBootMs != 0 {
			t.Fatalf("a restart moved the attempt start from %d to %d", began, got[0].BeganBootMs)
		}
	}
	if h.authorized() {
		t.Fatal("after two minutes of crash-restart cycles the guest is STILL authorized")
	}
	if authorizedFor > phase3ActivationGrace+2*step {
		t.Fatalf("the guest was authorized for %v across the run; the bound is %v", authorizedFor, phase3ActivationGrace)
	}
}

// An unreadable journal is not an empty one: no fresh grace, but a session proven active is still admitted.
func TestDurable_ACorruptJournalDeniesFreshGraceButNotAProvenActiveSession(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	if err := os.WriteFile(h.journalPath(), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.restart(t)
	if !h.p.unprovenUnknown {
		t.Fatal("a corrupt journal was not recognised as unknown history")
	}
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("a corrupt journal awarded a fresh grace period")
	}

	h.enf.err, h.enf.confirmErr = nil, nil
	h.enf.setState("live-1", "active")
	h.advance(time.Second)
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("a session durable state proves ACTIVE was denied because of an unrelated corrupt journal")
	}
}

// A proven activation clears the record; a failed clear leaves it behind rather than disconnecting the guest.
func TestDurable_AProvenActivationClearsTheRecordAndAFailedClearIsSafe(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)
	if len(h.attempts(t)) != 1 {
		t.Fatal("setup: no attempt recorded")
	}

	h.enf.err, h.enf.confirmErr = nil, nil
	h.advance(5 * time.Second)
	h.pass(t, nil)
	if len(h.attempts(t)) != 0 {
		t.Fatalf("a proven activation left its attempt record behind: %+v", h.attempts(t))
	}
	if !h.authorized() {
		t.Fatal("a proven session lost access")
	}
	if got := h.g.leaseOf("br-guest", provIP); got != phase3LeaseTTL {
		t.Fatalf("a proven session holds a %v lease, want the full %v", got, phase3LeaseTTL)
	}

	brokenDB(h)
	h.advance(5 * time.Second)
	h.pass(t, nil)
	h.p.journal.writeErr = errors.New("read-only file system")
	h.enf.err, h.enf.confirmErr = nil, nil
	h.advance(5 * time.Second)
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("a failed record CLEAR disconnected a session that was proven active")
	}
}

// A lost acknowledgement recovers across a crash without a disconnect.
func TestDurable_ACommittedButUnacknowledgedActivationRecoversAcrossACrash(t *testing.T) {
	h := newDurHarness(t)
	h.enf.commitThenLoseAck["live-1"] = true
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("the guest was disconnected by a lost acknowledgement")
	}
	h.advance(3 * time.Second)
	h.restart(t)
	h.enf.commitThenLoseAck = map[string]bool{}
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("a genuinely active session was disconnected during recovery")
	}
	if h.enf.sessionState("live-1") != "active" {
		t.Fatal("the session did not converge to active")
	}
	if len(h.attempts(t)) != 0 {
		t.Fatalf("a recovered session left an attempt record behind: %+v", h.attempts(t))
	}
}

// The grace is monotonic, so a wall-clock jump back does not extend it.
func TestSecurityTime_WallClockBackwardsOneHourCannotExtendTheGrace(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)

	h.advance(10 * time.Second)
	h.moveWall(-time.Hour)
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("the guest lost access early; the wall-clock jump should have changed nothing")
	}

	h.advance(phase3ActivationGrace)
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("moving the wall clock back an hour extended the activation grace")
	}
}

// Large jumps in either direction, across a restart, still cannot extend the grace.
func TestSecurityTime_LargeClockJumpsAndRestartsCannotExtendTheGrace(t *testing.T) {
	for _, tc := range []struct {
		name string
		jump time.Duration
	}{
		{"back 24h", -24 * time.Hour},
		{"forward 24h", 24 * time.Hour},
		{"back 1h then restart", -time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDurHarness(t)
			brokenDB(h)
			h.pass(t, nil)
			h.advance(5 * time.Second)
			h.moveWall(tc.jump)
			h.restart(t)
			h.pass(t, nil)
			if !h.authorized() {
				t.Fatal("the guest lost access early")
			}
			h.advance(phase3ActivationGrace)
			h.pass(t, nil)
			if h.authorized() {
				t.Fatalf("a %v wall-clock jump extended the activation grace", tc.jump)
			}
		})
	}
}

// An unreadable monotonic clock denies admission: an unmeasurable bound is not assumed unspent.
func TestSecurityTime_AnUnreadableMonotonicClockDeniesAdmission(t *testing.T) {
	h := newDurHarness(t)
	h.clk.err = errors.New("/proc/uptime: no such file or directory")
	res := h.pass(t, nil)
	if !res.Degraded {
		t.Fatal("an unmeasurable bound was reported as a clean convergence")
	}
	if h.authorized() {
		t.Fatal("a guest was authorized while the security clock was unreadable")
	}
}

// A reboot never yields a fresh grace; bridging the timelines would require trusting the RTC.
func TestSecurityTime_ARebootDoesNotCreateAFreshGrace(t *testing.T) {
	for _, tc := range []struct {
		name string
		rtc  time.Duration
	}{
		{"RTC behind the previous value", -6 * time.Hour},
		{"RTC ahead of the previous value", 6 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDurHarness(t)
			brokenDB(h)
			h.pass(t, nil)
			if !h.authorized() {
				t.Fatal("setup: the guest was never admitted")
			}

			h.advance(5 * time.Second)
			h.moveWall(tc.rtc)
			h.reboot(t, bootIDFor("B"))
			h.pass(t, nil)

			if h.authorized() {
				t.Fatal("a reboot re-admitted a session whose activation was still unproven")
			}
			if h.tc.countForwarding() != 0 {
				t.Fatal("a reboot re-installed a classifying class for an unresolved session")
			}
		})
	}
}

// After a reboot, durable state proving the session active resolves it and rebuilds enforcement.
func TestSecurityTime_ARebootWhereTheDatabaseProvesActiveRebuildsEnforcement(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)
	h.advance(5 * time.Second)

	h.enf.err, h.enf.confirmErr = nil, nil
	h.enf.setState("live-1", "active")
	h.reboot(t, bootIDFor("C"))
	h.pass(t, nil)

	if !h.authorized() {
		t.Fatal("a session durable state proves ACTIVE was left denied after a reboot")
	}
	if len(h.attempts(t)) != 0 {
		t.Fatalf("the resolved attempt record survived: %+v", h.attempts(t))
	}
}

// After a reboot with the database unreadable the session stays denied; no fresh grace.
func TestSecurityTime_ARebootWithAnUnreadableDatabaseStaysDenied(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)
	h.advance(5 * time.Second)
	h.reboot(t, bootIDFor("D"))

	for i := 0; i < 6; i++ {
		h.advance(20 * time.Second)
		h.pass(t, nil)
		if h.authorized() {
			t.Fatalf("a reboot with an unreadable database re-admitted the guest after %d passes", i+1)
		}
	}
}

// A session with no access boundary is bounded only by the activation grace, across clock jumps and reboots.
func TestSecurityTime_NoAccessBoundarySessionIsStillBoundedAcrossJumpsAndReboot(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)

	h.advance(5 * time.Second)
	h.moveWall(-12 * time.Hour)
	h.restart(t)
	h.advance(phase3ActivationGrace)
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("a session with no wall-clock boundary outlived its activation grace after a clock jump")
	}
	h.reboot(t, bootIDFor("E"))
	h.advance(time.Minute)
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("a reboot re-admitted a boundary-less session whose activation was never proven")
	}
}

// The record is keyed to durable resolution, not age, so wall-clock jumps never prune an unresolved one.
func TestSecurityTime_WallClockJumpsCannotPruneAnUnresolvedSecurityRecord(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)
	if len(h.attempts(t)) != 1 {
		t.Fatal("setup: no attempt recorded")
	}

	for _, jump := range []time.Duration{48 * time.Hour, -72 * time.Hour, 365 * 24 * time.Hour} {
		h.moveWall(jump)
		h.advance(time.Second)
		h.pass(t, nil)
		if len(h.attempts(t)) != 1 {
			t.Fatalf("a %v wall-clock jump pruned an unresolved security record: %+v", jump, h.attempts(t))
		}
	}
	if !h.authorized() {
		t.Fatal("a few seconds of monotonic time ended a 30-second grace; the wall-clock jumps leaked into the bound")
	}
	h.advance(phase3ActivationGrace)
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("the guest outlived the grace once monotonic time had genuinely elapsed")
	}
	if len(h.attempts(t)) != 1 {
		t.Fatalf("the security record was dropped when the grace was enforced: %+v", h.attempts(t))
	}
}
