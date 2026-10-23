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

// Renders the real landing page into the fixture the hotel-admin/e2e browser tests read; fails when stale. Regenerate with UPDATE_E2E_FIXTURE=1 go test ./cmd/portald/ -run TestE2EPortalFixtures.
// The nonce is a fixed placeholder; the page is rendered for an arriving device with no ARP entry and no published design.
var e2eFixtureDir = filepath.Join("..", "..", "..", "hotel-admin", "e2e", "fixtures")

var nonceAttr = regexp.MustCompile(`nonce="[^"]*"`)

func renderE2E(t *testing.T, path string, commerce bool) string {
	t.Helper()
	h := designHandler(t, map[string]any{})
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return nil, false }
	if commerce {
		// Phase 2 dark: the commerce panel renders only when the portal surface is on; the browser tests cover both pages.
		h.commerceCfg = iamv2.CommerceConfig{MasterEnabled: true, PortalEnabled: true}
	}
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = "192.0.2.10:51000"
	w := httptest.NewRecorder()
	h.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s answered %d", path, w.Code)
	}
	out := nonceAttr.ReplaceAllString(w.Body.String(), `nonce="e2e-nonce"`)
	return leaveAttr.ReplaceAllString(out, `name="leave" value="e2e-leave"`)
}

var leaveAttr = regexp.MustCompile(`name="leave" value="[^"]*"`)

func TestE2EPortalFixtures(t *testing.T) {
	// With the Phase-2 surface on or off the success page is the same page; the test below proves it.
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
