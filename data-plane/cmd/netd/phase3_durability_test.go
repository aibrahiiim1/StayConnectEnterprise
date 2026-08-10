package main

// DURABILITY AND SECURITY TIME.
//
// Two properties are under test here, and both are about what happens when something goes wrong at exactly
// the wrong instant:
//
//	WRITE-AHEAD — the bound on an unproven activation is fsynced to disk BEFORE the guest is provisionally
//	authorized. A crash anywhere after that recovers the same bound; a crash before it means no guest was
//	ever authorized, so there is nothing to recover. A write that cannot be proven durable denies access.
//
//	SECURITY TIME — that bound is measured against boot-relative monotonic time, not the wall clock. Moving
//	the wall clock backwards (NTP, a wrong RTC, a resumed snapshot) must not lengthen it by a single
//	millisecond, and a reboot must not manufacture a fresh one.
//
// The tests drive the REAL composition — real journal file, real fsync path, real load/restore/submit — because
// both defects lived in the seams between those steps rather than inside any one of them.
//
// FAKE-KERNEL tests. nft and tc are modelled; the disposable-namespace suite covers the kernel contract.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---- harness ----------------------------------------------------------------

// durHarness is one appliance: its two durable files, its kernel, and a clock the test controls.
//
// It keeps the MONOTONIC clock and the WALL clock as separate dials on purpose. Almost every test here is
// about what happens when they disagree, and a harness that moved them together could not express that.
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
		clk:  &fixedSecurityClock{ms: 60_000, bootID: "boot-A"},
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

// restart models a netd RESTART: new process object, same files, same kernel, same boot. It goes through the
// real load/restore path exactly as main.go does, so anything the files do not carry is genuinely lost.
func (h *durHarness) restart(t *testing.T) {
	t.Helper()
	p := h.build()
	st, _, _ := p.classStore.load()
	inv, verified := kernelInventory(t.Context(), p.shp, bridgesIn(st))
	p.restore(st, h.clk.bootID, inv, verified)
	p.restoreAttempts(p.journal.load())
	h.p = p
}

// reboot models a REBOOT: a new boot id, an empty kernel, the same files on disk.
func (h *durHarness) reboot(t *testing.T, bootID string) {
	t.Helper()
	h.tc, h.g = newFakeTC(), newFakeGate()
	h.clk.bootID = bootID
	h.clk.ms = 45_000 // the monotonic clock restarts near zero, as it does on every boot
	h.restart(t)
}

// advance moves monotonic time, kernel lease time and wall time together — an ordinary healthy interval.
func (h *durHarness) advance(d time.Duration) {
	h.clk.ms += d.Milliseconds()
	h.g.advance(d)
	h.wall = h.wall.Add(d)
}

// moveWall moves ONLY the wall clock. This is what an NTP correction or a wrong RTC does, and the whole point
// of security time is that it changes nothing.
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
	return h.p.journal.load().Attempts
}

func brokenDB(h *durHarness) {
	h.enf.err = errors.New("database unreachable")
	h.enf.confirmErr = errors.New("database unreachable")
}

// ---- 1. write-ahead durability ------------------------------------------------

// THE ORDERING ITSELF. The attempt is on disk, fsynced, before the guest is authorized — so a crash anywhere
// after admission finds the bound already recorded.
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
	if at[0].BootID != "boot-A" {
		t.Fatalf("the attempt does not name the boot its readings belong to: %+v", at[0])
	}
}

// CRASH IMMEDIATELY BEFORE THE RECORD. The write fails, so nothing is authorized — and a restart finds no
// attempt, because there is genuinely nothing to recover.
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

	// And a restart is a clean first run, because nothing was ever granted.
	h.p.journal.writeErr = nil
	h.restart(t)
	if h.p.unprovenUnknown {
		t.Fatal("a never-written journal was treated as corrupt")
	}
}

// THE FSYNC SEAM SPECIFICALLY. A write that reached the page cache and not the platter is the one that looks
// successful and is not, so it must be treated exactly like any other failure to prove durability.
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

// A WRITE FAILURE WITH A PREVIOUS VALID FILE ON DISK is the dangerous variant: the old file still loads, so a
// restart could easily look like a healthy appliance with no outstanding attempt. It must not — the earlier
// attempt is still recorded, and the bound continues from it.
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
	// The countdown continues from the ORIGINAL start, so the grace still expires on time.
	h.advance(phase3ActivationGrace)
	h.pass(t, nil)
	if h.authorized() {
		t.Fatalf("the guest is still authorized %v after the original attempt began",
			time.Duration(h.clk.ms-first[0].BeganBootMs)*time.Millisecond)
	}
}

// CRASH AFTER ADMISSION, REPEATEDLY. This is the loop the write-ahead record exists to stop: a process that
// dies just after authorizing must not hand the next process a clean slate.
func TestDurable_RepeatedCrashesJustAfterAdmissionCannotManufactureFreshGrace(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)
	began := h.attempts(t)[0].BeganBootMs

	authorizedFor := time.Duration(0)
	const step = 5 * time.Second
	for i := 0; i < 24; i++ { // two minutes of crash-restart cycles
		h.advance(step)
		h.restart(t) // the crash: no clean shutdown, no extra persistence
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

// AN UNREADABLE JOURNAL is not an empty one: the history of every session is unknown, so no fresh grace may
// be granted. A session durable state proves ACTIVE needs no grace and is still admitted.
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

	// The one thing that resolves it honestly: durable state proving the Session is already active.
	h.enf.err, h.enf.confirmErr = nil, nil
	h.enf.setState("live-1", "active")
	h.advance(time.Second)
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("a session durable state proves ACTIVE was denied because of an unrelated corrupt journal")
	}
}

// A PROVEN ACTIVATION CLEARS THE RECORD, and a clear that fails leaves it behind rather than turning a
// cleanup problem into a disconnection.
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

	// Now make the clear itself fail: the guest must stay online, and the stale marker is the safe residue.
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

// THE LOST-ACK CASE still recovers without an unnecessary disconnect, across a crash.
func TestDurable_ACommittedButUnacknowledgedActivationRecoversAcrossACrash(t *testing.T) {
	h := newDurHarness(t)
	h.enf.commitThenLoseAck["live-1"] = true
	h.pass(t, nil)
	if !h.authorized() {
		t.Fatal("the guest was disconnected by a lost acknowledgement")
	}
	h.advance(3 * time.Second)
	h.restart(t)
	h.enf.commitThenLoseAck = map[string]bool{} // the database answers normally again
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

// ---- 2. security time ---------------------------------------------------------

// WALL CLOCK BACKWARDS BY AN HOUR, same boot. The grace is measured monotonically, so it expires exactly on
// schedule — the jump changes nothing.
func TestSecurityTime_WallClockBackwardsOneHourCannotExtendTheGrace(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)

	h.advance(10 * time.Second)
	h.moveWall(-time.Hour) // NTP correction, or an RTC that came back wrong
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

// BACKWARDS BY A DAY, and forwards, and across a restart. Same conclusion, and the restart is included because
// that is where a persisted wall-clock timestamp would have been re-read and believed.
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

// AN UNREADABLE MONOTONIC CLOCK means the bound cannot be measured, and a bound that cannot be measured must
// not be assumed unspent.
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

// A REBOOT never yields a fresh grace. The prior monotonic readings belong to a timeline that no longer
// exists, and the only way to bridge them would be to ask the RTC — the clock this design refuses to trust.
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
			h.reboot(t, "boot-B")
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

// AFTER A REBOOT, durable state proving the Session ACTIVE resolves it safely and enforcement is rebuilt.
func TestSecurityTime_ARebootWhereTheDatabaseProvesActiveRebuildsEnforcement(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)
	h.advance(5 * time.Second)

	// The promotion had in fact committed before the crash; the database says so once it is reachable.
	h.enf.err, h.enf.confirmErr = nil, nil
	h.enf.setState("live-1", "active")
	h.reboot(t, "boot-C")
	h.pass(t, nil)

	if !h.authorized() {
		t.Fatal("a session durable state proves ACTIVE was left denied after a reboot")
	}
	if len(h.attempts(t)) != 0 {
		t.Fatalf("the resolved attempt record survived: %+v", h.attempts(t))
	}
}

// AFTER A REBOOT WITH THE DATABASE UNREADABLE, the session stays denied — it cannot be resolved, and a fresh
// grace is exactly what must not be granted.
func TestSecurityTime_ARebootWithAnUnreadableDatabaseStaysDenied(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil)
	h.advance(5 * time.Second)
	h.reboot(t, "boot-D")

	for i := 0; i < 6; i++ {
		h.advance(20 * time.Second)
		h.pass(t, nil)
		if h.authorized() {
			t.Fatalf("a reboot with an unreadable database re-admitted the guest after %d passes", i+1)
		}
	}
}

// A SESSION WITH NO WALL-CLOCK BOUNDARY is covered by every case above — it has no business deadline at all,
// so its only bound is this one, and it must still be finite across clock jumps and reboots.
func TestSecurityTime_NoAccessBoundarySessionIsStillBoundedAcrossJumpsAndReboot(t *testing.T) {
	h := newDurHarness(t)
	brokenDB(h)
	h.pass(t, nil) // leasePlanAt(_, nil, _) — no AccessEndsAt

	h.advance(5 * time.Second)
	h.moveWall(-12 * time.Hour)
	h.restart(t)
	h.advance(phase3ActivationGrace)
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("a session with no wall-clock boundary outlived its activation grace after a clock jump")
	}
	h.reboot(t, "boot-E")
	h.advance(time.Minute)
	h.pass(t, nil)
	if h.authorized() {
		t.Fatal("a reboot re-admitted a boundary-less session whose activation was never proven")
	}
}

// PRUNING must not be able to erase a still-relevant security record because wall time moved. The record is
// keyed to durable resolution, not to age, so a wall-clock jump of any size leaves it in place.
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
	// The record survived every jump, and the MONOTONIC bound is what ends the access — on schedule, and only
	// once monotonic time has actually passed.
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
