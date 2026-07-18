package main

// Phase 2 (DARK) guest-portal commerce bridge. These routes are mounted only when the Phase-2 portal
// surface is ON; while dark they are absent and no scd commerce call is ever made.
//
// TRUST BOUNDARY (WS-D/item 3): the guest browser may submit ONLY opaque ids — a package_id to quote and
// a quote_id to confirm. Tenant, Site, Auth Context, IAM-v2 Device and Guest Network are NEVER read from
// the browser. Portald derives Auth Context / Device / Guest Network from its own trusted server-side
// commerce session (issued by the IAM-v2 auth flow, keyed by an opaque httpOnly cookie the browser cannot
// forge) and forwards them to scd over the existing protected Unix socket; scd fills Tenant/Site from its
// appliance config. Because IAM-v2 authentication is still DARK, no commerce session exists in production,
// so every bridge call fail-closes to a single generic "unavailable" — it never fabricates an Auth Context
// and never falls back to a browser-supplied id.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const commerceCookie = "sc_commerce"

// commerceSession holds the server-derived pins for a guest whose IAM-v2 auth produced an Auth Context.
type commerceSession struct {
	authContextID  string
	deviceID       string
	guestNetworkID string
	expiry         time.Time
}

// commerceSessionStore maps an opaque cookie token to a trusted commerceSession. The browser only ever
// holds the token; it can never set the pins directly.
type commerceSessionStore struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]commerceSession
}

func newCommerceSessionStore() *commerceSessionStore {
	return &commerceSessionStore{now: time.Now, m: map[string]commerceSession{}}
}

func (s *commerceSessionStore) put(token string, sess commerceSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[token] = sess
}

func (s *commerceSessionStore) get(token string) (commerceSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok || !sess.expiry.After(s.now()) {
		return commerceSession{}, false
	}
	return sess, true
}

// resolveCommerceSession returns the trusted pins for this request, or ok=false when there is no valid
// IAM-v2 commerce session (the dark default). It reads ONLY the opaque server-issued cookie.
func (h *handler) resolveCommerceSession(r *http.Request) (commerceSession, bool) {
	if h.commerceSessions == nil {
		return commerceSession{}, false
	}
	c, err := r.Cookie(commerceCookie)
	if err != nil || c.Value == "" {
		return commerceSession{}, false
	}
	return h.commerceSessions.get(c.Value)
}

// commerceUnavailable is the single generic guest-facing response for every non-happy path.
func commerceUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"unavailable"}`))
}

// scdDo issues a request to scd over the trusted Unix socket and returns the response.
func (h *handler) scdDo(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return h.scd.Do(req)
}

// relay copies scd's (already guest-safe, generic) status + JSON body to the guest.
func relay(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(payload)
}

// GET /api/commerce/packages — list the guest's eligible free packages.
func (h *handler) commercePackages(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.resolveCommerceSession(r)
	if !ok {
		commerceUnavailable(w)
		return
	}
	q := url.Values{}
	q.Set("auth_context_id", sess.authContextID)
	q.Set("device_id", sess.deviceID)
	q.Set("guest_network_id", sess.guestNetworkID)
	resp, err := h.scdDo(r.Context(), "GET", "/v1/commerce/packages?"+q.Encode(), nil)
	if err != nil {
		commerceUnavailable(w)
		return
	}
	relay(w, resp)
}

// browser-submittable fields ONLY. Any other JSON keys are ignored by construction.
type portalQuoteReq struct {
	PackageID string `json:"package_id"`
}
type portalConfirmReq struct {
	QuoteID string `json:"quote_id"`
}

// POST /api/commerce/quote — create a quote for the selected package.
func (h *handler) commerceQuote(w http.ResponseWriter, r *http.Request) {
	var in portalQuoteReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		commerceUnavailable(w)
		return
	}
	sess, ok := h.resolveCommerceSession(r)
	if !ok {
		commerceUnavailable(w)
		return
	}
	// Pins come SOLELY from the trusted session; only the opaque package_id comes from the browser.
	body, _ := json.Marshal(map[string]string{
		"auth_context_id":  sess.authContextID,
		"device_id":        sess.deviceID,
		"guest_network_id": sess.guestNetworkID,
		"package_id":       in.PackageID,
	})
	resp, err := h.scdDo(r.Context(), "POST", "/v1/commerce/quote", body)
	if err != nil {
		commerceUnavailable(w)
		return
	}
	relay(w, resp)
}

// POST /api/commerce/confirm — confirm a free purchase for a prior quote.
func (h *handler) commerceConfirm(w http.ResponseWriter, r *http.Request) {
	var in portalConfirmReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		commerceUnavailable(w)
		return
	}
	sess, ok := h.resolveCommerceSession(r)
	if !ok {
		commerceUnavailable(w)
		return
	}
	body, _ := json.Marshal(map[string]string{
		"quote_id":         in.QuoteID,
		"device_id":        sess.deviceID,
		"guest_network_id": sess.guestNetworkID,
	})
	resp, err := h.scdDo(r.Context(), "POST", "/v1/commerce/confirm", body)
	if err != nil {
		commerceUnavailable(w)
		return
	}
	relay(w, resp)
}
