package portaldesign

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// THE DESIGN DOCUMENT, FIELD BY FIELD.
//
// A design is a flat JSON object stored in tenants.branding. It has no schema in the database -- new keys are
// stored, versioned and served without a migration -- so THIS is its schema: every key the portal will act on,
// with the rule it must meet. A key that is not listed here is kept in the document (older designs may carry
// keys a later release stopped using) but is never handed to a guest: ForGuests copies known keys only.

// Size limits. Each is generous for its purpose and small enough that the document, which every guest page
// load reads, stays small.
const (
	MaxAdvancedBytes    = 64 * 1024  // custom_css, custom_html
	MaxHotelName        = 120        // characters
	MaxWelcomeText      = 280        // one or two sentences under the name
	MaxHelpText         = 600        // a short paragraph at the foot of the page
	MaxURLLength        = 2048       // https / appliance paths
	MaxInlineImageBytes = 256 * 1024 // a data:image/… URI, from before uploads existed
	MaxLanguages        = 16
	MaxTranslationKeys  = 120
	MaxTranslationText  = 500
	MaxLanguageLabel    = 40
	MaxFontStack        = 200

	// MaxBodyBytes caps a request that carries a design. It is the sum of the field limits above at their
	// worst -- both advanced fields at 64 KB with JSON escaping doubling them, three inline images, sixteen
	// fully translated languages -- rounded up, so no design that would validate is refused for size.
	MaxBodyBytes = 2 << 20
)

// Template is one of the portal's built-in page layouts.
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// DefaultTemplate is what a design with no template_id renders as: the layout every appliance had before
// templates existed, so an upgrade changes nothing a guest sees until an operator chooses otherwise.
const DefaultTemplate = "classic"

// Templates are the layouts the portal ships, in the order the designer offers them. The CSS for each lives
// in portald's own template (trusted code), never in a hotel's design.
var Templates = []Template{
	{"classic", "Classic", "A centred card over your photograph. The original layout."},
	{"split", "Split", "Your photograph and welcome on one side, sign-in on the other. Stacks on a phone."},
	{"immersive", "Immersive", "A full-screen photograph with a frosted-glass sign-in panel and large type."},
	{"headerbar", "Header bar", "A business layout: top bar with your logo, sign-in beside a help column, terms in a footer."},
	{"editorial", "Resort", "A tall banner with your welcome as the headline, the sign-in card overlapping it, your content below."},
	{"kiosk", "Kiosk", "No imagery and large controls, for a lobby tablet or a guest in a hurry."},
}

// TemplateID is the design's template, or the default when it names none or one this build does not have.
func TemplateID(d map[string]any) string {
	if v, ok := d["template_id"].(string); ok {
		for _, t := range Templates {
			if t.ID == v {
				return v
			}
		}
	}
	return DefaultTemplate
}

// Template options: small, closed vocabularies that templates translate into their own CSS. None of them can
// carry markup or a stylesheet, which is why changing them does not ask for the Advanced step-up.
var templateOptionEnums = map[string][]string{
	"panel_position": {"start", "center", "end"},
	"density":        {"compact", "comfortable", "spacious"},
	"hero_height":    {"short", "medium", "tall"},
	"surface":        {"solid", "glass"},
}

var (
	reColor      = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$|^rgb\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*\)$`)
	reLength     = regexp.MustCompile(`^\d{1,3}(px|rem|em|%)$`)
	reFontStack  = regexp.MustCompile(`^[\p{L}\p{N} ,'"._-]*$`)
	reLangCode   = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})?$`)
	reStringKey  = regexp.MustCompile(`^[a-z][a-zA-Z0-9._-]{0,63}$`)
	colourFields = []string{"brand_color", "brand_color_dark", "text_color"}
	imageFields  = []string{"logo_url", "background_url", "hero_image_url"}
)

// ImageFields are the design keys that name an image, so the asset store can tell what is in use.
func ImageFields() []string { return append([]string(nil), imageFields...) }

// AdvancedFields are the keys that carry markup or a stylesheet. Changing any of them -- including clearing
// it -- requires the operator's password (the Product Owner's rule for this page). A new field that can carry
// markup or CSS MUST be added here, and to ADVANCED_KEYS in the Hotel Admin designer, or the step-up is
// silently bypassed for it; TestAdvancedFieldsAreExactlyTheMarkupFields pins the list.
var AdvancedFields = []string{"custom_css", "custom_html"}

// Validate checks a whole design and returns every finding. Errors make the design unsaveable.
func Validate(d map[string]any) []Issue {
	var out []Issue
	add := func(field, format string, a ...any) {
		out = append(out, Issue{Field: field, Message: fmt.Sprintf(format, a...), Severity: SeverityError})
	}
	text := func(k string, max int) {
		v, present := d[k]
		if !present || v == nil {
			return
		}
		s, ok := v.(string)
		if !ok {
			add(k, "must be text")
			return
		}
		if n := utf8.RuneCountInString(s); n > max {
			add(k, "is %d characters long; the limit is %d", n, max)
		}
		if strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 && r != '\n' && r != '\t' }) {
			add(k, "contains a control character")
		}
	}
	str := func(k string) (string, bool) {
		v, present := d[k]
		if !present || v == nil {
			return "", false
		}
		s, ok := v.(string)
		if !ok {
			add(k, "must be text")
			return "", false
		}
		return s, s != ""
	}

	text("hotel_name", MaxHotelName)
	text("welcome_text", MaxWelcomeText)
	text("help_text", MaxHelpText)

	for _, k := range colourFields {
		if v, ok := str(k); ok && !reColor.MatchString(v) {
			add(k, "must be a hex or rgb() colour")
		}
	}
	if v, ok := str("corner_radius"); ok && !reLength.MatchString(v) {
		add("corner_radius", "must be a length such as 18px")
	}
	if v, ok := str("font_family"); ok {
		if msg := fontStackProblem(v); msg != "" {
			add("font_family", "%s", msg)
		}
	}
	for _, k := range imageFields {
		if v, ok := str(k); ok {
			if msg := imageURLProblem(v); msg != "" {
				add(k, "%s", msg)
			}
		}
	}
	if v, ok := str("terms_url"); ok {
		if len(v) > MaxURLLength {
			add("terms_url", "is longer than %d characters", MaxURLLength)
		} else if _, err := CheckLinkURL(v); err != nil {
			add("terms_url", "%s", err)
		}
	}
	if v, present := d["template_id"]; present && v != nil {
		s, ok := v.(string)
		switch {
		case !ok:
			add("template_id", "must be text")
		case s != "" && TemplateID(d) != s:
			add("template_id", "%q is not one of the portal's templates", clip(s))
		}
	}
	if v, present := d["template_options"]; present && v != nil {
		out = append(out, validateTemplateOptions(v)...)
	}
	if v, present := d["languages"]; present && v != nil {
		out = append(out, validateLanguages(v)...)
	}
	if v, present := d["translations"]; present && v != nil {
		out = append(out, validateTranslations(v)...)
	}
	for _, k := range AdvancedFields {
		v, present := d[k]
		if !present || v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			add(k, "must be text")
			continue
		}
		if len(s) > MaxAdvancedBytes {
			add(k, "is larger than %d KB", MaxAdvancedBytes/1024)
			continue
		}
		if k == "custom_css" {
			_, issues := SanitizeCSS(s)
			out = append(out, issues...)
		} else {
			_, issues := SanitizeHTML(s)
			out = append(out, issues...)
		}
	}
	return out
}

func fontStackProblem(v string) string {
	if utf8.RuneCountInString(v) > MaxFontStack {
		return fmt.Sprintf("is longer than %d characters", MaxFontStack)
	}
	if !reFontStack.MatchString(v) {
		return "may contain only font names, quotes and commas"
	}
	return ""
}

func imageURLProblem(v string) string {
	limit := MaxURLLength
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "data:") {
		limit = MaxInlineImageBytes
	}
	if len(v) > limit {
		return fmt.Sprintf("is larger than %d KB; upload the image instead", limit/1024)
	}
	if _, err := CheckImageURL(v); err != nil {
		return err.Error()
	}
	return ""
}

func validateTemplateOptions(v any) []Issue {
	var out []Issue
	add := func(format string, a ...any) {
		out = append(out, Issue{Field: "template_options", Message: fmt.Sprintf(format, a...), Severity: SeverityError})
	}
	m, ok := v.(map[string]any)
	if !ok {
		add("must be an object")
		return out
	}
	for _, k := range sortedKeys(m) {
		val := m[k]
		if val == nil {
			continue
		}
		if allowed, isEnum := templateOptionEnums[k]; isEnum {
			s, _ := val.(string)
			if !contains(allowed, s) {
				add("%s must be one of %s", k, strings.Join(allowed, ", "))
			}
			continue
		}
		switch k {
		case "overlay":
			if n, ok := number(val); !ok || n < 0 || n > 90 || n != math.Trunc(n) {
				add("overlay must be a whole number from 0 to 90")
			}
		case "heading_font":
			s, ok := val.(string)
			if !ok {
				add("heading_font must be text")
			} else if msg := fontStackProblem(s); msg != "" {
				add("heading_font %s", msg)
			}
		default:
			add("%q is not a template option", clip(k))
		}
	}
	return out
}

func validateLanguages(v any) []Issue {
	var out []Issue
	add := func(format string, a ...any) {
		out = append(out, Issue{Field: "languages", Message: fmt.Sprintf(format, a...), Severity: SeverityError})
	}
	list, ok := v.([]any)
	if !ok {
		add("must be a list")
		return out
	}
	if len(list) > MaxLanguages {
		add("lists %d languages; the limit is %d", len(list), MaxLanguages)
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			add("each language must have a code and a label")
			continue
		}
		code, _ := m["code"].(string)
		if !reLangCode.MatchString(code) {
			add("%q is not a language code such as en or pt-br", clip(code))
		}
		if label, present := m["label"]; present && label != nil {
			s, ok := label.(string)
			if !ok || utf8.RuneCountInString(s) > MaxLanguageLabel {
				add("the label for %s must be text of at most %d characters", clip(code), MaxLanguageLabel)
			}
		}
	}
	return out
}

func validateTranslations(v any) []Issue {
	var out []Issue
	add := func(format string, a ...any) {
		out = append(out, Issue{Field: "translations", Message: fmt.Sprintf(format, a...), Severity: SeverityError})
	}
	m, ok := v.(map[string]any)
	if !ok {
		add("must be an object of languages")
		return out
	}
	if len(m) > MaxLanguages {
		add("covers %d languages; the limit is %d", len(m), MaxLanguages)
	}
	for _, code := range sortedKeys(m) {
		if !reLangCode.MatchString(code) {
			add("%q is not a language code", clip(code))
			continue
		}
		strs, ok := m[code].(map[string]any)
		if !ok {
			add("%s must be an object of strings", code)
			continue
		}
		if len(strs) > MaxTranslationKeys {
			add("%s has %d entries; the limit is %d", code, len(strs), MaxTranslationKeys)
		}
		for _, key := range sortedKeys(strs) {
			if !reStringKey.MatchString(key) {
				add("%s: %q is not a string key", code, clip(key))
				continue
			}
			s, ok := strs[key].(string)
			if !ok {
				add("%s/%s must be text", code, key)
				continue
			}
			if n := utf8.RuneCountInString(s); n > MaxTranslationText {
				add("%s/%s is %d characters long; the limit is %d", code, key, n, MaxTranslationText)
			}
		}
	}
	return out
}

// ForGuests returns the design as a guest's browser may receive it: known keys only, each re-checked, invalid
// values dropped, and the custom stylesheet and fragment sanitised. It never fails -- a design that is broken
// in some field still serves every field that is not, because a guest must never be unable to sign in over a
// logo.
func ForGuests(d map[string]any) map[string]any {
	out := map[string]any{}
	if d == nil {
		return out
	}
	keepText := func(k string, max int) {
		if s, ok := d[k].(string); ok && s != "" {
			out[k] = capText(s, max)
		}
	}
	keepText("hotel_name", MaxHotelName)
	keepText("welcome_text", MaxWelcomeText)
	keepText("help_text", MaxHelpText)
	for _, k := range colourFields {
		if s, ok := d[k].(string); ok && reColor.MatchString(s) {
			out[k] = s
		}
	}
	if s, ok := d["corner_radius"].(string); ok && reLength.MatchString(s) {
		out["corner_radius"] = s
	}
	if s, ok := d["font_family"].(string); ok && s != "" && fontStackProblem(s) == "" {
		out["font_family"] = s
	}
	for _, k := range imageFields {
		if s, ok := d[k].(string); ok && s != "" && imageURLProblem(s) == "" {
			v, _ := CheckImageURL(s)
			out[k] = v
		}
	}
	if s, ok := d["terms_url"].(string); ok && s != "" && len(s) <= MaxURLLength {
		if v, err := CheckLinkURL(s); err == nil {
			out["terms_url"] = v
		}
	}
	out["template_id"] = TemplateID(d)
	if opts := guestTemplateOptions(d["template_options"]); len(opts) > 0 {
		out["template_options"] = opts
	}
	if langs := guestLanguages(d["languages"]); len(langs) > 0 {
		out["languages"] = langs
	}
	if tr := guestTranslations(d["translations"]); len(tr) > 0 {
		out["translations"] = tr
	}
	if s, ok := d["custom_css"].(string); ok && s != "" && len(s) <= MaxAdvancedBytes {
		if css, _ := SanitizeCSS(s); strings.TrimSpace(css) != "" {
			out["custom_css"] = css
		}
	}
	if s, ok := d["custom_html"].(string); ok && s != "" && len(s) <= MaxAdvancedBytes {
		if h, _ := SanitizeHTML(s); strings.TrimSpace(h) != "" {
			out["custom_html"] = h
		}
	}
	return out
}

func guestTemplateOptions(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for k, allowed := range templateOptionEnums {
		if s, ok := m[k].(string); ok && contains(allowed, s) {
			out[k] = s
		}
	}
	if n, ok := number(m["overlay"]); ok && n >= 0 && n <= 90 {
		out["overlay"] = int(n)
	}
	if s, ok := m["heading_font"].(string); ok && s != "" && fontStackProblem(s) == "" {
		out["heading_font"] = s
	}
	return out
}

func guestLanguages(v any) []any {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []any
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		code, _ := m["code"].(string)
		if !reLangCode.MatchString(code) {
			continue
		}
		label, _ := m["label"].(string)
		out = append(out, map[string]any{"code": code, "label": capText(label, MaxLanguageLabel)})
		if len(out) == MaxLanguages {
			break
		}
	}
	return out
}

func guestTranslations(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for _, code := range sortedKeys(m) {
		if !reLangCode.MatchString(code) || len(out) == MaxLanguages {
			continue
		}
		strs, ok := m[code].(map[string]any)
		if !ok {
			continue
		}
		one := map[string]any{}
		for _, key := range sortedKeys(strs) {
			if s, ok := strs[key].(string); ok && reStringKey.MatchString(key) && len(one) < MaxTranslationKeys {
				one[key] = capText(s, MaxTranslationText)
			}
		}
		if len(one) > 0 {
			out[code] = one
		}
	}
	return out
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
