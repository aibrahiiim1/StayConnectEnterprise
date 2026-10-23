package main

// Provider client secrets (or Apple .p8 keys) are sealed by scd with AES-256-GCM under social_dek.key, the AAD binding tenant, provider row and
// generation, into iam_v2.social_provider_secret_generations; client_secret is then NULL. Providers are built from opened sealed secrets ONLY,
// never the plaintext column. The registry is rebuilt on POST /v1/social/reload or a fingerprint change (checked every 60 s) and swapped atomically.
// A provider is OFFERED only while the portal is served over HTTPS and the canonical hostname is a callback host the hotel registered; real providers accept HTTPS callbacks only.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
	"github.com/stayconnect/enterprise/data-plane/internal/social"
	"github.com/stayconnect/enterprise/data-plane/internal/socialloader"
)

const (
	socialKeyFile = "social_dek.key"
	socialDomain  = "social-secret-aead:v1"

	// How often scd compares the configuration fingerprint; a rebuild happens only on change.
	socialReloadEvery = 60 * time.Second

	socialCallbackPath = "/auth/social/callback"
)

// Reason codes for a provider that is not offered; they reach edged and the portald log, never a guest.
const (
	reasonHTTPFallback       = "social_unavailable_http_fallback"
	reasonCallbackNotRegd    = "social_callback_not_registered"
	reasonProviderNotReady   = "social_provider_not_configured"
	reasonProviderNotEnabled = "social_provider_not_enabled"
)

// The portal HTTPS state (active, canonical hostname) from the portal-TLS subsystem; the default reports not active, which hides every real provider (the HTTP fallback).
type portalHosts interface {
	PortalTLS() (httpsActive bool, canonical string)
}

type noPortalHTTPS struct{}

func (noPortalHTTPS) PortalTLS() (bool, string) { return false, "" }

func (a *portalTLSAgent) PortalTLS() (bool, string) {
	st := a.State()
	return st.HTTPSActive, st.Canonical
}

func (s *server) portalHTTPS() (bool, string) {
	if s.socialPortal == nil {
		return false, ""
	}
	active, canonical := s.socialPortal.PortalTLS()
	canonical = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(canonical)), ".")
	if !active || canonical == "" {
		return false, ""
	}
	return true, canonical
}

type socialSet struct {
	reg   *social.Registry
	hosts map[string][]string // provider -> registered callback hosts (normalised)
	real  map[string]bool     // providers built from a configuration row
	fp    string              // configuration fingerprint this set was built from
}

func (s *server) currentSocial() *socialSet {
	if cur := s.socialSet.Load(); cur != nil {
		return cur
	}
	l := socialloader.Assemble(context.Background(), nil, nil, s.socialBase...)
	return &socialSet{reg: l.Registry, hosts: l.CallbackHosts, real: l.Real}
}

// Without the key no secret can be sealed or opened, so no real provider is built; other sign-in methods are unaffected.
func (s *server) initSocialKey(secretsDir string) {
	raw, err := localkeys.LoadExistingKey(filepath.Join(secretsDir, socialKeyFile))
	if err != nil {
		slog.Warn("social sign-in: sealing key missing (run keybootstrap at deploy); social providers will NOT be built", "err", err)
		return
	}
	k, err := sealbox.NewKey(socialDomain, raw)
	if err != nil {
		slog.Warn("social sign-in: bad sealing key; social providers will NOT be built", "err", err)
		return
	}
	s.socialKey = k
}

// The AAD names tenant, provider row and generation, so a ciphertext copied elsewhere does not open.
func sealSocial(k sealbox.Key, tenant, providerID string, gen int, secret string) (sealbox.Sealed, error) {
	return k.Seal([]byte(secret), tenant, providerID, strconv.Itoa(gen))
}

func openSocial(k sealbox.Key, tenant, providerID string, gen int, sl sealbox.Sealed) (string, error) {
	plain, err := k.Open(sl, tenant, providerID, strconv.Itoa(gen))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *server) openSocialSecret(ctx context.Context, providerID string) (string, bool) {
	if !s.socialKey.Present() || s.db == nil {
		return "", false
	}
	var gen, ver int
	var keyID string
	var nonce, ct []byte
	err := s.db.QueryRow(ctx, `SELECT generation_no, key_id, nonce, ciphertext, cipher_version
	      FROM iam_v2.social_provider_secret_generations
	     WHERE tenant_id=$1 AND provider_id=$2::uuid AND superseded_at IS NULL`, s.tenID, providerID).
		Scan(&gen, &keyID, &nonce, &ct, &ver)
	if err != nil {
		return "", false
	}
	plain, err := openSocial(s.socialKey, s.tenID, providerID, gen,
		sealbox.Sealed{Ciphertext: ct, Nonce: nonce, KeyID: keyID, CipherVersion: ver})
	if err != nil {
		slog.Warn("social sign-in: cannot open sealed secret (replaced key or tampered row)", "provider_id", providerID)
		return "", false
	}
	return plain, true
}

// Supersedes the old generation; the caller clears the plaintext column in the same transaction.
func (s *server) sealSocialSecretTx(ctx context.Context, tx pgx.Tx, providerID, secret string) (int, error) {
	// One writer per provider row; the partial unique index would refuse a second current generation anyway, this makes the loser wait instead of fail.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('social-secret:' || $1, 0))`, providerID); err != nil {
		return 0, fmt.Errorf("lock: %w", err)
	}
	var next int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(generation_no),0)+1 FROM iam_v2.social_provider_secret_generations
	     WHERE tenant_id=$1 AND provider_id=$2::uuid`, s.tenID, providerID).Scan(&next); err != nil {
		return 0, fmt.Errorf("generation: %w", err)
	}
	sealed, err := sealSocial(s.socialKey, s.tenID, providerID, next, secret)
	if err != nil {
		return 0, errors.New("seal")
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.social_provider_secret_generations SET superseded_at = now()
	     WHERE tenant_id=$1 AND provider_id=$2::uuid AND superseded_at IS NULL`, s.tenID, providerID); err != nil {
		return 0, fmt.Errorf("supersede: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.social_provider_secret_generations
	     (tenant_id, provider_id, generation_no, ciphertext, nonce, key_id, cipher_version)
	     VALUES ($1,$2::uuid,$3,$4,$5,$6,$7)`, s.tenID, providerID, next, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, sealed.CipherVersion); err != nil {
		return 0, fmt.Errorf("store: %w", err)
	}
	return next, nil
}

// Clears the plaintext only if it still equals the value sealed: if edged wrote a newer one meanwhile, the transaction rolls back and the next
// pass seals it, so a newer secret is never overwritten.
func (s *server) sealPlaintextSocialSecrets(ctx context.Context) {
	if s.db == nil || s.tenID == "" {
		return
	}
	type plain struct{ id, provider, secret string }
	rows, err := s.db.Query(ctx, `SELECT id::text, provider, client_secret FROM social_oauth_providers
	     WHERE tenant_id=$1 AND client_secret IS NOT NULL`, s.tenID)
	if err != nil {
		slog.Warn("social sign-in: plaintext secrets unreadable", "err", err)
		return
	}
	var todo []plain
	for rows.Next() {
		var p plain
		if rows.Scan(&p.id, &p.provider, &p.secret) == nil {
			todo = append(todo, p)
		}
	}
	rows.Close()
	if len(todo) == 0 {
		return
	}
	if !s.socialKey.Present() {
		slog.Error("social sign-in: plaintext provider secrets present but the sealing key is missing; they are NOT used and NOT sealed (run keybootstrap)", "rows", len(todo))
		return
	}
	for _, p := range todo {
		if err := s.sealOnePlaintext(ctx, p.id, p.secret); err != nil {
			slog.Warn("social sign-in: sealing a plaintext secret failed", "provider", p.provider, "err", err)
			continue
		}
		slog.Info("social sign-in: plaintext secret sealed and cleared", "provider", p.provider)
	}
}

var errPlaintextChanged = errors.New("plaintext changed while sealing")

func (s *server) sealOnePlaintext(ctx context.Context, id, secret string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if strings.TrimSpace(secret) != "" {
		if _, err := s.sealSocialSecretTx(ctx, tx, id, secret); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE social_oauth_providers SET client_secret = NULL
	     WHERE id=$1::uuid AND tenant_id=$2 AND client_secret = $3`, id, s.tenID, secret)
	if err != nil {
		return fmt.Errorf("clear: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errPlaintextChanged
	}
	return tx.Commit(ctx)
}

// Everything a rebuild depends on: provider config columns and updated_at, current sealed generation, whether plaintext is waiting, whether the
// key loaded. No secret enters it.
func (s *server) socialFingerprint(ctx context.Context) (string, error) {
	var fp string
	err := s.db.QueryRow(ctx, `
	    SELECT COALESCE(string_agg(md5(concat_ws('|', p.id::text, p.provider, p.enabled::text, p.client_id,
	                  COALESCE(p.scopes,''), p.extra::text, array_to_string(p.callback_hosts, ','), p.updated_at::text,
	                  COALESCE(g.id::text,'-'), (p.client_secret IS NOT NULL)::text)), ',' ORDER BY p.id), '')
	      FROM social_oauth_providers p
	      LEFT JOIN iam_v2.social_provider_secret_generations g
	        ON g.tenant_id = p.tenant_id AND g.provider_id = p.id AND g.superseded_at IS NULL
	     WHERE p.tenant_id = $1`, s.tenID).Scan(&fp)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("key=%t;%s", s.socialKey.Present(), fp), nil
}

// A failed read keeps the current registry: a database hiccup must not unconfigure a working sign-in method.
func (s *server) reloadSocial(ctx context.Context, force bool) error {
	s.socialMu.Lock()
	defer s.socialMu.Unlock()
	if s.db == nil || s.tenID == "" {
		if s.socialSet.Load() == nil {
			l := socialloader.Assemble(ctx, nil, nil, s.socialBase...)
			s.socialSet.Store(&socialSet{reg: l.Registry, hosts: l.CallbackHosts, real: l.Real})
		}
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s.sealPlaintextSocialSecrets(rctx)
	fp, err := s.socialFingerprint(rctx)
	if err != nil {
		slog.Warn("social sign-in: configuration fingerprint unreadable; keeping the current providers", "err", err)
		return err
	}
	cur := s.socialSet.Load()
	if !force && cur != nil && cur.fp == fp {
		return nil
	}
	var open socialloader.SecretOpener
	if s.socialKey.Present() {
		open = s.openSocialSecret
	}
	l, err := socialloader.Load(rctx, s.db, s.tenID, open, s.socialBase...)
	if err != nil && cur != nil {
		slog.Warn("social sign-in: provider load failed; keeping the current providers", "err", err)
		return err
	}
	s.socialSet.Store(&socialSet{reg: l.Registry, hosts: l.CallbackHosts, real: l.Real, fp: fp})
	slog.Info("social sign-in: providers loaded", "real", len(l.Real), "key", s.socialKey.Present())
	return err
}

func (s *server) socialReloadLoop(ctx context.Context) {
	t := time.NewTicker(socialReloadEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.reloadSocial(ctx, false)
		}
	}
}

type socialOffer struct {
	ok          bool
	reason      string
	provider    social.Provider
	redirectURI string // the callback the attempt is bound to; empty when not offered
	stub        bool
}

// devRedirect is the portal's own callback URL, used ONLY for the development stub on plain HTTP; it never reaches a real provider.
func (s *server) offerSocial(provider, devRedirect string) socialOffer {
	set := s.currentSocial()
	p, err := set.reg.Get(provider)
	if err != nil {
		return socialOffer{reason: reasonProviderNotReady}
	}
	active, canonical := s.portalHTTPS()
	if _, isStub := p.(*social.Stub); isStub && !set.real[provider] {
		redirect := strings.TrimSpace(devRedirect)
		if active {
			redirect = "https://" + canonical + socialCallbackPath
		}
		if redirect == "" {
			return socialOffer{reason: reasonHTTPFallback}
		}
		return socialOffer{ok: true, provider: p, redirectURI: redirect, stub: true}
	}
	if !active {
		return socialOffer{reason: reasonHTTPFallback}
	}
	registered := false
	for _, h := range set.hosts[provider] {
		if h == canonical {
			registered = true
			break
		}
	}
	if !registered {
		return socialOffer{reason: reasonCallbackNotRegd}
	}
	return socialOffer{ok: true, provider: p, redirectURI: "https://" + canonical + socialCallbackPath}
}

const maxSocialSecretBytes = 64 << 10 // an Apple .p8 key is ~250 bytes; anything this large is not a secret

// socialSecretStore: PUT /v1/social/providers/{id}/secret {"secret": "..."} -> {"ok":true,"generation":N}.
// Seals the secret as a new current generation, supersedes the previous one, clears the plaintext column and reloads. edged has authorised the operator.
func (s *server) socialSecretStore(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSocialSecretBytes)).Decode(&req); err != nil || strings.TrimSpace(req.Secret) == "" {
		httpErr(w, http.StatusBadRequest, "secret required")
		return
	}
	if !s.socialKey.Present() {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "social_key_missing",
			"message": "The appliance has no social sign-in sealing key; run keybootstrap at deploy."})
		return
	}
	if s.db == nil {
		httpErr(w, http.StatusServiceUnavailable, "no database")
		return
	}
	ctx := r.Context()
	var provider string
	err := s.db.QueryRow(ctx, `SELECT provider FROM social_oauth_providers WHERE id=$1::uuid AND tenant_id=$2`, id, s.tenID).Scan(&provider)
	if err != nil {
		httpErr(w, http.StatusNotFound, "provider not found")
		return
	}
	// An Apple "secret" is the .p8 key: refuse one that will never sign, now rather than at a guest's sign-in.
	if provider == "apple" {
		if _, err := social.ParseP256PrivateKey(req.Secret); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "error": "invalid_secret", "message": err.Error()})
			return
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "begin")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	gen, err := s.sealSocialSecretTx(ctx, tx, id, req.Secret)
	if err != nil {
		slog.Warn("social secret store", "provider", provider, "err", err)
		httpErr(w, http.StatusInternalServerError, "seal failed")
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE social_oauth_providers SET client_secret = NULL WHERE id=$1::uuid AND tenant_id=$2`, id, s.tenID); err != nil {
		httpErr(w, http.StatusInternalServerError, "clear")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpErr(w, http.StatusInternalServerError, "commit")
		return
	}
	_ = s.reloadSocial(ctx, true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "generation": gen})
}

// socialSecretState: GET /v1/social/providers/{id}/secret-state -> {"sealed": bool}. A yes/no, never the secret.
func (s *server) socialSecretState(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var sealed bool
	if s.db != nil {
		_ = s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM iam_v2.social_provider_secret_generations
		     WHERE tenant_id=$1 AND provider_id=$2::uuid AND superseded_at IS NULL)`, s.tenID, id).Scan(&sealed)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sealed": sealed})
}

// socialReload: POST /v1/social/reload -> {"ok":true,"providers":[...]} (the providers built from a row).
func (s *server) socialReload(w http.ResponseWriter, r *http.Request) {
	err := s.reloadSocial(r.Context(), true)
	names := []string{}
	for n := range s.currentSocial().real {
		names = append(names, n)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": err == nil, "providers": names})
}

// socialStatus: GET /v1/social/status -> the Admin Console's status line per enabled provider:
// {"https_active":bool,"canonical_hostname":"…","providers":[{"provider","offered","reason","callback_url"}]}.
func (s *server) socialStatus(w http.ResponseWriter, r *http.Request) {
	active, canonical := s.portalHTTPS()
	_, enabled, ok := s.readProviderReadiness(r.Context())
	if !ok {
		enabled = map[string]bool{}
	}
	for n := range s.currentSocial().real {
		enabled[n] = true
	}
	type row struct {
		Provider    string `json:"provider"`
		Offered     bool   `json:"offered"`
		Reason      string `json:"reason,omitempty"`
		CallbackURL string `json:"callback_url,omitempty"`
	}
	out := []row{}
	for _, p := range []string{"google", "microsoft", "apple", "facebook"} {
		if !enabled[p] {
			continue
		}
		o := s.offerSocial(p, "")
		out = append(out, row{Provider: p, Offered: o.ok, Reason: o.reason, CallbackURL: o.redirectURI})
	}
	writeJSON(w, http.StatusOK, map[string]any{"https_active": active, "canonical_hostname": canonical, "providers": out})
}
