package portaldesign

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// THE CUSTOM HTML FRAGMENT: AN ALLOWLIST, APPLIED TO A PARSED TREE.
//
// What this replaced was a regular expression listing spellings of "script". A denylist over raw text loses
// to any spelling it did not think of, and HTML has a great many: <img/onerror=...> has no whitespace before
// the handler, <base href> contains no script at all and re-points every relative form action on the page,
// <button form=… formaction=…> submits the guest's own voucher form somewhere else, <meta http-equiv=refresh>
// navigates away, and javascript&#58; is javascript: once the parser has decoded it.
//
// So the fragment is PARSED -- by the same HTML5 algorithm a browser uses, so the tree here is the tree a guest's
// browser would build -- and then REBUILT from nothing but what is explicitly allowed. Anything not on the
// lists below never reaches the output, whatever it is called, however it is spelled, and whether or not
// anybody has thought of it yet. Text is re-escaped on the way out, so a string that decoded to "<script>"
// leaves as &lt;script&gt;.
//
// THE CHOICES, AND WHY.
//
//   - Elements are the vocabulary of an amenities block: text, headings, lists, a figure, a table of opening
//     hours, links and images. Tables are included because "Pool 08:00–20:00" is the single most common thing a
//     hotel wants to add, and table markup can carry nothing a <div> cannot.
//   - NO ids. The page's own elements are found by id (#form-voucher, #pms-room, #brand-name…); a fragment that
//     declared a second id="form-voucher" would change which element the sign-in script talks to.
//   - NO name. <img name="cookie"> replaces document.cookie for every script on the page (DOM clobbering), and
//     the language preference is stored in exactly that.
//   - NO data-* attributes. The sign-in script selects by data attributes (data-i18n, data-otp, data-stage); a
//     fragment carrying them would be edited by the page's own code.
//   - style IS allowed, through the same declaration rules as the custom stylesheet (see css.go). Refusing it
//     would only move the same declarations into the stylesheet, where they are subject to exactly the same
//     rules; and the fragment's container is paint-contained on the page, so an inline position:fixed cannot
//     lift an element out over the sign-in forms.
//   - Links go to https, this appliance, #anchors, mailto: or tel:, and a link that opens a new tab always
//     carries rel="noopener noreferrer" so the page it opens cannot reach back into this one.
//   - Images come from the hotel's uploads, https, or inline PNG/JPEG/GIF/WebP -- never SVG, which as a
//     document can carry script.
//   - Elements that are DANGEROUS are removed together with everything inside them (a <script>'s body is
//     code, not text to keep). Elements that are merely UNSUPPORTED (<font>, <center>, a custom element) are
//     unwrapped: the tag goes, the text inside stays.

// MaxFragmentDepth bounds nesting. Nothing a hotel writes by hand is forty levels deep, and a pathological
// document should cost a refusal rather than a deep recursion on every guest page load.
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

// Removed WITH their content. Their bodies are code, foreign markup, or controls -- none of it is text a
// hotel meant a guest to read.
var droppedWithContent = map[string]string{
	"script": "script is not allowed on the sign-in page",
	"style":  "put styling in Custom CSS instead",
	"iframe": "frames are not allowed", "frame": "frames are not allowed", "frameset": "frames are not allowed",
	"object": "embedded objects are not allowed", "embed": "embedded objects are not allowed",
	"applet": "embedded objects are not allowed", "param": "embedded objects are not allowed",
	"form": "a second form could collect a guest's credentials", "input": "form controls are not allowed",
	"button": "form controls are not allowed", "select": "form controls are not allowed",
	"option": "form controls are not allowed", "optgroup": "form controls are not allowed",
	"textarea": "form controls are not allowed", "datalist": "form controls are not allowed",
	"output": "form controls are not allowed", "label": "form controls are not allowed",
	"fieldset": "form controls are not allowed", "legend": "form controls are not allowed",
	"base": "it would re-point every link and form on the page",
	"meta": "it can redirect the guest away from the sign-in page",
	"link": "external stylesheets and resources are not allowed",
	"svg":  "SVG can carry script", "math": "MathML is not supported",
	"template": "templates are not supported", "slot": "templates are not supported",
	"noscript": "not supported", "noembed": "not supported", "noframes": "not supported",
	"title": "the page title comes from the hotel name", "xmp": "not supported", "plaintext": "not supported",
	"audio": "media elements are not allowed", "video": "media elements are not allowed",
	"source": "media elements are not allowed", "track": "media elements are not allowed",
	"picture": "use <img> instead", "canvas": "not supported", "dialog": "not supported",
	"portal": "not supported", "head": "not supported",
}

// Attributes allowed on every allowed element.
var globalAttrs = map[string]bool{"class": true, "title": true, "lang": true, "dir": true, "role": true, "style": true}

var (
	reLangAttr = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$`)
	reRole     = regexp.MustCompile(`^[a-z]+( [a-z]+)*$`)
	reAria     = regexp.MustCompile(`^aria-[a-z]{2,32}$`)
	reDim      = regexp.MustCompile(`^[0-9]{1,4}$`)
	reSpan     = regexp.MustCompile(`^[0-9]{1,3}$`)
)

var voidElements = map[string]bool{"br": true, "hr": true, "img": true}

// SanitizeHTML returns the fragment rebuilt from allowed parts only, and a finding for everything that was
// removed or changed. An empty issue list means the output says exactly what the input said.
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
		// Comments are dropped without comment: they are invisible, carry no behaviour once removed, and
		// reporting them would make an operator fix something that was never a problem.
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
		// Parsed as SVG or MathML: foreign content has its own parsing rules and is where most mutation-XSS
		// lives. None of it is needed for an amenities block.
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
	if tag == "a" && (hasBlank || hasHref) {
		// Whatever the operator wrote. A page opened from the sign-in page must not be able to navigate it
		// (window.opener), and the guest's portal URL is nobody's referrer.
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

// checkAttr decides one attribute. It returns the value to emit (possibly rewritten) and whether to emit it.
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

	// formaction, srcdoc, srcset, action, xlink:href, background, poster, ping, http-equiv… and anything
	// else nobody has invented yet: not on the list, so not on the page.
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
