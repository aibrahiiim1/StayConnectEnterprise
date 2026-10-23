package iamv2

import "strings"

// A credential is the short label for how a client proved themselves (e.g. email vs SMS code, which identity
// provider), written on the auth context and carried onto sessions.credential_method. It is never a secret
// or an identity.
const (
	CredentialEmailOTP    = "EMAIL_OTP"
	CredentialSMSOTP      = "SMS_OTP"
	CredentialWhatsAppOTP = "WHATSAPP_OTP"
	CredentialVoucher     = "VOUCHER"
	CredentialAccount     = "ACCOUNT"
	// CredentialEmailAccount is a self-registered account, distinct from a staff-created ACCOUNT; both are the ACCOUNT subject.
	CredentialEmailAccount = "EMAIL_ACCOUNT"
	CredentialRoom         = "PMS"
	CredentialOpen         = "OPEN"
	credentialSocialPrefix = "SOCIAL_"
)

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

// CredentialAssistedSuffix marks a sign-in completed with a code a staff member read to the client.
const CredentialAssistedSuffix = "_ASSISTED"

func AssistedCredential(c string) string {
	if c == "" || strings.HasSuffix(c, CredentialAssistedSuffix) {
		return c
	}
	return NormalizeCredential(c + CredentialAssistedSuffix)
}

func IsAssistedCredential(c string) bool { return strings.HasSuffix(c, CredentialAssistedSuffix) }

func CredentialForSocial(provider string) string {
	p := NormalizeCredential(provider)
	if p == "" {
		return ""
	}
	return credentialSocialPrefix + p
}

// NormalizeCredential keeps only letters, digits and underscores (max 32), so a label can never carry a value.
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
