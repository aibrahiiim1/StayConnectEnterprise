package assignment

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Store persists the current signed assignment document as the appliance's LOCAL
// authoritative source of truth for tenant/site. It lives next to the identity
// (a separate dir so a factory reset can wipe assignment without touching
// identity, or vice-versa). The file holds the full signed Document so it can be
// re-verified on every boot.
type Store struct{ Dir string }

func (s *Store) path() string { return filepath.Join(s.Dir, "assignment.json") }

// Load returns the persisted signed assignment, or (nil, os.ErrNotExist) if the
// appliance has never been assigned.
func (s *Store) Load() (*Document, error) {
	b, err := os.ReadFile(s.path())
	if err != nil {
		return nil, err
	}
	var d Document
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Save atomically writes the signed assignment (temp file + rename).
//
// Perms are deliberately world-readable: the assignment is a vendor-SIGNED,
// tamper-evident public document (appliance/tenant/site ids + signature, no key
// material). Every local service — scd (root) and edged/others (stayconnect) —
// must read it as the shared source of truth, so it is not root-private.
func (s *Store) Save(d *Document) error {
	if d == nil {
		return errors.New("nil assignment")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	_ = os.Chmod(s.Dir, 0o755) // correct a pre-existing restrictive dir
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		return err
	}
	return os.Chmod(s.path(), 0o644)
}

// Clear removes the local assignment (factory-reset / unassign).
func (s *Store) Clear() error {
	err := os.Remove(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Resolved returns the operational tenant/site + version for the current
// assignment. tenant/site are non-empty ONLY when state == assigned; an
// unassigned/revoked/absent assignment yields empty tenant/site (awaiting
// assignment / halted), with the version still returned for replay defense.
func (s *Store) Resolved() (tenantID, siteID, state string, version int64) {
	d, err := s.Load()
	if err != nil || d == nil {
		return "", "", "", 0
	}
	if d.State == StateAssigned {
		return d.TenantID, d.SiteID, d.State, d.Version
	}
	return "", "", d.State, d.Version
}
