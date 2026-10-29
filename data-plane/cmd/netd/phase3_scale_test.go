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
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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
	sh("ip link del " + scaleBridge + " 2>/dev/null; ip link del ifb-gtest 2>/dev/null; nft delete table inet stayconnect 2>/dev/null; true")
	sh("ip link add " + scaleBridge + " type bridge && ip link set " + scaleBridge + " up")
	sh("nft add table inet stayconnect")
	sh("nft add set inet stayconnect auth_ipv4 '{ type ifname . ipv4_addr ; flags timeout ; }'")
	sh("nft add set inet stayconnect phase3_auth_ipv4 '{ type ifname . ipv4_addr ; flags timeout ; size 65536 ; }'")
	sh("nft add set inet stayconnect phase3_auth_mac_ipv4 '{ type ifname . ipv4_addr . ether_addr ; flags timeout ; size 65536 ; }'")
	t.Cleanup(func() {
		_ = exec.Command("sh", "-c", "ip link del "+scaleBridge+"; ip link del ifb-gtest; nft delete table inet stayconnect").Run()
	})
	return &realKernel{}
}

type countingEnforcement struct {
	mu                                  sync.Mutex
	activate, confirm, ended, activated int
	seen                                map[string]bool
}

func (e *countingEnforcement) Activate(ctx context.Context, sessionID, bridge string, minor int, epoch int64) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activate++
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
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ended++
	return nil
}
func (e *countingEnforcement) Confirm(ctx context.Context, sessionID string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.confirm++
	return e.seen[sessionID], nil
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
	mu sync.Mutex
	n  int
}

func (o *countingOrigins) RegisterClassOrigin(ctx context.Context, c classOrigin) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.n++
	return "ORIGIN_REGISTERED", nil
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
	e.gate = newFlushingGate(&nftGate{c: k.nftClient()}, &nopFlusher{})
	p.gate = e.gate
	p.enforcement = e.enf
	p.origins = e.org
	p.owner = addressOwner{src: e.leases, now: k.clock}
	e.p = p
	return e
}

func scaleSession(i int) shapeplan.Session {
	a := 2 + i
	return shapeplan.Session{
		SessionID: fmt.Sprintf("s-%05d", i), DeviceID: fmt.Sprintf("d-%05d", i),
		IP: fmt.Sprintf("10.20.%d.%d", a/256, a%256), Bridge: scaleBridge, DownKbps: 5000, UpKbps: 2000, Entitled: true,
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
		ManagedBridges: []string{scaleBridge}, Sessions: append([]shapeplan.Session(nil), e.sessions...),
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
	line := fmt.Sprintf("SCALE kernel=%s scenario=%s n=%d execs=%d tc=%d nft=%d nft_list=%d nft_write=%d class_change=%d class_show=%d tc_batch=%d nft_script=%d db=%d lease_reads=%d shaped=%d failed=%d wall_ms=%d est_at_15ms_s=%.1f %s",
		kind, scenario, n, st.cnt.total(), st.cnt.TC, st.cnt.NFT, st.cnt.NFTList, st.cnt.NFTWrite, st.cnt.TCClassChange, st.cnt.TCClassShow,
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

// TestScaleTable prints the measurement table (see the header). It asserts only convergence, not cost: the cost regressions are
// TestSteadyPassCostIsConstant and TestNewSessionsAreNotStarvedByActiveOnes.
func TestScaleTable(t *testing.T) {
	real := os.Getenv("NETD_SCALE_REAL") == "1"
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
				e.k.advance(5 * time.Second)
				e.pass()
			}
			if got := e.activeCount(); got != n {
				t.Fatalf("only %d of %d sessions became active", got, n)
			}
			// 2. one steady pass: n active, nothing changed
			e.k.advance(5 * time.Second)
			st = e.pass()
			logScale(t, kind, "steady", n, st, "")
			// 3. a pass when every lease is due for renewal (remaining < 60 s of 90 s)
			e.k.advance(35 * time.Second)
			st = e.pass()
			logScale(t, kind, "renew_due", n, st, "")
			// 4. 300 new sessions arrive on top of n active ones
			e.addSessions(n, n+300)
			e.k.advance(5 * time.Second)
			st = e.pass()
			logScale(t, kind, "arrive300", n, st, fmt.Sprintf("new_active=%d/300", e.activeCount()-n))
			for i := 0; i < 20 && e.activeCount() < n+300; i++ {
				e.k.advance(5 * time.Second)
				e.pass()
			}
			if got := e.activeCount(); got != n+300 {
				t.Fatalf("only %d of %d sessions became active after arrivals", got, n+300)
			}
		})
	}
}

var _ = net.IPv4len
