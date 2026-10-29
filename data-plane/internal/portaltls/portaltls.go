// Package portaltls owns the appliance's portal TLS files: per-hostname key, CSR, and verified atomic installation
// of the chain Central returns. The private key never leaves the appliance; it is never logged, returned or sent,
// only the CSR goes to Central, and every renewal generates a new key.
//
// File layout under Dir (default /etc/stayconnect/portal-tls):
//
//	<host>.key          the installed key, PKCS#8 PEM, 0640 root:stayconnect-portald
//	<host>.crt          the installed fullchain (leaf first), 0644
//	<host>.key.pending  the key of an order not yet issued, 0640 root:stayconnect-portald
package portaltls

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const DefaultDir = "/etc/stayconnect/portal-tls"

// DefaultGroup is the only group that may read the private keys: portald's own.
const DefaultGroup = "stayconnect-portald"

var (
	ErrNotInstalled = errors.New("portal certificate not installed")
	// ErrKeyMismatch: serving the pair would fail every handshake, so it counts as no certificate and a new one is ordered.
	ErrKeyMismatch  = errors.New("installed certificate does not match the installed key")
	ErrNoPendingKey = errors.New("no pending key for this hostname")
	ErrBadHostname  = errors.New("not a valid portal hostname")
	// ErrKeyGroupMissing: portald could not read a key installed without its group, so a certificate installed
	// anyway would never be served. The install fails instead (audit AD-66).
	ErrKeyGroupMissing = errors.New("portal-tls: the key group does not exist, so portald could not read the key; create the group (stayconnect-portald) and retry")
)

// hostnameRE matches migration 0116's custom_domain shape and makes a hostname safe as a file name: no slash, "..", upper case or trailing dot.
var hostnameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

func ValidHostname(h string) bool {
	return len(h) >= 4 && len(h) <= 253 && hostnameRE.MatchString(h)
}

type Installed struct {
	Hostname    string
	Serial      string // hex, lower case
	NotBefore   time.Time
	NotAfter    time.Time
	Fingerprint string // SHA-256 of the leaf DER, hex
}

type Store struct {
	Dir string
	// Group defaults to DefaultGroup.
	Group string
	// LookupGID resolves Group (nil means os/user). If the group is missing, keys are written 0600 root-only,
	// portald cannot read them, and Warn says so.
	LookupGID func(name string) (gid int, ok bool)
	Chown     func(path string, uid, gid int) error
	Now       func() time.Time
	// Roots, when set, is the only trust anchor set (tests); otherwise the system pool plus ExtraRoots.
	Roots *x509.CertPool
	// ExtraRoots are opt-in anchors (SCD_PORTAL_TLS_EXTRA_ROOTS) for Let's Encrypt staging or Pebble; empty in production.
	ExtraRoots []*x509.Certificate
	// Warn receives operational warnings, never key material.
	Warn func(msg string, args ...any)

	certWriter func(path string, chainPEM []byte) error // tests only
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) warn(msg string, args ...any) {
	if s.Warn != nil {
		s.Warn(msg, args...)
	}
}

func (s *Store) dir() string {
	if s.Dir == "" {
		return DefaultDir
	}
	return s.Dir
}

// The hostname must already be valid; callers taking a name from outside go through ValidHostname first.
func (s *Store) KeyPath(host string) string  { return filepath.Join(s.dir(), host+".key") }
func (s *Store) CertPath(host string) string { return filepath.Join(s.dir(), host+".crt") }
func (s *Store) PendingKeyPath(host string) string {
	return filepath.Join(s.dir(), host+".key.pending")
}

func (s *Store) keyGID() (int, bool) {
	name := s.Group
	if name == "" {
		name = DefaultGroup
	}
	if s.LookupGID != nil {
		return s.LookupGID(name)
	}
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, false
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, false
	}
	return gid, true
}

func (s *Store) chown(path string, uid, gid int) error {
	if s.Chown != nil {
		return s.Chown(path, uid, gid)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Chown(path, uid, gid)
}

// EnsureDir creates the directory 0750 root:<group> (0700 when the group is missing) and re-asserts mode and ownership on an existing one.
func (s *Store) EnsureDir() error {
	gid, ok := s.keyGID()
	mode := os.FileMode(0o750)
	if !ok {
		mode = 0o700
		s.warn("portal-tls: the key group does not exist; the directory is root-only and portald cannot read the keys",
			"group", s.groupName())
	}
	if err := os.MkdirAll(s.dir(), mode); err != nil {
		return err
	}
	// Temporary files left by a crash mid-write may hold key bytes; nothing else uses them.
	if stale, err := filepath.Glob(filepath.Join(s.dir(), ".*.tmp-*")); err == nil {
		for _, f := range stale {
			_ = os.Remove(f)
		}
	}
	if ok {
		if err := s.chown(s.dir(), 0, gid); err != nil {
			return fmt.Errorf("portal-tls: chown %s: %w", s.dir(), err)
		}
	}
	return os.Chmod(s.dir(), mode)
}

func (s *Store) groupName() string {
	if s.Group == "" {
		return DefaultGroup
	}
	return s.Group
}

// writeAtomic goes through a temporary file, fsync, rename and directory fsync, so portald sees the old file or the new one, never a half-written key.
func (s *Store) writeAtomic(path string, data []byte, mode os.FileMode, gid int, setGID bool) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := func() { _ = os.Remove(tmp) }
	// Mode first, before any key byte is written.
	if err := f.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		f.Close()
		cleanup()
		return err
	}
	if setGID {
		if err := s.chown(tmp, 0, gid); err != nil {
			f.Close()
			cleanup()
			return err
		}
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // not supported on every platform
		d.Close()
	}
	return nil
}

func (s *Store) writeKey(path string, pemBytes []byte) error {
	gid, ok := s.keyGID()
	if !ok {
		s.warn("portal-tls: the key group does not exist; writing the key root-only (0600), portald cannot serve it",
			"group", s.groupName())
		return s.writeAtomic(path, pemBytes, 0o600, 0, false)
	}
	return s.writeAtomic(path, pemBytes, 0o640, gid, true)
}

func (s *Store) HasPending(host string) bool {
	if !ValidHostname(host) {
		return false
	}
	_, err := os.Stat(s.PendingKeyPath(host))
	return err == nil
}

func (s *Store) DropPending(host string) error {
	if !ValidHostname(host) {
		return ErrBadHostname
	}
	err := os.Remove(s.PendingKeyPath(host))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// NewPendingKey generates a new ECDSA P-256 key, replacing any pending key, and returns its CSR.
func (s *Store) NewPendingKey(host string) ([]byte, error) {
	if !ValidHostname(host) {
		return nil, ErrBadHostname
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := s.writeKey(s.PendingKeyPath(host), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		return nil, err
	}
	return CreateCSR(host, key)
}

// PendingCSR rebuilds the CSR for the existing pending key. Reuse it after a failed order request or a 409, since
// the open order may have been made with this key and can only be installed with it.
func (s *Store) PendingCSR(host string) ([]byte, error) {
	if !ValidHostname(host) {
		return nil, ErrBadHostname
	}
	key, err := readKey(s.PendingKeyPath(host))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoPendingKey
		}
		return nil, err
	}
	return CreateCSR(host, key)
}

// CreateCSR names exactly one DNS name and nothing else (no common name, no other SAN); Central refuses anything else.
func CreateCSR(host string, key crypto.Signer) ([]byte, error) {
	if !ValidHostname(host) {
		return nil, ErrBadHostname
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{host}}, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

func readKey(path string) (crypto.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseKey(b)
}

func parseKey(b []byte) (crypto.Signer, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("key file is not PEM")
	}
	var k any
	var err error
	switch blk.Type {
	case "PRIVATE KEY":
		k, err = x509.ParsePKCS8PrivateKey(blk.Bytes)
	case "EC PRIVATE KEY":
		k, err = x509.ParseECPrivateKey(blk.Bytes)
	default:
		return nil, fmt.Errorf("unexpected key block %q", blk.Type)
	}
	if err != nil {
		return nil, err
	}
	sg, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("key is not a signer")
	}
	return sg, nil
}

// ParseChain refuses non-certificate blocks rather than skipping them: a chain carrying a key or junk is not installable.
func ParseChain(chainPEM []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := bytes.TrimSpace(chainPEM)
	for len(rest) > 0 {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			return nil, errors.New("chain contains data that is not PEM")
		}
		if blk.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("chain contains a %q block", blk.Type)
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("chain certificate %d: %w", len(out)+1, err)
		}
		out = append(out, c)
		rest = bytes.TrimSpace(rest)
	}
	if len(out) == 0 {
		return nil, errors.New("chain is empty")
	}
	return out, nil
}

func samePublicKey(a, b crypto.PublicKey) bool {
	type eq interface{ Equal(crypto.PublicKey) bool }
	ea, ok := a.(eq)
	return ok && ea.Equal(b)
}

func (s *Store) roots() *x509.CertPool {
	if s.Roots != nil {
		return s.Roots
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	for _, c := range s.ExtraRoots {
		pool.AddCert(c)
	}
	return pool
}

// VerifyChain checks the leaf names host, carries pub (a chain for another key would break every handshake), is
// valid now, and verifies to a trusted root for server authentication using the bundle's own intermediates.
func (s *Store) VerifyChain(host string, chainPEM []byte, pub crypto.PublicKey) (*Installed, error) {
	certs, err := ParseChain(chainPEM)
	if err != nil {
		return nil, err
	}
	leaf := certs[0]
	named := false
	for _, n := range leaf.DNSNames {
		if strings.EqualFold(n, host) {
			named = true
		}
	}
	if !named {
		return nil, fmt.Errorf("certificate does not name %s", host)
	}
	if !samePublicKey(pub, leaf.PublicKey) {
		return nil, errors.New("certificate public key does not match the appliance's key")
	}
	now := s.now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, fmt.Errorf("certificate is not valid now (valid %s to %s)",
			leaf.NotBefore.UTC().Format(time.RFC3339), leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:       host,
		Roots:         s.roots(),
		Intermediates: inter,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		// A Let's Encrypt staging chain is refused like any untrusted chain; only the message says it is expected.
		var ua x509.UnknownAuthorityError
		if errors.As(err, &ua) && isStagingChain(certs) {
			return nil, fmt.Errorf("certificate chain does not verify: it is a Let's Encrypt STAGING certificate, "+
				"which no device trusts, so it is not installed (expected until Central switches to the production "+
				"directory): %w", err)
		}
		return nil, fmt.Errorf("certificate chain does not verify: %w", err)
	}
	return describe(host, leaf), nil
}

// isStagingChain matches the "(STAGING)" marker in Let's Encrypt staging names; a description, never a trust decision.
func isStagingChain(certs []*x509.Certificate) bool {
	for _, c := range certs {
		if strings.Contains(c.Issuer.CommonName, "(STAGING)") || strings.Contains(c.Subject.CommonName, "(STAGING)") {
			return true
		}
	}
	return false
}

func describe(host string, leaf *x509.Certificate) *Installed {
	sum := sha256.Sum256(leaf.Raw)
	return &Installed{
		Hostname:    host,
		Serial:      strings.ToLower(leaf.SerialNumber.Text(16)),
		NotBefore:   leaf.NotBefore,
		NotAfter:    leaf.NotAfter,
		Fingerprint: hex.EncodeToString(sum[:]),
	}
}

// Install verifies the chain against the pending key, then installs the key, the chain, and drops the pending
// key. portald keeps its current pair if a load fails, so the gap between the renames cannot break a handshake. If the
// chain cannot be written after the key was, the previous key is put back so the installed pair still matches; the
// pending key stays until both are in place.
func (s *Store) Install(host string, chainPEM []byte) (*Installed, error) {
	if !ValidHostname(host) {
		return nil, ErrBadHostname
	}
	if _, ok := s.keyGID(); !ok {
		// Alert, then fail: a root-only key beside a certificate portald cannot use is an outage in waiting.
		s.warn("portal-tls: ALERT: the key group does not exist; the certificate is NOT installed",
			"host", host, "group", s.groupName())
		return nil, ErrKeyGroupMissing
	}
	keyPEM, err := os.ReadFile(s.PendingKeyPath(host))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoPendingKey
		}
		return nil, err
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("pending key: %w", err)
	}
	inst, err := s.VerifyChain(host, chainPEM, key.Public())
	if err != nil {
		return nil, err
	}
	oldKey, oldKeyErr := os.ReadFile(s.KeyPath(host))
	if err := s.writeKey(s.KeyPath(host), keyPEM); err != nil {
		return nil, err
	}
	if err := s.writeCert(s.CertPath(host), chainPEM); err != nil {
		switch {
		case oldKeyErr == nil:
			if rerr := s.writeKey(s.KeyPath(host), oldKey); rerr != nil {
				s.warn("portal-tls: chain write failed and the previous key could not be restored", "host", host, "err", rerr)
			}
		case errors.Is(oldKeyErr, os.ErrNotExist):
			_ = os.Remove(s.KeyPath(host))
		}
		return nil, err
	}
	if err := s.DropPending(host); err != nil {
		s.warn("portal-tls: installed, but the pending key could not be removed", "host", host, "err", err)
	}
	return inst, nil
}

// writeCert is the chain write Install uses; tests replace it to simulate a failure.
func (s *Store) writeCert(path string, chainPEM []byte) error {
	if s.certWriter != nil {
		return s.certWriter(path, chainPEM)
	}
	return s.writeAtomic(path, chainPEM, 0o644, 0, false)
}

// Inspect does not check expiry or trust (the agent decides), but refuses a mismatched pair (ErrKeyMismatch) and a
// certificate that does not name host.
func (s *Store) Inspect(host string) (*Installed, error) {
	if !ValidHostname(host) {
		return nil, ErrBadHostname
	}
	chain, err := os.ReadFile(s.CertPath(host))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInstalled
	} else if err != nil {
		return nil, err
	}
	key, err := readKey(s.KeyPath(host))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInstalled
	} else if err != nil {
		return nil, fmt.Errorf("installed key: %w", err)
	}
	certs, err := ParseChain(chain)
	if err != nil {
		return nil, err
	}
	if !samePublicKey(key.Public(), certs[0].PublicKey) {
		return nil, ErrKeyMismatch
	}
	if err := certs[0].VerifyHostname(host); err != nil {
		return nil, fmt.Errorf("installed certificate does not name %s", host)
	}
	return describe(host, certs[0]), nil
}

func (s *Store) Remove(host string) error {
	if !ValidHostname(host) {
		return ErrBadHostname
	}
	var first error
	for _, p := range []string{s.CertPath(host), s.KeyPath(host), s.PendingKeyPath(host)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

// RemoveInstalled keeps the pending key: a revoked certificate stops at once but an open replacement order must survive.
func (s *Store) RemoveInstalled(host string) error {
	if !ValidHostname(host) {
		return ErrBadHostname
	}
	var first error
	for _, p := range []string{s.CertPath(host), s.KeyPath(host)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

func (s *Store) Hosts() ([]string, error) {
	ents, err := os.ReadDir(s.dir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	seen := map[string]bool{}
	for _, e := range ents {
		n := e.Name()
		var h string
		switch {
		case strings.HasSuffix(n, ".key.pending"):
			h = strings.TrimSuffix(n, ".key.pending")
		case strings.HasSuffix(n, ".key"):
			h = strings.TrimSuffix(n, ".key")
		case strings.HasSuffix(n, ".crt"):
			h = strings.TrimSuffix(n, ".crt")
		default:
			continue
		}
		if ValidHostname(h) {
			seen[h] = true
		}
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// LoadExtraRoots accepts only CA certificates: a leaf in a roots bundle would silently trust one server.
func LoadExtraRoots(path string) ([]*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	certs, err := ParseChain(b)
	if err != nil {
		return nil, err
	}
	for _, c := range certs {
		if !c.IsCA {
			return nil, fmt.Errorf("%s contains a certificate that is not a CA (%s)", path, c.Subject.String())
		}
	}
	return certs, nil
}
