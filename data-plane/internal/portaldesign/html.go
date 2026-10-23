package portaldesign

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The custom HTML fragment is an allowlist applied to a tree parsed with the HTML5 algorithm (the tree a
// guest's browser would build) and rebuilt from only what is allowed; a denylist over raw text loses to
// spellings nobody thought of. Text is re-escaped on output.
//
//   - Elements suit an amenities block (text, headings, lists, tables, links, images).
//   - No ids: the sign-in script finds the page's elements by id, so a duplicate would redirect it.
//   - No name: <img name="cookie"> clobbers document.cookie, where the language preference lives.
//   - No data-*: the sign-in script selects by data attributes and would edit the fragment.
//   - style is allowed through the same declaration rules as the stylesheet (css.go); the fragment container
//     is paint-contained so an inline position:fixed can't cover the sign-in forms.
//   - Links go to https, this appliance, #anchors, mailto: or tel:; new-tab links always get
//     rel="noopener noreferrer".
//   - Images: uploads, https, or inline PNG/JPEG/GIF/WebP; never SVG, which can carry script.
//   - Dangerous elements are removed with their content; merely unsupported ones (<font>, custom elements)
//     are unwrapped and their text kept.

// MaxFragmentDepth bounds recursion on every guest page load.
const MaxFragmentDepth = 40

var allowedElements = map[string]bool{
	"div": true, "span": true, "p": true, "br": true, "hr": true,
	"h1": true, "h2": true, "h3": true, "h4": true,
	"strong": true, "em": true, "b": true, "i": true, "u": true, "s": true, "small": true, "mark": true,
	"sub": true, "sup": true, "abbr": true, "time": true, "address": true,
	"ul": true, "ol": true, "li": true, "dl": true, "dt": true, "dd": true,
	"blockquote": true, "figure": true, "figcaption": true,
	"section": true, "article": true, "aside": true, "header": true, "footer": true,
	"a": true, "img": true,
	"table": true, "caption": true, "thead": true, "tbody": true, "tfoot": true, "tr": true, "th": true, "td": true,
}

// Removed with their content: bodies are code, foreign markup or controls, not text to keep.
var droppedWithContent = map[string]string{
	"script": "script is not allowed on the sign-in page",
	"style":  "put styling in Custom CSS instead",
	"iframe": "frames are not allowed", "frame": "frames are not allowed", "frameset": "frames are not allowed",
	"object": "embedded objects are not allowed", "embed": "embedded objects are not allowed",
	"applet": "embedded objects are not allowed", "param": "embedded objects are not allowed",
	"form": "a second form could collect a client's credentials", "input": "form controls are not allowed",
	"button": "form controls are not allowed", "select": "form controls are not allowed",
	"option": "form controls are not allowed", "optgroup": "form controls are not allowed",
	"textarea": "form controls are not allowed", "datalist": "form controls are not allowed",
	"output": "form controls are not allowed", "label": "form controls are not allowed",
	"fieldset": "form controls are not allowed", "legend": "form controls are not allowed",
	"base": "it would re-point every link and form on the page",
	"meta": "it can redirect the client away from the sign-in page",
	"link": "external stylesheets and resources are not allowed",
	"svg":  "SVG can carry script", "math": "MathML is not supported",
	"template": "templates are not supported", "slot": "templates are not supported",
	"noscript": "not supported", "noembed": "not supported", "noframes": "not supported",
	"title": "the page title comes from the site name", "xmp": "not supported", "plaintext": "not supported",
	"audio": "media elements are not allowed", "video": "media elements are not allowed",
	"source": "media elements are not allowed", "track": "media elements are not allowed",
	"picture": "use <img> instead", "canvas": "not supported", "dialog": "not supported",
	"portal": "not supported", "head": "not supported",
}

var globalAttrs = map[string]bool{"class": true, "title": true, "lang": true, "dir": true, "role": true, "style": true}

var (
	reLangAttr = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$`)
	reRole     = regexp.MustCompile(`^[a-z]+( [a-z]+)*$`)
	reAria     = regexp.MustCompile(`^aria-[a-z]{2,32}$`)
	reDim      = regexp.MustCompile(`^[0-9]{1,4}$`)
	reSpan     = regexp.MustCompile(`^[0-9]{1,3}$`)
)

var voidElements = map[string]bool{"br": true, "hr": true, "img": true}

// SanitizeHTML: an empty issue list means the output says exactly what the input said.
func SanitizeHTML(in string) (string, []Issue) {
	return sanitizeHTMLField("custom_html", in)
}

func sanitizeHTMLField(field, in string) (string, []Issue) {
	is := newIssues(field)
	if strings.TrimSpace(in) == "" {
		return "", nil
	}
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(in), ctx)
	if err != nil {
		is.errorf("could not be read as HTML")
		return "", is.list()
	}
	var b strings.Builder
	for _, n := range nodes {
		writeNode(&b, n, 0, is)
	}
	return b.String(), is.list()
}

func writeChildren(b *strings.Builder, n *html.Node, depth int, is *issueSet) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		writeNode(b, c, depth, is)
	}
}

func writeNode(b *strings.Builder, n *html.Node, depth int, is *issueSet) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(html.EscapeString(n.Data))
		return
	case html.CommentNode, html.DoctypeNode:
		// Dropped silently: invisible, and reporting them would flag a non-problem.
		return
	case html.DocumentNode:
		writeChildren(b, n, depth, is)
		return
	case html.ElementNode:
	default:
		return
	}
	if depth >= MaxFragmentDepth {
		is.errorf("is nested more than %d levels deep; the deeper content was removed", MaxFragmentDepth)
		return
	}
	tag := n.Data
	if n.Namespace != "" {
		// SVG/MathML have their own parsing rules, where most mutation-XSS lives.
		is.errorf("removed <%s> (%s markup is not allowed)", tag, strings.ToUpper(n.Namespace))
		return
	}
	if why, bad := droppedWithContent[tag]; bad {
		is.errorf("removed <%s> and its content: %s", tag, why)
		return
	}
	if !allowedElements[tag] {
		is.errorf("removed the unsupported <%s> tag (its text was kept)", tag)
		writeChildren(b, n, depth+1, is)
		return
	}

	b.WriteByte('<')
	b.WriteString(tag)
	hasBlank := false
	hasHref := false
	seen := map[string]bool{}
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		if a.Namespace != "" {
			is.errorf("removed the %s:%s attribute on <%s>", a.Namespace, key, tag)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		val, ok := checkAttr(tag, key, a.Val, is)
		if !ok {
			continue
		}
		switch key {
		case "target":
			hasBlank = true
		case "href":
			hasHref = true
		}
		writeAttr(b, key, val)
	}
	if tag == "img" && !seen["alt"] {
		// Served as written but warned: alt="" (decoration) is a decision and is not reported.
		is.warnf(`an <img> has no alt text: describe it in alt="…", or write alt="" if it is only decoration`)
	}
	if tag == "a" && (hasBlank || hasHref) {
		// Overrides whatever was written: an opened page must not reach window.opener or see the portal URL as referrer.
		writeAttr(b, "rel", "noopener noreferrer")
	}
	b.WriteByte('>')
	if voidElements[tag] {
		return
	}
	writeChildren(b, n, depth+1, is)
	b.WriteString("</")
	b.WriteString(tag)
	b.WriteByte('>')
}

func writeAttr(b *strings.Builder, key, val string) {
	b.WriteByte(' ')
	b.WriteString(key)
	b.WriteString(`="`)
	b.WriteString(html.EscapeString(val))
	b.WriteByte('"')
}

func checkAttr(tag, key, val string, is *issueSet) (string, bool) {
	switch {
	case strings.HasPrefix(key, "on"):
		is.errorf("removed the %s attribute on <%s>: inline script is not allowed", key, tag)
		return "", false
	case key == "id":
		is.errorf("removed the id attribute on <%s>: ids are reserved for the sign-in page's own elements; use class instead", tag)
		return "", false
	case key == "name":
		is.errorf("removed the name attribute on <%s>: it can replace the page's own objects (DOM clobbering)", tag)
		return "", false
	case strings.HasPrefix(key, "data-"):
		is.errorf("removed the %s attribute on <%s>: the sign-in page's script reads data attributes", key, tag)
		return "", false
	case reAria.MatchString(key):
		return capText(val, 512), true
	}

	if globalAttrs[key] {
		switch key {
		case "class":
			return capText(strings.Join(strings.Fields(val), " "), 512), true
		case "title":
			return capText(val, 512), true
		case "lang":
			if reLangAttr.MatchString(val) {
				return val, true
			}
		case "dir":
			if v := strings.ToLower(strings.TrimSpace(val)); v == "ltr" || v == "rtl" || v == "auto" {
				return v, true
			}
		case "role":
			if v := strings.ToLower(strings.TrimSpace(val)); reRole.MatchString(v) {
				return v, true
			}
		case "style":
			out, found := sanitizeDeclarationList(val, false)
			for _, f := range found {
				is.add(f.sev, "in the style attribute on <%s>: %s", tag, f.msg)
			}
			if strings.TrimSpace(out) == "" {
				return "", false
			}
			return out, true
		}
		is.errorf("removed the %s attribute on <%s>: %q is not a valid value", key, tag, capText(val, 40))
		return "", false
	}

	switch tag + "|" + key {
	case "a|href":
		v, err := checkFragmentHref(val)
		if err != nil {
			is.errorf("removed the href on <a>: the link %s", err)
			return "", false
		}
		return v, true
	case "a|target":
		if strings.EqualFold(strings.TrimSpace(val), "_blank") {
			return "_blank", true
		}
		is.errorf("removed target=%q on <a>: only _blank (a new tab) is allowed", capText(val, 20))
		return "", false
	case "a|rel":
		// Replaced, not checked: every link gets noopener noreferrer (see writeNode).
		return "", false
	case "img|src":
		v, err := checkFragmentImage(val)
		if err != nil {
			is.errorf("removed the src on <img>: the image %s", err)
			return "", false
		}
		return v, true
	case "img|alt":
		return capText(val, 512), true
	case "img|width", "img|height":
		if reDim.MatchString(strings.TrimSpace(val)) {
			return strings.TrimSpace(val), true
		}
		is.errorf("removed %s=%q on <img>: use a whole number of pixels", key, capText(val, 20))
		return "", false
	case "td|colspan", "td|rowspan", "th|colspan", "th|rowspan":
		if reSpan.MatchString(strings.TrimSpace(val)) {
			return strings.TrimSpace(val), true
		}
		return "", false
	case "th|scope":
		if v := strings.ToLower(strings.TrimSpace(val)); v == "row" || v == "col" || v == "rowgroup" || v == "colgroup" {
			return v, true
		}
		return "", false
	case "time|datetime":
		return capText(val, 64), true
	case "abbr|title":
		return capText(val, 256), true
	}

	// Anything unlisted (formaction, srcdoc, srcset, ping, http-equiv, ...) is dropped.
	is.errorf("removed the %s attribute on <%s>: it is not an allowed attribute", key, tag)
	return "", false
}

func capText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
