package main

// With Phase-2 commerce dark, a successful voucher or guest-account sign-in must not redirect to the unmounted /packages nor issue a commerce cookie: tryIAMv2Auth's "unavailable" branch needs commerceSessions == nil, so newHandler must leave it nil when the surface is off.
// Both the OFF and ON states are pinned.

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

	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("the guest was redirected to %q while commerce is dark; that page cannot serve them", loc)
	}
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
	// This must go through newHandler: fixtures construct the field themselves, so a test over them cannot fail on the real defect. The constructor reads the flags from the environment.
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
	// A cookie minted while the surface was on must not be honoured once it is off.
	h := portalFixtureCommerceOff()
	r := httptest.NewRequest(http.MethodPost, "/packages/acquire", nil)
	r.AddCookie(&http.Cookie{Name: commerceCookie, Value: "any-value-at-all"})

	if _, ok := h.resolveCommerceSession(r); ok {
		t.Error("a commerce session resolved while the surface is dark")
	}
}
