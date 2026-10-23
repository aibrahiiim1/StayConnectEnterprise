package portaldesign

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The custom stylesheet is sanitised against three things:
//  1. Loading: @import, url(), image-set() make the guest's browser request an address. Only the hotel's
//     uploads, https and inline images may be named, only through a checked url(). (https is acceptable
//     because outside hosts are unreachable to a device not yet signed in.)
//  2. Execution: expression(), behavior:, -moz-binding and javascript: URLs are refused however escaped.
//  3. Breaking sign-in: the page wraps this sheet in `@layer hotel` under an unlayered guard sheet, and a
//     layered !important would outrank the guard, so !important is removed (a warning, not a refusal).
//
// The output is rebuilt from parsed rules, so braces balance by construction; a stray "}" must not close
// `@layer hotel {` early and put the rest outside the layer.
//
// Escapes are decoded before any check ("\65 xpression(" is expression( to a browser).

const MaxCSSNesting = 12

type finding struct {
	sev Severity
	msg string
}

type cssSan struct {
	found          []finding
	stripImportant bool
	// selectors is the enclosing style-rule chain (at-rules excluded).
	selectors []string
}

func (c *cssSan) errorf(format string, a ...any) {
	c.found = append(c.found, finding{SeverityError, sprintf(format, a...)})
}
func (c *cssSan) warnf(format string, a ...any) {
	c.found = append(c.found, finding{SeverityWarning, sprintf(format, a...)})
}

func SanitizeCSS(in string) (string, []Issue) {
	return sanitizeCSSField("custom_css", in)
}

func sanitizeCSSField(field, in string) (string, []Issue) {
	is := newIssues(field)
	if strings.TrimSpace(in) == "" {
		return "", nil
	}
	c := &cssSan{stripImportant: true}
	src := in
	if reEndStyle.MatchString(src) {
		c.errorf("contains </style, which could end the stylesheet early; it was removed")
		src = reEndStyle.ReplaceAllString(src, "")
	}
	if !utf8.ValidString(src) {
		c.errorf("is not valid UTF-8 text")
		src = strings.ToValidUTF8(src, "�")
	}
	clean := stripCSSComments(src)
	out := c.rules(clean, 0)
	for _, f := range c.found {
		is.add(f.sev, "%s", f.msg)
	}
	return out, is.list()
}

// sanitizeDeclarationList applies the same rules to a style="" attribute.
func sanitizeDeclarationList(in string, stripImportant bool) (string, []finding) {
	c := &cssSan{stripImportant: stripImportant}
	clean := stripCSSComments(strings.ToValidUTF8(in, "�"))
	if strings.ContainsAny(clean, "{}") {
		c.errorf("braces are not allowed in a style attribute")
		return "", c.found
	}
	return c.declarations(clean, MaxCSSNesting), c.found
}

var reEndStyle = regexp.MustCompile(`(?i)<\s*/\s*style`)

func sprintf(format string, a ...any) string {
	if len(a) == 0 {
		return format
	}
	return fmt.Sprintf(format, a...)
}

// stripCSSComments leaves string literals alone; an unterminated comment swallows the rest, as in a browser.
func stripCSSComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		ch := s[i]
		switch {
		case ch == 0:
			i++
		case ch == '"' || ch == '\'':
			j := scanString(s, i)
			b.WriteString(s[i:j])
			i = j
		case ch == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			b.WriteByte(' ')
			i = i + 2 + end + 2
		default:
			b.WriteByte(ch)
			i++
		}
	}
	return b.String()
}

// scanString: an unescaped newline ends a CSS string.
func scanString(s string, i int) int {
	q := s[i]
	j := i + 1
	for j < len(s) {
		switch s[j] {
		case '\\':
			j += 2
			continue
		case q:
			return j + 1
		case '\n':
			return j
		}
		j++
	}
	return len(s)
}

func scanTo(s string, i int, stop string) int {
	depth := 0
	for i < len(s) {
		ch := s[i]
		switch {
		case ch == '\\':
			i += 2
			continue
		case ch == '"' || ch == '\'':
			i = scanString(s, i)
			continue
		case ch == '(':
			depth++
		case ch == ')':
			if depth > 0 {
				depth--
			}
		case depth == 0 && strings.IndexByte(stop, ch) >= 0:
			return i
		}
		i++
	}
	return len(s)
}

func matchBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); {
		ch := s[i]
		switch {
		case ch == '\\':
			i += 2
			continue
		case ch == '"' || ch == '\'':
			i = scanString(s, i)
			continue
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return -1
}

var blockAtRules = map[string]string{
	"media": "rules", "supports": "rules", "container": "rules", "layer": "rules", "scope": "rules",
	"starting-style": "rules", "keyframes": "rules", "-webkit-keyframes": "rules",
	"font-face": "decls", "page": "decls", "counter-style": "decls", "property": "decls",
}

func (c *cssSan) rules(s string, depth int) string {
	if depth > MaxCSSNesting {
		c.errorf("rules are nested more than %d levels deep; the deeper rules were removed", MaxCSSNesting)
		return ""
	}
	var out []string
	for i := 0; i < len(s); {
		for i < len(s) && isCSSSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			break
		}
		j := scanTo(s, i, "{};")
		prelude := strings.TrimSpace(s[i:j])
		if j >= len(s) {
			if prelude != "" {
				c.errorf("ends in the middle of a rule (%q); that rule was removed", clip(prelude))
			}
			break
		}
		switch s[j] {
		case ';':
			if r := c.statement(prelude); r != "" {
				out = append(out, r)
			}
			i = j + 1
		case '}':
			c.errorf("has a closing brace with no rule to close; it was removed")
			i = j + 1
		case '{':
			end := matchBrace(s, j)
			if end < 0 {
				c.errorf("has a rule that is never closed (%q); it was removed", clip(prelude))
				return strings.Join(out, "\n")
			}
			if r := c.rule(prelude, s[j+1:end], depth); r != "" {
				out = append(out, r)
			}
			i = end + 1
		}
	}
	return strings.Join(out, "\n")
}

func (c *cssSan) statement(prelude string) string {
	if prelude == "" {
		return ""
	}
	if prelude[0] != '@' {
		c.errorf("contains text that is not a rule (%q); it was removed", clip(prelude))
		return ""
	}
	name := atName(prelude)
	switch name {
	case "import":
		c.errorf("@import is not allowed: it loads a stylesheet from elsewhere into the sign-in page")
	case "charset":
		c.errorf("@charset is not allowed: the stylesheet is always UTF-8")
	case "namespace":
		c.errorf("@namespace is not allowed")
	case "layer":
		// Only orders sub-layers of the hotel's own layer; the portal's layers are already fixed.
		if strings.ContainsAny(prelude, "(){}") {
			c.errorf("@layer must name layers only")
			return ""
		}
		return prelude + ";"
	default:
		c.errorf("@%s is not allowed", name)
	}
	return ""
}

func (c *cssSan) rule(prelude, body string, depth int) string {
	if prelude == "" {
		c.errorf("has a block with no selector; it was removed")
		return ""
	}
	if prelude[0] == '@' {
		name := atName(prelude)
		kind, ok := blockAtRules[name]
		if !ok {
			switch name {
			case "import", "charset", "namespace":
				c.statement(prelude)
			default:
				c.errorf("@%s is not allowed", name)
			}
			return ""
		}
		if bad := c.valueProblems(prelude); bad != "" {
			c.errorf("@%s %s", name, bad)
			return ""
		}
		inner := ""
		if kind == "rules" {
			inner = c.rules(body, depth+1)
		} else {
			inner = c.declarations(body, depth+1)
		}
		return prelude + " {\n" + inner + "\n}"
	}
	if bad := c.valueProblems(prelude); bad != "" {
		c.errorf("the selector %q %s", clip(prelude), bad)
		return ""
	}
	c.selectors = append(c.selectors, prelude)
	inner := c.declarations(body, depth+1)
	c.selectors = c.selectors[:len(c.selectors)-1]
	return prelude + " {\n" + inner + "\n}"
}

func (c *cssSan) focusSelector() string {
	for i := len(c.selectors) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(decodeCSSEscapes(c.selectors[i])), ":focus") {
			return c.selectors[i]
		}
	}
	return ""
}

// hidesOutline: none, hidden, zero width or a transparent colour.
func hidesOutline(name, value string) bool {
	v := strings.ToLower(strings.Join(strings.Fields(decodeCSSEscapes(value)), " "))
	zero := func(w string) bool {
		if w == "" || !(w[0] == '.' || (w[0] >= '0' && w[0] <= '9')) {
			return false
		}
		return strings.TrimLeft(strings.TrimRight(w, "pxemrchvwi%"), "0.") == ""
	}
	switch name {
	case "outline":
		for _, w := range strings.Fields(v) {
			if w == "none" || w == "hidden" || w == "transparent" || zero(w) {
				return true
			}
		}
	case "outline-style":
		return v == "none" || v == "hidden"
	case "outline-width":
		return zero(v)
	case "outline-color":
		return v == "transparent"
	}
	return false
}

func (c *cssSan) declarations(s string, depth int) string {
	var out []string
	for i := 0; i < len(s); {
		for i < len(s) && (isCSSSpace(s[i]) || s[i] == ';') {
			i++
		}
		if i >= len(s) {
			break
		}
		j := scanTo(s, i, "{;}")
		if j < len(s) && s[j] == '{' {
			end := matchBrace(s, j)
			if end < 0 {
				c.errorf("has a nested rule that is never closed; it was removed")
				break
			}
			if depth >= MaxCSSNesting {
				c.errorf("rules are nested more than %d levels deep; the deeper rules were removed", MaxCSSNesting)
			} else if r := c.rule(strings.TrimSpace(s[i:j]), s[j+1:end], depth); r != "" {
				out = append(out, r)
			}
			i = end + 1
			continue
		}
		if j < len(s) && s[j] == '}' {
			c.errorf("has a closing brace with no rule to close; it was removed")
			i = j + 1
			continue
		}
		if d := c.declaration(strings.TrimSpace(s[i:j])); d != "" {
			out = append(out, d)
		}
		i = j + 1
	}
	return strings.Join(out, ";\n")
}

var reIdent = regexp.MustCompile(`^-{0,2}[a-zA-Z_][a-zA-Z0-9_-]*$`)

func (c *cssSan) declaration(d string) string {
	if d == "" {
		return ""
	}
	colon := scanTo(d, 0, ":")
	if colon >= len(d) {
		c.errorf("contains %q, which is not a declaration; it was removed", clip(d))
		return ""
	}
	rawName := strings.TrimSpace(d[:colon])
	value := strings.TrimSpace(d[colon+1:])
	name := strings.ToLower(decodeCSSEscapes(rawName))
	if !reIdent.MatchString(name) {
		c.errorf("the property %q is not a valid name; it was removed", clip(rawName))
		return ""
	}
	switch name {
	case "behavior", "-ms-behavior", "-moz-binding":
		c.errorf("the %s property is not allowed: it can run code in some browsers", name)
		return ""
	}

	// !important is found on the decoded text but cut from the raw one so other escapes survive.
	if bang := lastTopLevelBang(value); bang >= 0 {
		tail := strings.ToLower(strings.Join(strings.Fields(decodeCSSEscapes(value[bang+1:])), ""))
		if tail == "important" {
			if c.stripImportant {
				c.warnf("!important was removed (on %s): the sign-in controls stay usable whatever the stylesheet says, and your rules already take precedence over the portal's own styling", name)
				value = strings.TrimSpace(value[:bang])
			}
		}
	}
	if bad := c.valueProblems(value); bad != "" {
		c.errorf("%s: %s", name, bad)
		return ""
	}
	if strings.TrimSpace(value) == "" {
		return ""
	}
	// Served as written (the guard sheet keeps its own ring), but the operator is warned.
	if sel := c.focusSelector(); sel != "" && hidesOutline(name, value) {
		c.warnf("%s: %s on %q removes the focus outline that keyboard users follow; the portal still draws its own focus ring on links, buttons and fields", name, clip(value), clip(sel))
	}
	return rawName + ": " + value
}

var reLoader = regexp.MustCompile(`(^|[^a-z0-9_-])(image-set|-webkit-image-set|image|src|cross-fade|-webkit-cross-fade)\(`)

// valueProblems checks decoded text (value, selector or at-rule prelude) and describes the first problem.
func (c *cssSan) valueProblems(raw string) string {
	dec := strings.ToLower(decodeCSSEscapes(raw))
	flat := strings.Join(strings.Fields(dec), "")
	switch {
	case strings.Contains(flat, "expression("):
		return "expression() is not allowed: it runs code in some browsers"
	case strings.Contains(flat, "javascript:"), strings.Contains(flat, "vbscript:"):
		return "script URLs are not allowed"
	case strings.Contains(flat, "-moz-binding"), strings.Contains(flat, "behavior:"):
		return "bindings and behaviors are not allowed: they can run code in some browsers"
	case strings.Contains(flat, "@import"):
		return "@import is not allowed"
	case reLoader.MatchString(flat):
		return "image-set(), image() and src() are not allowed; name an image with url() instead"
	}
	// Checked on the decoded text and the whitespace-stripped text, so "u/**/rl(" reads as the url( an older engine sees.
	for _, src := range []string{dec, flat} {
		for i := 0; ; {
			k := strings.Index(src[i:], "url(")
			if k < 0 {
				break
			}
			arg, next := cssURLArg(src, i+k+4)
			if err := checkCSSURL(arg); err != nil {
				return "url(" + clip(arg) + ") is not allowed: " + err.Error()
			}
			i = next
		}
	}
	return ""
}

func cssURLArg(s string, i int) (string, int) {
	for i < len(s) && isCSSSpace(s[i]) {
		i++
	}
	if i < len(s) && (s[i] == '"' || s[i] == '\'') {
		end := scanString(s, i)
		inner := s[i+1 : end]
		if end > i+1 && end <= len(s) && s[end-1] == s[i] {
			inner = s[i+1 : end-1]
		}
		return inner, end
	}
	j := strings.IndexByte(s[i:], ')')
	if j < 0 {
		return strings.TrimSpace(s[i:]), len(s)
	}
	return strings.TrimSpace(s[i : i+j]), i + j + 1
}

func lastTopLevelBang(s string) int {
	last := -1
	depth := 0
	for i := 0; i < len(s); {
		ch := s[i]
		switch {
		case ch == '\\':
			i += 2
			continue
		case ch == '"' || ch == '\'':
			i = scanString(s, i)
			continue
		case ch == '(':
			depth++
		case ch == ')':
			if depth > 0 {
				depth--
			}
		case ch == '!' && depth == 0:
			last = i
		}
		i++
	}
	return last
}

func atName(prelude string) string {
	rest := prelude[1:]
	end := 0
	for end < len(rest) {
		ch := rest[end]
		if ch == '\\' {
			end += 2
			continue
		}
		if isCSSSpace(ch) || ch == '(' || ch == '{' || ch == ';' || ch == '"' || ch == '\'' {
			break
		}
		end++
	}
	if end > len(rest) {
		end = len(rest)
	}
	return strings.ToLower(decodeCSSEscapes(rest[:end]))
}

// decodeCSSEscapes follows the CSS tokenizer: \ plus 1-6 hex digits (and one optional space) is a code point.
func decodeCSSEscapes(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			i++
			continue
		}
		i++
		if i >= len(s) {
			break
		}
		j := i
		for j < len(s) && j-i < 6 && isHex(s[j]) {
			j++
		}
		if j > i {
			n, _ := strconv.ParseUint(s[i:j], 16, 32)
			r := rune(n)
			if n == 0 || n > utf8.MaxRune || (r >= 0xD800 && r <= 0xDFFF) {
				r = utf8.RuneError
			}
			b.WriteRune(r)
			if j < len(s) && isCSSSpace(s[j]) {
				j++
			}
			i = j
			continue
		}
		if s[i] == '\n' {
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isCSSSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}
