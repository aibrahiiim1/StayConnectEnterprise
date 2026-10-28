//go:build integration

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// P9 L15: when scd refuses a new secret, the provider's other settings are not changed either, and the failure is
// audited (it used to commit the row, answer 502 and audit nothing).
func TestIntegration_NotifyProviderSecretRefusalChangesNothingAndIsAudited(t *testing.T) {
	f := newAPI(t)
	_, client := newFakeSCD(t, f) // answers 404 to the secret call: a refusal
	f.app.scd = client
	ctx := context.Background()

	var id string
	if err := f.pool.QueryRow(ctx, `INSERT INTO notification_providers (tenant_id, channel, kind, enabled, display_name)
	     VALUES ($1, 'sms', 'stub', false, 'Before') RETURNING id::text`, f.tenant).Scan(&id); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM notification_providers WHERE id=$1::uuid`, id)
	})

	req := httptest.NewRequest(http.MethodPatch, "/"+id, strings.NewReader(`{"display_name":"After","api_key":"new-secret"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	f.app.patchNotifyProvider(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d %s, want 502", rec.Code, rec.Body.String())
	}
	var name string
	if err := f.pool.QueryRow(ctx, `SELECT display_name FROM notification_providers WHERE id=$1::uuid`, id).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Before" {
		t.Fatalf("display_name = %q: a refused secret must leave the provider unchanged", name)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='notification_provider.update_failed'
	     AND target_id=$1 AND payload->>'stage'='secret' AND payload::text NOT LIKE '%new-secret%'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the refused update must be audited once without the secret, found %d", n)
	}
}
