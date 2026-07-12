package assignment

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Assignment status as seen by the appliance.
const (
	StatusCurrent = "current" // verified and not past expires_at
	StatusStale   = "stale"   // past expires_at and Central has not refreshed it
	StatusNone    = "none"    // never assigned
)

// Record is the appliance's DURABLE assignment state.
//
// The signed assignment is configuration, not an auth token: a hotel must keep
// serving guests through a Central outage, so the last successfully-verified
// document stays authoritative even after it expires. Previous is retained so a
// bad adoption can be rolled back.
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

// Store persists the Record as the appliance's LOCAL source of truth for
// tenant/site. It lives beside (not inside) the identity so a factory reset can
// wipe either independently.
type Store struct{ Dir string }

func (s *Store) path() string { return filepath.Join(s.Dir, "assignment.json") }

// Load returns the persisted record. It accepts the legacy on-disk format (a bare
// signed Document) and upgrades it in memory, so an appliance that was assigned
// before this change keeps its assignment across the upgrade.
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

// Doc returns the currently-applied signed document (nil if none).
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

// Adopt atomically applies a newly-verified document: the outgoing one is kept as
// Previous (rollback-safe), and refresh/staleness metadata is reset.
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

// NoteRefresh records a refresh attempt against Central. ok=true means Central
// answered (even with "no change"), which clears staleness. ok=false only records
// the attempt — it NEVER downgrades the assignment.
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
	// Staleness tracks the DOCUMENT, not the link: reaching Central does not make an
	// expired document current — only adopting a newer one does (see Adopt). So keep
	// stale_since set while the held document is still past its expiry, and clear it
	// only when the document itself is no longer expired.
	if IsExpired(r.Current, time.Now()) {
		if r.StaleSince == "" {
			r.StaleSince = now
		}
	} else {
		r.StaleSince = ""
	}
	_ = s.save(r)
}

// Clear removes the local assignment (factory reset only — never expiry).
func (s *Store) Clear() error {
	err := os.Remove(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Status reports current/stale/none. Stale means "past expiry and not refreshed";
// it is an operator warning, NOT a loss of authority.
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

// Resolved returns the operational tenant/site + state + version.
//
// Ownership is derived ONLY from the state of the last verified document. An
// expired document still grants tenant/site — expiry never unassigns an
// appliance; only a newer signed unassigned/revoked/decommissioned document does.
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
