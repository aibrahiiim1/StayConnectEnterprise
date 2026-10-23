package main

// The guest portal held to the reference design: structural assertions (branded card, two sign-in groups, language selector, information affordance with the device's own addresses), not cosmetic ones.

import (
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

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
		{`max-width: 680px`, "the phone layout"},
		{`class="page"`, "the column wrapper; a flex ROW put the language bar BESIDE the card on a phone"},
	} {
		if !strings.Contains(html, want.frag) {
			t.Errorf("the landing page is missing %s (%q)", want.why, want.frag)
		}
	}
}

func TestLandingShowsTheDeviceItsOwnAddresses(t *testing.T) {
	// Both addresses come from the connection and the ARP table, so reception can help a guest who cannot state their MAC.
	html := renderLanding(t, "10.77.0.42", "aa:bb:cc:dd:ee:ff")
	if !strings.Contains(html, "10.77.0.42") {
		t.Error("the information panel does not show the device's IP address")
	}
	if !strings.Contains(html, "aa:bb:cc:dd:ee:ff") {
		t.Error("the information panel does not show the device's MAC address")
	}
}

func TestLandingSaysNotDetectedRatherThanGuessing(t *testing.T) {
	// No ARP entry is ordinary for a device that has just appeared; the panel says so instead of rendering an empty row.
	html := renderLanding(t, "10.77.0.42", "")
	if !strings.Contains(strings.ToLower(html), "not detected") {
		t.Error("a missing MAC address should be reported as 'not detected'")
	}
}

func TestLandingIgnoresAForgedClientAddress(t *testing.T) {
	// clientIP reads the connection, never a header: a guest setting X-Forwarded-For must not make the portal display or act on an address that is not theirs.
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
	// Grouping the methods behind two tabs must not drop any form a hotel runs.
	html := renderLanding(t, "10.77.0.42", "aa:bb:cc:dd:ee:ff")
	for _, id := range []string{
		"form-voucher", "form-credentials", "panel-email", "form-acct-signin", "form-acct-create", "form-acct-forgot",
		"panel-sms", "panel-pms", "panel-social",
	} {
		if !strings.Contains(html, id) {
			t.Errorf("%s is no longer rendered; restyling the portal must not remove a sign-in method", id)
		}
	}
	if !strings.Contains(html, `id="use-personal"`) {
		t.Error("the Use Personal Account toggle is missing, so one of the two account forms cannot be reached")
	}
}

// Translation contract, portal half: each guest-facing string is tagged data-i18n and Hotel Admin offers a field per key; drift means an operator types words that never appear.
func TestLandingTagsEveryStringTheDesignerOffersToTranslate(t *testing.T) {
	html := renderLanding(t, "10.77.0.42", "aa:bb:cc:dd:ee:ff")

	// Kept in step with PORTAL_STRINGS in hotel-admin/app/(app)/portal-branding/page.tsx.
	for _, key := range []string{
		"pms.room", "account.pass", "account.user", "voucher.label",
		"email.dest", "sms.dest", "otp.code",
		"acct.forgot.link", "acct.create.link", "acct.signin.link", "acct.confirm", "acct.rule", "acct.create.btn",
		"acct.forgot.lead", "acct.forgot.btn", "help.email.account",
		"btn.submit", "btn.login", "btn.verify",
		"info.device", "info.ip", "info.mac", "info.help",
	} {
		if !strings.Contains(html, `data-i18n="`+key+`"`) {
			t.Errorf("the portal never tags %q, so translating it changes nothing a guest sees", key)
		}
	}
	// The two group tabs are translated by re-rendering, so their keys are asserted in the script.
	for _, key := range []string{"tab.guest", "tab.account"} {
		if !strings.Contains(html, "'tab.' + gid") && !strings.Contains(html, key) {
			t.Errorf("group tab key %q is not reachable by a translation", key)
		}
	}
}

// The six languages ship with the build: these read the shipped wording as data, the same map the page renders and /api/languages serves.
func TestPortalShipsWordsForEveryLanguageItOffers(t *testing.T) {
	want := []struct{ code, native string }{
		{"en", "English"}, {"ar", "العربية"}, {"de", "Deutsch"},
		{"fr", "Français"}, {"it", "Italiano"}, {"ru", "Русский"},
	}
	if len(portalLanguages) != len(want) {
		t.Fatalf("the portal offers %d languages, want %d", len(portalLanguages), len(want))
	}
	byCode := map[string]portalLanguage{}
	for _, l := range portalLanguages {
		byCode[l.Code] = l
	}
	for _, w := range want {
		l, ok := byCode[w.code]
		if !ok {
			t.Errorf("%s is not among the offered languages", w.code)
			continue
		}
		// Offered under its own name; "RU" is not a word a Russian speaker looks for.
		if l.Label != w.native {
			t.Errorf("%s is offered as %q, want its native name %q", w.code, l.Label, w.native)
		}
		if len(builtinStrings[w.code]) == 0 {
			t.Errorf("%s is offered with no wording behind it", w.code)
		}
	}
}

func TestEveryShippedLanguageIsComplete(t *testing.T) {
	// A language missing a key renders English mid-sentence; English is the reference because it is the text in the markup.
	en := builtinStrings["en"]
	if len(en) < 40 {
		t.Fatalf("the English dictionary is implausibly small (%d keys)", len(en))
	}
	for code, keys := range builtinStrings {
		if code == "en" {
			continue
		}
		var missing, extra []string
		for k := range en {
			if _, ok := keys[k]; !ok {
				missing = append(missing, k)
			}
		}
		for k := range keys {
			if _, ok := en[k]; !ok {
				extra = append(extra, k)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("%s is missing %d of the %d guest-facing strings: %v", code, len(missing), len(en), missing)
		}
		if len(extra) > 0 {
			t.Errorf("%s translates %v, which no longer exists in English", code, extra)
		}
		// The words must actually be in that language: an entry copied verbatim from English passes a key count but reads as untranslated. Proper nouns are excluded.
		same := 0
		for k, v := range keys {
			if v == en[k] {
				same++
			}
		}
		if same > len(en)/4 {
			t.Errorf("%s repeats the English wording for %d of %d strings; that is a gap, not a translation",
				code, same, len(en))
		}
	}
}

// The page and /api/languages must serve the same words, or Hotel Admin shows wording no guest receives.
func TestTheRenderedPageCarriesTheShippedWording(t *testing.T) {
	html := renderLanding(t, "10.77.0.42", "")
	for _, l := range portalLanguages {
		if !strings.Contains(html, `"code":"`+l.Code+`"`) {
			t.Errorf("the page does not carry language %s", l.Code)
		}
		if !strings.Contains(html, l.Label) {
			t.Errorf("the page does not carry %s's native name", l.Code)
		}
	}
	// A sample from each dictionary is enough to prove the map reached the page.
	for code, key := range map[string]string{
		"ar": "pms.room", "de": "btn.submit", "fr": "voucher.label", "it": "tab.guest", "ru": "info.device",
	} {
		if want := builtinStrings[code][key]; want != "" && !strings.Contains(html, want) {
			t.Errorf("the page does not carry %s/%s (%q)", code, key, want)
		}
	}
}

func TestTheLanguagesEndpointServesWhatThePageRenders(t *testing.T) {
	h := &handler{}
	w := httptest.NewRecorder()
	h.languages(w, httptest.NewRequest(http.MethodGet, "/api/languages", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var got struct {
		Languages []portalLanguage             `json:"languages"`
		Strings   map[string]map[string]string `json:"strings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("the endpoint does not serve JSON: %v", err)
	}
	if len(got.Languages) != len(portalLanguages) {
		t.Errorf("serves %d languages, the page renders %d", len(got.Languages), len(portalLanguages))
	}
	for code, want := range builtinStrings {
		for k, v := range want {
			if got.Strings[code][k] != v {
				t.Errorf("%s/%s served as %q, rendered as %q", code, k, got.Strings[code][k], v)
				break
			}
		}
	}
}

func TestArabicIsLaidOutRightToLeft(t *testing.T) {
	// Direction follows the chosen language, and the two corner-anchored controls flip with it.
	var rtl []string
	for _, l := range portalLanguages {
		if l.RTL {
			rtl = append(rtl, l.Code)
		}
	}
	if len(rtl) != 1 || rtl[0] != "ar" {
		t.Errorf("right-to-left languages are %v; Arabic and only Arabic should be", rtl)
	}
	html := renderLanding(t, "10.77.0.42", "")
	if !strings.Contains(html, `document.documentElement.dir = isRTL(code) ? 'rtl' : 'ltr'`) {
		t.Error("the page direction does not follow the chosen language")
	}
	// A language the hotel added (Hebrew, Persian, Urdu) reads right to left in the script as the server renders it (rtlCode).
	if !strings.Contains(html, `return ['ar', 'he', 'fa', 'ur'].indexOf(`) {
		t.Error("the script does not judge a hotel-added language's direction by its code")
	}
	for _, code := range []string{"ar", "he", "fa", "ur", "fa-IR"} {
		if !rtlCode(code) {
			t.Errorf("rtlCode(%q) is false; the script treats it as right to left", code)
		}
	}
	for _, sel := range []string{`[dir="rtl"] .langbar`, `[dir="rtl"] .info-btn`} {
		if !strings.Contains(html, sel) {
			t.Errorf("%s is not mirrored, so it sits over the wrong corner in Arabic", sel)
		}
	}
}

func TestTheSelectorOffersOnlyWhatTheHotelConfigured(t *testing.T) {
	// A configured code is offered only if the portal ships words for it or the hotel published its own.
	html := renderLanding(t, "10.77.0.42", "")
	if !strings.Contains(html, "renderLanguages(Array.isArray(d.languages) && d.languages.length ? d.languages : null)") {
		t.Error("the published language list does not drive the selector")
	}
	if !strings.Contains(html, "if (!meta && !(I18N[code] && Object.keys(I18N[code]).length)) return;") {
		t.Error("a configured language with no words behind it would still be offered")
	}
}

// Automatic language selection is asserted in a browser (hotel-admin/e2e/guest-language-detection.spec.ts); here the page must still contain the rules those tests exercise.
func TestAutomaticLanguageSelectionReadsTheWholeDeviceList(t *testing.T) {
	html := renderLanding(t, "10.77.0.42", "")
	for _, want := range []struct{ frag, why string }{
		{"navigator.languages", "the device's ORDERED list, not just its first language"},
		{"push(t.split('-')[0])", "ar-EG must match a hotel that enabled ar"},
		{"function chooseLanguage(offeredCodes)", "the choice is made against what the HOTEL enabled"},
		{"var want = rememberedLanguage();", "a guest's own choice outranks detection"},
		{"if (!want || codes.indexOf(want) < 0) want = chooseLanguage(codes);", "detection runs only when there is no usable stored choice"},
		{"want = codes.indexOf('en') >= 0 ? 'en' : (codes[0] || 'en');", "English is the safe fallback"},
		{"document.cookie = LANG_KEY", "iOS captive-portal WebViews do not reliably keep localStorage"},
	} {
		if !strings.Contains(html, want.frag) {
			t.Errorf("automatic language selection is missing %s (%q)", want.why, want.frag)
		}
	}
	// Detection must leave no trace: writing the preference would record a choice the guest never made.
	if strings.Contains(html, "rememberLanguage(want)") {
		t.Error("automatic detection stores its result, which makes it indistinguishable from a guest's choice")
	}
}

func TestGeneratedTextIsTranslatedToo(t *testing.T) {
	// Script-created text carries its key so the language pass reaches it; text set once at render stays in the starting language.
	html := renderLanding(t, "10.77.0.42", "")
	for _, frag := range []struct{ code, why string }{
		{`h.dataset.i18n = 'alt.title'`, "the alternative-methods heading"},
		{`b.dataset.i18n = 'method.' + m`, "the alternative method buttons"},
		{`data-i18n="tab.' + gid + '"`, "the two group tabs"},
		{`prompt.dataset.i18n = key`, "the room sign-in prompt"},
		{`a.dataset.i18n = 'social.' + p`, "the social provider buttons"},
		{`n.dataset.i18n = 'notice.nopackages'`, "the no-packages notice"},
		{`none.dataset.i18n = 'notice.nomethods'`, "the no-sign-in-methods notice"},
		{`h.dataset.i18n = 'pms.choose'`, "the package chooser"},
	} {
		if !strings.Contains(html, frag.code) {
			t.Errorf("%s is generated without a translation key, so it stays in English", frag.why)
		}
	}
}

func TestTheRoomPromptIsNotPrintedTwice(t *testing.T) {
	// The hint was also set as the placeholder, so the guest read the same sentence twice.
	html := renderLanding(t, "10.77.0.42", "")
	if strings.Contains(html, "sec.placeholder = PMSPrompts") {
		t.Error("the room prompt is still duplicated into the field's placeholder")
	}
}

func TestLandingFallsBackToEnglishRatherThanShowingKeys(t *testing.T) {
	// A hotel that translates six strings and forgets the seventh must still get a readable portal; a raw key is worse than English.
	html := renderLanding(t, "10.77.0.42", "")
	if !strings.Contains(html, "data-i18n-en=") {
		t.Fatal("no English fallback is carried, so a partial translation would render keys")
	}
	if !strings.Contains(html, "else if (el.dataset.i18nEn)") {
		t.Error("the translation pass does not fall back to the English it carries")
	}
}

func TestPortalServesItsOwnAssets(t *testing.T) {
	// A captive portal's device has no internet, so uploads are served from the appliance.
	if portalAssetDir == "" {
		t.Fatal("no asset directory is configured")
	}
	if !strings.HasPrefix(portalAssetDir, "/opt/stayconnect/") {
		t.Errorf("assets are served from %q, outside the appliance's own tree", portalAssetDir)
	}
}

// The reference design asserted as drawn: Voucher and Personal Account share one panel, buttons say Submit/Login, labels are title case.
func TestLandingMatchesTheReferenceDesign(t *testing.T) {
	html := renderLanding(t, "10.77.0.42", "aa:bb:cc:dd:ee:ff")

	for _, want := range []struct{ frag, why string }{
		{`data-i18n-en="Room Number"`, "Room Number is title case in the reference"},
		{`id="pms-secondary"`, "the guest's second field"},
		{`data-i18n-en="Password"`, "the second field is labelled Password, not 'last name or reservation'"},
		{`id="pms-prompt"`, "the hint line beneath the password field"},
		{`data-i18n-en="Submit"`, "the guest button says Submit"},
		{`data-i18n-en="Use Personal Account"`, "the pill that toggles voucher and personal account"},
		{`data-i18n-en="Voucher Code"`, "Voucher Code is title case in the reference"},
		{`data-i18n-en="Login"`, "the account button says Login"},
		{`class="pill"`, "the pill is a pill, not a bare checkbox"},
	} {
		if !strings.Contains(html, want.frag) {
			t.Errorf("the page does not match the reference: missing %s (%q)", want.why, want.frag)
		}
	}

	// The wording the reference replaced must be gone.
	for _, gone := range []string{
		`data-i18n-en="Room number"`, `data-i18n-en="Voucher code"`, `>Connect</`,
	} {
		if strings.Contains(html, gone) {
			t.Errorf("superseded wording is still rendered: %q", gone)
		}
	}
}

func TestAccountLoginKeepsBothFormsReachable(t *testing.T) {
	// Merging two methods into one panel is where one quietly becomes unreachable: both forms and the toggle must exist.
	html := renderLanding(t, "10.77.0.42", "")
	for _, frag := range []string{
		`action="/auth/voucher"`,
		`action="/auth/credentials"`,
		`id="use-personal"`,
	} {
		if !strings.Contains(html, frag) {
			t.Errorf("account login lost %q, so one of its two ways in is unreachable", frag)
		}
	}
	// The hidden form's required fields are disabled, else the browser blocks the visible form over an invisible empty field.
	if !strings.Contains(html, "el.disabled = personal") {
		t.Error("the hidden form's required fields are not disabled; the visible form will refuse to submit")
	}
}

// The hotel's own words must reach the page: welcome_text, help_text, terms_url, custom_css and custom_html were settable but read by nothing.
func TestLandingRendersTheHotelsOwnContent(t *testing.T) {
	html := renderLanding(t, "10.77.0.42", "")
	for _, want := range []struct{ frag, why string }{
		{`id="brand-welcome"`, "the welcome line"},
		{`id="brand-help"`, "the help line"},
		{`id="brand-terms"`, "the terms link"},
		{`id="custom-html"`, "the Advanced markup fragment"},
		{`d.welcome_text`, "the welcome line is filled from the design"},
		{`d.help_text`, "the help line is filled from the design"},
		{`d.terms_url`, "the terms link is filled from the design"},
		{`d.custom_css`, "the Advanced stylesheet is applied"},
		{`d.custom_html`, "the Advanced markup is inserted"},
	} {
		if !strings.Contains(html, want.frag) {
			t.Errorf("the portal does not carry %s (%q); the setting would change nothing a guest sees", want.why, want.frag)
		}
	}
}

func TestABrokenLogoLeavesNothingBehind(t *testing.T) {
	// A removed logo or unreachable https host (every external host on a captive portal) would render a broken-image glyph.
	html := renderLanding(t, "10.77.0.42", "")
	if !strings.Contains(html, "img.onerror") {
		t.Error("a logo that fails to load is not hidden, so the sign-in page shows a broken image")
	}
}

// Translation contract, admin half: the operator's key list from the branding screen is compared with the shipped English in both directions.
func TestTheDesignerOffersExactlyTheStringsThePortalRenders(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "hotel-admin", "app", "(app)", "portal-branding", "strings.ts"))
	if err != nil {
		t.Skipf("the branding screen is not in this checkout (%v); the two lists cannot be compared", err)
	}
	offered := map[string]bool{}
	for _, m := range regexp.MustCompile(`key: "([a-z][a-zA-Z0-9.]*)"`).FindAllStringSubmatch(string(src), -1) {
		offered[m[1]] = true
	}
	if len(offered) == 0 {
		t.Fatal("no translatable strings were found in the branding screen; this test is not reading what it thinks it is")
	}
	en := builtinStrings["en"]
	var onlyPortal, onlyDesigner []string
	for k := range en {
		if !offered[k] {
			onlyPortal = append(onlyPortal, k)
		}
	}
	for k := range offered {
		if _, ok := en[k]; !ok {
			onlyDesigner = append(onlyDesigner, k)
		}
	}
	sort.Strings(onlyPortal)
	sort.Strings(onlyDesigner)
	if len(onlyPortal) > 0 {
		t.Errorf("the portal ships %v, which the branding screen never offers to translate", onlyPortal)
	}
	if len(onlyDesigner) > 0 {
		t.Errorf("the branding screen offers %v, which the portal no longer ships — an operator would translate nothing", onlyDesigner)
	}
	// The admin's English must match the page's, or every translation is made against the wrong original.
	englishRe := regexp.MustCompile(`key: "([a-z][a-zA-Z0-9.]*)", english: "([^"]*)"`)
	for _, m := range englishRe.FindAllStringSubmatch(string(src), -1) {
		want := m[2]
		if got, ok := en[m[1]]; ok && got != want {
			t.Errorf("%s reads %q on the portal and %q in the branding screen", m[1], got, want)
		}
	}
}
