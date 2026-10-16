package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// THE BROWSER TESTS IN hotel-admin/e2e NEED THE PAGE PORTALD ACTUALLY SERVES.
//
// They used to cut the landing template out of this package's Go source with a regular expression and
// substitute its template actions by hand. Once the page was assembled from shared pieces (head, chrome,
// scripts) and gained more actions, that imitation could no longer be kept honest. So the page is rendered
// here, by the real handler, and written to a fixture the browser tests read. This test fails when the
// fixture is stale; regenerate it with
//
//	UPDATE_E2E_FIXTURE=1 go test ./cmd/portald/ -run TestE2EPortalFixtures
//
// The nonce is replaced by a fixed placeholder (the e2e harness serves the page without a CSP header) and the
// page is rendered for an arriving device the appliance has no ARP entry for, with no published design.
var e2eFixtureDir = filepath.Join("..", "..", "..", "hotel-admin", "e2e", "fixtures")

var nonceAttr = regexp.MustCompile(`nonce="[^"]*"`)

func renderE2E(t *testing.T, path string, commerce bool) string {
	t.Helper()
	h := designHandler(t, map[string]any{})
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return nil, false }
	if commerce {
		// Phase 2 DARK: the guest commerce panel renders only when the portal surface is ON. The browser
		// tests exercise both the ON page (its panel and script) and the OFF page (no panel, no API call).
		h.commerceCfg = iamv2.CommerceConfig{MasterEnabled: true, PortalEnabled: true}
	}
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = "192.0.2.10:51000"
	w := httptest.NewRecorder()
	h.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s answered %d", path, w.Code)
	}
	// The nonce and the sign-out token change with every response / process; the fixture carries placeholders.
	out := nonceAttr.ReplaceAllString(w.Body.String(), `nonce="e2e-nonce"`)
	return leaveAttr.ReplaceAllString(out, `name="leave" value="e2e-leave"`)
}

var leaveAttr = regexp.MustCompile(`name="leave" value="[^"]*"`)

func TestE2EPortalFixtures(t *testing.T) {
	// One success page: with the Phase-2 surface on or off it is the same page, since the package panel it used
	// to carry is gone (audit P-16). The test below proves the two are identical.
	pages := map[string]string{
		"portal-landing.html": renderE2E(t, "/", false),
		"portal-success.html": renderE2E(t, "/success?s=sess-1", false),
	}
	if on := renderE2E(t, "/success?s=sess-1", true); on != pages["portal-success.html"] {
		t.Error("the success page differs with the Phase-2 surface on: it must offer nothing to buy")
	}
	update := os.Getenv("UPDATE_E2E_FIXTURE") != ""
	for name, got := range pages {
		path := filepath.Join(e2eFixtureDir, name)
		if update {
			if err := os.MkdirAll(e2eFixtureDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (regenerate with UPDATE_E2E_FIXTURE=1)", path, err)
		}
		if string(want) != got {
			t.Error(fmt.Sprintf("%s is stale: the portal page changed. Regenerate the fixtures with "+
				"UPDATE_E2E_FIXTURE=1 go test ./cmd/portald/ -run TestE2EPortalFixtures", path))
		}
	}
}
