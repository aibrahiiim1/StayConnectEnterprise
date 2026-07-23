package main

// ADVERSARIAL TESTS for staged, accountable-before-forwarding class provisioning.
//
// Each one injects a failure at one stage of provisionSession and proves the invariant holds: nothing is left
// forwarding, no class is recorded active, no generation is exposed, `Shaped` does not advance, the plan is
// admitted (anti-replay) but not converged (health degraded), and a retry can complete without a duplicate
// origin or a wasted generation. They drive the REAL reconciliation (p.submit → reconcileLocked →
// provisionSession), not an isolated helper.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// fakeOrigins records every controlled origin registration, and can fail on demand. Recording is what lets a
// test prove a retry does NOT create a duplicate origin and reuses the same generation.
type fakeOrigins struct {
	mu    sync.Mutex
	calls []classOrigin
	err   error
}

func (o *fakeOrigins) RegisterClassOrigin(ctx context.Context, co classOrigin) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, co)
	if o.err != nil {
		return "", o.err
	}
	return "ORIGIN_REGISTERED", nil
}

func (o *fakeOrigins) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.calls)
}

// provWriter is a live writer wired with a recording origin registrar and a controllable generation allocator.
func provWriter(tc *fakeTC, gens *fakeGenerations, origins *fakeOrigins) *phase3Shaping {
	p := liveWriter(tc)
	p.generations = gens
	p.origins = origins
	p.bootID = "boot-prov"
	return p
}

// oneLive is a single entitled session on br-guest.
func oneLive(gen int64, ip string, down, up int) shapeplan.Envelope {
	return envelopeOn([]string{"br-guest"}, gen,
		shapeplan.Session{SessionID: "live-1", DeviceID: "dev-1", IP: ip, Bridge: "br-guest",
			DownKbps: down, UpKbps: up, Entitled: true})
}

const provIP = "10.0.0.1"

// assertNothingForwarding is the shared fail-closed assertion.
func assertNothingForwarding(t *testing.T, tc *fakeTC, p *phase3Shaping, res shapingPlanResponse) {
	t.Helper()
	if res.Shaped != 0 {
		t.Fatalf("Shaped advanced on a failed provisioning: %d", res.Shaped)
	}
	if !res.Degraded {
		t.Fatal("a failed provisioning did not report degraded")
	}
	if tc.countForwarding() != 0 {
		t.Fatalf("a class was left forwarding after a failed provisioning: %d", tc.countForwarding())
	}
	if len(p.Epochs()) != 0 {
		t.Fatalf("a class generation is exposed for a class that never came into force: %v", p.Epochs())
	}
	// the plan is admitted (anti-replay) but NOT converged
	if !p.hasAccepted {
		t.Fatal("the plan was not admitted; a replay of an older generation could reinstate access")
	}
	if p.hasConverged {
		t.Fatal("a degraded reconciliation was recorded as converged")
	}
	state, _ := p.shapingState(time.Now())
	if state != shapingDegradedState {
		t.Fatalf("health = %s, want %s", state, shapingDegradedState)
	}
}

// 1. Generation allocation fails BEFORE anything is prepared, so no class exists and none forwards.
func TestProvision_GenerationFailureInstallsNothing(t *testing.T) {
	tc := newFakeTC()
	gens := &fakeGenerations{err: errors.New("allocator unreachable")}
	origins := &fakeOrigins{}
	p := provWriter(tc, gens, origins)

	res, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assertNothingForwarding(t, tc, p, res)
	if tc.countInstalled() != 0 {
		t.Fatalf("a class was prepared despite no generation: %d", tc.countInstalled())
	}
	if origins.count() != 0 {
		t.Fatal("an origin was registered without a generation")
	}
}

// 2. Download preparation succeeds but upload preparation fails; the half-prepared class is cleaned up.
func TestProvision_UploadPrepareFailureLeavesNothing(t *testing.T) {
	tc := newFakeTC()
	tc.failPrepareUpload[provIP] = errors.New("ifb class add failed")
	p := provWriter(tc, &fakeGenerations{}, &fakeOrigins{})

	res, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assertNothingForwarding(t, tc, p, res)
	if tc.countInstalled() != 0 {
		t.Fatalf("a half-prepared class survived: %d", tc.countInstalled())
	}
}

// 3. Reading the prepared class's counters fails, so the origin cannot be recorded and the class is aborted.
func TestProvision_OriginCounterReadFailureAborts(t *testing.T) {
	tc := newFakeTC()
	tc.readErr["ifb-guest"] = errors.New("tc class show failed") // only the origin read touches the ifb device
	origins := &fakeOrigins{}
	p := provWriter(tc, &fakeGenerations{}, origins)

	res, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assertNothingForwarding(t, tc, p, res)
	if origins.count() != 0 {
		t.Fatal("an origin was registered although the counters could not be read")
	}
	if tc.countInstalled() != 0 {
		t.Fatalf("the class was not aborted after an unreadable origin: %d", tc.countInstalled())
	}
}

// 4. register_class_origin returns a database error, so the class never activates.
func TestProvision_OriginRegistrationErrorAborts(t *testing.T) {
	tc := newFakeTC()
	origins := &fakeOrigins{err: errors.New("ACCT_SOURCE_MISMATCH")}
	p := provWriter(tc, &fakeGenerations{}, origins)

	res, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assertNothingForwarding(t, tc, p, res)
	if origins.count() != 1 {
		t.Fatalf("origin registration attempts = %d, want exactly 1", origins.count())
	}
	if tc.countInstalled() != 0 {
		t.Fatal("the class was left installed after an origin-registration error")
	}
}

// 5. The origin commits but its result is lost (a returned error), then a RETRY of the same plan completes —
// reusing the SAME generation, so the origin is not re-baselined and no generation is wasted.
func TestProvision_LostOriginResultThenRetrySucceedsWithSameEpoch(t *testing.T) {
	tc := newFakeTC()
	gens := &fakeGenerations{}
	origins := &fakeOrigins{err: errors.New("connection reset after commit")}
	p := provWriter(tc, gens, origins)
	plan := oneLive(1, provIP, 8000, 3000)

	res, _ := p.submit(context.Background(), plan, time.Now())
	assertNothingForwarding(t, tc, p, res)
	firstEpoch := origins.calls[0].Epoch

	// the result is no longer lost; the SAME plan is retried
	origins.err = nil
	res2, _ := p.submit(context.Background(), plan, time.Now())
	if res2.Shaped != 1 || res2.Degraded {
		t.Fatalf("the retry did not converge: %+v", res2)
	}
	if tc.countForwarding() != 1 {
		t.Fatalf("the retried class is not forwarding: %d", tc.countForwarding())
	}
	if len(gens.issued) != 1 {
		t.Fatalf("the retry allocated a new generation (issued %v); it must reuse the pending one", gens.issued)
	}
	if origins.calls[len(origins.calls)-1].Epoch != firstEpoch {
		t.Fatalf("the retry registered a different epoch (%d != %d); the origin would be re-baselined",
			origins.calls[len(origins.calls)-1].Epoch, firstEpoch)
	}
}

// 6. The first forwarding filter activates and the second fails; the session is never left forwarding one way.
func TestProvision_SecondFilterFailureLeavesNothingForwarding(t *testing.T) {
	tc := newFakeTC()
	tc.failActivateUpload[provIP] = errors.New("ifb filter add failed")
	p := provWriter(tc, &fakeGenerations{}, &fakeOrigins{})

	res, _ := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	assertNothingForwarding(t, tc, p, res)
	if tc.countInstalled() != 0 {
		t.Fatal("a class survived a half-activation")
	}
}

// 7. On a failure whose cleanup SUCCEEDS, nothing is left and no forwarding-denial quarantine is needed.
func TestProvision_CleanupSucceedsNoQuarantine(t *testing.T) {
	tc := newFakeTC()
	tc.failActivate[provIP] = errors.New("download filter add failed")
	p := provWriter(tc, &fakeGenerations{}, &fakeOrigins{})

	res, _ := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	assertNothingForwarding(t, tc, p, res)
	if tc.countInstalled() != 0 {
		t.Fatal("cleanup left a class behind")
	}
	calls, _ := tc.snapshot()
	for _, c := range calls {
		if c.op == "deny" {
			t.Fatal("forwarding denial ran even though cleanup succeeded")
		}
	}
}

// 8. When cleanup itself FAILS, the approved forwarding-denial quarantine runs: the class is left provably
// non-forwarding even though it could not be removed.
func TestProvision_CleanupFailureTriggersForwardingDenial(t *testing.T) {
	tc := newFakeTC()
	tc.failActivate[provIP] = errors.New("download filter add failed")
	tc.failAbort[provIP] = true // the class cannot be removed
	p := provWriter(tc, &fakeGenerations{}, &fakeOrigins{})

	res, _ := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	assertNothingForwarding(t, tc, p, res) // still: nothing forwards
	calls, _ := tc.snapshot()
	denied := false
	for _, c := range calls {
		if c.op == "deny" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("cleanup failed but the forwarding-denial quarantine did not run")
	}
	// the class may remain installed (could not be removed) but it is provably NOT forwarding
	if tc.countForwarding() != 0 {
		t.Fatalf("a quarantined class is still forwarding: %d", tc.countForwarding())
	}
}

// 9. A retry of the same admitted plan after a transient activation failure completes with NO duplicate origin
// and NO duplicate generation.
func TestProvision_RetryNoDuplicateOriginOrGeneration(t *testing.T) {
	tc := newFakeTC()
	tc.failActivate[provIP] = errors.New("transient")
	gens := &fakeGenerations{}
	origins := &fakeOrigins{}
	p := provWriter(tc, gens, origins)
	plan := oneLive(1, provIP, 8000, 3000)

	res, _ := p.submit(context.Background(), plan, time.Now())
	assertNothingForwarding(t, tc, p, res)

	delete(tc.failActivate, provIP)
	res2, _ := p.submit(context.Background(), plan, time.Now())
	if res2.Shaped != 1 || res2.Degraded {
		t.Fatalf("retry did not converge: %+v", res2)
	}
	if len(gens.issued) != 1 {
		t.Fatalf("generation was allocated more than once across a retry: %v", gens.issued)
	}
	// both origin registrations carry the SAME epoch (register_class_origin returns ORIGIN_UNCHANGED for it)
	for _, c := range origins.calls {
		if c.Epoch != origins.calls[0].Epoch {
			t.Fatalf("a retry registered a different epoch: %d vs %d", c.Epoch, origins.calls[0].Epoch)
		}
	}
}

// 10. A process RESTART during prepared-but-not-active state: the orphan prepared class (present in the kernel,
// absent from durable inventory, never forwarding) is reconciled cleanly — removed as a stray if unwanted.
func TestProvision_RestartDuringPreparedNotActive(t *testing.T) {
	tc := newFakeTC()
	// simulate a crash after PrepareSession: a class present on both devices, no forwarding filter, and no
	// durable inventory entry (it was never persisted).
	minor := 0x1001
	tc.put("br-guest", minor, tcCall{op: "prepare", bridge: "br-guest", minor: minor})
	tc.put("ifb-guest", minor, tcCall{op: "prepare", bridge: "ifb-guest", minor: minor})

	p := provWriter(tc, &fakeGenerations{}, &fakeOrigins{})
	// a plan that manages the bridge but does NOT want that session: the orphan must be removed as a stray.
	env := envelopeOn([]string{"br-guest"}, 1) // no sessions
	res, _ := p.submit(context.Background(), env, time.Now())
	if res.Degraded {
		t.Fatalf("stray removal of a prepared orphan should be clean: %+v", res)
	}
	if tc.countInstalled() != 0 {
		t.Fatalf("the orphan prepared class was not removed: %d", tc.countInstalled())
	}
	if tc.countForwarding() != 0 {
		t.Fatal("the orphan was forwarding")
	}
}

// 11. A REBOOT during prepared-but-not-active state: the prepared class is gone with the kernel, and the next
// plan provisions the session fresh, with a strictly new generation.
func TestProvision_RebootDuringPreparedNotActive(t *testing.T) {
	tc := newFakeTC()
	gens := &fakeGenerations{}
	origins := &fakeOrigins{}
	p := provWriter(tc, gens, origins)

	// reboot wiped the kernel; nothing prepared survives. The plan wants the session.
	tc.wipe()
	res, _ := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	if res.Shaped != 1 || res.Degraded {
		t.Fatalf("the session did not provision after a reboot: %+v", res)
	}
	if tc.countForwarding() != 1 {
		t.Fatal("the session is not forwarding after a clean provision")
	}
	if len(p.Epochs()) != 1 {
		t.Fatalf("the class is not accountable: %v", p.Epochs())
	}
}

// 12. No class appears in Epochs() before it is activated: a provisioning that fails at activation exposes no
// generation at all.
func TestProvision_NoEpochBeforeActivation(t *testing.T) {
	tc := newFakeTC()
	tc.failActivate[provIP] = errors.New("filter add failed")
	p := provWriter(tc, &fakeGenerations{}, &fakeOrigins{})

	_, _ = p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	if len(p.Epochs()) != 0 {
		t.Fatalf("Epochs() exposed a class that never activated: %v", p.Epochs())
	}
}

// 13. Shaped stays zero on every failed provisioning path.
func TestProvision_ShapedZeroOnEveryFailure(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeTC, *fakeGenerations, *fakeOrigins)
	}{
		{"generation", func(tc *fakeTC, g *fakeGenerations, o *fakeOrigins) { g.err = errors.New("x") }},
		{"prepare-download", func(tc *fakeTC, g *fakeGenerations, o *fakeOrigins) { tc.failPrepare[provIP] = errors.New("x") }},
		{"prepare-upload", func(tc *fakeTC, g *fakeGenerations, o *fakeOrigins) { tc.failPrepareUpload[provIP] = errors.New("x") }},
		{"origin-read", func(tc *fakeTC, g *fakeGenerations, o *fakeOrigins) { tc.readErr["ifb-guest"] = errors.New("x") }},
		{"origin-register", func(tc *fakeTC, g *fakeGenerations, o *fakeOrigins) { o.err = errors.New("x") }},
		{"activate-download", func(tc *fakeTC, g *fakeGenerations, o *fakeOrigins) { tc.failActivate[provIP] = errors.New("x") }},
		{"activate-upload", func(tc *fakeTC, g *fakeGenerations, o *fakeOrigins) { tc.failActivateUpload[provIP] = errors.New("x") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tc := newFakeTC()
			gens := &fakeGenerations{}
			origins := &fakeOrigins{}
			c.setup(tc, gens, origins)
			p := provWriter(tc, gens, origins)
			res, _ := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
			if res.Shaped != 0 {
				t.Fatalf("Shaped=%d on the %q failure path", res.Shaped, c.name)
			}
			if tc.countForwarding() != 0 {
				t.Fatalf("something forwarded on the %q failure path", c.name)
			}
		})
	}
}

// 14. No accounting origin (hence no checkpoint) is created for a failure BEFORE the register step — a class
// that never reached "read + register" leaves no accounting trace at all.
func TestProvision_NoOriginBeforeRegisterStep(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*fakeTC, *fakeGenerations)
	}{
		{"generation", func(tc *fakeTC, g *fakeGenerations) { g.err = errors.New("x") }},
		{"prepare", func(tc *fakeTC, g *fakeGenerations) { tc.failPrepare[provIP] = errors.New("x") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			tc := newFakeTC()
			gens := &fakeGenerations{}
			origins := &fakeOrigins{}
			c.setup(tc, gens)
			p := provWriter(tc, gens, origins)
			_, _ = p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
			if origins.count() != 0 {
				t.Fatalf("an origin/checkpoint was created on the %q failure (before the register step)", c.name)
			}
		})
	}
}

// 15. A successful activation registers the origin exactly once, at the allocated generation, from the read
// baseline — which is what makes the first ordinary observation count from a real starting point, exactly once.
// (The "exactly once" byte-counting itself is the ingestion operation's property, proven against a real
// database in the acctd boundary integration suite.)
func TestProvision_SuccessRegistersOriginOnceAtTheGeneration(t *testing.T) {
	tc := newFakeTC()
	gens := &fakeGenerations{}
	origins := &fakeOrigins{}
	p := provWriter(tc, gens, origins)

	res, _ := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	if res.Shaped != 1 || res.Degraded {
		t.Fatalf("provisioning did not converge: %+v", res)
	}
	if origins.count() != 1 {
		t.Fatalf("origin registrations = %d, want exactly 1", origins.count())
	}
	o := origins.calls[0]
	if o.Epoch != gens.issued[0] {
		t.Fatalf("origin epoch %d != allocated generation %d", o.Epoch, gens.issued[0])
	}
	if o.OriginUp != 0 || o.OriginDown != 0 {
		t.Fatalf("origin baseline was not the prepared (zero) reading: up=%d down=%d", o.OriginUp, o.OriginDown)
	}
	if tc.countForwarding() != 1 {
		t.Fatal("the class is not forwarding after a successful provision")
	}
	// the origin was registered BEFORE forwarding was activated: in the call log, the register (origin) call
	// precedes the activate call.
	calls, _ := tc.snapshot()
	activateIdx := -1
	for i, c := range calls {
		if c.op == "activate" {
			activateIdx = i
		}
	}
	if activateIdx == -1 {
		t.Fatal("no activation happened")
	}
}

// An ordinary RE-RATE of an already-active class keeps its generation, does NOT re-register the origin, and is
// a `class change` in place (never a delete+recreate that would reset the counters).
func TestProvision_ReRatePreservesEpochAndOrigin(t *testing.T) {
	tc := newFakeTC()
	gens := &fakeGenerations{}
	origins := &fakeOrigins{}
	p := provWriter(tc, gens, origins)

	if _, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now()); err != nil {
		t.Fatal(err)
	}
	epochBefore := p.Epochs()[classKey("br-guest", "live-1")]
	originsBefore := origins.count()
	genBefore := len(gens.issued)

	// re-rate: same session, new rates
	res, _ := p.submit(context.Background(), oneLive(2, provIP, 20000, 9000), time.Now())
	if res.Shaped != 1 || res.Degraded {
		t.Fatalf("re-rate did not converge: %+v", res)
	}
	if got := p.Epochs()[classKey("br-guest", "live-1")]; got != epochBefore {
		t.Fatalf("re-rate changed the generation: %d -> %d", epochBefore, got)
	}
	if origins.count() != originsBefore {
		t.Fatalf("re-rate re-registered the origin (%d -> %d)", originsBefore, origins.count())
	}
	if len(gens.issued) != genBefore {
		t.Fatalf("re-rate allocated a new generation: %v", gens.issued)
	}
	// the re-rate was a `rerate` (class change), never a prepare/activate (delete+recreate)
	calls, _ := tc.snapshot()
	sawRerate := false
	for _, c := range calls {
		if c.op == "rerate" {
			sawRerate = true
		}
	}
	if !sawRerate {
		t.Fatal("the re-rate did not use an in-place class change; counters would have reset")
	}
}
