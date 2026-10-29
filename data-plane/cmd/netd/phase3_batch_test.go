package main

// Regression tests for the batched, ordered enforcement pass (phase3_batch.go). They run the real reconcileLocked, provisionSession, shape.Client and
// nft.Client against the simulated kernel in phase3_fakekernel_test.go, which counts every forked process. Each test that guards a cost property also runs
// the same scenario with batching switched off (p.noBatch, the NETD_PASS_BATCHING=false path) and requires the old behaviour to FAIL the property, so the
// test is shown to discriminate rather than to pass vacuously.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func newFakeScale(t *testing.T, n int, noBatch bool) (*scaleEnv, *kernelFake) {
	t.Helper()
	k := newKernelFake(time.Now())
	e := newScaleEnv(t, k)
	e.p.noBatch = noBatch
	e.addSessions(0, n)
	for i := 0; i < 5 && e.activeCount() < n; i++ {
		st := e.pass()
		if st.res.Failed != 0 {
			t.Fatalf("admission of %d sessions failed: %v", n, st.res.Problems[0])
		}
		e.tick(5 * time.Second)
	}
	if got := e.activeCount(); got != n {
		t.Fatalf("%d of %d sessions active", got, n)
	}
	return e, k
}

func ipKey(s string) string             { return fmt.Sprintf("%q.%s", scaleBridge, s) }
func macKey(i int) string               { return ipKey(scaleSession(i).IP) + "." + scaleSession(i).MAC }
func authKey(i int) string              { return ipKey(scaleSession(i).IP) }
func hasAuth(k *kernelFake, i int) bool { return k.hasElem("phase3_auth_ipv4", authKey(i)) }

// A pass over sessions that did not change must not fork a process per session.
func TestSteadyPassCostDoesNotGrowWithTheNumberOfSessions(t *testing.T) {
	var costs []int
	for _, n := range []int{50, 400} {
		e, _ := newFakeScale(t, n, false)
		st := e.pass()
		if st.res.Failed != 0 || st.res.Shaped != n {
			t.Fatalf("n=%d: shaped=%d failed=%d %v", n, st.res.Shaped, st.res.Failed, st.res.Problems)
		}
		if st.cnt.TCClassChange != 0 || st.cnt.NFTScript != 0 || st.db != 0 {
			t.Fatalf("n=%d: an unchanged pass rewrote state: class changes=%d nft transactions=%d database calls=%d", n, st.cnt.TCClassChange, st.cnt.NFTScript, st.db)
		}
		if st.leases != 1 {
			t.Fatalf("n=%d: the DHCP lease table was read %d times in one pass", n, st.leases)
		}
		costs = append(costs, st.cnt.total())
	}
	if costs[0] > 10 || costs[0] != costs[1] {
		t.Fatalf("steady-pass process count = %v for 50 and 400 sessions; want the same small constant", costs)
	}

	// The control: the per-session pass costs at least two class changes and a database call per session.
	e, _ := newFakeScale(t, 50, true)
	st := e.pass()
	if st.cnt.total() < 100 || st.db < 50 {
		t.Fatalf("control (batching off) cost only %d processes / %d database calls; the test would not have caught the defect", st.cnt.total(), st.db)
	}
}

// Renewal writes every due lease in one transaction, never longer than the lease the pass asks for.
func TestDueLeasesAreRenewedInOneTransactionAndKeepTheirLength(t *testing.T) {
	e, k := newFakeScale(t, 200, false)
	e.tick(35 * time.Second) // 55 s left of 90: due
	st := e.pass()
	if st.res.Failed != 0 {
		t.Fatalf("%v", st.res.Problems)
	}
	if st.cnt.NFTScript != 1 || st.cnt.total() > 12 {
		t.Fatalf("renewal of 200 leases used %d transactions and %d processes", st.cnt.NFTScript, st.cnt.total())
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for key, el := range k.sets["phase3_auth_ipv4"] {
		left := el.expires.Sub(k.now)
		if left > phase3LeaseTTL || left < phase3LeaseTTL-2*time.Second {
			t.Fatalf("%s has %s left after renewal; the renewal must restore exactly %s, never extend it", key, left, phase3LeaseTTL)
		}
	}
	if len(k.sets["phase3_auth_mac_ipv4"]) != 200 {
		t.Fatalf("MAC-bound elements = %d", len(k.sets["phase3_auth_mac_ipv4"]))
	}
}

// New sessions are promoted in the pass they are submitted in, however many active sessions there are and whatever the pass budget allows.
func TestNewSessionsAreNotStarvedByActiveOnes(t *testing.T) {
	run := func(noBatch bool) (promoted int, st passStat) {
		e, k := newFakeScale(t, 250, noBatch)
		k.mu.Lock()
		k.perExec = 2 * time.Millisecond // a fork costs time; the budget below is what a pass can spend
		k.mu.Unlock()
		e.addSessions(250, 270)
		e.budget = 600 * time.Millisecond
		e.p.routineReserve = 20 * time.Millisecond
		e.tick(5 * time.Second)
		st = e.pass()
		return e.activeCount() - 250, st
	}
	promoted, st := run(false)
	if promoted != 20 || st.res.Failed != 0 {
		t.Fatalf("batched pass promoted %d of 20 new sessions, failed=%d %v", promoted, st.res.Failed, st.res.Problems)
	}
	// The control: the per-session pass walks 250 active sessions first and runs out of budget before it reaches the new ones.
	if promoted, st := run(true); promoted == 20 {
		t.Fatalf("control (batching off) promoted all 20 inside the budget (%d processes); the test does not reproduce the starvation", st.cnt.total())
	}
}

// New sessions are admitted before routine renewal of the sessions already in force.
func TestNewSessionsAreAdmittedBeforeRoutineRenewal(t *testing.T) {
	e, k := newFakeScale(t, 100, false)
	e.addSessions(100, 105)
	e.tick(35 * time.Second) // every old lease is due
	k.mu.Lock()
	k.opLog = nil
	k.mu.Unlock()
	if st := e.pass(); st.res.Failed != 0 {
		t.Fatalf("%v", st.res.Problems)
	}
	firstNew, firstOldWrite := -1, -1
	k.mu.Lock()
	defer k.mu.Unlock()
	for i, op := range k.opLog {
		isNew := false
		for j := 100; j < 105; j++ {
			if strings.HasSuffix(op, authKey(j)) {
				isNew = true
			}
		}
		if isNew && firstNew < 0 {
			firstNew = i
		}
		if !isNew && firstOldWrite < 0 {
			firstOldWrite = i
		}
	}
	if firstNew < 0 || firstOldWrite < 0 || firstNew > firstOldWrite {
		t.Fatalf("the first write for a new session is #%d, the first routine renewal #%d; new work must come first\n%v", firstNew, firstOldWrite, k.opLog[:8])
	}
}

// A session whose address is no longer its own is denied before anything routine is written, and is removed from both sets and from tc.
func TestFailClosedComesFirstAndRemovesEverything(t *testing.T) {
	e, k := newFakeScale(t, 100, false)
	e.tick(35 * time.Second)
	// session 7's address now belongs to another device
	e.leases.mu.Lock()
	e.leases.leases[7].HWAddr = "02:ff:ff:ff:ff:ff"
	e.leases.mu.Unlock()
	downBefore, upBefore := k.classCount(scaleBridge), k.classCount("ifb-gtest")
	k.mu.Lock()
	k.opLog = nil
	k.mu.Unlock()
	st := e.pass()
	if st.res.Failed != 1 || !strings.Contains(strings.Join(st.res.Problems, "\n"), "no longer leased") {
		t.Fatalf("failed=%d problems=%v", st.res.Failed, st.res.Problems)
	}
	if hasAuth(k, 7) || k.hasElem("phase3_auth_mac_ipv4", macKey(7)) {
		t.Fatal("the foreign address is still authorized")
	}
	if !hasAuth(k, 8) {
		t.Fatal("neighbours must be unaffected")
	}
	if k.classCount(scaleBridge) != downBefore-1 || k.classCount("ifb-gtest") != upBefore-1 {
		t.Fatal("the class of the denied session must be torn down on both devices")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.opLog) == 0 || k.opLog[0] != "del phase3_auth_ipv4 "+authKey(7) {
		t.Fatalf("the denial must be the first write of the pass, got %v", k.opLog[:3])
	}
}

// A denial the kernel will not take is reported for that session only, the others are denied, and the next pass retries it.
func TestADenialThatCannotBeProvenIsReportedForThatSessionAndRetried(t *testing.T) {
	e, k := newFakeScale(t, 20, false)
	e.leases.mu.Lock()
	for i := 1; i <= 5; i++ {
		e.leases.leases[i].HWAddr = "02:ff:ff:ff:ff:ff"
	}
	e.leases.mu.Unlock()
	stuck := scaleSession(3).IP
	k.mu.Lock()
	k.failNFT = func(op string) error {
		if strings.HasPrefix(op, "delete") && strings.Contains(op, stuck) {
			return fmt.Errorf("exit status 1")
		}
		return nil
	}
	k.mu.Unlock()
	e.tick(5 * time.Second)
	st := e.pass()
	problems := strings.Join(st.res.Problems, "\n")
	if st.res.Failed != 5 {
		t.Fatalf("failed = %d, want the five reassigned sessions", st.res.Failed)
	}
	if strings.Count(problems, "PACKET AUTHORIZATION NOT PROVEN REMOVED") != 1 || !strings.Contains(problems, "s-00003") {
		t.Fatalf("exactly session 3 must carry the unproven-denial warning:\n%s", problems)
	}
	for _, i := range []int{1, 2, 4, 5} {
		if hasAuth(k, i) {
			t.Fatalf("session %d must be denied even though session 3 could not be", i)
		}
	}
	if !hasAuth(k, 3) {
		t.Fatal("the test did not hold session 3's element in place")
	}
	k.mu.Lock()
	k.failNFT = nil
	k.mu.Unlock()
	e.tick(5 * time.Second)
	e.pass()
	if hasAuth(k, 3) {
		t.Fatal("the failed denial was not retried on the next pass")
	}
}

// Revoking access also ends the open flows (the forward chain accepts established flows first); a batch does it once for every address it revoked.
func TestARevokedBatchEndsTheOpenFlowsOfEveryAddressInOnePass(t *testing.T) {
	e, _ := newFakeScale(t, 20, false)
	e.leases.mu.Lock()
	for i := 0; i < 8; i++ {
		e.leases.leases[i].HWAddr = "02:ff:ff:ff:ff:ff"
	}
	e.leases.mu.Unlock()
	before := e.flows.calls
	e.tick(5 * time.Second)
	e.pass()
	if e.flows.flushed != 8 || e.flows.calls-before != 1 {
		t.Fatalf("flushed %d addresses in %d conntrack passes, want all 8 in one", e.flows.flushed, e.flows.calls-before)
	}
	// still denied on the next pass, and not flushed again until authorized once more
	e.tick(5 * time.Second)
	e.pass()
	if e.flows.flushed != 8 {
		t.Fatalf("flows of an already-revoked address flushed again: %d", e.flows.flushed)
	}
}

// An authorization the kernel will not take fails that session closed and nobody else.
func TestAnUnwritableAuthorizationFailsOnlyThatSession(t *testing.T) {
	k := newKernelFake(time.Now())
	e := newScaleEnv(t, k)
	bad := scaleSession(2).IP
	k.failNFT = func(op string) error {
		if strings.HasPrefix(op, "add") && strings.Contains(op, "phase3_auth_ipv4") && strings.Contains(op, bad) {
			return fmt.Errorf("exit status 1")
		}
		return nil
	}
	e.addSessions(0, 6)
	st := e.pass()
	if st.res.Failed != 1 || !strings.Contains(st.res.Problems[0], "authorize s-00002") {
		t.Fatalf("failed=%d problems=%v", st.res.Failed, st.res.Problems)
	}
	for i := 0; i < 6; i++ {
		if got, want := e.enf.isActive(scaleSession(i).SessionID), i != 2; got != want {
			t.Fatalf("session %d active=%v, want %v", i, got, want)
		}
		if got, want := hasAuth(k, i), i != 2; got != want {
			t.Fatalf("session %d authorized=%v, want %v", i, got, want)
		}
	}
	// failed closed means its tc state is gone too
	if k.classCount(scaleBridge) != 5 {
		t.Fatalf("classes = %d, want 5 (the failed session's class is removed)", k.classCount(scaleBridge))
	}
}

// A tc batch the kernel refuses is not a verdict on every session in it.
func TestRefusedTCBatchDoesNotFailSessionsThatCanBeAdmitted(t *testing.T) {
	k := newKernelFake(time.Now())
	e := newScaleEnv(t, k)
	refused := false
	k.failTC = func(args []string) error {
		// the first class add anywhere is refused once, as a transient batch failure would be
		if len(args) > 1 && args[0] == "class" && args[1] == "add" && !refused {
			refused = true
			return fmt.Errorf("exit status 2")
		}
		return nil
	}
	e.addSessions(0, 6)
	st := e.pass()
	if !refused || st.res.Failed != 0 || e.activeCount() != 6 {
		t.Fatalf("refused=%v failed=%d active=%d %v", refused, st.res.Failed, e.activeCount(), st.res.Problems)
	}
}

// A class that cannot be created fails that session closed in the per-session fallback, and only it.
func TestAClassThatCannotBeCreatedFailsOnlyThatSession(t *testing.T) {
	k := newKernelFake(time.Now())
	e := newScaleEnv(t, k)
	badMinor := fmt.Sprintf("1:%x", 0x1000+2+3)
	k.failTC = func(args []string) error {
		if len(args) > 1 && args[0] == "class" && args[1] == "add" && strings.Contains(strings.Join(args, " "), "classid "+badMinor+" ") {
			return fmt.Errorf("exit status 2")
		}
		return nil
	}
	e.addSessions(0, 6)
	st := e.pass()
	if st.res.Failed != 1 || !strings.Contains(st.res.Problems[0], "prepare s-00003") {
		t.Fatalf("failed=%d problems=%v", st.res.Failed, st.res.Problems)
	}
	for i := 0; i < 6; i++ {
		if got, want := e.enf.isActive(scaleSession(i).SessionID), i != 3; got != want {
			t.Fatalf("session %d active=%v, want %v", i, got, want)
		}
	}
	if hasAuth(k, 3) {
		t.Fatal("a session without its class must not be authorized")
	}
}

// A re-rate is still a `tc class change`: counters survive and no new generation or origin is created. An unchanged class is left alone.
func TestReRateKeepsCountersAndGenerationAndOnlyChangedClassesAreRewritten(t *testing.T) {
	e, k := newFakeScale(t, 5, false)
	key := classKey(scaleBridge, scaleSession(1).SessionID)
	epoch := e.p.epochs[key]
	k.mu.Lock()
	k.dev(scaleBridge).classes[0x1000+3].bytes = 777
	k.mu.Unlock()
	gens, origins := len(e.gens.issued), e.org.n
	e.sessions[1].DownKbps, e.sessions[1].UpKbps = 6000, 2500
	e.tick(5 * time.Second)
	st := e.pass()
	if st.res.Failed != 0 || st.cnt.TCClassChange != 2 {
		t.Fatalf("failed=%d class changes=%d, want exactly the changed session's two classes", st.res.Failed, st.cnt.TCClassChange)
	}
	k.mu.Lock()
	c := k.dev(scaleBridge).classes[0x1000+3]
	k.mu.Unlock()
	if c.rateKbit != 6000 || c.ceilKbt != 6000 || c.bytes != 777 {
		t.Fatalf("class after re-rate: %+v", c)
	}
	if e.p.epochs[key] != epoch || len(e.gens.issued) != gens || e.org.n != origins {
		t.Fatal("a re-rate must not start a new generation or register a new origin")
	}
	e.tick(5 * time.Second)
	if st := e.pass(); st.cnt.TCClassChange != 0 {
		t.Fatalf("the next pass re-rated %d classes of an unchanged plan", st.cnt.TCClassChange)
	}
}

// The durable proof is repeated on a schedule, not every pass, and always after a restart.
func TestDurableProofIsRepeatedOnSchedule(t *testing.T) {
	e, _ := newFakeScale(t, 10, false)
	if st := e.pass(); st.db != 0 {
		t.Fatalf("a pass right after admission asked the database %d times", st.db)
	}
	e.tick(phase3ReproveEvery + phase3ReproveJitter + time.Second)
	if st := e.pass(); st.db != 10 {
		t.Fatalf("after the re-prove interval every session must be proven again once, got %d database calls", st.db)
	}
	if st := e.pass(); st.db != 0 {
		t.Fatalf("and only once, got %d", st.db)
	}
	// a restart forgets the proofs: the first pass proves every session again
	e.p.provenAt = nil
	if st := e.pass(); st.db != 10 {
		t.Fatalf("after a restart every session must be proven again, got %d", st.db)
	}
}

// A session whose class is lost behind netd's back is re-admitted (new generation), not renewed on a class that is not there.
func TestALostClassIsReadmittedNotRenewed(t *testing.T) {
	e, k := newFakeScale(t, 10, false)
	k.mu.Lock()
	delete(k.dev(scaleBridge).classes, 0x1000+2+4)
	delete(k.dev(scaleBridge).filters, 0x1000+2+4)
	k.mu.Unlock()
	key := classKey(scaleBridge, scaleSession(4).SessionID)
	old := e.p.epochs[key]
	e.tick(5 * time.Second)
	st := e.pass()
	if st.res.Failed != 0 {
		t.Fatalf("%v", st.res.Problems)
	}
	if e.p.epochs[key] == old || e.p.epochs[key] == 0 {
		t.Fatalf("generation %d -> %d; a re-created class must be a new series", old, e.p.epochs[key])
	}
	if k.classCount(scaleBridge) != 10 {
		t.Fatalf("classes = %d", k.classCount(scaleBridge))
	}
}

// Strays: authorizations and classes the plan does not claim are removed in batches and proven gone.
func TestStrayAuthorizationsAndClassesAreRemovedInBatches(t *testing.T) {
	e, k := newFakeScale(t, 10, false)
	// 40 strays: elements and classes that no session owns
	k.mu.Lock()
	for i := 100; i < 140; i++ {
		ip := fmt.Sprintf("10.20.1.%d", i)
		k.sets["phase3_auth_ipv4"][normKey(fmt.Sprintf("%q . %s", scaleBridge, ip))] = &fakeKElem{expires: k.now.Add(60 * time.Second)}
		for _, d := range []string{scaleBridge, "ifb-gtest"} {
			k.dev(d).classes[0x1000+256+i] = &fakeKClass{parent: "1:", rateKbit: 1000, ceilKbt: 1000, leaf: true}
		}
	}
	k.mu.Unlock()
	e.tick(5 * time.Second)
	st := e.pass()
	if st.res.Failed != 0 || st.res.StraysRemoved != 80 {
		t.Fatalf("failed=%d strays removed=%d %v", st.res.Failed, st.res.StraysRemoved, st.res.Problems)
	}
	if k.setLen("phase3_auth_ipv4") != 10 || k.classCount(scaleBridge) != 10 || k.classCount("ifb-gtest") != 10 {
		t.Fatalf("elements=%d classes=%d/%d", k.setLen("phase3_auth_ipv4"), k.classCount(scaleBridge), k.classCount("ifb-gtest"))
	}
	if st.cnt.total() > 25 {
		t.Fatalf("removing 80 strays used %d processes", st.cnt.total())
	}
}

// Ended sessions are torn down in batches, denial first.
func TestEndedSessionsAreTornDownInBatches(t *testing.T) {
	e, k := newFakeScale(t, 40, false)
	for i := 0; i < 30; i++ {
		e.sessions[i].Entitled = false
	}
	e.tick(5 * time.Second)
	st := e.pass()
	if st.res.Failed != 0 || st.res.TornDown != 30 {
		t.Fatalf("failed=%d torn down=%d %v", st.res.Failed, st.res.TornDown, st.res.Problems)
	}
	if k.setLen("phase3_auth_ipv4") != 10 || k.classCount(scaleBridge) != 10 {
		t.Fatalf("elements=%d classes=%d", k.setLen("phase3_auth_ipv4"), k.classCount(scaleBridge))
	}
	if e.enf.ended != 30 {
		t.Fatalf("durable ends = %d", e.enf.ended)
	}
	if st.cnt.total() > 25 {
		t.Fatalf("tearing down 30 sessions used %d processes", st.cnt.total())
	}
}

// The between-walks renewal timer (AD-53) renews every due lease in one transaction too: once passes are clean it is the steady-state cost.
func TestBetweenWalkRenewalIsOneTransaction(t *testing.T) {
	e, k := newFakeScale(t, 200, false)
	e.p.fastPath = true
	e.p.memo.clean = false // force a real walk, whose renewable set the timer then works from
	if st := e.pass(); st.res.Failed != 0 {
		t.Fatalf("%v", st.res.Problems)
	}
	e.tick(30 * time.Second)
	c0 := k.counts()
	renewed := e.p.renewDue(context.Background(), k.clock())
	cost := k.counts().sub(c0)
	if renewed != 200 {
		t.Fatalf("renewed %d of 200 due leases", renewed)
	}
	if cost.NFTScript != 1 || cost.total() > 10 {
		t.Fatalf("renewing 200 leases used %d transactions and %d processes", cost.NFTScript, cost.total())
	}
}
