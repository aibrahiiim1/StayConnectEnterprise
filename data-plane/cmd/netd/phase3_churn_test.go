package main

// Sustained churn against the simulated kernel and a database with latency. A fixed population of long sessions keeps forwarding while a set of
// clients cycles 60-second sessions: every tick some sessions are created and some end, the ended ones stay listed in the plan for a while, and a
// client keeps its address, so each ended session is "handed over" to its successor. The database answers after a latency that is added to the
// simulated clock (the kernel's element expiry and the boot clock move with it), so a pass that waits on the database lets leases run down exactly
// as it would on the appliance.
//
//	NETD_CHURN_PRINT=1 go test ./cmd/netd -run TestChurn -v   prints one CHURN line per scenario

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

type churnSess struct {
	slot    int
	id      string
	started time.Time
	endsAt  time.Time
	sess    shapeplan.Session
}

type churnReport struct {
	passes           int
	lapsedBefore     int // long sessions whose authorization was missing when a pass began
	lapsedAfter      int
	maxPassSim       time.Duration
	passSim          []time.Duration
	arrivals         int
	notPromoted      int
	promoteSim       []time.Duration
	endedSessions    int
	endedWrites      int
	isEndedReads     int
	failedTotal      int
	endedMidPass     int
	firstProblem     string
	dbCalls          int
	maxPlanSessions  int
	tornDownReported int
}

type churnSim struct {
	t       *testing.T
	e       *scaleEnv
	k       *kernelFake
	longs   int
	clients int
	tick    time.Duration
	window  time.Duration
	lat     map[string]time.Duration

	mu      sync.Mutex
	live    map[int]*churnSess
	ended   []endedRec
	seq     int
	dbCalls int
	armed   bool
	stuck   time.Duration
}

type endedRec struct {
	s  shapeplan.Session
	at time.Time
}

func newChurnSim(t *testing.T, noBatch bool, longs, clients int) *churnSim {
	k := newKernelFake(time.Now())
	e := newScaleEnv(t, k)
	e.p.noBatch = noBatch
	c := &churnSim{t: t, e: e, k: k, longs: longs, clients: clients, tick: 5 * time.Second, window: 10 * time.Minute,
		live: map[int]*churnSess{}, stuck: 100 * time.Second,
		// Latencies of the database under the lab's load, per call kind.
		lat: map[string]time.Duration{"activate": 40 * time.Millisecond, "ended": 40 * time.Millisecond, "origin": 40 * time.Millisecond,
			"alloc": 40 * time.Millisecond, "confirm": 5 * time.Millisecond, "is_ended": 5 * time.Millisecond}}
	hook := func(kind string) {
		c.mu.Lock()
		c.dbCalls++
		d := c.lat[kind]
		if kind == "alloc" && c.armed {
			// one call that waits on a lock held elsewhere: the lab saw a class-generation allocation take 106 s
			c.armed = false
			d = c.stuck
		}
		if limit := dbCallCap(); limit > 0 && d > limit {
			// the appliance bounds every database call of the pass (timedEnforcement and friends); the simulated wait ends there too
			d = limit
		}
		c.mu.Unlock()
		e.tick(d)
	}
	e.enf.call = hook
	e.org.call = hook
	e.p.generations = slowGenerations{generationAllocator: e.gens, call: hook}
	e.addSessions(0, longs+clients) // the leases of every address that will be used
	e.sessions = nil
	return c
}

func (c *churnSim) longSession(i int) shapeplan.Session { return scaleSession(i) }

func (c *churnSim) clientSession(slot int, id string, ends time.Time) shapeplan.Session {
	s := scaleSession(c.longs + slot)
	s.SessionID = id
	ea := ends
	s.AccessEndsAt = &ea
	return s
}

// step runs one submission and returns what happened.
func (c *churnSim) step(r *churnReport, firstPhase bool) {
	e, k := c.e, c.k
	now := k.clock()
	// The expiry sweep: sessions whose window has run out are ended in durable state; their clients sign in again at once.
	var arrivals []string
	for slot := 0; slot < c.clients; slot++ {
		cur := c.live[slot]
		if cur != nil && !cur.endsAt.After(now) {
			e.enf.markEndedInDB(cur.id)
			s := cur.sess
			s.Entitled = false
			s.DownKbps, s.UpKbps = 0, 0
			c.ended = append(c.ended, endedRec{s: s, at: now})
			r.endedSessions++
			c.live[slot] = nil
			cur = nil
		}
		if cur == nil {
			// first arrivals are spread over one window so the population is in steady state from the start
			if firstPhase {
				if slot*int(c.tick/time.Second) >= int(c.window/time.Second) && false {
					continue
				}
			}
			c.seq++
			id := fmt.Sprintf("c-%03d-%05d", slot, c.seq)
			ends := now.Add(60*time.Second + time.Duration((slot*3)%5)*time.Second)
			if firstPhase {
				ends = now.Add(time.Duration(5+(slot*7)%55) * time.Second)
			}
			sess := c.clientSession(slot, id, ends)
			sess.Entitled = true
			sess.DownKbps, sess.UpKbps = 5000, 2000
			c.live[slot] = &churnSess{slot: slot, id: id, started: now, endsAt: ends, sess: sess}
			arrivals = append(arrivals, id)
			r.arrivals++
		}
	}
	// the plan: long sessions, live short ones, and the recently ended ones (kept for the listing window)
	var plan []shapeplan.Session
	for i := 0; i < c.longs; i++ {
		s := c.longSession(i)
		plan = append(plan, s)
	}
	slots := make([]int, 0, len(c.live))
	for slot := range c.live {
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	for _, slot := range slots {
		if cs := c.live[slot]; cs != nil {
			plan = append(plan, cs.sess)
		}
	}
	kept := c.ended[:0]
	for _, er := range c.ended {
		if now.Sub(er.at) < c.window {
			kept = append(kept, er)
			plan = append(plan, er.s)
		}
	}
	c.ended = kept
	if len(plan) > r.maxPlanSessions {
		r.maxPlanSessions = len(plan)
	}
	// Sessions about to run out end in durable state while this plan is in flight.
	for _, cs := range c.live {
		if cs != nil && cs.endsAt.After(now) && cs.endsAt.Sub(now) <= 3*time.Second {
			e.enf.markEndedInDB(cs.id)
		}
	}
	e.sessions = plan

	// authorization of the long sessions must not have lapsed since the previous pass
	for i := 0; i < c.longs; i++ {
		if !hasAuth(k, i) && e.enf.isActive(c.longSession(i).SessionID) {
			r.lapsedBefore++
		}
	}
	t0 := k.clock()
	st := e.pass()
	dur := k.clock().Sub(t0)
	r.passes++
	r.passSim = append(r.passSim, dur)
	if dur > r.maxPassSim {
		r.maxPassSim = dur
	}
	r.failedTotal += st.res.Failed
	r.endedMidPass += endedMidPassOf(st.res)
	r.tornDownReported += st.res.TornDown
	if st.res.Failed > 0 && r.firstProblem == "" && len(st.res.Problems) > 0 {
		r.firstProblem = st.res.Problems[0]
	}
	for i := 0; i < c.longs; i++ {
		if !hasAuth(k, i) && e.enf.isActive(c.longSession(i).SessionID) {
			r.lapsedAfter++
		}
	}
	for _, id := range arrivals {
		if e.enf.isActive(id) {
			r.promoteSim = append(r.promoteSim, k.clock().Sub(now))
		} else if !e.enf.endedIDsHas(id) {
			r.notPromoted++
		}
	}
	// the rest of the tick passes
	if rest := c.tick - dur; rest > 0 {
		e.tick(rest)
	}
}

func (e *countingEnforcement) endedIDsHas(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.endedIDs[id]
}

func (c *churnSim) run(passes int) churnReport {
	var r churnReport
	// the long sessions come up first
	c.e.sessions = nil
	for i := 0; i < c.longs; i++ {
		c.e.sessions = append(c.e.sessions, c.longSession(i))
	}
	for n := 0; n < 20 && c.e.activeCount() < c.longs; n++ {
		c.e.pass()
		c.e.tick(5 * time.Second)
	}
	if got := c.e.activeCount(); got != c.longs {
		c.t.Fatalf("only %d of %d long sessions came up", got, c.longs)
	}
	c.step(&churnReport{}, true) // staggered start; not measured
	c.mu.Lock()
	c.dbCalls = 0
	c.mu.Unlock()
	w0, i0 := c.e.enf.ended, c.e.enf.isEndedCalls
	for n := 0; n < passes; n++ {
		if n == passes/3 {
			c.mu.Lock()
			c.armed = true
			c.mu.Unlock()
		}
		c.step(&r, false)
	}
	c.e.enf.mu.Lock()
	r.endedWrites, r.isEndedReads = c.e.enf.ended-w0, c.e.enf.isEndedCalls-i0
	c.e.enf.mu.Unlock()
	c.mu.Lock()
	r.dbCalls = c.dbCalls
	c.mu.Unlock()
	return r
}

func (r churnReport) p95Sim() time.Duration {
	if len(r.passSim) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), r.passSim...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)*95/100]
}

func (r churnReport) promoteMax() time.Duration {
	var m time.Duration
	for _, d := range r.promoteSim {
		if d > m {
			m = d
		}
	}
	return m
}

func (r churnReport) line(name string, longs, clients int) string {
	return fmt.Sprintf("CHURN scenario=%s longs=%d clients=%d passes=%d arrivals=%d not_promoted=%d promote_max_s=%.1f ended=%d ended_writes=%d is_ended_reads=%d db_calls=%d "+
		"lapsed_before_pass=%d lapsed_after_pass=%d pass_sim_p95_s=%.1f pass_sim_max_s=%.1f failed=%d ended_mid_pass=%d plan_max=%d first_problem=%q",
		name, longs, clients, r.passes, r.arrivals, r.notPromoted, r.promoteMax().Seconds(), r.endedSessions, r.endedWrites, r.isEndedReads, r.dbCalls,
		r.lapsedBefore, r.lapsedAfter, r.p95Sim().Seconds(), r.maxPassSim.Seconds(), r.failedTotal, r.endedMidPass, r.maxPlanSessions, r.firstProblem)
}

func churnSizes() (longs, clients, passes int) {
	longs, clients, passes = 300, 100, 60
	if os.Getenv("NETD_CHURN_FULL") == "1" {
		longs, clients, passes = 1000, 300, 120
	}
	return
}

func printChurn(t *testing.T, line string) {
	t.Log(line)
	if os.Getenv("NETD_CHURN_PRINT") == "1" {
		fmt.Println(line)
	}
}

// Sustained churn keeps the long sessions forwarding, promotes every new session in the pass it arrives in, and records an ended session once.
func TestChurnKeepsLongSessionsForwardingAndEndsEachSessionOnce(t *testing.T) {
	longs, clients, passes := churnSizes()
	c := newChurnSim(t, false, longs, clients)
	r := c.run(passes)
	printChurn(t, r.line("batched", longs, clients))
	if r.lapsedBefore != 0 || r.lapsedAfter != 0 {
		t.Fatalf("authorization of a long session lapsed: %d before a pass, %d after", r.lapsedBefore, r.lapsedAfter)
	}
	if r.notPromoted != 0 || r.arrivals == 0 {
		t.Fatalf("%d of %d new sessions were not promoted in the pass they arrived in", r.notPromoted, r.arrivals)
	}
	if r.promoteMax() > 10*time.Second {
		t.Fatalf("a new session waited %s of simulated time for promotion", r.promoteMax())
	}
	if r.failedTotal != 0 {
		t.Fatalf("the churn produced %d failures; first: %s", r.failedTotal, r.firstProblem)
	}
	if r.endedWrites != 0 {
		t.Fatalf("%d locking end writes for sessions durable state had already ended", r.endedWrites)
	}
	if r.isEndedReads > r.endedSessions+r.arrivals {
		t.Fatalf("%d ended-state reads for %d ended sessions: each must be looked at once, not every pass", r.isEndedReads, r.endedSessions)
	}
}

// The control: the same churn on the previous pass (batching off) loses leases or stalls; the test fails if the harness cannot tell the two apart.
func TestChurnHarnessSeparatesTheOldPassFromTheNew(t *testing.T) {
	longs, clients, passes := 120, 40, 30
	old := newChurnSim(t, true, longs, clients).run(passes)
	printChurn(t, old.line("per-session", longs, clients))
	cur := newChurnSim(t, false, longs, clients).run(passes)
	if old.dbCalls < 3*cur.dbCalls {
		t.Fatalf("the per-session pass made %d database calls against %d: the harness does not show the difference", old.dbCalls, cur.dbCalls)
	}
	if old.maxPassSim <= cur.maxPassSim {
		t.Fatalf("the per-session pass was not slower (%s against %s)", old.maxPassSim, cur.maxPassSim)
	}
}

// A plan that waited for the lock while a newer one arrived is not walked; the newer one is. The previous behaviour walked each queued plan in turn,
// every one starting with its two-minute budget already spent.
func TestAPlanOvertakenWhileWaitingIsSkipped(t *testing.T) {
	e, _ := newFakeScale(t, 5, false)
	e.p.noteArrival(100)
	env := envelopeOn([]string{scaleBridge}, 50, e.sessions...)
	res, err := e.p.submit(context.Background(), env, time.Time{})
	if err == nil || res.Reason != reasonSuperseded || res.Accepted {
		t.Fatalf("an overtaken plan must be answered as superseded, got %+v %v", res, err)
	}
	if e.p.rejections != 0 {
		t.Fatal("being overtaken is not a refusal of the plan's content and must not count as one")
	}
	// the newest plan is walked
	e.p.noteArrival(200)
	env = envelopeOn([]string{scaleBridge}, 200, e.sessions...)
	res, err = e.p.submit(context.Background(), env, time.Time{})
	if err != nil || !res.Accepted {
		t.Fatalf("the newest plan must be walked: %+v %v", res, err)
	}
}

// A database call that never answers costs that session its turn, not the pass: leases are renewed, the other sessions admitted.
func TestADatabaseCallThatNeverAnswersDelaysNothingElse(t *testing.T) {
	old := phase3DBCallTimeout
	phase3DBCallTimeout = 150 * time.Millisecond
	defer func() { phase3DBCallTimeout = old }()
	e, k := newFakeScale(t, 40, false)
	hang := scaleSession(41).SessionID
	e.p.enforcement = timedEnforcement{&hangingEnforcement{countingEnforcement: e.enf, hang: hang}}
	e.addSessions(40, 43) // sessions 40, 41, 42: 41 is the one whose promotion never answers
	e.tick(35 * time.Second)
	t0 := time.Now()
	st := e.pass()
	if took := time.Since(t0); took > 3*time.Second {
		t.Fatalf("the pass took %s: one unanswered call held it", took)
	}
	if !e.enf.isActive(scaleSession(40).SessionID) || !e.enf.isActive(scaleSession(42).SessionID) {
		t.Fatal("the sessions around the stuck one were not admitted")
	}
	for i := 0; i < 40; i++ {
		if !hasAuth(k, i) {
			t.Fatalf("long session %d lost its authorization behind a call that did not answer", i)
		}
	}
	if st.res.Failed == 0 {
		t.Fatal("the stuck session must be reported, not silently dropped")
	}
	if hasAuth(k, 41) && e.enf.isActive(hang) {
		t.Fatal("a session whose promotion never answered must not be promoted")
	}
}

// hangingEnforcement never answers a promotion for one session until its context ends.
type hangingEnforcement struct {
	*countingEnforcement
	hang string
}

func (h *hangingEnforcement) Activate(ctx context.Context, sessionID, bridge string, minor int, epoch int64) (string, error) {
	if sessionID == h.hang {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return h.countingEnforcement.Activate(ctx, sessionID, bridge, minor, epoch)
}

// A session that durable state has ended while it was being admitted, proved or registered is withdrawn once and quietly: no failure, no grace, no
// quarantine, and nothing is retried.
func TestASessionEndedInDurableStateIsWithdrawnQuietlyAndOnce(t *testing.T) {
	e, k := newFakeScale(t, 10, false)
	// (a) ended before its origin is registered
	e.addSessions(10, 12)
	e.enf.markEndedInDB(scaleSession(10).SessionID)
	// (b) ended between origin and promotion: the origin is accepted, the promotion is refused as ended
	e.enf.mu.Lock()
	e.enf.endOnActivate = map[string]bool{scaleSession(11).SessionID: true}
	e.enf.mu.Unlock()
	e.tick(5 * time.Second)
	st := e.pass()
	if st.res.Failed != 0 || endedMidPassOf(st.res) != 2 {
		t.Fatalf("failed=%d ended_mid_pass=%d problems=%v", st.res.Failed, endedMidPassOf(st.res), st.res.Problems)
	}
	for _, i := range []int{10, 11} {
		key := classKey(scaleBridge, scaleSession(i).SessionID)
		if hasAuth(k, i) || e.p.quarantined[key] != nil || e.p.pending[key] != 0 {
			t.Fatalf("session %d: authorized=%v quarantine=%v pending=%d; an ended session leaves nothing behind", i, hasAuth(k, i), e.p.quarantined[key], e.p.pending[key])
		}
	}
	// (c) the next passes do not try again while the plan still lists them as entitled
	before := e.enf.db() + e.org.n
	e.tick(5 * time.Second)
	e.pass()
	if got := e.enf.db() + e.org.n; got > before+2 {
		t.Fatalf("an ended session was asked about again: %d database calls on the next pass", got-before)
	}
}

// Ended sessions are recorded once, however long the plan lists them, even when the client's next session has taken their address.
func TestAHandedOverSessionIsRecordedAsEndedOnceNotEveryPass(t *testing.T) {
	for _, noBatch := range []bool{false, true} {
		e, _ := newFakeScale(t, 6, noBatch)
		// session 0's address is taken by a newer session; the old one is listed as ended
		old := e.sessions[0]
		old.Entitled = false
		next := scaleSession(0)
		next.SessionID = "successor"
		e.sessions = append([]shapeplan.Session{old, next}, e.sessions[1:]...)
		e.leases.mu.Lock()
		e.leases.leases[0].HWAddr = next.MAC
		e.leases.mu.Unlock()
		for n := 0; n < 5; n++ {
			e.tick(5 * time.Second)
			e.pass()
		}
		if e.enf.ended > 1 {
			t.Fatalf("noBatch=%v: the handed-over session was recorded as ended %d times in 5 passes", noBatch, e.enf.ended)
		}
	}
}

// A pass that has run long renews the unchanged sessions a second time, judging their leases by what is left now: the renewal at its start leaves alone
// a lease with more than a minute left, which a long enough pass would otherwise let run out.
func TestALongPassRenewsTheLeasesItLeftAloneAtItsStart(t *testing.T) {
	e, k := newFakeScale(t, 30, false)
	e.tick(20 * time.Second) // 70 s left of 90: not due at the start of a pass
	e.p.mu.Lock()
	defer e.p.mu.Unlock()
	e.p.pb = newPassBatch()
	defer func() { e.p.pb, e.p.passStart = nil, time.Time{} }()
	view := e.p.snapshotGate(context.Background())
	var steady []batchCand
	for i := 0; i < 30; i++ {
		s := scaleSession(i)
		ip := net.ParseIP(s.IP)
		steady = append(steady, batchCand{minor: 0x1000 + 2 + i, s: s, pc: provCtx{ip: ip, key: classKey(scaleBridge, s.SessionID), lease: phase3LeaseTTL}})
	}
	var res shapingPlanResponse
	// a pass that has only just begun leaves them alone
	e.p.passStart = time.Now()
	e.p.renewAgain(context.Background(), scaleBridge, steady, view, &res)
	if k.counts().NFTScript != 0 && res.Failed != 0 {
		t.Fatal("renewed at the start of a pass")
	}
	c0 := k.counts()
	// 20 s into the pass: the 70 s they had are 50 s now, which is due
	e.p.passStart = time.Now().Add(-20 * time.Second)
	e.p.renewAgain(context.Background(), scaleBridge, steady, view, &res)
	if res.Failed != 0 {
		t.Fatalf("%v", res.Problems)
	}
	if got := k.counts().sub(c0); got.NFTScript != 1 {
		t.Fatalf("second renewal used %d transactions, want 1", got.NFTScript)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for key, el := range k.sets["phase3_auth_ipv4"] {
		if left := el.expires.Sub(k.now); left > 71*time.Second || left < 69*time.Second {
			t.Fatalf("%s has %s left; a lease renewed 20 s into a pass must be the pass's lease less those 20 s, never longer than before", key, left)
		}
	}
}

// A pass stops taking on new work after its soft budget; what it did not reach is not a failure and is first in line next time.
func TestAPassDefersWhatItDidNotReachWithoutFailingIt(t *testing.T) {
	old := phase3SoftBudget
	phase3SoftBudget = 20 * time.Millisecond
	defer func() { phase3SoftBudget = old }()
	e, k := newFakeScale(t, 20, false)
	e.addSessions(20, 160)
	k.mu.Lock()
	k.perExec = 3 * time.Millisecond
	k.mu.Unlock()
	e.tick(5 * time.Second)
	st := e.pass()
	got := e.activeCount() - 20
	if st.res.Failed != 0 {
		t.Fatalf("deferral reported as failure: %v", st.res.Problems)
	}
	if got == 0 || got >= 140 {
		t.Fatalf("promoted %d of 140 in the first pass; want the first chunk(s) but not all", got)
	}
	for n := 0; n < 20 && e.activeCount() < 160; n++ {
		e.tick(5 * time.Second)
		if st := e.pass(); st.res.Failed != 0 {
			t.Fatalf("pass %d: %v", n, st.res.Problems)
		}
	}
	if e.activeCount() != 160 {
		t.Fatalf("only %d of 160 active after repeated passes", e.activeCount())
	}
	for i := 0; i < 20; i++ {
		if !hasAuth(k, i) {
			t.Fatalf("long session %d lost its authorization while admissions were deferred", i)
		}
	}
}

var _ = strings.Contains
