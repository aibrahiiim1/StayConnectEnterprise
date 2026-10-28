package license

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Persisted version and clock high-water marks prevent license replay and clock rollback.
type Store struct {
	dir      string
	verifier *Verifier
	// mu makes each read-modify-write of state.json and revoked.json atomic within the process.
	mu sync.Mutex
}

var ErrRollback = errors.New("LICENSE_ROLLBACK_REJECTED: license rollback rejected")

// ErrStateDamaged means a licence state file exists but cannot be read or parsed. It is never read as "empty",
// which would silently switch off version, clock and revocation protection.
var ErrStateDamaged = errors.New("LICENSE_STATE_DAMAGED: licence state file unreadable")

const clockRollbackTolerance = 48 * time.Hour

const (
	stateFile   = "state.json"
	currentFile = "current.json"
	revokedFile = "revoked.json"
	prevSuffix  = ".prev"
)

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

// readFileOrPrev reads name, falling back to its .prev copy when the current file is damaged. A missing file (and
// no .prev) reports os.ErrNotExist; anything else unreadable or unparsable is ErrStateDamaged.
func (s *Store) readFileOrPrev(name string, parse func([]byte) error) error {
	raw, err := os.ReadFile(s.path(name))
	if err == nil {
		if err = parse(raw); err == nil {
			return nil
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if _, perr := os.Stat(s.path(name + prevSuffix)); errors.Is(perr, os.ErrNotExist) {
			return os.ErrNotExist
		}
	}
	prev, perr := os.ReadFile(s.path(name + prevSuffix))
	if perr == nil && parse(prev) == nil {
		slog.Warn("license: state file damaged, using previous copy", "file", name, "err", err)
		return nil
	}
	return fmt.Errorf("%w: %s: %v", ErrStateDamaged, name, err)
}

func (s *Store) readState() (storeState, error) {
	var st storeState
	err := s.readFileOrPrev(stateFile, func(raw []byte) error {
		var tmp storeState
		if err := json.Unmarshal(raw, &tmp); err != nil {
			return err
		}
		st = tmp
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return storeState{}, nil
	}
	return st, err
}

func (s *Store) writeState(st storeState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.writeDurable(stateFile, raw)
}

func (s *Store) readRevoked() (map[string]bool, error) {
	out := map[string]bool{}
	err := s.readFileOrPrev(revokedFile, func(raw []byte) error {
		var ids []string
		if err := json.Unmarshal(raw, &ids); err != nil {
			return err
		}
		out = map[string]bool{}
		for _, id := range ids {
			out[id] = true
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	return out, err
}

// writeDurable replaces name crash-safely: the current file is kept as .prev, the new content goes to a unique temp
// file that is synced before the rename, and the directory is synced after it.
func (s *Store) writeDurable(name string, raw []byte) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	dst := s.path(name)
	// Only a well-formed current file becomes .prev, so a damaged file never replaces the good fallback.
	if cur, err := os.ReadFile(dst); err == nil && json.Valid(cur) {
		if err := writeSynced(s.dir, dst+prevSuffix, cur); err != nil {
			return err
		}
	}
	return writeSynced(s.dir, dst, raw)
}

func writeSynced(dir, dst string, raw []byte) error {
	f, err := os.CreateTemp(dir, filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
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
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func advanceHighWater(st *storeState, now time.Time) bool {
	if !now.After(st.HighWater) {
		return false
	}
	st.HighWater = now
	return true
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
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readState()
	if err != nil {
		return nil, err
	}
	revoked, err := s.readRevoked()
	if err != nil {
		return nil, err
	}
	fpr := fmt.Sprintf("%x", sha256.Sum256([]byte(env.PayloadB64)))

	// Revocation overrides a valid signature and future expiry.
	if revoked[doc.LicenseID] {
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
	// license_version is the rollback guard for versioned licences, so a newer version is never refused because an
	// earlier licence carried a later issued_at (vendor clock ahead). Unversioned licences keep the issued_at guard.
	newerVersion := doc.LicenseVersion > 0 && doc.LicenseVersion > st.HighestVersion
	if !newerVersion && !st.InstalledIssuedAt.IsZero() && doc.IssuedAt.Before(st.InstalledIssuedAt) {
		return nil, fmt.Errorf("%w: incoming issued_at %s older than installed %s",
			ErrRollback, doc.IssuedAt.Format(time.RFC3339), st.InstalledIssuedAt.Format(time.RFC3339))
	}
	if err := s.writeDurable(currentFile, raw); err != nil {
		return nil, err
	}
	st.InstalledIssuedAt = doc.IssuedAt
	if doc.LicenseVersion >= st.HighestVersion {
		st.HighestVersion = doc.LicenseVersion
		st.AcceptedLicenseID = doc.LicenseID
		st.AcceptedFingerprint = fpr
	}
	advanceHighWater(&st, now)
	if err := s.writeState(st); err != nil {
		return nil, err
	}
	return doc, nil
}

func (s *Store) MarkCloudValidated(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readState()
	if err != nil {
		return err
	}
	st.LastCloudValidation = now
	advanceHighWater(&st, now)
	return s.writeState(st)
}

// The caller must authenticate the revocation notice before storing its license ID.
func (s *Store) AddRevocation(licenseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rev, err := s.readRevoked()
	if err != nil {
		return err
	}
	if rev[licenseID] {
		return nil
	}
	rev[licenseID] = true
	ids := make([]string, 0, len(rev))
	for id := range rev {
		ids = append(ids, id)
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return s.writeDurable(revokedFile, raw)
}

var ErrNoLicense = errors.New("no license installed")

func (s *Store) Load() (*Document, error) {
	var doc *Document
	err := s.readFileOrPrev(currentFile, func(raw []byte) error {
		env, err := DecodeEnvelope(raw)
		if err != nil {
			return err
		}
		d, err := s.verifier.Verify(env)
		if err != nil {
			return err
		}
		doc = d
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoLicense
	}
	if err != nil {
		return nil, err
	}
	// The document read may be the previous copy (readFileOrPrev). It must not be older than what the store has
	// accepted: a damaged current.json would otherwise restore superseded terms (for example an Active v1 after a
	// Suspended v2), the very rollback the version guard exists to refuse. Fail closed instead.
	st, err := s.readState()
	if err != nil {
		return nil, err
	}
	if stale := supersededByState(doc, st); stale != "" {
		return nil, fmt.Errorf("%w: %s: %s", ErrStateDamaged, currentFile, stale)
	}
	return doc, nil
}

// supersededByState reports why doc is older than the licence the store last accepted, or "" when it is not.
func supersededByState(doc *Document, st storeState) string {
	if st.HighestVersion > 0 {
		if doc.LicenseVersion < st.HighestVersion {
			return fmt.Sprintf("license_version %d is older than the accepted %d", doc.LicenseVersion, st.HighestVersion)
		}
		if doc.LicenseVersion == st.HighestVersion && st.AcceptedLicenseID != "" && doc.LicenseID != st.AcceptedLicenseID {
			return fmt.Sprintf("license %s is not the accepted %s at version %d", doc.LicenseID, st.AcceptedLicenseID,
				doc.LicenseVersion)
		}
		return ""
	}
	if !st.InstalledIssuedAt.IsZero() && doc.IssuedAt.Before(st.InstalledIssuedAt) {
		return fmt.Sprintf("issued_at %s is older than the installed %s", doc.IssuedAt.Format(time.RFC3339),
			st.InstalledIssuedAt.Format(time.RFC3339))
	}
	return ""
}

// Evaluation uses the high-water mark if the system clock moves back beyond tolerance.
func (s *Store) Evaluate(now time.Time) (Evaluation, error) {
	doc, err := s.Load()
	if err != nil {
		return Evaluation{State: StateExpired}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readState()
	if err != nil {
		return Evaluation{State: StateExpired}, err
	}
	revoked, err := s.readRevoked()
	if err != nil {
		return Evaluation{State: StateExpired}, err
	}
	effective := now
	rollback := false
	if st.HighWater.Sub(now) > clockRollbackTolerance {
		effective = st.HighWater
		rollback = true
	}
	if advanceHighWater(&st, now) {
		if err := s.writeState(st); err != nil {
			slog.Warn("license: high-water write failed", "err", err)
		}
	}
	ev := Evaluate(doc, effective, st.LastCloudValidation, revoked[doc.LicenseID])
	ev.ClockRollback = rollback
	return ev, nil
}
