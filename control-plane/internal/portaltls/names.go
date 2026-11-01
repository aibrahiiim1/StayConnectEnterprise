// Package portaltls is Central's half of the HTTPS client portal: permanent appliance hostnames, their A
// records, hotel-domain verification, and ACME (DNS-01) certificates for CSRs the appliance generated.
// Private keys never leave the appliance, and nothing here carries guest data.
package portaltls

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Name shapes; they are also CHECK constraints in the database.
const (
	labelLen           = 8  // <label>.gw.<base>: [a-z][a-z0-9]{7}
	challengeLabelLen  = 16 // <challenge_label>.acme.<base>: [a-z0-9]{16}
	GatewaySubdomain   = "gw"
	ChallengeSubdomain = "acme"
)

const (
	alpha    = "abcdefghijklmnopqrstuvwxyz"
	alphaNum = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// randomFrom draws uniformly with crypto/rand; a modulo over random bytes would bias the first characters.
func randomFrom(alphabet string, n int) (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < n; i++ {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[k.Int64()])
	}
	return b.String(), nil
}

// NewLabel returns a letter then seven letters or digits (a letter first never looks like a number). The unique
// index catches the rare collision and the caller retries.
func NewLabel() (string, error) {
	first, err := randomFrom(alpha, 1)
	if err != nil {
		return "", err
	}
	rest, err := randomFrom(alphaNum, labelLen-1)
	if err != nil {
		return "", err
	}
	return first + rest, nil
}

// NewChallengeLabel is unguessable so one hotel cannot point its _acme-challenge at another's delegate.
func NewChallengeLabel() (string, error) { return randomFrom(alphaNum, challengeLabelLen) }

// NewVerifyToken is 160 random bits as lowercase base32 (no mixed case for TXT tooling).
func NewVerifyToken() (string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])), nil
}

func IsLabel(s string) bool {
	if len(s) != labelLen || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !strings.ContainsRune(alphaNum, rune(s[i])) {
			return false
		}
	}
	return true
}

func HostnameFor(label, zone string) string { return label + "." + GatewaySubdomain + "." + zone }

// ChallengeNameFor is the DNS-01 TXT name: the usual _acme-challenge name for OneGate hostnames, the delegate a
// hotel's _acme-challenge CNAME points at for custom domains.
func ChallengeNameFor(kind, hostname, challengeLabel, zone string) string {
	if kind == KindCustom {
		return challengeLabel + "." + ChallengeSubdomain + "." + zone
	}
	return "_acme-challenge." + hostname
}

// ParsePortalIP accepts a single RFC 1918 IPv4 address ("10.255.255.1" or "/32"). Anything else is refused:
// the A record is published in public DNS, and a public address would point guests at somebody else's machine.
func ParsePortalIP(s string) (netip.Addr, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil || p.Bits() != 32 {
			return netip.Addr{}, errors.New("portal_ip must be a single IPv4 address")
		}
		s = p.Addr().String()
	}
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return netip.Addr{}, errors.New("portal_ip must be an IPv4 address")
	}
	if !a.IsPrivate() {
		return netip.Addr{}, errors.New("portal_ip must be a private (RFC 1918) address")
	}
	return a, nil
}

func NormalizeFQDN(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// validHostnameSyntax checks LDH syntax; an all-digit TLD would be an address, not a name.
func validHostnameSyntax(name string) error {
	if len(name) < 4 || len(name) > 253 {
		return errors.New("the name must be between 4 and 253 characters")
	}
	labels := strings.Split(name, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return errors.New("each part of the name must be 1 to 63 characters")
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return errors.New("a part of the name cannot start or end with a hyphen")
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return fmt.Errorf("the name contains %q; only letters, digits, hyphens and dots are allowed", string(c))
			}
		}
	}
	tld := labels[len(labels)-1]
	if strings.Trim(tld, "0123456789") == "" {
		return errors.New("the name must end in a top-level domain, not a number")
	}
	return nil
}

// ValidateCustomDomain requires lowercase LDH, at least three labels, strictly below its registrable domain
// (an apex cannot carry the delegation CNAME), and never under the OneGate zone (Central allocates those).
func ValidateCustomDomain(raw, zone string) (string, error) {
	if strings.ToLower(raw) != raw {
		return "", errors.New("enter the name in lowercase")
	}
	fqdn := NormalizeFQDN(raw)
	if err := validHostnameSyntax(fqdn); err != nil {
		return "", err
	}
	if strings.Count(fqdn, ".") < 2 {
		return "", errors.New("use a subdomain of your own domain, for example wifi.example.com")
	}
	zone = NormalizeFQDN(zone)
	if fqdn == zone || strings.HasSuffix(fqdn, "."+zone) {
		return "", fmt.Errorf("names under %s are allocated by OneGate; enter your own domain", zone)
	}
	reg, err := publicsuffix.EffectiveTLDPlusOne(fqdn)
	if err != nil {
		return "", errors.New("the name is a public suffix, not a domain you can own")
	}
	if reg == fqdn {
		return "", fmt.Errorf("use a subdomain such as wifi.%s, not the domain itself", fqdn)
	}
	return fqdn, nil
}

func RegistrableDomain(fqdn string) (string, error) { return publicsuffix.EffectiveTLDPlusOne(fqdn) }
