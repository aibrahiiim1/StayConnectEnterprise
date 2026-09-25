package main

import (
	"encoding/json"
	"html/template"
	"regexp"
	"strings"
)

// A CAPTIVE PORTAL IS READ OVER WHATEVER THE LOBBY'S WI-FI CAN DO, BEFORE THE GUEST HAS ANY INTERNET.
//
// The templates are written to be read and reviewed -- indented, with the reasoning next to each rule. None of
// that needs to travel to a phone. compactMarkup removes it when the templates are parsed: stylesheet
// comments, script lines that are only a comment, and indentation. It never rewrites a declaration, a
// selector or a statement, so what runs is exactly what the source says.

var (
	reStyleBlock  = regexp.MustCompile(`(?s)(<style[^>]*>)(.*?)(</style>)`)
	reScriptBlock = regexp.MustCompile(`(?s)(<script[^>]*>)(.*?)(</script>)`)
	reCSSComment  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reSpaces      = regexp.MustCompile(`\s+`)
	reCSSPunct    = regexp.MustCompile(`\s*([{};,])\s*`)
	reCSSColon    = regexp.MustCompile(`:\s+`)
	reJSLineNote  = regexp.MustCompile(`(?m)^[ \t]*//.*$\n?`)
	reIndent      = regexp.MustCompile(`(?m)^[ \t]+`)
	reBlankLines  = regexp.MustCompile(`\n{2,}`)
)

func compactMarkup(s string) string {
	s = reStyleBlock.ReplaceAllStringFunc(s, func(block string) string {
		m := reStyleBlock.FindStringSubmatch(block)
		css := reCSSComment.ReplaceAllString(m[2], "")
		css = reSpaces.ReplaceAllString(css, " ")
		css = reCSSPunct.ReplaceAllString(css, "$1")
		// "prop: value" -> "prop:value". The portal's own sheets never put a space after a colon inside a
		// selector (":hover", "::before", ":not(...)"), so only declarations and media features are touched.
		css = reCSSColon.ReplaceAllString(css, ":")
		return m[1] + strings.TrimSpace(css) + m[3]
	})
	s = reScriptBlock.ReplaceAllStringFunc(s, func(block string) string {
		m := reScriptBlock.FindStringSubmatch(block)
		js := reJSLineNote.ReplaceAllString(m[2], "")
		return m[1] + js + m[3]
	})
	s = reIndent.ReplaceAllString(s, "")
	return reBlankLines.ReplaceAllString(s, "\n")
}

// jsData is a value for a page's script, as JSON. encoding/json escapes <, > and & (and U+2028/U+2029), so
// the text cannot close the script element or break the statement; handing html/template the JSON rather than
// the value keeps Arabic and Cyrillic as themselves instead of six-byte escapes, which on the sign-in page is
// the difference between carrying six dictionaries and carrying them twice over.
func jsData(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return template.JS("null")
	}
	return template.JS(b)
}
