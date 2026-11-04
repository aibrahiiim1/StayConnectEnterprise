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

// callWithSession runs a handler directly, as the router would after requireAuth: the operator's session is in the context.
func callWithSession(f *apiFixture, h http.HandlerFunc, method, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/"+id, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	if id != "" {
		rctx.URLParams.Add("id", id)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, sessKey, &session{OperatorID: f.operator, Email: "op@test.local", Roles: []string{"site_admin"}})
	rec := httptest.NewRecorder()
	h(rec, req.WithContext(ctx))
	return rec
}

// Audit P9 M2 / P9 L16: removing a sender, social sign-in and the client device self-service switch change only on the operator's password.
// Adding, changing or switching a notification sender is confirmed in the console without it (Product Owner, 2026-11-04), and audited.
func TestIntegration_ProviderAndDeviceSettingChangesNeedThePassword(t *testing.T) {
	f := newAPI(t, "site_admin")
	_, client := newFakeSCD(t, f)
	f.app.scd = client
	ctx := context.Background()
	var id string
	if err := f.pool.QueryRow(ctx, `INSERT INTO notification_providers (tenant_id, channel, kind, enabled, display_name)
	     VALUES ($1, 'email', 'stub', false, 'Before') RETURNING id::text`, f.tenant).Scan(&id); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	var sid string
	if err := f.pool.QueryRow(ctx, `INSERT INTO social_oauth_providers (tenant_id, provider, enabled, client_id, display_name)
	     VALUES ($1, 'google', false, 'cid', 'G') RETURNING id::text`, f.tenant).Scan(&sid); err != nil {
		t.Skipf("cannot seed a social provider on this schema: %v", err)
	}

	type call struct {
		name string
		h    http.HandlerFunc
		meth string
		id   string
		body string
	}
	for _, c := range []call{
		{"remove sender", f.app.deleteNotifyProvider, http.MethodDelete, id, ``},
		{"create social", f.app.createSocialProvider, http.MethodPost, "", `{"provider":"google","client_id":"c","client_secret":"s"}`},
		{"change social", f.app.patchSocialProvider, http.MethodPatch, sid, `{"display_name":"After"}`},
		{"remove social", f.app.deleteSocialProvider, http.MethodDelete, sid, ``},
		{"device switch", f.app.setGuestDeviceSetting, http.MethodPut, "", `{"enabled":true}`},
	} {
		if rec := callWithSession(f, c.h, c.meth, c.id, c.body); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "reauth_required") {
			t.Errorf("%s without a password = %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	// A wrong password changes nothing either.
	if rec := callWithSession(f, f.app.patchNotifyProvider, http.MethodPatch, id, `{"display_name":"After","password":"not-it"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong password = %d %s", rec.Code, rec.Body.String())
	}
	var name string
	if err := f.pool.QueryRow(ctx, `SELECT display_name FROM notification_providers WHERE id=$1::uuid`, id).Scan(&name); err != nil || name != "Before" {
		t.Fatalf("the sender changed on a wrong password: %q (%v)", name, err)
	}
	// Adding, changing and switching a sender need no password, and each is audited.
	if rec := callWithSession(f, f.app.createNotifyProvider, http.MethodPost, "", `{"channel":"email","kind":"stub"}`); rec.Code != http.StatusCreated {
		t.Errorf("adding a sender without a password = %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{"display_name":"After"}`, `{"enabled":false}`} {
		if rec := callWithSession(f, f.app.patchNotifyProvider, http.MethodPatch, id, body); rec.Code != http.StatusOK {
			t.Errorf("changing a sender without a password (%s) = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	var audited int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action LIKE 'notification_provider.%'`).Scan(&audited); err != nil || audited < 3 {
		t.Errorf("sender changes audited = %d (%v), want at least 3", audited, err)
	}
	if rec := callWithSession(f, f.app.deleteNotifyProvider, http.MethodDelete, id, `{"password":"`+f.password+`"}`); rec.Code != http.StatusNoContent {
		t.Errorf("a confirmed removal = %d %s", rec.Code, rec.Body.String())
	}
	if rec := callWithSession(f, f.app.deleteSocialProvider, http.MethodDelete, sid, `{"password":"`+f.password+`"}`); rec.Code != http.StatusNoContent {
		t.Errorf("a confirmed social removal = %d %s", rec.Code, rec.Body.String())
	}
	// The password is never audited.
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE payload::text LIKE '%'||$1||'%'`, f.password).Scan(&n); err != nil || n != 0 {
		t.Errorf("the password reached the audit log: %d (%v)", n, err)
	}
}
