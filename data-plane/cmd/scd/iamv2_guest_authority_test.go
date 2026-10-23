package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// When the flag is on the legacy pipeline must NOT be reached. Checkable without a database: reaching legacy touches s.vou/s.sess, both nil here.

func enabledCfg(m iamv2.Method) iamv2.Config {
	return iamv2.Config{MasterEnabled: true, Methods: map[iamv2.Method]bool{m: true}}
}

// A server with NO legacy dependencies. Any fall-through to legacy dereferences a nil and fails loudly.
func authoritySrv(t *testing.T, cfg iamv2.Config) *server {
	t.Helper()
	// A non-required licence manager lets unlicensed DEV mode reach the authority switch; the licence gate runs BEFORE the switch by design.
	return &server{
		lic:      licstate.New(nil, "11111111-1111-4111-8111-111111111111", "", "", false),
		iamv2Cfg: cfg,
		tenID:    "11111111-1111-4111-8111-111111111111",
		siteID:   "22222222-2222-4222-8222-222222222222",
		methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) {
			on := &tenantcfg.AuthMethod{Enabled: true}
			return &tenantcfg.AuthMethods{Voucher: on, GuestAccount: on}, nil
		},
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
	// Must refuse, never fall through and succeed: with no resolvable guest network IAM-v2 cannot place the device.
	if rec.Code == http.StatusOK {
		t.Fatalf("enabled VOUCHER returned 200 without IAM-v2 state; body=%s", rec.Body.String())
	}
	assertIAMv2Authority(t, rec)
}

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

// Every refusal on an enabled method names iam_v2 as the authority, so an operator can tell who refused.
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

// A disabled method has no legacy pipeline to reach; the nil legacy dependencies would panic if one survived.
func TestDisabledMethodNoLongerReachesAnyLegacyPath(t *testing.T) {
	s := authoritySrv(t, iamv2.Config{MasterEnabled: false, Methods: map[iamv2.Method]bool{}})
	panicked := func() (p bool) {
		defer func() { p = recover() != nil }()
		rec := postJSON(t, s.authorize, map[string]string{
			"ip": "10.77.0.51", "mac": "02:de:00:00:00:01", "voucher": "ANYCODE",
		})
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("response was not JSON: %s", rec.Body.String())
		}
		if body["authority"] != "iam_v2" {
			t.Fatalf("a disabled method must still be answered by iam_v2, not by a surviving legacy "+
				"path: %v", body)
		}
		return false
	}()
	if panicked {
		t.Fatal("a legacy pipeline was still reachable: the entry point must serve IAM-v2 unconditionally")
	}
}

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
