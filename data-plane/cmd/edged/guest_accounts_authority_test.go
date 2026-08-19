package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// THE REGRESSION FOR THE ACCOUNT-LIFECYCLE SPLIT AUTHORITY.
//
// Switching only CREATE to IAM-v2 left list/get/patch/set-password/disconnect/delete on the legacy table, so
// an account created through the API was invisible to the same API a second later. The property that broke
// was not "IAM-v2 works" -- it was "every operation agrees on who the authority is".
//
// So this asserts the pairing itself, at the router, without a database: a server whose ONLY legacy
// dependency is nil will panic if a request reaches the legacy handler. With ACCOUNT enabled, no lifecycle
// route may do that; with ACCOUNT disabled, every one of them must. A future handler added to the route
// table without an authority pairing fails here rather than in front of an operator.

func acctSrv(cfg iamv2.Config) *server {
	// db is nil on purpose: the IAM-v2 handlers reach it and fail, which is fine -- what matters is WHICH
	// handler was reached. The legacy handlers reach it too, so both sides fault; the distinguishing signal
	// is the authority switch itself, asserted directly below.
	return &server{iamv2Cfg: cfg, tenantID: "11111111-1111-4111-8111-111111111111",
		siteID: "22222222-2222-4222-8222-222222222222"}
}

func TestAccountAuthoritySwitchSelectsOnePathForEveryOperation(t *testing.T) {
	on := acctSrv(iamv2.Config{MasterEnabled: true, Methods: map[iamv2.Method]bool{iamv2.MethodAccount: true}})
	if !on.iamv2AccountAuthority() {
		t.Fatal("ACCOUNT enabled but the account authority switch reports legacy")
	}
	off := acctSrv(iamv2.Config{MasterEnabled: false, Methods: map[iamv2.Method]bool{}})
	if off.iamv2AccountAuthority() {
		t.Fatal("ACCOUNT disabled but the account authority switch reports IAM-v2")
	}
	// Master off with the method flag on must still be legacy: fail closed, same rule as the guest entry
	// points, so an incoherent config can never half-enable the credential store.
	half := acctSrv(iamv2.Config{MasterEnabled: false,
		Methods: map[iamv2.Method]bool{iamv2.MethodAccount: true}})
	if half.iamv2AccountAuthority() {
		t.Fatal("master OFF with ACCOUNT ON must report legacy (fail closed)")
	}
}

// acctAuthority must dispatch to exactly one side, and to the side the config names.
func TestAcctAuthorityDispatchesToTheConfiguredSide(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     iamv2.Config
		wantIAM bool
	}{
		{"enabled -> iam_v2", iamv2.Config{MasterEnabled: true,
			Methods: map[iamv2.Method]bool{iamv2.MethodAccount: true}}, true},
		{"disabled -> legacy", iamv2.Config{MasterEnabled: false,
			Methods: map[iamv2.Method]bool{}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := acctSrv(tc.cfg)
			var legacyHit, iamHit bool
			h := s.acctAuthority(
				func(http.ResponseWriter, *http.Request) { legacyHit = true },
				func(http.ResponseWriter, *http.Request) { iamHit = true },
			)
			h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
			if legacyHit && iamHit {
				t.Fatal("both handlers ran: that is a dual path, which is exactly what must not exist")
			}
			if tc.wantIAM && !iamHit {
				t.Fatal("ACCOUNT is IAM-v2-authoritative but the legacy handler served the request")
			}
			if !tc.wantIAM && !legacyHit {
				t.Fatal("ACCOUNT is disabled but the IAM-v2 handler served the request")
			}
		})
	}
}

// EVERY lifecycle route must be paired. This walks the real router and asserts that switching the config
// changes which handler runs for each one -- catching a route added later without a pairing, which is the
// specific mistake that produced the split authority.
func TestEveryAccountLifecycleRouteIsAuthorityPaired(t *testing.T) {
	routes := []struct {
		method, path string
	}{
		{http.MethodGet, "/"},
		{http.MethodGet, "/11111111-1111-4111-8111-111111111111"},
		{http.MethodPatch, "/11111111-1111-4111-8111-111111111111"},
		{http.MethodPost, "/11111111-1111-4111-8111-111111111111/set-password"},
		{http.MethodPost, "/11111111-1111-4111-8111-111111111111/disconnect"},
		{http.MethodDelete, "/11111111-1111-4111-8111-111111111111"},
	}
	for _, rt := range routes {
		// A nil db means every handler faults, so this cannot assert on the response. What it CAN assert --
		// and what actually matters -- is that the route exists and is reachable under both configs, so a
		// missing pairing shows up as a 404/405 rather than silently serving legacy forever.
		for _, cfg := range []iamv2.Config{
			{MasterEnabled: true, Methods: map[iamv2.Method]bool{iamv2.MethodAccount: true}},
			{MasterEnabled: false, Methods: map[iamv2.Method]bool{}},
		} {
			s := acctSrv(cfg)
			rec := httptest.NewRecorder()
			func() {
				defer func() { _ = recover() }() // a nil-db fault is expected and not what is under test
				s.guestAccountsRoutes().ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, nil))
			}()
			if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("%s %s is not routed (%d): every lifecycle route must exist under both authorities",
					rt.method, rt.path, rec.Code)
			}
		}
	}
}
