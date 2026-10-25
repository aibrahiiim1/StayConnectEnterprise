//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

func (f *authFixture) setOverride(t *testing.T, network string, off []string, primary string, remember *int) {
	t.Helper()
	if off == nil {
		off = []string{}
	}
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO iam_v2.network_signin_overrides (network_id, tenant_id, site_id, disabled_methods, primary_method, remember_days)
		VALUES ($1::uuid,$2,$3,$4,NULLIF($5,''),$6)
		ON CONFLICT (network_id) DO UPDATE SET disabled_methods=EXCLUDED.disabled_methods, primary_method=EXCLUDED.primary_method, remember_days=EXCLUDED.remember_days`,
		network, f.tenant, f.site, off, primary, remember); err != nil {
		t.Fatal(err)
	}
}

func (f *authFixture) listed(t *testing.T, ip string) map[string]json.RawMessage {
	t.Helper()
	rec := httptest.NewRecorder()
	f.srv.tenantAuthMethods(rec, httptest.NewRequest(http.MethodGet, "/v1/tenant/auth-methods?"+url.Values{"ip": {ip}}.Encode(), nil))
	if rec.Code != 200 {
		t.Fatalf("auth-methods = %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A network can switch room sign-in off for itself, and only for itself.
func TestIntegration_NetworkSignin_RoomSignInSwitchedOffHereStillWorksOnTheOtherNetwork(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	second, secondID, _ := f.addNetwork(t, true)

	if res, _ := f.signInAt(t, f.net.guestIP, "00000c01-0000-4000-8000-000000000000"); res.Outcome != outcomeVerified {
		t.Fatalf("baseline sign-in: %+v", res)
	}
	f.setOverride(t, f.network, []string{"pms"}, "", nil)
	if res, _ := f.signInAt(t, f.net.guestIP, "00000c02-0000-4000-8000-000000000000"); res.Outcome == outcomeVerified {
		t.Fatal("room sign-in was accepted on a network that switched it off")
	}
	if res, _ := f.signInAt(t, second.guestIP, "00000c03-0000-4000-8000-000000000000"); res.Outcome != outcomeVerified {
		t.Fatalf("another network lost room sign-in: %+v", res)
	}
	_ = secondID
}

func TestIntegration_NetworkSignin_TheListingNarrowsButNeverWidens(t *testing.T) {
	f := newAuthFixture(t)
	if _, ok := f.listed(t, f.net.guestIP)["voucher"]; !ok {
		t.Fatal("the site's voucher method is not listed at all")
	}
	f.setOverride(t, f.network, []string{"voucher"}, "", nil)
	listing := f.listed(t, f.net.guestIP)
	if _, ok := listing["voucher"]; ok {
		t.Fatalf("a method switched off on this network is still listed: %v", listing)
	}
	// An address on no network, or none supplied, gets the site's list; a method the site never offered stays absent whatever the network says.
	if _, ok := f.listed(t, "203.0.113.9")["voucher"]; !ok {
		t.Fatal("an address on no network lost the site's methods")
	}
	f.setOverride(t, f.network, nil, "sms", nil)
	if _, ok := f.listed(t, f.net.guestIP)["sms"]; ok {
		t.Fatal("a network override switched on a method the site does not offer")
	}
}

func TestIntegration_NetworkSignin_RememberDevicesNeverGrowsBeyondTheSite(t *testing.T) {
	f := newAuthFixture(t)
	site := f.srv.rememberDeviceDays(context.Background())
	shorter, longer := 3, site+100
	ctx := withNetwork(context.Background(), f.network)
	f.setOverride(t, f.network, nil, "", &shorter)
	if got := f.srv.rememberDeviceDays(ctx); got != shorter {
		t.Fatalf("a shorter period was not applied: %d", got)
	}
	f.setOverride(t, f.network, nil, "", &longer)
	if got := f.srv.rememberDeviceDays(ctx); got != site {
		t.Fatalf("a longer period widened the site's: %d vs %d", got, site)
	}
}

// An override that cannot be read refuses the method rather than offering everything.
func TestIntegration_NetworkSignin_AnUnreadableOverrideRefuses(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	if !f.srv.networkOffers(ctx, f.network, keyVoucher, keyPMS) {
		t.Fatal("a network without an override must offer the site's methods")
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE iam_v2.network_signin_overrides RENAME TO network_signin_overrides_off`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `ALTER TABLE iam_v2.network_signin_overrides_off RENAME TO network_signin_overrides`)
	})
	if f.srv.networkOffers(ctx, f.network, keyVoucher) {
		t.Fatal("an unreadable override offered the method")
	}
}

func TestIntegration_NetworkSignin_TheAuthorityRefusesAMethodTheNetworkSwitchedOff(t *testing.T) {
	f := newAuthFixture(t)
	f.setOverride(t, f.network, []string{"voucher", "guest_account"}, "", nil)
	ip := net.ParseIP(f.net.guestIP)
	try := func(method iamv2.Method, key string) (int, string) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		if key != "" {
			r = withMethodKey(r, key)
		}
		f.srv.authorizeViaIAMv2(rec, r, method, iamv2.Request{}, ip)
		return rec.Code, rec.Body.String()
	}
	if code, body := try(iamv2.MethodVoucher, ""); code != 403 || !strings.Contains(body, "METHOD_NOT_OFFERED_ON_NETWORK") {
		t.Fatalf("voucher on a network that switched it off = %d %s", code, body)
	}
	if code, body := try(iamv2.MethodAccount, keyGuestAccount); code != 403 || !strings.Contains(body, "METHOD_NOT_OFFERED_ON_NETWORK") {
		t.Fatalf("staff-account sign-in = %d %s", code, body)
	}
	if !f.srv.networkOffers(context.Background(), f.network, keyEmailAccount) {
		t.Fatal("a method the network still offers was refused")
	}
}
