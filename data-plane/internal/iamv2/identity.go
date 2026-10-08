package iamv2

// ONE CLIENT, MANY VERIFIED FACTORS.
//
// Contract: docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §2. A Client (guest principal) is
// reached only through a verified factor. A sign-in proves one PRIMARY factor -- the email a code was sent to,
// the phone, or the identity provider's (issuer, sub) -- and may carry SECONDARY factors the same proof
// established: a trusted issuer's email_verified claim proves control of that mailbox exactly as a code would.
//
// Resolution is deterministic: primary first, then secondaries, then a new Client. Two existing Clients are
// never merged automatically; the primary factor wins and the conflict is recorded.

import (
	"strings"
)

// FactorClaim is one verified identity factor as a sign-in established it.
type FactorClaim struct {
	Type   string            // EMAIL | PHONE | SOCIAL_SUBJECT
	Issuer string            // provider name for SOCIAL_SUBJECT; "" otherwise
	Value  string            // normalised: lower-cased email, E.164 phone, the provider's sub
	Attrs  map[string]string // verified claims about this factor (hd, tid, email, source); never from the browser
	// LookupOnly marks a factor that may FIND a Client but is never inserted: the rows the previous social
	// code wrote (SOCIAL_SUBJECT keyed by email) are honoured through it without being re-created.
	LookupOnly bool
}

// PrincipalResolution is the outcome of ResolvePrincipalByFactors.
type PrincipalResolution struct {
	PrincipalID string
	Created     bool     // a new Client was created
	Linked      []string // factors ("TYPE:issuer") newly attached to an existing Client
	Conflicts   []string // secondary factors that belong to ANOTHER Client and were left there
}

// trustedEmailIssuers are the identity providers whose "verified email" claim is believed as proof of the
// mailbox and may therefore LINK a social subject to an email-keyed Client. It is code, not configuration:
// the one place that decides what an issuer's word is worth.
//
//	google    email_verified is an explicit claim on the id_token.
//	apple     Apple only returns an email it verified (a private-relay address is still Apple's own).
//	microsoft believed only when internal/social/microsoft.go's own rules report verified (work accounts
//	          with xms_edov / personal accounts), never a bare email claim.
//	facebook  asserts no verification; NOT trusted. A Facebook Client is keyed by its app-scoped sub only.
var trustedEmailIssuers = map[string]bool{"google": true, "apple": true, "microsoft": true}

// IssuerVerifiesEmail reports whether a provider's verified-email assertion links identities.
func IssuerVerifiesEmail(provider string) bool {
	return trustedEmailIssuers[strings.ToLower(strings.TrimSpace(provider))]
}

// NormalizeFactorValue applies the one normalisation the unique index relies on.
func NormalizeFactorValue(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// SocialFactors builds the factor set of a verified social sign-in: the (issuer, sub) subject as the primary,
// and -- for a trusted issuer that verified an email -- that email as a secondary EMAIL factor, plus a
// lookup-only legacy claim so a Client created by the previous code (which keyed the subject by email) is
// found rather than duplicated.
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
		// Honour rows written before 0107 (subject keyed by the email) -- find only, never insert.
		secondary = append(secondary, FactorClaim{Type: "SOCIAL_SUBJECT", Issuer: provider, Value: email, LookupOnly: true})
		if IssuerVerifiesEmail(provider) {
			secondary = append(secondary, FactorClaim{Type: "EMAIL", Value: email, Attrs: map[string]string{"source": provider}})
		}
	}
	return primary, secondary
}

// OTPFactor builds the factor of a code-verified email or phone.
func OTPFactor(factorType, value string) FactorClaim {
	return FactorClaim{Type: strings.ToUpper(strings.TrimSpace(factorType)), Value: NormalizeFactorValue(value),
		Attrs: map[string]string{"source": "otp"}}
}

// Key is the factor's identity tuple without the value, for evidence and logs ("EMAIL", "SOCIAL_SUBJECT:google").
func (f FactorClaim) Key() string {
	if f.Issuer == "" {
		return f.Type
	}
	return f.Type + ":" + f.Issuer
}

// MaskIdentity renders a factor for a guest page without disclosing it: a•••@example.com, +20•••1234,
// "Google account". The domain of an email is kept because it is what tells a Client which identity this is.
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
