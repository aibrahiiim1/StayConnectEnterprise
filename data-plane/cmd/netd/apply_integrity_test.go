package main

// The apply / rollback pipeline's integrity (audit Phase 8: M6, M7, M10). Every test drives the real applier against a recording fake kernel and
// a fake revision store, and asserts on what reached the wire and what was recorded, not on internal calls.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// ---- a fake revision store ---------------------------------------------------------------------------------------------

type fakeStore struct {
	mu sync.Mutex

	intent    []netcfg.GuestNetwork // guest_networks rows (LoadIntent)
	seq       int64
	state     map[string]string // revision id -> state
	reasons   map[string]string // revision id -> failure/rollback reason
	events    []string
	healthLog []string

	inFlightID, inFlightState string
	applying                  []string

	activeID     string
	activeBundle string
	activeIntent []netcfg.GuestNetwork

	recorded map[string]recordedBridge

	markApplyingErr error
	markPendingErr  error
	stateReadErr    error
	resolveErr      error
	intents         map[string][]netcfg.GuestNetwork // the intent each revision was created with
}

func newFakeStore(intent []netcfg.GuestNetwork) *fakeStore {
	return &fakeStore{intent: intent, state: map[string]string{}, reasons: map[string]string{}}
}

func (f *fakeStore) Event(_ context.Context, revID, phase string, ok bool, detail map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, fmt.Sprintf("%s/%s=%v %v", revID, phase, ok, detail))
}
func (f *fakeStore) Health(_ context.Context, revID, name string, ok bool, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthLog = append(f.healthLog, fmt.Sprintf("%s/%s=%v", revID, name, ok))
}
func (f *fakeStore) LoadIntent(context.Context) ([]netcfg.GuestNetwork, error) { return f.intent, nil }
func (f *fakeStore) CreateRevision(_ context.Context, _ string, intent []netcfg.GuestNetwork, _ string) (string, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("rev-%d", f.seq)
	f.state[id] = "draft"
	if f.intents == nil {
		f.intents = map[string][]netcfg.GuestNetwork{}
	}
	f.intents[id] = intent
	return id, f.seq, nil
}
func (f *fakeStore) SetRevisionValidation(_ context.Context, id string, _ netcfg.ValidationResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[id] = "validated"
	return nil
}
func (f *fakeStore) SetRevisionState(_ context.Context, id, state string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[id] = state
	return nil
}
func (f *fakeStore) MarkApplying(_ context.Context, id, _, _ string, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markApplyingErr != nil {
		return f.markApplyingErr
	}
	f.state[id] = "applying"
	return nil
}
func (f *fakeStore) MarkPending(_ context.Context, id string, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markPendingErr != nil {
		return f.markPendingErr
	}
	f.state[id] = "pending_confirmation"
	return nil
}
func (f *fakeStore) MarkActive(_ context.Context, id, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state[id] != "pending_confirmation" {
		return errNotPending
	}
	f.state[id] = "active"
	f.activeID = id
	return nil
}
func (f *fakeStore) MarkFailed(_ context.Context, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[id], f.reasons[id] = "failed", reason
	return nil
}
func (f *fakeStore) MarkRolledBack(_ context.Context, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.state[id]; s != "applying" && s != "pending_confirmation" {
		return errNotRollbackable
	}
	f.state[id], f.reasons[id] = "rolled_back", reason
	return nil
}
func (f *fakeStore) RevisionState(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stateReadErr != nil {
		return "", f.stateReadErr
	}
	return f.state[id], nil
}
func (f *fakeStore) RevisionIntent(_ context.Context, id string) ([]netcfg.GuestNetwork, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.intents[id], nil
}
func (f *fakeStore) CurrentActiveIntent(context.Context) (string, string, []netcfg.GuestNetwork, error) {
	if f.activeID == "" {
		return "", "", nil, ErrNoConfirmedActiveRevision
	}
	return f.activeID, f.activeBundle, f.activeIntent, nil
}
func (f *fakeStore) ActiveBundlePath(context.Context, string) (string, error) {
	return f.activeBundle, nil
}
func (f *fakeStore) ActiveIntent(context.Context, string) ([]netcfg.GuestNetwork, error) {
	return f.activeIntent, nil
}
func (f *fakeStore) InFlightRevision(context.Context) (string, string, error) {
	return f.inFlightID, f.inFlightState, nil
}
func (f *fakeStore) IncompleteRollback(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, r := range f.reasons {
		if f.state[id] == "rolled_back" && strings.Contains(r, "ROLLBACK INCOMPLETE") {
			return id, nil
		}
	}
	return "", nil
}
func (f *fakeStore) ResolveIncompleteRollbacks(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resolveErr != nil {
		return f.resolveErr
	}
	for id, r := range f.reasons {
		f.reasons[id] = strings.ReplaceAll(r, "ROLLBACK INCOMPLETE", "rollback incomplete, since resolved")
	}
	return nil
}
func (f *fakeStore) ApplyingRevisions(context.Context) ([]string, error) { return f.applying, nil }
func (f *fakeStore) RecordedBridges(context.Context) (map[string]recordedBridge, error) {
	return f.recorded, nil
}
func (f *fakeStore) LoadPortalIP(context.Context) (string, error) { return "", nil }
func (f *fakeStore) LoadPortalTLS(context.Context) (netcfg.PortalTLS, error) {
	return netcfg.PortalTLS{}, nil
}
func (f *fakeStore) KnownIfaceSet(context.Context) (map[string]bool, error) {
	return map[string]bool{"ens192": true}, nil
}
func (f *fakeStore) SyncInterfaces(context.Context, []Interface, string, string) error { return nil }

func (f *fakeStore) eventsJoined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.events, " | ")
}

// ---- the applier under test ----------------------------------------------------------------------------------------------

func oldIntent() []netcfg.GuestNetwork { return localIntent() }

// newIntent differs from oldIntent in everything the firewall renders.
func newIntent() []netcfg.GuestNetwork {
	n := localIntent()
	n[0].Name, n[0].VLANID, n[0].BridgeName = "guest91", 91, "br-g91"
	n[0].GatewayIP, n[0].SubnetCIDR = "10.21.0.1", "10.21.0.0/22"
	n[0].Pools = []netcfg.Pool{{StartIP: "10.21.0.100", EndIP: "10.21.3.200"}}
	return n
}

func applyFixture(t *testing.T, k *fakeKernel, st *fakeStore) *applier {
	t.Helper()
	a := newTestApplier(t, k)
	a.st = st
	a.confirmWindow = 2 * time.Minute
	a.netplanFile = filepath.Join(t.TempDir(), "50-stayconnect-guest.yaml")
	a.unboundFrag = filepath.Join(t.TempDir(), "stayconnect-guest.conf")
	a.keaConfFile = filepath.Join(t.TempDir(), "kea", "kea-dhcp4.conf")
	a.discoverFn = func(context.Context) ([]Interface, error) {
		return []Interface{{Name: "ens192"}, {Name: "ens160"}}, nil
	}
	a.portalIPFn = func(context.Context) (string, error) { return "", nil }
	a.portalTLSFn = func(context.Context) (netcfg.PortalTLS, error) { return netcfg.PortalTLS{}, nil }
	a.keaRTSeam = &fakeKea{}
	return a
}

func fpOf(a *applier, intent []netcfg.GuestNetwork) string {
	return netcfg.RenderFingerprint(intent, a.renderTopo())
}

// loadedFingerprints returns every ruleset fingerprint the fake kernel has been given, in order.
func (k *fakeKernel) loadedFingerprints() []string { return k.loaded }

// ---- M10: a rejected apply never becomes the running firewall ---------------------------------------------------------------

// A second change is refused before it publishes anything, so the firewall tick keeps rendering the intent that is awaiting confirmation.
func TestApplyWhileAnotherChangeIsAwaitingConfirmationChangesNothing(t *testing.T) {
	k := newFakeKernel(t)
	st := newFakeStore(newIntent())
	st.inFlightID, st.inFlightState = "rev-A", "pending_confirmation"
	a := applyFixture(t, k, st)
	a.setInForce(oldIntent(), "")

	_, err := a.Apply(context.Background(), "second change", "tester")
	if !errors.Is(err, errChangeInFlight) {
		t.Fatalf("a change was started on top of one awaiting confirmation: %v", err)
	}
	if len(st.state) != 0 {
		t.Fatalf("the refused change left revision rows behind: %v", st.state)
	}
	k.reset()
	a.reassertNftStructure(context.Background(), "periodic")
	if got, want := k.fp, fpOf(a, oldIntent()); got != want {
		t.Fatalf("the firewall tick installed fingerprint %q; the intent awaiting confirmation renders %q", got, want)
	}
	for _, fp := range k.loadedFingerprints() {
		if fp == fpOf(a, newIntent()) {
			t.Fatal("the refused change's ruleset reached the kernel")
		}
	}
}

// The in-flight slot being unreadable is not permission to publish a new intent.
func TestApplyRefusesWhenTheInFlightSlotCannotBeRead(t *testing.T) {
	k := newFakeKernel(t)
	a := applyFixture(t, k, newFakeStore(newIntent()))
	a.inFlightFn = func(context.Context) (string, string, error) { return "", "", errors.New("connection reset") }
	a.setInForce(oldIntent(), "")
	if _, err := a.Apply(context.Background(), "x", "tester"); err == nil {
		t.Fatal("an apply proceeded without knowing whether another change was in flight")
	}
	if _, _, known := a.inForce(); !known {
		t.Fatal("the intent in force was cleared")
	}
	if got, _, _ := a.inForce(); len(got) != 1 || got[0].BridgeName != "br-g90" {
		t.Fatalf("the intent in force changed to %+v", got)
	}
}

// The database refusing the in-flight slot (the unique index) must not leave the rejected intent in force for the firewall tick to install.
func TestRejectedApplyIsNeverInstalledByTheNextFirewallTick(t *testing.T) {
	k := newFakeKernel(t)
	st := newFakeStore(newIntent())
	st.markApplyingErr = errors.New(`duplicate key value violates unique constraint "ncr_single_inflight"`)
	a := applyFixture(t, k, st)
	a.setInForce(oldIntent(), "")
	converge := func() {
		if _, err := a.ensureNftStructure(context.Background(), oldIntent(), "boot"); err != nil {
			t.Fatal(err)
		}
	}
	converge()

	_, err := a.Apply(context.Background(), "doomed", "tester")
	if err == nil {
		t.Fatal("an apply the database refused reported success")
	}
	if got, _, _ := a.inForce(); len(got) != 1 || got[0].BridgeName != "br-g90" {
		t.Fatalf("the rejected intent stayed in force: %+v", got)
	}
	for i := 0; i < 3; i++ {
		a.reassertNftStructure(context.Background(), "periodic")
	}
	for _, fp := range k.loadedFingerprints() {
		if fp == fpOf(a, newIntent()) {
			t.Fatal("the firewall tick installed the rejected apply's ruleset")
		}
	}
	if k.fp != fpOf(a, oldIntent()) {
		t.Fatalf("the live ruleset is %q, not the one in force before the apply (%q)", k.fp, fpOf(a, oldIntent()))
	}
	if st.state["rev-1"] != "failed" {
		t.Fatalf("the refused revision is %q, want failed", st.state["rev-1"])
	}
}

// The race itself: while an apply is between publishing its intent and proving it, a firewall tick must not render it.
func TestFirewallTickNeverRendersAnIntentAnApplyHasNotProven(t *testing.T) {
	k := newFakeKernel(t)
	st := newFakeStore(newIntent())
	st.markApplyingErr = errors.New("unique violation")
	a := applyFixture(t, k, st)
	a.setInForce(oldIntent(), "")
	if _, err := a.ensureNftStructure(context.Background(), oldIntent(), "boot"); err != nil {
		t.Fatal(err)
	}

	entered, release := make(chan struct{}), make(chan struct{})
	inner := a.runFn
	a.runFn = func(ctx context.Context, name string, args ...string) error {
		if name == "nft" && len(args) > 0 && args[0] == "-c" {
			close(entered)
			<-release // the apply is mid-flight, its intent published
		}
		return inner(ctx, name, args...)
	}
	done := make(chan error, 1)
	go func() { _, err := a.Apply(context.Background(), "slow", "tester"); done <- err }()
	<-entered

	// Hammer the tick while the apply holds its intent in force.
	for i := 0; i < 50; i++ {
		a.reassertNftStructure(context.Background(), "periodic")
	}
	for _, fp := range k.loadedFingerprints() {
		if fp == fpOf(a, newIntent()) {
			t.Fatal("a firewall tick installed an intent the apply had published but not proven")
		}
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("the doomed apply reported success")
	}
	a.reassertNftStructure(context.Background(), "periodic")
	if k.fp != fpOf(a, oldIntent()) {
		t.Fatalf("after the apply failed the firewall is %q, want the previous %q", k.fp, fpOf(a, oldIntent()))
	}
}

// Two applies cannot overlap in one process.
func TestConcurrentApplyIsRefusedAsBusy(t *testing.T) {
	k := newFakeKernel(t)
	st := newFakeStore(newIntent())
	st.markApplyingErr = errors.New("unique violation")
	a := applyFixture(t, k, st)
	a.setInForce(oldIntent(), "")
	entered, release := make(chan struct{}), make(chan struct{})
	inner := a.runFn
	a.runFn = func(ctx context.Context, name string, args ...string) error {
		if name == "nft" && len(args) > 0 && args[0] == "-c" {
			close(entered)
			<-release
		}
		return inner(ctx, name, args...)
	}
	done := make(chan struct{})
	go func() { _, _ = a.Apply(context.Background(), "first", "tester"); close(done) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := a.Apply(ctx, "second", "tester"); !errors.Is(err, errApplyBusy) {
		t.Fatalf("a second apply ran concurrently with the first: %v", err)
	}
	close(release)
	<-done
}

// If the change reached the wire but cannot be recorded as awaiting confirmation, nothing could ever confirm or expire it: it is rolled back.
func TestApplyThatCannotBeRecordedAsPendingIsRolledBack(t *testing.T) {
	k := newFakeKernel(t)
	st := newFakeStore(newIntent())
	st.markPendingErr = errors.New("connection lost")
	a := applyFixture(t, k, st)
	a.dryRun = true
	a.setInForce(oldIntent(), "")

	_, err := a.Apply(context.Background(), "unrecordable", "tester")
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("an unrecordable apply was not rolled back: %v", err)
	}
	if st.state["rev-1"] != "rolled_back" {
		t.Fatalf("the revision is %q, want rolled_back", st.state["rev-1"])
	}
}

// ---- M7: a rollback that did not finish is never reported as one that did -----------------------------------------------------

// rollbackFixture is a non-dry-run applier with a confirmed previous revision (oldIntent, bundle on disk), failing whatever failAt names.
func rollbackFixture(t *testing.T, failAt ...string) (*applier, *fakeKernel, *fakeStore, *fakeKea) {
	t.Helper()
	k := newFakeKernel(t)
	st := newFakeStore(newIntent())
	a := applyFixture(t, k, st)
	fk := &fakeKea{}
	a.keaRTSeam = fk

	bundle := t.TempDir()
	prev := oldIntent()
	topo := a.renderTopo()
	files := map[string][]byte{
		"stayconnect.nft":           netcfg.RenderNftables(prev, topo),
		"50-stayconnect-guest.yaml": netcfg.RenderNetplan(prev),
		"stayconnect-guest.conf":    netcfg.RenderUnbound(prev),
		netcfg.PortalVIPBundleFile:  []byte("\n"),
	}
	keaBytes, err := netcfg.RenderKeaFile(prev, topo, "", "")
	if err != nil {
		t.Fatal(err)
	}
	files["kea-dhcp4.json"] = keaBytes
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(bundle, name), data, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	st.activeID, st.activeBundle, st.activeIntent = "rev-prev", bundle, prev
	st.state["rev-prev"] = "active"
	st.state["rev-failed"] = "pending_confirmation"
	a.prevBundleFn = func(context.Context, string) (string, error) { return bundle, nil }
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{"br-g90": true} }

	inner := a.runFn
	a.runFn = func(ctx context.Context, name string, args ...string) error {
		cmd := name + " " + strings.Join(args, " ")
		for _, f := range failAt {
			if strings.HasPrefix(cmd, f) {
				return errors.New("simulated failure: " + cmd)
			}
		}
		return inner(ctx, name, args...)
	}
	return a, k, st, fk
}

func TestACleanRollbackIsReportedAsSuchAndLeavesNoMarker(t *testing.T) {
	a, _, st, _ := rollbackFixture(t)
	if steps := a.rollback(context.Background(), "rev-failed", "health check failed"); len(steps) != 0 {
		t.Fatalf("a clean rollback reported unfinished steps: %v", steps)
	}
	if _, bad := a.rollbackIncomplete(); bad {
		t.Fatal("a clean rollback left the incomplete marker")
	}
	if st.state["rev-failed"] != "rolled_back" || strings.Contains(st.reasons["rev-failed"], "INCOMPLETE") {
		t.Fatalf("revision %q reason %q", st.state["rev-failed"], st.reasons["rev-failed"])
	}
	ev := st.eventsJoined()
	if !strings.Contains(ev, "rollback_complete=true") || strings.Contains(ev, "rollback_incomplete") {
		t.Fatalf("events do not record a clean rollback: %s", ev)
	}
}

func TestARollbackWhoseStepFailedIsNotReportedAsSuccessful(t *testing.T) {
	cases := []struct {
		name   string
		failAt []string
		setup  func(a *applier, k *fakeKernel, fk *fakeKea)
		want   string
	}{
		{"DHCP configuration cannot be restored", nil, func(a *applier, k *fakeKernel, fk *fakeKea) { fk.setErr = errors.New("kea refused") }, "rollback_kea"},
		{"the resolver will neither restart nor reload", []string{"systemctl restart unbound", "unbound-control reload"}, nil, "rollback_unbound"},
		{"netplan cannot be regenerated", []string{"netplan generate"}, nil, "rollback_netplan"},
		{"the firewall cannot be restored", nil, func(a *applier, k *fakeKernel, fk *fakeKea) { k.failNftF = true }, "rollback_nft"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			a, k, st, fk := rollbackFixture(t, tc.failAt...)
			if tc.setup != nil {
				tc.setup(a, k, fk)
			}
			steps := a.rollback(context.Background(), "rev-failed", "health check failed")
			if len(steps) == 0 {
				t.Fatal("a rollback with a failed step reported none")
			}
			joined := strings.Join(steps, " ")
			if !strings.Contains(strings.ToLower(joined), strings.ToLower(strings.TrimPrefix(tc.want, "rollback_"))) && !strings.Contains(joined, "nft") {
				t.Logf("steps: %v", steps)
			}
			ev := st.eventsJoined()
			if strings.Contains(ev, "rollback_complete") {
				t.Fatalf("a success was recorded for an incomplete rollback: %s", ev)
			}
			if !strings.Contains(ev, "rollback_incomplete=false") || !strings.Contains(ev, tc.want+"=false") {
				t.Fatalf("the failure was not recorded (%s): %s", tc.want, ev)
			}
			if !strings.Contains(st.reasons["rev-failed"], "ROLLBACK INCOMPLETE") {
				t.Fatalf("the revision's recorded reason hides the failure: %q", st.reasons["rev-failed"])
			}
			if _, bad := a.rollbackIncomplete(); !bad {
				t.Fatal("no marker was left")
			}
			if _, err := os.Stat(a.incompleteMarkerPath()); err != nil {
				t.Fatalf("the marker is not durable: %v", err)
			}
		})
	}
}

// With the marker set, a new apply is refused without touching anything, and so is one after a restart (the marker is a file).
func TestAnIncompleteRollbackBlocksFurtherAppliesUntilBootConverges(t *testing.T) {
	a, k, st, fk := rollbackFixture(t)
	fk.setErr = errors.New("kea refused")
	if steps := a.rollback(context.Background(), "rev-failed", "x"); len(steps) == 0 {
		t.Fatal("setup: the rollback should have been incomplete")
	}
	k.reset()
	st.seq = 10
	_, err := a.Apply(context.Background(), "next", "tester")
	if !errors.Is(err, errRollbackIncomplete) {
		t.Fatalf("an apply was allowed on top of an unfinished rollback: %v", err)
	}
	if len(k.cmds) != 0 {
		t.Fatalf("the refused apply touched the system: %v", k.cmds)
	}

	// A restart: a fresh process reads the marker from disk and still refuses.
	b := applyFixture(t, newFakeKernel(t), st)
	b.generatedDir = a.generatedDir
	b.loadIncompleteMarker(context.Background())
	if _, err := b.Apply(context.Background(), "after restart", "tester"); !errors.Is(err, errRollbackIncomplete) {
		t.Fatalf("a restart forgot the unfinished rollback: %v", err)
	}

	// A boot reconcile that converges completely clears it.
	fk.setErr = nil
	a.activeIntentFn = nil
	st.activeIntent = oldIntent()
	if !a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("the boot reconcile did not converge")
	}
	if _, bad := a.rollbackIncomplete(); bad {
		t.Fatal("a complete boot convergence did not clear the marker")
	}
	if _, err := os.Stat(a.incompleteMarkerPath()); !os.IsNotExist(err) {
		t.Fatalf("the marker file is still there: %v", err)
	}
}

// A boot reconcile that could not converge does NOT clear it.
func TestAnIncompleteBootReconcileKeepsTheMarker(t *testing.T) {
	a, _, st, fk := rollbackFixture(t)
	a.markRollbackIncomplete("rev-failed", []string{"earlier"})
	fk.setErr = errors.New("kea refused")
	st.activeIntent = oldIntent()
	if a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("a boot reconcile with a failed DHCP push claimed to converge")
	}
	if _, bad := a.rollbackIncomplete(); !bad {
		t.Fatal("an incomplete boot reconcile cleared the marker")
	}
}

// An operator rollback reports the incomplete outcome as an error, not as success.
func TestOperatorRollbackReportsAnIncompleteOutcome(t *testing.T) {
	a, _, _, fk := rollbackFixture(t)
	fk.setErr = errors.New("kea refused")
	err := a.Rollback(context.Background(), "rev-failed", "operator")
	if !errors.Is(err, errRollbackIncomplete) {
		t.Fatalf("an incomplete operator rollback was reported as success: %v", err)
	}
}

// A rollback that cannot even read the revision's state did not roll anything back; it says so and blocks further changes.
func TestRollbackWithUnreadableStateIsIncomplete(t *testing.T) {
	a, _, st, _ := rollbackFixture(t)
	st.stateReadErr = errors.New("connection reset")
	if steps := a.rollback(context.Background(), "rev-failed", "x"); len(steps) == 0 {
		t.Fatal("an unreadable revision state reported a finished rollback")
	}
	if _, bad := a.rollbackIncomplete(); !bad {
		t.Fatal("no marker was left")
	}
}

// When the previous revision cannot be rendered, the rejected intent does not stay in force for the firewall tick.
func TestRollbackThatCannotRenderThePreviousRevisionAbandonsTheRejectedIntent(t *testing.T) {
	a, k, _, _ := rollbackFixture(t)
	a.prevIntentFn = func(context.Context, string) ([]netcfg.GuestNetwork, error) {
		return nil, errors.New("intent unreadable")
	}
	a.setInForce(oldIntent(), "")
	// The apply that is being abandoned published newIntent over the old one.
	before := a.snapshotInForce()
	a.setInForce(newIntent(), "")
	a.beginApplyInflight("rev-failed", before)

	a.rollback(context.Background(), "rev-failed", "health check failed")

	if got, _, _ := a.inForce(); len(got) != 1 || got[0].BridgeName != "br-g90" {
		t.Fatalf("the rejected intent is still in force: %+v", got)
	}
	k.reset()
	_ = k
}

// ---- M6: a change interrupted mid-apply does not hold the in-flight slot forever -----------------------------------------------

func TestInterruptedApplyIsRolledBackAtBoot(t *testing.T) {
	a, _, st, _ := rollbackFixture(t)
	st.state["rev-stuck"] = "applying"
	st.applying = []string{"rev-stuck"}

	a.ReclaimInterruptedOnBoot(context.Background())

	if st.state["rev-stuck"] != "rolled_back" {
		t.Fatalf("the interrupted revision is %q; it would hold the in-flight slot forever", st.state["rev-stuck"])
	}
	if !strings.Contains(st.reasons["rev-stuck"], "interrupted") {
		t.Fatalf("the reason does not say why: %q", st.reasons["rev-stuck"])
	}
	if !strings.Contains(st.eventsJoined(), "boot_reclaim=true") {
		t.Fatalf("the reclaim was not recorded: %s", st.eventsJoined())
	}
}

// With no earlier confirmed revision the interrupted first apply comes back factory-clean.
func TestInterruptedFirstApplyIsRolledBackAtBoot(t *testing.T) {
	k := newFakeKernel(t)
	st := newFakeStore(newIntent())
	a := applyFixture(t, k, st)
	a.prevBundleFn = func(context.Context, string) (string, error) { return "", nil }
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{} }
	st.state["rev-first"] = "applying"
	st.applying = []string{"rev-first"}

	a.ReclaimInterruptedOnBoot(context.Background())

	if st.state["rev-first"] != "rolled_back" {
		t.Fatalf("the interrupted first apply is %q", st.state["rev-first"])
	}
}

// ---- helpers shared with the other new tests ---------------------------------------------------------------------------------------

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
