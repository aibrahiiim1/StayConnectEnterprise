// Package pki is the appliance certificate authority: client certificates bound to appliance, tenant and site,
// and the mTLS server certificate. CA private keys stay in 0600 files; only public certificates are persisted.
package pki

import (
	"crypto/ed25519"
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

// LoadCAChain builds the two-tier CA. The root key is needed only to sign the intermediate on first setup;
// after that it is moved offline and all issuance uses the intermediate.
func LoadCAChain(rootKeyPath, intKeyPath string, version int) (*CA, error) {
	intPriv, err := loadOrCreateKey(intKeyPath)
	if err != nil {
		return nil, err
	}
	rootCertPath := certSibling(rootKeyPath)
	intCertPath := certSibling(intKeyPath)

	// Persisted public certs mean the root private key is not needed.
	rootPEM, rerr := os.ReadFile(rootCertPath)
	intPEM, ierr := os.ReadFile(intCertPath)
	if rerr == nil && ierr == nil {
		intCert, err := parseCertPEM(intPEM)
		if err != nil {
			return nil, err
		}
		bundle := append(append([]byte{}, intPEM...), rootPEM...)
		return &CA{priv: intPriv, cert: intCert, intPEM: intPEM, rootPEM: rootPEM, certPEM: bundle, Version: version}, nil
	}

	rootPriv, err := loadOrCreateKey(rootKeyPath)
	if err != nil {
		return nil, err
	}
	rootCert, rootPEM2, err := selfSignRoot(rootPriv)
	if err != nil {
		return nil, err
	}
	intCert, intPEM2, err := signIntermediate(intPriv, rootCert, rootPriv, version)
	if err != nil {
		return nil, err
	}
	// Persist the public certs so later boots never need the root key.
	_ = writeAtomic(rootCertPath, rootPEM2, 0o644)
	_ = writeAtomic(intCertPath, intPEM2, 0o644)
	bundle := append(append([]byte{}, intPEM2...), rootPEM2...)
	return &CA{priv: intPriv, cert: intCert, intPEM: intPEM2, rootPEM: rootPEM2, certPEM: bundle, Version: version}, nil
}

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

func loadOrCreateKey(keyPath string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(keyPath)
	if err == nil {
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
	if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := writeAtomic(keyPath, pemBytes, 0o600); err != nil {
		return nil, err
	}
	return priv, nil
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
