package appliancecert

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// AD-2: the appliance pins Central's appliance Root CA and accepts only certificate chains that end in it.
//
// Before, ca.crt (the root set for every mutual-TLS call to Central) was written from the ca_chain field of whatever
// Central's ingress response carried, on every reconcile and rotation, and never compared with anything provisioned at
// install. Whoever controlled that response controlled the trust anchor.
//
// The pin is a file of one or more self-signed CA certificates (default <cert dir>/../appliance-root-ca.crt, that is
// /etc/stayconnect/appliance-root-ca.crt). Provisioning installs it from Central's published appliance-root-ca.crt
// (deploy/scripts/install-appliance-root-ca.sh). An appliance provisioned before this existed has no pin file: the
// roots of the CA bundle it already holds (ca.crt, or the offline package's ca-bundle.crt) are adopted as its pin, and
// an appliance with neither adopts the roots of the first bundle it installs (logged, because only provisioning gives
// the strong guarantee). From then on a bundle that does not chain to the pin is refused and the installed pair stays.

// ErrCAChangedFromPin means a CA bundle (or client certificate) does not chain to the pinned appliance Root CA.
var ErrCAChangedFromPin = errors.New("PINNED_ROOT_MISMATCH: the CA bundle does not chain to the pinned appliance Root CA")

func (m *Manager) pinFile() string {
	if m.PinPath != "" {
		return m.PinPath
	}
	return filepath.Join(filepath.Dir(m.dir), "appliance-root-ca.crt")
}

func parseCerts(pemBytes []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for rest := pemBytes; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("certificate parse: %w", err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("no certificate in PEM")
	}
	return out, nil
}

func isSelfSignedCA(c *x509.Certificate) bool {
	return c.IsCA && bytes.Equal(c.RawSubject, c.RawIssuer) && c.CheckSignatureFrom(c) == nil
}

func selfSignedRoots(certs []*x509.Certificate) []*x509.Certificate {
	var out []*x509.Certificate
	for _, c := range certs {
		if isSelfSignedCA(c) {
			out = append(out, c)
		}
	}
	return out
}

func pemOf(certs []*x509.Certificate) []byte {
	var b []byte
	for _, c := range certs {
		b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	return b
}

// loadPin returns the pinned roots; (nil, nil) when no pin exists yet.
func (m *Manager) loadPin() ([]*x509.Certificate, error) {
	raw, err := os.ReadFile(m.pinFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	certs, err := parseCerts(raw)
	if err != nil {
		return nil, fmt.Errorf("pinned appliance Root CA %s: %w", m.pinFile(), err)
	}
	roots := selfSignedRoots(certs)
	if len(roots) == 0 {
		return nil, fmt.Errorf("pinned appliance Root CA %s holds no self-signed CA certificate", m.pinFile())
	}
	return roots, nil
}

// checkAgainstPin proves the bundle and the client certificate chain to the pin. When no pin exists, the roots of
// the bundle become the pin (adopt reports true; the caller writes it once the pair is installed).
//
// at is the verification time: now when a certificate is being installed; a moment after the leaf was issued when an
// installed pair is re-read at boot, so an expired certificate never prevents the appliance from starting.
func (m *Manager) checkAgainstPin(certPEM, caPEM []byte, at time.Time, fallbackBundle []byte) (adopt []*x509.Certificate, err error) {
	bundle, err := parseCerts(caPEM)
	if err != nil {
		return nil, fmt.Errorf("ca bundle parse failed: %w", err)
	}
	leaves, err := parseCerts(certPEM)
	if err != nil {
		return nil, err
	}
	pinned, err := m.loadPin()
	if err != nil {
		return nil, err
	}
	if pinned == nil {
		if fallbackBundle != nil {
			if fb, ferr := parseCerts(fallbackBundle); ferr == nil {
				pinned = selfSignedRoots(fb)
			}
		}
		if pinned == nil {
			pinned = selfSignedRoots(bundle)
		}
		if pinned == nil {
			return nil, fmt.Errorf("%w: the bundle holds no self-signed root to pin", ErrCAChangedFromPin)
		}
		adopt = pinned
	}
	roots := x509.NewCertPool()
	for _, c := range pinned {
		roots.AddCert(c)
	}
	inter := x509.NewCertPool()
	for _, c := range bundle {
		inter.AddCert(c)
	}
	for _, c := range bundle {
		if isSelfSignedCA(c) {
			ok := false
			for _, p := range pinned {
				if bytes.Equal(p.Raw, c.Raw) {
					ok = true
				}
			}
			if !ok {
				return nil, fmt.Errorf("%w (root %q)", ErrCAChangedFromPin, c.Subject.CommonName)
			}
			continue
		}
		if _, verr := c.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: at,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); verr != nil {
			return nil, fmt.Errorf("%w (CA %q: %v)", ErrCAChangedFromPin, c.Subject.CommonName, verr)
		}
	}
	leaf := leaves[0]
	when := at
	if when.IsZero() {
		when = leaf.NotBefore.Add(time.Minute)
	}
	if _, verr := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: when,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); verr != nil {
		return nil, fmt.Errorf("%w (client certificate: %v)", ErrCAChangedFromPin, verr)
	}
	return adopt, nil
}

// writePin records adopted roots as the pin. Failure is logged, not fatal: the pair is already installed and the
// next load adopts again from ca.crt.
func (m *Manager) writePin(roots []*x509.Certificate, source string) {
	if len(roots) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.pinFile()), 0o755); err != nil {
		slog.Warn("appliancecert: could not create the pin directory", "err", err)
		return
	}
	if err := writeAtomic(m.pinFile(), pemOf(roots), 0o644); err != nil {
		slog.Warn("appliancecert: could not record the pinned appliance Root CA", "err", err)
		return
	}
	slog.Warn("appliancecert: the appliance Root CA was not provisioned; pinned from the "+source+" (install appliance-root-ca.crt at provisioning for a stronger guarantee)",
		"path", m.pinFile(), "roots", len(roots))
}
