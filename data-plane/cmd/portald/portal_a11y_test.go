package main

// Structural WCAG 2.2 AA checks on the markup, script and stylesheet a guest receives; a screen reader and
// keyboard prove the effect, these keep the enabling parts from going away.

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// A ticking wait inside role=alert is re-announced every second: say it once (visually hidden), hide the ticking copy.
func TestTheRoomWaitIsAnnouncedOnceNotEverySecond(t *testing.T) {
	if !strings.Contains(landingHTML, `<div class="err" id="pms-err" role="alert"`) {
		t.Fatal("the room refusal is no longer an alert; this test is not reading what it thinks it is")
	}
	fn := landingHTML[strings.Index(landingHTML, "function phase3Countdown("):]
	fn = fn[:strings.Index(fn, "PHASE3_WAIT_TIMER = setInterval(render, 1000);")]
	for _, want := range []string{
		`said.className = 'sc-vh';`, `said.textContent = message;`, `shown.setAttribute('aria-hidden', 'true');`,
		`shown.textContent = tmpl.replace('%WAIT%', String(left));`, `errEl.textContent = t('err.retry');`,
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("the countdown is missing %q", want)
		}
	}
	if strings.Contains(fn, `errEl.textContent = tmpl.replace`) {
		t.Error("the countdown still rewrites the alert itself every second")
	}
}

// The guard sheet keeps focus outlines and readable colours whatever the hotel's layer says, without !important.
func TestTheGuardKeepsTheFocusRingAndTheReadableColours(t *testing.T) {
	for name, sheet := range map[string]string{"sign-in": portalGuardCSS, "pages": portalPageGuardCSS} {
		for _, want := range []string{
			`button:focus-visible`, `input:focus-visible`, `select:focus-visible`, `a:focus-visible`,
			`outline: 2px solid #14161a`,
			`textarea { color: #14161a; background-color: #fff; }`,
			`.notice--error, .err:not(:empty) { color: #8f1c13; background-color: #fdeceb; }`,
			`.err.err--ok:not(:empty) { color: #1d4f7c; background-color: #eaf2f8; }`,
		} {
			if !strings.Contains(sheet, want) {
				t.Errorf("%s guard is missing %q", name, want)
			}
		}
		if strings.Contains(sheet, "!important") {
			t.Errorf("%s guard uses !important", name)
		}
	}
	pages := guestPages(t, map[string]any{"custom_css": "a:focus { outline: none }"}, http.Header{})
	for name, html := range pages {
		if !strings.Contains(html, `outline: 2px solid #14161a`) && !strings.Contains(html, `outline:2px solid #14161a`) {
			t.Errorf("%s does not carry the focus guard", name)
		}
	}
}

func TestTheRoomSecondFieldNamesWhatItAsksFor(t *testing.T) {
	for _, want := range []string{
		`const PMSAutocomplete = { room_lastname: 'family-name', room_firstname: 'given-name' };`,
		`secondary.setAttribute('autocomplete', PMSAutocomplete[cfg.pms.mode] || 'off');`,
	} {
		if !strings.Contains(landingHTML, want) {
			t.Errorf("the room field's autocomplete is not set from the mode (missing %q)", want)
		}
	}
}

// Arrow keys through the closed select must not reload on each step.
func TestTheLanguagePillDoesNotReloadOnEveryArrowKey(t *testing.T) {
	js := guestScripts[strings.Index(guestScripts, "var sel = document.getElementById('lang');"):]
	js = js[:strings.Index(js, "A refusal the server rendered")]
	for _, want := range []string{
		`sel.addEventListener('keydown'`, `e.key === 'Enter' && pending`, `sel.addEventListener('blur', applyLang);`,
		`pending = true; if (!keyed) applyLang();`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the language pill is missing %q", want)
		}
	}
	if regexp.MustCompile(`addEventListener\('change', function \(\) \{\s*try \{ localStorage`).MatchString(js) {
		t.Error("the language pill still reloads on every change")
	}
}

func TestEveryLanguageOptionCarriesItsLanguage(t *testing.T) {
	pages := guestPages(t, map[string]any{}, http.Header{})
	for name, html := range pages {
		if !strings.Contains(html, `<select id="lang"`) {
			continue
		}
		for _, l := range portalLanguages {
			if !strings.Contains(html, `<option value="`+l.Code+`" lang="`+l.Code+`"`) {
				t.Errorf("%s: the %s option has no lang", name, l.Code)
			}
		}
	}
	if !strings.Contains(landingHTML, "o.lang = l.code;") {
		t.Error("the script that redraws the selector drops the options' lang")
	}
}

func TestNewTabLinksSaySo(t *testing.T) {
	html := renderLanding(t, "10.77.0.42", "aa:bb:cc:dd:ee:ff")
	for _, id := range []string{"privacy-url", "brand-terms"} {
		i := strings.Index(html, `id="`+id+`"`)
		if i < 0 {
			t.Fatalf("%s is missing", id)
		}
		a := html[i : i+strings.Index(html[i:], "</a>")]
		if !strings.Contains(a, `target="_blank"`) {
			t.Errorf("%s no longer opens a new tab; this test is not needed", id)
		}
		if !strings.Contains(a, `<span class="sc-vh" data-i18n="link.newtab" data-i18n-en="(opens in a new tab)">(opens in a new tab)</span>`) {
			t.Errorf("%s does not say it opens a new tab: %s", id, a)
		}
	}
	for _, l := range portalLanguages {
		if builtinStrings[l.Code]["link.newtab"] == "" {
			t.Errorf("%s has no words for a new tab", l.Code)
		}
	}
}

// The validity comes from the server's ttl_seconds, never a number in the page.
func TestTheCodeStepSaysHowLongTheCodeLastsAndOffersANewOne(t *testing.T) {
	html := renderLanding(t, "10.77.0.42", "aa:bb:cc:dd:ee:ff")
	for _, want := range []string{
		`id="sms-valid" data-otp-valid role="status" aria-live="polite"`,
		`id="wa-valid" data-otp-valid role="status" aria-live="polite"`,
		`aria-describedby="sms-valid"`, `aria-describedby="wa-valid"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the code step is missing %q", want)
		}
	}
	if n := strings.Count(html, `data-otp-again data-i18n="otp.resend" data-i18n-en="Send a new code"`); n != 2 {
		t.Errorf("%d Send-a-new-code buttons; one per code step is two", n)
	}
	for _, want := range []string{
		`otpValidity(codeForm, j.ttl_seconds, '');`, `otpValidity(codeForm, p.ttl_seconds, '');`,
		`fetch('/auth/otp/resend'`, `otpValidity(codeForm, j.ttl_seconds, 'otp.resent');`,
		`t('otp.valid').split('{n}').join(el.dataset.n)`,
	} {
		if !strings.Contains(landingHTML, want) {
			t.Errorf("the code step script is missing %q", want)
		}
	}
	for _, l := range portalLanguages {
		w := builtinStrings[l.Code]
		if !strings.Contains(w["otp.valid"], "{n}") || w["otp.resend"] == "" || w["otp.resent"] == "" {
			t.Errorf("%s is missing the code-step words", l.Code)
		}
	}
}

func TestCopyingTheAddressIsAnnounced(t *testing.T) {
	for _, want := range []string{
		`<p class="sc-vh" id="copy-status" role="status" aria-live="polite"></p>`,
		`var s = document.getElementById('copy-status');`, `s.textContent = c;`,
	} {
		if !strings.Contains(openBrowserHTML, want) {
			t.Errorf("the copy button's confirmation is missing %q", want)
		}
	}
}

func TestWelcomeBackIsNotALiveRegion(t *testing.T) {
	i := strings.Index(landingHTML, `id="welcome"`)
	tag := landingHTML[i : i+strings.Index(landingHTML[i:], ">")]
	if strings.Contains(tag, "role=") {
		t.Errorf("the welcome-back block is still a live region: %s", tag)
	}
}

func TestPressableControlsHaveAVisibleEdge(t *testing.T) {
	for _, sel := range []string{"  .pill {", "  .alt button.link {", "  button.choice {"} {
		i := strings.Index(portalBaseCSS, sel)
		if i < 0 {
			t.Fatalf("%q is missing", sel)
		}
		rule := portalBaseCSS[i : i+strings.Index(portalBaseCSS[i:], "}")]
		if !strings.Contains(rule, "border: 1px solid var(--sc-edge)") {
			t.Errorf("%s does not draw its edge in --sc-edge: %s", strings.TrimSpace(sel), rule)
		}
	}
	edge, _ := parseColour("#7f8795")
	if c := contrast(edge, white); c < 3 {
		t.Errorf("--sc-edge is %.2f:1 on the card", c)
	}
	recess, _ := parseColour("#f6f7f9")
	if c := contrast(edge, recess); c < 3 {
		t.Errorf("--sc-edge is %.2f:1 on the switch row's recess", c)
	}
	if !strings.Contains(portalBaseCSS, ".sc-foot {\n    display: flex; flex-wrap: wrap; align-items: center;\n    margin-top: 24px; padding-top: 6px; border-top: 1px solid var(--sc-line);") {
		t.Error("the footer divider changed; dividers keep --sc-line")
	}
}
