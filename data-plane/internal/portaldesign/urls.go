package portaldesign

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// URL RULES.
//
// Every URL a hotel can put on the sign-in page is loaded or followed by a guest's browser, on the page that
// holds their credentials. So the question is never "is this a URL" but "where can this send the guest, or
// what can it load". The answer is always one of three places: this appliance, an https origin, or bytes
// carried inline as an image. Nothing else -- no javascript:, no data:text/html, no protocol-relative //host,
// no plain http that anyone on the path can rewrite.
//
// BROWSERS NORMALISE BEFORE THEY DECIDE, SO WE DO TOO. The HTML parser has already decoded entities by the
// time a value reaches here (javascript&#58; arrives as javascript:), and the URL parser in every browser
// deletes ASCII tab and newline anywhere in a URL -- "java\tscript:" IS javascript:. A check that ran on the
// raw string would be checking a different URL from the one the browser follows.

var (
	reDataHead = regexp.MustCompile(`^data:image/(png|jpeg|jpg|gif|webp);base64,$`)
	reBase64   = regexp.MustCompile(`^[A-Za-z0-9+/=]*$`)
)
var reCSSDataImage = regexp.MustCompile(`^data:image/(png|jpeg|jpg|gif|webp|avif|svg\+xml)[;,]`)

// normaliseURL applies the browser's own pre-processing: trim C0 controls and spaces from both ends and delete
// every tab, line feed and carriage return. Any other control character left inside is refused outright; no
// legitimate link contains one and several parsers disagree about what they mean.
func normaliseURL(raw string) (string, error) {
	s := strings.TrimFunc(raw, func(r rune) bool { return r <= 0x20 })
	s = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(s)
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("contains a control character")
		}
	}
	if strings.Contains(s, `\`) {
		// Browsers treat a backslash as a slash in special URLs: "/\evil.example" is protocol-relative.
		return "", errors.New(`contains a backslash, which browsers read as "/"`)
	}
	return s, nil
}

// sitePath reports whether s is a path on THIS appliance: it starts with exactly one slash. "//host" is
// protocol-relative and resolves to another server over whatever scheme the page was served with.
func sitePath(s string) bool {
	return strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//")
}

func httpsURL(s string) bool {
	if !strings.HasPrefix(strings.ToLower(s), "https://") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return false
	}
	// https://reception@evil.example/ reads to a guest like a link to "reception". It is nobody's terms page.
	return u.User == nil
}

// CheckLinkURL is the rule for a link a guest can FOLLOW from the sign-in page: the terms-of-use link, and
// href on an <a> in the custom fragment. https, or a path on this appliance.
func CheckLinkURL(raw string) (string, error) {
	s, err := normaliseURL(raw)
	if err != nil {
		return "", err
	}
	switch {
	case s == "":
		return "", errors.New("is empty")
	case sitePath(s), httpsURL(s):
		return s, nil
	case strings.HasPrefix(s, "//"):
		return "", errors.New("is protocol-relative (//host); use a full https:// address or a path on this appliance")
	}
	return "", errors.New("must be an https:// address or a path on this appliance starting with /")
}

// checkFragmentHref is CheckLinkURL plus the three link kinds that make sense inside a hotel's own markup and
// cannot navigate anywhere dangerous: an in-page #anchor, mailto: and tel: ("call reception on 9").
func checkFragmentHref(raw string) (string, error) {
	s, err := normaliseURL(raw)
	if err != nil {
		return "", err
	}
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(s, "#"):
		return s, nil
	case strings.HasPrefix(low, "mailto:"), strings.HasPrefix(low, "tel:"):
		return s, nil
	}
	return CheckLinkURL(s)
}

// CheckImageURL is the rule for an image the design names directly (logo, background, hero): a path on this
// appliance, an https URL, or an inline raster image. SVG is not an inline option: as a document it can carry
// script, and this is the page that collects credentials.
func CheckImageURL(raw string) (string, error) {
	s, err := normaliseURL(raw)
	if err != nil {
		return "", err
	}
	switch {
	case s == "":
		return "", errors.New("is empty")
	case strings.HasPrefix(s, "//"):
		return "", errors.New("must not be protocol-relative; use an appliance path or a full https URL")
	case sitePath(s), httpsURL(s):
		return s, nil
	case isDataImage(s):
		return s, nil
	}
	return "", errors.New("must be an appliance path, an https URL or an inline PNG/JPEG/GIF/WebP image")
}

// checkFragmentImage is the rule for <img src> inside the custom fragment: the hotel's uploaded assets, https,
// or an inline raster image. Stricter than CheckImageURL on purpose -- an arbitrary appliance path such as
// /auth/voucher is not an image, and an <img> pointing at it is a request the guest's browser makes on the
// hotel's behalf.
func checkFragmentImage(raw string) (string, error) {
	s, err := CheckImageURL(raw)
	if err != nil {
		return "", err
	}
	if sitePath(s) && !assetPath(s) {
		return "", errors.New("must be an uploaded image (/assets/…), an https URL or an inline image")
	}
	return s, nil
}

// checkCSSURL is the rule for url(...) inside custom CSS: uploaded assets, https, or an inline image.
func checkCSSURL(raw string) error {
	s, err := normaliseURL(raw)
	if err != nil {
		return err
	}
	low := strings.ToLower(s)
	switch {
	case assetPath(s):
		return nil
	case httpsURL(s):
		return nil
	case reCSSDataImage.MatchString(low):
		return nil
	}
	return errors.New("only uploaded images (/assets/…), https:// addresses and inline data:image/… are allowed in url()")
}

// isDataImage accepts an inline raster image: a data: URL whose media type is one of the four image formats
// every guest browser decodes, carried as base64 and nothing else.
func isDataImage(s string) bool {
	i := strings.IndexByte(s, ',')
	if i < 0 {
		return false
	}
	return reDataHead.MatchString(strings.ToLower(s[:i+1])) && reBase64.MatchString(s[i+1:])
}

// assetPath is an uploaded image on this appliance. Dot segments are refused, raw or percent-encoded:
// "/assets/../auth/voucher" is normalised by the browser into a request for the sign-in endpoint.
func assetPath(s string) bool {
	if !strings.HasPrefix(s, "/assets/") {
		return false
	}
	low := strings.ToLower(s)
	return !strings.Contains(low, "..") && !strings.Contains(low, "%2e") && !strings.Contains(low, "%2f")
}
