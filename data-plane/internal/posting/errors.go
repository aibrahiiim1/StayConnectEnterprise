package posting

import "errors"

// Code is a deterministic, non-sensitive classification for a financial refusal.
//
// Every code below names a reason a posting was NOT executed. That is the point: an operator has to be able
// to read the refusal and know which onboarding step, which pinned object or which review decision is
// missing, without anyone having to reconstruct it from a stack trace. None of these values ever carries an
// amount, a guest identifier, a room number or a secret.
type Code string

const (
	ErrConfig Code = "config"
	ErrRepo   Code = "repository"

	// ---- fail-closed creation gate (all of these happen BEFORE any side effect) ----
	ErrFolioStrategyUnset  Code = "folio_strategy_unset"
	ErrRNMissing           Code = "rn_missing"
	ErrGNumberMissing      Code = "g_number_missing"
	ErrRNNotWireSafe       Code = "rn_not_wire_safe"
	ErrGNumberNotWireSafe  Code = "g_number_not_wire_safe"
	ErrStayNotInHouse      Code = "stay_not_in_house"
	ErrPostingNotAllowed   Code = "posting_not_allowed"
	ErrEvidenceStale       Code = "evidence_stale"
	ErrEvidenceOutOfScope  Code = "evidence_out_of_scope"
	ErrInterfaceNoCurrency Code = "interface_currency_not_onboarded"
	ErrCurrencyMismatch    Code = "currency_mismatch"
	ErrExponentMismatch    Code = "currency_exponent_mismatch"
	ErrAmountInvalid       Code = "amount_invalid"
	ErrInterfaceInactive   Code = "interface_not_active"

	// ---- execution / protocol ----
	ErrDarkNoEgress     Code = "dark_no_egress"
	ErrAlreadyInFlight  Code = "already_in_flight"
	ErrRetryNotAuthed   Code = "retry_not_authorized"
	ErrPACorrelation    Code = "pa_correlation_failed"
	ErrPAStatusUnknown  Code = "pa_status_not_in_catalog"
	ErrPAAmbiguous      Code = "pa_ambiguous"
	ErrUnknownTerminal  Code = "unknown_requires_manual_review"
	ErrReviewConflict   Code = "review_conflict"
	ErrReviewStale      Code = "review_version_stale"
	ErrWireFieldInvalid Code = "wire_field_invalid"
)

// Error is a deterministic typed error. Msg must never contain secrets, card data, guest PII or amounts.
type Error struct {
	Code Code
	Msg  string
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return "posting: " + string(e.Code)
	}
	return "posting: " + string(e.Code) + ": " + e.Msg
}

// CodeOf returns the classification of an error, or "" if it is not an *Error.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func fail(c Code, msg string) error { return &Error{Code: c, Msg: msg} }
