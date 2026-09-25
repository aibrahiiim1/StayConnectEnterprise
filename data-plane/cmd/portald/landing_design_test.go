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
		{`max-width: 680px`, "the phone layout"},
		{`class="page"`, "the column wrapper; a flex ROW put the language bar BESIDE the card on a phone"},
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
	if !strings.Contains(strings.ToLower(html), "not detected") {
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
	// Voucher and personal account now share one panel behind the "Use Personal Account" pill, as the
	// reference design presents them -- so the claim is asserted on the FORMS, which is what actually carries
	// a sign-in method, rather than on a panel id that the design legitimately changed.
	for _, id := range []string{
		"form-voucher", "form-credentials", "panel-email", "panel-sms", "panel-pms", "panel-social",
	} {
		if !strings.Contains(html, id) {
			t.Errorf("%s is no longer rendered; restyling the portal must not remove a sign-in method", id)
		}
	}
	// And the pill that chooses between the two must exist, or one of them is unreachable.
	if !strings.Contains(html, `id="use-personal"`) {
		t.Error("the Use Personal Account toggle is missing, so one of the two account forms cannot be reached")
	}
}

// THE TRANSLATION CONTRACT, PINNED ON BOTH SIDES.
//
// The language selector is driven by keys: the portal tags each guest-facing string with data-i18n, and Hotel
// Admin offers the operator a field per key. If the two lists drift, an operator types words that never
// appear on the page and has no way to discover why. This asserts the portal's half.
func TestLandingTagsEveryStringTheDesignerOffersToTranslate(t *testing.T) {
	html := renderLanding(t, "10.77.0.42", "aa:bb:cc:dd:ee:ff")

	// Kept in step with PORTAL_STRINGS in hotel-admin/app/(app)/portal-branding/page.tsx.
	for _, key := range []string{
		"pms.room", "account.pass", "account.user", "voucher.label",
		"email.dest", "sms.dest", "otp.code",
		"btn.submit", "btn.login", "btn.verify",
		"info.device", "info.ip", "info.mac", "info.help",
	} {
		if !strings.Contains(html, `data-i18n="`+key+`"`) {
			t.Errorf("the portal never tags %q, so translating it changes nothing a guest sees", key)
		}
	}
	// The two group tabs are translated by re-rendering rather than by a data attribute, so their keys are
	// asserted in the script instead.
	for _, key := range []string{"tab.guest", "tab.account"} {
		if !strings.Contains(html, "'tab.' + gid") && !strings.Contains(html, key) {
			t.Errorf("group tab key %q is not reachable by a translation", key)
		}
	}
}

// THE SIX LANGUAGES ARE PART OF THE BUILD, NOT A DATA-ENTRY TASK.
//
// A language selector whose other five entries are empty until somebody types four dozen strings is the same
// broken promise as a selector that changes nothing. These read the shipped wording as DATA -- the same map
// the page renders and /api/languages serves -- rather than scraping it back out of the HTML, which is what
// they had to do while it existed only as a JavaScript literal inside the template.
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
		// Offered under its own name. "RU" is not a word a Russian speaker is looking for.
		if l.Label != w.native {
			t.Errorf("%s is offered as %q, want its native name %q", w.code, l.Label, w.native)
		}
		if len(builtinStrings[w.code]) == 0 {
			t.Errorf("%s is offered with no wording behind it", w.code)
		}
	}
}

func TestEveryShippedLanguageIsComplete(t *testing.T) {
	// A language missing a key silently renders English in the middle of a Russian sentence. English is the
	// reference because it is also the text sitting in the markup.
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
		// AND THE WORDS MUST ACTUALLY BE IN THAT LANGUAGE. An entry copied verbatim from English is a gap
		// wearing a translation's clothes -- it passes a key-count check and reads as untranslated on the
		// page. Proper nouns and the like are excluded because they are legitimately identical.
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

// THE PAGE AND THE ENDPOINT MUST SERVE THE SAME WORDS.
//
// Two consumers of one map is the whole point of moving it out of the template. If the page ever rendered
// something other than what /api/languages reports, Hotel Admin would be showing an operator wording no guest
// receives -- which is the defect this replaced, in a new place.
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
	// A sample from each dictionary, rather than all 306: enough to prove the map reached the page.
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
	// Translating the words and leaving the page left-aligned is half a translation. The direction follows the
	// chosen language, and the two corner-anchored controls flip with it.
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
	if !strings.Contains(html, `document.documentElement.dir = (meta && meta.rtl) ? 'rtl' : 'ltr'`) {
		t.Error("the page direction does not follow the chosen language")
	}
	for _, sel := range []string{`[dir="rtl"] .langbar`, `[dir="rtl"] .info-btn`} {
		if !strings.Contains(html, sel) {
			t.Errorf("%s is not mirrored, so it sits over the wrong corner in Arabic", sel)
		}
	}
}

func TestTheSelectorOffersOnlyWhatTheHotelConfigured(t *testing.T) {
	// The rule that stops the selector promising a language nothing stands behind: a configured code is offered
	// only if the portal ships words for it OR the hotel published its own.
	html := renderLanding(t, "10.77.0.42", "")
	if !strings.Contains(html, "renderLanguages(Array.isArray(d.languages) && d.languages.length ? d.languages : null)") {
		t.Error("the published language list does not drive the selector")
	}
	if !strings.Contains(html, "if (!meta && !(I18N[code] && Object.keys(I18N[code]).length)) return;") {
		t.Error("a configured language with no words behind it would still be offered")
	}
}

// AUTOMATIC LANGUAGE SELECTION.
//
// The behaviour is asserted for real in a browser -- see hotel-admin/e2e/guest-language-detection.spec.ts,
// which drives the actual page under a dozen device locale lists including an iPhone profile. What is
// asserted HERE is that the page still contains the rules those tests exercise, because each one replaced a
// specific way of getting this wrong.
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
	// A DETECTION MUST LEAVE NO TRACE. If detection wrote the preference, the first load would record a
	// "choice" the guest never made, and every later load would defer to it -- including after the hotel
	// changed which languages it offers.
	if strings.Contains(html, "rememberLanguage(want)") {
		t.Error("automatic detection stores its result, which makes it indistinguishable from a guest's choice")
	}
}

func TestGeneratedTextIsTranslatedToo(t *testing.T) {
	// Half the portal's words are created by script -- tabs, alternative methods, the package chooser, the
	// site notices. Text set once at render time and never re-read is text that stays in the language the page
	// started in. Each generated element carries its key so the language pass reaches it.
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
	// It was set as the hint under the field AND as the field's own placeholder, so the guest read the same
	// sentence twice, the second copy sitting exactly where their answer goes.
	html := renderLanding(t, "10.77.0.42", "")
	if strings.Contains(html, "sec.placeholder = PMSPrompts") {
		t.Error("the room prompt is still duplicated into the field's placeholder")
	}
}

func TestLandingFallsBackToEnglishRatherThanShowingKeys(t *testing.T) {
	// A hotel that translates six strings and forgets the seventh must get a portal that still reads. Showing
	// a raw key is worse than showing English: it is not a language anybody speaks.
	html := renderLanding(t, "10.77.0.42", "")
	if !strings.Contains(html, "data-i18n-en=") {
		t.Fatal("no English fallback is carried, so a partial translation would render keys")
	}
	if !strings.Contains(html, "else if (el.dataset.i18nEn)") {
		t.Error("the translation pass does not fall back to the English it carries")
	}
}

func TestPortalServesItsOwnAssets(t *testing.T) {
	// A captive portal is reached by a device with NO internet. An image hosted anywhere else is an image
	// that fails exactly when it matters, which is why uploads are served from the appliance.
	if portalAssetDir == "" {
		t.Fatal("no asset directory is configured")
	}
	if !strings.HasPrefix(portalAssetDir, "/opt/stayconnect/") {
		t.Errorf("assets are served from %q, outside the appliance's own tree", portalAssetDir)
	}
}

// THE REFERENCE DESIGN, ASSERTED AS THE PRODUCT OWNER DREW IT.
//
// The first attempt at this page was styled like the screenshots but structured like the old portal: Voucher
// and Personal Account were two separate sign-in methods, the buttons said "Connect", and the labels were
// sentence case. It looked close and behaved differently, which is the failure mode a screenshot is supposed
// to prevent. These assert the specifics rather than the impression.
func TestLandingMatchesTheReferenceDesign(t *testing.T) {
	html := renderLanding(t, "10.77.0.42", "aa:bb:cc:dd:ee:ff")

	// Guest Login: Room Number, Password, a hint beneath it, and Submit.
	for _, want := range []struct{ frag, why string }{
		{`data-i18n-en="Room Number"`, "Room Number is title case in the reference"},
		{`id="pms-secondary"`, "the guest's second field"},
		{`data-i18n-en="Password"`, "the second field is labelled Password, not 'last name or reservation'"},
		{`id="pms-prompt"`, "the hint line beneath the password field"},
		{`data-i18n-en="Submit"`, "the guest button says Submit"},
		// Account Login: the pill, Voucher Code, and Login.
		{`data-i18n-en="Use Personal Account"`, "the pill that toggles voucher and personal account"},
		{`data-i18n-en="Voucher Code"`, "Voucher Code is title case in the reference"},
		{`data-i18n-en="Login"`, "the account button says Login"},
		{`class="pill"`, "the pill is a pill, not a bare checkbox"},
	} {
		if !strings.Contains(html, want.frag) {
			t.Errorf("the page does not match the reference: missing %s (%q)", want.why, want.frag)
		}
	}

	// The wording the reference REPLACED must be gone, or the page carries both vocabularies.
	for _, gone := range []string{
		`data-i18n-en="Room number"`, `data-i18n-en="Voucher code"`, `>Connect</`,
	} {
		if strings.Contains(html, gone) {
			t.Errorf("superseded wording is still rendered: %q", gone)
		}
	}
}

func TestAccountLoginKeepsBothFormsReachable(t *testing.T) {
	// Merging two sign-in methods into one panel is exactly where one of them quietly stops being reachable.
	// Both forms must be present, each with its own action, and the toggle that selects between them.
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
	// The hidden form's required fields are disabled when it is hidden — otherwise the browser blocks
	// submission of the VISIBLE form because an invisible field elsewhere is empty and required.
	if !strings.Contains(html, "el.disabled = personal") {
		t.Error("the hidden form's required fields are not disabled; the visible form will refuse to submit")
	}
}

// THE HOTEL'S OWN WORDS MUST REACH THE PAGE.
//
// welcome_text, help_text, terms_url, custom_css and custom_html were all settable in Hotel Admin and read by
// NOTHING. Five fields an operator could fill in that changed nothing a guest saw -- the same write-only
// configuration the branding screen was built to replace, sitting inside the replacement.
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
	// A logo whose file has been removed, or whose https host is unreachable -- which on a captive portal is
	// every external host -- otherwise renders as a broken-image glyph on every guest device.
	html := renderLanding(t, "10.77.0.42", "")
	if !strings.Contains(html, "img.onerror") {
		t.Error("a logo that fails to load is not hidden, so the sign-in page shows a broken image")
	}
}

// THE TRANSLATION CONTRACT, PINNED ON BOTH SIDES.
//
// The portal decides what strings exist; Hotel Admin offers an operator a field per string. If the two lists
// drift, an operator either types words that never appear or cannot change words that do. This reads the
// operator's list out of the branding screen and compares it with the shipped English, in both directions.
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
	// AND THE ENGLISH MUST MATCH, not merely the keys. The admin shows its own English as the reference an
	// operator compares a translation against; if it says something different from what the page renders,
	// every translation is made against the wrong original.
	englishRe := regexp.MustCompile(`key: "([a-z][a-zA-Z0-9.]*)", english: "([^"]*)"`)
	for _, m := range englishRe.FindAllStringSubmatch(string(src), -1) {
		want := m[2]
		if got, ok := en[m[1]]; ok && got != want {
			t.Errorf("%s reads %q on the portal and %q in the branding screen", m[1], got, want)
		}
	}
}
