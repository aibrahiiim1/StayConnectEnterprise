package main

// EVERY GUEST PAGE, IN THE GUEST'S LANGUAGE, FROM THE APPLIANCE ALONE.
//
// The pages after sign-in used to be English-only and unbranded, and the server's refusals reached guests as
// raw codes. These pin the redesign's promises: every page renders in Arabic right to left with Arabic words
// in it; no default page names anything outside the appliance; every sentence a handler composes has a
// translation; and the room sign-in sentences the page translates are exactly the server's closed set, so a
// translated page distinguishes no more than the English one does.

import (
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func mustParse(t *testing.T, name, src string) *template.Template {
	t.Helper()
	tpl, err := template.New(name).Parse(compactMarkup(src))
	if err != nil {
		t.Fatalf("%s does not parse: %v", name, err)
	}
	return tpl
}

// guestPages renders the four guest pages (and the sign-in page carrying a refusal) the way a guest receives
// them: through the production parse, with the given design and request headers.
func guestPages(t *testing.T, design map[string]any, header http.Header) map[string]string {
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
	out := map[string]string{}
	rt := h.routes()
	for name, path := range map[string]string{"sign-in": "/", "online": "/success?s=abc&t=8100"} {
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, req(path))
		out[name] = w.Body.String()
	}
	w := httptest.NewRecorder()
	h.landing(w, req("/auth/voucher"), "Voucher AUTH_DENIED.")
	out["sign-in refused"] = w.Body.String()
	w = httptest.NewRecorder()
	h.renderPackages(w, req("/packages"), []struct {
		PackageID string         `json:"package_id"`
		Display   map[string]any `json:"display"`
	}{{"p1", map[string]any{"name": "Standard", "down_kbps": float64(10000), "time_quota_seconds": float64(3600)}}})
	out["packages"] = w.Body.String()
	w = httptest.NewRecorder()
	h.renderGuestError(w, req("/auth/social/callback"), http.StatusBadGateway, "errpage.social")
	out["sign-in failed"] = w.Body.String()
	return out
}

func TestEveryGuestPageRendersInArabicRightToLeft(t *testing.T) {
	ar := builtinStrings["ar"]
	en := builtinStrings["en"]
	want := map[string][]string{
		"sign-in":         {"pms.room", "btn.submit", "voucher.label", "info.help"},
		"sign-in refused": {"err.voucher.invalid", "btn.login"},
		"online":          {"online.title", "online.lead", "online.remaining", "online.disconnect", "online.status"},
		"packages":        {"pkg.title", "pkg.subtitle"},
		"sign-in failed":  {"errpage.title", "errpage.social", "errpage.back"},
	}
	for _, header := range []http.Header{
		{"Accept-Language": {"ar-EG,ar;q=0.9,en;q=0.5"}},
		// The guest's own choice, remembered by the selector, outranks what the device asks for.
		{"Accept-Language": {"en-GB,en"}, "Cookie": {"sc-lang=ar"}},
	} {
		pages := guestPages(t, map[string]any{"hotel_name": "Coral Bay"}, header)
		for name, keys := range want {
			html := pages[name]
			if !strings.Contains(html, `lang="ar"`) || !strings.Contains(html, `dir="rtl"`) {
				t.Errorf("%s (%v): not rendered as Arabic, right to left", name, header)
			}
			for _, k := range keys {
				if ar[k] == "" || ar[k] == en[k] {
					t.Fatalf("%s has no Arabic wording", k)
				}
				if !strings.Contains(html, template.HTMLEscapeString(ar[k])) {
					t.Errorf("%s (%v): the Arabic for %s is not on the page", name, header, k)
				}
			}
		}
		if strings.Contains(pages["sign-in refused"], "AUTH_DENIED") {
			t.Error("a raw server code reached the guest")
		}
	}
}

func TestEveryGuestPageRendersInEachShippedLanguage(t *testing.T) {
	for _, l := range portalLanguages {
		pages := guestPages(t, map[string]any{}, http.Header{"Accept-Language": {l.Code}})
		for name, html := range pages {
			if !strings.Contains(html, `lang="`+l.Code+`"`) {
				t.Errorf("%s in %s: wrong lang attribute", name, l.Code)
			}
			wantDir := `dir="ltr"`
			if l.RTL {
				wantDir = `dir="rtl"`
			}
			if !strings.Contains(html, wantDir) {
				t.Errorf("%s in %s: want %s", name, l.Code, wantDir)
			}
		}
		if !strings.Contains(pages["online"], template.HTMLEscapeString(builtinStrings[l.Code]["online.title"])) {
			t.Errorf("the online page in %s does not carry its title", l.Code)
		}
	}
}

var reAbsoluteURL = regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`)

func TestDefaultGuestPagesNameNothingOutsideTheAppliance(t *testing.T) {
	// A captive portal is reached with no internet: a default page that names a font CDN, an analytics host or
	// an icon set is a page that stalls on exactly the device it is for.
	for name, html := range guestPages(t, map[string]any{}, nil) {
		if m := reAbsoluteURL.FindAllString(html, -1); len(m) > 0 {
			t.Errorf("the default %s page names an external address: %v", name, m)
		}
	}
	// With a design, the only external addresses are the ones the hotel configured.
	hotel := map[string]any{
		"hotel_name": "Coral Bay", "logo_url": "https://cdn.coralbay.example/logo.png",
		"terms_url": "https://coralbay.example/terms",
	}
	allowed := map[string]bool{"https://cdn.coralbay.example": true, "https://coralbay.example": true}
	for name, html := range guestPages(t, hotel, nil) {
		for _, m := range reAbsoluteURL.FindAllString(html, -1) {
			if !allowed[m] {
				t.Errorf("the %s page names %q, which the hotel did not configure", name, m)
			}
		}
	}
}

func TestNoStayConnectBrandingReachesAGuest(t *testing.T) {
	for name, html := range guestPages(t, map[string]any{}, nil) {
		// Script comments and CSS are compacted away; what remains is what a guest's browser receives.
		if strings.Contains(strings.ToLower(html), "stayconnect") {
			t.Errorf("the %s page mentions StayConnect", name)
		}
	}
}

func TestEveryServerSentenceHasATranslation(t *testing.T) {
	for msg, key := range serverMessageKeys {
		for _, l := range portalLanguages {
			if builtinStrings[l.Code][key] == "" {
				t.Errorf("%s has no %s wording for %q", key, l.Code, msg)
			}
		}
	}
	// The sentences a test elsewhere pins as reaching the guest are shown to an English guest word for word.
	for msg, key := range serverMessageKeys {
		if builtinStrings["en"][key] == msg {
			continue
		}
		switch msg {
		case "Please enter a voucher code.", "Your device isn't on the guest network.", "Unable to detect your device address.",
			"Internet packages are not available right now. Please ask reception.", "Please sign in again.",
			"That package is not available. Please choose another.":
			t.Errorf("%q no longer reaches an English guest as written", msg)
		}
	}
	// A raw scd code is never shown.
	for _, raw := range []string{"Voucher AUTH_DENIED.", "Voucher VOUCHER_EXPIRED.", "Voucher INTERNAL."} {
		if got := localise(builtinStrings["en"], raw); got.Key != "err.voucher.invalid" {
			t.Errorf("%q maps to %q", raw, got.Key)
		}
	}
}

// THE ROOM SIGN-IN SENTENCES STAY THE SERVER'S. The page translates by looking up the English the server
// sent; if the server's closed set and the page's keys ever disagree, a guest would silently get English --
// or worse, a translation of a different sentence. The English of each key must BE the server's sentence.
func TestRoomSignInTranslationsAreTheServersClosedSet(t *testing.T) {
	en := builtinStrings["en"]
	for key, server := range map[string]string{
		"err.room.credential": guestAuthMessage,
		"err.room.technical":  guestAuthTechnicalMessage,
		"err.wait":            guestAuthRateLimitedUnknownMessage,
		"err.generic":         guestPostStayMessage,
	} {
		if en[key] != server {
			t.Errorf("%s reads %q; the server sends %q", key, en[key], server)
		}
	}
	if got := strings.Replace(en["err.wait.seconds"], "{n}", "42", 1); got != guestAuthRateLimitedMessage(42) {
		t.Errorf("err.wait.seconds renders %q; the server sends %q", got, guestAuthRateLimitedMessage(42))
	}
	// The page's pattern for the counted sentence matches what the server sends.
	re := regexp.MustCompile(`^Too many attempts\. Please wait ([0-9]+) seconds and try again\.$`)
	if !re.MatchString(guestAuthRateLimitedMessage(42)) || !strings.Contains(landingHTML, re.String()) {
		t.Error("the sign-in page's countdown pattern no longer matches the server's sentence")
	}
	for _, key := range []string{"err.room.credential", "err.room.technical", "err.wait", "err.generic"} {
		if !strings.Contains(landingHTML, "ROOM_MESSAGES[BUILTIN.en['"+key+"']] = '"+key+"'") {
			t.Errorf("the sign-in page does not translate %s", key)
		}
	}
	// One sentence for a wrong room AND a wrong name, in every language, and it never names either as the
	// one that was wrong.
	for _, l := range portalLanguages {
		msg := strings.ToLower(builtinStrings[l.Code]["err.room.credential"])
		for _, leak := range []string{"not found", "does not exist", "no such", "occupied", "checked out", "wrong name"} {
			if strings.Contains(msg, leak) {
				t.Errorf("%s: the room sentence discloses %q", l.Code, leak)
			}
		}
	}
}

func TestSignInTabsAreAnAccessibleTablist(t *testing.T) {
	html := landingHTML
	for _, frag := range []string{
		`role="tablist"`, `el.setAttribute('role', 'tab')`, `el.setAttribute('aria-controls', 'signin-panel')`,
		`setAttribute('role', 'tabpanel')`, `el.setAttribute('aria-selected', on ? 'true' : 'false')`,
		`el.tabIndex = on ? 0 : -1`, `'ArrowRight'`, `'ArrowLeft'`, `'Home'`, `'End'`,
		`setAttribute('aria-labelledby', el.id)`,
	} {
		if !strings.Contains(html, frag) {
			t.Errorf("the sign-in tabs are missing %q", frag)
		}
	}
	// Every error box is a live region, so a refusal is announced.
	page := guestPages(t, map[string]any{}, nil)["sign-in"]
	if n := strings.Count(page, `class="err"`); n == 0 || n != strings.Count(page, `class="err" role="alert"`)+strings.Count(page, `class="err" id=`) {
		t.Errorf("an error box is not announced: %d boxes", n)
	}
	for _, id := range []string{"pms-err", "ps-err"} {
		if !strings.Contains(page, `id="`+id+`" role="alert"`) {
			t.Errorf("#%s is not announced", id)
		}
	}
}

func TestTheOnlinePageNoLongerPrintsTheSessionID(t *testing.T) {
	html := guestPages(t, map[string]any{}, nil)["online"]
	if strings.Contains(html, "abc") && strings.Contains(html, "Session:") {
		t.Error("the raw session id is still printed on the online page")
	}
	if strings.Contains(html, "window.confirm(") {
		t.Error("device removal still uses the browser's confirm dialog")
	}
}

// THE SOCIAL PROVIDER'S RETURN LEG. A refusal there used to be a bare "Sign-in failed" page carrying scd's own
// words, or a plain-text "device not on guest network". It is now the branded failure page in the guest's
// language -- with the status code the handler always sent.
func TestSocialSignInFailuresAreFriendlyPages(t *testing.T) {
	h := designHandler(t, map[string]any{"hotel_name": "Coral Bay"})
	for _, tc := range []struct {
		path, lang string
		status     int
		key        string
	}{
		{"/auth/social/callback", "en", http.StatusBadRequest, "errpage.social"},
		{"/auth/social/callback?provider=google&state=s&code=c", "ar", http.StatusBadRequest, "err.device.network"},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.RemoteAddr = "10.77.0.42:51000"
		r.Header.Set("Accept-Language", tc.lang)
		h.socialCallback(w, r)
		body := w.Body.String()
		if w.Code != tc.status {
			t.Errorf("%s: status %d, want %d", tc.path, w.Code, tc.status)
		}
		if !strings.Contains(body, template.HTMLEscapeString(builtinStrings[tc.lang][tc.key])) ||
			!strings.Contains(body, template.HTMLEscapeString(builtinStrings[tc.lang]["errpage.back"])) {
			t.Errorf("%s: the friendly page is not shown in %s", tc.path, tc.lang)
		}
		for _, raw := range []string{"missing provider", "device not on guest network", "Sign-in failed"} {
			if strings.Contains(body, raw) {
				t.Errorf("%s: the guest reads %q", tc.path, raw)
			}
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'nonce-") {
			t.Errorf("%s: the failure page has no policy", tc.path)
		}
	}
}
