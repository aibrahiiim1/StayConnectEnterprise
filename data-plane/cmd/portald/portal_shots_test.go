package main

// RENDERING EVERY GUEST PAGE TO DISK, FOR A PERSON TO LOOK AT.
//
// Skipped unless PORTAL_SHOTS_DIR is set. With it set, this writes every page -- sign-in (with and without a
// refusal), package choice, "You're online" and the failure page -- in every layout, in English and Arabic,
// plus the unbranded default, together with the design each was rendered from. A browser script loads them
// (answering the pages' own fetches) and takes screenshots; see the delivery notes for the one used.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

func shotsDesign(template string) map[string]any {
	return map[string]any{
		"hotel_name":       "Coral Bay Resort",
		"welcome_text":     "Welcome to paradise. Connect in seconds and enjoy your stay.",
		"help_text":        "Need a hand? Dial 9 from your room or visit the front desk, open 24 hours.",
		"terms_url":        "/assets/terms.html",
		"logo_url":         "/assets/logo.png",
		"background_url":   "/assets/photo.jpg",
		"brand_color":      "#b4533f",
		"brand_color_dark": "#8e3f2f",
		"template_id":      template,
		"template_options": map[string]any{"overlay": 35},
	}
}

func TestRenderPortalShots(t *testing.T) {
	dir := os.Getenv("PORTAL_SHOTS_DIR")
	if dir == "" {
		t.Skip("set PORTAL_SHOTS_DIR to render every guest page to disk")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	req := func(path, lang string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "10.77.0.42:51000"
		r.Header.Set("Accept-Language", lang)
		return r
	}
	pkgs := []struct {
		PackageID string         `json:"package_id"`
		Display   map[string]any `json:"display"`
	}{
		{"p1", map[string]any{"name": "Standard", "down_kbps": float64(10000), "time_quota_seconds": float64(86400)}},
		{"p2", map[string]any{"name": "Premium streaming", "down_kbps": float64(50000), "time_quota_seconds": float64(86400 * 3)}},
		{"p3", map[string]any{"name": "Express 2 hours", "down_kbps": float64(25000), "time_quota_seconds": float64(7200)}},
	}
	type variant struct {
		name   string
		design map[string]any
	}
	variants := []variant{{"default", map[string]any{}}}
	for _, tp := range portaldesign.Templates {
		variants = append(variants, variant{tp.ID, shotsDesign(tp.ID)})
	}
	for _, v := range variants {
		b, _ := json.Marshal(map[string]any{"design": v.design})
		write("design-"+v.name+".json", string(b))
		for _, lang := range []string{"en", "ar"} {
			h := designHandler(t, v.design)
			h.tmplLand = mustParse(t, "land", landingHTML)
			h.tmplSucc = mustParse(t, "succ", successHTML)
			h.arpCache = func(net.IP) (net.HardwareAddr, bool) {
				return net.HardwareAddr{0xaa, 0xbb, 0xcc, 0x12, 0x34, 0x56}, true
			}
			rt := h.routes()
			render := func(path string) string {
				w := httptest.NewRecorder()
				rt.ServeHTTP(w, req(path, lang))
				return w.Body.String()
			}
			write("landing-"+v.name+"-"+lang+".html", render("/"))
			write("success-"+v.name+"-"+lang+".html", render("/success?s=abc&t=8100"))
			w := httptest.NewRecorder()
			h.landing(w, req("/auth/voucher", lang), "Voucher AUTH_DENIED.")
			write("landingerr-"+v.name+"-"+lang+".html", w.Body.String())
			w = httptest.NewRecorder()
			h.renderPackages(w, req("/packages", lang), pkgs)
			write("packages-"+v.name+"-"+lang+".html", w.Body.String())
			w = httptest.NewRecorder()
			h.renderGuestError(w, req("/auth/social/callback", lang), http.StatusBadGateway, "errpage.social")
			write("error-"+v.name+"-"+lang+".html", w.Body.String())
		}
	}
}
