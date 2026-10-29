package main

// Batched, ordered enforcement pass.
//
// The pass used to cost O(N) forked tc/nft processes PER SESSION PER PASS, and several of them (every nft element operation lists the whole set) cost
// O(N) themselves, so a pass over N sessions was O(N^2) forks. A 2 vCPU appliance stopped converging at about 380 sessions: the pass outran its budget,
// the tc call that hit the deadline failed a session closed (more work), and the sessions at the end of the sorted walk, the NEW ones, were never reached.
// See docs/reviews/NETD_ENFORCEMENT_SCALING.md for the measurements.
//
// What this file changes, and what it keeps:
//
//   - ORDER. Fail-closed work comes first, then new and changed sessions, then routine renewal of sessions that are already in force and unchanged.
//     Routine work is bounded by the pass deadline and can never delay the urgent work.
//   - NO WORK FOR AN UNCHANGED SESSION. A class whose installed parent, rate and ceiling (read once per device per pass) equal the plan's is not rewritten;
//     a session proven durably active on this boot is not proven again every pass; a lease with time left is not rewritten.
//   - BATCHES. What does have to be written is written in one process: authorizations and revocations in one `nft -f -` transaction with one read before
//     and one after, class creation, filter installation and class removal in one `tc -batch`.
//   - ATTRIBUTION. A refused batch is never reported as one opaque failure and never fails every session in it: the nft batch retries once on a fresh read and
//     then runs element by element; a refused tc batch sends its sessions through the per-session path, which clears the slot and so converges from any
//     partial state. Only the session that really cannot be written is failed closed.
//   - FAIL CLOSED STAYS FAIL CLOSED. A denial is proven (re-read) before the tc teardown that follows it, as before; it is only grouped. The flush that runs
//     denials and the renewal writes uses a context detached from the pass deadline, so an exhausted pass cannot leave a revocation undone.
//
// Everything here is active only when the shaper and the gate implement the batch interfaces (the production ones do; the per-session test doubles do not)
// and NETD_PASS_BATCHING is not "false". Otherwise the pass is exactly the per-session pass it always was.

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"sort"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

const (
	// phase3ReproveEvery is how long a durable ACTIVE proof is trusted before the database is asked again; the per-session jitter spreads those calls
	// over phase3ReproveEvery..+phase3ReproveJitter instead of making every session ask in the same pass.
	phase3ReproveEvery  = 60 * time.Second
	phase3ReproveJitter = 30 * time.Second
	// phase3RoutineReserve is the part of the pass budget routine renewal must leave untouched.
	phase3RoutineReserve = 15 * time.Second
	// phase3UrgentBudget bounds the flushes that fail sessions closed and write leases, detached from the pass deadline.
	phase3UrgentBudget = 30 * time.Second
)

// batchGate is implemented by a gate that can write and prove many authorizations in one operation. Each result slice has one entry per request.
type batchGate interface {
	// AuthorizeBatch asserts the leases and verifies they are present; a nil entry means authorized.
	AuthorizeBatch(ctx context.Context, reqs []gateLease) []error
	// RevokeBatch removes the authorizations and proves them gone; a nil entry means the address is no longer authorized.
	RevokeBatch(ctx context.Context, reqs []gateAddr) []error
}

type gateLease struct {
	Bridge string
	IP     net.IP
	TTL    time.Duration
}

type gateAddr struct {
	Bridge string
	IP     net.IP
}

// batchShaper is implemented by a shaper that can run many tc operations in one process.
type batchShaper interface {
	PrepareBatch(ctx context.Context, bridge string, items []shape.PrepItem) error
	ActivateBatch(ctx context.Context, bridge string, ips []net.IP) error
	ForwardingBatch(ctx context.Context, bridge string, ips []net.IP) (map[string]bool, error)
	DeleteClassesBatch(ctx context.Context, bridge string, minors []int) error
}

// batchSetOps is the part of the nft client the batch gate uses.
type batchSetOps interface {
	LeaseBatch(ctx context.Context, reqs []nft.LeaseReq, requireMAC bool) []error
	RevokeBatch(ctx context.Context, reqs []nft.AddrReq, requireMAC bool) []error
}

func (p *phase3Shaping) routineReserveOrDefault() time.Duration {
	if p.routineReserve > 0 {
		return p.routineReserve
	}
	return phase3RoutineReserve
}

func (p *phase3Shaping) batching() bool {
	if p.noBatch {
		return false
	}
	if _, ok := p.shp.(batchShaper); !ok {
		return false
	}
	if p.gate != nil {
		if _, ok := p.gate.(batchGate); !ok {
			return false
		}
	}
	return true
}

// ---- gates ----

func (g *nftGate) AuthorizeBatch(ctx context.Context, reqs []gateLease) []error {
	bs, ok := g.c.(batchSetOps)
	if !ok {
		errs := make([]error, len(reqs))
		for i, r := range reqs {
			errs[i] = g.Authorize(ctx, r.Bridge, r.IP, r.TTL)
		}
		return errs
	}
	lr := make([]nft.LeaseReq, len(reqs))
	for i, r := range reqs {
		mac := g.macFor(r.Bridge, r.IP)
		if mac == "" && g.bound() {
			slog.Warn("netd: MAC binding is on and this address has no known device address; it is not authorized by the MAC-bound set",
				"bridge", r.Bridge, "ip", r.IP.String())
		}
		lr[i] = nft.LeaseReq{Iface: r.Bridge, IP: r.IP, MAC: mac, TTL: r.TTL}
	}
	return bs.LeaseBatch(ctx, lr, g.bound())
}

func (g *nftGate) RevokeBatch(ctx context.Context, reqs []gateAddr) []error {
	bs, ok := g.c.(batchSetOps)
	if !ok {
		errs := make([]error, len(reqs))
		for i, r := range reqs {
			if errs[i] = g.Revoke(ctx, r.Bridge, r.IP); errs[i] != nil {
				continue
			}
			if still, err := g.Authorized(ctx, r.Bridge, r.IP); err != nil {
				errs[i] = err
			} else if still {
				errs[i] = errStillAuthorized
			}
		}
		return errs
	}
	ar := make([]nft.AddrReq, len(reqs))
	for i, r := range reqs {
		ar[i] = nft.AddrReq{Iface: r.Bridge, IP: r.IP}
	}
	errs := bs.RevokeBatch(ctx, ar, g.bound())
	for i, r := range reqs {
		switch {
		case errs[i] == nil:
			if r.IP != nil {
				g.macs.Delete(gateKey(r.Bridge, r.IP))
			}
		case errs[i] == nft.ErrStillPresent:
			errs[i] = errStillAuthorized
		}
	}
	return errs
}

func (g *flushingGate) AuthorizeBatch(ctx context.Context, reqs []gateLease) []error {
	g.mu.Lock()
	for _, r := range reqs {
		delete(g.clean, gateKey(r.Bridge, r.IP))
	}
	g.mu.Unlock()
	if bg, ok := g.gate.(batchGate); ok {
		return bg.AuthorizeBatch(ctx, reqs)
	}
	errs := make([]error, len(reqs))
	for i, r := range reqs {
		errs[i] = g.gate.Authorize(ctx, r.Bridge, r.IP, r.TTL)
	}
	return errs
}

func (g *flushingGate) RevokeBatch(ctx context.Context, reqs []gateAddr) []error {
	var errs []error
	if bg, ok := g.gate.(batchGate); ok {
		errs = bg.RevokeBatch(ctx, reqs)
	} else {
		errs = make([]error, len(reqs))
		for i, r := range reqs {
			if errs[i] = g.gate.Revoke(ctx, r.Bridge, r.IP); errs[i] != nil {
				continue
			}
			if still, err := g.gate.Authorized(ctx, r.Bridge, r.IP); err != nil {
				errs[i] = err
			} else if still {
				errs[i] = errStillAuthorized
			}
		}
	}
	// Open flows end with the authorization (the forward chain accepts established flows before the sets): one conntrack pass for the whole batch.
	var ips []net.IP
	var keys []string
	g.mu.Lock()
	for i, r := range reqs {
		if errs[i] != nil || r.IP == nil {
			continue
		}
		if k := gateKey(r.Bridge, r.IP); !g.clean[k] {
			ips, keys = append(ips, r.IP), append(keys, k)
		}
	}
	g.mu.Unlock()
	if len(ips) == 0 || g.flows == nil {
		return errs
	}
	n, err := g.flows.FlushAddrs(ctx, ips)
	if err != nil {
		// The revocations stand; only already-open flows survive, and the next revocation retries.
		slog.Error("netd: authorizations revoked but the addresses' open flows could not be ended", "addresses", len(ips), "err", err)
		return errs
	}
	if n > 0 {
		slog.Info("netd: ended the open flows of revoked addresses", "addresses", len(ips), "flows", n)
	}
	g.mu.Lock()
	for _, k := range keys {
		g.clean[k] = true
	}
	g.mu.Unlock()
	return errs
}

// ---- pass state ----

type pendingDeny struct {
	bridge string
	ip     net.IP
	// problem is reported (with Failed++) once the denial and, when abort is set, the tc teardown have run.
	problem string
	// abort: tear the session's tc state down after the denial, as failClosed does.
	abort bool
	// errLabel: for a plain hold, the text a failed denial is reported under (the hold itself is reported by the caller).
	errLabel string
	// quiet: the access ended in the ordinary way (the window ran out, durable state says ended). Withdrawing it is counted, not reported as a failure,
	// unless the withdrawal itself cannot be proven.
	quiet bool
}

type pendingRenew struct {
	bridge    string
	ip        net.IP
	want      time.Duration
	sessionID string
	// known: the pass could read the set; false makes the failure read "renew" as the unconditional refresh did.
	known bool
}

type passBatch struct {
	denies []pendingDeny
	renew  map[string]pendingRenew
	// ends are durable end records still to be written for sessions whose kernel state is already gone (finishTears).
	ends []pendingEnd
	// activations are promotion results fetched concurrently ahead of the sequential state machine that consumes them.
	activations map[string]activationResult
}

func newPassBatch() *passBatch {
	return &passBatch{renew: map[string]pendingRenew{}, activations: map[string]activationResult{}}
}

func urgentCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), phase3UrgentBudget)
}

type provenMark struct {
	epoch  int64
	bootMs int64
}

func reproveAfter(key string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return (phase3ReproveEvery + time.Duration(h.Sum32()%uint32(phase3ReproveJitter/time.Millisecond))*time.Millisecond).Milliseconds()
}

// recentlyProven: this class generation was proven durably active on this boot, within the re-prove interval, and nothing has questioned it since.
func (p *phase3Shaping) recentlyProven(key string, epoch, nowBoot int64) bool {
	m, ok := p.provenAt[key]
	if !ok || m.epoch != epoch || p.quarantined[key] != nil {
		return false
	}
	age := nowBoot - m.bootMs
	return age >= 0 && age < reproveAfter(key)
}

// phase3ProofMaxAge is how long a proof of durable ACTIVE is trusted for a full-length lease. Re-proof normally comes every 60-90 s; this is the margin
// for a database that is slow, not a second interval.
const phase3ProofMaxAge = 5 * time.Minute

// proofFresh: proven on this boot, for this class generation, within phase3ProofMaxAge.
func (p *phase3Shaping) proofFresh(key string, epoch, nowBoot int64) bool {
	m, ok := p.provenAt[key]
	if !ok || m.epoch != epoch {
		return false
	}
	age := nowBoot - m.bootMs
	return age >= 0 && age < phase3ProofMaxAge.Milliseconds()
}

func (p *phase3Shaping) provenKnown(key string, epoch int64) bool {
	m, ok := p.provenAt[key]
	return ok && m.epoch == epoch
}

func (p *phase3Shaping) noteProven(key string, epoch, nowBoot int64) {
	if p.provenAt == nil {
		p.provenAt = map[string]provenMark{}
	}
	p.provenAt[key] = provenMark{epoch: epoch, bootMs: nowBoot}
}

// holdDeny is the denial a quarantine or cross-boot hold repeats every pass; the hold itself is reported by the caller.
func (p *phase3Shaping) holdDeny(ctx context.Context, bridge string, ip net.IP, res *shapingPlanResponse, label string) {
	if p.pb != nil {
		p.pb.denies = append(p.pb.denies, pendingDeny{bridge: bridge, ip: ip, errLabel: label})
		return
	}
	if derr := p.denyAccess(ctx, bridge, ip); derr != nil {
		res.Problems = append(res.Problems, label+": PACKET AUTHORIZATION NOT PROVEN REMOVED: "+derr.Error())
	}
}

func (p *phase3Shaping) denyBatch(ctx context.Context, reqs []gateAddr) []error {
	if p.gate == nil {
		return make([]error, len(reqs))
	}
	if bg, ok := p.gate.(batchGate); ok && !p.noBatch {
		return bg.RevokeBatch(ctx, reqs)
	}
	errs := make([]error, len(reqs))
	for i, r := range reqs {
		errs[i] = p.denyAccess(ctx, r.Bridge, r.IP)
	}
	return errs
}

func (p *phase3Shaping) authorizeBatch(ctx context.Context, reqs []gateLease) []error {
	if p.gate == nil {
		return make([]error, len(reqs))
	}
	if bg, ok := p.gate.(batchGate); ok && !p.noBatch {
		return bg.AuthorizeBatch(ctx, reqs)
	}
	errs := make([]error, len(reqs))
	for i, r := range reqs {
		errs[i] = p.gate.Authorize(ctx, r.Bridge, r.IP, r.TTL)
	}
	return errs
}

// flushDenies proves the queued denials in one transaction, then tears the tc state of the sessions that failed closed down, in that order.
func (p *phase3Shaping) flushDenies(ctx context.Context, res *shapingPlanResponse) {
	if p.pb == nil || len(p.pb.denies) == 0 {
		return
	}
	items := p.pb.denies
	p.pb.denies = nil
	uctx, cancel := urgentCtx(ctx)
	defer cancel()
	reqs := make([]gateAddr, len(items))
	for i, d := range items {
		reqs[i] = gateAddr{Bridge: d.bridge, IP: d.ip}
	}
	errs := p.denyBatch(uctx, reqs)
	suffix := make([]string, len(items))
	byBridge := map[string][]int{}
	for i, d := range items {
		if errs[i] != nil {
			if d.abort {
				// The guest may still be authorized: the most serious outcome, reported as such.
				suffix[i] = " (PACKET AUTHORIZATION NOT PROVEN REMOVED: " + errs[i].Error() + ")"
			} else {
				res.Problems = append(res.Problems, d.errLabel+": PACKET AUTHORIZATION NOT PROVEN REMOVED: "+errs[i].Error())
			}
		}
		if d.abort {
			byBridge[d.bridge] = append(byBridge[d.bridge], i)
		}
	}
	bridges := make([]string, 0, len(byBridge))
	for b := range byBridge {
		bridges = append(bridges, b)
	}
	sort.Strings(bridges)
	for _, b := range bridges {
		for i, s := range p.abortBatch(uctx, b, items, byBridge[b]) {
			suffix[i] += s
		}
	}
	for i, d := range items {
		if !d.abort {
			continue
		}
		if d.quiet && suffix[i] == "" {
			res.EndedMidPass++
			continue
		}
		res.Failed++
		res.Problems = append(res.Problems, d.problem+suffix[i])
	}
}

// abortBatch is AbortSession for many sessions of one bridge: filters first, then classes, then ONE read per device to prove them gone. A session
// whose class is still there, or whose batch could not run, goes through the per-session AbortSession and its RemoveClassification fallback.
// The result maps an item index to the text to append to its problem.
func (p *phase3Shaping) abortBatch(ctx context.Context, bridge string, items []pendingDeny, idx []int) map[int]string {
	out := map[int]string{}
	single := func(i int) {
		d := items[i]
		if err := p.shp.AbortSession(ctx, bridge, d.ip); err != nil {
			if clsErr := p.shp.RemoveClassification(ctx, bridge, d.ip); clsErr != nil {
				out[i] = " (tc cleanup failed AND classification removal failed: " + err.Error() + "; " + clsErr.Error() + ")"
			} else {
				out[i] = " (tc cleanup unproven; classification removed so the leftover class meters nothing: " + err.Error() + ")"
			}
		}
	}
	bs, ok := p.shp.(batchShaper)
	if !ok {
		for _, i := range idx {
			single(i)
		}
		return out
	}
	var minors []int
	for _, i := range idx {
		if m, ok := shape.MinorForIP(items[i].ip); ok {
			minors = append(minors, m)
		}
	}
	derr := bs.DeleteClassesBatch(ctx, bridge, minors)
	down, e1 := p.shp.ReadClasses(ctx, bridge)
	up, e2 := p.shp.ReadClasses(ctx, shape.IFBName(bridge))
	for _, i := range idx {
		m, ok := shape.MinorForIP(items[i].ip)
		if !ok || derr != nil || e1 != nil || e2 != nil {
			single(i)
			continue
		}
		_, d := down[m]
		_, u := up[m]
		if d || u {
			single(i)
		}
	}
	return out
}

// flushRenewals writes the leases that need writing, in one transaction, and fails closed any session whose lease cannot be written.
func (p *phase3Shaping) flushRenewals(ctx context.Context, res *shapingPlanResponse) {
	if p.pb == nil || len(p.pb.renew) == 0 {
		return
	}
	keys := make([]string, 0, len(p.pb.renew))
	for k := range p.pb.renew {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	items := make([]pendingRenew, 0, len(keys))
	for _, k := range keys {
		items = append(items, p.pb.renew[k])
	}
	p.pb.renew = map[string]pendingRenew{}
	uctx, cancel := urgentCtx(ctx)
	defer cancel()
	reqs := make([]gateLease, len(items))
	for i, r := range items {
		reqs[i] = gateLease{Bridge: r.bridge, IP: r.ip, TTL: r.want}
	}
	for i, err := range p.authorizeBatch(uctx, reqs) {
		if err == nil {
			continue
		}
		verb := "authorize "
		if !items[i].known {
			verb = "renew "
		}
		p.failClosed(ctx, items[i].bridge, items[i].ip, res, verb+items[i].sessionID+": "+err.Error())
	}
}

// removeStrayAuthorizations removes authorizations the plan does not claim, all in one transaction.
func (p *phase3Shaping) removeStrayAuthorizations(ctx context.Context, els []gateElem, view gateView, res *shapingPlanResponse) {
	uctx, cancel := urgentCtx(ctx)
	defer cancel()
	reqs := make([]gateAddr, len(els))
	for i, el := range els {
		reqs[i] = gateAddr{Bridge: el.Bridge, IP: el.IP}
	}
	for i, err := range p.denyBatch(uctx, reqs) {
		el := els[i]
		if err != nil {
			res.Failed++
			res.Problems = append(res.Problems, "stray authorization "+el.IP.String()+" on "+el.Bridge+" NOT removed: "+err.Error())
			continue
		}
		delete(view.byKey, gateKey(el.Bridge, el.IP))
		res.StraysRemoved++
	}
}

// removeStrayClasses removes classes the plan does not claim in one tc batch and proves them gone with one read.
func (p *phase3Shaping) removeStrayClasses(ctx context.Context, bridge string, minors []int, strayReads map[string]map[int]shape.ClassBytes, res *shapingPlanResponse) {
	bs := p.shp.(batchShaper)
	fail := func(m int, why string) {
		res.Failed++
		res.Problems = append(res.Problems, fmt.Sprintf("stray %d on %s: %s", m, bridge, why))
	}
	if err := bs.DeleteClassesBatch(ctx, bridge, minors); err != nil {
		for _, m := range minors {
			fail(m, err.Error())
		}
		return
	}
	down, err := p.shp.ReadClasses(ctx, bridge)
	if err != nil {
		for _, m := range minors {
			fail(m, "removal not proven: "+err.Error())
		}
		return
	}
	strayReads[bridge] = down
	for _, m := range minors {
		if _, still := down[m]; still {
			fail(m, "still present after removal")
			continue
		}
		if owner := p.minorOwner[minorKey(bridge, m)]; owner != "" {
			p.endSeries(bridge, owner)
		}
		delete(p.minorOwner, minorKey(bridge, m))
		res.StraysRemoved++
	}
}

type tearItem struct {
	s  shapeplan.Session
	ip net.IP
}

// tearBatch is the teardown of ended sessions: every denial proven in one transaction, then every class removed in one tc batch, then each session's
// durable end recorded. A session whose denial is not proven keeps its class, exactly as before.
func (p *phase3Shaping) tearBatch(ctx context.Context, items []tearItem, stillTorn map[string]bool, res *shapingPlanResponse) {
	uctx, cancel := urgentCtx(ctx)
	defer cancel()
	reqs := make([]gateAddr, len(items))
	for i, it := range items {
		reqs[i] = gateAddr{Bridge: it.s.Bridge, IP: it.ip}
	}
	errs := p.denyBatch(uctx, reqs)
	byBridge := map[string][]int{}
	for i, it := range items {
		if errs[i] != nil {
			continue
		}
		if m, ok := shape.MinorForIP(it.ip); ok {
			byBridge[it.s.Bridge] = append(byBridge[it.s.Bridge], m)
		}
	}
	tcErr := map[string]error{}
	bs, _ := p.shp.(batchShaper)
	for b, minors := range byBridge {
		if bs != nil {
			tcErr[b] = bs.DeleteClassesBatch(uctx, b, minors)
		}
	}
	for i, it := range items {
		s := it.s
		if errs[i] != nil {
			res.Failed++
			res.Problems = append(res.Problems, "tear "+s.SessionID+": PACKET AUTHORIZATION NOT PROVEN REMOVED: "+errs[i].Error())
			continue
		}
		if err := tcErr[s.Bridge]; err != nil {
			// Access is already denied; this is degraded cleanup, still reported because a leftover class distorts accounting.
			res.Failed++
			res.Problems = append(res.Problems, "tear "+s.SessionID+": access denied but tc cleanup failed: "+err.Error())
			continue
		}
		if p.queueEnd(ctx, classKey(s.Bridge, s.SessionID), s.SessionID, s.EndReason) {
			stillTorn[classKey(s.Bridge, s.SessionID)] = true
		}
		if minor, ok := shape.MinorForIP(it.ip); ok {
			delete(p.minorOwner, minorKey(s.Bridge, minor))
		}
		delete(p.classes, classKey(s.Bridge, s.SessionID))
		res.TornDown++
	}
}

// installedMatchesPlan: the class is installed on both devices exactly as the plan asks (PER_DEVICE: directly under the root, rate = ceil = the plan's
// rate). Anything it cannot establish is "no", which re-rates as before.
func (p *phase3Shaping) installedMatchesPlan(ctx context.Context, bridge string, minor int, s shapeplan.Session, groupCid string) bool {
	if groupCid != "" {
		return false
	}
	down, err := p.readClassesForPass(ctx, bridge)
	if err != nil {
		return false
	}
	up, err := p.readClassesForPass(ctx, shape.IFBName(bridge))
	if err != nil {
		return false
	}
	d, ok1 := down[minor]
	u, ok2 := up[minor]
	return ok1 && ok2 && d.Params && u.Params &&
		d.Parent == shape.RootParent && u.Parent == shape.RootParent &&
		d.RateKbit == s.DownKbps && d.CeilKbit == s.DownKbps &&
		u.RateKbit == s.UpKbps && u.CeilKbit == s.UpKbps
}

type workKind int

const (
	workIndividual workKind = iota // changed, unproven, quarantined, SHARED or otherwise not routine: the per-session path
	workAdmit                      // a new session whose slot is free in the kernel: batched admission
	workSteady                     // in force, unchanged: routine
)

func (p *phase3Shaping) classifySession(ctx context.Context, bridge string, minor int, s shapeplan.Session) workKind {
	if s.SpeedAllocation == speedAllocationShared {
		return workIndividual
	}
	key := classKey(bridge, s.SessionID)
	if p.quarantined[key] != nil {
		return workIndividual
	}
	_, active := p.epochs[key]
	_, held := p.classes[key]
	down, err := p.readClassesForPass(ctx, bridge)
	if err != nil {
		return workIndividual
	}
	up, err := p.readClassesForPass(ctx, shape.IFBName(bridge))
	if err != nil {
		return workIndividual
	}
	_, d := down[minor]
	_, u := up[minor]
	switch {
	case active && held:
		if d && u && p.installedMatchesPlan(ctx, bridge, minor, s, "") {
			return workSteady
		}
	case !active && !held:
		if !d && !u {
			return workAdmit
		}
	}
	return workIndividual
}

type batchCand struct {
	minor int
	s     shapeplan.Session
	pc    provCtx
}

// provisionBridgeBatched is step 3 of the pass for one bridge, in priority order.
func (p *phase3Shaping) provisionBridgeBatched(ctx context.Context, bridge string, byMinor map[int]shapeplan.Session, view gateView, now time.Time, res *shapingPlanResponse) {
	minors := sortedIntKeys(byMinor)
	var steady, admit []batchCand
	// 1. Decide every session from memory and the DHCP evidence (no kernel work); the ones that fail closed queue their denial, individual ones run now.
	for idx, minor := range minors {
		if ctx.Err() != nil {
			res.Failed++
			res.Problems = append(res.Problems, fmt.Sprintf("pass budget exhausted on %s: %d sessions were not examined and are retried next pass", bridge, len(minors)-idx))
			break
		}
		s := byMinor[minor]
		pc, ok := p.provisionPrecheck(ctx, bridge, minor, s, view, now, res)
		if !ok {
			continue
		}
		switch p.classifySession(ctx, bridge, minor, s) {
		case workSteady:
			steady = append(steady, batchCand{minor, s, pc})
		case workAdmit:
			admit = append(admit, batchCand{minor, s, pc})
		default:
			p.provisionTail(ctx, bridge, minor, s, view, now, res, pc)
		}
	}
	p.mark("precheck")
	p.count("n_admit", len(admit))
	p.count("n_steady", len(steady))
	// 2. Fail closed first: the sessions whose access must be withdrawn.
	p.flushDenies(ctx, res)
	p.mark("deny_flush")
	// 3. Then keep every unchanged, already-proven session forwarding. This needs nothing but memory and one nft transaction, so it comes BEFORE anything that
	// can wait on the database: in the lab the leases of the long sessions lapsed behind admissions and bookkeeping that were stuck on a lock.
	nowBoot, _ := p.bootNow()
	for _, c := range steady {
		p.steadyLease(ctx, bridge, c, nowBoot, view, now, res)
	}
	p.flushRenewals(ctx, res)
	p.flushDenies(ctx, res)
	p.mark("renew")
	// 4. New sessions.
	p.admitBatch(ctx, bridge, admit, view, now, res)
	p.mark("admit")
	p.flushRenewals(ctx, res)
	p.flushDenies(ctx, res)
	p.mark("urgent_flush")
	// 5. Routine bookkeeping: re-prove the sessions whose proof has aged. Bounded by what is left of the pass; it never delays a lease, which is written above.
	if len(steady) == 0 {
		return
	}
	if dl, ok := ctx.Deadline(); ctx.Err() != nil || (ok && time.Until(dl) < p.routineReserveOrDefault()) {
		slog.Warn("netd: pass budget nearly exhausted; the re-proof of unchanged sessions is deferred to the next pass", "bridge", bridge, "sessions", len(steady))
		p.count("reprove_deferred", len(steady))
		return
	}
	p.reproveSteady(ctx, bridge, steady, view, now, res)
	p.mark("reprove")
	p.flushRenewals(ctx, res)
	p.flushDenies(ctx, res)
	p.mark("routine_flush")
}

// admitBatch admits new sessions in chunks of shape.BatchChunk.
func (p *phase3Shaping) admitBatch(ctx context.Context, bridge string, cands []batchCand, view gateView, now time.Time, res *shapingPlanResponse) {
	for start := 0; start < len(cands); start += shape.BatchChunk {
		if ctx.Err() != nil {
			res.Failed++
			res.Problems = append(res.Problems, fmt.Sprintf("pass budget exhausted on %s: %d new sessions were not admitted and are retried next pass", bridge, len(cands)-start))
			return
		}
		end := start + shape.BatchChunk
		if end > len(cands) {
			end = len(cands)
		}
		p.admitChunk(ctx, bridge, cands[start:end], view, now, res)
		p.flushRenewals(ctx, res)
		p.flushDenies(ctx, res)
	}
}

type liveCand struct {
	batchCand
	epoch int64
}

// admitChunk runs the staged admission of provisionTail for a chunk of new sessions at once. The order is the same (generation, classes without filters,
// origin, filters, verification, write-ahead record, provisional authorization, proof of durable ACTIVE); only the number of processes changes.
func (p *phase3Shaping) admitChunk(ctx context.Context, bridge string, chunk []batchCand, view gateView, now time.Time, res *shapingPlanResponse) {
	bs := p.shp.(batchShaper)
	ifb := shape.IFBName(bridge)
	if p.epochs == nil {
		p.epochs = map[string]int64{}
	}
	if p.pending == nil {
		p.pending = map[string]int64{}
	}
	lv := p.admissionAllocs(ctx, bridge, chunk, res)
	if len(lv) == 0 {
		return
	}
	// A refused batch is not a verdict on any one session: each goes through the per-session path, which clears its slot first.
	fallback := func(why string, items []liveCand) {
		slog.Warn("netd: batched admission refused; admitting these sessions one by one", "bridge", bridge, "sessions", len(items), "why", why)
		for _, l := range items {
			p.provisionTail(ctx, bridge, l.minor, l.s, view, now, res, l.pc)
		}
	}
	ips := func(items []liveCand) []net.IP {
		out := make([]net.IP, len(items))
		for i, l := range items {
			out[i] = l.pc.ip
		}
		return out
	}

	prep := make([]shape.PrepItem, len(lv))
	for i, l := range lv {
		prep[i] = shape.PrepItem{IP: l.pc.ip, DownKbps: l.s.DownKbps, UpKbps: l.s.UpKbps}
	}
	if err := bs.PrepareBatch(ctx, bridge, prep); err != nil {
		fallback(err.Error(), lv)
		return
	}

	// The origin is recorded before any forwarding filter exists so the first packet counts from a real baseline.
	down, derr := p.shp.ReadClasses(ctx, bridge)
	up, uerr := p.shp.ReadClasses(ctx, ifb)
	var reg []liveCand
	if derr != nil || uerr != nil {
		for _, l := range lv {
			p.failClosed(ctx, bridge, l.pc.ip, res, "class origin not registered for "+l.s.SessionID+": counters unreadable at creation")
		}
	} else {
		reg = p.admissionOrigins(ctx, bridge, lv, up, down, res)
	}
	if len(reg) == 0 {
		return
	}

	// Metering only; the nft gate below is what admits the guest.
	if err := bs.ActivateBatch(ctx, bridge, ips(reg)); err != nil {
		fallback(err.Error(), reg)
		return
	}
	// Both classes were read present moments ago, after they were created; installing a filter cannot remove a class, so that read stands for the
	// class half of the post-activation verification (it costs a full class dump per device, which grows with the number of sessions). The filter half
	// is read afresh, for both directions, here.
	fw, ferr := bs.ForwardingBatch(ctx, bridge, ips(reg))
	var verified []liveCand
	for _, l := range reg {
		_, d := down[l.minor]
		_, u := up[l.minor]
		if ferr != nil || !fw[l.pc.ip.String()] || !d || !u {
			p.failClosed(ctx, bridge, l.pc.ip, res, "post-activation verification failed for "+l.s.SessionID)
			continue
		}
		verified = append(verified, l)
	}

	// The bound is measured on the boot clock read now, not on the one read when the session was examined.
	nowBoot, cerr := p.bootNow()
	if cerr != nil {
		for _, l := range verified {
			p.failClosed(ctx, bridge, l.pc.ip, res, "shape "+l.s.SessionID+": the monotonic security clock is unreadable ("+cerr.Error()+
				"); the activation bound cannot be measured, so no provisional access is granted")
		}
		return
	}
	if p.gate != nil {
		var authd []liveCand
		var reqs []gateLease
		for _, l := range verified {
			// Write-ahead: the attempt is fsynced before the element goes in (see phase3_journal.go).
			if problem := p.beginAttempt(ctx, l.pc.key, l.s.SessionID, nowBoot); problem != "" {
				p.failClosed(ctx, bridge, l.pc.ip, res, "authorize "+l.s.SessionID+": "+problem)
				continue
			}
			authd = append(authd, l)
			reqs = append(reqs, gateLease{Bridge: bridge, IP: l.pc.ip, TTL: provisionalOrLess(l.pc.lease)})
		}
		verified = verified[:0]
		for i, err := range p.authorizeBatch(ctx, reqs) {
			if err != nil {
				p.failClosed(ctx, bridge, authd[i].pc.ip, res, "authorize "+authd[i].s.SessionID+": "+err.Error())
				continue
			}
			verified = append(verified, authd[i])
		}
	}
	// The promotions are asked of the database a few at a time; completeAdmission consumes the answers in order.
	asks := make([]activationAsk, 0, len(verified))
	for _, l := range verified {
		asks = append(asks, activationAsk{key: l.pc.key, sessionID: l.s.SessionID, bridge: bridge, minor: l.minor, epoch: l.epoch})
	}
	p.prefetchActivations(ctx, asks)
	for _, l := range verified {
		l.pc.nowBoot = nowBoot
		p.completeAdmission(ctx, bridge, l.minor, l.s, view, now, res, l.pc, l.epoch)
	}
}
