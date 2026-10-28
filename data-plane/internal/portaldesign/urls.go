package portaldesign

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// A URL on the sign-in page may point only at this appliance, an https origin, or inline image bytes: no
// javascript:, data:text/html, protocol-relative //host or plain http.
//
// Normalise as browsers do before deciding: entities are already decoded by the HTML parser, and browsers
// delete tab and newline anywhere in a URL ("java\tscript:" is javascript:), so checking the raw string
// would check a different URL than the one followed.

var (
	reDataHead = regexp.MustCompile(`^data:image/(png|jpeg|jpg|gif|webp);base64,$`)
	reBase64   = regexp.MustCompile(`^[A-Za-z0-9+/=]*$`)
)
var reCSSDataImage = regexp.MustCompile(`^data:image/(png|jpeg|jpg|gif|webp|avif|svg\+xml)[;,]`)

// normaliseURL mimics the browser: trim C0 controls and spaces, delete tab/LF/CR; any other control character
// is refused since parsers disagree about it.
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

// sitePath: exactly one leading slash; "//host" is protocol-relative and resolves to another server.
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
	// https://reception@evil.example/ reads like a link to "reception".
	return u.User == nil
}

// CheckLinkURL covers links a guest follows (terms, fragment <a>): https or an appliance path.
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

// checkFragmentHref adds #anchor, mailto: and tel: to CheckLinkURL.
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

// CheckImageURL is the base image rule: appliance path, https, or inline raster. Never inline SVG (it can carry script).
// Design image fields and fragments use the stricter checkFragmentImage.
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

// checkFragmentImage is stricter than CheckImageURL: an <img> at an arbitrary appliance path such as
// /auth/voucher would make the guest's browser issue that request.
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

func isDataImage(s string) bool {
	i := strings.IndexByte(s, ',')
	if i < 0 {
		return false
	}
	return reDataHead.MatchString(strings.ToLower(s[:i+1])) && reBase64.MatchString(s[i+1:])
}

// assetPath refuses dot segments, raw or percent-encoded: "/assets/../auth/voucher" becomes a sign-in request.
func assetPath(s string) bool {
	if !strings.HasPrefix(s, "/assets/") {
		return false
	}
	low := strings.ToLower(s)
	return !strings.Contains(low, "..") && !strings.Contains(low, "%2e") && !strings.Contains(low, "%2f")
}
