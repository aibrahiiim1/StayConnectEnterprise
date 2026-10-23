package license

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Persisted version and clock high-water marks prevent license replay and clock rollback.
type Store struct {
	dir      string
	verifier *Verifier
}

var ErrRollback = errors.New("LICENSE_ROLLBACK_REJECTED: license rollback rejected")

const clockRollbackTolerance = 48 * time.Hour

type storeState struct {
	InstalledIssuedAt time.Time `json:"installed_issued_at"`
	// Persists across restarts so a valid signature cannot replay an older grant.
	HighestVersion int64 `json:"highest_version"`
	// The same version is accepted only for the identical document.
	AcceptedLicenseID   string    `json:"accepted_license_id"`
	AcceptedFingerprint string    `json:"accepted_fingerprint"`
	HighWater           time.Time `json:"high_water"`
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

// Verify leaves storage untouched so binding checks can reject a license before installation.
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
	fpr := fmt.Sprintf("%x", sha256.Sum256([]byte(env.PayloadB64)))

	// Revocation overrides a valid signature and future expiry.
	if s.readRevoked()[doc.LicenseID] {
		return nil, fmt.Errorf("%w: license %s was revoked", ErrRollback, doc.LicenseID)
	}
	// Only an identical re-import may reuse a version; unversioned licenses cannot replace versioned ones.
	if st.HighestVersion > 0 {
		switch {
		case doc.LicenseVersion < st.HighestVersion:
			return nil, fmt.Errorf("%w: license_version %d older than accepted %d (license %s superseded)",
				ErrRollback, doc.LicenseVersion, st.HighestVersion, doc.LicenseID)
		case doc.LicenseVersion == st.HighestVersion &&
			(doc.LicenseID != st.AcceptedLicenseID || (st.AcceptedFingerprint != "" && fpr != st.AcceptedFingerprint)):
			return nil, fmt.Errorf("%w: different document at already-accepted version %d",
				ErrRollback, doc.LicenseVersion)
		}
	}
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
	if doc.LicenseVersion >= st.HighestVersion {
		st.HighestVersion = doc.LicenseVersion
		st.AcceptedLicenseID = doc.LicenseID
		st.AcceptedFingerprint = fpr
	}
	if now.After(st.HighWater) {
		st.HighWater = now
	}
	if err := s.writeState(st); err != nil {
		return nil, err
	}
	return doc, nil
}

func (s *Store) MarkCloudValidated(now time.Time) error {
	st := s.readState()
	st.LastCloudValidation = now
	if now.After(st.HighWater) {
		st.HighWater = now
	}
	return s.writeState(st)
}

// The caller must authenticate the revocation notice before storing its license ID.
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

// Evaluation uses the high-water mark if the system clock moves back beyond tolerance.
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
