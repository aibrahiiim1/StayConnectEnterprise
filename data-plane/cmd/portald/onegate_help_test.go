package main

// THE LIGHTBULB AND THE ATTRIBUTION, ON EVERY GUEST PAGE.
//
// Every guest page carries a lightbulb that opens a sheet of tips, and a small "Wi-Fi by OneGate" line at its
// foot. These pin what that must never cost: every script still carries the response's CSP nonce, no inline
// handler appears, the wordmark is never translated, Arabic still reads right to left, and the sheet's
// English fallbacks are the shipped English.

import (
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

type renderedPage struct {
	body, nonce string
}

var reCSPNonce = regexp.MustCompile(`'nonce-([^']+)'`)

// allGuestPages renders the sign-in page and every page after it, keeping each response's CSP nonce.
func allGuestPages(t *testing.T, design map[string]any, header http.Header) map[string]renderedPage {
	t.Helper()
	h := designHandler(t, design)
	h.tmplLand = mustParse(t, "land", landingHTML)
	h.tmplSucc = mustParse(t, "succ", successHTML)
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return net.HardwareAddr{0xaa, 0xbb, 0xcc, 1, 2, 3}, true }
	req := func(path string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "10.77.0.42:51000"
		for k, v := range header {
			r.Header[k] = v
		}
		return r
	}
	keep := func(w *httptest.ResponseRecorder) renderedPage {
		m := reCSPNonce.FindStringSubmatch(w.Header().Get("Content-Security-Policy"))
		if m == nil {
			t.Fatalf("a guest page was served without a nonce CSP: %q", w.Header().Get("Content-Security-Policy"))
		}
		return renderedPage{body: w.Body.String(), nonce: m[1]}
	}
	out := map[string]renderedPage{}
	rt := h.routes()
	for name, path := range map[string]string{"sign-in": "/", "online": "/success?s=abc&t=8100"} {
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, req(path))
		out[name] = keep(w)
	}
	w := httptest.NewRecorder()
	h.renderPackages(w, req("/packages"), []struct {
		PackageID string         `json:"package_id"`
		Display   map[string]any `json:"display"`
	}{{"p1", map[string]any{"name": "Standard"}}})
	out["packages"] = keep(w)
	w = httptest.NewRecorder()
	h.renderGuestError(w, req("/auth/social/callback"), http.StatusBadGateway, "errpage.social")
	out["sign-in failed"] = keep(w)
	w = httptest.NewRecorder()
	h.renderStatus(w, req("/status?s=abc"), scdStatus{SessionID: "abc", Active: true})
	out["status"] = keep(w)
	return out
}

var (
	reScriptOpen   = regexp.MustCompile(`<script[^>]*>`)
	reInlineHandle = regexp.MustCompile(`(?i)<[^>]+\son[a-z]+\s*=`)
)

func TestEveryGuestPageHasTheLightbulbSheetAndItsScriptCarriesTheNonce(t *testing.T) {
	for name, p := range allGuestPages(t, map[string]any{"hotel_name": "Semantics Demo Hotel", "help_text": "Dial 9 for the front desk."}, nil) {
		html := p.body
		for _, frag := range []string{
			`<details class="sc-help" id="sc-help">`,
			`<summary class="help-btn" data-i18n-aria="help.button" aria-label="` + builtinStrings["en"]["help.button"] + `"`,
			`id="sc-help-sheet"`,
			`id="sc-help-title"`,
			`data-help-close`,
			`aria-label="` + builtinStrings["en"]["help.close"] + `"`,
			template.HTMLEscapeString(builtinStrings["en"]["help.fail"]),
			`id="help-hotel"`, `Dial 9 for the front desk.`,
			// The modal upgrade: role, focus kept inside, Esc to close.
			`sheet.setAttribute('role', 'dialog')`, `sheet.setAttribute('aria-modal', 'true')`,
			`e.key === 'Escape'`, `e.key !== 'Tab'`, `sum.focus()`,
		} {
			if !strings.Contains(html, frag) {
				t.Errorf("%s: the help sheet is missing %q", name, frag)
			}
		}
		if n := strings.Count(html, `id="sc-help"`); n != 1 {
			t.Errorf("%s: the help sheet appears %d times", name, n)
		}
		// Every script carries this response's nonce, and nothing relies on an inline handler.
		for _, tag := range reScriptOpen.FindAllString(html, -1) {
			if !strings.Contains(tag, `nonce="`+p.nonce+`"`) {
				t.Errorf("%s: %s does not carry the response's nonce, so the CSP would refuse it", name, tag)
			}
		}
		if m := reInlineHandle.FindString(html); m != "" {
			t.Errorf("%s: an inline event handler reached the page: %s", name, m)
		}
	}
}

func TestTheSignInSheetExplainsEveryMethodAndTheDevice(t *testing.T) {
	html := allGuestPages(t, map[string]any{}, nil)["sign-in"].body
	for _, m := range []string{"pms", "poststay", "voucher", "account", "email", "sms", "social"} {
		if !strings.Contains(html, `data-help-method="`+m+`"`) {
			t.Errorf("the help sheet has no tip for %s", m)
		}
	}
	// The script hides the tips for methods this hotel does not offer.
	if !strings.Contains(html, `el.hidden = enabled.indexOf(el.dataset.helpMethod) < 0`) {
		t.Error("the tips do not follow the enabled methods")
	}
	// The device's addresses are in the sheet as well as behind the information button.
	sheet := html[strings.Index(html, `id="sc-help"`):strings.Index(html, `id="info-btn"`)]
	for _, want := range []string{"10.77.0.42", "aa:bb:cc:01:02:03", `data-i18n="info.help"`} {
		if !strings.Contains(sheet, want) {
			t.Errorf("the help sheet does not show %q", want)
		}
	}
	// What stays on the page: the error slots, the notices, the field hints and the terms link.
	for _, id := range []string{"access-ended", "site-notice", "pms-err", "ps-err", "pms-prompt", "ps-hint", "sms-hint", "brand-terms"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("%s left the page; it must stay visible", id)
		}
	}
	// The social note moved into the sheet, and is still translatable there.
	if strings.Count(html, `data-i18n="social.note"`) != 1 || !strings.Contains(sheet, `data-i18n="social.note"`) {
		t.Error("the social note should be in the help sheet, once")
	}
}

var reI18nEn = regexp.MustCompile(`data-i18n="([a-z][a-zA-Z0-9.]*)" data-i18n-en="([^"]*)"`)

func TestTheSheetsEnglishFallbacksAreTheShippedEnglish(t *testing.T) {
	src := landingHTML + helpOpen + helpClose + ogAttribution
	for _, m := range reI18nEn.FindAllStringSubmatch(src, -1) {
		if !strings.HasPrefix(m[1], "help.") && m[1] != "brand.by" && m[1] != "social.note" && !strings.HasPrefix(m[1], "method.") {
			continue
		}
		if want := builtinStrings["en"][m[1]]; m[2] != want {
			t.Errorf("%s falls back to %q; the shipped English is %q", m[1], m[2], want)
		}
	}
}

func TestOneGateAttributionOnEveryPageAndNeverTranslated(t *testing.T) {
	for _, l := range portalLanguages {
		for name, p := range allGuestPages(t, map[string]any{}, http.Header{"Accept-Language": {l.Code}}) {
			html := p.body
			if !strings.Contains(html, `<bdi class="og-mark" dir="ltr" lang="en"><b class="og-one">One</b><b class="og-gate">Gate</b></bdi>`) {
				t.Errorf("%s in %s: the OneGate wordmark is missing or altered", name, l.Code)
			}
			if !strings.Contains(html, `<span data-i18n="brand.by" data-i18n-en="Wi-Fi by">`+template.HTMLEscapeString(builtinStrings[l.Code]["brand.by"])+`</span>`) {
				t.Errorf("%s in %s: the attribution prefix is not in the page's language", name, l.Code)
			}
			if !strings.Contains(html, template.HTMLEscapeString(builtinStrings[l.Code]["help.title"])) {
				t.Errorf("%s in %s: the help sheet title is not in the page's language", name, l.Code)
			}
			if l.RTL && !strings.Contains(html, `dir="rtl"`) {
				t.Errorf("%s in %s: not rendered right to left", name, l.Code)
			}
		}
	}
	// The wordmark's colours are the brand's, and the gradient has a solid fallback declared before it.
	for _, frag := range []string{
		`linear-gradient(95deg, #0b7a3b, #149c4a 38%, #3cc05a 70%, #8fdc5f)`,
		`.og-gate { color: #0a0a0a; }`, `.og-one { color: #149c4a; }`,
		`@supports ((-webkit-background-clip: text) or (background-clip: text))`,
		`[dir="rtl"] .sc-help`, `[dir="rtl"] .help-card`,
	} {
		if !strings.Contains(portalBaseCSS, frag) {
			t.Errorf("the portal stylesheet is missing %q", frag)
		}
	}
}

func TestEveryHelpStringShipsInEveryLanguage(t *testing.T) {
	for _, l := range portalLanguages {
		for k, en := range helpStrings["en"] {
			v := builtinStrings[l.Code][k]
			if v == "" {
				t.Errorf("%s has no %s", l.Code, k)
			}
			if l.Code != "en" && v == en {
				t.Errorf("%s/%s is still the English", l.Code, k)
			}
		}
	}
}
