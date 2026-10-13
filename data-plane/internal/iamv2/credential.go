package iamv2

import "strings"

// HOW A CLIENT PROVED THEMSELVES, in the words an operator needs.
//
// The auth context's METHOD names the authority that decided (OTP, SOCIAL, VOUCHER, ACCOUNT, PMS, OPEN). It
// does not say whether the code came by email, text message or WhatsApp, nor which identity provider was
// used -- and "Signed in with" on the Admin Console's sessions screen is exactly that question. The CREDENTIAL
// records it: one short token, written on the auth context when it is minted and carried onto the session
// opened from it (sessions.credential_method). It is a label, never a secret and never an identity.
const (
	CredentialEmailOTP     = "EMAIL_OTP"
	CredentialSMSOTP       = "SMS_OTP"
	CredentialWhatsAppOTP  = "WHATSAPP_OTP"
	CredentialVoucher      = "VOUCHER"
	CredentialAccount      = "ACCOUNT"
	CredentialRoom         = "PMS"
	CredentialOpen         = "OPEN"
	credentialSocialPrefix = "SOCIAL_"
)

// CredentialForOTP maps a verified challenge's channel ("email" | "sms" | "whatsapp") onto its credential.
func CredentialForOTP(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "email":
		return CredentialEmailOTP
	case "sms":
		return CredentialSMSOTP
	case "whatsapp":
		return CredentialWhatsAppOTP
	}
	return ""
}

// CredentialAssistedSuffix marks a sign-in completed with a code a staff member read to the client (contract
// §6.7): EMAIL_OTP_ASSISTED, SMS_OTP_ASSISTED, WHATSAPP_OTP_ASSISTED. The Admin Console says "Code shown by staff".
const CredentialAssistedSuffix = "_ASSISTED"

// AssistedCredential returns the staff-assisted form of an OTP credential.
func AssistedCredential(c string) string {
	if c == "" || strings.HasSuffix(c, CredentialAssistedSuffix) {
		return c
	}
	return NormalizeCredential(c + CredentialAssistedSuffix)
}

// IsAssistedCredential reports a credential minted from a staff-shown code.
func IsAssistedCredential(c string) bool { return strings.HasSuffix(c, CredentialAssistedSuffix) }

// CredentialForSocial names an identity provider: SOCIAL_GOOGLE, SOCIAL_APPLE, SOCIAL_MICROSOFT, SOCIAL_FACEBOOK.
func CredentialForSocial(provider string) string {
	p := NormalizeCredential(provider)
	if p == "" {
		return ""
	}
	return credentialSocialPrefix + p
}

// NormalizeCredential upper-cases and bounds a credential label: letters, digits and underscores only, at
// most 32 characters. Anything else is dropped rather than stored, so a label can never carry a value.
func NormalizeCredential(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '.':
			b.WriteRune('_')
		}
		if b.Len() >= 32 {
			break
		}
	}
	return b.String()
}
