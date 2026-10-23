package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/social"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

type socialStartReq struct {
	Provider string `json:"provider"`
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	// portald's own view of its callback: read only by the development stub on plain HTTP; real providers get the canonical HTTPS URI (offerSocial).
	RedirectURI string `json:"redirect_uri"`
}

type socialStartResp struct {
	State        string `json:"state"`
	AuthorizeURL string `json:"authorize_url"`
	ExpiresAt    string `json:"expires_at"`
}

func (s *server) socialStart(w http.ResponseWriter, r *http.Request) {
	if !s.licenseGate(w, licstate.FeatSocialLogin) {
		return
	}
	var req socialStartReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if req.Provider == "" {
		httpErr(w, http.StatusBadRequest, "provider required")
		return
	}

	load := s.methodSwitches
	if load == nil {
		load = func(ctx context.Context) (*tenantcfg.AuthMethods, error) { return tenantcfg.Load(ctx, s.db, s.tenID) }
	}
	cfg, err := load(r.Context())
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "tenant config unavailable")
		return
	}
	method, ok := cfg.Social[req.Provider]
	if !ok || method == nil || !method.Enabled {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": reasonProviderNotEnabled})
		return
	}

	// OFFERED ONLY OVER HTTPS, ON A CALLBACK HOST THE HOTEL REGISTERED. The redirect URI is derived from the canonical hostname, not the Host header
	// or the provider row, and stored on the attempt so the exchange uses the value the provider was sent.
	offer := s.offerSocial(req.Provider, req.RedirectURI)
	if !offer.ok {
		slog.Info("social sign-in start refused", "provider", req.Provider, "reason", offer.reason)
		writeJSON(w, http.StatusConflict, map[string]any{"error": offer.reason})
		return
	}

	if ok, retry := s.throttleGuard(r.Context(), "social", net.ParseIP(req.IP), nil, ""); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
		return
	}

	state, err := newSocialState()
	if err != nil {
		slog.Error("social state gen", "err", err)
		httpErr(w, http.StatusInternalServerError, "state gen failed")
		return
	}
	verifier, nonce, err := social.NewAttemptSecrets(offer.provider.Binding())
	if err != nil {
		slog.Error("social attempt secrets", "err", err)
		httpErr(w, http.StatusInternalServerError, "state gen failed")
		return
	}
	expires := time.Now().Add(10 * time.Minute)

	// An OAuth handshake record: binds the callback to the device that started the flow (anti-CSRF, never identity) and carries the PKCE
	// verifier and OIDC nonce, which only scd holds.
	_, err = s.db.Exec(r.Context(), `
        INSERT INTO social_oauth_states
          (state, tenant_id, appliance_id, provider,
           client_ip, client_mac, redirect_uri, user_agent, expires_at, code_verifier, nonce)
        VALUES
          ($1, $2, NULLIF($3,'')::uuid, $4,
           CASE WHEN $5 = '' THEN NULL ELSE $5::inet END,
           CASE WHEN $6 = '' THEN NULL ELSE $6::macaddr END,
           $7, NULLIF($8,''), $9, NULLIF($10,''), NULLIF($11,''))
    `, state, s.tenID, s.applID, req.Provider,
		req.IP, req.MAC, offer.redirectURI, r.UserAgent(), expires, verifier, nonce)
	if err != nil {
		slog.Error("social state insert", "err", err)
		httpErr(w, http.StatusInternalServerError, "state insert failed")
		return
	}

	writeJSON(w, http.StatusOK, socialStartResp{
		State:        state,
		AuthorizeURL: authorizeURLFor(offer.provider, state, offer.redirectURI, verifier, nonce),
		ExpiresAt:    expires.UTC().Format(time.RFC3339),
	})
}

// The PKCE challenge is derived here so the verifier never leaves scd except on the server-to-server token request.
func authorizeURLFor(p social.Provider, state, redirectURI, verifier, nonce string) string {
	a := social.AuthRequest{State: state, RedirectURI: redirectURI, Nonce: nonce}
	if verifier != "" {
		a.CodeChallenge = social.PKCEChallengeS256(verifier)
	}
	return p.AuthorizeURL(a)
}

type socialAuthorizeReq struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Code     string `json:"code"`
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
}

func (s *server) authorizeSocial(w http.ResponseWriter, r *http.Request) {
	if !s.licenseGate(w, licstate.FeatSocialLogin) {
		return
	}
	var req socialAuthorizeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	ip := net.ParseIP(req.IP)
	if ip == nil || ip.To4() == nil {
		httpErr(w, http.StatusBadRequest, "bad ip")
		return
	}
	mac, err := net.ParseMAC(req.MAC)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "bad mac")
		return
	}
	if ok, retry := s.throttleGuard(r.Context(), "social", ip, mac, ""); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
		return
	}

	// Look up and consume state atomically: UPDATE ... RETURNING, so a second call sees it consumed.
	var (
		stTenant, stProvider, stRedirect string
		stVerifier, stNonce              string
		stClientIP                       *string
		stClientMAC                      *string
		stExpires                        time.Time
		stConsumed                       *time.Time
	)
	tx, err := s.db.BeginTx(r.Context(), pgx.TxOptions{})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "tx begin")
		return
	}
	defer tx.Rollback(r.Context())

	err = tx.QueryRow(r.Context(), `
        SELECT tenant_id::text, provider, redirect_uri, COALESCE(code_verifier,''), COALESCE(nonce,''),
               host(client_ip), client_mac::text, expires_at, consumed_at
          FROM social_oauth_states WHERE state = $1 FOR UPDATE
    `, req.State).Scan(&stTenant, &stProvider, &stRedirect, &stVerifier, &stNonce,
		&stClientIP, &stClientMAC, &stExpires, &stConsumed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpErr(w, http.StatusBadRequest, "unknown state — possible CSRF")
			return
		}
		httpErr(w, http.StatusInternalServerError, "state lookup")
		return
	}
	if stConsumed != nil {
		httpErr(w, http.StatusConflict, "state already consumed")
		return
	}
	if time.Now().After(stExpires) {
		httpErr(w, http.StatusGone, "state expired")
		return
	}
	// The provider is the one the state row was started for. A real IdP returns only code and state, so portald may send no provider; if it
	// sends one (the Stub does) it must agree with the state.
	if req.Provider == "" {
		req.Provider = stProvider
	}
	if stProvider != req.Provider {
		httpErr(w, http.StatusBadRequest, "provider mismatch")
		return
	}
	if stTenant != s.tenID {
		// Defends against cross-tenant state replay if scd is ever multi-tenanted.
		httpErr(w, http.StatusForbidden, "tenant mismatch")
		return
	}
	// CSRF binding: the device that started must be the device finishing.
	if stClientIP != nil && *stClientIP != ip.String() {
		httpErr(w, http.StatusForbidden, "device IP changed mid-flow")
		return
	}
	if stClientMAC != nil && !strings.EqualFold(*stClientMAC, mac.String()) {
		httpErr(w, http.StatusForbidden, "device MAC changed mid-flow")
		return
	}

	prov, err := s.currentSocial().reg.Get(req.Provider)
	if err != nil {
		s.met.SocialLoginTotal.WithLabelValues(req.Provider, "bad_state").Inc()
		httpErr(w, http.StatusBadRequest, "unknown provider")
		return
	}
	// The exchange uses the attempt's OWN redirect URI, verifier and nonce, never anything the callback carries; the provider refuses a missing
	// binding or an id_token whose nonce is not this attempt's.
	exchStart := time.Now()
	info, err := prov.Exchange(r.Context(), social.ExchangeRequest{
		Code: req.Code, RedirectURI: stRedirect, CodeVerifier: stVerifier, Nonce: stNonce})
	s.met.SocialLoginDuration.WithLabelValues(req.Provider).Observe(time.Since(exchStart).Seconds())
	if err != nil {
		// ErrEmailUnverified is returned WITH a populated UserInfo (for a friendlier prompt); every other error is a hard failure.
		if errors.Is(err, social.ErrEmailUnverified) {
			s.met.SocialLoginTotal.WithLabelValues(req.Provider, "email_unverified").Inc()
			httpErr(w, http.StatusForbidden, "email not verified by provider")
			return
		}
		slog.Warn("social exchange", "err", err)
		s.met.SocialLoginTotal.WithLabelValues(req.Provider, "failed").Inc()
		httpErr(w, http.StatusBadRequest, "code exchange failed")
		return
	}
	if !info.EmailVerified || info.Email == "" {
		s.met.SocialLoginTotal.WithLabelValues(req.Provider, "email_unverified").Inc()
		httpErr(w, http.StatusForbidden, "email not verified by provider")
		return
	}
	s.met.SocialLoginTotal.WithLabelValues(req.Provider, "ok").Inc()

	if _, err := tx.Exec(r.Context(),
		`UPDATE social_oauth_states SET consumed_at = now() WHERE state = $1`, req.State); err != nil {
		httpErr(w, http.StatusInternalServerError, "state consume")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpErr(w, http.StatusInternalServerError, "state commit")
		return
	}

	// The provider verifies the identity; the principal is IAM-v2. social_oauth_states is only the handshake nonce store: just the verified,
	// issuer-scoped subject crosses into iam_v2 (authSocialIdentity). The provider sub is the identity factor, so an email change at the provider
	// is not a new Client; a trusted issuer's verified email is a second factor on the same Client (identity.go). Facebook asserts no verification and links nothing.
	primary, secondary := iamv2.SocialFactors(req.Provider, info.Sub, info.Email, info.EmailVerified, info.Claims)
	s.authorizeViaIAMv2(w, r, iamv2.MethodSocial, iamv2.Request{
		Provider:         req.Provider,
		Credential:       iamv2.CredentialForSocial(req.Provider),
		FactorIssuer:     primary.Issuer,
		FactorValue:      primary.Value,
		FactorAttrs:      primary.Attrs,
		SecondaryFactors: secondary,
		Device:           iamv2.DeviceContext{MAC: mac.String()},
	}, ip)
}

func newSocialState() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Removes every social provider that may not be started now (offerSocial), so the portal renders a button only for a start that would succeed.
func (s *server) applySocialOffering(cfg *tenantcfg.AuthMethods) {
	if cfg == nil {
		return
	}
	for p := range cfg.Social {
		if !s.offerSocial(p, "").ok && !s.stubOfferable(p) {
			delete(cfg.Social, p)
		}
	}
	if len(cfg.Social) == 0 {
		cfg.Social = nil
	}
}

// In a development build the stub is reachable over plain HTTP through portald's own callback, which the method list cannot know, so its button stays.
func (s *server) stubOfferable(provider string) bool {
	set := s.currentSocial()
	p, err := set.reg.Get(provider)
	if err != nil || set.real[provider] {
		return false
	}
	_, isStub := p.(*social.Stub)
	return isStub
}
