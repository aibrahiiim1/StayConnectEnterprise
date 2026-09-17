package main

// THE GUEST PORTAL, HELD TO THE REFERENCE DESIGN.
//
// These assert the structure the Product Owner's screenshots specify -- a branded card, two sign-in groups,
// a language selector and an information affordance carrying the device's own addresses -- and the security
// property that makes the last of those safe to show.
//
// They are structural, not cosmetic: nothing here checks a colour. What they protect is that the elements
// the design depends on exist, are reachable, and carry the values they claim to.

import (
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// renderLanding runs the real template through the real landing handler, with a stub ARP lookup, and returns
// the HTML a guest would receive.
func renderLanding(t *testing.T, ip string, mac string) string {
	t.Helper()
	tmpl, err := template.New("land").Parse(landingHTML)
	if err != nil {
		t.Fatalf("the landing template does not parse: %v", err)
	}
	h := &handler{tmplLand: tmpl}
	if mac != "" {
		hw, err := net.ParseMAC(mac)
		if err != nil {
			t.Fatal(err)
		}
		h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return hw, true }
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if ip != "" {
		r.RemoteAddr = net.JoinHostPort(ip, "51000")
	}
	w := httptest.NewRecorder()
	h.landing(w, r, "")
	return w.Body.String()
}

func TestLandingCarriesTheReferenceStructure(t *testing.T) {
	html := renderLanding(t, "10.77.0.42", "aa:bb:cc:dd:ee:ff")

	for _, want := range []struct{ frag, why string }{
		{`class="card"`, "the centred login card"},
		{`id="brand-logo"`, "the hotel logo slot"},
		{`id="brand-name"`, "the hotel name slot"},
		{`id="tabs"`, "the sign-in group tabs"},
		{`id="lang"`, "the language selector"},
		{`id="info-btn"`, "the information affordance"},
		{`--sc-brand`, "the brand colour custom property branding drives"},
		{`/api/branding`, "the portal must ASK for the hotel's design, or branding is write-only config"},
		{`max-width: 640px`, "the phone layout"},
	} {
		if !strings.Contains(html, want.frag) {
			t.Errorf("the landing page is missing %s (%q)", want.why, want.frag)
		}
	}
}

func TestLandingShowsTheDeviceItsOwnAddresses(t *testing.T) {
	// A guest who cannot tell reception their MAC address cannot be helped by reception. Both values come
	// from the connection and the ARP table, which the portal must already know to authorise the device.
	html := renderLanding(t, "10.77.0.42", "aa:bb:cc:dd:ee:ff")
	if !strings.Contains(html, "10.77.0.42") {
		t.Error("the information panel does not show the device's IP address")
	}
	if !strings.Contains(html, "aa:bb:cc:dd:ee:ff") {
		t.Error("the information panel does not show the device's MAC address")
	}
}

func TestLandingSaysNotDetectedRatherThanGuessing(t *testing.T) {
	// No ARP entry is an ordinary state on a device that has only just appeared. The panel must say so
	// instead of rendering an empty row that reads as a broken page.
	html := renderLanding(t, "10.77.0.42", "")
	if !strings.Contains(html, "not detected") {
		t.Error("a missing MAC address should be reported as 'not detected'")
	}
}

func TestLandingIgnoresAForgedClientAddress(t *testing.T) {
	// THE SECURITY PROPERTY THAT MAKES THE PANEL SAFE. clientIP reads the CONNECTION, never a header; a guest
	// who sets X-Forwarded-For must not be able to make the portal display -- or act on -- an address that is
	// not theirs. Without this the information panel would be a way to probe the network from the outside.
	tmpl, err := template.New("land").Parse(landingHTML)
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{tmplLand: tmpl}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = net.JoinHostPort("10.77.0.42", "51000")
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.Header.Set("X-Real-IP", "203.0.113.9")
	w := httptest.NewRecorder()
	h.landing(w, r, "")

	html := w.Body.String()
	if strings.Contains(html, "203.0.113.9") {
		t.Fatal("a client-supplied address reached the page; clientIP must read the connection only")
	}
	if !strings.Contains(html, "10.77.0.42") {
		t.Error("the connection's real address should be shown")
	}
}

func TestLandingRendersEveryPanelItStillSupports(t *testing.T) {
	// The two-group presentation is PRESENTATION. Grouping seven methods behind two tabs must not quietly
	// drop any of them -- each panel is the same form it always was, and a hotel running vouchers, accounts,
	// email, SMS, social, room sign-in or post-stay must still find its form in the page.
	html := renderLanding(t, "10.77.0.42", "aa:bb:cc:dd:ee:ff")
	for _, id := range []string{
		"panel-voucher", "panel-account", "panel-email", "panel-sms", "panel-pms", "panel-social",
	} {
		if !strings.Contains(html, id) {
			t.Errorf("the %s form is no longer rendered; regrouping the tabs must not remove a sign-in method", id)
		}
	}
}
