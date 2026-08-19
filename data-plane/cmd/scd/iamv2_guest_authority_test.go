package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
)

// THE REGRESSION FOR THE AUTHORITY DEFECT.
//
// The defect was not that IAM-v2 was broken -- it was that IAM-v2 being ENABLED changed nothing. The flags
// were read, the Authenticator was constructed, and /v1/sessions/authorize went on calling the legacy
// voucher pipeline and writing public.sessions while iam_v2 stayed at zero rows.
//
// So the property under test is deliberately NOT "IAM-v2 works". It is: when the flag is on, the legacy
// pipeline is NOT REACHED. That is what a future regression would break, and it is checkable without a
// database, because reaching legacy means touching s.vou / s.sess -- both nil in this fixture, which would
// panic. A test that needed a live DB would be skipped in CI and would have caught nothing, which is
// exactly how the original defect survived.

func enabledCfg(m iamv2.Method) iamv2.Config {
	return iamv2.Config{MasterEnabled: true, Methods: map[iamv2.Method]bool{m: true}}
}

// A server with NO legacy dependencies. Any fall-through to legacy dereferences a nil and fails loudly.
func authoritySrv(t *testing.T, cfg iamv2.Config) *server {
	t.Helper()
	// A non-required license manager: unlicensed DEV mode still allows new sessions, which is what lets
	// this test reach the authority switch. The license gate runs BEFORE the switch by design (an
	// unlicensed appliance must not authorize anyone regardless of which IAM owns the decision).
	return &server{
		lic:      licstate.New(nil, "11111111-1111-4111-8111-111111111111", "", "", false),
		iamv2Cfg: cfg,
		tenID:    "11111111-1111-4111-8111-111111111111",
		siteID:   "22222222-2222-4222-8222-222222222222",
		// vou, sess, db, nft, shp, met all left nil on purpose: the enabled path must not touch them.
	}
}

func postJSON(t *testing.T, h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	return rec
}

// With VOUCHER enabled, the voucher entry point must not reach the legacy voucher/session pipeline.
func TestVoucherEntryPointDoesNotFallBackToLegacyWhenIAMv2Enabled(t *testing.T) {
	s := authoritySrv(t, enabledCfg(iamv2.MethodVoucher))
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("the enabled VOUCHER path reached a legacy dependency (panic: %v) -- "+
				"IAM-v2 is the configured authority and legacy must not be consulted", p)
		}
	}()
	rec := postJSON(t, s.authorize, map[string]string{
		"ip": "10.77.0.51", "mac": "02:de:00:00:00:01", "voucher": "ANYCODE",
	})
	// It must refuse rather than serve: with no guest network resolvable (nil db) IAM-v2 cannot place the
	// device, and refusing is the required behaviour. What it must never do is fall through and succeed.
	if rec.Code == http.StatusOK {
		t.Fatalf("enabled VOUCHER returned 200 without IAM-v2 state; body=%s", rec.Body.String())
	}
	assertIAMv2Authority(t, rec)
}

// Same contract for ACCOUNT.
func TestAccountEntryPointDoesNotFallBackToLegacyWhenIAMv2Enabled(t *testing.T) {
	s := authoritySrv(t, enabledCfg(iamv2.MethodAccount))
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("the enabled ACCOUNT path reached a legacy dependency (panic: %v) -- "+
				"IAM-v2 is the configured authority and legacy must not be consulted", p)
		}
	}()
	rec := postJSON(t, s.authorizeGuestAccount, map[string]string{
		"ip": "10.77.0.52", "mac": "02:de:00:00:00:02", "username": "u", "password": "p",
	})
	if rec.Code == http.StatusOK {
		t.Fatalf("enabled ACCOUNT returned 200 without IAM-v2 state; body=%s", rec.Body.String())
	}
	assertIAMv2Authority(t, rec)
}

// Every refusal on an enabled method must name iam_v2 as the authority. Without this an operator reading a
// 403 cannot tell whether IAM-v2 refused or legacy did -- which is the ambiguity that hid the defect.
func assertIAMv2Authority(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response was not JSON: %s", rec.Body.String())
	}
	if body["authority"] != "iam_v2" {
		t.Fatalf("refusal did not identify iam_v2 as the authority: %v", body)
	}
}

// The other half of the contract: with the method DISABLED, the switch must not engage at all, so the
// legacy path is reached exactly as before. Proven by the nil legacy dependency panicking -- if the switch
// wrongly swallowed a disabled method, no panic would occur and this test would fail.
func TestDisabledMethodStillReachesLegacy(t *testing.T) {
	s := authoritySrv(t, iamv2.Config{MasterEnabled: false, Methods: map[iamv2.Method]bool{}})
	reached := func() (r bool) {
		defer func() { r = recover() != nil }()
		postJSON(t, s.authorize, map[string]string{
			"ip": "10.77.0.51", "mac": "02:de:00:00:00:01", "voucher": "ANYCODE",
		})
		return false
	}()
	if !reached {
		t.Fatal("with VOUCHER disabled the legacy pipeline must still be reached; the authority switch " +
			"must be inert when IAM-v2 is not the configured authority")
	}
}

// The switch must read the same config the Authenticator was gated on.
func TestAuthoritySwitchTracksMethodConfig(t *testing.T) {
	on := authoritySrv(t, enabledCfg(iamv2.MethodVoucher))
	if !on.iamv2MethodEnabled(iamv2.MethodVoucher) {
		t.Fatal("VOUCHER enabled in config but the switch reports disabled")
	}
	if on.iamv2MethodEnabled(iamv2.MethodAccount) {
		t.Fatal("ACCOUNT is not enabled but the switch reports enabled")
	}
	// Master off must disable every method even when a per-method flag is set: fail closed.
	off := authoritySrv(t, iamv2.Config{MasterEnabled: false,
		Methods: map[iamv2.Method]bool{iamv2.MethodVoucher: true}})
	if off.iamv2MethodEnabled(iamv2.MethodVoucher) {
		t.Fatal("master OFF with VOUCHER ON must report disabled (fail closed)")
	}
}
