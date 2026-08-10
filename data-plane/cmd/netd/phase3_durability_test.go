package main

// THE ACTIVATION-UNCERTAINTY BOUND MUST SURVIVE A RESTART AND A REBOOT.
//
// A guest whose kernel enforcement is in force but whose Session cannot be proven `active` holds only a short
// provisional lease, and after a bounded grace is denied and quarantined. That bound used to be measured
// entirely in memory — so a netd restart handed the same guest a brand-new grace. A process restarting every
// few seconds could therefore renew a provisional authorization indefinitely while durable state never once
// said the guest was active, and every individual bound in the code would still have looked correct. The
// bound was real; it was just measured against process uptime, and process uptime is not a security property.
//
// These tests drive the REAL composition — real classStore file, real load(), real restore(), real submit() —
// rather than an isolated timer helper, because the defect lived in the seam between them.
//
// FAKE-KERNEL tests. nft and tc are modelled; the disposable-namespace suite covers the kernel contract.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// durableWriter builds a writer backed by a real on-disk class store, as netd's main does.
func durableWriter(t *testing.T, path string, tc *fakeTC, g *fakeGate, gens *fakeGenerations,
	origins *fakeOrigins, enf *fakeEnforcement) *phase3Shaping {
	t.Helper()
	p := sysWriter(tc, g, gens, origins, enf)
	p.classStore = &classStore{path: path}
	return p
}

// restartWriter models a netd RESTART: a brand-new process object, the same durable file, the same kernel.
// It goes through load() + restore() exactly as main.go does, so anything the file does not carry is
// genuinely lost — which is the point of the test.
func restartWriter(t *testing.T, path, bootID string, tc *fakeTC, g *fakeGate, gens *fakeGenerations,
	origins *fakeOrigins, enf *fakeEnforcement) *phase3Shaping {
	t.Helper()
	p := durableWriter(t, path, tc, g, gens, origins, enf)
	st, _, unreadable := p.classStore.load()
	inv, verified := kernelInventory(t.Context(), p.shp, bridgesIn(st))
	p.restore(st, bootID, inv, verified)
	p.unprovenUnknown = unreadable
	return p
}

func brokenEnforcement() *fakeEnforcement {
	enf := newFakeEnforcement()
	enf.err = errors.New("database unreachable")
	enf.confirmErr = errors.New("database unreachable")
	return enf
}

// ---- 1-3. unreadable ACTIVE, netd restarting throughout ----------------------

// The scenario the correction names: the promotion is unreadable for far longer than the grace, and netd
// restarts every few seconds throughout. The guest must not end up with an endlessly renewed provisional
// authorization.
func TestDurable_RestartsThroughoutAnUnreadableActivationCannotRenewForever(t *testing.T) {
	path := filepath.Join(t.TempDir(), "classes.json")
	tc, g := newFakeTC(), newFakeGate()
	gens, origins, enf := &fakeGenerations{}, &fakeOrigins{}, brokenEnforcement()

	start := time.Now()
	p := durableWriter(t, path, tc, g, gens, origins, enf)
	if _, err := p.submit(t.Context(), leasePlanAt(1, nil, start), start); err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if !g.isAuthorized("br-guest", provIP) {
		t.Fatal("the provisional admission did not take, so this proves nothing")
	}

	// Three minutes of restarts, every six seconds. The kernel keeps its state across a process restart, so
	// only netd's memory is lost — exactly the window the durable clock has to cover.
	at := start
	authorizedFor := time.Duration(0)
	const step = 6 * time.Second
	for i := int64(2); at.Sub(start) < 3*time.Minute; i++ {
		at = at.Add(step)
		g.advance(step)
		p = restartWriter(t, path, "boot-same", tc, g, gens, origins, enf)
		if _, err := p.submit(t.Context(), leasePlanAt(i, nil, at), at); err != nil {
			t.Fatalf("plan refused at %v: %v", at.Sub(start), err)
		}
		if g.isAuthorized("br-guest", provIP) {
			authorizedFor += step
		}
	}

	if g.isAuthorized("br-guest", provIP) {
		t.Fatalf("after %v of restarts with an unprovable activation the guest is STILL authorized: the "+
			"restart resets the grace, so the bound is process uptime rather than a durable clock", at.Sub(start))
	}
	// And the total authorized time is bounded by roughly the grace, not by the length of the run.
	if authorizedFor > phase3ActivationGrace+2*step {
		t.Fatalf("the guest was authorized for %v across the run; the durable bound is %v",
			authorizedFor, phase3ActivationGrace)
	}
	if tc.countForwarding() != 0 {
		t.Fatal("packet authorization ended but the accountable class was left classifying")
	}
}

// The durable file must actually carry the clock. This asserts the mechanism directly, so a change that keeps
// the behaviour by accident — and loses it on the next refactor — still fails here.
func TestDurable_TheActivationClockIsPersistedAndRestored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "classes.json")
	tc, g := newFakeTC(), newFakeGate()
	gens, origins, enf := &fakeGenerations{}, &fakeOrigins{}, brokenEnforcement()

	start := time.Now()
	p := durableWriter(t, path, tc, g, gens, origins, enf)
	if _, err := p.submit(t.Context(), leasePlanAt(1, nil, start), start); err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	st, ok, unreadable := p.classStore.load()
	if !ok || unreadable {
		t.Fatalf("durable state not readable back (ok=%v unreadable=%v)", ok, unreadable)
	}
	if len(st.Unproven) != 1 {
		t.Fatalf("the activation clock was not persisted: %+v", st.Unproven)
	}
	if st.Unproven[0].SinceUnixMs == 0 {
		t.Fatalf("the persisted record carries no start time: %+v", st.Unproven[0])
	}

	// And it comes back on restore, continuing the SAME countdown.
	p2 := restartWriter(t, path, "boot-same", tc, g, gens, origins, enf)
	q := p2.quarantined[classKey("br-guest", "live-1")]
	if q == nil || q.Since.IsZero() {
		t.Fatalf("the activation clock did not survive the restart: %+v", q)
	}
	if delta := q.Since.Sub(start); delta > time.Second || delta < -time.Second {
		t.Fatalf("the restored clock starts %v from the original failure rather than at it", delta)
	}
}

// ---- 4. the same, across a REBOOT --------------------------------------------

// A reboot legitimately drops every tc class and every nft element — the kernel comes back empty. It must NOT
// drop the fact that this session's activation has been unprovable, or rebooting would be a way to buy a
// fresh grace period.
func TestDurable_ARebootDoesNotResetTheUnprovenBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "classes.json")
	tc, g := newFakeTC(), newFakeGate()
	gens, origins, enf := &fakeGenerations{}, &fakeOrigins{}, brokenEnforcement()

	start := time.Now()
	p := durableWriter(t, path, tc, g, gens, origins, enf)
	if _, err := p.submit(t.Context(), leasePlanAt(1, nil, start), start); err != nil {
		t.Fatalf("plan refused: %v", err)
	}

	// Let the grace run out, then REBOOT: new boot id, empty kernel.
	at := start.Add(phase3ActivationGrace + 5*time.Second)
	g.advance(phase3ActivationGrace + 5*time.Second)
	if _, err := p.submit(t.Context(), leasePlanAt(2, nil, at), at); err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("the grace was not enforced before the reboot, so the reboot proves nothing")
	}

	tc2, g2 := newFakeTC(), newFakeGate() // the kernel came back empty
	at = at.Add(10 * time.Second)
	p2 := restartWriter(t, path, "boot-AFTER-REBOOT", tc2, g2, gens, origins, enf)
	if _, err := p2.submit(t.Context(), leasePlanAt(3, nil, at), at); err != nil {
		t.Fatalf("plan refused after reboot: %v", err)
	}
	if g2.isAuthorized("br-guest", provIP) {
		t.Fatal("a reboot re-admitted a session whose activation grace was already exhausted")
	}
	if tc2.countForwarding() != 0 {
		t.Fatal("a reboot re-installed a classifying class for a quarantined session")
	}
}

// ---- 5. the durable record itself is unreadable ------------------------------

// If the clock cannot be read, the session's history is UNKNOWN — not empty. Awarding a fresh grace there
// would mean corrupting or deleting one file buys a guest unbounded provisional authorization.
func TestDurable_AnUnreadableClockFailsClosedRatherThanGrantingAFreshGrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "classes.json")
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	tc, g := newFakeTC(), newFakeGate()
	gens, origins, enf := &fakeGenerations{}, &fakeOrigins{}, brokenEnforcement()

	now := time.Now()
	p := restartWriter(t, path, "boot-x", tc, g, gens, origins, enf)
	if !p.unprovenUnknown {
		t.Fatal("a corrupt durable file was not recognised as an unknown activation clock")
	}
	res, err := p.submit(t.Context(), leasePlanAt(1, nil, now), now)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if !res.Degraded {
		t.Fatal("an unprovable activation with an unknown clock reported a clean convergence")
	}
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("a corrupt durable clock awarded a fresh grace period: the guest is authorized")
	}
	if tc.countForwarding() != 0 {
		t.Fatal("the class was left classifying after failing closed")
	}
}

// A LEGITIMATELY ABSENT file (a first-ever run) is not the same thing, and must still allow the ordinary
// grace — otherwise the fail-closed rule would break every new appliance.
func TestDurable_AnAbsentFileIsNotTreatedAsCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	tc, g := newFakeTC(), newFakeGate()
	gens, origins, enf := &fakeGenerations{}, &fakeOrigins{}, newFakeEnforcement()
	p := restartWriter(t, path, "boot-first", tc, g, gens, origins, enf)
	if p.unprovenUnknown {
		t.Fatal("a first-ever run was treated as a corrupt clock; every new appliance would fail closed")
	}
	now := time.Now()
	if _, err := p.submit(t.Context(), leasePlanAt(1, nil, now), now); err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if !g.isAuthorized("br-guest", provIP) {
		t.Fatal("a healthy first run did not authorize the guest")
	}
}

// ---- 6-7. recovery, and what recovery is NOT ---------------------------------

// THE LOST-ACK CASE MUST STILL RECOVER. If the promotion really committed and only the answer was lost, the
// authoritative re-read finds it and the guest is never disconnected — across a restart too.
func TestDurable_ARecoveredCommittedActivationConvergesAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "classes.json")
	tc, g := newFakeTC(), newFakeGate()
	gens, origins, enf := &fakeGenerations{}, &fakeOrigins{}, brokenEnforcement()

	start := time.Now()
	p := durableWriter(t, path, tc, g, gens, origins, enf)
	if _, err := p.submit(t.Context(), leasePlanAt(1, nil, start), start); err != nil {
		t.Fatalf("plan refused: %v", err)
	}

	// The database comes back and reveals that the promotion HAD committed all along.
	enf.err, enf.confirmErr = nil, nil
	enf.setState("live-1", "active")

	at := start.Add(10 * time.Second)
	g.advance(10 * time.Second)
	p2 := restartWriter(t, path, "boot-same", tc, g, gens, origins, enf)
	res, err := p2.submit(t.Context(), leasePlanAt(2, nil, at), at)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if res.Degraded {
		t.Fatalf("a recovered, genuinely active session was reported degraded: %+v", res.Problems)
	}
	if !g.isAuthorized("br-guest", provIP) {
		t.Fatal("a genuinely active session was disconnected during recovery")
	}
	if got := g.leaseOf("br-guest", provIP); got != phase3LeaseTTL {
		t.Fatalf("a proven session holds a %v lease, want the full %v", got, phase3LeaseTTL)
	}
	// and the durable clock is cleared, so it cannot later be mistaken for an exhausted grace
	st, _, _ := p2.classStore.load()
	for _, r := range st.Unproven {
		if r.Key == classKey("br-guest", "live-1") && r.SinceUnixMs != 0 {
			t.Fatalf("a proven activation left an unproven record behind: %+v", r)
		}
	}
}

// RECOVERY THAT SHOWS *PENDING* IS NOT RECOVERY. The database becoming readable again while the Session is
// still PENDING_ENFORCEMENT must not silently restart the countdown.
func TestDurable_DatabaseRecoveryShowingPendingDoesNotResetTheExhaustedGrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "classes.json")
	tc, g := newFakeTC(), newFakeGate()
	gens, origins, enf := &fakeGenerations{}, &fakeOrigins{}, brokenEnforcement()

	start := time.Now()
	p := durableWriter(t, path, tc, g, gens, origins, enf)
	if _, err := p.submit(t.Context(), leasePlanAt(1, nil, start), start); err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	at := start.Add(phase3ActivationGrace + 5*time.Second)
	g.advance(phase3ActivationGrace + 5*time.Second)
	if _, err := p.submit(t.Context(), leasePlanAt(2, nil, at), at); err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("the grace was not exhausted, so this proves nothing")
	}
	strikesBefore := p.quarantined[classKey("br-guest", "live-1")].Strikes

	// The database is readable again — but the Session is still PENDING, and the promotion still fails.
	enf.confirmErr = nil

	at = at.Add(5 * time.Second)
	g.advance(5 * time.Second)
	p2 := restartWriter(t, path, "boot-same", tc, g, gens, origins, enf)
	if _, err := p2.submit(t.Context(), leasePlanAt(3, nil, at), at); err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("a readable-but-PENDING database re-admitted a session whose grace was already exhausted")
	}
	q := p2.quarantined[classKey("br-guest", "live-1")]
	if q == nil || q.Strikes < strikesBefore {
		t.Fatalf("the strike count regressed across the restart: %+v", q)
	}
}

// ---- 8. no wall-clock boundary is still a finite bound -----------------------

// A session with no AccessEndsAt has no business deadline at all. Its unproven-activation bound must still be
// finite — otherwise "no stated end date" would quietly mean "unbounded provisional authorization".
func TestDurable_ASessionWithNoAccessBoundaryStillHasAFiniteUnprovenBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "classes.json")
	tc, g := newFakeTC(), newFakeGate()
	gens, origins, enf := &fakeGenerations{}, &fakeOrigins{}, brokenEnforcement()

	start := time.Now()
	p := durableWriter(t, path, tc, g, gens, origins, enf)
	// leasePlanAt(_, nil, _) is exactly the no-boundary case.
	if _, err := p.submit(t.Context(), leasePlanAt(1, nil, start), start); err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	at := start
	for i := int64(2); at.Sub(start) < 2*time.Minute; i++ {
		at = at.Add(10 * time.Second)
		g.advance(10 * time.Second)
		p = restartWriter(t, path, "boot-same", tc, g, gens, origins, enf)
		if _, err := p.submit(t.Context(), leasePlanAt(i, nil, at), at); err != nil {
			t.Fatalf("plan refused: %v", err)
		}
	}
	if g.isAuthorized("br-guest", provIP) {
		t.Fatalf("a session with no wall-clock boundary is still authorized after %v of unprovable activation",
			at.Sub(start))
	}
}
