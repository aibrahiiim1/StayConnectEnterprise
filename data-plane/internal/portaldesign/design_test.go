package portaldesign

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func errorsFor(d map[string]any) []Issue { return Errors(Validate(d)) }

func TestTermsURLIsALinkAGuestCanSafelyFollow(t *testing.T) {
	// It used to be copied into a live <a href> without any check at all.
	for _, bad := range []string{"javascript:alert(1)", "http://hotel.example/terms", "//evil.example/", "data:text/html,x",
		"https://" + strings.Repeat("a", MaxURLLength) + ".example/"} {
		if len(errorsFor(map[string]any{"terms_url": bad})) == 0 {
			t.Errorf("terms_url %q was accepted", clip(bad))
		}
	}
	for _, good := range []string{"https://hotel.example/terms", "/assets/terms.pdf", ""} {
		if errs := errorsFor(map[string]any{"terms_url": good}); len(errs) > 0 {
			t.Errorf("terms_url %q refused: %v", good, errs)
		}
	}
}

func TestTextFieldsAreBounded(t *testing.T) {
	for field, max := range map[string]int{"hotel_name": MaxHotelName, "welcome_text": MaxWelcomeText, "help_text": MaxHelpText} {
		if errs := errorsFor(map[string]any{field: strings.Repeat("é", max)}); len(errs) > 0 {
			t.Errorf("%s at exactly its limit (in characters, not bytes) was refused: %v", field, errs)
		}
		if len(errorsFor(map[string]any{field: strings.Repeat("a", max+1)})) == 0 {
			t.Errorf("%s over its limit was accepted", field)
		}
		if len(errorsFor(map[string]any{field: 42})) == 0 {
			t.Errorf("%s as a number was accepted", field)
		}
	}
}

func TestTemplatesAreAClosedList(t *testing.T) {
	if len(Templates) < 5 {
		t.Fatalf("the portal ships %d templates; at least five were asked for", len(Templates))
	}
	seen := map[string]bool{}
	for _, tp := range Templates {
		if seen[tp.ID] {
			t.Errorf("template %s is listed twice", tp.ID)
		}
		seen[tp.ID] = true
		if errs := errorsFor(map[string]any{"template_id": tp.ID}); len(errs) > 0 {
			t.Errorf("template %s refused: %v", tp.ID, errs)
		}
	}
	if Templates[0].ID != DefaultTemplate || DefaultTemplate != "classic" {
		t.Error("the default template must be the original layout, so an upgrade changes nothing a guest sees")
	}
	for _, bad := range []any{"brutalist", "classic;}", 3} {
		if len(errorsFor(map[string]any{"template_id": bad})) == 0 {
			t.Errorf("template_id %v was accepted", bad)
		}
	}
	if TemplateID(map[string]any{}) != "classic" || TemplateID(map[string]any{"template_id": "nope"}) != "classic" {
		t.Error("an absent or unknown template must render as classic")
	}
}

func TestTemplateOptionsAreClosedVocabularies(t *testing.T) {
	ok := map[string]any{"overlay": json.Number("40"), "panel_position": "end", "density": "compact",
		"hero_height": "tall", "surface": "glass", "heading_font": `"Playfair Display", Georgia, serif`}
	if errs := errorsFor(map[string]any{"template_options": ok}); len(errs) > 0 {
		t.Fatalf("ordinary options refused: %v", errs)
	}
	for _, bad := range []map[string]any{
		{"overlay": json.Number("95")}, {"overlay": json.Number("12.5")}, {"overlay": "40"},
		{"panel_position": "top; background:url(x)"}, {"density": "huge"},
		{"heading_font": "x; } body { display:none"}, {"heading_font": "url(https://x)"},
		{"custom_css": ".x{}"}, // an option that tried to be a stylesheet
	} {
		if len(errorsFor(map[string]any{"template_options": bad})) == 0 {
			t.Errorf("option %v was accepted", bad)
		}
	}
	if len(errorsFor(map[string]any{"template_options": "compact"})) == 0 {
		t.Error("template_options as a string was accepted")
	}
}

func TestLanguagesAndTranslationsAreBounded(t *testing.T) {
	var langs []any
	for i := 0; i < MaxLanguages+1; i++ {
		langs = append(langs, map[string]any{"code": "en", "label": "English"})
	}
	if len(errorsFor(map[string]any{"languages": langs})) == 0 {
		t.Error("an unbounded language list was accepted")
	}
	if len(errorsFor(map[string]any{"languages": []any{map[string]any{"code": "<b>", "label": "x"}}})) == 0 {
		t.Error("a language code that is not a code was accepted")
	}
	if len(errorsFor(map[string]any{"translations": map[string]any{"it": map[string]any{"pms.room": strings.Repeat("x", MaxTranslationText+1)}}})) == 0 {
		t.Error("an unbounded translation was accepted")
	}
	good := map[string]any{
		"languages":    []any{map[string]any{"code": "en", "label": "English"}, map[string]any{"code": "pt-br", "label": "Português"}},
		"translations": map[string]any{"it": map[string]any{"pms.room": "Camera n."}},
	}
	if errs := errorsFor(good); len(errs) > 0 {
		t.Errorf("ordinary languages refused: %v", errs)
	}
}

// THE STEP-UP LIST IS THE MARKUP LIST. Product-Owner decision: changing custom CSS or HTML asks for the
// operator's password, and nothing else does. Any field that can carry markup or a stylesheet belongs here; a
// field that cannot must not, or every ordinary save turns into a password prompt.
func TestAdvancedFieldsAreExactlyTheMarkupFields(t *testing.T) {
	if strings.Join(AdvancedFields, ",") != "custom_css,custom_html" {
		t.Fatalf("AdvancedFields = %v; if a new field carries markup or CSS, add it here AND to ADVANCED_KEYS in "+
			"hotel-admin/app/(app)/portal-branding/strings.ts, then update this test", AdvancedFields)
	}
	// Template choice and options are NOT markup: they are closed vocabularies, proven above.
	for _, k := range []string{"template_id", "template_options", "hero_image_url"} {
		for _, a := range AdvancedFields {
			if a == k {
				t.Errorf("%s is not markup and must not require the step-up", k)
			}
		}
	}
}

func TestForGuestsServesOnlyWhatIsSafe(t *testing.T) {
	stored := map[string]any{
		"hotel_name":       "Coral Sea",
		"brand_color":      "red; background:url(//evil)",
		"logo_url":         "javascript:alert(1)",
		"background_url":   "/assets/bg.jpg",
		"terms_url":        "javascript:alert(1)",
		"template_id":      "split",
		"template_options": map[string]any{"overlay": json.Number("30"), "density": "huge", "surface": "glass"},
		"custom_html":      `<p>Pool</p><img src=x onerror=alert(1)><base href="https://evil/">`,
		"custom_css":       `form { display: none !important } @import url(//evil/x.css);`,
		"draft_secret":     "never served",
	}
	g := ForGuests(stored)
	if g["hotel_name"] != "Coral Sea" || g["background_url"] != "/assets/bg.jpg" || g["template_id"] != "split" {
		t.Errorf("valid fields were not served: %v", g)
	}
	for _, k := range []string{"brand_color", "logo_url", "terms_url", "draft_secret"} {
		if _, ok := g[k]; ok {
			t.Errorf("%s was served although it is invalid or unknown: %v", k, g[k])
		}
	}
	opts := g["template_options"].(map[string]any)
	if opts["overlay"] != 30 || opts["surface"] != "glass" || opts["density"] != nil {
		t.Errorf("template options were not filtered: %v", opts)
	}
	h := g["custom_html"].(string)
	if !strings.Contains(h, "<p>Pool</p>") || strings.Contains(h, "onerror") || strings.Contains(h, "<base") {
		t.Errorf("custom HTML served unsanitised: %q", h)
	}
	css := g["custom_css"].(string)
	if strings.Contains(css, "important") || strings.Contains(css, "import") {
		t.Errorf("custom CSS served unsanitised: %q", css)
	}
	if ForGuests(nil)["template_id"] != nil && len(ForGuests(nil)) != 0 {
		t.Error("a nil design should serve nothing")
	}
}

// THE DESIGNER AND THE PORTAL AGREE ON THE LAYOUTS AND ON WHAT IS "ADVANCED".
//
// Hotel Admin offers the template gallery from its own list and asks for the password from its own list of
// advanced keys. If either drifted from this package, the designer would offer a layout the server refuses, or
// -- worse -- save a new markup field without the step-up the server would have required for it.
func TestTheDesignerMirrorsThisPackage(t *testing.T) {
	root := filepath.Join("..", "..", "..", "hotel-admin")
	api, err := os.ReadFile(filepath.Join(root, "lib", "api", "portal-design.ts"))
	if err != nil {
		t.Skipf("the admin is not in this checkout (%v)", err)
	}
	var ids []string
	for _, m := range regexp.MustCompile(`\{ id: "([a-z]+)", name: "`).FindAllStringSubmatch(string(api), -1) {
		ids = append(ids, m[1])
	}
	var want []string
	for _, tp := range Templates {
		want = append(want, tp.ID)
	}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("the designer offers templates %v; the portal has %v", ids, want)
	}

	strs, err := os.ReadFile(filepath.Join(root, "app", "(app)", "portal-branding", "strings.ts"))
	if err != nil {
		t.Fatalf("strings.ts: %v", err)
	}
	m := regexp.MustCompile(`ADVANCED_KEYS: \(keyof Design\)\[\] = \[([^\]]*)\]`).FindStringSubmatch(string(strs))
	if m == nil {
		t.Fatal("ADVANCED_KEYS not found in strings.ts")
	}
	var keys []string
	for _, k := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(m[1], -1) {
		keys = append(keys, k[1])
	}
	if strings.Join(keys, ",") != strings.Join(AdvancedFields, ",") {
		t.Errorf("the designer asks for the password on %v; the server on %v", keys, AdvancedFields)
	}
}
