package assignment

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// The durable, rollback-safe registry state: the current verified signed registry plus the previous one.
type registryRecord struct {
	Current   *SignedRegistry `json:"current"`
	Previous  *SignedRegistry `json:"previous,omitempty"`
	AdoptedAt string          `json:"adopted_at"`
}

// Persists the SIGNED trust registry: replaces the current one only with a validly-signed, higher-or-identical version, keeps the previous for rollback
// and never overwrites a good registry with a bad file. The last-known-good keeps verifying through a Central outage.
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

// The best last-known-good key registry: current if it verifies against the baked-in root, else previous; nil if NEITHER verifies, in which case the
// caller must refuse to trust anything rather than downgrade to an unsigned source.
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

// A present signed-registry file means the root-anchored regime is in force, so an invalid file must NEVER be silently replaced by the unauthenticated
// legacy plain trust file.
func (s *RegistryStore) FileExists() bool {
	_, err := os.Stat(s.recPath())
	return err == nil
}

func (s *RegistryStore) CurrentVersion() int64 {
	rec := s.Load()
	if rec == nil || rec.Current == nil {
		return 0
	}
	return rec.Current.RegistryVersion
}

// Validates and persists a fetched signed registry: the signature must verify against the baked-in root within its validity window; a LOWER version is
// rejected (rollback defence); an EQUAL version is accepted only if byte-identical; the outgoing registry is kept as previous and the write is atomic.
// Returns empty on adoption or benign no-op, else a rejection reason; a rejection NEVER replaces the current registry.
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
			return false, ""
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
