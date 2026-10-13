package main

// SOCIAL SECRETS ARE SEALED, THE PLAINTEXT COLUMN EMPTIES ITSELF, PROVIDERS ARE BUILT FROM SEALED SECRETS ONLY,
// AND AN ATTEMPT CARRIES ITS PKCE VERIFIER, NONCE AND CANONICAL REDIRECT URI (contract §5).
// Needs PHASE3_TEST_DSN (a disposable full-schema database); skipped otherwise.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/social"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	lic "github.com/stayconnect/enterprise/license"
)

func TestIntegration_SocialSecretsAreSealedAndAttemptsAreBound(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const tenant = "22222222-2222-2222-2222-222222222222"
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.social_oauth_states WHERE tenant_id=$1`, tenant)
		_, _ = pool.Exec(ctx, `DELETE FROM iam_v2.social_provider_secret_generations WHERE tenant_id=$1`, tenant)
		_, _ = pool.Exec(ctx, `DELETE FROM public.social_oauth_providers WHERE tenant_id=$1`, tenant)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := pool.Exec(ctx, `INSERT INTO public.tenants (id, slug, name) VALUES ($1,'t-social','t') ON CONFLICT (id) DO NOTHING`, tenant); err != nil {
		t.Fatal(err)
	}
	// A provider row as an older edged wrote it: the secret in plaintext, the callback host registered.
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO public.social_oauth_providers
	      (tenant_id, provider, enabled, client_id, client_secret, callback_hosts)
	      VALUES ($1,'google',true,'cid.apps.googleusercontent.com','plain-secret-1', ARRAY['wifi.hotel.example'])
	      RETURNING id::text`, tenant).Scan(&id); err != nil {
		t.Fatal(err)
	}

	// scd's own queries run as svc_scd when SOCIAL_TEST_SCD_DSN names it, proving the Gate-P grants (column
	// UPDATE of client_secret, the generations table, the advisory lock) are enough; the fixture uses the owner.
	scdPool := pool
	if d := os.Getenv("SOCIAL_TEST_SCD_DSN"); d != "" {
		if scdPool, err = pgxpool.New(ctx, d); err != nil {
			t.Fatal(err)
		}
		defer scdPool.Close()
	}
	newServer := func(withKey bool) *server {
		s := &server{db: scdPool, tenID: tenant, met: metrics.New("test", prometheus.Labels{}),
			socialPortal: fakePortal{true, "wifi.hotel.example"}}
		if withKey {
			s.socialKey = socialKeyForTest(t)
		}
		return s
	}

	// 1. WITHOUT THE KEY: nothing is sealed, nothing is built, the plaintext is NOT used.
	nokey := newServer(false)
	if err := nokey.reloadSocial(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := nokey.currentSocial().reg.Get("google"); err == nil {
		t.Fatal("a provider was built from a plaintext column without the sealing key")
	}

	// 2. WITH THE KEY: start-up seals the plaintext, clears the column, and builds from the sealed secret.
	s := newServer(true)
	if err := s.reloadSocial(ctx, true); err != nil {
		t.Fatal(err)
	}
	var plain *string
	var gens int
	_ = pool.QueryRow(ctx, `SELECT client_secret FROM public.social_oauth_providers WHERE id=$1::uuid`, id).Scan(&plain)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.social_provider_secret_generations WHERE provider_id=$1::uuid AND superseded_at IS NULL`, id).Scan(&gens)
	if plain != nil || gens != 1 {
		t.Fatalf("plaintext not migrated: column=%v current generations=%d", plain, gens)
	}
	p, err := s.currentSocial().reg.Get("google")
	if err != nil || p.(*social.Google).ClientSecret != "plain-secret-1" {
		t.Fatalf("provider not built from the sealed secret: %v", err)
	}
	fp := s.currentSocial().fp

	// 3. An unchanged configuration is not rebuilt; a changed one is, within one reload.
	before := s.currentSocial()
	_ = s.reloadSocial(ctx, false)
	if s.currentSocial() != before {
		t.Fatal("an unchanged configuration was rebuilt")
	}

	// 4. edged's write-only secret: a new generation supersedes the old, and the registry follows.
	body, _ := json.Marshal(map[string]string{"secret": "rotated-secret-2"})
	r := httptest.NewRequest(http.MethodPut, "/v1/social/providers/"+id+"/secret", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.socialSecretStore(w, r)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"generation":2`)) {
		t.Fatalf("store: %d %s", w.Code, w.Body.String())
	}
	if p, _ := s.currentSocial().reg.Get("google"); p.(*social.Google).ClientSecret != "rotated-secret-2" {
		t.Fatal("the registry was not reloaded with the new secret")
	}
	if s.currentSocial().fp == fp {
		t.Fatal("the fingerprint did not change with the generation")
	}
	var superseded int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.social_provider_secret_generations WHERE provider_id=$1::uuid AND superseded_at IS NOT NULL`, id).Scan(&superseded)
	if superseded != 1 {
		t.Fatalf("previous generation not superseded: %d", superseded)
	}
	// Another server instance with another key cannot open it (the key id differs): nothing is built.
	other := newServer(true)
	_ = other.reloadSocial(ctx, true)
	if _, err := other.currentSocial().reg.Get("google"); err == nil {
		t.Fatal("a secret sealed under one key was opened under another")
	}

	// 5. A plaintext written AFTER sealing (an older edged) becomes the next generation on the next pass.
	if _, err := pool.Exec(ctx, `UPDATE public.social_oauth_providers SET client_secret='late-plain-3', updated_at=now() WHERE id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	_ = s.reloadSocial(ctx, false)
	if p, _ := s.currentSocial().reg.Get("google"); p.(*social.Google).ClientSecret != "late-plain-3" {
		t.Fatal("a plaintext written after sealing was not sealed and adopted")
	}

	// 6. THE ATTEMPT: canonical redirect URI, PKCE challenge and nonce in the authorize URL; verifier, nonce and
	// redirect URI stored on the state row.
	s.lic = licenceWithModules(t, lic.ModuleSocialLogin)
	s.methodSwitches = func(context.Context) (*tenantcfg.AuthMethods, error) {
		return &tenantcfg.AuthMethods{Social: map[string]*tenantcfg.AuthMethod{"google": {Enabled: true}}}, nil
	}
	sb, _ := json.Marshal(map[string]string{"provider": "google", "ip": "10.77.0.42", "mac": "aa:bb:cc:01:02:03",
		"redirect_uri": "http://192.0.2.1:8380/auth/social/callback"})
	w = httptest.NewRecorder()
	s.socialStart(w, httptest.NewRequest(http.MethodPost, "/v1/auth/social/start", bytes.NewReader(sb)))
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var out socialStartResp
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	u, _ := url.Parse(out.AuthorizeURL)
	q := u.Query()
	var stRedirect, stVerifier, stNonce string
	if err := pool.QueryRow(ctx, `SELECT redirect_uri, code_verifier, nonce FROM public.social_oauth_states WHERE state=$1`, out.State).
		Scan(&stRedirect, &stVerifier, &stNonce); err != nil {
		t.Fatal(err)
	}
	const want = "https://wifi.hotel.example/auth/social/callback"
	if stRedirect != want || q.Get("redirect_uri") != want {
		t.Errorf("redirect uri: stored %q, sent %q", stRedirect, q.Get("redirect_uri"))
	}
	if stVerifier == "" || q.Get("code_challenge") != social.PKCEChallengeS256(stVerifier) || q.Get("code_challenge_method") != "S256" {
		t.Errorf("PKCE: verifier %q, challenge %q", stVerifier, q.Get("code_challenge"))
	}
	if stNonce == "" || q.Get("nonce") != stNonce {
		t.Errorf("nonce: stored %q, sent %q", stNonce, q.Get("nonce"))
	}
	if bytes.Contains(w.Body.Bytes(), []byte(stVerifier)) {
		t.Error("the PKCE verifier left scd in the start response")
	}
}
