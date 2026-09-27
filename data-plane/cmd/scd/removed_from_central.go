package main

// AN APPLIANCE CENTRAL HAS DELETED, AND WHAT IT MAY DO ABOUT IT.
//
// Product-Owner rule: changing an appliance's customer goes through retire -> FACTORY-CLEAN -> new activation.
// There is no in-place cross-customer path.
//
// The orphan detection (the hello probe in main.go) used to break that rule. When Central answered "not
// enrolled / unknown appliance / not found" twice, scd cleared its identity, assignment, licence and client
// certificate and restarted -- and the restarted scd generated a NEW identity key and registered itself as a
// fresh waiting appliance, while the previous customer's data was still in the local database. Central cannot
// tell that registration from a genuine factory reset, so the box could be activated for a different customer
// and carry customer A's data into customer B (the cross-tenant purge is gated and refuses, leaving it stuck
// fail-closed rather than clean).
//
// So the reset now depends on what the appliance has held:
//
//   - NEVER held a customer (no granting assignment on disk, ever; no tenant rows in the site database): a
//     waiting appliance whose record was deleted. There is nothing to carry, so it may clear its identity and
//     register again exactly as before.
//   - HAS held a customer (a granting or terminal assignment on disk, or tenant data in the site database):
//     it stops serving new guests (the licence and the client certificate are removed), KEEPS its identity,
//     its assignment and every local record, does NOT generate a new key and does NOT register again. It
//     records the fact durably, so a restart or a reboot changes nothing, and says so plainly in Hotel Admin.
//     The only way out is the factory-clean procedure (deploy/scripts/provision-fresh-appliance.sh,
//     docs/DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md) followed by a new activation. There is no remote wipe.
//
// A TERMINAL assignment (revoked / decommissioned / unassigned) is the second case: the appliance held a
// customer before it was retired, so if Central later deletes the retired record the appliance lands here
// rather than re-registering under a new key.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// removedFromCentralCode is the refusal code and the status reason for this state.
const removedFromCentralCode = "removed_from_central"

// removedRecord is the durable marker. Its presence IS the state.
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mayRegister decides whether an appliance with no identity may register itself with Central: self-registration
// on, a Central configured, and NOT removed from Central after holding a customer. A removed appliance never
// registers again, under any key, until it is factory-reset.
func mayRegister(autoRegister bool, ctrlBase string, removed *removedRecord) bool {
	return autoRegister && ctrlBase != "" && removed == nil
}

// orphanPaths are the directories the orphan reset touches.
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

// handleConfirmedOrphan acts on a deletion Central has confirmed twice. It returns what it did, for the log
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

// restartSelf restarts scd so every subsystem re-evaluates. Tests replace s.restartFn.
func (s *server) restartSelf() {
	if s.restartFn != nil {
		s.restartFn()
		return
	}
	restartSCDUnit()
}

// isRemovedFromCentral reports whether this appliance is in the removed state.
func (s *server) isRemovedFromCentral() bool { return s.removed.Load() != nil }

// refuseWhileRemoved answers an operator action that would re-enter a customer's licence or activation on a
// removed appliance. The only way back is a factory-clean install and a new activation.
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
