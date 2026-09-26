package main

// THE PAGES AFTER SIGN-IN: THE STATUS PAGE, THE HOTEL'S LOOK, AND NO RAW ANSWERS IN A GUEST'S BROWSER.
//
// A guest who tapped "Status" used to be shown scd's JSON, and one whose social sign-in could not start was
// shown {"error":"bad ip"}. These pin the fix without moving the contract underneath it: a browser navigating
// gets a branded page in its language, and a script -- the online page's own fetch among them -- gets exactly
// the bytes it always got. They also pin that the hotel's custom CSS reaches every page after sign-in through
// the same sanitiser, the same layer and under the same guard as the sign-in page.

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

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

// statusHandler is a portald handler whose scd answers the branding and session-status lookups. A nil status
// body makes scd unreachable for the status call only (the connection is closed without an answer).
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

// squash drops all whitespace, so a fragment can be found in a compacted stylesheet.
func squash(s string) string { return strings.Join(strings.Fields(s), "") }

// esc is a dictionary entry as it appears in the page's text ("You're" is "You&#39;re").
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

// The raw shapes a guest must never read: scd's JSON, and the plain errors this service used to write.
var rawAnswer = regexp.MustCompile(`"session_id"|"active"|"time_mode"|\{"error"|scd unreachable|bad ip|lookup failed`)

const scdAggregate = `{"ip":"10.77.0.42","session_id":"sess-9","active":true,"time_mode":"AGGREGATE_ONLINE_TIME",` +
	`"remaining_online_seconds":7800,"hard_expiry":"2026-09-30T12:00:00Z"}` + "\n"

func TestStatusNavigatedIsABrandedPageAndFetchedIsTheSameJSON(t *testing.T) {
	h := statusHandler(t, shotsDesign("classic"), scdAggregate, 200)

	// A SCRIPT: the online page's own fetch sends Accept: application/json; a bare fetch sends */*. Both get
	// scd's answer byte for byte, as before.
	for _, accept := range []string{"application/json", "*/*", ""} {
		w := serve(h, http.MethodGet, "/status", map[string]string{"Accept": accept})
		if w.Code != 200 || w.Body.String() != scdAggregate {
			t.Errorf("Accept %q: got %d %q, want scd's JSON unchanged", accept, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Accept %q: Content-Type %q, want application/json", accept, ct)
		}
	}

	// A BROWSER: a branded page, in words, with the online page's actions.
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

// An ordinary package: scd says only that the device is online. The page says that and invents nothing -- no
// time it was not given.
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
	// The script still reads active:false as it always did.
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
	// And a script still gets the plain answer it always got.
	w = serve(h, http.MethodGet, "/status", map[string]string{"Accept": "application/json"})
	if w.Code != 500 || !strings.Contains(w.Body.String(), "scd unreachable") {
		t.Errorf("the script's answer changed: %d %q", w.Code, w.Body.String())
	}
	// scd answering with an error is also words, never its body.
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

// Social sign-in is started by a LINK, so its refusals are pages a browser lands on.
func TestSocialStartRefusalsAreFriendlyPagesForABrowser(t *testing.T) {
	h := statusHandler(t, map[string]any{}, `{}`, 200)
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return nil, false }
	w := serve(h, http.MethodGet, "/auth/social/start?provider=google", map[string]string{"Accept": browserAccept, "Accept-Language": "fr"})
	page := w.Body.String()
	if w.Code != 400 {
		t.Errorf("status %d, want the 400 it always sent", w.Code)
	}
	if rawAnswer.MatchString(page) || strings.Contains(page, "device not on guest network") {
		t.Error("the guest reads the raw refusal")
	}
	if !strings.Contains(page, esc(builtinStrings["fr"]["err.device.network"])) {
		t.Error("the refusal is not in the guest's language")
	}
	w = serve(h, http.MethodGet, "/auth/social/start?provider=google", map[string]string{"Accept": "application/json"})
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"device not on guest network"`) {
		t.Errorf("the JSON refusal changed: %d %s", w.Code, w.Body.String())
	}
	w = serve(h, http.MethodGet, "/auth/social/start", map[string]string{"Accept": browserAccept})
	if w.Code != 400 || !strings.Contains(w.Body.String(), esc(builtinStrings["en"]["errpage.social"])) {
		t.Errorf("a start with no provider is not the friendly page: %d", w.Code)
	}
}

// THE HOTEL'S CSS ON EVERY PAGE AFTER SIGN-IN -- sanitised, layered, guarded, nonced.
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
	h.renderPackages(w, req("/packages"), []struct {
		PackageID string         `json:"package_id"`
		Display   map[string]any `json:"display"`
	}{{"p1", map[string]any{"name": "Standard"}}})
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
		// The same sanitiser as the sign-in page: no !important, no @import, no script URL.
		for _, bad := range []string{"important", "@import", "evil.example", "javascript:"} {
			if strings.Contains(strings.ToLower(sheet), bad) {
				t.Errorf("%s: %q survived into the hotel sheet", name, bad)
			}
		}
		// The cascade: portal layers, then the hotel's layer, then the unlayered guard over this page's controls.
		// Compared without whitespace: the production parse compacts the stylesheets.
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
		// A browser without layers still gets base, template, hotel, guard in that order.
		if !strings.Contains(squashed, `['sc-base','sc-templates','sc-hotel']`) {
			t.Errorf("%s: the no-layers fallback does not unwrap the hotel sheet", name)
		}
		// The extras block stays on the sign-in page.
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

// THE COMMERCE PANEL'S WORDS: every key, non-empty, in every shipped language; none of them in the dictionary a
// hotel overrides (so neither counted against the cap nor offered in Hotel Admin); and a page in Arabic that
// carries them in Arabic.
func TestCommercePanelIsTranslatedInEveryShippedLanguage(t *testing.T) {
	en := commerceStrings["en"]
	for _, l := range portalLanguages {
		words := commerceStrings[l.Code]
		for _, k := range commerceKeys {
			if strings.TrimSpace(words[k]) == "" {
				t.Errorf("%s: commerce key %s is missing or empty", l.Code, k)
			}
		}
		if len(words) != len(commerceKeys) {
			t.Errorf("%s has %d commerce strings, want exactly the %d keys the panel reads", l.Code, len(words), len(commerceKeys))
		}
		if l.Code != "en" {
			same := 0
			for _, k := range commerceKeys {
				if words[k] == en[k] {
					same++
				}
			}
			if same > len(commerceKeys)/4 {
				t.Errorf("%s repeats the English for %d of %d commerce strings", l.Code, same, len(commerceKeys))
			}
		}
	}
	for _, k := range commerceKeys {
		if _, ok := builtinStrings["en"][k]; ok {
			t.Errorf("%s is in the hotel-overridable dictionary; the commerce table is portal-only", k)
		}
	}
	// The English the browser tests read.
	for k, want := range map[string]string{"cx.select": "Select", "cx.confirm": "Confirm", "cx.devices": "Devices: {n}", "cx.active.title": "Package active"} {
		if en[k] != want {
			t.Errorf("%s reads %q, the guest-portal e2e spec expects %q", k, en[k], want)
		}
	}

	h := designHandler(t, map[string]any{})
	h.tmplSucc = mustParse(t, "succ", successHTML)
	h.commerceCfg = iamv2.CommerceConfig{MasterEnabled: true, PortalEnabled: true}
	for _, lang := range []string{"ar", "en"} {
		r := httptest.NewRequest(http.MethodGet, "/success?s=a&t=60", nil)
		r.RemoteAddr = "10.77.0.42:51000"
		r.Header.Set("Accept-Language", lang)
		w := httptest.NewRecorder()
		h.routes().ServeHTTP(w, r)
		page := w.Body.String()
		words := commerceWords(lang)
		if !strings.Contains(page, `id="commerce"`) || !strings.Contains(page, words["cx.title"]) || !strings.Contains(page, words["cx.loading"]) {
			t.Errorf("%s: the panel heading is not in the guest's language", lang)
		}
		// The script's table is this language's, not English's.
		if !strings.Contains(page, `"cx.select":"`+words["cx.select"]+`"`) || !strings.Contains(page, `"cx.end.MANUAL_END":"`+words["cx.end.MANUAL_END"]+`"`) {
			t.Errorf("%s: the panel script does not carry this language's words", lang)
		}
		if lang == "ar" && (!strings.Contains(page, `dir="rtl"`) || strings.Contains(page, ">Available packages<")) {
			t.Error("the Arabic panel is not right to left, or its heading is English")
		}
		// No English string and no raw code is left in the panel's script.
		for _, raw := range []string{"'Select'", "'Confirm'", "Devices: '", "Ends: '", "end_mode||'MANUAL_END'", "Package active<", "Offer expires: '"} {
			if strings.Contains(page, raw) {
				t.Errorf("%s: the panel still hard-codes %q", lang, raw)
			}
		}
	}
}

// THE OVERRIDE BUDGET. edged refuses a language with more than MaxTranslationKeys overrides, so a hotel that
// adds a language of its own can only translate the whole portal if the portal ships no more keys than that.
func TestTheDictionaryFitsTheHotelsOverrideLimit(t *testing.T) {
	if n := len(builtinStrings["en"]); n > portaldesign.MaxTranslationKeys {
		t.Errorf("the portal ships %d keys; a hotel can override at most %d per language", n, portaldesign.MaxTranslationKeys)
	}
}
