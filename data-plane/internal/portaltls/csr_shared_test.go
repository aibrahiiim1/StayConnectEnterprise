package portaltls_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/portaltls"
)

// The shared-name CSR names the shared name and the appliance's own hostname, nothing else, never twice.
func TestCreateCSRWithFurtherNames(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := portaltls.CreateCSR("portal.onegate.getsemantics.com", k, host, "portal.onegate.getsemantics.com")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := pem.Decode(der)
	csr, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(csr.DNSNames) != 2 || csr.DNSNames[0] != "portal.onegate.getsemantics.com" || csr.DNSNames[1] != host ||
		csr.Subject.CommonName != "" {
		t.Fatalf("names %v, CN %q", csr.DNSNames, csr.Subject.CommonName)
	}
	if _, err := portaltls.CreateCSR("portal.onegate.getsemantics.com", k, "bad name"); err == nil {
		t.Fatal("an invalid further name is refused")
	}
	one, _ := portaltls.CreateCSR(host, k)
	b, _ = pem.Decode(one)
	csr, _ = x509.ParseCertificateRequest(b.Bytes)
	if len(csr.DNSNames) != 1 || csr.DNSNames[0] != host {
		t.Fatalf("without further names exactly one name: %v", csr.DNSNames)
	}
}
