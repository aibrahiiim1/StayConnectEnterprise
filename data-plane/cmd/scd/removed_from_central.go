package main

// An appliance Central has deleted. Changing an appliance's customer goes through retire, factory-clean, new
// activation; there is no in-place cross-customer path. A re-registered appliance with a new key would be
// indistinguishable from a genuine factory reset and could carry customer A's data to customer B, so the
// reset depends on what the appliance has held:
//
//   - Never held a customer (no granting assignment ever, no tenant rows): clears its identity and registers again.
//   - Has held a customer (granting or terminal assignment on disk, or tenant data): stops serving new guests
//     (licence and client certificate removed), KEEPS identity, assignment and local records, generates no new
//     key and never registers again. The fact is recorded durably; the only way out is the factory-clean
//     procedure (deploy/scripts/provision-fresh-appliance.sh,
//     docs/DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md) and a new activation. There is no remote wipe.
//
// A terminal assignment (revoked / decommissioned / unassigned) counts as having held a customer.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

const removedFromCentralCode = "removed_from_central"

// removedRecord is the durable marker; its presence is the state.
type removedRecord struct {
	At          string `json:"at"`
	ApplianceID string `json:"appliance_id"`
	Serial      string `json:"serial"`
	Reason      string `json:"reason"`
}

func removedMarkerPath() string {
	return envOr("SCD_REMOVED_MARKER", "/etc/stayconnect/removed-from-central.json")
}

// loadRemoved returns the marker, or nil when this appliance has not been removed. An unreadable marker is
// treated as present: a file that exists and cannot be parsed must not be read as permission to register.
func loadRemoved(path string) *removedRecord {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec removedRecord
	if json.Unmarshal(b, &rec) != nil {
		return &removedRecord{Reason: "marker unreadable"}
	}
	return &rec
}

func writeRemoved(path string, rec removedRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	return writeFileSync(path, b, 0o644)
}

// mayRegister reports whether an appliance with no identity may self-register: enabled, a Central configured,
// and not removed after holding a customer (never again, under any key, until factory-reset).
func mayRegister(autoRegister bool, ctrlBase string, removed *removedRecord) bool {
	return autoRegister && ctrlBase != "" && removed == nil
}

type orphanPaths struct {
	IdentityDir, AssignmentDir, LicenseDir, CertDir, Marker string
}

// heldCustomer reports whether this appliance has ever held a customer: a granting or terminal assignment on
// disk (current or previous), or tenant data in the site database. An error means "cannot tell", and the
// caller must not act on it.
func (s *server) heldCustomer(ctx context.Context, assignmentDir string) (bool, error) {
	if rec, err := (&assignment.Store{Dir: assignmentDir}).Load(); err == nil && rec != nil {
		for _, d := range []*assignment.Document{rec.Current, rec.Previous} {
			if d != nil && (assignment.Grants(d.State) || assignment.Clears(d.State)) {
				return true, nil
			}
		}
	}
	if s.db == nil {
		return false, nil
	}
	var any bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM tenants) OR EXISTS(SELECT 1 FROM sites)`).Scan(&any); err != nil {
		return false, err
	}
	return any, nil
}

// handleConfirmedOrphan acts on a deletion Central has confirmed in a sustained way (orphanTracker). It returns what it did, for the log
// and the tests: "reset" (identity cleared, the restarted scd registers again), "removed" (identity kept,
// marker written, no re-registration), or "" when it could not decide and did nothing.
func (s *server) handleConfirmedOrphan(ctx context.Context, p orphanPaths) string {
	held, err := s.heldCustomer(ctx, p.AssignmentDir)
	if err != nil {
		slog.Warn("hello: Central no longer knows this appliance, but whether it has held a customer could not be read; not acting yet",
			"err", err)
		return ""
	}
	// Both branches stop the appliance from serving on credentials Central no longer recognises.
	_ = removeDirContents(p.LicenseDir)
	_ = os.Remove(filepath.Join(p.CertDir, "client.crt"))
	_ = os.Remove(filepath.Join(p.CertDir, "mtls-client.key"))
	if !held {
		slog.Warn("hello: appliance unknown to Central (deleted) and it never held a customer — clearing its identity to register again as waiting")
		for _, d := range []string{p.IdentityDir, p.AssignmentDir} {
			_ = removeDirContents(d)
		}
		s.restartSelf()
		return "reset"
	}
	rec := removedRecord{At: time.Now().UTC().Format(time.RFC3339), ApplianceID: s.applID,
		Serial: firstNonEmpty(s.hw.Serial, s.serial), Reason: "Central no longer knows this appliance"}
	if err := writeRemoved(p.Marker, rec); err != nil {
		// Without the marker a restart would register again, which is the one thing this must prevent, so
		// do not restart: the licence is already gone and nothing new is admitted in this process either.
		slog.Error("hello: could not record that this appliance was removed from Central; staying up without restarting",
			"err", err)
		s.removed.Store(&rec)
		return "removed"
	}
	slog.Warn("hello: appliance unknown to Central (deleted) after holding a customer — it keeps its identity and data, " +
		"will not register again, and needs a factory-clean install before it can be activated again")
	s.removed.Store(&rec)
	s.restartSelf()
	return "removed"
}

// Removal requires a sustained "not found": at least orphanConfirmReplies consecutive replies, first and last
// at least orphanConfirmWindow apart. Two close replies are one observation (a restored backup, failover or
// broken deploy answers that way and recovers) and the reaction is permanent. Any other definite reply
// resets the count; a transport failure leaves it as is. These are licensing-protocol constants, not hotel
// settings: shortening them only loses licences by accident. At the five-minute hello cadence deletion is
// acted on after about ten minutes.
const (
	orphanConfirmReplies = 3
	orphanConfirmWindow  = 10 * time.Minute
)

// orphanTracker accumulates the evidence; used from the single hello goroutine, the mutex covers future changes.
type orphanTracker struct {
	mu    sync.Mutex
	count int
	first time.Time
}

// observe records one hello outcome and reports whether the deletion is now confirmed. notFound is a definite
// "this appliance is unknown" reply; replied is false when no reply arrived at all (transport error).
func (o *orphanTracker) observe(now time.Time, replied, notFound bool) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !replied {
		return false
	}
	if !notFound {
		o.count, o.first = 0, time.Time{}
		return false
	}
	if o.count == 0 {
		o.first = now
	}
	o.count++
	if o.count >= orphanConfirmReplies && now.Sub(o.first) >= orphanConfirmWindow {
		o.count, o.first = 0, time.Time{}
		return true
	}
	return false
}

func (s *server) restartSelf() {
	if s.restartFn != nil {
		s.restartFn()
		return
	}
	restartSCDUnit()
}

func (s *server) isRemovedFromCentral() bool { return s.removed.Load() != nil }

// refuseWhileRemoved refuses operator actions that would re-enter a customer's licence or activation on a removed appliance.
func (s *server) refuseWhileRemoved(w http.ResponseWriter) bool {
	if !s.isRemovedFromCentral() {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": removedFromCentralCode,
		"message": "This appliance was removed from OneGate Central. To use it again, factory-reset it and have " +
			"your vendor activate it.",
	})
	return true
}
