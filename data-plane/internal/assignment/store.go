package assignment

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// storeMu serialises every read-modify-write of the assignment record in this process, so a refresh note never overwrites a concurrent adoption.
var storeMu sync.Mutex

const (
	StatusCurrent = "current" // verified and not past expires_at
	StatusStale   = "stale"   // past expires_at and Central has not refreshed it
	StatusNone    = "none"    // never assigned
)

// The appliance's DURABLE assignment state. The signed assignment is configuration, not an auth token: the last verified document stays authoritative
// even after it expires (a hotel must serve through a Central outage); Previous allows rollback of a bad adoption.
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

// Persists the Record as the LOCAL source of truth for tenant/site, beside (not inside) the identity so a factory reset can wipe either independently.
type Store struct{ Dir string }

func (s *Store) path() string { return filepath.Join(s.Dir, "assignment.json") }

// highestPath records the highest assignment version ever adopted for an appliance id, outside the record itself, so an older signed document written
// back to assignment.json (an old image, a restored copy) is not accepted again.
func (s *Store) highestPath() string { return filepath.Join(s.Dir, "assignment.highest") }

type highestRecord struct {
	ApplianceID string `json:"appliance_id"`
	Version     int64  `json:"version"`
}

// HighestAdopted returns the highest version adopted for applianceID, or 0 when none is recorded for it.
func (s *Store) HighestAdopted(applianceID string) int64 {
	b, err := os.ReadFile(s.highestPath())
	if err != nil {
		return 0
	}
	var h highestRecord
	if json.Unmarshal(b, &h) != nil || h.ApplianceID == "" || h.ApplianceID != applianceID {
		return 0
	}
	return h.Version
}

// Accepts the legacy on-disk format (a bare signed Document) and upgrades it in memory, so an appliance assigned earlier keeps its assignment.
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
	return writeDurable(s.path(), b, 0o644)
}

// writeDurable replaces path crash-safely: unique temp file, fsync, rename, directory fsync. A power cut never leaves a zero-length file.
func writeDurable(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Atomically applies a newly-verified document, keeping the outgoing one as Previous (rollback-safe) and resetting staleness.
func (s *Store) Adopt(d *Document) error {
	if d == nil {
		return errors.New("nil assignment")
	}
	storeMu.Lock()
	defer storeMu.Unlock()
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
	rec.StaleSince = ""
	if err := s.save(rec); err != nil {
		return err
	}
	if d.ApplianceID != "" && d.Version > s.HighestAdopted(d.ApplianceID) {
		hb, _ := json.Marshal(highestRecord{ApplianceID: d.ApplianceID, Version: d.Version})
		if err := writeDurable(s.highestPath(), hb, 0o644); err != nil {
			return errors.New("record highest assignment version: " + err.Error() + " (version " + strconv.FormatInt(d.Version, 10) + ")")
		}
	}
	return nil
}

// ok=true means Central answered (even with no change); ok=false only records the attempt and NEVER downgrades the assignment.
func (s *Store) NoteRefresh(ok bool) {
	storeMu.Lock()
	defer storeMu.Unlock()
	r, err := s.Load()
	if err != nil || r == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	r.LastRefreshAttempt = now
	if ok {
		r.LastRefreshSuccess = now
	}
	// Staleness tracks the DOCUMENT, not the link: reaching Central does not make an expired document current, only Adopt does; keep stale_since set
	// while the held document is still past expiry.
	if IsExpired(r.Current, time.Now()) {
		if r.StaleSince == "" {
			r.StaleSince = now
		}
	} else {
		r.StaleSince = ""
	}
	_ = s.save(r)
}

// Factory reset only, never expiry.
func (s *Store) Clear() error {
	storeMu.Lock()
	defer storeMu.Unlock()
	if err := os.Remove(s.highestPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err := os.Remove(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Stale means past expiry and not refreshed: an operator warning, NOT a loss of authority.
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

// Ownership is derived ONLY from the state of the last verified document: an expired one still grants tenant/site, and only a newer signed
// unassigned, revoked or decommissioned document unassigns.
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
