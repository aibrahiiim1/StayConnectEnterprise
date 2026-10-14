// Package portaltls is the appliance half of the Client Portal's HTTPS certificate lifecycle
// (docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md section 3).
//
// WHAT LIVES HERE AND WHAT DOES NOT. This package owns the files: the private key the appliance generates for
// each portal hostname, the certificate signing request built from it, and the verified, atomic installation of
// the chain Central hands back. It does not talk to Central and it does not touch the database -- scd's agent
// (cmd/scd/portal_tls.go) decides WHEN a certificate is needed; this package decides whether what came back is
// safe to serve.
//
// THE PRIVATE KEY NEVER LEAVES THE APPLIANCE (contract section 1, decision 2). It is generated here, written once
// to a 0640 file readable by portald's group, and never logged, returned or sent. Only the CSR -- which carries
// the public key -- goes to Central. Every renewal generates a new key.
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

// DefaultDir is where the portal's keys and certificates live on the appliance.
const DefaultDir = "/etc/stayconnect/portal-tls"

// DefaultGroup is the group that may read the private keys: portald's own primary group, and nobody else's.
const DefaultGroup = "stayconnect-portald"

var (
	// ErrNotInstalled: no certificate (or no key) is installed for the hostname.
	ErrNotInstalled = errors.New("portal certificate not installed")
	// ErrKeyMismatch: the installed certificate does not carry the installed key's public half. Serving it
	// would fail every handshake, so it counts as no certificate at all and a new one is ordered.
	ErrKeyMismatch = errors.New("installed certificate does not match the installed key")
	// ErrNoPendingKey: an issued chain arrived but there is no pending key to pair it with.
	ErrNoPendingKey = errors.New("no pending key for this hostname")
	// ErrBadHostname: the name is not one this package will put in a file name or a CSR.
	ErrBadHostname = errors.New("not a valid portal hostname")
)

// hostnameRE is the same shape migration 0116 enforces on custom_domain, applied to every hostname this package
// handles. It is what makes a hostname safe to use as a FILE NAME: no slash, no "..", no upper case, no
// trailing dot.
var hostnameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// ValidHostname reports whether h is a lower-case DNS name of 4..253 characters with at least two labels.
func ValidHostname(h string) bool {
	return len(h) >= 4 && len(h) <= 253 && hostnameRE.MatchString(h)
}

// Installed describes the certificate installed for one hostname. It carries nothing secret.
type Installed struct {
	Hostname    string
	Serial      string // hex, lower case
	NotBefore   time.Time
	NotAfter    time.Time
	Fingerprint string // SHA-256 of the leaf DER, hex
}

// Store manages the portal certificate files in one directory.
type Store struct {
	Dir string
	// Group is the group that may read the keys (DefaultGroup when empty).
	Group string
	// LookupGID resolves Group; nil means os/user. ok=false means the group does not exist on this machine, and
	// keys are then written 0600 root-only -- portald cannot read them, and Warn says so once per write.
	LookupGID func(name string) (gid int, ok bool)
	// Chown replaces os.Chown (tests; and platforms where it is meaningless).
	Chown func(path string, uid, gid int) error
	// Now replaces time.Now (tests).
	Now func() time.Time
	// Roots, when set, is the ONLY trust anchor set used to verify an issued chain (tests). When nil the system
	// pool is used, plus ExtraRoots.
	Roots *x509.CertPool
	// ExtraRoots are opt-in additional anchors (SCD_PORTAL_TLS_EXTRA_ROOTS): Let's Encrypt staging or a Pebble
	// test CA. Empty in production.
	ExtraRoots []*x509.Certificate
	// Warn receives operational warnings (never key material). Nil discards them.
	Warn func(msg string, args ...any)
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

// KeyPath, CertPath and PendingKeyPath name the three files of one hostname. The hostname must already be valid;
// callers that take a name from outside go through ValidHostname first.
func (s *Store) KeyPath(host string) string  { return filepath.Join(s.dir(), host+".key") }
func (s *Store) CertPath(host string) string { return filepath.Join(s.dir(), host+".crt") }
func (s *Store) PendingKeyPath(host string) string {
	return filepath.Join(s.dir(), host+".key.pending")
}

// keyGID returns the gid keys are group-owned by, or ok=false when the group is missing.
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

// EnsureDir creates the directory 0750 root:<group> (root-only 0700 when the group is missing) and re-asserts
// ownership and mode on an existing one, so a directory created by hand with the wrong mode heals at startup.
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

// writeAtomic writes data to path through a temporary file in the same directory: write, fsync, chmod/chown,
// rename, then fsync the directory. A reader (portald) therefore sees either the old file or the new one, never
// a half-written key -- and a crash leaves at worst a stray temporary, never a truncated key.
func (s *Store) writeAtomic(path string, data []byte, mode os.FileMode, gid int, setGID bool) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := func() { _ = os.Remove(tmp) }
	// Mode first, before a single byte of a key exists in the file.
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
		_ = d.Sync() // best effort: not supported on every platform
		d.Close()
	}
	return nil
}

// writeKey writes a private key 0640 root:<group>, or 0600 root when the group is missing.
func (s *Store) writeKey(path string, pemBytes []byte) error {
	gid, ok := s.keyGID()
	if !ok {
		s.warn("portal-tls: the key group does not exist; writing the key root-only (0600), portald cannot serve it",
			"group", s.groupName())
		return s.writeAtomic(path, pemBytes, 0o600, 0, false)
	}
	return s.writeAtomic(path, pemBytes, 0o640, gid, true)
}

// HasPending reports whether a pending key exists for host.
func (s *Store) HasPending(host string) bool {
	if !ValidHostname(host) {
		return false
	}
	_, err := os.Stat(s.PendingKeyPath(host))
	return err == nil
}

// DropPending removes the pending key, if any.
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

// NewPendingKey generates a NEW ECDSA P-256 key for host, writes it as the pending key (replacing any previous
// pending key) and returns a CSR for it.
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

// PendingCSR rebuilds the CSR for the existing pending key. Used when an order request failed AFTER the key was
// written, and when Central answered 409 for an order that may well have been made with this very key: reusing
// the pending key is what lets that open order be installed when it issues.
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

// CreateCSR builds a PEM CSR that names exactly one DNS name, host, and nothing else: no common name, no other
// SAN. Central refuses anything else (contract section 3), and so does this function's own test.
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

// ParseChain parses every CERTIFICATE block of a PEM bundle, leaf first. Anything other than certificates is
// refused rather than skipped: a chain from Central that carries a key or junk is not a chain to install.
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

// VerifyChain checks that chainPEM is a certificate this appliance may serve for host with key's public half:
//
//   - every block parses and the leaf names host among its DNS names (and only DNS-verifies for host);
//   - the leaf's public key IS the given key's public key -- a chain issued for some other key is worthless and,
//     installed, would break every handshake;
//   - now is within the leaf's validity;
//   - the chain verifies to a trusted root for server authentication, using the bundle's own intermediates.
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
		// A LET'S ENCRYPT STAGING CHAIN is refused here exactly like any other untrusted chain -- its roots are in no
		// trust store, which is the point of staging -- but saying so turns an alarming "unknown authority" into
		// the expected step it is: Central is still on the staging directory (runbook ONEGATE_WORK_DNS_AND_
		// CERTIFICATES.md section 5). Verification is unchanged; only the words differ.
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

// isStagingChain reports whether an intermediate or the leaf's issuer is a Let's Encrypt STAGING authority (their
// names carry the "(STAGING)" marker). A description, never a trust decision.
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

// Install verifies an issued chain against the PENDING key and, only if it verifies, installs both atomically:
// the key first, then the chain, then the pending key is removed. portald loads the pair by SNI and keeps the
// pair it already has if a load fails, so the instant between the two renames cannot break a handshake.
func (s *Store) Install(host string, chainPEM []byte) (*Installed, error) {
	if !ValidHostname(host) {
		return nil, ErrBadHostname
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
	if err := s.writeKey(s.KeyPath(host), keyPEM); err != nil {
		return nil, err
	}
	if err := s.writeAtomic(s.CertPath(host), chainPEM, 0o644, 0, false); err != nil {
		return nil, err
	}
	if err := s.DropPending(host); err != nil {
		s.warn("portal-tls: installed, but the pending key could not be removed", "host", host, "err", err)
	}
	return inst, nil
}

// Inspect reads the installed pair for host and returns its description. It does NOT check expiry or trust --
// the agent decides what an expired certificate means -- but it does refuse a pair whose key and certificate
// do not belong together (ErrKeyMismatch), because that pair can never serve a handshake.
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
	return describe(host, certs[0]), nil
}

// Remove deletes every file of host: installed key, certificate and pending key.
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

// RemoveInstalled deletes the installed key and certificate of host and keeps a pending key (a revoked
// certificate must stop being served at once; an order already open for its replacement must survive).
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

// Hosts lists every hostname that has any file in the directory (certificate, key or pending key).
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

// LoadExtraRoots reads a PEM bundle of additional trust anchors. Only CA certificates are accepted: a leaf in
// a roots bundle is a configuration mistake that would otherwise silently trust one server.
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
