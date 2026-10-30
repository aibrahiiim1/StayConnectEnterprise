package assignment

import (
	"crypto/ed25519"
	"encoding/base64"
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

func (s *RegistryStore) recPath() string { return s.Path }

// adoptedMarkerPath is a one-way marker written when a signed registry is first adopted: from then on the unsigned plain trust file is never used,
// even if the signed registry file goes missing.
func (s *RegistryStore) adoptedMarkerPath() string { return s.Path + ".adopted" }

// EverAdopted reports whether a signed registry was ever adopted on this appliance.
func (s *RegistryStore) EverAdopted() bool {
	_, err := os.Stat(s.adoptedMarkerPath())
	return err == nil
}

// verifyAdopted checks an already-adopted registry: signature and expiry, but not NotBefore, which matters only at adoption. A clock that drifts back
// must not make the current registry "not yet valid" and push trust back to an older registry.
func verifyAdopted(rootPub ed25519.PublicKey, sr *SignedRegistry, now time.Time) (sigOK bool, reason string) {
	if sr == nil {
		return false, "no registry"
	}
	if sr.SignerKeyID != KeyID(rootPub) {
		return false, "registry signed by an unknown root key"
	}
	sig, err := base64.StdEncoding.DecodeString(sr.Signature)
	if err != nil || !ed25519.Verify(rootPub, regSigningBytes(sr), sig) {
		return false, "registry signature invalid (modified or wrong signer)"
	}
	if sr.NotAfter != 0 && now.Unix() > sr.NotAfter {
		return true, "registry expired"
	}
	return true, ""
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

// The best last-known-good key registry: the current one if it verifies against the baked-in root (NotBefore is not re-checked once adopted). The
// previous registry is used only when the current one's signature fails (a damaged file), never for a validity-window reason: the previous registry
// may still list a key the current one revoked. nil means the caller must refuse to trust anything rather than downgrade to an unsigned source.
func (s *RegistryStore) Trusted() *Registry {
	rec := s.Load()
	if rec == nil {
		return nil
	}
	now := time.Now()
	if rec.Current != nil {
		sigOK, reason := verifyAdopted(s.RootPub, rec.Current, now)
		if reason == "" {
			return rec.Current.Registry()
		}
		if sigOK {
			return nil
		}
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
	noop, reason := s.Check(sr, now)
	if reason != "" || noop {
		return false, reason
	}
	cur := s.Load()
	rec := &registryRecord{Current: sr, AdoptedAt: now.UTC().Format(time.RFC3339)}
	if cur != nil {
		rec.Previous = cur.Current
	}
	if err := s.save(rec); err != nil {
		return false, "persist failed: " + err.Error()
	}
	if !s.EverAdopted() {
		if err := writeDurable(s.adoptedMarkerPath(), []byte(rec.AdoptedAt+"\n"), 0o644); err != nil {
			return false, "persist failed: " + err.Error()
		}
	}
	return true, ""
}

// Check applies every rule Adopt applies, without writing: the signature must verify against the baked-in root within its validity window, a LOWER
// version than the held one is rejected, an EQUAL version only if byte-identical (noop=true: nothing to write). reason is empty when the registry
// may be adopted or is already held. Shared by Adopt and by callers that must vet a registry before a multi-step apply (offline first activation).
func (s *RegistryStore) Check(sr *SignedRegistry, now time.Time) (noop bool, reason string) {
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
			return true, ""
		}
	}
	return false, ""
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
	return writeDurable(s.recPath(), b, 0o644)
}
