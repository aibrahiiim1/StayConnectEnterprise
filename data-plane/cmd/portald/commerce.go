package main

// Phase 2 (dark) guest-portal commerce bridge, mounted only when the portal surface is on.
// Trust boundary: the browser submits only opaque ids (package_id, quote_id); Auth Context, Device and Guest Network come from portald's server-side commerce session and Tenant/Site from scd, so with no session every call fails closed to a generic "unavailable".

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

type commerceSession struct {
	authContextID  string
	deviceID       string
	guestNetworkID string
	expiry         time.Time
	// returnCode is shown once to a client who chose open selection, then cleared.
	returnCode string
	// identityLabel is the masked factor shown so a client on a shared device can tell whose allowance is used; never the full value.
	identityLabel string
	// acquired is set once the device is online; an unfinished journey continues at package selection, a finished one goes to the success page.
	acquired bool
	// notice is a refusal shown once on the package page; the reason travels here, never in a URL.
	notice string
	// purchaseID: a client returning to "/" is sent to the page asking where the card purchase stands, not the package list.
	purchaseID string
	// held: activation was refused until the email account verifies, so landing does not loop back to packages.
	held bool
	// replacedEntitlement: a retry activates on the package a replacement already moved a place to, rather than replacing another device.
	replacedEntitlement string
}

// commerceSessionStore maps an opaque cookie token to a trusted session; the browser never sets the pins.
type commerceSessionStore struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]commerceSession
}

func newCommerceSessionStore() *commerceSessionStore {
	return &commerceSessionStore{now: time.Now, m: map[string]commerceSession{}}
}

// commerceSessionPruneAt bounds the store: past it, put drops expired sessions (audit P5 L3), as pendingOTPStore does.
const commerceSessionPruneAt = 20000

func (s *commerceSessionStore) put(token string, sess commerceSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[token] = sess
	if len(s.m) > commerceSessionPruneAt {
		now := s.now()
		for k, v := range s.m {
			if !v.expiry.After(now) {
				delete(s.m, k)
			}
		}
	}
}

func (s *commerceSessionStore) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, token)
}

func (s *commerceSessionStore) get(token string) (commerceSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok || !sess.expiry.After(s.now()) {
		delete(s.m, token)
		return commerceSession{}, false
	}
	return sess, true
}

// resolveCommerceSession reads only the opaque server-issued cookie; ok=false when there is no session (the dark default).
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

func (h *handler) updateCommerceSession(r *http.Request, fn func(*commerceSession)) {
	if h.commerceSessions == nil {
		return
	}
	c, err := r.Cookie(commerceCookie)
	if err != nil || c.Value == "" {
		return
	}
	sess, ok := h.commerceSessions.get(c.Value)
	if !ok {
		return
	}
	fn(&sess)
	h.commerceSessions.put(c.Value, sess)
}

func commerceUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"unavailable"}`))
}

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

func relay(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(payload)
}

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

// Browser-submittable fields only; other JSON keys are ignored by construction.
type portalQuoteReq struct {
	PackageID string `json:"package_id"`
}
type portalConfirmReq struct {
	QuoteID string `json:"quote_id"`
}

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
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var granted struct {
		PurchaseID    string `json:"purchase_id"`
		EntitlementID string `json:"entitlement_id"`
	}
	_ = json.Unmarshal(raw, &granted)
	if resp.StatusCode != http.StatusOK || granted.EntitlementID == "" {
		// A refusal is relayed as scd gave it: the panel shows no success without an entitlement.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		return
	}
	// A grant is not a connection: the entitlement is returned only once the device is activated and enforced, as /packages/acquire requires.
	sid, failure := h.activateEnforced(r, sess, granted.EntitlementID)
	if failure != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": failure})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"purchase_id": granted.PurchaseID, "entitlement_id": granted.EntitlementID,
		"session_id": sid, "enforced": true})
}
