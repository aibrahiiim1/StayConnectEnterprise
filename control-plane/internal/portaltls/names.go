// Package portaltls is Central's half of the HTTPS client portal (docs/architecture/
// ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md): it gives every appliance a permanent hostname under
// gw.onegate.work, publishes that name's A record (the appliance's private portal address), verifies a hotel's
// own portal domain, and obtains publicly trusted certificates for both from an ACME CA with DNS-01 -- always
// for a CSR the appliance generated. Private keys never leave the appliance; Central holds public material,
// one ACME account key and one DNS API token, both root-only files.
//
// Nothing here carries guest data: hostnames, an address, CSRs and certificate status only (CLAUDE.md §0E).
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

// Name shapes, from the contract §2. They are also CHECK constraints in migration 0050.
const (
	labelLen          = 8  // <label>.gw.onegate.work: [a-z][a-z0-9]{7}
	challengeLabelLen = 16 // <challenge_label>.acme.onegate.work: [a-z0-9]{16}
	// GatewaySubdomain and ChallengeSubdomain sit under the configured zone (onegate.work).
	GatewaySubdomain   = "gw"
	ChallengeSubdomain = "acme"
)

const (
	alpha    = "abcdefghijklmnopqrstuvwxyz"
	alphaNum = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// randomFrom draws n characters uniformly from alphabet with crypto/rand. rand.Int rejects internally, so no
// character is more likely than another (a modulo over random bytes would bias the first few).
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

// NewLabel returns a fresh appliance hostname label: a letter, then seven letters or digits. A letter first
// keeps the name from ever looking like a number to a sloppy parser. 26*36^7 ≈ 2·10^12 labels: the unique
// index catches the collision a fleet will never see, and the caller retries.
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

// NewChallengeLabel returns the label of a hotel domain's delegated DNS-01 name,
// <challenge_label>.acme.onegate.work. It is unguessable so that one hotel cannot point its _acme-challenge
// at another's delegate and ride on its validations.
func NewChallengeLabel() (string, error) { return randomFrom(alphaNum, challengeLabelLen) }

// NewVerifyToken returns the value a hotel publishes at _onegate-verify.<fqdn>: 160 random bits as 32
// lowercase base32 characters (DNS is case-insensitive in practice for TXT tooling, so no mixed case).
func NewVerifyToken() (string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])), nil
}

// IsLabel reports whether s has the shape of an appliance hostname label.
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

// HostnameFor is the appliance's OneGate portal hostname.
func HostnameFor(label, zone string) string { return label + "." + GatewaySubdomain + "." + zone }

// ChallengeNameFor is the TXT name Central writes to answer DNS-01 for hostname: under the OneGate zone the
// usual _acme-challenge name, for a hotel domain the delegate its _acme-challenge CNAME points at.
func ChallengeNameFor(kind, hostname, challengeLabel, zone string) string {
	if kind == KindCustom {
		return challengeLabel + "." + ChallengeSubdomain + "." + zone
	}
	return "_acme-challenge." + hostname
}

// ParsePortalIP validates the portal address an appliance reports: a single IPv4 address in RFC 1918 space
// (contract §2.1). It accepts "10.255.255.1" and "10.255.255.1/32" and returns the bare address. Anything
// public, loopback, link-local, IPv6 or wider than a /32 is refused -- the A record is published in public
// DNS, and a public address there would point every guest's portal name at somebody else's machine.
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

// NormalizeFQDN lowercases a DNS name and drops one trailing dot.
func NormalizeFQDN(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// validHostnameSyntax checks LDH syntax: 1-63 character labels of letters, digits and inner hyphens, at
// most 253 characters, and a TLD that is not all digits (that would be an address, not a name).
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

// ValidateCustomDomain checks a hotel portal domain (contract §3.1) and returns it normalised. The name must
// be lowercase LDH syntax, at least three labels, strictly BELOW its registrable domain (wifi.nilehotel.com,
// never nilehotel.com: an apex cannot carry the CNAME the delegation relies on, and a public suffix is not
// anybody's to claim), and never under the OneGate zone (those names are Central's to allocate).
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

// RegistrableDomain is fqdn's eTLD+1 (the Public Suffix List's idea of the domain somebody registered).
func RegistrableDomain(fqdn string) (string, error) { return publicsuffix.EffectiveTLDPlusOne(fqdn) }
