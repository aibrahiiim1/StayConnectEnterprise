package iamv2

// A Client is reached only through a verified factor: one primary (email, phone or the provider's issuer/sub)
// plus secondaries the same proof established. Resolution is primary, then secondaries, then a new Client;
// two existing Clients are never merged automatically, the primary wins and the conflict is recorded.

import (
	"strings"
)

type FactorClaim struct {
	Type   string            // EMAIL | PHONE | SOCIAL_SUBJECT
	Issuer string            // provider name for SOCIAL_SUBJECT; "" otherwise
	Value  string            // normalised: lower-cased email, E.164 phone, the provider's sub
	Attrs  map[string]string // verified claims about this factor; never from the browser
	// LookupOnly marks a factor that may find a Client but is never inserted (legacy SOCIAL_SUBJECT rows keyed by email).
	LookupOnly bool
}

type PrincipalResolution struct {
	PrincipalID string
	Created     bool     // a new Client was created
	Linked      []string // factors ("TYPE:issuer") newly attached to an existing Client
	Conflicts   []string // secondary factors that belong to ANOTHER Client and were left there
}

// trustedEmailIssuers are the providers whose verified-email claim may link a social subject to an email-keyed
// Client. It is code, not configuration. Microsoft is believed only when internal/social/microsoft.go reports
// verified, never a bare email claim; Facebook asserts no verification and is keyed by its app-scoped sub only.
var trustedEmailIssuers = map[string]bool{"google": true, "apple": true, "microsoft": true}

func IssuerVerifiesEmail(provider string) bool {
	return trustedEmailIssuers[strings.ToLower(strings.TrimSpace(provider))]
}

// Assurance of an email or phone factor, kept in the identity row's attrs. "staff_assisted" (a code a staff
// member read out) satisfies no email-domain group and earns no remembered device; the client's own later
// proof overwrites the marker (attrs merge by key).
const (
	AssuranceKey           = "assurance"
	AssuranceVerified      = "verified"
	AssuranceStaffAssisted = "staff_assisted"
)

// NormalizeFactorValue is the normalisation the unique index relies on.
func NormalizeFactorValue(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// SocialFactors returns the (issuer, sub) primary plus, for a verified email, a secondary EMAIL factor (trusted
// issuers only) and a lookup-only legacy claim so older email-keyed Clients are found, not duplicated.
func SocialFactors(provider, sub, email string, emailVerified bool, claims map[string]string) (FactorClaim, []FactorClaim) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	attrs := map[string]string{"source": provider}
	for _, k := range []string{"hd", "tid", "oid"} {
		if v := strings.TrimSpace(claims[k]); v != "" {
			attrs[k] = v
		}
	}
	email = NormalizeFactorValue(email)
	if email != "" && emailVerified {
		attrs["email"] = email
	}
	primary := FactorClaim{Type: "SOCIAL_SUBJECT", Issuer: provider, Value: strings.TrimSpace(sub), Attrs: attrs}
	var secondary []FactorClaim
	if email != "" && emailVerified {
		// Legacy rows keyed the subject by the email: find only, never insert.
		secondary = append(secondary, FactorClaim{Type: "SOCIAL_SUBJECT", Issuer: provider, Value: email, LookupOnly: true})
		if IssuerVerifiesEmail(provider) {
			secondary = append(secondary, FactorClaim{Type: "EMAIL", Value: email, Attrs: map[string]string{"source": provider, AssuranceKey: AssuranceVerified}})
		}
	}
	return primary, secondary
}

func OTPFactor(factorType, value string) FactorClaim {
	return FactorClaim{Type: strings.ToUpper(strings.TrimSpace(factorType)), Value: NormalizeFactorValue(value),
		Attrs: map[string]string{"source": "otp", AssuranceKey: AssuranceVerified}}
}

// Key is the identity tuple without the value, safe for evidence and logs.
func (f FactorClaim) Key() string {
	if f.Issuer == "" {
		return f.Type
	}
	return f.Type + ":" + f.Issuer
}

// MaskIdentity renders a factor for a guest page without disclosing it (the email domain is kept).
func MaskIdentity(f FactorClaim) string {
	switch f.Type {
	case "EMAIL":
		at := strings.IndexByte(f.Value, '@')
		if at <= 0 {
			return "•••"
		}
		return f.Value[:1] + "•••" + f.Value[at:]
	case "PHONE":
		v := f.Value
		if len(v) <= 4 {
			return "•••"
		}
		return v[:3] + "•••" + v[len(v)-4:]
	case "SOCIAL_SUBJECT":
		if e := f.Attrs["email"]; e != "" {
			return MaskIdentity(FactorClaim{Type: "EMAIL", Value: e})
		}
		return providerLabel(f.Issuer) + " account"
	}
	return "•••"
}

func providerLabel(p string) string {
	switch strings.ToLower(p) {
	case "google":
		return "Google"
	case "apple":
		return "Apple"
	case "microsoft":
		return "Microsoft"
	case "facebook":
		return "Facebook"
	}
	return "Provider"
}
