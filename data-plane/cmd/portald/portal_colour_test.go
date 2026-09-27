package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func derivedMap(brand, brandDark, text string, overlay float64, photo bool) map[string]string {
	out := map[string]string{}
	for _, kv := range derivedColours(brand, brandDark, text, overlay, photo) {
		out[kv[0]] = kv[1]
	}
	return out
}

func TestParseColourReadsEveryFormADesignMayCarry(t *testing.T) {
	for _, tc := range []struct {
		in         string
		r, g, b, a float64
	}{
		{"#fff", 255, 255, 255, 1},
		{"#f5c518", 245, 197, 24, 1},
		{"#F5C518", 245, 197, 24, 1},
		{"#f5c51880", 245, 197, 24, 128.0 / 255},
		{"#0008", 0, 0, 0, 136.0 / 255},
		{"rgb(245, 197, 24)", 245, 197, 24, 1},
		{"rgba(23,115,189,0.2)", 23, 115, 189, 0.2},
		{"rgba(23, 115, 189, 50%)", 23, 115, 189, 0.5},
		{"RGB(300,0,0)", 255, 0, 0, 1},
	} {
		c, ok := parseColour(tc.in)
		if !ok {
			t.Errorf("%q did not parse", tc.in)
			continue
		}
		if c.r != tc.r || c.g != tc.g || c.b != tc.b || c.a != tc.a {
			t.Errorf("%q = %+v, want %v %v %v %v", tc.in, c, tc.r, tc.g, tc.b, tc.a)
		}
	}
	for _, bad := range []string{"", "#12", "#12345", "#1234567", "#ggg", "red", "rgb(1,2)", "hsl(0,0%,0%)"} {
		if _, ok := parseColour(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestAPaleBrandGetsDarkButtonText(t *testing.T) {
	// White on this yellow is about 1.7:1; the Login label is 16px/600 and needs 4.5:1.
	for _, brand := range []string{"#f5c518", "#f5c518ff", "rgb(245, 197, 24)", "rgba(245,197,24,1)"} {
		if got := derivedMap(brand, "", "", defaultOverlay, false)["--sc-on-brand"]; got != inkDark {
			t.Errorf("brand %s: on-brand %q, want the dark ink", brand, got)
		}
	}
	// The portal's own blue keeps white, and says nothing (the stylesheet's default is white).
	if got, ok := derivedMap("#1773bd", "", "", defaultOverlay, false)["--sc-on-brand"]; ok {
		t.Errorf("the default blue was given %q; white already reads on it", got)
	}
	// A dark brand keeps white.
	if got, ok := derivedMap("rgb(20, 40, 90)", "", "", defaultOverlay, false)["--sc-on-brand"]; ok {
		t.Errorf("a dark rgb() brand was given %q", got)
	}
}

func TestATranslucentBrandIsJudgedAsItIsSeen(t *testing.T) {
	// The portal's blue at 20% is a pale tint on the white card: white text on it is unreadable.
	for _, brand := range []string{"#1773bd33", "rgba(23, 115, 189, 0.2)", "rgba(23,115,189,20%)"} {
		if got := derivedMap(brand, "", "", defaultOverlay, false)["--sc-on-brand"]; got != inkDark {
			t.Errorf("brand %s: on-brand %q, want the dark ink", brand, got)
		}
	}
}

func TestTheHoverShadeAndLinksAreCheckedToo(t *testing.T) {
	m := derivedMap("#1773bd", "#9ecbf0", "", defaultOverlay, false)
	if m["--sc-on-brand-dark"] != inkDark {
		t.Errorf("a pale darker shade kept white hover text: %q", m["--sc-on-brand-dark"])
	}
	link, ok := parseColour(m["--sc-link"])
	if !ok {
		t.Fatalf("a pale darker shade was not darkened for links: %q", m["--sc-link"])
	}
	if c := contrast(link, white); c < minTextContrast {
		t.Errorf("the derived link colour %s is %.2f:1 on the card", m["--sc-link"], c)
	}
	// A yellow brand with the default darker shade: the button is dark-on-yellow, its hover white-on-blue.
	m = derivedMap("#f5c518", "", "", defaultOverlay, false)
	if _, ok := m["--sc-on-brand-dark"]; ok {
		t.Errorf("the default darker shade was given %q; white reads on it", m["--sc-on-brand-dark"])
	}
	// A readable darker shade is used as it is.
	if _, ok := derivedMap("#1773bd", "#125c97", "", defaultOverlay, false)["--sc-link"]; ok {
		t.Error("a readable link colour was replaced")
	}
}

func TestAPaleTextColourFallsBackToThePortalsInk(t *testing.T) {
	if _, ok := derivedMap("", "", "#c8c8c8", defaultOverlay, false)["--sc-ink"]; ok {
		t.Error("a pale text colour (about 1.7:1 on the card) was kept")
	}
	if _, ok := derivedMap("", "", "rgba(20, 22, 26, 0.3)", defaultOverlay, false)["--sc-ink"]; ok {
		t.Error("a faint translucent text colour was kept")
	}
	if got := derivedMap("", "", "#333333", defaultOverlay, false)["--sc-ink"]; got != "#333333" {
		t.Errorf("a readable text colour was dropped: %q", got)
	}
}

func TestTheHeroWordsReadOnTheBrandGradient(t *testing.T) {
	// No photograph: a yellow brand over a yellow shade is dark ink, without the dark halo.
	m := derivedMap("#f5c518", "#e0b000", "", defaultOverlay, false)
	if m["--sc-on-hero"] != inkDark || m["--sc-hero-shade"] != "transparent" {
		t.Errorf("hero ink on a yellow gradient: %v", m)
	}
	// A photograph keeps white words.
	if _, ok := derivedMap("#f5c518", "#e0b000", "", defaultOverlay, true)["--sc-on-hero"]; ok {
		t.Error("the hero words changed colour over a photograph")
	}
}

func TestBrandForDerivesAndDoesNotRepeatThePhoto(t *testing.T) {
	b := brandFor(map[string]any{
		"brand_color":      "#f5c518",
		"background_url":   "/assets/bg.jpg",
		"text_color":       "#dddddd",
		"template_id":      "split",
		"logo_url":         "/assets/logo.png",
		"template_options": map[string]any{"overlay": float64(0)},
	})
	style := string(b.Style)
	if strings.Count(style, "bg.jpg") != 1 {
		t.Errorf("the background photograph is written %d times: %s", strings.Count(style, "bg.jpg"), style)
	}
	if strings.Contains(style, "--sc-hero:") {
		t.Errorf("--sc-hero repeats the background; the stylesheet defaults it: %s", style)
	}
	if !strings.Contains(style, "--sc-on-brand:"+inkDark) {
		t.Errorf("the yellow brand has no dark button text: %s", style)
	}
	if strings.Contains(style, "--sc-ink") {
		t.Errorf("a pale text colour reached the page: %s", style)
	}
	// 0% darkening over a photograph is raised to the minimum.
	if !strings.Contains(style, "--sc-overlay:0.30") {
		t.Errorf("the photograph's darkening was not raised to 0.30: %s", style)
	}
	if !b.HeroLogo || b.CardLogo {
		t.Errorf("split shows the logo in the hero only; got hero=%v card=%v", b.HeroLogo, b.CardLogo)
	}
	// With no photograph, 0% darkening is the hotel's choice.
	if s := string(brandFor(map[string]any{"template_options": map[string]any{"overlay": float64(0)}}).Style); !strings.Contains(s, "--sc-overlay:0.00") {
		t.Errorf("no photograph: %s", s)
	}
}

func TestTheLogoIsDrawnOnlyWhereTheLayoutShowsIt(t *testing.T) {
	for _, tc := range []struct {
		tpl        string
		hero, card int
	}{{"classic", 0, 1}, {"kiosk", 0, 1}, {"split", 1, 0}, {"immersive", 1, 0}, {"editorial", 1, 0}, {"headerbar", 1, 0}} {
		h := designHandler(t, map[string]any{"template_id": tc.tpl, "logo_url": "/assets/logo-x.png"})
		html := get(t, h, "/").Body.String()
		if n := strings.Count(html, `src="/assets/logo-x.png"`); n != tc.hero+tc.card {
			t.Errorf("%s: the logo is drawn %d times", tc.tpl, n)
		}
		if tc.card == 1 && !strings.Contains(html, `id="brand-logo" alt="" src="/assets/logo-x.png"`) {
			t.Errorf("%s: the card logo is missing", tc.tpl)
		}
	}
}

func TestSignInWorksWithoutScript(t *testing.T) {
	html := renderLanding(t, "10.77.0.42", "")
	ns := strings.ReplaceAll(html[strings.Index(html, "<noscript>"):strings.Index(html, "</noscript>")], " ", "")
	for _, want := range []string{"#panel-accountlogin{display:block;}", "#form-credentials{display:block!important"} {
		if !strings.Contains(ns, want) {
			t.Errorf("without script the plain-HTML forms stay hidden (missing %q in %q)", want, ns)
		}
	}
	if strings.Index(html, "<noscript>") < strings.Index(html, `<style id="sc-guard">`) {
		t.Error("the no-script sheet must come after the guard, which hides inactive panels")
	}
}

func TestAServerRefusalIsFocusedAndPointsAtTheField(t *testing.T) {
	h := designHandler(t, map[string]any{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/auth/voucher", strings.NewReader("code=ab%22%3Ccd"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "10.77.0.42:51000"
	_ = r.ParseForm()
	h.landing(w, r, "Invalid voucher.")
	html := w.Body.String()
	for _, want := range []string{
		`id="server-error" role="alert" tabindex="-1"`,
		`document.getElementById('server-error')`,
		`value="ab&#34;&lt;cd"`,
		`aria-describedby="server-error" aria-invalid="true"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(html, `id="server-error" role="alert" aria-live`) {
		t.Error("role=alert and aria-live are both set on the server's refusal")
	}
	// A refused personal account comes back with the personal-account form showing, the username kept and the
	// password not.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/auth/credentials", strings.NewReader("username=guest7&password=hunter2"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "10.77.0.42:51000"
	_ = r.ParseForm()
	h.landing(w, r, "Invalid username or password.")
	html = w.Body.String()
	if !strings.Contains(html, `id="use-personal" role="switch" checked`) || !strings.Contains(html, `value="guest7"`) {
		t.Error("the refused personal account is not what the guest sees")
	}
	if strings.Contains(html, "hunter2") {
		t.Error("the password was given back")
	}
}

func TestTextResponsesAreCompressed(t *testing.T) {
	h := designHandler(t, map[string]any{"hotel_name": "Semantics Demo Hotel"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.77.0.42:51000"
	r.Header.Set("Accept-Encoding", "gzip, deflate, br")
	h.routes().ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("the sign-in page was not gzipped: %v", w.Header())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("compression changed Cache-Control: %q", w.Header().Get("Cache-Control"))
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(zr)
	if err != nil || !strings.Contains(string(body), "Semantics Demo Hotel") {
		t.Errorf("the compressed page does not decompress to the page (%v)", err)
	}
	// Without Accept-Encoding the page is sent as it always was.
	if w := get(t, h, "/"); w.Header().Get("Content-Encoding") != "" || !strings.Contains(w.Body.String(), "<!doctype html>") {
		t.Error("a client that did not ask for gzip got it")
	}
	if compressible("image/png") || compressible("image/jpeg") || !compressible("application/json") || !compressible("text/html; charset=utf-8") {
		t.Error("compressible() is wrong about a content type")
	}
}
