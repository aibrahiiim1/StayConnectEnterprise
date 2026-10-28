package api

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// applianceFacts are the self-reported hardware facts of an online registration or an offline activation
// request. Both are unauthenticated input stored and shown to operators, so they are checked first (audit
// Phase 1 M11). The appliance (data-plane internal/hwid) sends SC-XXXX-XXXX-XXXX serials, a 32-hex
// fingerprint, permanent NIC MACs and the DMI vendor and product.
type applianceFacts struct {
	Serial, Hostname, Model, WANMAC, LANMAC, Fingerprint string
}

var (
	serialRe      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	hostnameRe    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
	fingerprintRe = regexp.MustCompile(`^[0-9a-fA-F]{1,128}$`)
)

// normalize checks every fact and returns them with MACs in canonical form. Empty optional facts stay empty.
func (f applianceFacts) normalize() (applianceFacts, error) {
	f.Serial = strings.TrimSpace(f.Serial)
	if !serialRe.MatchString(f.Serial) {
		return f, errors.New("serial must be 1-64 characters: letters, digits, dot, underscore or hyphen")
	}
	if f.Hostname = strings.TrimSpace(f.Hostname); f.Hostname != "" && !hostnameRe.MatchString(f.Hostname) {
		return f, errors.New("hostname must be at most 253 characters: letters, digits, dot, underscore or hyphen")
	}
	if f.Model = strings.TrimSpace(f.Model); f.Model != "" {
		if utf8.RuneCountInString(f.Model) > 120 || strings.IndexFunc(f.Model, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			return f, errors.New("model must be at most 120 printable characters")
		}
	}
	for _, m := range []*string{&f.WANMAC, &f.LANMAC} {
		if strings.TrimSpace(*m) == "" {
			*m = ""
			continue
		}
		c, ok := canonicalMAC(*m)
		if !ok {
			return f, errors.New("wan_mac and lan_mac must be MAC addresses")
		}
		*m = c
	}
	if f.Fingerprint = strings.TrimSpace(f.Fingerprint); f.Fingerprint != "" && !fingerprintRe.MatchString(f.Fingerprint) {
		return f, errors.New("hardware_fingerprint must be hexadecimal, at most 128 characters")
	}
	return f, nil
}
