package assignment

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type registryRecord struct {
	Current   *SignedRegistry `json:"current"`
	Previous  *SignedRegistry `json:"previous,omitempty"`
	AdoptedAt string          `json:"adopted_at"`
}

// RegistryStore persists the signed trust registry. It only replaces the current one with a validly signed,
// higher-or-identical version and keeps the previous for rollback, so the last-known-good registry keeps
// verifying assignments during a Central outage.
type RegistryStore struct {
	Path    string
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
	// The file may be a bare SignedRegistry.
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

// Trusted returns the current registry if it verifies against the baked-in root, else the previous one. It
// returns nil if neither verifies; the caller must then trust nothing rather than downgrade to an unsigned source.
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

// FileExists means the root-anchored signed regime is in force, so an invalid file must never be silently
// replaced by the unauthenticated plain trust file.
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

// Adopt validates and persists a fetched registry: the signature must verify against the baked-in root within
// its validity window, a lower version is rejected (rollback), an equal version only if byte-identical, and the
// outgoing registry is kept as previous. A rejection never replaces the current registry.
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
