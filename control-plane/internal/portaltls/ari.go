package portaltls

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// RENEWAL TIMING. Central decides when each certificate should be renewed and tells the appliance
// (renew_after); the appliance renews once now > renew_after. The CA's own advice wins when it gives any:
// ACME Renewal Information (ARI, RFC 9773) is how Let's Encrypt asks for early renewal -- after an incident
// that requires mass revocation, for instance -- so a revocation the CA plans shows up here as a window in
// the past, and the appliance renews before the certificate dies. Without ARI: two thirds of the lifetime.

// ARICertID is the RFC 9773 §4.1 certificate identifier: base64url(AKI keyIdentifier) "." base64url(serial),
// where the serial is the DER INTEGER's content octets -- including the leading zero a positive serial with
// its top bit set carries, which big.Int.Bytes() drops.
func ARICertID(cert *x509.Certificate) (string, error) {
	if len(cert.AuthorityKeyId) == 0 {
		return "", errors.New("certificate has no authority key identifier")
	}
	return ariCertID(cert.AuthorityKeyId, cert.SerialNumber), nil
}

func ariCertID(aki []byte, serial *big.Int) string {
	sb := serial.Bytes()
	if len(sb) == 0 {
		sb = []byte{0}
	}
	if sb[0]&0x80 != 0 {
		sb = append([]byte{0}, sb...)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(aki) + "." + enc.EncodeToString(sb)
}

// DefaultRenewAfter is two thirds of the way through the validity period.
func DefaultRenewAfter(notBefore, notAfter time.Time) time.Time {
	return notBefore.Add(notAfter.Sub(notBefore) * 2 / 3)
}

// pickInWindow chooses a uniformly random moment in [start, end): spreading renewals across the window is
// what the CA asks for, so a fleet does not renew in the same second.
func pickInWindow(start, end time.Time) time.Time {
	if !end.After(start) {
		return start
	}
	return start.Add(time.Duration(rand.Int64N(int64(end.Sub(start)))))
}

// directoryRenewalInfo returns the CA's renewalInfo URL, "" when it offers none. golang.org/x/crypto/acme does
// not expose this directory field, so it is read here.
func directoryRenewalInfo(ctx context.Context, hc *http.Client, directoryURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, directoryURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ACME directory: HTTP %d", resp.StatusCode)
	}
	var d struct {
		RenewalInfo string `json:"renewalInfo"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return "", err
	}
	return d.RenewalInfo, nil
}

// RenewAfter computes renew_after for a freshly issued certificate: a random point inside the ARI suggested
// window when the CA offers ARI and answers, otherwise DefaultRenewAfter. The second value reports whether
// ARI decided it.
func RenewAfter(ctx context.Context, hc *http.Client, directoryURL string, cert *x509.Certificate) (time.Time, bool) {
	if start, end, ok := ARIWindow(ctx, hc, directoryURL, cert); ok {
		return pickInWindow(start, end), true
	}
	return DefaultRenewAfter(cert.NotBefore, cert.NotAfter), false
}

// ARIWindow asks the issuing CA for the certificate's suggested renewal window. ok=false when the CA offers no
// ARI or does not answer usefully.
func ARIWindow(ctx context.Context, hc *http.Client, directoryURL string, cert *x509.Certificate) (start, end time.Time, ok bool) {
	ri, err := directoryRenewalInfo(ctx, hc, directoryURL)
	if err != nil || ri == "" {
		return start, end, false
	}
	id, err := ARICertID(cert)
	if err != nil {
		return start, end, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(ri, "/")+"/"+id, nil)
	if err != nil {
		return start, end, false
	}
	resp, err := hc.Do(req)
	if err != nil {
		return start, end, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return start, end, false
	}
	var v struct {
		SuggestedWindow struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"suggestedWindow"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&v); err != nil ||
		v.SuggestedWindow.Start.IsZero() || v.SuggestedWindow.End.Before(v.SuggestedWindow.Start) {
		return start, end, false
	}
	return v.SuggestedWindow.Start, v.SuggestedWindow.End, true
}
