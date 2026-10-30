package main

// Fault injection for outage continuity at package level: the real governor, the real plan admission path, a fake kernel
// (nft set with decaying leases, tc classes with settable counters), a settable monotonic clock and a settable database probe.
// "acctd gone" is modelled by simply not submitting plans; "database down" by a probe that errors.

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// meterTC is a fakeTC whose classes carry byte counters.
type meterTC struct {
	*fakeTC
	cmu     sync.Mutex
	bytes   map[string]uint64
	readErr error
}

func (m *meterTC) ReadClasses(ctx context.Context, device string) (map[int]shape.ClassBytes, error) {
	out, err := m.fakeTC.ReadClasses(ctx, device)
	if err != nil {
		return nil, err
	}
	m.cmu.Lock()
	defer m.cmu.Unlock()
	if m.readErr != nil {
		return nil, m.readErr
	}
	for minor := range out {
		out[minor] = shape.ClassBytes{Bytes: m.bytes[device+"|"+strconv.Itoa(minor)]}
	}
	return out, nil
}

// carry adds n bytes of traffic for the session at ip (download side).
func (m *meterTC) carry(bridge, ip string, n uint64) {
	minor, _ := shape.MinorForIP(netIP(ip))
	m.cmu.Lock()
	m.bytes[bridge+"|"+strconv.Itoa(minor)] += n
	m.cmu.Unlock()
}

type fakeProbe struct {
	mu  sync.Mutex
	out map[string]bool
	err error
	n   int
}

func (f *fakeProbe) NotEntitled(ctx context.Context, ids []string) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.err != nil {
		return nil, f.err
	}
	m := map[string]bool{}
	for _, id := range ids {
		if f.out[id] {
			m[id] = true
		}
	}
	return m, nil
}

type contH struct {
	t     *testing.T
	p     *phase3Shaping
	tc    *meterTC
	g     *fakeGate
	enf   *fakeEnforcement
	clk   *fixedSecurityClock
	probe *fakeProbe
	dir   string
	start time.Time
	el    time.Duration // elapsed since start
	gen   int64
	kea   *switchableLeases
}

func newContH(t *testing.T) *contH {
	t.Helper()
	tc := &meterTC{fakeTC: newFakeTC(), bytes: map[string]uint64{}}
	p := liveWriter(tc)
	g, enf := newFakeGate(), newFakeEnforcement()
	p.generations, p.origins, p.gate, p.enforcement, p.bootID = &fakeGenerations{}, &fakeOrigins{}, g, enf, "boot-sys"
	clk := p.secClock.(*fixedSecurityClock)
	dir := t.TempDir()
	h := &contH{t: t, p: p, tc: tc, g: g, enf: enf, clk: clk, probe: &fakeProbe{out: map[string]bool{}}, dir: dir,
		start: time.Now(), kea: &switchableLeases{}}
	p.owner = addressOwner{src: h.kea}
	p.cont = &continuity{statePath: filepath.Join(dir, "state.json"), journalPath: filepath.Join(dir, "journal.json"), probe: h.probe}
	return h
}

// guest is one session with a lease in the fake DHCP table.
func (h *contH) guest(id, ip, mac, ent string) shapeplan.Session {
	// A long lease: the simulated hours must not outrun the DHCP lease, which clients renew in real life.
	l := lease(ip, mac)
	l.ValidLft = 30 * 24 * 3600
	h.kea.rows = append(h.kea.rows, l)
	h.enf.setState(id, "active")
	return shapeplan.Session{SessionID: id, DeviceID: "dev-" + id, IP: ip, MAC: mac, Bridge: "br-guest",
		EntitlementID: ent, DownKbps: 8000, UpKbps: 3000, Entitled: true}
}

func (h *contH) now() time.Time { return h.start.Add(h.el) }

// submit delivers a healthy plan carrying a continuity block (nil block = strict).
func (h *contH) submit(blk *shapeplan.Continuity, sessions ...shapeplan.Session) {
	h.t.Helper()
	h.gen++
	env := planAt(h.gen, h.now(), sessions...)
	if blk != nil {
		b := *blk
		if b.AsOf.IsZero() {
			b.AsOf = h.now()
		}
		b.ContentsHash = shapeplan.HashContinuity(b)
		env.Continuity = &b
	}
	res, err := h.p.submit(context.Background(), env, h.now())
	if err != nil || !res.Accepted || res.Failed != 0 {
		h.t.Fatalf("plan %d: %+v %v", h.gen, res, err)
	}
}

// block builds a block for the sessions with optional hard ends and budgets.
func block(window time.Duration, sessions []shapeplan.Session, budgets map[string]int64, hard map[string]time.Time) *shapeplan.Continuity {
	c := &shapeplan.Continuity{WindowSeconds: int(window / time.Second)}
	for _, s := range sessions {
		cs := shapeplan.ContinuitySession{SessionID: s.SessionID}
		if t, ok := hard[s.SessionID]; ok {
			tt := t
			cs.HardEnd = &tt
		}
		c.Sessions = append(c.Sessions, cs)
	}
	for id, b := range budgets {
		c.Budgets = append(c.Budgets, shapeplan.ContinuityBudget{EntitlementID: id, RemainingBytes: b})
	}
	return c
}

// step advances every clock by d (monotonic, kernel leases, and the wall time handed to the governor) and runs one governor tick.
func (h *contH) step(d time.Duration) int {
	h.el += d
	h.clk.ms += d.Milliseconds()
	h.g.advance(d)
	return h.p.contTick(context.Background(), h.now())
}

// run steps in 5 s ticks for total, returning after each tick to the check.
func (h *contH) run(total time.Duration, each func(el time.Duration)) {
	for done := time.Duration(0); done < total; done += 5 * time.Second {
		h.step(5 * time.Second)
		if each != nil {
			each(h.el)
		}
	}
}

func (h *contH) authorized(ip string) bool { return h.g.isAuthorized("br-guest", ip) }

// healthy runs n seconds of normal operation: plans arrive every 5 s, the governor samples counters.
func (h *contH) healthy(secs int, blk func() *shapeplan.Continuity, sessions ...shapeplan.Session) {
	h.t.Helper()
	for i := 0; i < secs/5; i++ {
		h.el += 5 * time.Second
		h.clk.ms += 5000
		h.g.advance(5 * time.Second)
		h.gen++
		env := planAt(h.gen, h.now(), sessions...)
		if blk != nil {
			if b := blk(); b != nil {
				b.AsOf = h.now()
				b.ContentsHash = shapeplan.HashContinuity(*b)
				env.Continuity = b
			}
		}
		if res, err := h.p.submit(context.Background(), env, h.now()); err != nil || !res.Accepted {
			h.t.Fatalf("healthy plan: %+v %v", res, err)
		}
		h.p.contTick(context.Background(), h.now())
	}
}

// ---------------------------------------------------------------------------------------------------------------

// acctd gone: authorized for exactly the window, not a second more. The strict control lapses at 90 s.
func TestContinuity_AcctdGone_StaysAuthorizedUntilTheWindowAndNoLonger(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	window := 10 * time.Minute
	h.healthy(60, func() *shapeplan.Continuity { return block(window, []shapeplan.Session{a}, nil, nil) }, a)
	if !h.authorized(a.IP) {
		t.Fatal("precondition: the guest must be online")
	}
	lastPlanAt := h.el
	var lost time.Duration
	h.run(window+2*time.Minute, func(el time.Duration) {
		if lost == 0 && !h.authorized(a.IP) {
			lost = el
		}
	})
	if lost == 0 {
		t.Fatal("the guest was never cut off: continuity must end at the window")
	}
	// The deadline is window after the last accepted block; the lease TTL is truncated to whole seconds and a tick is 5 s.
	if lost < lastPlanAt+window-time.Second || lost > lastPlanAt+window+10*time.Second {
		t.Fatalf("cut off at +%v after the last plan, want about %v", lost-lastPlanAt, window)
	}
}

func TestContinuity_StrictModeIsTheNinetySecondLease(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	h.healthy(60, nil, a) // plans without a block: setting 0
	if !h.authorized(a.IP) {
		t.Fatal("precondition")
	}
	var lost time.Duration
	h.run(3*time.Minute, func(el time.Duration) {
		if lost == 0 && !h.authorized(a.IP) {
			lost = el
		}
	})
	if lost == 0 || lost > h.el-3*time.Minute+120*time.Second {
		t.Fatalf("strict mode must lapse with the lease (about 90 s), lost at %v", lost)
	}
	if h.p.cont.st != nil {
		t.Fatal("strict mode must hold no continuity record")
	}
	if _, err := os.Stat(h.p.cont.statePath); !os.IsNotExist(err) {
		t.Fatal("strict mode must leave no record on disk")
	}
	if h.p.cont.renewed != 0 {
		t.Fatalf("strict mode renewed %d leases", h.p.cont.renewed)
	}
}

func TestContinuity_WithdrawingTheBlockEndsContinuityAtOnce(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	h.healthy(30, func() *shapeplan.Continuity { return block(time.Hour, []shapeplan.Session{a}, nil, nil) }, a)
	if h.p.cont.st == nil {
		t.Fatal("precondition: a record")
	}
	h.el += 5 * time.Second
	h.clk.ms += 5000
	h.submit(shapeplan.OffContinuity(), a) // the operator set 0
	if h.p.cont.st != nil {
		t.Fatal("an explicit off must clear continuity immediately")
	}
	if _, err := os.Stat(h.p.cont.statePath); !os.IsNotExist(err) {
		t.Fatal("an explicit off must leave no record on disk")
	}
}

// A plan with no block (acctd just restarted and has not read its setting, or accounting is stalled) says nothing new: what is held is
// kept, its deadlines are NOT extended, and the plan restarts the outage clock. acctd flapping must not cost the guests their continuity.
func TestContinuity_ABlocklessPlanKeepsWhatIsHeldAndExtendsNothing(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	window := 10 * time.Minute
	h.healthy(60, func() *shapeplan.Continuity { return block(window, []shapeplan.Session{a}, nil, nil) }, a)
	deadline := h.p.cont.st.Sessions[classKey("br-guest", "c-1")].DeadlineBoot
	// acctd flaps: block-less plans every 5 s for two minutes, then it dies for good.
	h.healthy(120, nil, a)
	if h.p.cont.st == nil {
		t.Fatal("block-less plans must not discard the record")
	}
	if got := h.p.cont.st.Sessions[classKey("br-guest", "c-1")].DeadlineBoot; got != deadline {
		t.Fatalf("a block-less plan moved a deadline: %d -> %d", deadline, got)
	}
	var lost time.Duration
	h.run(window+time.Minute, func(el time.Duration) {
		if lost == 0 && !h.authorized(a.IP) {
			lost = el
		}
	})
	endAt := time.Duration(deadline-60_000) * time.Millisecond // boot ms 60000 == h.el 0
	if lost == 0 || lost > endAt+10*time.Second || lost < endAt-10*time.Second {
		t.Fatalf("lost at %v, the original deadline was %v", lost, endAt)
	}
}

func TestContinuity_HardEndEarlierThanTheWindowWins(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	end := h.now().Add(10 * time.Minute) // the entitlement ends in 10 minutes; the window is 60
	h.healthy(60, func() *shapeplan.Continuity {
		return block(time.Hour, []shapeplan.Session{a}, nil, map[string]time.Time{"c-1": end})
	}, a)
	var lost time.Duration
	h.run(20*time.Minute, func(el time.Duration) {
		if lost == 0 && !h.authorized(a.IP) {
			lost = el
		}
	})
	if lost == 0 {
		t.Fatal("never cut off")
	}
	// end is 10 min after the START of the healthy phase's first tick; allow a tick of slack both ways.
	if got := h.start.Add(lost); got.After(end.Add(10*time.Second)) || got.Before(end.Add(-10*time.Second)) {
		t.Fatalf("cut off at %v, the hard end was %v", got.Sub(h.start), end.Sub(h.start))
	}
}

// A data allowance spent during the outage ends every session of that entitlement and none of the others.
func TestContinuity_QuotaSpentDuringTheOutageRemovesOnlyThatEntitlement(t *testing.T) {
	h := newContH(t)
	a1 := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	a2 := h.guest("c-2", "10.0.0.12", "96:48:f9:7a:9b:12", "ent-a")
	b := h.guest("c-3", "10.0.0.13", "96:48:f9:7a:9b:13", "ent-b")
	ss := []shapeplan.Session{a1, a2, b}
	mk := func() *shapeplan.Continuity {
		return block(time.Hour, ss, map[string]int64{"ent-a": 10 << 20, "ent-b": 1 << 30}, nil)
	}
	h.healthy(60, mk, ss...)
	for _, ip := range []string{a1.IP, a2.IP, b.IP} {
		if !h.authorized(ip) {
			t.Fatalf("precondition: %s must be online (record: %+v)", ip, h.p.cont.st)
		}
	}
	// 30 s of silence, then the guests use data: 6 MiB each on ent-a (12 MiB > 10 MiB), 100 MiB on ent-b (< 1 GiB).
	h.run(65*time.Second, nil)
	if !h.authorized(a1.IP) || !h.authorized(a2.IP) {
		t.Fatal("ent-a must still be online before it uses its data")
	}
	h.tc.carry("br-guest", a1.IP, 6<<20)
	h.step(5 * time.Second)
	if !h.authorized(a1.IP) || !h.authorized(a2.IP) {
		t.Fatal("6 MiB of a 10 MiB allowance must not end anything")
	}
	h.tc.carry("br-guest", a2.IP, 6<<20)
	h.tc.carry("br-guest", b.IP, 100<<20)
	h.step(5 * time.Second)
	if h.authorized(a1.IP) || h.authorized(a2.IP) {
		t.Fatal("an allowance spent during the outage must end EVERY session of the entitlement at the next tick")
	}
	if !h.authorized(b.IP) {
		t.Fatal("another entitlement with allowance left must be unaffected")
	}
	h.run(2*time.Minute, nil)
	if h.authorized(a1.IP) || h.authorized(a2.IP) {
		t.Fatal("a spent session must never be renewed again")
	}
}

// Uncertainty never grants quota: no counter sample before the block's AsOf means no continuity for a budgeted session.
func TestContinuity_BudgetWithoutABaselineGivesNoContinuity(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	h.submit(block(time.Hour, []shapeplan.Session{a}, map[string]int64{"ent-a": 1 << 30}, nil), a)
	if got := len(h.p.cont.st.Sessions); got != 0 {
		t.Fatalf("a budgeted session with no counter baseline was given continuity (%d sessions)", got)
	}
}

func TestContinuity_UnreadableCountersMeanBudgetedSessionsAreNotRenewed(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	mk := func() *shapeplan.Continuity {
		return block(time.Hour, []shapeplan.Session{a}, map[string]int64{"ent-a": 1 << 30}, nil)
	}
	h.healthy(60, mk, a)
	if !h.authorized(a.IP) {
		t.Fatal("precondition")
	}
	h.tc.cmu.Lock()
	h.tc.readErr = errors.New("tc: netlink busy")
	h.tc.cmu.Unlock()
	var lost time.Duration
	h.run(4*time.Minute, func(el time.Duration) {
		if lost == 0 && !h.authorized(a.IP) {
			lost = el
		}
	})
	if lost == 0 {
		t.Fatal("a session that cannot be metered must run down to the strict lease, not be carried")
	}
}

// Revocation made while acctd is away reaches the kernel at once and survives a netd restart.
func TestContinuity_StaffDisconnectDuringTheOutageIsImmediateAndDurable(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	b := h.guest("c-2", "10.0.0.12", "96:48:f9:7a:9b:12", "ent-b")
	ss := []shapeplan.Session{a, b}
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, ss, nil, nil) }, ss...)
	h.run(2*time.Minute, nil)
	if !h.authorized(a.IP) || !h.authorized(b.IP) {
		t.Fatal("precondition: both online on continuity")
	}
	stale, err := os.ReadFile(h.p.cont.statePath) // what a restart would find if the save were older
	if err != nil {
		t.Fatal(err)
	}

	srv := &server{phase3: h.p}
	if _, err := srv.endFlows(context.Background(), netIP(a.IP), "", &nopFlusher{}); err != nil {
		t.Fatal(err)
	}
	if h.authorized(a.IP) {
		t.Fatal("a staff disconnect must remove the authorization before the call returns")
	}
	if !h.authorized(b.IP) {
		t.Fatal("the other guest must be unaffected")
	}
	h.run(30*time.Second, nil)
	if h.authorized(a.IP) {
		t.Fatal("a disconnected guest was brought back by the governor")
	}

	// A restart that finds an OLDER record (the save lagged) must still honour the journal.
	if err := os.WriteFile(h.p.cont.statePath, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	h.g.put("br-guest", a.IP) // and the set still holds the old element, as after a netd-only restart
	h.p.cont = &continuity{statePath: h.p.cont.statePath, journalPath: h.p.cont.journalPath, probe: h.probe}
	if why := h.p.contRestore(); why != "" {
		t.Fatalf("restore refused a valid record: %s", why)
	}
	h.step(5 * time.Second)
	if h.authorized(a.IP) {
		t.Fatal("a restart resurrected a revoked session")
	}
	if !h.authorized(b.IP) {
		t.Fatal("a restart with the database still away must keep the un-revoked guest within the window")
	}
}

// A revocation found in the database (checkout, suspension, operator end) is acted on within a tick of the probe answering.
func TestContinuity_DatabaseRevocationIsFoundByTheProbe(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	b := h.guest("c-2", "10.0.0.12", "96:48:f9:7a:9b:12", "ent-b")
	ss := []shapeplan.Session{a, b}
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, ss, nil, nil) }, ss...)
	h.run(90*time.Second, nil)
	h.probe.mu.Lock()
	h.probe.out["c-1"] = true
	h.probe.mu.Unlock()
	h.step(5 * time.Second)
	if h.authorized(a.IP) {
		t.Fatal("a session the database no longer lists as entitled must be removed at the next tick")
	}
	if !h.authorized(b.IP) {
		t.Fatal("others must be unaffected")
	}
}

// Database down: the probe cannot answer. The documented consequence: no revocation can be made either, so sessions run to the bound.
func TestContinuity_DatabaseDownKeepsSessionsToTheBoundAndReportsIt(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	h.healthy(60, func() *shapeplan.Continuity { return block(30*time.Minute, []shapeplan.Session{a}, nil, nil) }, a)
	h.probe.mu.Lock()
	h.probe.err = errors.New("connection refused")
	h.probe.mu.Unlock()
	h.run(25*time.Minute, func(el time.Duration) {
		if !h.authorized(a.IP) {
			t.Fatalf("lost the guest at +%v while the database was down and the window had not ended", el)
		}
	})
	st := h.p.contStatus()
	if st.ProbeOK || st.ProbeFails == 0 || !st.Active {
		t.Fatalf("status must say the governor is active and the probe failing: %+v", st)
	}
	h.run(8*time.Minute, nil)
	if h.authorized(a.IP) {
		t.Fatal("the bound must hold with the database down")
	}
}

// Never create an authorization that is absent.
func TestContinuity_NeverRecreatesAnAbsentAuthorization(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, []shapeplan.Session{a}, nil, nil) }, a)
	h.run(2*time.Minute, nil)
	_ = h.g.Revoke(context.Background(), "br-guest", netIP(a.IP)) // an administrator, or a reboot that emptied the set
	h.run(3*time.Minute, nil)
	if h.authorized(a.IP) {
		t.Fatal("the governor re-created an authorization somebody removed")
	}
}

func TestContinuity_AddressReassignedOrUnprovenIsNotRenewed(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	b := h.guest("c-2", "10.0.0.12", "96:48:f9:7a:9b:12", "ent-b")
	ss := []shapeplan.Session{a, b}
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, ss, nil, nil) }, ss...)
	h.run(90*time.Second, nil)
	// The address of a now belongs to another device; the DHCP table says nothing about b (evidence unavailable).
	h.kea.rows = []KeaLease{lease("10.0.0.11", "aa:aa:aa:aa:aa:aa")}
	h.step(5 * time.Second)
	if h.authorized(a.IP) {
		t.Fatal("an address now leased to another device was kept")
	}
	h.kea.err = errors.New("kea control socket: connection refused")
	h.run(3*time.Minute, nil)
	if h.authorized(b.IP) {
		t.Fatal("with no ownership evidence the lease must run down, not be extended")
	}
}

// Unmeasurable time extends nothing.
func TestContinuity_ABrokenMonotonicClockExtendsNothing(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, []shapeplan.Session{a}, nil, nil) }, a)
	h.clk.err = errors.New("no /proc/uptime")
	var lost time.Duration
	h.run(4*time.Minute, func(el time.Duration) {
		if lost == 0 && !h.authorized(a.IP) {
			lost = el
		}
	})
	if lost == 0 {
		t.Fatal("without a clock the lease must run down")
	}
}

// A wall clock that jumps (NTP step, operator) changes nothing: the window is monotonic.
func TestContinuity_WallClockJumpsDoNotStretchTheWindow(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	window := 10 * time.Minute
	h.healthy(60, func() *shapeplan.Continuity { return block(window, []shapeplan.Session{a}, nil, nil) }, a)
	planAt := h.el
	var lost time.Duration
	for h.el < planAt+window+2*time.Minute {
		h.el += 5 * time.Second
		h.clk.ms += 5000
		h.g.advance(5 * time.Second)
		// Wall time handed to the governor goes BACKWARDS by 6 hours each tick.
		h.p.contTick(context.Background(), h.start.Add(h.el).Add(-6*time.Hour))
		if lost == 0 && !h.authorized(a.IP) {
			lost = h.el
		}
	}
	if lost == 0 || lost > planAt+window+10*time.Second {
		t.Fatalf("a backwards wall clock stretched the window: lost at %v, window ended %v", lost, planAt+window)
	}
}

// ---- persisted record: tamper, age, scope ---------------------------------------------------------------------------

func TestContinuity_PersistedRecordIsRefusedWhenAlteredFromAnotherBootOrOtherScope(t *testing.T) {
	setup := func() *contH {
		h := newContH(t)
		a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
		h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, []shapeplan.Session{a}, nil, nil) }, a)
		if err := h.p.cont.saveState(); err != nil {
			t.Fatal(err)
		}
		return h
	}
	fresh := func(h *contH) {
		h.p.cont = &continuity{statePath: h.p.cont.statePath, journalPath: h.p.cont.journalPath, probe: h.probe}
	}
	t.Run("intact record restores", func(t *testing.T) {
		h := setup()
		fresh(h)
		if why := h.p.contRestore(); why != "" || h.p.cont.st == nil || len(h.p.cont.st.Sessions) != 1 {
			t.Fatalf("why=%q st=%+v", why, h.p.cont.st)
		}
	})
	t.Run("a longer deadline written into the file is refused", func(t *testing.T) {
		h := setup()
		raw, _ := os.ReadFile(h.p.cont.statePath)
		tampered := []byte(string(raw))
		for _, s := range h.p.cont.st.Sessions {
			tampered = replaceOnce(tampered, `"deadline_boot_ms":`+strconv.FormatInt(s.DeadlineBoot, 10),
				`"deadline_boot_ms":`+strconv.FormatInt(s.DeadlineBoot+86_400_000, 10))
		}
		if string(tampered) == string(raw) {
			t.Fatal("test could not alter the record")
		}
		_ = os.WriteFile(h.p.cont.statePath, tampered, 0o600)
		fresh(h)
		if why := h.p.contRestore(); why == "" || h.p.cont.st != nil {
			t.Fatalf("an altered record was accepted (why=%q)", why)
		}
		if _, err := os.Stat(h.p.cont.statePath); !os.IsNotExist(err) {
			t.Fatal("a refused record must be removed")
		}
	})
	t.Run("another boot", func(t *testing.T) {
		h := setup()
		h.clk.bootID = "11111111-2222-3333-4444-555555555555"
		fresh(h)
		if why := h.p.contRestore(); why == "" || h.p.cont.st != nil {
			t.Fatalf("a record from another boot was accepted (why=%q)", why)
		}
	})
	t.Run("another appliance or an older assignment", func(t *testing.T) {
		h := setup()
		h.p.mode.ApplianceID = "00000000-0000-0000-0000-00000000dead"
		fresh(h)
		if why := h.p.contRestore(); why == "" {
			t.Fatal("another appliance's record was accepted")
		}
		h = setup()
		h.p.mode.AssignGen = 99
		fresh(h)
		if why := h.p.contRestore(); why == "" {
			t.Fatal("a record older than the assignment was accepted")
		}
	})
	t.Run("a corrupt journal disables continuity rather than hiding a revocation", func(t *testing.T) {
		h := setup()
		_ = h.p.contRevoke(context.Background(), contRevocation{SessionID: "zzz", Reason: "TEST"})
		_ = os.WriteFile(h.p.cont.journalPath, []byte(`{"version":1,"entries":[],"sum":"bogus"}`), 0o600)
		fresh(h)
		if why := h.p.contRestore(); why == "" || h.p.cont.st != nil {
			t.Fatalf("continuity must stay off when the journal cannot be trusted (why=%q)", why)
		}
	})
	t.Run("a record whose plan time is in the future of this boot", func(t *testing.T) {
		h := setup()
		h.clk.ms = 1000 // as if the monotonic clock were behind the record
		fresh(h)
		if why := h.p.contRestore(); why == "" {
			t.Fatal("a record from the future was accepted")
		}
	})
}

// A restart with the database still away restores and keeps governing within the original window.
func TestContinuity_NetdRestartDuringTheOutageKeepsTheOriginalDeadline(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	window := 10 * time.Minute
	h.healthy(60, func() *shapeplan.Continuity { return block(window, []shapeplan.Session{a}, nil, nil) }, a)
	planAt := h.el
	h.run(4*time.Minute, nil)
	if err := h.p.cont.saveState(); err != nil {
		t.Fatal(err)
	}
	h.p.cont = &continuity{statePath: h.p.cont.statePath, journalPath: h.p.cont.journalPath, probe: h.probe}
	if why := h.p.contRestore(); why != "" {
		t.Fatalf("restore: %s", why)
	}
	var lost time.Duration
	h.run(window, func(el time.Duration) {
		if lost == 0 && !h.authorized(a.IP) {
			lost = el
		}
	})
	if lost == 0 || lost > planAt+window+10*time.Second {
		t.Fatalf("the restart must not reset the window: lost at %v, original end %v", lost, planAt+window)
	}
}

// When plans return, the governor stands down and reports no outage.
func TestContinuity_PlansReturningEndTheOutage(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	mk := func() *shapeplan.Continuity { return block(time.Hour, []shapeplan.Session{a}, nil, nil) }
	h.healthy(60, mk, a)
	h.run(5*time.Minute, nil)
	if !h.p.contStatus().Active {
		t.Fatal("precondition: outage active")
	}
	h.healthy(30, mk, a)
	if h.p.contStatus().Active {
		t.Fatal("the outage must end when plans return")
	}
	if !h.authorized(a.IP) {
		t.Fatal("recovery lost the guest")
	}
}

// A block that is stale, altered or oversized is refused and the plan still applies strictly.
func TestContinuity_BadBlocksAreRefusedAndTheGuestIsHeldStrictly(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	good := block(time.Hour, []shapeplan.Session{a}, nil, nil)
	cases := map[string]func(c *shapeplan.Continuity){
		"altered hash":    func(c *shapeplan.Continuity) { c.WindowSeconds += 60 }, // hash recomputed by submit(): re-break it below
		"window too long": func(c *shapeplan.Continuity) { c.WindowSeconds = 121 * 60 },
		"stale budget":    func(c *shapeplan.Continuity) { c.AsOf = h.now().Add(-10 * time.Minute) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h.gen++
			env := planAt(h.gen, h.now(), a)
			c := *good
			c.AsOf = h.now()
			c.ContentsHash = shapeplan.HashContinuity(c)
			mutate(&c)
			if name != "altered hash" {
				c.ContentsHash = shapeplan.HashContinuity(c)
			}
			env.Continuity = &c
			res, err := h.p.submit(context.Background(), env, h.now())
			if err != nil || !res.Accepted {
				t.Fatalf("the plan itself must still be accepted: %+v %v", res, err)
			}
			if h.p.cont.st != nil {
				t.Fatalf("%s: a refused block left continuity in force", name)
			}
			if h.p.cont.refused == "" {
				t.Fatalf("%s: no refusal reason recorded", name)
			}
		})
	}
}

func netIP(s string) net.IP { return net.ParseIP(s) }

func replaceOnce(b []byte, old, new string) []byte {
	s := string(b)
	i := indexOf(s, old)
	if i < 0 {
		return b
	}
	return []byte(s[:i] + new + s[i+len(old):])
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
