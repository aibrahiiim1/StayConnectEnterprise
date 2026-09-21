package main

// WHAT A GUEST MET AT THE END OF A SUCCESSFUL SIGN-IN.
//
// Found by running Guest Internet as a controlled pilot on PRE-LIVE. A voucher issued through the real
// issuance path authenticated correctly -- scd returned 200 -- and the guest was then redirected to
// /packages, where scd answered 404 in fifteen microseconds. That 404 is correct: Phase-2 commerce is dark
// on this appliance (master=false portal=false admin=false), so those routes are deliberately not mounted.
//
// The defect was on this side. tryIAMv2Auth already contained the right answer for exactly this case --
// "Internet packages are not available right now. Please ask reception." -- behind a check for
// `commerceSessions == nil`. But newHandler built that store unconditionally, so the field was never nil,
// the branch was unreachable, and its own comment ("the store is only constructed when the Phase-2 portal
// surface is on") described something the code did not do. The guest was issued a commerce cookie, sent to
// an empty page, and told to choose another package -- advice they could not act on.
//
// The scope is narrow and worth stating precisely: tryIAMv2Auth is reached only from the voucher and
// guest-account handlers. Room/PMS sign-in does not pass through it, which is why every session this
// appliance has ever recorded came from a stay.
//
// These tests pin the behaviour in both worlds, because a fix that made the OFF state honest by breaking
// the ON state would be no fix at all.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

func TestWithCommerceDarkTheGuestIsToldTheTruthInsteadOfBeingSentNowhere(t *testing.T) {
	h := portalFixtureCommerceOff()
	rec := httptest.NewRecorder()

	handled := h.tryIAMv2Auth(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), iamv2Payload())
	if !handled {
		t.Fatal("an IAM-v2 reply must still be handled here; falling through would run the legacy path on a " +
			"reply that carries no session and tell the guest they are online")
	}

	// The whole point: no redirect into a surface that does not exist.
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("the guest was redirected to %q while commerce is dark; that page cannot serve them", loc)
	}
	// ...and no cookie, because there is no session store to put anything in.
	for _, c := range rec.Result().Cookies() {
		if c.Name == commerceCookie {
			t.Error("a commerce cookie was issued although there is no commerce surface to use it")
		}
	}

	body := rec.Body.String()
	if !strings.Contains(body, "not available") {
		t.Errorf("the guest was not told packages are unavailable; they got: %q", strings.TrimSpace(body))
	}
	if strings.Contains(strings.ToLower(body), "choose another") {
		t.Error("the guest is being asked to choose another package; there are none to choose from")
	}
}

func TestTheStoreExistsExactlyWhenTheSurfaceDoes(t *testing.T) {
	// THIS MUST GO THROUGH newHandler, and the first version of it did not.
	//
	// It originally asserted over the two fixtures in this package -- which construct the field themselves --
	// so it restated how the fixtures were written and proved nothing about the binary. Reintroducing the
	// defect in newHandler left it passing. A test that cannot fail on the broken code is not protection,
	// and the whole defect being fixed here is a guard that could not fire.
	//
	// The real constructor reads the flags from the environment, so the environment is what this drives.
	cases := []struct {
		name           string
		master, portal string
		wantStore      bool
	}{
		{"dark, as the appliance actually ships", "", "", false},
		{"master on but portal off", "true", "false", false},
		{"portal surface genuinely on", "true", "true", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(iamv2.EnvPhase2Master, tc.master)
			t.Setenv(iamv2.EnvPhase2Portal, tc.portal)
			h, err := newHandler(cfgVal())
			if err != nil {
				t.Fatalf("newHandler: %v", err)
			}
			switch {
			case tc.wantStore && h.commerceSessions == nil:
				t.Error("the portal commerce surface is on yet there is no session store; an authenticated " +
					"guest could not proceed")
			case !tc.wantStore && h.commerceSessions != nil:
				t.Error("commerce is off yet a session store exists; every `commerceSessions == nil` guard in " +
					"this package is dead code, including the one that tells the guest to ask reception")
			}
		})
	}
}

func TestADarkPortalNeverResolvesACommerceSessionEvenWithACookie(t *testing.T) {
	// A cookie minted while the surface was on -- or by anyone at all -- must not be honoured once it is off.
	h := portalFixtureCommerceOff()
	r := httptest.NewRequest(http.MethodPost, "/packages/acquire", nil)
	r.AddCookie(&http.Cookie{Name: commerceCookie, Value: "any-value-at-all"})

	if _, ok := h.resolveCommerceSession(r); ok {
		t.Error("a commerce session resolved while the surface is dark")
	}
}
