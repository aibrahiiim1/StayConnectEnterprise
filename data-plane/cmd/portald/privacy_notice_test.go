package main

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

// GDPR plan F1 and F7: the hotel's notice is on the sign-in card, readable before signing in, as text (never
// markup), and the remembered-device cookie is explained.
func TestTheSignInPageCarriesThePrivacyNotice(t *testing.T) {
	tmpl, err := template.New("land").Parse(landingHTML)
	if err != nil {
		t.Fatal(err)
	}
	d := portaldesign.ForGuests(map[string]any{
		"privacy_text": "Who is responsible\n<script>alert(1)</script> The Hotel",
		"privacy_url":  "https://hotel.example/privacy",
	})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, newLandingView(r, d, "nonce", "", "", "")); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{
		`id="privacy-notice"`, "How we use your data", "Who is responsible",
		`href="https://hotel.example/privacy"`, `id="privacy-remember"`, "d.privacy_text",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the sign-in page does not carry %q", want)
		}
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Error("the notice was rendered as markup")
	}
	// The notice sits inside the sign-in card, after the forms and before the footer.
	card, privacy, foot := strings.Index(html, `<main class="card">`), strings.Index(html, `id="privacy"`), strings.Index(html, `class="sc-foot"`)
	if !(card < privacy && privacy < foot) {
		t.Errorf("the notice is not on the sign-in card (card %d, notice %d, footer %d)", card, privacy, foot)
	}
}

// GDPR audit 2026-10-19, A5: a hotel that wrote no notice still shows guests a short built-in one, and an
// empty link is not shown. The hotel's own text, once written, replaces the built-in one.
func TestNoHotelNoticeShowsTheBuiltInOne(t *testing.T) {
	tmpl, _ := template.New("land").Parse(landingHTML)
	render := func(d map[string]any) string {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, newLandingView(httptest.NewRequest(http.MethodGet, "/", nil), d, "n", "", "", "")); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	html := render(map[string]any{})
	if strings.Contains(html, `id="privacy-notice" hidden`) || strings.Contains(html, `id="privacy-default" hidden`) ||
		!strings.Contains(html, "which is responsible for your personal data") {
		t.Error("with no hotel notice, the built-in notice is not shown")
	}
	if !strings.Contains(html, `hidden
         data-i18n="privacy.url"`) {
		t.Error("an empty notice link is shown")
	}
	html = render(portaldesign.ForGuests(map[string]any{"privacy_text": "The Hotel's own notice"}))
	if !strings.Contains(html, `id="privacy-default" hidden`) || !strings.Contains(html, "The Hotel&#39;s own notice") {
		t.Error("the hotel's notice does not replace the built-in one")
	}
}

func TestEveryLanguageHasThePrivacyWords(t *testing.T) {
	for _, l := range portalLanguages {
		for k := range privacyStrings["en"] {
			if builtinStrings[l.Code][k] == "" {
				t.Errorf("%s has no %s", l.Code, k)
			}
		}
	}
}
