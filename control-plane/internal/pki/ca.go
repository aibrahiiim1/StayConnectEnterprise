// Package pki is the appliance certificate authority: client certificates bound to appliance, tenant and site,
// and the mTLS server certificate. CA private keys stay in 0600 files; only public certificates are persisted.
package pki

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// uriScheme identifies the client cert's URI SAN: stayconnect://appliance/<appliance_id>
const uriScheme = "stayconnect"

type CA struct {
	// priv/cert are the online intermediate signer; the root key is never held here.
	priv    ed25519.PrivateKey
	cert    *x509.Certificate
	intPEM  []byte
	rootPEM []byte
	certPEM []byte // trust bundle: intermediate + root
	Version int
}

// LoadOrCreate builds a single self-signed CA (dev path).
func LoadOrCreate(keyPath string, version int) (*CA, error) {
	priv, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	cert, certPEM, err := selfSign(priv, version)
	if err != nil {
		return nil, err
	}
	return &CA{priv: priv, cert: cert, intPEM: certPEM, certPEM: certPEM, Version: version}, nil
}

// LoadCA loads the two-tier CA that InitCA created. It never creates key or certificate material: a missing
// root certificate, intermediate certificate or intermediate key is an error, so a restore that lost one file
// leaves the PKI off instead of minting a new root that no appliance trusts (audit Phase 1 H1).
func LoadCA(rootCertPath, intCertPath, intKeyPath string, version int) (*CA, error) {
	rootPEM, err := os.ReadFile(rootCertPath)
	if err != nil {
		return nil, fmt.Errorf("pki: root CA certificate: %w", err)
	}
	intPEM, err := os.ReadFile(intCertPath)
	if err != nil {
		return nil, fmt.Errorf("pki: intermediate CA certificate: %w", err)
	}
	intPriv, err := loadKey(intKeyPath)
	if err != nil {
		return nil, fmt.Errorf("pki: intermediate CA key: %w", err)
	}
	rootCert, err := parseCertPEM(rootPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: root CA certificate: %w", err)
	}
	intCert, err := parseCertPEM(intPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: intermediate CA certificate: %w", err)
	}
	if err := checkChain(rootCert, intCert, intPriv, time.Now()); err != nil {
		return nil, err
	}
	bundle := append(append([]byte{}, intPEM...), rootPEM...)
	return &CA{priv: intPriv, cert: intCert, intPEM: intPEM, rootPEM: rootPEM, certPEM: bundle, Version: version}, nil
}

// checkChain proves the intermediate is the root's, holds intKey, and is still valid (audit Phase 1 L16).
func checkChain(root, inter *x509.Certificate, intKey ed25519.PrivateKey, now time.Time) error {
	if !root.IsCA || !inter.IsCA {
		return errors.New("pki: root and intermediate must both be CA certificates")
	}
	if err := inter.CheckSignatureFrom(root); err != nil {
		return fmt.Errorf("pki: intermediate CA is not signed by the root CA: %w", err)
	}
	pub, ok := inter.PublicKey.(ed25519.PublicKey)
	if !ok || !pub.Equal(intKey.Public()) {
		return errors.New("pki: intermediate CA key does not match the intermediate CA certificate")
	}
	if now.After(inter.NotAfter) || now.After(root.NotAfter) {
		return errors.New("pki: the CA certificate has expired")
	}
	return nil
}

// InitCA creates a brand-new two-tier CA, once, for a fresh Central (`ctrlapi init-ca`). It refuses when any of
// the files already exists. The root key is written to rootKeyPath, which belongs outside the runtime PKI
// directory and then in cold storage; serve never needs it.
func InitCA(rootKeyPath, rootCertPath, intCertPath, intKeyPath string, version int) (*CA, error) {
	for _, p := range []string{rootKeyPath, rootCertPath, intCertPath, intKeyPath} {
		if _, err := os.Lstat(p); err == nil {
			return nil, fmt.Errorf("pki: %s already exists; refusing to create a new CA over existing material", p)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	_, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	_, intPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	rootCert, rootPEM, err := selfSignRoot(rootPriv)
	if err != nil {
		return nil, err
	}
	intCert, intPEM, err := signIntermediate(intPriv, rootCert, rootPriv, version)
	if err != nil {
		return nil, err
	}
	rootKeyPEM, err := keyPEM(rootPriv)
	if err != nil {
		return nil, err
	}
	intKeyPEM, err := keyPEM(intPriv)
	if err != nil {
		return nil, err
	}
	for _, f := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{{rootKeyPath, rootKeyPEM, 0o600}, {intKeyPath, intKeyPEM, 0o600}, {rootCertPath, rootPEM, 0o644}, {intCertPath, intPEM, 0o644}} {
		if err := writeNew(f.path, f.data, f.mode); err != nil {
			return nil, err
		}
	}
	bundle := append(append([]byte{}, intPEM...), rootPEM...)
	return &CA{priv: intPriv, cert: intCert, intPEM: intPEM, rootPEM: rootPEM, certPEM: bundle, Version: version}, nil
}

// CertSibling is the certificate path that belongs to a CA key path (root-ca.key → root-ca.crt).
func CertSibling(keyPath string) string { return certSibling(keyPath) }

func certSibling(keyPath string) string {
	ext := filepath.Ext(keyPath)
	return keyPath[:len(keyPath)-len(ext)] + ".crt"
}

func parseCertPEM(p []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(p)
	if block == nil {
		return nil, errors.New("pki: cert PEM decode failed")
	}
	return x509.ParseCertificate(block.Bytes)
}

func (c *CA) IntermediatePEM() []byte { return c.intPEM }

func (c *CA) RootPEM() []byte { return c.rootPEM }

func loadKey(keyPath string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("pki: CA key PEM decode failed")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA key: %w", err)
	}
	ed, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("pki: CA key is not Ed25519")
	}
	return ed, nil
}

func keyPEM(priv ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// loadOrCreateKey is the single-tier dev CA's key (LoadOrCreate, tests only).
func loadOrCreateKey(keyPath string) (ed25519.PrivateKey, error) {
	priv, err := loadKey(keyPath)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return priv, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, err
	}
	_, priv, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	pemBytes, err := keyPEM(priv)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(keyPath, pemBytes, 0o600); err != nil {
		return nil, err
	}
	return priv, nil
}

// writeNew creates path (and its directory) and fails if it exists.
func writeNew(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// selfSignRoot builds the deterministic root (pathlen=1: it signs one intermediate).
func selfSignRoot(priv ed25519.PrivateKey) (*x509.Certificate, []byte, error) {
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "StayConnect Root CA", Organization: []string{"StayConnect"}},
		NotBefore:             time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2045, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, priv.Public(), priv)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// signIntermediate builds the deterministic intermediate (pathlen=0: leaf certificates only).
func signIntermediate(intPriv ed25519.PrivateKey, rootCert *x509.Certificate, rootPriv ed25519.PrivateKey, version int) (*x509.Certificate, []byte, error) {
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(int64(1000 + version)),
		Subject:               pkix.Name{CommonName: fmt.Sprintf("StayConnect Appliance Intermediate CA v%d", version), Organization: []string{"StayConnect"}},
		NotBefore:             time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, rootCert, intPriv.Public(), rootPriv)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func selfSign(priv ed25519.PrivateKey, version int) (*x509.Certificate, []byte, error) {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(int64(version)),
		Subject:      pkix.Name{CommonName: fmt.Sprintf("StayConnect Appliance CA v%d", version), Organization: []string{"StayConnect"}},
		// Fixed validity keeps the cert byte-stable: appliances pin the CA cert PEM.
		NotBefore:             time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2045, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, priv.Public(), priv)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return cert, certPEM, nil
}

func (c *CA) CertPEM() []byte { return c.certPEM }

func (c *CA) Subject() string { return c.cert.Subject.CommonName }

func (c *CA) KeyFingerprint() string {
	sum := sha256.Sum256(c.priv.Public().(ed25519.PublicKey))
	return fmt.Sprintf("%x", sum[:])
}

type SignedClient struct {
	CertPEM        []byte
	SerialHex      string
	FingerprintHex string
	NotBefore      time.Time
	NotAfter       time.Time
}

// SignApplianceCSR issues a client certificate binding appliance_id (URI SAN), tenant (OU) and site (O).
func (c *CA) SignApplianceCSR(csrPEM []byte, applianceID, tenantID, siteID string, validity time.Duration) (*SignedClient, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("pki: expected CERTIFICATE REQUEST PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CSR: %w", err)
	}
	// Proof of possession.
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("pki: CSR signature invalid (proof-of-possession failed): %w", err)
	}
	if err := checkCSRKey(csr.PublicKey); err != nil {
		return nil, err
	}
	uri, err := url.Parse(fmt.Sprintf("%s://appliance/%s", uriScheme, applianceID))
	if err != nil {
		return nil, err
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         applianceID,
			Organization:       []string{siteID},
			OrganizationalUnit: []string{tenantID},
		},
		NotBefore:             now.Add(-1 * time.Minute),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.priv)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(der)
	return &SignedClient{
		CertPEM:        pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		SerialHex:      fmt.Sprintf("%x", serial),
		FingerprintHex: fmt.Sprintf("%x", sum[:]),
		NotBefore:      tmpl.NotBefore,
		NotAfter:       tmpl.NotAfter,
	}, nil
}

// checkCSRKey accepts the appliance's Ed25519 key, or ECDSA P-256/P-384; nothing else (audit Phase 1 L11).
func checkCSRKey(pub any) error {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return nil
	case *ecdsa.PublicKey:
		if k.Curve == elliptic.P256() || k.Curve == elliptic.P384() {
			return nil
		}
	}
	return errors.New("pki: CSR key must be Ed25519 or ECDSA P-256/P-384")
}

func (c *CA) SignServer(hosts []string, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "stayconnect-mtls", Organization: []string{"StayConnect"}},
		NotBefore:             now.Add(-1 * time.Minute),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, pub, c.priv)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// ApplianceIDFromCert returns the appliance_id in a verified client cert's URI SAN, or "".
func ApplianceIDFromCert(cert *x509.Certificate) string {
	for _, u := range cert.URIs {
		if u.Scheme == uriScheme && u.Host == "appliance" {
			return trimSlash(u.Path)
		}
	}
	return ""
}

func FingerprintHex(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return fmt.Sprintf("%x", sum[:])
}

func trimSlash(s string) string {
	for len(s) > 0 && s[0] == '/' {
		s = s[1:]
	}
	return s
}

func randSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-ca-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
