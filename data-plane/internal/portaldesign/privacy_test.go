package portaldesign

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestThePrivacyNoticeIsCheckedAndServed(t *testing.T) {
	d := map[string]any{
		"privacy_text": "Who is responsible\nThe Hotel Ltd.",
		"privacy_url":  "https://hotel.example/privacy",
	}
	if errs := Errors(Validate(d)); len(errs) > 0 {
		t.Fatalf("a valid notice was refused: %v", errs)
	}
	g := ForGuests(d)
	if g["privacy_text"] != d["privacy_text"] || g["privacy_url"] != d["privacy_url"] {
		t.Fatalf("the notice did not reach guests: %v", g)
	}

	for _, bad := range []string{"javascript:alert(1)", "http://hotel.example/privacy", "//evil.example"} {
		if errs := Errors(Validate(map[string]any{"privacy_url": bad})); len(errs) == 0 {
			t.Errorf("privacy_url %q was accepted", bad)
		}
		if _, ok := ForGuests(map[string]any{"privacy_url": bad})["privacy_url"]; ok {
			t.Errorf("privacy_url %q was served to guests", bad)
		}
	}
	if errs := Errors(Validate(map[string]any{"privacy_text": strings.Repeat("x", MaxPrivacyText+1)})); len(errs) == 0 {
		t.Error("an over-long notice was accepted")
	}
}

// An unfinished template is allowed so the hotel can save as it goes, but never silently.
func TestAnUnfinishedNoticeIsAWarningNotARefusal(t *testing.T) {
	issues := Validate(map[string]any{"privacy_text": "Contact: [[privacy contact email]]. Kept for [[period]]."})
	if len(Errors(issues)) > 0 {
		t.Fatalf("an unfinished notice was refused: %v", issues)
	}
	if len(issues) != 1 || issues[0].Field != "privacy_text" || !strings.Contains(issues[0].Message, "2 part(s)") {
		t.Fatalf("issues = %v, want one warning counting two placeholders", issues)
	}
	if issues := Validate(map[string]any{"privacy_text": "A finished notice [with brackets]."}); len(issues) != 0 {
		t.Fatalf("a finished notice was flagged: %v", issues)
	}
}

// Every outside host the page would load from is named, and none blocks a save.
func TestOutsideHostsAreNamed(t *testing.T) {
	d := map[string]any{
		"logo_url":       "https://cdn.example.com/logo.png",
		"background_url": "/assets/bg.jpg",
		"custom_css":     `@font-face { font-family: X; src: url("https://Fonts.Example.net/x.woff2"); } .a { background: url(/assets/a.png) }`,
		"custom_html":    `<p><a href="https://linked.example/">a link is not a load</a><img src="https://img.example.org/a.png" alt=""></p>`,
	}
	issues := Validate(d)
	if errs := Errors(issues); len(errs) > 0 {
		t.Fatalf("outside hosts were refused: %v", errs)
	}
	got := map[string]string{}
	for _, i := range issues {
		if strings.Contains(i.Message, "outside this appliance") {
			got[i.Field] = i.Message
		}
	}
	for field, host := range map[string]string{"logo_url": "cdn.example.com", "custom_css": "fonts.example.net", "custom_html": "img.example.org"} {
		if !strings.Contains(got[field], host) {
			t.Errorf("%s: %q does not name %s", field, got[field], host)
		}
	}
	if _, ok := got["background_url"]; ok {
		t.Error("an uploaded image was reported as an outside host")
	}
	if strings.Contains(got["custom_html"], "linked.example") {
		t.Error("a link was reported as a load")
	}
}

func TestTheDesignerHasThePrivacyLimit(t *testing.T) {
	api, err := os.ReadFile(filepath.Join("..", "..", "..", "hotel-admin", "lib", "api", "portal-design.ts"))
	if err != nil {
		t.Skipf("the admin is not in this checkout (%v)", err)
	}
	m := regexp.MustCompile(`privacyText: (\d+)`).FindSubmatch(api)
	if m == nil || string(m[1]) != "8000" || MaxPrivacyText != 8000 {
		t.Errorf("the designer's privacy limit does not match MaxPrivacyText (%d)", MaxPrivacyText)
	}
}
