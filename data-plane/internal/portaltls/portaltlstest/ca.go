// Package portaltlstest is a throw-away certificate authority for tests of the portal HTTPS lifecycle: a root, an
// intermediate under it, and leaves issued for an appliance CSR exactly as Let's Encrypt would issue them (leaf +
// intermediate in the chain, the root NOT in it). It is test support and is imported only from _test files.
package portaltlstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"time"
)

// CA is a two-level test authority.
type CA struct {
	Root     *x509.Certificate
	RootKey  *ecdsa.PrivateKey
	Inter    *x509.Certificate
	InterKey *ecdsa.PrivateKey
	serial   int64
}

// New builds a root and an intermediate valid from a year before to ten years after now.
func New(now time.Time) (*CA, error) {
	ca := &CA{serial: 100}
	var err error
	if ca.RootKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		return nil, err
	}
	rt := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Portal Root"},
		NotBefore: now.AddDate(-1, 0, 0), NotAfter: now.AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, rt, rt, &ca.RootKey.PublicKey, ca.RootKey)
	if err != nil {
		return nil, err
	}
	if ca.Root, err = x509.ParseCertificate(der); err != nil {
		return nil, err
	}
	if ca.InterKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		return nil, err
	}
	it := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Test Portal Intermediate"},
		NotBefore: now.AddDate(-1, 0, 0), NotAfter: now.AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	if der, err = x509.CreateCertificate(rand.Reader, it, ca.Root, &ca.InterKey.PublicKey, ca.RootKey); err != nil {
		return nil, err
	}
	if ca.Inter, err = x509.ParseCertificate(der); err != nil {
		return nil, err
	}
	return ca, nil
}

// Pool is a pool holding only the root.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Root)
	return p
}

// RootPEM is the root as PEM.
func (ca *CA) RootPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Root.Raw})
}

// IssueForCSR issues a leaf for the CSR's DNS names and public key, valid [notBefore, notAfter), and returns
// the fullchain PEM (leaf, then intermediate).
func (ca *CA) IssueForCSR(csrPEM []byte, notBefore, notAfter time.Time) ([]byte, error) {
	blk, _ := pem.Decode(csrPEM)
	if blk == nil || blk.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("not a CSR")
	}
	csr, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, err
	}
	return ca.Issue(csr.DNSNames, csr.PublicKey, notBefore, notAfter)
}

// Issue issues a leaf for names and pub (any key, so tests can issue for the WRONG key on purpose).
func (ca *CA) Issue(names []string, pub any, notBefore, notAfter time.Time) ([]byte, error) {
	ca.serial++
	lt := &x509.Certificate{
		SerialNumber: big.NewInt(ca.serial), DNSNames: names,
		NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, lt, ca.Inter, pub, ca.InterKey)
	if err != nil {
		return nil, err
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Inter.Raw})...)
	return out, nil
}
