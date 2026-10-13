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

// ---- /v1/auth/social/start --------------------------------------------------

type socialStartReq struct {
	Provider string `json:"provider"`
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	// RedirectURI is portald's own view of its callback URL. It is NOT what a real provider is sent: that is
	// always https://<canonical>/auth/social/callback (offerSocial). It is read only for the development stub
	// on plain HTTP.
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

	// The same switch record the portal's listing reads (methodSwitches is the test seam for it).
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

	// OFFERED ONLY OVER HTTPS, AND ONLY ON A CALLBACK HOST THE HOTEL REGISTERED (contract §5). The redirect URI
	// is derived here from the canonical hostname -- not from the Host header portald saw and not from the
	// provider row's stored redirect_uri -- and the attempt row stores it, so the exchange uses the very value
	// the provider was sent.
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

	// The state row is an OAuth HANDSHAKE record: it binds the callback to the device that started the flow
	// (anti-CSRF; never identity) and carries the attempt's PKCE verifier and OIDC nonce, which only scd ever
	// holds. It no longer carries an access-plan id -- in the current model the guest authenticates first and
	// chooses a package afterwards in the commerce flow.
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

// authorizeURLFor builds the consent URL of one attempt: the PKCE challenge is derived from the verifier here,
// so the verifier itself never leaves scd except on the server-to-server token request.
func authorizeURLFor(p social.Provider, state, redirectURI, verifier, nonce string) string {
	a := social.AuthRequest{State: state, RedirectURI: redirectURI, Nonce: nonce}
	if verifier != "" {
		a.CodeChallenge = social.PKCEChallengeS256(verifier)
	}
	return p.AuthorizeURL(a)
}

// ---- /v1/sessions/authorize-social -----------------------------------------

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

	// Look up + consume state atomically. UPDATE ... RETURNING gives us the
	// row on first fetch, then sets consumed_at; second call sees it consumed.
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
	// The provider is the one the state row was started for. A real IdP
	// returns only code + state to the callback (the redirect_uri registered
	// with it is the bare /auth/social/callback), so portald may send no
	// provider at all; when it does send one — the Stub round-trips it — it
	// must agree with the state.
	if req.Provider == "" {
		req.Provider = stProvider
	}
	if stProvider != req.Provider {
		httpErr(w, http.StatusBadRequest, "provider mismatch")
		return
	}
	if stTenant != s.tenID {
		// Should be impossible in single-tenant scd, but defends against
		// cross-tenant state replay if scd is ever multi-tenanted.
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
	// The exchange uses the attempt's OWN redirect URI, verifier and nonce -- the values the provider was sent
	// at start -- never anything the callback request carries. The provider refuses an attempt that lacks the
	// binding it uses, and an id_token whose nonce is not this attempt's.
	exchStart := time.Now()
	info, err := prov.Exchange(r.Context(), social.ExchangeRequest{
		Code: req.Code, RedirectURI: stRedirect, CodeVerifier: stVerifier, Nonce: stNonce})
	s.met.SocialLoginDuration.WithLabelValues(req.Provider).Observe(time.Since(exchStart).Seconds())
	if err != nil {
		// social.ErrEmailUnverified is returned WITH a populated UserInfo
		// (so callers can show a friendlier "verify your email" prompt);
		// every other error is a hard failure.
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

	// THE IDENTITY IS VERIFIED BY THE PROVIDER; THE PRINCIPAL IS IAM-v2's.
	//
	// social_oauth_states is the OAuth handshake nonce store and is deliberately not part of the IAM-v2
	// identity model, exactly as public.auth_otps is not: the provider exchange happens here and only the
	// verified, issuer-scoped subject crosses into iam_v2, where authSocialIdentity maps it to a principal
	// identity in iam_v2.guest_principal_identities.
	//
	// What was REMOVED is the superseded pipeline that followed: a legacy access-plan lookup for duration
	// and shaping, a public.sessions row, and direct nft/shaping calls. Access comes from the package the
	// guest selects after authenticating, not from the credential they used.
	//
	// THE SUBJECT IS THE IDENTITY (contract §2.1). The previous code stored the email string as the social
	// subject and discarded info.Sub, which contradicted the Phase-0 contract ("issuer-scoped social subject")
	// and made an email change at the provider a brand-new Client. The provider's sub is now the factor; a
	// TRUSTED issuer's verified email is a second factor on the same Client (identity.go), which is what joins a
	// Google sign-in to the Client an email code created. Facebook asserts no verification and links nothing.
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

// ---- helpers ----------------------------------------------------------------

func newSocialState() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// applySocialOffering removes from the portal's method list every social provider that may not be started
// right now (offerSocial): the portal then renders a button only for a provider whose start would succeed.
// Under the HTTP fallback that is none of the real ones.
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

// stubOfferable: in a development build the stub is reachable over plain HTTP through portald's own callback,
// which the method list cannot know; the button stays, as it always did there.
func (s *server) stubOfferable(provider string) bool {
	set := s.currentSocial()
	p, err := set.reg.Get(provider)
	if err != nil || set.real[provider] {
		return false
	}
	_, isStub := p.(*social.Stub)
	return isStub
}
