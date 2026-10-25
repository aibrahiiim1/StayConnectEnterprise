package netcfg

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Free-text and name fields reach the generated nftables, netplan, Kea and Unbound files. Each is checked here against a closed
// character set, so a quote, a newline or a brace in a stored value is refused before it can become syntax in a rendered file.
// The renderers escape again (SafeText): the validator is the first barrier, not the only one.

const (
	// MaxNetworkNameRunes bounds a client network's display name.
	MaxNetworkNameRunes = 64
	// maxIfaceNameLen is IFNAMSIZ-1.
	maxIfaceNameLen = 15
	maxDomainLen    = 253
	maxLabelLen     = 63
)

var (
	ifaceNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	domainRE    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$`)
)

// networkNamePunct is the punctuation a display name may carry besides letters, marks, digits and the plain space.
const networkNamePunct = "-_.,()+&/:'"

// CheckNetworkName reports why a client network's display name cannot be used, or "" when it can.
func CheckNetworkName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "network name is required"
	}
	if !utf8.ValidString(name) {
		return "network name is not valid text"
	}
	if n := utf8.RuneCountInString(name); n > MaxNetworkNameRunes {
		return fmt.Sprintf("network name is %d characters; at most %d are allowed", n, MaxNetworkNameRunes)
	}
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), unicode.Is(unicode.M, r), r == ' ':
		case strings.ContainsRune(networkNamePunct, r):
		default:
			return fmt.Sprintf("network name contains %q, which is not allowed; use letters, digits, spaces and %s", r, networkNamePunct)
		}
	}
	return ""
}

// CheckIfaceName reports why a Linux interface or bridge name cannot be used, or "" when it can.
func CheckIfaceName(name string) string {
	if name == "" {
		return "name is required"
	}
	if len(name) > maxIfaceNameLen {
		return fmt.Sprintf("%q is longer than the %d characters a Linux interface name may have", name, maxIfaceNameLen)
	}
	if !ifaceNameRE.MatchString(name) {
		return fmt.Sprintf("%q is not a valid interface name: use letters, digits, '.', '_' and '-' only", name)
	}
	return ""
}

// CheckDomainName reports why a DNS domain or host name cannot be used, or "" when it can. The empty string is allowed:
// it means the field is unset.
func CheckDomainName(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > maxDomainLen {
		return fmt.Sprintf("name is longer than %d characters", maxDomainLen)
	}
	for _, label := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if label == "" || len(label) > maxLabelLen || !domainRE.MatchString(label) {
			return fmt.Sprintf("%q is not a valid DNS name: each part must be 1-%d letters, digits or '-', and must not start or end with '-'", s, maxLabelLen)
		}
	}
	return ""
}

// SafeText makes untrusted text safe inside a double-quoted nftables string or comment: anything outside the set
// CheckNetworkName allows, and every control character, becomes '_'. It exists so a value that bypassed validation still
// cannot end the string, start a statement or add a rule.
func SafeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), unicode.Is(unicode.M, r), r == ' ':
			b.WriteRune(r)
		case strings.ContainsRune(networkNamePunct, r):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// SafeIface is SafeText for an interface or bridge name: only letters, digits, '.', '_' and '-' survive.
func SafeIface(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
