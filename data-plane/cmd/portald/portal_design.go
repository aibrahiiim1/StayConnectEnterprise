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

// ---- Content-Security-Policy ---------------------------------------------------------------------------
//
// THE SIGN-IN PAGE'S LAST LINE OF DEFENCE. Guests reach portald directly -- nftables DNATs their traffic to
// :8380/:8343 -- so no reverse proxy's headers ever apply to these pages; if portald does not send a policy,
// there is none. The hotel's design is allowlisted twice before it reaches a guest (on save in edged, on serve
// below), and this policy is what still holds if both of those were somehow wrong:
//
//   - script-src 'nonce-…': only the page's OWN script blocks run, each carrying a nonce minted for this one
//     response. An inline event handler or an injected <script> has no nonce and does not execute.
//   - base-uri 'none': a <base href> cannot re-point the voucher and credentials forms' relative actions.
//   - form-action: a form submits to this portal; its redirect chain may end only on this portal's own HTTPS
//     names or on a payment provider's hosted checkout (formActionSources below).
//   - connect-src 'self': the page's fetches (room sign-in, OTP, branding) cannot be sent anywhere else.
//   - object-src 'none', frame-ancestors 'none': no plugins, and the portal cannot be framed by another site.
//   - img-src/font-src allow https and data: because a hotel may name an https logo or font; style-src keeps
//     'unsafe-inline' because the portal and the hotel style the page with inline CSS, which cannot run code.
//
// Every existing flow is same-origin (fetch to /auth/…, form POST to /auth/…, redirects to /success and
// /packages) or a plain navigation (social sign-in is a link that redirects), so none of them is affected.

func newNonce() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on a supported platform; if it ever did, a fixed nonce would be a policy
		// that allows a guessable value. Refusing every script is the safe failure -- the page degrades, it
		// does not open.
		return "unavailable"
	}
	// URL-safe and unpadded: the CSP grammar allows "-" and "_", and neither needs escaping in an HTML attribute,
	// so the value in the header and the value in the markup are byte-for-byte the same string.
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

// ---- form-action: where a form submission may END UP ----------------------------------------------------
//
// Chromium and WebKit check form-action against every hop of a form submission's redirect chain, not only the
// form's own action. Two journeys therefore end outside 'self' by design:
//
//   - CARD PAYMENT. "Pay by card" posts to /packages/acquire, which answers 303 to the provider's hosted checkout
//     (checkout.stripe.com, a Paymob checkout host). With form-action 'self' alone that redirect was blocked and
//     the client saw nothing happen (audit P-1). The page now submits that form by script and navigates with
//     window.location (which form-action does not govern), and -- so the plain form still works without script --
//     the hosted-checkout origins the payment adapters declare are allowed here too. They are the adapters' own
//     RequiredHostedDomains (stripe, every Paymob region): fixed provider hosts, https only, never a value from a
//     request or the database.
//   - THE HTTPS PORTAL. A form loaded over the HTTP fallback (or on a portal name that is not canonical) and
//     submitted after HTTPS came up is answered 307 to https://<canonical>/... (portal_https.go). That origin is
//     not 'self' for the page that posted, so its portal names are allowed as well (audit P-33). They are
//     published by refreshTLSState from scd's sanitised state.

// hostedCheckoutOrigins are the payment providers' hosted-checkout origins, as https:// sources.
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

// portalFormOrigins holds the portal's own other origins (https://<canonical>, https://<each portal name>),
// replaced whenever portald learns scd's HTTPS state.
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

// setPortalCSP sets the policy for one HTML response and returns the nonce its scripts must carry.
func setPortalCSP(w http.ResponseWriter) string {
	nonce := newNonce()
	w.Header().Set("Content-Security-Policy", portalCSP(nonce))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	return nonce
}

// ---- the published design, as a guest may receive it ----------------------------------------------------

// designCacheTTL bounds how often a busy portal asks scd for the design. Every captive-portal probe renders
// the landing page, and a lobby full of phones produces many of them per second; a few seconds' staleness
// after an operator saves is invisible, a database read per probe is not.
const designCacheTTL = 3 * time.Second

type designCache struct {
	mu      sync.Mutex
	fetched time.Time
	design  map[string]any
	ok      bool
}

// guestDesign returns the published design with every field re-checked and the custom CSS and HTML
// sanitised (portaldesign.ForGuests), or ok=false when there is none to be had. It never errors: a guest must
// never be unable to sign in because branding could not be read.
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
