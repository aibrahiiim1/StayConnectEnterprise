package main

// The pages after sign-in: a browser navigation gets a branded page in its language, a script gets exactly the bytes it always got, and the hotel's custom CSS reaches every page through the same sanitiser, layer and guard as the sign-in page.

import (
	"context"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

// statusHandler's scd answers branding and status lookups; a nil status body makes scd unreachable for the status call only.
func statusHandler(t *testing.T, design map[string]any, status string, code int) *handler {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tenant/branding":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"design": design})
		case "/v1/sessions/status":
			if status == "" {
				hj, _ := w.(http.Hijacker)
				c, _, _ := hj.Hijack()
				_ = c.Close()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(status))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ts.Close)
	addr := ts.Listener.Addr().String()
	return &handler{
		tmplLand: mustParse(t, "land", landingHTML),
		tmplSucc: mustParse(t, "succ", successHTML),
		designs:  &designCache{},
		arpCache: func(net.IP) (net.HardwareAddr, bool) { return net.HardwareAddr{0xaa, 0xbb, 0xcc, 1, 2, 3}, true },
		scd: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", addr)
			},
		}},
	}
}

func squash(s string) string { return strings.Join(strings.Fields(s), "") }

func esc(s string) string { return template.HTMLEscapeString(s) }

const browserAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"

func serve(h *handler, method, path string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "10.77.0.42:51000"
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.routes().ServeHTTP(w, r)
	return w
}

var rawAnswer = regexp.MustCompile(`"session_id"|"active"|"time_mode"|\{"error"|scd unreachable|bad ip|lookup failed`)

const scdAggregate = `{"ip":"10.77.0.42","session_id":"sess-9","active":true,"time_mode":"AGGREGATE_ONLINE_TIME",` +
	`"remaining_online_seconds":7800,"hard_expiry":"2026-09-30T12:00:00Z"}` + "\n"

func TestStatusNavigatedIsABrandedPageAndFetchedIsTheSameJSON(t *testing.T) {
	h := statusHandler(t, shotsDesign("classic"), scdAggregate, 200)

	// A script (Accept: application/json or */*) gets scd's answer byte for byte.
	for _, accept := range []string{"application/json", "*/*", ""} {
		w := serve(h, http.MethodGet, "/status", map[string]string{"Accept": accept})
		if w.Code != 200 || w.Body.String() != scdAggregate {
			t.Errorf("Accept %q: got %d %q, want scd's JSON unchanged", accept, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Accept %q: Content-Type %q, want application/json", accept, ct)
		}
	}

	w := serve(h, http.MethodGet, "/status?s=sess-9&t=8100", map[string]string{"Accept": browserAccept, "Accept-Language": "en"})
	page := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("navigation got %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if rawAnswer.MatchString(page) {
		t.Errorf("the status page shows a raw answer: %s", rawAnswer.FindString(page))
	}
	en := builtinStrings["en"]
	for _, want := range []string{
		"Semantics Demo Hotel", esc(en["online.title"]), en["online.remaining"], "2 h 10 min", en["tl.note"],
		`data-at="2026-09-30T12:00:00Z"`, `action="/logout"`, en["online.disconnect"], en["online.back"],
		`href="/success?s=sess-9&amp;t=8100"`, `id="lang"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the status page lacks %q", want)
		}
	}
	assertNonced(t, w, "the status page")
}

func TestStatusPageIsTranslatedAndMirrored(t *testing.T) {
	h := statusHandler(t, map[string]any{}, scdAggregate, 200)
	w := serve(h, http.MethodGet, "/status", map[string]string{"Accept": browserAccept, "Accept-Language": "ar"})
	page := w.Body.String()
	ar := builtinStrings["ar"]
	if !strings.Contains(page, `dir="rtl"`) || !strings.Contains(page, `lang="ar"`) {
		t.Error("the Arabic status page is not right to left")
	}
	for _, k := range []string{"online.title", "online.remaining", "online.disconnect", "online.back", "brand.fallback"} {
		if !strings.Contains(page, ar[k]) {
			t.Errorf("the Arabic status page lacks %s (%q)", k, ar[k])
		}
	}
	for _, k := range []string{"online.title", "online.disconnect", "online.back"} {
		if strings.Contains(page, ">"+builtinStrings["en"][k]+"<") {
			t.Errorf("the Arabic status page still reads %q in English", builtinStrings["en"][k])
		}
	}
}

// An ordinary package: the page says the device is online and invents no time it was not given.
func TestStatusPageForAnOrdinaryPackageClaimsNoTime(t *testing.T) {
	h := statusHandler(t, map[string]any{}, `{"ip":"10.77.0.42","session_id":"sess-1","active":true}`, 200)
	page := serve(h, http.MethodGet, "/status", map[string]string{"Accept": browserAccept}).Body.String()
	en := builtinStrings["en"]
	if !strings.Contains(page, esc(en["online.title"])) {
		t.Error("the page does not say the device is online")
	}
	if strings.Contains(page, en["online.remaining"]) || strings.Contains(page, en["online.unlimited"]) {
		t.Error("the page states a time scd did not report")
	}
	if !strings.Contains(page, `href="/success?s=sess-1"`) {
		t.Error("the way back does not lead to the online page")
	}
}

func TestStatusNavigatedWhenNotOnlineGoesToSignIn(t *testing.T) {
	h := statusHandler(t, map[string]any{}, `{"ip":"10.77.0.42","session_id":"","active":false}`, 200)
	w := serve(h, http.MethodGet, "/status", map[string]string{"Accept": browserAccept})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("got %d to %q, want 303 to the sign-in page", w.Code, w.Header().Get("Location"))
	}
	w = serve(h, http.MethodGet, "/status", map[string]string{"Accept": "application/json"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"active":false`) {
		t.Errorf("the JSON answer changed: %d %s", w.Code, w.Body.String())
	}
}

func TestStatusWhenTheApplianceCannotAnswerIsAFriendlyPage(t *testing.T) {
	h := statusHandler(t, map[string]any{}, "", 0)
	w := serve(h, http.MethodGet, "/status?s=sess-1", map[string]string{"Accept": browserAccept, "Accept-Language": "de"})
	page := w.Body.String()
	if w.Code != 500 {
		t.Errorf("status %d, want the 500 it always sent", w.Code)
	}
	if rawAnswer.MatchString(page) {
		t.Errorf("the guest reads %q", rawAnswer.FindString(page))
	}
	de := builtinStrings["de"]
	for _, k := range []string{"online.status", "err.service", "online.back"} {
		if !strings.Contains(page, esc(de[k])) {
			t.Errorf("the page lacks %s in German (%q)", k, de[k])
		}
	}
	if !strings.Contains(page, `role="alert"`) || !strings.Contains(page, `href="/success?s=sess-1"`) {
		t.Error("the message is not announced, or the way back is missing")
	}
	w = serve(h, http.MethodGet, "/status", map[string]string{"Accept": "application/json"})
	if w.Code != 500 || !strings.Contains(w.Body.String(), "scd unreachable") {
		t.Errorf("the script's answer changed: %d %q", w.Code, w.Body.String())
	}
	h = statusHandler(t, map[string]any{}, `{"error":"lookup failed"}`, 500)
	page = serve(h, http.MethodGet, "/status", map[string]string{"Accept": browserAccept}).Body.String()
	if rawAnswer.MatchString(page) || !strings.Contains(page, builtinStrings["en"]["err.service"]) {
		t.Error("scd's refusal reached the guest's page")
	}
}

func TestWantsHTMLTellsANavigationFromAScript(t *testing.T) {
	for accept, want := range map[string]bool{
		browserAccept:                        true,
		"text/html":                          true,
		"application/json":                   false,
		"*/*":                                false,
		"":                                   false,
		"application/json, text/html;q=0.5":  false,
		"text/html;q=0.9, application/json":  false,
		"application/json;q=0.2, text/html":  true,
		"application/xhtml+xml,text/*;q=0.1": true,
	} {
		r := httptest.NewRequest(http.MethodGet, "/status", nil)
		if accept != "" {
			r.Header.Set("Accept", accept)
		}
		if got := wantsHTML(r); got != want {
			t.Errorf("Accept %q: wantsHTML %v, want %v", accept, got, want)
		}
	}
}

func TestSocialStartRefusalsAreFriendlyPagesForABrowser(t *testing.T) {
	h := statusHandler(t, map[string]any{}, `{}`, 200)
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return nil, false }
	w := serve(h, http.MethodGet, "/auth/social/start?provider=google", map[string]string{"Accept": browserAccept, "Accept-Language": "fr"})
	page := w.Body.String()
	if w.Code != 400 {
		t.Errorf("status %d, want the 400 it always sent", w.Code)
	}
	if rawAnswer.MatchString(page) || strings.Contains(page, "device not on this Wi-Fi network") {
		t.Error("the guest reads the raw refusal")
	}
	if !strings.Contains(page, esc(builtinStrings["fr"]["err.device.network"])) {
		t.Error("the refusal is not in the guest's language")
	}
	w = serve(h, http.MethodGet, "/auth/social/start?provider=google", map[string]string{"Accept": "application/json"})
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"device not on this Wi-Fi network"`) {
		t.Errorf("the JSON refusal changed: %d %s", w.Code, w.Body.String())
	}
	w = serve(h, http.MethodGet, "/auth/social/start", map[string]string{"Accept": browserAccept})
	if w.Code != 400 || !strings.Contains(w.Body.String(), esc(builtinStrings["en"]["errpage.social"])) {
		t.Errorf("a start with no provider is not the friendly page: %d", w.Code)
	}
}

// The hotel's CSS on every page after sign-in: sanitised, layered, guarded, nonced.
func TestHotelCSSReachesEveryPageAfterSignIn(t *testing.T) {
	design := shotsDesign("classic")
	design["custom_css"] = ".page-title { color: #0a5c4a !important; letter-spacing: 0.01em; }\n" +
		"@import url('https://evil.example/x.css');\n.fact { background: url(javascript:alert(1)); border-radius: 2px; }"
	design["custom_html"] = `<p class="hotel-extra">Pool opens at 7</p>`
	h := statusHandler(t, design, scdAggregate, 200)

	pages := map[string]*httptest.ResponseRecorder{
		"online": serve(h, http.MethodGet, "/success?s=a&t=60", map[string]string{"Accept": browserAccept}),
		"status": serve(h, http.MethodGet, "/status", map[string]string{"Accept": browserAccept}),
	}
	req := func(path string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "10.77.0.42:51000"
		return r
	}
	w := httptest.NewRecorder()
	h.renderPackages(w, req("/packages"), []guestPackage{{PackageID: "p1", Display: map[string]any{"name": "Standard"}}}, "", "", false, "")
	pages["packages"] = w
	w = httptest.NewRecorder()
	h.renderGuestError(w, req("/auth/social/callback"), http.StatusBadGateway, "errpage.social")
	pages["failure"] = w
	pages["status failure"] = serve(statusHandler(t, design, "", 0), http.MethodGet, "/status", map[string]string{"Accept": browserAccept})

	for name, w := range pages {
		html := w.Body.String()
		assertNonced(t, w, name)
		i := strings.Index(html, `<style id="sc-hotel">@layer hotel {`)
		if i < 0 {
			t.Errorf("%s: the hotel's stylesheet is not applied", name)
			continue
		}
		sheet := html[i : i+strings.Index(html[i:], "</style>")]
		if !strings.Contains(sheet, "color:#0a5c4a") && !strings.Contains(sheet, "color: #0a5c4a") {
			t.Errorf("%s: the hotel's own rule is missing from its sheet: %s", name, sheet)
		}
		for _, bad := range []string{"important", "@import", "evil.example", "javascript:"} {
			if strings.Contains(strings.ToLower(sheet), bad) {
				t.Errorf("%s: %q survived into the hotel sheet", name, bad)
			}
		}
		// The cascade: portal layers, then the hotel's layer, then the unlayered guard; compared without whitespace.
		squashed := squash(html)
		last := -1
		for _, frag := range []string{`@layer sc-base, sc-template, hotel;`, `@layer sc-template {`,
			`<style id="sc-hotel">@layer hotel {`, `<style id="sc-guard">`, `</head>`} {
			j := strings.Index(squashed, squash(frag))
			if j < 0 || j < last {
				t.Errorf("%s: %q is missing or out of order", name, frag)
			}
			last = j
		}
		guard := html[strings.Index(html, `<style id="sc-guard">`):]
		guard = guard[:strings.Index(guard, "</style>")]
		if strings.Contains(guard, "@layer") || strings.Contains(guard, "!important") {
			t.Errorf("%s: the guard must be unlayered and free of !important", name)
		}
		for _, control := range []string{".actions .btn", ".choice-list button.choice", "[role=alert]", "#lang"} {
			if !strings.Contains(guard, control) {
				t.Errorf("%s: the guard does not cover %s", name, control)
			}
		}
		if !strings.Contains(squashed, `['sc-base','sc-templates','sc-hotel']`) {
			t.Errorf("%s: the no-layers fallback does not unwrap the hotel sheet", name)
		}
		if strings.Contains(html, "hotel-extra") {
			t.Errorf("%s: the hotel's custom HTML appears after sign-in", name)
		}
	}
}

func TestNoHotelCSSMeansNoHotelSheet(t *testing.T) {
	w := get(t, designHandler(t, map[string]any{}), "/success?s=a&t=1")
	if strings.Contains(w.Body.String(), `id="sc-hotel"`) {
		t.Error("an empty hotel sheet was emitted")
	}
	if got := hotelSheet(map[string]any{"custom_css": "a{color:red}</style><script>x</script>"}); got != "" {
		t.Errorf("a sheet that could close its element was served: %q", got)
	}
}

// edged refuses a language with more than MaxTranslationKeys overrides, so the portal must ship no more keys than that.
func TestTheDictionaryFitsTheHotelsOverrideLimit(t *testing.T) {
	if n := len(builtinStrings["en"]); n > portaldesign.MaxTranslationKeys {
		t.Errorf("the portal ships %d keys; a hotel can override at most %d per language", n, portaldesign.MaxTranslationKeys)
	}
}
