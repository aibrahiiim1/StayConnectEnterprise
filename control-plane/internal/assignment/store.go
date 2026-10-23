package assignment

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const (
	StatusCurrent = "current" // verified and not past expires_at
	StatusStale   = "stale"   // past expires_at and Central has not refreshed it
	StatusNone    = "none"    // never assigned
)

// Record is the appliance's durable assignment state. The assignment is configuration, not an auth token: the last
// verified document stays authoritative after it expires so a hotel serves through a Central outage. Previous is
// kept for rollback.
type Record struct {
	Current  *Document `json:"current"`
	Previous *Document `json:"previous,omitempty"`

	Version     int64  `json:"version"`
	SignerKeyID string `json:"signer_key_id"`
	State       string `json:"lifecycle_state"` // assigned|reassigned|unassigned|revoked|decommissioned

	AdoptedAt          string `json:"adopted_at"`
	LastRefreshSuccess string `json:"last_refresh_success,omitempty"`
	LastRefreshAttempt string `json:"last_refresh_attempt,omitempty"`
	StaleSince         string `json:"stale_since,omitempty"`
}

// Store persists the Record beside (not inside) the identity so a factory reset can wipe either independently.
type Store struct{ Dir string }

func (s *Store) path() string { return filepath.Join(s.Dir, "assignment.json") }

// Load accepts the legacy on-disk format (a bare signed Document) and upgrades it in memory.
func (s *Store) Load() (*Record, error) {
	b, err := os.ReadFile(s.path())
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(b, &rec); err == nil && rec.Current != nil {
		return &rec, nil
	}
	var d Document // legacy: the file was just the signed document
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	if d.AssignmentID == "" && d.ApplianceID == "" {
		return nil, errors.New("assignment file is not a valid record or document")
	}
	return &Record{
		Current: &d, Version: d.Version, SignerKeyID: d.SignerKeyID, State: d.State,
	}, nil
}

func (s *Store) Doc() *Document {
	r, err := s.Load()
	if err != nil || r == nil {
		return nil
	}
	return r.Current
}

func (s *Store) save(r *Record) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	_ = os.Chmod(s.Dir, 0o755)
	b, err := json.MarshalIndent(r, "", "  ")
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

// Adopt atomically applies a verified document, keeping the outgoing one as Previous and resetting staleness.
func (s *Store) Adopt(d *Document) error {
	if d == nil {
		return errors.New("nil assignment")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := &Record{}
	if old, err := s.Load(); err == nil && old != nil {
		rec.Previous = old.Current
	}
	rec.Current = d
	rec.Version = d.Version
	rec.SignerKeyID = d.SignerKeyID
	rec.State = d.State
	rec.AdoptedAt = now
	rec.LastRefreshSuccess = now
	rec.LastRefreshAttempt = now
	rec.StaleSince = "" // a freshly adopted document is current by definition
	return s.save(rec)
}

// NoteRefresh records a refresh attempt. ok=true means Central answered, which clears staleness; ok=false only
// records the attempt and never downgrades the assignment.
func (s *Store) NoteRefresh(ok bool) {
	r, err := s.Load()
	if err != nil || r == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	r.LastRefreshAttempt = now
	if ok {
		r.LastRefreshSuccess = now
	}
	// Staleness tracks the document, not the link: reaching Central does not make an expired document current, only
	// adopting a newer one does, so stale_since stays set while the held document is expired.
	if IsExpired(r.Current, time.Now()) {
		if r.StaleSince == "" {
			r.StaleSince = now
		}
	} else {
		r.StaleSince = ""
	}
	_ = s.save(r)
}

func (s *Store) Clear() error {
	err := os.Remove(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Status reports current/stale/none. Stale is an operator warning, not a loss of authority.
func (s *Store) Status() string {
	r, err := s.Load()
	if err != nil || r == nil || r.Current == nil {
		return StatusNone
	}
	if IsExpired(r.Current, time.Now()) {
		return StatusStale
	}
	return StatusCurrent
}

// Resolved returns the operational tenant/site, state and version. Ownership derives only from the last verified
// document: an expired one still grants tenant/site, and only a newer signed terminal document unassigns.
func (s *Store) Resolved() (tenantID, siteID, state string, version int64) {
	r, err := s.Load()
	if err != nil || r == nil || r.Current == nil {
		return "", "", "", 0
	}
	d := r.Current
	if Grants(d.State) {
		return d.TenantID, d.SiteID, d.State, d.Version
	}
	return "", "", d.State, d.Version
}
