package iamv2

import (
	"regexp"
	"strings"
)

// Observer receives redacted events; implementations must not log secrets.
type Observer interface {
	Event(name string, fields map[string]string)
}

type NopObserver struct{}

func (NopObserver) Event(string, map[string]string) {}

var (
	emailRe  = regexp.MustCompile(`[\w.+-]+@[\w.-]+`)
	digitsRe = regexp.MustCompile(`\d{3,}`)
)

// Redact scrubs emails, long digit runs (phone/OTP) and MAC-like strings from logs and evidence.
func Redact(s string) string {
	s = emailRe.ReplaceAllString(s, "«email»")
	s = redactMAC(s)
	s = digitsRe.ReplaceAllString(s, "«num»")
	return s
}

func redactMAC(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		if looksLikeMAC(f) {
			fields[i] = "«mac»"
		}
	}
	return strings.Join(fields, " ")
}

func looksLikeMAC(s string) bool {
	s = strings.Trim(s, ".,;")
	if strings.Count(s, ":") == 5 || strings.Count(s, "-") == 5 {
		for _, c := range s {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c == ':' || c == '-') {
				return false
			}
		}
		return true
	}
	return false
}
