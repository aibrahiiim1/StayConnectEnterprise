package iamv2

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
	ErrACExpired       Code = "auth_context_expired"
	ErrACConsumed      Code = "auth_context_consumed"
	ErrACMismatch      Code = "auth_context_mismatch"
	ErrACNotFound      Code = "auth_context_not_found"
	ErrCapacity        Code = "capacity"
	ErrConflict        Code = "conflict"
	// ErrBusy: a check could not be run now (the password-hashing slots stayed taken). Nothing was decided about the
	// credential, so it is neither a wrong password nor counted as one; the caller answers "busy, try again" (AD-35).
	ErrBusy Code = "busy"
)

// Msg must never contain secrets, codes, OTPs or PII.
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

func CodeOf(err error) Code {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return ""
}
