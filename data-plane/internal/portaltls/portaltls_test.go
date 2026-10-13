package portaltls_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/portaltls"
	"github.com/stayconnect/enterprise/data-plane/internal/portaltls/portaltlstest"
)

const host = "ab12cd34.gw.onegate.work"

func fixture(t *testing.T) (*portaltls.Store, *portaltlstest.CA, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	ca, err := portaltlstest.New(now)
	if err != nil {
		t.Fatal(err)
	}
	var chowned []string
	s := &portaltls.Store{
		Dir:       t.TempDir(),
		LookupGID: func(string) (int, bool) { return 4242, true },
		Chown:     func(p string, _, gid int) error { chowned = append(chowned, p); return nil },
		Now:       func() time.Time { return now },
		Roots:     ca.Pool(),
	}
	return s, ca, now
}

func parseCSR(t *testing.T, b []byte) *x509.CertificateRequest {
	t.Helper()
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("not a CSR PEM: %q", b)
	}
	csr, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatal(err)
	}
	return csr
}

// THE CSR NAMES EXACTLY ONE DNS NAME AND CARRIES A P-256 KEY -- what Central's validation demands.
func TestTheCSRNamesExactlyOneHostAndCarriesAP256Key(t *testing.T) {
	s, _, _ := fixture(t)
	csrPEM, err := s.NewPendingKey(host)
	if err != nil {
		t.Fatal(err)
	}
	csr := parseCSR(t, csrPEM)
	if len(csr.DNSNames) != 1 || csr.DNSNames[0] != host {
		t.Fatalf("DNS names %v, want exactly [%s]", csr.DNSNames, host)
	}
	if csr.Subject.CommonName != "" || len(csr.IPAddresses) != 0 || len(csr.EmailAddresses) != 0 || len(csr.URIs) != 0 {
		t.Fatalf("the CSR must carry no other name: %+v", csr.Subject)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Fatalf("want an ECDSA P-256 key, got %T", csr.PublicKey)
	}
	if !s.HasPending(host) {
		t.Fatal("the key must be kept as the pending key until its certificate installs")
	}
	// The same pending key rebuilds the same public key into a new CSR.
	again, err := s.PendingCSR(host)
	if err != nil {
		t.Fatal(err)
	}
	if !parseCSR(t, again).PublicKey.(*ecdsa.PublicKey).Equal(pub) {
		t.Fatal("PendingCSR must reuse the pending key")
	}
}

func TestEveryNewPendingKeyIsANewKey(t *testing.T) {
	s, _, _ := fixture(t)
	a, _ := s.NewPendingKey(host)
	b, _ := s.NewPendingKey(host)
	if parseCSR(t, a).PublicKey.(*ecdsa.PublicKey).Equal(parseCSR(t, b).PublicKey) {
		t.Fatal("every renewal must use a new key")
	}
}

func TestInstallVerifiesAndInstallsAtomically(t *testing.T) {
	s, ca, now := fixture(t)
	csrPEM, _ := s.NewPendingKey(host)
	chain, err := ca.IssueForCSR(csrPEM, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := s.Install(host, chain)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if inst.Serial == "" || inst.Fingerprint == "" || !inst.NotAfter.Equal(now.Add(90*24*time.Hour).Truncate(time.Second)) {
		t.Fatalf("description: %+v", inst)
	}
	if s.HasPending(host) {
		t.Fatal("the pending key must be gone once installed")
	}
	// The installed pair loads as a TLS key pair -- what portald does.
	if _, err := tls.LoadX509KeyPair(s.CertPath(host), s.KeyPath(host)); err != nil {
		t.Fatalf("installed pair does not load: %v", err)
	}
	got, err := s.Inspect(host)
	if err != nil || got.Serial != inst.Serial {
		t.Fatalf("inspect: %v %+v", err, got)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{s.KeyPath(host): 0o640, s.CertPath(host): 0o644} {
			fi, err := os.Stat(p)
			if err != nil || fi.Mode().Perm() != want {
				t.Errorf("%s: mode %v, want %v", filepath.Base(p), fi.Mode().Perm(), want)
			}
		}
	}
	// No temporary file is left behind.
	ents, _ := os.ReadDir(s.Dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("stray temporary %s", e.Name())
		}
	}
}

func TestInstallRefusesWhatMustNotBeServed(t *testing.T) {
	s, ca, now := fixture(t)
	csrPEM, _ := s.NewPendingKey(host)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	stranger, _ := portaltlstest.New(now)

	wrongHost, _ := ca.Issue([]string{"zz99yy88.gw.onegate.work"}, parseCSR(t, csrPEM).PublicKey, now.Add(-time.Hour), now.Add(time.Hour))
	wrongKey, _ := ca.Issue([]string{host}, &other.PublicKey, now.Add(-time.Hour), now.Add(time.Hour))
	expired, _ := ca.IssueForCSR(csrPEM, now.Add(-48*time.Hour), now.Add(-time.Hour))
	future, _ := ca.IssueForCSR(csrPEM, now.Add(time.Hour), now.Add(48*time.Hour))
	untrusted, _ := stranger.IssueForCSR(csrPEM, now.Add(-time.Hour), now.Add(time.Hour))
	good, _ := ca.IssueForCSR(csrPEM, now.Add(-time.Hour), now.Add(time.Hour))
	leafOnly := good[:strings.Index(string(good), "-----END CERTIFICATE-----")+len("-----END CERTIFICATE-----\n")]
	keyPEM, _ := os.ReadFile(s.PendingKeyPath(host))
	withKey := append(append([]byte{}, good...), keyPEM...)

	for name, chain := range map[string][]byte{
		"wrong host":      wrongHost,
		"wrong key":       wrongKey,
		"expired":         expired,
		"not yet valid":   future,
		"untrusted root":  untrusted,
		"no intermediate": []byte(leafOnly),
		"carries a key":   withKey,
		"garbage":         []byte("not a chain"),
		"empty":           nil,
	} {
		if _, err := s.Install(host, chain); err == nil {
			t.Errorf("%s: installed, must be refused", name)
		}
		if _, err := os.Stat(s.CertPath(host)); err == nil {
			t.Fatalf("%s: a certificate file was written for a refused chain", name)
		}
		if !s.HasPending(host) {
			t.Fatalf("%s: a refused install must leave the pending key for the caller to decide", name)
		}
	}
}

func TestInstallWithoutAPendingKeyIsRefused(t *testing.T) {
	s, ca, now := fixture(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	chain, _ := ca.Issue([]string{host}, &k.PublicKey, now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := s.Install(host, chain); !errors.Is(err, portaltls.ErrNoPendingKey) {
		t.Fatalf("got %v, want ErrNoPendingKey", err)
	}
}

func TestInspectDetectsAMissingFileAndAMismatchedPair(t *testing.T) {
	s, ca, now := fixture(t)
	if _, err := s.Inspect(host); !errors.Is(err, portaltls.ErrNotInstalled) {
		t.Fatalf("nothing installed: %v", err)
	}
	csrPEM, _ := s.NewPendingKey(host)
	chain, _ := ca.IssueForCSR(csrPEM, now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := s.Install(host, chain); err != nil {
		t.Fatal(err)
	}
	// A key replaced underneath the certificate.
	if _, err := s.NewPendingKey(host); err != nil {
		t.Fatal(err)
	}
	pend, _ := os.ReadFile(s.PendingKeyPath(host))
	if err := os.WriteFile(s.KeyPath(host), pend, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inspect(host); !errors.Is(err, portaltls.ErrKeyMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	_ = os.Remove(s.KeyPath(host))
	if _, err := s.Inspect(host); !errors.Is(err, portaltls.ErrNotInstalled) {
		t.Fatalf("key missing: %v", err)
	}
}

func TestHostsAndRemove(t *testing.T) {
	s, _, _ := fixture(t)
	_, _ = s.NewPendingKey(host)
	_, _ = s.NewPendingKey("wifi.nilehotel.com")
	_ = os.WriteFile(filepath.Join(s.Dir, "README"), []byte("x"), 0o644)
	hs, err := s.Hosts()
	if err != nil || len(hs) != 2 || hs[0] != host || hs[1] != "wifi.nilehotel.com" {
		t.Fatalf("hosts %v %v", hs, err)
	}
	if err := s.Remove("wifi.nilehotel.com"); err != nil {
		t.Fatal(err)
	}
	if hs, _ = s.Hosts(); len(hs) != 1 {
		t.Fatalf("after remove: %v", hs)
	}
}

func TestHostnamesThatCouldEscapeTheDirectoryAreRefused(t *testing.T) {
	s, _, _ := fixture(t)
	for _, h := range []string{"../etc/passwd", "a/b.example", "UPPER.example.com", "example.com.", "x", "", "a..b.com"} {
		if portaltls.ValidHostname(h) {
			t.Errorf("%q accepted", h)
		}
		if _, err := s.NewPendingKey(h); err == nil {
			t.Errorf("%q: key written", h)
		}
	}
}

func TestAMissingKeyGroupFallsBackToRootOnlyAndSaysSo(t *testing.T) {
	s, _, _ := fixture(t)
	var warned []string
	s.LookupGID = func(string) (int, bool) { return 0, false }
	s.Chown = func(string, int, int) error { t.Fatal("must not chown to a group that does not exist"); return nil }
	s.Warn = func(msg string, _ ...any) { warned = append(warned, msg) }
	if _, err := s.NewPendingKey(host); err != nil {
		t.Fatal(err)
	}
	if len(warned) == 0 {
		t.Fatal("the fallback must be reported")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(s.PendingKeyPath(host))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
		}
	}
}

// THE OPT-IN TEST ROOTS: a chain to a root outside the system pool verifies only when that root is configured.
func TestExtraRootsAreOptInAndCAOnly(t *testing.T) {
	now := time.Now()
	ca, _ := portaltlstest.New(now)
	dir := t.TempDir()
	s := &portaltls.Store{Dir: dir, LookupGID: func(string) (int, bool) { return 1, true },
		Chown: func(string, int, int) error { return nil }}
	csrPEM, _ := s.NewPendingKey(host)
	chain, _ := ca.IssueForCSR(csrPEM, now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := s.Install(host, chain); err == nil {
		t.Fatal("a test root must not be trusted by default")
	}
	rootsFile := filepath.Join(dir, "extra.pem")
	_ = os.WriteFile(rootsFile, ca.RootPEM(), 0o644)
	roots, err := portaltls.LoadExtraRoots(rootsFile)
	if err != nil || len(roots) != 1 {
		t.Fatalf("load: %v %d", err, len(roots))
	}
	s.ExtraRoots = roots
	if _, err := s.Install(host, chain); err != nil {
		t.Fatalf("with the configured test root: %v", err)
	}
	// A leaf in a roots bundle is refused.
	leafFile := filepath.Join(dir, "leaf.pem")
	_ = os.WriteFile(leafFile, chain, 0o644)
	if _, err := portaltls.LoadExtraRoots(leafFile); err == nil {
		t.Fatal("a non-CA certificate in the roots bundle must be refused")
	}
}
