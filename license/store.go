package license

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Store persists the appliance's license material under a directory
// (default /etc/stayconnect/license):
//
//	current.json    — latest verified Envelope
//	state.json      — monotonic anti-rollback record + validation timestamps
//	revoked.json    — license_ids named by authenticated revocation notices
//
// Anti-rollback rules enforced here:
//   - a newly installed document must not have issued_at older than the
//     currently installed one (prevents replaying an old, more generous
//     license after a downgrade or revocation);
//   - the evaluation clock never runs backwards: we persist the highest
//     wall-clock time observed and evaluate against max(now, high-water)
//     when the clock has been set back beyond tolerance.
type Store struct {
	dir      string
	verifier *Verifier
}

var ErrRollback = errors.New("license rollback rejected")

const clockRollbackTolerance = 48 * time.Hour

type storeState struct {
	// InstalledIssuedAt of the current document (monotonic).
	InstalledIssuedAt time.Time `json:"installed_issued_at"`
	// HighWater is the max wall-clock time this appliance has observed.
	HighWater time.Time `json:"high_water"`
	// LastCloudValidation is the last time the cloud confirmed this
	// license (successful license fetch or explicit ack).
	LastCloudValidation time.Time `json:"last_cloud_validation"`
}

func NewStore(dir string, v *Verifier) *Store { return &Store{dir: dir, verifier: v} }

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

func (s *Store) readState() storeState {
	var st storeState
	raw, err := os.ReadFile(s.path("state.json"))
	if err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func (s *Store) writeState(st storeState) error {
	raw, _ := json.Marshal(st)
	tmp := s.path("state.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path("state.json"))
}

func (s *Store) readRevoked() map[string]bool {
	out := map[string]bool{}
	raw, err := os.ReadFile(s.path("revoked.json"))
	if err == nil {
		var ids []string
		if json.Unmarshal(raw, &ids) == nil {
			for _, id := range ids {
				out[id] = true
			}
		}
	}
	return out
}

// Install verifies and persists a new envelope. now is injected for tests.
// Verify decodes and cryptographically verifies raw WITHOUT persisting it —
// used to enforce hardware/identity binding before a license is written to disk.
func (s *Store) Verify(raw []byte) (*Document, error) {
	env, err := DecodeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	return s.verifier.Verify(env)
}

func (s *Store) Install(raw []byte, now time.Time) (*Document, error) {
	env, err := DecodeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	doc, err := s.verifier.Verify(env)
	if err != nil {
		return nil, err
	}
	st := s.readState()
	if !st.InstalledIssuedAt.IsZero() && doc.IssuedAt.Before(st.InstalledIssuedAt) {
		return nil, fmt.Errorf("%w: incoming issued_at %s older than installed %s",
			ErrRollback, doc.IssuedAt.Format(time.RFC3339), st.InstalledIssuedAt.Format(time.RFC3339))
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	tmp := s.path("current.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, s.path("current.json")); err != nil {
		return nil, err
	}
	st.InstalledIssuedAt = doc.IssuedAt
	if now.After(st.HighWater) {
		st.HighWater = now
	}
	if err := s.writeState(st); err != nil {
		return nil, err
	}
	return doc, nil
}

// MarkCloudValidated records a successful cloud license confirmation.
func (s *Store) MarkCloudValidated(now time.Time) error {
	st := s.readState()
	st.LastCloudValidation = now
	if now.After(st.HighWater) {
		st.HighWater = now
	}
	return s.writeState(st)
}

// AddRevocation records an authenticated revocation notice. The notice
// itself must be verified by the caller (it arrives over the mutually
// authenticated sync channel or as a signed document).
func (s *Store) AddRevocation(licenseID string) error {
	rev := s.readRevoked()
	rev[licenseID] = true
	ids := make([]string, 0, len(rev))
	for id := range rev {
		ids = append(ids, id)
	}
	raw, _ := json.Marshal(ids)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path("revoked.json"), raw, 0o600)
}

var ErrNoLicense = errors.New("no license installed")

// Load returns the current verified document without evaluating state.
func (s *Store) Load() (*Document, error) {
	raw, err := os.ReadFile(s.path("current.json"))
	if err != nil {
		return nil, ErrNoLicense
	}
	env, err := DecodeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	return s.verifier.Verify(env)
}

// Evaluate loads the current document and computes its operational state,
// applying clock-rollback protection and updating the high-water mark.
func (s *Store) Evaluate(now time.Time) (Evaluation, error) {
	doc, err := s.Load()
	if err != nil {
		return Evaluation{State: StateExpired}, err
	}
	st := s.readState()
	effective := now
	rollback := false
	if st.HighWater.Sub(now) > clockRollbackTolerance {
		effective = st.HighWater
		rollback = true
	}
	if now.After(st.HighWater) {
		st.HighWater = now
		_ = s.writeState(st)
	}
	ev := Evaluate(doc, effective, st.LastCloudValidation, s.readRevoked()[doc.LicenseID])
	ev.ClockRollback = rollback
	return ev, nil
}
