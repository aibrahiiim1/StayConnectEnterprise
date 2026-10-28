package portaltls

import (
	"bytes"
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/portaltls/portaltlstest"
)

const pairHost = "ab12cd34.gw.onegate.work"

func pairFixture(t *testing.T) (*Store, *portaltlstest.CA, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	ca, err := portaltlstest.New(now)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{
		Dir:       t.TempDir(),
		LookupGID: func(string) (int, bool) { return 4242, true },
		Chown:     func(string, int, int) error { return nil },
		Now:       func() time.Time { return now },
		Roots:     ca.Pool(),
	}, ca, now
}

func issue(t *testing.T, s *Store, ca *portaltlstest.CA, now time.Time) []byte {
	t.Helper()
	csr, err := s.NewPendingKey(pairHost)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := ca.IssueForCSR(csr, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return chain
}

// P2 M13: a chain write that fails after the key was written leaves the previously installed, matching pair on disk.
func TestFailedChainWriteKeepsThePreviousPair(t *testing.T) {
	s, ca, now := pairFixture(t)
	if _, err := s.Install(pairHost, issue(t, s, ca, now)); err != nil {
		t.Fatal(err)
	}
	oldKey, _ := os.ReadFile(s.KeyPath(pairHost))
	oldCert, _ := os.ReadFile(s.CertPath(pairHost))

	next := issue(t, s, ca, now)
	s.certWriter = func(string, []byte) error { return errors.New("disk full") }
	if _, err := s.Install(pairHost, next); err == nil {
		t.Fatal("install with a failing chain write must fail")
	}
	key, _ := os.ReadFile(s.KeyPath(pairHost))
	cert, _ := os.ReadFile(s.CertPath(pairHost))
	if !bytes.Equal(key, oldKey) || !bytes.Equal(cert, oldCert) {
		t.Fatal("the previous pair must be on disk after a failed install")
	}
	if _, err := tls.LoadX509KeyPair(s.CertPath(pairHost), s.KeyPath(pairHost)); err != nil {
		t.Fatalf("the pair on disk must match: %v", err)
	}
	if !s.HasPending(pairHost) {
		t.Fatal("the pending key must stay until both files are installed")
	}
	// The retry with a working disk installs the new pair.
	s.certWriter = nil
	if _, err := s.Install(pairHost, next); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// P2 L19: stale temporary files (possibly key bytes) are removed when the directory is ensured.
func TestEnsureDirRemovesStaleTemporaries(t *testing.T) {
	s, _, _ := pairFixture(t)
	stale := filepath.Join(s.Dir, "."+pairHost+".key.tmp-123")
	if err := os.WriteFile(stale, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a stale temporary file survived")
	}
}

// P2 L19: Inspect refuses an installed certificate that does not name the host.
func TestInspectChecksTheHostName(t *testing.T) {
	s, ca, now := pairFixture(t)
	if _, err := s.Install(pairHost, issue(t, s, ca, now)); err != nil {
		t.Fatal(err)
	}
	other := "zz99yy88.gw.onegate.work"
	for _, ext := range []string{".key", ".crt"} {
		b, _ := os.ReadFile(filepath.Join(s.Dir, pairHost+ext))
		if err := os.WriteFile(filepath.Join(s.Dir, other+ext), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Inspect(other); err == nil {
		t.Fatal("a certificate for another host was accepted")
	}
	if _, err := s.Inspect(pairHost); err != nil {
		t.Fatalf("own host: %v", err)
	}
}
