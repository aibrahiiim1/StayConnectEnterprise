package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/social"
)

// exchangeProbe stands in for a provider: during Exchange it records whether scd still held a database connection
// and whether the state was already claimed, then fails.
type exchangeProbe struct {
	pool     *pgxpool.Pool
	state    string
	acquired int32
	claimed  bool
}

func (p *exchangeProbe) Name() string                           { return "google" }
func (p *exchangeProbe) Binding() social.Binding                { return social.Binding{} }
func (p *exchangeProbe) AuthorizeURL(social.AuthRequest) string { return "" }
func (p *exchangeProbe) Exchange(ctx context.Context, _ social.ExchangeRequest) (*social.UserInfo, error) {
	p.acquired = p.pool.Stat().AcquiredConns()
	_ = p.pool.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM social_oauth_states WHERE state=$1`, p.state).Scan(&p.claimed)
	return nil, errors.New("provider says no")
}

// D2-M1 / P4 L4: the provider exchange runs with no database connection held; the state is claimed while it runs and
// released when it fails, so a failed exchange does not use the state up.
func TestIntegration_SocialExchangeHoldsNoConnection(t *testing.T) {
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
	const tenant = "22222222-2222-2222-2222-222222222223"
	const state = "aud-social-state-1"
	if _, err := pool.Exec(ctx, `INSERT INTO public.tenants (id, slug, name) VALUES ($1,'t-social-x','t') ON CONFLICT (id) DO NOTHING`, tenant); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM public.social_oauth_states WHERE tenant_id=$1`, tenant)
	if _, err := pool.Exec(ctx, `INSERT INTO social_oauth_states (state, tenant_id, provider, client_ip, client_mac, redirect_uri, expires_at)
		VALUES ($1,$2,'google','10.9.9.9'::inet,'02:00:00:00:99:01'::macaddr,'https://wifi.hotel.example/cb',$3)`,
		state, tenant, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	probe := &exchangeProbe{pool: pool, state: state}
	s := &server{db: pool, tenID: tenant, met: metrics.New("test", prometheus.Labels{}),
		lic:        licstate.New(nil, "", t.TempDir(), filepath.Join(t.TempDir(), "absent.pub"), false),
		socialBase: []social.Provider{probe}}
	body, _ := json.Marshal(socialAuthorizeReq{State: state, Code: "c", IP: "10.9.9.9", MAC: "02:00:00:00:99:01"})
	rec := httptest.NewRecorder()
	s.authorizeSocial(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/authorize-social", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d %s, want 400 (exchange failed)", rec.Code, rec.Body.String())
	}
	if probe.acquired != 0 {
		t.Fatalf("%d database connection(s) held during the provider call", probe.acquired)
	}
	if !probe.claimed {
		t.Fatal("the state must be claimed while the provider is called (single use)")
	}
	var consumed bool
	if err := pool.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM social_oauth_states WHERE state=$1`, state).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed {
		t.Fatal("a failed exchange must release the state, as before")
	}
}
