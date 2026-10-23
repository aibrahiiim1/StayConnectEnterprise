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

// ARICertID is the RFC 9773 §4.1 certificate identifier: base64url(AKI) "." base64url(serial). The serial
// keeps the leading zero a positive serial with its top bit set carries, which big.Int.Bytes() drops.
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

// DefaultRenewAfter is two thirds of the validity period; used when the CA offers no ARI.
func DefaultRenewAfter(notBefore, notAfter time.Time) time.Time {
	return notBefore.Add(notAfter.Sub(notBefore) * 2 / 3)
}

// pickInWindow picks a random moment in [start, end) so a fleet does not renew in the same second.
func pickInWindow(start, end time.Time) time.Time {
	if !end.After(start) {
		return start
	}
	return start.Add(time.Duration(rand.Int64N(int64(end.Sub(start)))))
}

// directoryRenewalInfo returns the CA's renewalInfo URL, "" when none; x/crypto/acme does not expose it.
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

// RenewAfter returns a random point in the CA's ARI window, else DefaultRenewAfter; the bool reports whether ARI decided.
func RenewAfter(ctx context.Context, hc *http.Client, directoryURL string, cert *x509.Certificate) (time.Time, bool) {
	if start, end, ok := ARIWindow(ctx, hc, directoryURL, cert); ok {
		return pickInWindow(start, end), true
	}
	return DefaultRenewAfter(cert.NotBefore, cert.NotAfter), false
}

// ARIWindow asks the CA for the suggested renewal window; ok=false when it offers none.
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
