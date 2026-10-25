package main

// Integrity of the apply / rollback pipeline: what keeps a rejected or half-finished change from reaching the wire, and what stops a
// second change from being applied on top of a rollback that did not finish.
//
//   - opMu serialises every operation that changes what is in force (apply, rollback, boot reconcile). The periodic loops that render the
//     in-force configuration (nft re-assertion, Kea captive sync, portal refresh) only run while they can take it, so they can never
//     read an intent that an apply has published but not yet proven.
//   - A rollback records every step that failed. If any did, the appliance is NOT reported as rolled back cleanly: the revision is
//     marked, a durable marker is written, and Apply refuses until a boot reconcile has converged the appliance to its confirmed
//     configuration.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

var (
	// errApplyBusy: another apply, rollback or boot reconcile is running in this process.
	errApplyBusy = errors.New("another network change is being applied or rolled back; try again in a moment")
	// errChangeInFlight: a revision is applying or awaiting confirmation.
	errChangeInFlight = errors.New("a network change is still awaiting confirmation")
	// errRollbackIncomplete: an earlier rollback did not restore everything.
	errRollbackIncomplete = errors.New("an earlier rollback did not finish")
)

// opWait is how long Apply waits for a periodic re-assertion tick (milliseconds long) before saying it is busy.
const opWait = 3 * time.Second

// revisionStore is the part of the database netd's applier uses; *store implements it, and a test supplies a fake.
type revisionStore interface {
	Event(ctx context.Context, revID, phase string, ok bool, detail map[string]any)
	Health(ctx context.Context, revID, name string, ok bool, detail string)
	LoadIntent(ctx context.Context) ([]netcfg.GuestNetwork, error)
	CreateRevision(ctx context.Context, summary string, intent []netcfg.GuestNetwork, createdBy string) (string, int64, error)
	SetRevisionValidation(ctx context.Context, id string, v netcfg.ValidationResult) error
	SetRevisionState(ctx context.Context, id, state string) error
	MarkApplying(ctx context.Context, id, bundlePath, appliedBy string, confirmDeadline any) error
	MarkPending(ctx context.Context, id string, deadline any) error
	MarkActive(ctx context.Context, id, confirmedBy string) error
	MarkFailed(ctx context.Context, id, reason string) error
	MarkRolledBack(ctx context.Context, id, reason string) error
	RevisionState(ctx context.Context, id string) (string, error)
	CurrentActiveIntent(ctx context.Context) (id, bundlePath string, intent []netcfg.GuestNetwork, err error)
	ActiveBundlePath(ctx context.Context, exceptID string) (string, error)
	ActiveIntent(ctx context.Context, exceptID string) ([]netcfg.GuestNetwork, error)
	InFlightRevision(ctx context.Context) (id, state string, err error)
	ApplyingRevisions(ctx context.Context) ([]string, error)
	RecordedBridges(ctx context.Context) (map[string]recordedBridge, error)
	LoadPortalIP(ctx context.Context) (string, error)
	LoadPortalTLS(ctx context.Context) (netcfg.PortalTLS, error)
	KnownIfaceSet(ctx context.Context) (map[string]bool, error)
	SyncInterfaces(ctx context.Context, ifaces []Interface, mgmt, wan string) error
}

var _ revisionStore = (*store)(nil)

// acquireOp takes the operation lock, waiting up to wait. false means another operation holds it.
func (a *applier) acquireOp(ctx context.Context, wait time.Duration) bool {
	if a.opMu.TryLock() {
		return true
	}
	if wait <= 0 {
		return false
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-tick.C:
			if a.opMu.TryLock() {
				return true
			}
		}
	}
}

func (a *applier) releaseOp() { a.opMu.Unlock() }

// inFlight reports a revision that is applying or awaiting confirmation. An unreadable answer is an error: not knowing is
// not permission to publish a new intent.
func (a *applier) inFlight(ctx context.Context) (id, state string, err error) {
	if a.inFlightFn != nil {
		return a.inFlightFn(ctx)
	}
	return a.st.InFlightRevision(ctx)
}

// applyInflight remembers what was in force before the apply that is running (or awaiting confirmation) in this process, so any
// path that abandons that apply puts the previous intent back in force rather than leaving the rejected one to be re-asserted.
type applyInflight struct {
	id     string
	before portalInForce
}

func (a *applier) beginApplyInflight(id string, before portalInForce) {
	a.inflightMu.Lock()
	defer a.inflightMu.Unlock()
	a.inflight = &applyInflight{id: id, before: before}
}

// takeApplyInflight returns the pre-apply state for id (and forgets it) when this process applied it.
func (a *applier) takeApplyInflight(id string) (portalInForce, bool) {
	a.inflightMu.Lock()
	defer a.inflightMu.Unlock()
	if a.inflight == nil || a.inflight.id != id {
		return portalInForce{}, false
	}
	b := a.inflight.before
	a.inflight = nil
	return b, true
}

func (a *applier) clearApplyInflight() {
	a.inflightMu.Lock()
	defer a.inflightMu.Unlock()
	a.inflight = nil
}

// ---- incomplete rollback marker ----------------------------------------------------------------------------------

type incompleteMarker struct {
	Revision string   `json:"revision"`
	Steps    []string `json:"steps"`
	At       string   `json:"at"`
}

func (a *applier) incompleteMarkerPath() string {
	return filepath.Join(a.generatedDir, "rollback-incomplete.json")
}

// markRollbackIncomplete records, in memory and durably, that a rollback left the appliance in a state that is not the confirmed
// configuration. A failure to write the file is logged by the caller's event; the in-memory flag still blocks this process.
func (a *applier) markRollbackIncomplete(id string, steps []string) {
	m := incompleteMarker{Revision: id, Steps: steps, At: time.Now().UTC().Format(time.RFC3339)}
	a.incMu.Lock()
	a.incomplete = &m
	a.incMu.Unlock()
	if a.generatedDir == "" || a.dryRun {
		return
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	_ = writeFileAtomic(a.incompleteMarkerPath(), raw, 0o640)
}

// rollbackIncomplete returns the marker when a rollback did not finish.
func (a *applier) rollbackIncomplete() (incompleteMarker, bool) {
	a.incMu.Lock()
	defer a.incMu.Unlock()
	if a.incomplete == nil {
		return incompleteMarker{}, false
	}
	return *a.incomplete, true
}

// loadIncompleteMarker restores the marker at start, so a restart does not forget an unfinished rollback. An unreadable file is
// treated as present: not knowing is not permission to apply.
func (a *applier) loadIncompleteMarker() {
	if a.generatedDir == "" {
		return
	}
	raw, err := os.ReadFile(a.incompleteMarkerPath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	m := incompleteMarker{Revision: "unknown", Steps: []string{"the marker file could not be read"}}
	if err == nil {
		if jerr := json.Unmarshal(raw, &m); jerr != nil {
			m = incompleteMarker{Revision: "unknown", Steps: []string{"the marker file is unreadable: " + jerr.Error()}}
		}
	} else {
		m.Steps = []string{"the marker file could not be read: " + err.Error()}
	}
	a.incMu.Lock()
	a.incomplete = &m
	a.incMu.Unlock()
}

// clearRollbackIncomplete removes the marker; only a fully converged boot reconcile calls it.
func (a *applier) clearRollbackIncomplete() {
	a.incMu.Lock()
	a.incomplete = nil
	a.incMu.Unlock()
	if a.generatedDir != "" {
		_ = os.Remove(a.incompleteMarkerPath())
	}
}

func (m incompleteMarker) describe() string {
	return fmt.Sprintf("rollback of revision %s did not finish (%s)", m.Revision, strings.Join(m.Steps, "; "))
}

// ---- rollback report -----------------------------------------------------------------------------------------------

// rollbackReport collects every rollback step that did not succeed, so the outcome is the real one.
type rollbackReport struct {
	a      *applier
	ctx    context.Context
	revID  string
	failed []string
}

func (a *applier) newRollbackReport(ctx context.Context, revID string) *rollbackReport {
	return &rollbackReport{a: a, ctx: ctx, revID: revID}
}

// fail records a failed step and the matching event. err may be nil when the failure is a condition rather than an error.
func (r *rollbackReport) fail(kind, step string, err error, extra map[string]any) {
	msg := step
	if err != nil {
		msg = step + ": " + err.Error()
	}
	r.failed = append(r.failed, msg)
	detail := map[string]any{"step": step}
	if err != nil {
		detail["error"] = err.Error()
	}
	for k, v := range extra {
		detail[k] = v
	}
	r.a.event(r.ctx, r.revID, kind, false, detail)
}

func (r *rollbackReport) incomplete() bool { return len(r.failed) > 0 }

// writeFileAtomic writes data to path so a reader (or a crash) sees the old file or the new one, never a partial one: a temp file in
// the same directory, fsync, rename, fsync of the directory.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// rollbackExclusive is rollback for a caller that does not already hold the operation lock (the confirmation watchdog). It waits for any
// apply in progress: a revision that is pending confirmation cannot be the one being applied.
func (a *applier) rollbackExclusive(ctx context.Context, id, reason string) []string {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	return a.rollback(ctx, id, reason)
}

// ReclaimInterruptedOnBoot rolls back every revision a previous run of netd left in 'applying'. At start no apply of this process can be running,
// so such a row was interrupted; nothing else would ever move it, and it holds the unique in-flight slot against every later apply. The rollback
// is the ordinary one: it returns to the previous confirmed revision, or to factory-clean when there is none.
func (a *applier) ReclaimInterruptedOnBoot(ctx context.Context) {
	if a.dryRun || a.st == nil {
		return
	}
	ids, err := a.st.ApplyingRevisions(ctx)
	if err != nil {
		slog.Error("netd could not list interrupted changes; a change left 'applying' by the last run, if any, still holds the in-flight slot", "err", err)
		return
	}
	for _, id := range ids {
		slog.Warn("netd: rolling back a change that the last run left half-applied", "revision", id)
		steps := a.rollbackExclusive(ctx, id, "interrupted: the network service stopped while this change was being applied")
		a.event(ctx, id, "boot_reclaim", len(steps) == 0, map[string]any{"steps": steps})
	}
}

// rollbackIncompleteStatus is the /v1/health view of the unfinished-rollback marker.
func (a *applier) rollbackIncompleteStatus() map[string]any {
	m, ok := a.rollbackIncomplete()
	if !ok {
		return map[string]any{"incomplete": false}
	}
	return map[string]any{"incomplete": true, "revision": m.Revision, "steps": m.Steps, "since": m.At}
}

func (a *applier) keaDiskStatus() map[string]any {
	ok, detail := a.keaDiskState()
	return map[string]any{"holds_confirmed_configuration": ok, "detail": detail}
}
