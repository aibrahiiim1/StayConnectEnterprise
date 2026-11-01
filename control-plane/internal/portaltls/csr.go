package portaltls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

const (
	KindOneGate = "ONEGATE" // <label>.gw.<base>, or the shared Client Portal name
	KindCustom  = "CUSTOM"  // the hotel's own verified domain
)

// ParseAndCheckCSR validates an appliance CSR before it goes near the CA: self-signed, exactly one DNS name
// and no IP/email/URI SANs, a CN that is empty or equal to it (a different CN is a second name in disguise),
// and ECDSA P-256 or RSA >= 2048. Whether the name belongs to the appliance is the caller's question.
func ParseAndCheckCSR(csrPEM string) (*x509.CertificateRequest, string, error) {
	block, rest := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, "", errors.New("csr_pem must be one PEM CERTIFICATE REQUEST")
	}
	if len(rest) > 0 {
		if b2, _ := pem.Decode(rest); b2 != nil {
			return nil, "", errors.New("csr_pem must contain exactly one PEM block")
		}
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("the CSR does not parse: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", errors.New("the CSR signature does not verify")
	}
	if len(csr.IPAddresses) > 0 || len(csr.EmailAddresses) > 0 || len(csr.URIs) > 0 {
		return nil, "", errors.New("the CSR may name only one DNS name (no IP, email or URI names)")
	}
	if len(csr.DNSNames) != 1 {
		return nil, "", fmt.Errorf("the CSR must name exactly one DNS name, it names %d", len(csr.DNSNames))
	}
	name := csr.DNSNames[0]
	if name != NormalizeFQDN(name) || name == "" {
		return nil, "", errors.New("the CSR's DNS name must be lowercase without a trailing dot")
	}
	if cn := csr.Subject.CommonName; cn != "" && cn != name {
		return nil, "", errors.New("the CSR's common name must be empty or equal to its DNS name")
	}
	switch k := csr.PublicKey.(type) {
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return nil, "", errors.New("an ECDSA CSR key must be P-256")
		}
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return nil, "", errors.New("an RSA CSR key must be at least 2048 bits")
		}
	default:
		return nil, "", errors.New("the CSR key must be ECDSA P-256 or RSA (2048 bits or more)")
	}
	return csr, name, nil
}
