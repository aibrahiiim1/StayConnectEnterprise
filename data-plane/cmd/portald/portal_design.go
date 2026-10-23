package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/payment/paymob"
	"github.com/stayconnect/enterprise/data-plane/internal/payment/stripe"
	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
	"github.com/stayconnect/enterprise/data-plane/internal/portaltls"
)

// Content-Security-Policy: guests reach portald directly (nftables DNAT), so no proxy sends headers and this
// policy is the last line of defence if the hotel design's two allowlists (on save in edged, on serve) fail.
//   - script-src 'nonce-…': only the page's own script blocks run; injected or inline-handler code has no nonce.
//   - base-uri 'none': a <base href> cannot re-point the forms' relative actions.
//   - form-action: this portal, its own HTTPS names, or a payment provider's hosted checkout.
//   - connect-src 'self', object-src 'none', frame-ancestors 'none'.
//   - img-src/font-src allow https and data: for hotel logos and fonts; style-src keeps 'unsafe-inline' for
//     inline CSS, which cannot run code.

func newNonce() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		// A fixed nonce would be guessable; refusing every script is the safe failure.
		return "unavailable"
	}
	// URL-safe and unpadded, so the header and the markup carry the identical string without escaping.
	return base64.RawURLEncoding.EncodeToString(b)
}

func portalCSP(nonce string) string {
	return "default-src 'self'; " +
		"script-src 'nonce-" + nonce + "'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: https:; " +
		"font-src 'self' data: https:; " +
		"connect-src 'self'; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"form-action " + formActionSources() + "; " +
		"frame-ancestors 'none'"
}

// Chromium and WebKit check form-action against every hop of a form's redirect chain, so two journeys end
// outside 'self' by design:
//   - Card payment: /packages/acquire answers 303 to the provider's hosted checkout. The page also submits
//     by script and navigates with window.location, but the plain form needs the adapters' own
//     RequiredHostedDomains allowed: fixed provider hosts, https only, never a request or database value.
//   - The HTTPS portal: a form posted over the HTTP fallback or a non-canonical name is answered 307 to
//     https://<canonical>/... (portal_https.go), so its portal names are allowed, published by
//     refreshTLSState from scd's sanitised state.

var hostedCheckoutOrigins = func() []string {
	var out []string
	seen := map[string]bool{}
	add := func(ds []string) {
		for _, d := range ds {
			d = strings.ToLower(strings.TrimSpace(d))
			if d == "" || strings.ContainsAny(d, " ;,'\"*") || seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, "https://"+d)
		}
	}
	add(stripe.New().RequiredHostedDomains())
	names := make([]string, 0, len(paymob.Regions))
	for n := range paymob.Regions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if a, err := paymob.New(paymob.WithRegion(n)); err == nil {
			add(a.RequiredHostedDomains())
		}
	}
	return out
}()

// portalFormOrigins holds https://<canonical> and each portal name, replaced when scd's HTTPS state is learned.
var portalFormOrigins atomic.Value // []string

func setPortalFormOrigins(st portalTLSState) {
	var out []string
	seen := map[string]bool{}
	for _, h := range append([]string{st.Canonical, st.OneGateHostname, st.CustomDomain}, st.Hostnames...) {
		if h == "" || seen[h] || !portaltls.ValidHostname(h) {
			continue
		}
		seen[h] = true
		out = append(out, "https://"+h)
	}
	portalFormOrigins.Store(out)
}

func formActionSources() string {
	parts := []string{"'self'"}
	if v, ok := portalFormOrigins.Load().([]string); ok {
		parts = append(parts, v...)
	}
	parts = append(parts, hostedCheckoutOrigins...)
	return strings.Join(parts, " ")
}

// setPortalCSP returns the nonce the response's scripts must carry.
func setPortalCSP(w http.ResponseWriter) string {
	nonce := newNonce()
	w.Header().Set("Content-Security-Policy", portalCSP(nonce))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	return nonce
}

// designCacheTTL: every captive-portal probe renders the landing page, so a few seconds' staleness after a
// save is invisible while a read per probe is not.
const designCacheTTL = 3 * time.Second

type designCache struct {
	mu      sync.Mutex
	fetched time.Time
	design  map[string]any
	ok      bool
}

// guestDesign returns the design sanitised by portaldesign.ForGuests, or ok=false. It never errors: branding
// must never stop a guest signing in.
func (h *handler) guestDesign(ctx context.Context) (map[string]any, bool) {
	if h.scd == nil {
		return nil, false
	}
	c := h.designs
	if c != nil {
		c.mu.Lock()
		if !c.fetched.IsZero() && time.Since(c.fetched) < designCacheTTL {
			d, ok := c.design, c.ok
			c.mu.Unlock()
			return d, ok
		}
		c.mu.Unlock()
	}
	d, ok := h.fetchGuestDesign(ctx)
	if c != nil {
		c.mu.Lock()
		c.fetched, c.design, c.ok = time.Now(), d, ok
		c.mu.Unlock()
	}
	return d, ok
}

func (h *handler) fetchGuestDesign(ctx context.Context) (map[string]any, bool) {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/tenant/branding", nil)
	if err != nil {
		return nil, false
	}
	resp, err := h.scd.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var doc struct {
		Design map[string]any `json:"design"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4*portaldesign.MaxBodyBytes))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, false
	}
	return portaldesign.ForGuests(doc.Design), true
}
