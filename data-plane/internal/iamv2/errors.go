package iamv2

// Code is a deterministic, non-sensitive error classification.
type Code string

const (
	ErrConfig          Code = "config"
	ErrInvalidInput    Code = "invalid_input"
	ErrInvalidCred     Code = "invalid_credential"
	ErrExpired         Code = "expired"
	ErrLocked          Code = "locked"
	ErrDisabledAccount Code = "account_disabled"
	ErrNotRedeemable   Code = "not_redeemable"
	ErrSubjectResolve  Code = "subject_resolve"
	ErrRepo            Code = "repository"
	ErrSocialStub      Code = "social_stub_refused"
)

// Error is a deterministic typed error. Msg must never contain secrets, codes, OTPs or PII.
type Error struct {
	Code Code
	Msg  string
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return "iamv2: " + string(e.Code)
	}
	return "iamv2: " + string(e.Code) + ": " + e.Msg
}

// CodeOf returns the classification of an error, or "" if it is not an *Error.
func CodeOf(err error) Code {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return ""
}
