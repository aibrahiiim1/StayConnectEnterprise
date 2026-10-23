// Package portaltlstest is a test-only CA issuing chains like Let's Encrypt (leaf + intermediate, root not included).
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

type CA struct {
	Root     *x509.Certificate
	RootKey  *ecdsa.PrivateKey
	Inter    *x509.Certificate
	InterKey *ecdsa.PrivateKey
	serial   int64
}

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

func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Root)
	return p
}

func (ca *CA) RootPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Root.Raw})
}

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

// Issue accepts any key so tests can issue for the wrong key on purpose.
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
