//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func (f *authFixture) designFor(t *testing.T, query url.Values) string {
	t.Helper()
	rec := httptest.NewRecorder()
	f.srv.tenantBranding(rec, httptest.NewRequest(http.MethodGet, "/v1/tenant/branding?"+query.Encode(), nil))
	var out struct {
		Design map[string]any `json:"design"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%q: %v", rec.Body.String(), err)
	}
	title, _ := out.Design["welcome_title"].(string)
	return title
}

func TestIntegration_PortalDesign_FollowsTheNetworkTheClientIsOn(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE public.tenants SET branding = '{"design":{"welcome_title":"Site default"}}'::jsonb WHERE id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	var profile string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.portal_profiles (tenant_id, site_id, name, doc) VALUES ($1,$2,'Pool','{"design":{"welcome_title":"Pool bar"}}'::jsonb) RETURNING id::text`,
		f.tenant, f.site).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM iam_v2.portal_profiles WHERE id=$1::uuid`, profile) })

	if got := f.designFor(t, url.Values{"ip": {f.net.guestIP}}); got != "Site default" {
		t.Fatalf("a network with no profile did not get the site default: %q", got)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.portal_profile_assignments (network_id, profile_id, tenant_id, site_id) VALUES ($1::uuid,$2::uuid,$3,$4)`,
		f.network, profile, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	if got := f.designFor(t, url.Values{"ip": {f.net.guestIP}}); got != "Pool bar" {
		t.Fatalf("a client on the assigned network did not get the profile: %q", got)
	}

	_, otherID, _ := f.addNetwork(t, false)
	var otherIP string
	if err := f.pool.QueryRow(ctx, `SELECT host(gateway_ip + 5) FROM public.guest_networks WHERE id=$1::uuid`, otherID).Scan(&otherIP); err != nil {
		t.Fatal(err)
	}
	if got := f.designFor(t, url.Values{"ip": {otherIP}}); got != "Site default" {
		t.Fatalf("a client on another network got the first network's profile: %q", got)
	}
	for _, ip := range []string{"", "not-an-ip", "203.0.113.9"} {
		if got := f.designFor(t, url.Values{"ip": {ip}}); got != "Site default" {
			t.Fatalf("address %q did not fall back to the site default: %q", ip, got)
		}
	}
	if got := f.designFor(t, url.Values{"profile": {profile}}); got != "Pool bar" {
		t.Fatalf("an explicit preview profile was not honoured: %q", got)
	}
	if got := f.designFor(t, url.Values{"profile": {"not-a-uuid"}}); got != "" {
		t.Fatalf("a malformed preview profile was served a design: %q", got)
	}
}
