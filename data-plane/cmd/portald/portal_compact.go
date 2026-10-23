package main

import (
	"encoding/json"
	"html/template"
	"regexp"
	"strings"
)

// compactMarkup strips stylesheet comments, comment-only script lines and indentation when templates are
// parsed, so less travels over lobby Wi-Fi. It never rewrites a declaration, selector or statement.

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
		// Safe because the portal's sheets never put a space after a colon inside a selector.
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

// jsData is JSON for a page's script. encoding/json escapes <, > and & (and U+2028/U+2029) so the text cannot
// close the script element; passing JSON rather than the value to html/template keeps Arabic and Cyrillic
// unescaped, roughly halving the dictionaries' size.
func jsData(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return template.JS("null")
	}
	return template.JS(b)
}
