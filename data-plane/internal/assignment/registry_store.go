package assignment

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// registryRecord is the appliance's durable, rollback-safe registry state:
// the current verified signed registry plus the previous one.
type registryRecord struct {
	Current   *SignedRegistry `json:"current"`
	Previous  *SignedRegistry `json:"previous,omitempty"`
	AdoptedAt string          `json:"adopted_at"`
}

// RegistryStore persists the SIGNED trust registry. It only ever replaces the
// current registry with a validly-signed, higher-or-identical version, keeps the
// previous one for rollback, and never overwrites a good registry with a bad file.
// During a Central outage the last-known-good registry keeps verifying assignments.
type RegistryStore struct {
	Path    string            // signed registry file
	RootPub ed25519.PublicKey // baked-in trust anchor
}

func (s *RegistryStore) recPath() string {
	if filepath.Ext(s.Path) == ".json" {
		return s.Path
	}
	return s.Path
}

// Load returns the persisted signed registry record (nil if none/invalid).
func (s *RegistryStore) Load() *registryRecord {
	b, err := os.ReadFile(s.recPath())
	if err != nil {
		return nil
	}
	var rec registryRecord
	if json.Unmarshal(b, &rec) == nil && rec.Current != nil {
		return &rec
	}
	// Legacy: file may be a bare SignedRegistry.
	var sr SignedRegistry
	if json.Unmarshal(b, &sr) == nil && sr.RegistryVersion > 0 {
		return &registryRecord{Current: &sr}
	}
	return nil
}

// Current returns the inner key registry from the last verified signed registry,
// or nil if none is trusted.
func (s *RegistryStore) Current() *Registry {
	rec := s.Load()
	if rec == nil || rec.Current == nil {
		return nil
	}
	if VerifyRegistry(s.RootPub, rec.Current, time.Now()) != "" {
		return nil
	}
	return rec.Current.Registry()
}

// Trusted returns the best last-known-good key registry: the current signed
// registry if it verifies against the baked-in root, else the previous one if it
// verifies. Returns nil if NEITHER verifies (tamper/corruption) — the caller must
// then refuse to trust anything rather than downgrade to an unsigned source.
func (s *RegistryStore) Trusted() *Registry {
	rec := s.Load()
	if rec == nil {
		return nil
	}
	now := time.Now()
	if rec.Current != nil && VerifyRegistry(s.RootPub, rec.Current, now) == "" {
		return rec.Current.Registry()
	}
	if rec.Previous != nil && VerifyRegistry(s.RootPub, rec.Previous, now) == "" {
		return rec.Previous.Registry()
	}
	return nil
}

// FileExists reports whether a signed-registry record file is present on disk. Its
// presence means the root-anchored signed regime is in force, so an invalid file
// must NEVER be silently replaced by the unauthenticated legacy plain trust file.
func (s *RegistryStore) FileExists() bool {
	_, err := os.Stat(s.recPath())
	return err == nil
}

// CurrentVersion is the persisted registry version (0 if none).
func (s *RegistryStore) CurrentVersion() int64 {
	rec := s.Load()
	if rec == nil || rec.Current == nil {
		return 0
	}
	return rec.Current.RegistryVersion
}

// Adopt validates and persists a fetched signed registry. Rules:
//   - signature must verify against the baked-in root, within its validity window
//   - a LOWER version is rejected (rollback defence)
//   - an EQUAL version is accepted only if byte-identical (idempotent refresh)
//   - the outgoing registry is kept as previous; the write is atomic
//
// Returns "" on adoption (or benign no-op), else a rejection reason. A rejection
// NEVER replaces the current registry — the last-known-good stays in force.
func (s *RegistryStore) Adopt(sr *SignedRegistry, now time.Time) (adopted bool, reason string) {
	if reason := VerifyRegistry(s.RootPub, sr, now); reason != "" {
		return false, reason
	}
	cur := s.Load()
	if cur != nil && cur.Current != nil {
		if sr.RegistryVersion < cur.Current.RegistryVersion {
			return false, "registry rollback rejected (lower version than current)"
		}
		if sr.RegistryVersion == cur.Current.RegistryVersion {
			a, _ := json.Marshal(sr)
			b, _ := json.Marshal(cur.Current)
			if string(a) != string(b) {
				return false, "registry version reuse with different content rejected"
			}
			return false, "" // identical — nothing to do
		}
	}
	rec := &registryRecord{Current: sr, AdoptedAt: now.UTC().Format(time.RFC3339)}
	if cur != nil {
		rec.Previous = cur.Current
	}
	if err := s.save(rec); err != nil {
		return false, "persist failed: " + err.Error()
	}
	return true, ""
}

func (s *RegistryStore) save(rec *registryRecord) error {
	if rec == nil || rec.Current == nil {
		return errors.New("nil registry")
	}
	if err := os.MkdirAll(filepath.Dir(s.recPath()), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.recPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.recPath())
}
