package main

// Scaling harness for the Phase-3 enforcement pass. It drives the REAL reconcileLocked / provisionSession and the REAL internal/shape and
// internal/nft clients against a simulated kernel (always) or a real one (NETD_SCALE_REAL=1, privileged container with tc, nft, ip), counts every
// forked tc/nft process, and prints one SCALE line per measurement.
//
//	go test ./cmd/netd/ -run TestScaleTable -v                      # N=100,500 against the simulated kernel
//	NETD_SCALE_FULL=1 go test ./cmd/netd/ -run TestScaleTable -v    # N=100,500,1000,2000
//	NETD_SCALE_REAL=1 NETD_SCALE_FULL=1 go test ./cmd/netd/ -run TestScaleTable -v   # real kernel (root, tc, nft, ip)

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

const scaleBridge = "br-gtest"

type kern interface {
	shapeClient() *shape.Client
	nftClient() *nft.Client
	counts() execCounts
	advance(d time.Duration)
	clock() time.Time
}

func (k *kernelFake) clock() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.now
}

func (r *realKernel) shapeClient() *shape.Client { return shape.NewWithExec(r.exec, r.execIn) }
func (r *realKernel) nftClient() *nft.Client     { return nft.NewWithExec(r.exec, r.execIn) }
func (r *realKernel) clock() time.Time           { return time.Now() }
func (r *realKernel) advance(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

// setupRealKernel prepares the dummy bridge and the three authorization sets the production ruleset has.
func setupRealKernel(t *testing.T) *realKernel {
	t.Helper()
	sh := func(cmd string) {
		out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("setup %q: %v: %s", cmd, err, out)
		}
	}
	for _, b := range scaleBridgeNames() {
		sh("ip link del " + b + " 2>/dev/null; ip link del " + shape.IFBName(b) + " 2>/dev/null; true")
		sh("ip link add " + b + " type bridge && ip link set " + b + " up")
	}
	sh("nft delete table inet stayconnect 2>/dev/null; true")
	sh("nft add table inet stayconnect")
	sh("nft add set inet stayconnect auth_ipv4 '{ type ifname . ipv4_addr ; flags timeout ; }'")
	sh("nft add set inet stayconnect phase3_auth_ipv4 '{ type ifname . ipv4_addr ; flags timeout ; size 65536 ; }'")
	sh("nft add set inet stayconnect phase3_auth_mac_ipv4 '{ type ifname . ipv4_addr . ether_addr ; flags timeout ; size 65536 ; }'")
	t.Cleanup(func() {
		for _, b := range scaleBridgeNames() {
			_ = exec.Command("sh", "-c", "ip link del "+b+"; ip link del "+shape.IFBName(b)).Run()
		}
		_ = exec.Command("sh", "-c", "nft delete table inet stayconnect").Run()
	})
	return &realKernel{}
}

type countingEnforcement struct {
	mu                                  sync.Mutex
	activate, confirm, ended, activated int
	isEndedCalls                        int
	seen                                map[string]bool
	// endedIDs are the sessions durable state has ended (by the expiry sweep, or by an Ended call).
	endedIDs map[string]bool
	// endOnActivate lists sessions that durable state ends the moment their promotion is asked for.
	endOnActivate map[string]bool
	// call, when set, runs on every database call (outside the lock): the churn harness advances its clock by the call's latency there.
	call func(kind string)
}

func (e *countingEnforcement) hook(kind string) {
	if e.call != nil {
		e.call(kind)
	}
}

func (e *countingEnforcement) markEndedInDB(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.endedIDs == nil {
		e.endedIDs = map[string]bool{}
	}
	e.endedIDs[id] = true
}

func (e *countingEnforcement) Activate(ctx context.Context, sessionID, bridge string, minor int, epoch int64) (string, error) {
	e.hook("activate")
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activate++
	if e.endOnActivate[sessionID] {
		// ended in durable state between the origin and the promotion
		e.endOnActivate[sessionID] = false
		if e.endedIDs == nil {
			e.endedIDs = map[string]bool{}
		}
		e.endedIDs[sessionID] = true
	}
	if e.endedIDs[sessionID] {
		return "", fmt.Errorf("ERROR: ENFORCE_SESSION_ENDED: session %s has ended and cannot be activated", sessionID)
	}
	if e.seen == nil {
		e.seen = map[string]bool{}
	}
	if e.seen[sessionID] {
		return "ALREADY_ACTIVE", nil
	}
	e.seen[sessionID] = true
	e.activated++
	return "ACTIVATED", nil
}
func (e *countingEnforcement) Ended(ctx context.Context, sessionID, reason string) error {
	e.hook("ended")
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ended++
	if e.endedIDs == nil {
		e.endedIDs = map[string]bool{}
	}
	e.endedIDs[sessionID] = true
	return nil
}
func (e *countingEnforcement) IsEnded(ctx context.Context, sessionID string) (bool, error) {
	e.hook("is_ended")
	e.mu.Lock()
	defer e.mu.Unlock()
	e.isEndedCalls++
	return e.endedIDs[sessionID], nil
}
func (e *countingEnforcement) Confirm(ctx context.Context, sessionID string) (bool, error) {
	e.hook("confirm")
	e.mu.Lock()
	defer e.mu.Unlock()
	e.confirm++
	return e.seen[sessionID] && !e.endedIDs[sessionID], nil
}
func (e *countingEnforcement) db() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.activate + e.confirm + e.ended
}
func (e *countingEnforcement) isActive(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seen[id]
}

type countingOrigins struct {
	mu   sync.Mutex
	n    int
	enf  *countingEnforcement
	call func(kind string)
}

func (o *countingOrigins) RegisterClassOrigin(ctx context.Context, c classOrigin) (string, error) {
	if o.call != nil {
		o.call("origin")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.enf != nil {
		o.enf.mu.Lock()
		gone := o.enf.endedIDs[c.SessionID]
		o.enf.mu.Unlock()
		if gone {
			return "", fmt.Errorf("ERROR: ACCT_SESSION_OUT_OF_SCOPE: session %s ended at 2026-01-01", c.SessionID)
		}
	}
	o.n++
	return "ORIGIN_REGISTERED", nil
}

// slowGenerations puts a latency hook in front of the generation allocator.
type slowGenerations struct {
	generationAllocator
	call func(kind string)
}

func (g slowGenerations) AllocateClassGeneration(ctx context.Context, tenant, site, appliance string) (int64, error) {
	if g.call != nil {
		g.call("alloc")
	}
	return g.generationAllocator.AllocateClassGeneration(ctx, tenant, site, appliance)
}

type scaleLeases struct {
	mu     sync.Mutex
	leases []KeaLease
	reads  int
}

func (l *scaleLeases) Leases() ([]KeaLease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads++
	return l.leases, nil
}

type scaleEnv struct {
	t        *testing.T
	k        kern
	p        *phase3Shaping
	enf      *countingEnforcement
	org      *countingOrigins
	leases   *scaleLeases
	gate     *flushingGate
	flows    *nopFlusher
	clk      *atomicClock
	gens     *fakeGenerations
	sessions []shapeplan.Session
	gen      int64
	budget   time.Duration
}

func newScaleEnv(t *testing.T, k kern) *scaleEnv {
	e := &scaleEnv{t: t, k: k, enf: &countingEnforcement{}, org: &countingOrigins{}, leases: &scaleLeases{}, budget: phase3PassTimeout}
	p := liveWriter(k.shapeClient())
	e.gens = &fakeGenerations{}
	p.generations = e.gens
	e.flows = &nopFlusher{}
	e.gate = newFlushingGate(&nftGate{c: k.nftClient()}, e.flows)
	p.gate = e.gate
	p.enforcement = e.enf
	p.origins = e.org
	e.org.enf = e.enf
	e.clk = newAtomicClock(60_000, "f81d4fae-7dec-11d0-a765-00a0c91e6bf6")
	p.secClock = e.clk
	p.owner = addressOwner{src: e.leases, now: k.clock}
	e.p = p
	return e
}

// scaleBridgeCount spreads the sessions over that many bridges (NETD_SCALE_BRIDGES). The kernel installs at most 2047 u32 filters per device parent (the
// hash-table id space is 0x7ff), so one bridge cannot hold more than 2047 shaped sessions whatever netd does; a site with more uses several guest networks.
var scaleBridgeCount = 1

func scaleBridgeName(b int) string {
	if b == 0 {
		return scaleBridge
	}
	return fmt.Sprintf("br-gtest%d", b+1)
}

func scaleBridgeNames() []string {
	out := make([]string, scaleBridgeCount)
	for b := range out {
		out[b] = scaleBridgeName(b)
	}
	return out
}

func scaleSession(i int) shapeplan.Session {
	b := i % scaleBridgeCount
	a := 2 + i/scaleBridgeCount
	return shapeplan.Session{
		SessionID: fmt.Sprintf("s-%05d", i), DeviceID: fmt.Sprintf("d-%05d", i),
		IP: fmt.Sprintf("10.%d.%d.%d", 20+b, a/256, a%256), Bridge: scaleBridgeName(b), DownKbps: 5000, UpKbps: 2000, Entitled: true,
		MAC: fmt.Sprintf("02:00:00:%02x:%02x:%02x", (i>>16)&0xff, (i>>8)&0xff, i&0xff),
	}
}

func (e *scaleEnv) addSessions(from, to int) {
	for i := from; i < to; i++ {
		s := scaleSession(i)
		e.sessions = append(e.sessions, s)
		now := e.k.clock()
		e.leases.mu.Lock()
		e.leases.leases = append(e.leases.leases, KeaLease{IPAddress: s.IP, HWAddr: s.MAC, CLTT: now.Unix(), ValidLft: 3600})
		e.leases.mu.Unlock()
	}
}

type passStat struct {
	res    shapingPlanResponse
	wall   time.Duration
	cnt    execCounts
	db     int
	leases int
}

// pass submits the whole plan once and reports what it cost.
func (e *scaleEnv) pass() passStat {
	e.gen++
	now := e.k.clock()
	env := shapeplan.Envelope{
		ContractVersion: shapeplan.ContractVersion, TenantID: testTenant, SiteID: testSite, ApplianceID: testAppliance,
		AssignmentGen: 7, PlanGeneration: e.gen, GeneratedAt: now, ExpiresAt: now.Add(90 * time.Second),
		ManagedBridges: scaleBridgeNames(), Sessions: append([]shapeplan.Session(nil), e.sessions...),
	}
	env.DesiredStateHash = shapeplan.HashDesiredState(env.ManagedBridges, env.Sessions)
	e.leases.mu.Lock()
	r0 := e.leases.reads
	e.leases.mu.Unlock()
	c0, d0 := e.k.counts(), e.enf.db()+e.org.n+len(e.gens.issued)
	ctx, cancel := context.WithTimeout(context.Background(), e.budget)
	defer cancel()
	t0 := time.Now()
	res, err := e.p.submit(ctx, env, now)
	wall := time.Since(t0)
	if err != nil {
		e.t.Fatalf("submit: %v", err)
	}
	e.leases.mu.Lock()
	r1 := e.leases.reads
	e.leases.mu.Unlock()
	return passStat{res: res, wall: wall, cnt: e.k.counts().sub(c0), db: e.enf.db() + e.org.n + len(e.gens.issued) - d0, leases: r1 - r0}
}

// tick moves the kernel's clock (element expiry) and the boot clock the activation bound is measured on forward together.
func (e *scaleEnv) tick(d time.Duration) {
	e.k.advance(d)
	e.clk.add(d)
}

// atomicClock is the boot clock of the harness; unlike fixedSecurityClock it can be advanced by the database-latency hooks, which run on several
// goroutines at once, while the pass reads it.
type atomicClock struct {
	ms     atomic.Int64
	bootID string
}

func newAtomicClock(ms int64, bootID string) *atomicClock {
	c := &atomicClock{bootID: bootID}
	c.ms.Store(ms)
	return c
}

func (c *atomicClock) add(d time.Duration)        { c.ms.Add(d.Milliseconds()) }
func (c *atomicClock) BootMillis() (int64, error) { return c.ms.Load(), nil }
func (c *atomicClock) BootID() (string, error)    { return c.bootID, nil }

func (e *scaleEnv) activeCount() int {
	n := 0
	for _, s := range e.sessions {
		if e.enf.isActive(s.SessionID) {
			n++
		}
	}
	return n
}

func logScale(t *testing.T, kind, scenario string, n int, st passStat, extra string) {
	line := fmt.Sprintf("SCALE bridges=%d kernel=%s scenario=%s n=%d execs=%d tc=%d nft=%d nft_list=%d nft_write=%d class_change=%d class_show=%d tc_batch=%d nft_script=%d db=%d lease_reads=%d shaped=%d failed=%d wall_ms=%d est_at_15ms_s=%.1f %s",
		scaleBridgeCount, kind, scenario, n, st.cnt.total(), st.cnt.TC, st.cnt.NFT, st.cnt.NFTList, st.cnt.NFTWrite, st.cnt.TCClassChange, st.cnt.TCClassShow,
		st.cnt.TCBatch, st.cnt.NFTScript, st.db, st.leases, st.res.Shaped, st.res.Failed, st.wall.Milliseconds(),
		float64(st.cnt.total())*0.015, extra)
	if len(st.res.Problems) > 0 {
		line += " first_problem=" + st.res.Problems[0]
	}
	t.Log(line)
	fmt.Println(line)
}

func scaleSizes() []int {
	if v := os.Getenv("NETD_SCALE_N"); v != "" {
		var out []int
		for _, f := range strings.Split(v, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
				out = append(out, n)
			}
		}
		return out
	}
	if os.Getenv("NETD_SCALE_FULL") == "1" {
		return []int{100, 500, 1000, 2000}
	}
	return []int{100, 500}
}

// TestRealKernelBatchFallsBackOnRealTCErrors runs only against a real kernel (NETD_SCALE_REAL=1). A qdisc elsewhere on the bridge already owns the
// handle one new session's leaf needs, so the real `tc -batch` refuses: the other sessions must still be admitted through the per-session path, and
// only the session that really cannot be admitted fails closed.
func TestRealKernelBatchFallsBackOnRealTCErrors(t *testing.T) {
	if os.Getenv("NETD_SCALE_REAL") != "1" {
		t.Skip("needs a real kernel: NETD_SCALE_REAL=1 in a privileged container")
	}
	k := setupRealKernel(t)
	e := newScaleEnv(t, k)
	e.addSessions(9, 10)
	if st := e.pass(); st.res.Failed != 0 {
		t.Fatalf("%v", st.res.Problems)
	}
	// session 2's leaf handle is 1004: (minor 0x1004); take it with an unrelated qdisc outside the guest class range
	for _, c := range []string{
		"tc class add dev " + scaleBridge + " parent 1: classid 1:7ff0 htb rate 1mbit",
		"tc qdisc add dev " + scaleBridge + " parent 1:7ff0 handle 1004: pfifo",
	} {
		if out, err := exec.Command("sh", "-c", c).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", c, err, out)
		}
	}
	e.addSessions(0, 5)
	e.tick(time.Second)
	st := e.pass()
	if st.cnt.TCBatch == 0 {
		t.Fatal("the batch path was not tried")
	}
	if st.res.Failed != 1 || !strings.Contains(st.res.Problems[0], "s-00002") {
		t.Fatalf("exactly session 2 must fail: failed=%d %v", st.res.Failed, st.res.Problems)
	}
	if got := e.activeCount(); got != 5 { // 9, 0, 1, 3, 4
		t.Fatalf("active = %d, want 5", got)
	}
}

// TestScaleTable prints the measurement table (see the header). It asserts only convergence, not cost: the cost regressions are
// TestSteadyPassCostIsConstant and TestNewSessionsAreNotStarvedByActiveOnes.
func TestScaleTable(t *testing.T) {
	real := os.Getenv("NETD_SCALE_REAL") == "1"
	if v, err := strconv.Atoi(os.Getenv("NETD_SCALE_BRIDGES")); err == nil && v > 0 {
		scaleBridgeCount = v
		defer func() { scaleBridgeCount = 1 }()
	}
	for _, n := range scaleSizes() {
		mk := func() kern {
			if real {
				return setupRealKernel(t)
			}
			return newKernelFake(time.Now())
		}
		kind := "fake"
		if real {
			kind = "real"
		}
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			// 1. cold admission of n sessions in one pass
			e := newScaleEnv(t, mk())
			e.addSessions(0, n)
			st := e.pass()
			logScale(t, kind, "admit", n, st, fmt.Sprintf("active=%d", e.activeCount()))
			// pending sessions (if the pass did not finish) are driven to completion so the later scenarios start from n active
			for i := 0; i < 20 && e.activeCount() < n; i++ {
				e.tick(5 * time.Second)
				e.pass()
			}
			if got := e.activeCount(); got != n {
				t.Fatalf("only %d of %d sessions became active", got, n)
			}
			// 2. one steady pass: n active, nothing changed
			e.tick(5 * time.Second)
			st = e.pass()
			logScale(t, kind, "steady", n, st, "")
			// 3. a pass when every lease is due for renewal (remaining < 60 s of 90 s)
			e.tick(35 * time.Second)
			st = e.pass()
			logScale(t, kind, "renew_due", n, st, "")
			// 3b. the durable proof of every session is due again (60-90 s after it was given)
			e.tick(60 * time.Second)
			st = e.pass()
			logScale(t, kind, "reprove_due", n, st, "")
			// 4. 300 new sessions arrive on top of n active ones
			e.addSessions(n, n+300)
			e.tick(5 * time.Second)
			st = e.pass()
			logScale(t, kind, "arrive300", n, st, fmt.Sprintf("new_active=%d/300", e.activeCount()-n))
			for i := 0; i < 20 && e.activeCount() < n+300; i++ {
				e.tick(5 * time.Second)
				e.pass()
			}
			if got := e.activeCount(); got != n+300 {
				t.Fatalf("only %d of %d sessions became active after arrivals", got, n+300)
			}
		})
	}
}
