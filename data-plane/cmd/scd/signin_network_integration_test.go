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
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
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

func (f *authFixture) post(t *testing.T, h http.HandlerFunc, body map[string]any) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw))))
	return rec.Code, rec.Body.String()
}

// Every route that starts or finishes a sign-in asks the same question about the network the connection comes from.
func TestIntegration_NetworkSignin_EveryEntryPointHonoursTheNetworkItIsReachedFrom(t *testing.T) {
	f := newAuthFixture(t)
	other, otherID, _ := f.addNetwork(t, false)
	f.setOverride(t, f.network, []string{"sms", "social", "email_account"}, "", nil)
	notOffered := func(code int, body string) bool {
		return code == 403 && strings.Contains(body, "METHOD_NOT_OFFERED_ON_NETWORK")
	}

	// Text-message code issue.
	issue := map[string]any{"channel": "sms", "destination": "+201001234567", "ip": f.net.guestIP, "mac": f.net.mac}
	if c, b := f.post(t, f.srv.otpIssue, issue); !notOffered(c, b) {
		t.Fatalf("code issue on a network that switched text codes off = %d %s", c, b)
	}
	issue["ip"] = other.guestIP
	if c, b := f.post(t, f.srv.otpIssue, issue); notOffered(c, b) {
		t.Fatalf("code issue on another network was refused by the first network's setting: %s", b)
	}

	// Social sign-in start.
	f.srv.methodSwitches = func(context.Context) (*tenantcfg.AuthMethods, error) {
		return &tenantcfg.AuthMethods{Social: map[string]*tenantcfg.AuthMethod{"google": {Enabled: true}}}, nil
	}
	start := map[string]any{"provider": "google", "ip": f.net.guestIP, "mac": f.net.mac}
	if c, b := f.post(t, f.srv.socialStart, start); !notOffered(c, b) {
		t.Fatalf("social start on a network that switched it off = %d %s", c, b)
	}
	start["ip"] = other.guestIP
	if c, b := f.post(t, f.srv.socialStart, start); notOffered(c, b) {
		t.Fatalf("social start on another network was refused by the first network's setting: %s", b)
	}

	// The authority, for each method and route key, from the first network and from the other.
	ip, otherIP := net.ParseIP(f.net.guestIP), net.ParseIP(other.guestIP)
	try := func(ip net.IP, m iamv2.Method, key string) bool {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		if key != "" {
			r = withMethodKey(r, key)
		}
		func() {
			defer func() { _ = recover() }() // a method that is offered goes on to the authenticator, which this fixture does not carry
			f.srv.authorizeViaIAMv2(rec, r, m, iamv2.Request{}, ip)
		}()
		return notOffered(rec.Code, rec.Body.String())
	}
	for _, c := range []struct {
		name   string
		m      iamv2.Method
		key    string
		refuse bool
	}{
		{"social callback", iamv2.MethodSocial, "", true},
		{"email account sign-in", iamv2.MethodAccount, keyEmailAccount, true},
		{"text code verify", iamv2.MethodOTP, keySMS, true},
		{"whatsapp code verify", iamv2.MethodOTP, keyWhatsApp, false},
		{"voucher", iamv2.MethodVoucher, "", false},
		{"staff account", iamv2.MethodAccount, keyGuestAccount, false},
	} {
		if got := try(ip, c.m, c.key); got != c.refuse {
			t.Errorf("%s on the narrowed network: refused=%v, want %v", c.name, got, c.refuse)
		}
		if try(otherIP, c.m, c.key) {
			t.Errorf("%s on the other network was refused by the first network's setting", c.name)
		}
	}
	_ = otherID
}

// The shared account route judges the network setting by the kind of account that signed in, and answers a refusal as a wrong password.
func TestIntegration_NetworkSignin_TheAccountRouteUsesTheKindOfTheAccount(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.srv.methodSwitches = func(context.Context) (*tenantcfg.AuthMethods, error) {
		return &tenantcfg.AuthMethods{GuestAccount: &tenantcfg.AuthMethod{Enabled: true}, EmailAccount: &tenantcfg.AuthMethod{Enabled: true}}, nil
	}
	mk := func(name string, self bool) {
		if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash, self_registered) VALUES ($1,$2,$3,'x',$4)`,
			f.tenant, f.site, name, self); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	mk("front.desk", false)
	mk("guest@example.com", true)

	try := func(user string) (reachedAuthority bool, body string) {
		raw, _ := json.Marshal(map[string]any{"username": user, "password": "wrong", "ip": f.net.guestIP, "mac": f.net.mac})
		rec := httptest.NewRecorder()
		func() {
			defer func() {
				if recover() != nil {
					reachedAuthority = true // past every network check, into the authenticator this fixture does not carry
				}
			}()
			f.srv.authorizeGuestAccount(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw))))
		}()
		return reachedAuthority, rec.Body.String()
	}
	denied := func(reached bool, body string) bool { return !reached && strings.Contains(body, "invalid_credentials") }

	f.setOverride(t, f.network, []string{"guest_account"}, "", nil)
	if r, b := try("front.desk"); !denied(r, b) {
		t.Fatalf("a staff account on a network that switched staff accounts off: reached=%v %s", r, b)
	}
	if r, b := try("guest@example.com"); r == false && denied(r, b) {
		t.Fatalf("an email account was refused because only staff accounts were switched off: %s", b)
	}
	f.setOverride(t, f.network, []string{"email_account"}, "", nil)
	if r, b := try("guest@example.com"); !denied(r, b) {
		t.Fatalf("an email account on a network that switched email accounts off: reached=%v %s", r, b)
	}
	if r, b := try("front.desk"); r == false && denied(r, b) {
		t.Fatalf("a staff account was refused because only email accounts were switched off: %s", b)
	}
}
