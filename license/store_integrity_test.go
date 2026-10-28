package license

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func signedVersion(t *testing.T, s *Signer, issued time.Time, ver int64, id string) []byte {
	t.Helper()
	d := testDoc(issued)
	d.LicenseID = id
	d.LicenseVersion = ver
	env, err := s.Sign(d)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := env.Encode()
	return raw
}

func readStateFile(t *testing.T, dir string) storeState {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		t.Fatal(err)
	}
	var st storeState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// P2 H1: concurrent Install and Evaluate never lower the accepted version.
func TestStoreConcurrentInstallEvaluateKeepsHighestVersion(t *testing.T) {
	s := newSigner(t)
	dir := t.TempDir()
	store := NewStore(dir, NewVerifier(s.PublicKey()))
	now := time.Now().UTC()
	const n = 30
	raws := make([][]byte, n+1)
	for v := 1; v <= n; v++ {
		raws[v] = signedVersion(t, s, now.Add(-time.Hour), int64(v), fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", v))
	}
	if _, err := store.Install(raws[1], now); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = store.Evaluate(now)
				}
			}
		}()
	}
	for v := 2; v <= n; v++ {
		if _, err := store.Install(raws[v], now); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("install v%d: %v", v, err)
		}
	}
	close(stop)
	wg.Wait()
	if got := readStateFile(t, dir).HighestVersion; got != n {
		t.Fatalf("HighestVersion = %d, want %d", got, n)
	}
	if _, err := store.Install(raws[n-1], now); !errors.Is(err, ErrRollback) {
		t.Fatalf("older version after concurrent run: want ErrRollback, got %v", err)
	}
}

// P2 H1: a damaged state.json without a usable previous copy fails closed instead of reading as empty.
func TestStoreDamagedStateFailsClosed(t *testing.T) {
	s := newSigner(t)
	dir := t.TempDir()
	store := NewStore(dir, NewVerifier(s.PublicKey()))
	now := time.Now().UTC()
	v5 := signedVersion(t, s, now.Add(-time.Hour), 5, "aaaaaaaa-0000-0000-0000-000000000005")
	v3 := signedVersion(t, s, now.Add(-time.Hour), 3, "aaaaaaaa-0000-0000-0000-000000000003")
	if _, err := store.Install(v5, now); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{stateFile, stateFile + prevSuffix} {
		_ = os.WriteFile(filepath.Join(dir, f), []byte(`{"highest_ver`), 0o600)
	}
	if _, err := store.Install(v3, now); !errors.Is(err, ErrStateDamaged) {
		t.Fatalf("install over damaged state: want ErrStateDamaged, got %v", err)
	}
	if _, err := store.Evaluate(now); !errors.Is(err, ErrStateDamaged) {
		t.Fatalf("evaluate over damaged state: want ErrStateDamaged, got %v", err)
	}
}

// P2 H1: a damaged state.json falls back to the previous copy, which still refuses a lower version.
func TestStoreDamagedStateUsesPreviousCopy(t *testing.T) {
	s := newSigner(t)
	dir := t.TempDir()
	store := NewStore(dir, NewVerifier(s.PublicKey()))
	now := time.Now().UTC()
	v4 := signedVersion(t, s, now.Add(-time.Hour), 4, "aaaaaaaa-0000-0000-0000-000000000004")
	v5 := signedVersion(t, s, now.Add(-time.Hour), 5, "aaaaaaaa-0000-0000-0000-000000000005")
	v3 := signedVersion(t, s, now.Add(-time.Hour), 3, "aaaaaaaa-0000-0000-0000-000000000003")
	if _, err := store.Install(v4, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(v5, now); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(v3, now); !errors.Is(err, ErrRollback) {
		t.Fatalf("lower version with damaged state: want ErrRollback, got %v", err)
	}
}

// P2 H1: a damaged revoked.json never forgets revocations.
func TestStoreDamagedRevocationsFailClosed(t *testing.T) {
	s := newSigner(t)
	dir := t.TempDir()
	store := NewStore(dir, NewVerifier(s.PublicKey()))
	now := time.Now().UTC()
	raw := signedVersion(t, s, now.Add(-time.Hour), 1, "aaaaaaaa-0000-0000-0000-000000000001")
	if err := store.AddRevocation("aaaaaaaa-0000-0000-0000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, revokedFile), []byte("[\"aaaa"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(raw, now); err == nil {
		t.Fatal("install with damaged revoked.json must fail")
	}
	if _, err := store.Evaluate(now); err == nil {
		t.Fatal("evaluate with damaged revoked.json must fail")
	}
	// A new revocation must not replace the damaged list with a list that forgets the old entries.
	if err := store.AddRevocation("bbbbbbbb-0000-0000-0000-000000000002"); !errors.Is(err, ErrStateDamaged) {
		t.Fatalf("AddRevocation over damaged list: want ErrStateDamaged, got %v", err)
	}
	// With a good previous copy, the damaged current file is read through it.
	if err := os.WriteFile(filepath.Join(dir, revokedFile+prevSuffix), []byte(`["aaaaaaaa-0000-0000-0000-000000000001"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRevocation("bbbbbbbb-0000-0000-0000-000000000002"); err != nil {
		t.Fatal(err)
	}
	rev, err := store.readRevoked()
	if err != nil {
		t.Fatal(err)
	}
	if !rev["aaaaaaaa-0000-0000-0000-000000000001"] || !rev["bbbbbbbb-0000-0000-0000-000000000002"] {
		t.Fatalf("revocations lost: %v", rev)
	}
}

// P2 H1: an interrupted write (temp file left, no rename) leaves the old revocation list intact.
func TestStoreInterruptedRevocationWriteKeepsOldList(t *testing.T) {
	s := newSigner(t)
	dir := t.TempDir()
	store := NewStore(dir, NewVerifier(s.PublicKey()))
	if err := store.AddRevocation("aaaaaaaa-0000-0000-0000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, revokedFile+".tmp-crash"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	rev, err := store.readRevoked()
	if err != nil || !rev["aaaaaaaa-0000-0000-0000-000000000001"] {
		t.Fatalf("old list lost: %v %v", rev, err)
	}
}

// P2 M2: a newer licence version installs even when an earlier one carried a later issued_at.
func TestStoreNewerVersionWithEarlierIssuedAtInstalls(t *testing.T) {
	s := newSigner(t)
	store := NewStore(t.TempDir(), NewVerifier(s.PublicKey()))
	now := time.Now().UTC()
	v5 := signedVersion(t, s, now.AddDate(0, 0, 10), 5, "aaaaaaaa-0000-0000-0000-000000000005")
	v6 := signedVersion(t, s, now, 6, "aaaaaaaa-0000-0000-0000-000000000006")
	if _, err := store.Install(v5, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(v6, now); err != nil {
		t.Fatalf("v6 after future-dated v5: %v", err)
	}
	// The version guard still refuses v5 again.
	if _, err := store.Install(v5, now); !errors.Is(err, ErrRollback) {
		t.Fatalf("v5 replay: want ErrRollback, got %v", err)
	}
}

// P2 L11: a wrong-length vendor key never panics.
func TestVerifierWrongLengthKeyDoesNotPanic(t *testing.T) {
	v := NewVerifier(ed25519.PublicKey(make([]byte, 31)))
	if err := v.AddKey(make([]byte, 33)); !errors.Is(err, ErrBadKey) {
		t.Fatalf("AddKey: want ErrBadKey, got %v", err)
	}
	if _, err := v.Verify(&Envelope{KeyID: KeyIDFor(make([]byte, 31))}); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Verify: want ErrUnknownKey, got %v", err)
	}
}

// P2 L11: GenerateVendorKey never overwrites an existing key.
func TestGenerateVendorKeyRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vendor.key")
	if _, err := GenerateVendorKey(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := GenerateVendorKey(path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second generate: want ErrExist, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("existing vendor key was overwritten")
	}
}

// P2 L12: grace days and the validity dates are bounded.
func TestValidateBoundsGraceAndDates(t *testing.T) {
	now := time.Now().UTC()
	d := testDoc(now)
	d.GracePeriodDays = 366
	if err := d.Validate(); err == nil {
		t.Fatal("grace_period_days 366 must be refused")
	}
	d = testDoc(now)
	d.ValidFrom = d.ValidUntil.Add(time.Hour)
	if err := d.Validate(); err == nil {
		t.Fatal("valid_from after valid_until must be refused")
	}
	d = testDoc(now)
	d.GracePeriodDays = 365
	d.ValidFrom = now
	if err := d.Validate(); err != nil {
		t.Fatalf("in-range document refused: %v", err)
	}
}

// P2 L13: only a missing file is "no licence"; an unreadable one is reported.
func TestLoadDistinguishesMissingFromDamaged(t *testing.T) {
	s := newSigner(t)
	dir := t.TempDir()
	store := NewStore(dir, NewVerifier(s.PublicKey()))
	if _, err := store.Load(); !errors.Is(err, ErrNoLicense) {
		t.Fatalf("missing: want ErrNoLicense, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, currentFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || errors.Is(err, ErrNoLicense) {
		t.Fatalf("damaged: want a non-ErrNoLicense error, got %v", err)
	}
}
