package main

import (
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
	Provider    string `json:"provider"`
	IP          string `json:"ip"`
	MAC         string `json:"mac"`
	RedirectURI string `json:"redirect_uri"` // portald's external callback URL
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
	if req.Provider == "" || req.RedirectURI == "" {
		httpErr(w, http.StatusBadRequest, "provider and redirect_uri required")
		return
	}

	cfg, err := tenantcfg.Load(r.Context(), s.db, s.tenID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "tenant config unavailable")
		return
	}
	method, ok := cfg.Social[req.Provider]
	if !ok || method == nil || !method.Enabled {
		httpErr(w, http.StatusForbidden, "provider not enabled for this tenant")
		return
	}

	if ok, retry := s.throttleGuard(r.Context(), "social", net.ParseIP(req.IP), nil, ""); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
		return
	}

	prov, err := s.socialReg.Get(req.Provider)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "unknown provider")
		return
	}

	state, err := newSocialState()
	if err != nil {
		slog.Error("social state gen", "err", err)
		httpErr(w, http.StatusInternalServerError, "state gen failed")
		return
	}
	expires := time.Now().Add(10 * time.Minute)

	// The state row is an OAuth HANDSHAKE nonce: it binds the callback to the device that started the
	// flow and nothing else. It no longer carries an access-plan id -- in the current model the guest
	// authenticates first and chooses a package afterwards in the commerce flow, so pinning a plan at
	// handshake time was the superseded coupling, not a requirement of OAuth.
	_, err = s.db.Exec(r.Context(), `
        INSERT INTO social_oauth_states
          (state, tenant_id, appliance_id, provider,
           client_ip, client_mac, redirect_uri, user_agent, expires_at)
        VALUES
          ($1, $2, NULLIF($3,'')::uuid, $4,
           CASE WHEN $5 = '' THEN NULL ELSE $5::inet END,
           CASE WHEN $6 = '' THEN NULL ELSE $6::macaddr END,
           $7, NULLIF($8,''), $9)
    `, state, s.tenID, s.applID, req.Provider,
		req.IP, req.MAC, req.RedirectURI, r.UserAgent(), expires)
	if err != nil {
		slog.Error("social state insert", "err", err)
		httpErr(w, http.StatusInternalServerError, "state insert failed")
		return
	}

	writeJSON(w, http.StatusOK, socialStartResp{
		State:        state,
		AuthorizeURL: prov.AuthorizeURL(state, req.RedirectURI),
		ExpiresAt:    expires.UTC().Format(time.RFC3339),
	})
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
        SELECT tenant_id::text, provider, redirect_uri,
               host(client_ip), client_mac::text, expires_at, consumed_at
          FROM social_oauth_states WHERE state = $1 FOR UPDATE
    `, req.State).Scan(&stTenant, &stProvider, &stRedirect,
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

	prov, err := s.socialReg.Get(req.Provider)
	if err != nil {
		s.met.SocialLoginTotal.WithLabelValues(req.Provider, "bad_state").Inc()
		httpErr(w, http.StatusBadRequest, "unknown provider")
		return
	}
	exchStart := time.Now()
	info, err := prov.Exchange(r.Context(), req.Code, stRedirect)
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
	s.authorizeViaIAMv2(w, r, iamv2.MethodSocial, iamv2.Request{
		FactorIssuer: req.Provider,
		FactorValue:  info.Email,
		Device:       iamv2.DeviceContext{MAC: mac.String()},
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
